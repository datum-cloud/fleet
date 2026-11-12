#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_DIR="${ROOT_DIR}/.e2e"
TEST_DIR="${ROOT_DIR}/test/e2e/machine-management"
MGMT_KUBECONFIG="${E2E_DIR}/management.kubeconfig"
LOCATION_KUBECONFIG="${E2E_DIR}/location.kubeconfig"
PATH="${E2E_DIR}/bin:${PATH}"
export PATH

for file in "${MGMT_KUBECONFIG}" "${LOCATION_KUBECONFIG}" "${E2E_DIR}/management-ca.crt"; do
  [[ -f "${file}" ]] || { echo "missing ${file}; run task e2e:up" >&2; exit 1; }
done
# Each run must prove a new boot rather than accepting evidence left by an
# earlier invocation against the same idempotent environment.
rm -f "${E2E_DIR}/virtual-bmc/evidence/pxe-callbacks.jsonl" \
  "${E2E_DIR}/virtual-bmc/evidence/pxe-operation-correlation.json" \
  "${E2E_DIR}/virtual-bmc/evidence/allocated-console.log"

proxy_pid=
second_proxy_pid=
management_paused=false
location_paused=false
cleanup() {
  if [[ -n "${proxy_pid}" ]]; then kill -TERM "${proxy_pid}" 2>/dev/null || true; wait "${proxy_pid}" 2>/dev/null || true; fi
  if [[ -n "${second_proxy_pid}" ]]; then kill -TERM "${second_proxy_pid}" 2>/dev/null || true; wait "${second_proxy_pid}" 2>/dev/null || true; fi
  if [[ "${management_paused}" == true ]]; then docker unpause fleet-management-e2e-control-plane >/dev/null 2>&1 || true; fi
  if [[ "${location_paused}" == true ]]; then docker unpause fleet-location-e2e-control-plane >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
wait_for() {
  local description=$1 command=$2
  for _ in $(seq 1 90); do eval "${command}" && return 0; sleep 1; done
  fail "timed out waiting for ${description}"
}
run_timeout() {
  local seconds=$1
  shift
  if command -v timeout >/dev/null; then timeout "${seconds}" "$@"; return; fi
  if command -v gtimeout >/dev/null; then gtimeout "${seconds}" "$@"; return; fi
  perl -e '$seconds = shift; alarm $seconds; exec @ARGV' "${seconds}" "$@"
}
file_mode() {
  if stat -c %a "$1" >/dev/null 2>&1; then stat -c %a "$1"; else stat -f %Lp "$1"; fi
}

api_server=$(kubectl config view --kubeconfig "${MGMT_KUBECONFIG}" --raw -o jsonpath='{.clusters[0].cluster.server}')

# A claim reader cannot create a management session. This invokes datumctl's
# actual plugin discovery/dispatch and credential-helper path.
set +e
HOME="${E2E_DIR}/home-viewer" datumctl fleet machine-management proxy db-01 -- \
  --api-server "${api_server}" --api-ca-file "${E2E_DIR}/management-ca.crt" \
  --ready-file "${E2E_DIR}/viewer-ready.json" >"${E2E_DIR}/viewer.log" 2>&1
viewer_status=$?
set -e
[[ ${viewer_status} -eq 11 ]] || fail "claim-only principal exit=${viewer_status}, expected permission exit 11"
[[ ! -e "${E2E_DIR}/viewer-ready.json" ]] || fail "unauthorized principal received a local endpoint"

start_proxy() {
  local ttl=${1:-15m}
  rm -f "${E2E_DIR}/ready.json" "${E2E_DIR}/credentials.json" "${E2E_DIR}/ca.pem"
  DATUM_TOKEN=must-not-be-used HOME="${E2E_DIR}/home-authorized" datumctl fleet machine-management proxy db-01 -- \
    --api-server "${api_server}" --api-ca-file "${E2E_DIR}/management-ca.crt" \
    --listen 127.0.0.1:9443 --ttl "${ttl}" \
    --ready-file "${E2E_DIR}/ready.json" --credential-file "${E2E_DIR}/credentials.json" \
    --ca-file "${E2E_DIR}/ca.pem" >"${E2E_DIR}/proxy.log" 2>&1 &
  proxy_pid=$!
  wait_for "datumctl fleet proxy readiness" "jq -e '.ready == true' '${E2E_DIR}/ready.json' >/dev/null 2>&1"
  endpoint=$(jq -r .endpoint "${E2E_DIR}/credentials.json")
  username=$(jq -r .username "${E2E_DIR}/credentials.json")
  password=$(jq -r .password "${E2E_DIR}/credentials.json")
  root=${endpoint%/redfish/v1/}
}

stop_proxy() {
  kill -TERM "${proxy_pid}" 2>/dev/null || true
  wait "${proxy_pid}" 2>/dev/null || true
  proxy_pid=
  wait_for "session deletion" "! kubectl --kubeconfig '${MGMT_KUBECONFIG}' -n default get machinemanagementsessions.fleet.datumapis.com -o name 2>/dev/null | grep -q ."
}

# Nested x86 QEMU on non-x86 developer hosts can take tens of seconds to apply
# a real libvirt boot mutation. Keep the deadline bounded while allowing the
# required software-emulation fallback to complete without manufacturing a
# client-side lost-response outcome.
rf() { curl --silent --show-error --max-time 90 --cacert "${E2E_DIR}/ca.pem" --user "${username}:${password}" "$@"; }
rf_status() { curl --silent --max-time 90 --output /dev/null --write-out '%{http_code}' --cacert "${E2E_DIR}/ca.pem" --user "${username}:${password}" "$@"; }

start_proxy
[[ $(file_mode "${E2E_DIR}/credentials.json") == 600 ]] || fail "credential file is not 0600"
[[ $(file_mode "${E2E_DIR}/ca.pem") == 600 ]] || fail "CA file is not 0600"
if curl --fail --silent --connect-timeout 2 http://172.30.0.10:8000/redfish/v1/ >/dev/null 2>&1; then
  fail "customer test process reached the private sushy endpoint"
fi
session_name=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machinemanagementsessions -o jsonpath='{.items[0].metadata.name}')
if kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default patch machinemanagementsession "${session_name}" --type=merge -p '{"spec":{"ttl":"10m"}}' >/dev/null 2>&1; then
  fail "immutable public session spec was mutable"
fi

# Use DMTF's generic Python Redfish client, not a project-specific client. A
# remote Docker engine cannot share the macOS loopback namespace, so the
# unqualified Darwin development path uses the same pinned client from a local
# temporary virtual environment. Native Linux and CI retain the container path.
if [[ $(uname -s) == Darwin ]]; then
  validator_bin=${FLEET_VALIDATOR_BIN:-/private/tmp/fleet-validator-venv/bin}
  FLEET_CREDENTIAL_FILE="${E2E_DIR}/credentials.json" FLEET_CA_BUNDLE="${E2E_DIR}/ca.pem" \
    "${validator_bin}/python" "${TEST_DIR}/generic-client.py"
else
  docker run --rm --network host \
    -e FLEET_CREDENTIAL_FILE=/state/credentials.json -e FLEET_CA_BUNDLE=/state/ca.pem \
    -v "${E2E_DIR}:/state:ro" -v "${TEST_DIR}:/work:ro" fleet-redfish-tools:e2e \
    "python /work/generic-client.py"
fi

# Session-local login credentials are revocable and are not BMC credentials.
login_headers="${E2E_DIR}/login.headers"
curl --silent --show-error --cacert "${E2E_DIR}/ca.pem" -D "${login_headers}" \
  -H 'Content-Type: application/json' -d "{\"UserName\":\"${username}\",\"Password\":\"${password}\"}" \
  "${root}/redfish/v1/SessionService/Sessions" >/dev/null
local_token=$(awk 'tolower($1)=="x-auth-token:" {gsub("\r", "", $2); print $2}' "${login_headers}")
[[ -n "${local_token}" ]] || fail "local Redfish login did not return a token"
curl --silent --show-error --cacert "${E2E_DIR}/ca.pem" -H "X-Auth-Token: ${local_token}" \
  -X DELETE "${root}/redfish/v1/SessionService/Sessions/local" -o /dev/null
[[ $(curl --silent --output /dev/null --write-out '%{http_code}' --cacert "${E2E_DIR}/ca.pem" -H "X-Auth-Token: ${local_token}" "${root}/redfish/v1/Systems") == 401 ]] || fail "deleted local Redfish token remained valid"

# The virtual tree is intentionally narrow and contains exactly one allocated system.
for path in AccountService Managers UpdateService Systems/unallocated Systems/allocated/Oem/Reset; do
  [[ $(rf_status "${root}/redfish/v1/${path}") == 404 ]] || fail "forbidden path ${path} was exposed"
done
[[ $(curl --silent --output /dev/null --write-out '%{http_code}' --cacert "${E2E_DIR}/ca.pem" --user "${username}:${password}" -H 'Host: 172.30.0.10' "${root}/redfish/v1/Systems") == 400 ]] || fail "Host routing was accepted"
[[ $(curl --silent --output /dev/null --write-out '%{http_code}' --cacert "${E2E_DIR}/ca.pem" --user "${username}:${password}" --request-target 'http://172.30.0.10/redfish/v1/Systems' "${root}/redfish/v1/Systems") == 400 ]] || fail "absolute request target was accepted"
[[ $(rf_status -X PATCH -H 'Content-Type: application/json' -d '{"AssetTag":"customer-controlled"}' "${root}/redfish/v1/Systems/allocated") == 400 ]] || fail "unsupported PATCH was accepted"
[[ $(rf_status -X POST -H 'Content-Type: application/json' -d '{"ResetType":"Nmi"}' "${root}/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset") == 400 ]] || fail "unsupported reset was accepted"
[[ $(docker exec fleet-e2e-virtual-bmc virsh domstate unallocated | tr -d '\r') == "shut off" ]] || fail "unallocated domain was affected"

# A second connector cannot consume the already-consumed rendezvous route.
route=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machinemanagementsessions -o jsonpath='{.items[0].status.connection.routeID}')
replay=$(run_timeout 3 bash -c "exec 9<>/dev/tcp/127.0.0.1/30443; printf 'CLIENT %s\\n' '${route}' >&9; IFS= read -r line <&9; printf '%s' \"\${line}\"")
[[ "${replay}" == ERROR* ]] || fail "relay route replay was not rejected: ${replay}"

# Per-claim admission limit remains distinct from claim read permission.
set +e
run_timeout 20 env HOME="${E2E_DIR}/home-authorized" datumctl fleet machine-management proxy db-01 -- \
  --api-server "${api_server}" --api-ca-file "${E2E_DIR}/management-ca.crt" >"${E2E_DIR}/second-session.log" 2>&1
second_status=$?
set -e
[[ ${second_status} -ne 0 ]] || fail "second concurrent claim session was admitted"
grep -q SessionLimitExceeded "${E2E_DIR}/second-session.log" || fail "session limit rejection was not actionable"

# The E2E controller uses a test-only project limit of two. A second distinct,
# pre-bound claim is admitted; a third is rejected by the real plugin workflow.
for claim in db-02 db-03; do
  kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f - <<EOF
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineClaim
metadata: {name: ${claim}, namespace: default}
spec: {machineClassRef: {name: synthetic-libvirt}}
EOF
done
db02_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machineclaim db-02 -o jsonpath='{.metadata.uid}')
db03_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machineclaim db-03 -o jsonpath='{.metadata.uid}')
for pair in "db-02:${db02_uid}:77777777-7777-4777-8777-777777777777:unallocated" "db-03:${db03_uid}:88888888-8888-4888-8888-888888888888:rejected-before-routing"; do
  IFS=: read -r claim claim_uid machine_uid bmc_id <<<"${pair}"
  kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f - <<EOF
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineAllocationBinding
metadata:
  name: ${claim}-allocation
  namespace: fleet-system
  labels: {fleet.datumapis.com/claim-uid: "${claim_uid}"}
spec:
  consumerProjectUID: 11111111-1111-4111-8111-111111111111
  providerProjectUID: 22222222-2222-4222-8222-222222222222
  claimNamespace: default
  claimName: ${claim}
  claimUID: "${claim_uid}"
  machineName: ${claim}-machine
  machineUID: "${machine_uid}"
  bindingID: ${claim}-binding
  bindingEpoch: 1
  locationUID: 44444444-4444-4444-8444-444444444444
  locationRevision: location-prototype-r1
  bareMetalMachineUID: "${machine_uid}"
  accessProfileRevision: fleet-provisioning-v1.0.0
  adapterRoute: sushy-libvirt-v1
  bmcSystemID: ${bmc_id}
  active: true
EOF
done
HOME="${E2E_DIR}/home-authorized" datumctl fleet machine-management proxy db-02 -- \
  --api-server "${api_server}" --api-ca-file "${E2E_DIR}/management-ca.crt" \
  --listen 127.0.0.1:9444 --ready-file "${E2E_DIR}/ready-2.json" \
  --credential-file "${E2E_DIR}/credentials-2.json" --ca-file "${E2E_DIR}/ca-2.pem" \
  >"${E2E_DIR}/proxy-2.log" 2>&1 &
second_proxy_pid=$!
wait_for "second project session" "jq -e '.ready == true' '${E2E_DIR}/ready-2.json' >/dev/null 2>&1"
set +e
run_timeout 20 env HOME="${E2E_DIR}/home-authorized" datumctl fleet machine-management proxy db-03 -- \
  --api-server "${api_server}" --api-ca-file "${E2E_DIR}/management-ca.crt" >"${E2E_DIR}/project-limit.log" 2>&1
project_status=$?
set -e
[[ ${project_status} -ne 0 ]] || fail "third project session was admitted over the limit"
grep -q SessionLimitExceeded "${E2E_DIR}/project-limit.log" || fail "project limit rejection was not actionable"
kill -TERM "${second_proxy_pid}"; wait "${second_proxy_pid}" || true; second_proxy_pid=

# Real sushy -> libvirt PXE effect, correlated by the allocated VM MAC callback.
pxe_body='{"Boot":{"BootSourceOverrideEnabled":"Once","BootSourceOverrideTarget":"Pxe"}}'
pxe_task=$(rf -X PATCH -H 'Content-Type: application/json' -d "${pxe_body}" "${root}/redfish/v1/Systems/allocated")
[[ $(jq -r .TaskState <<<"${pxe_task}") == Completed ]] || fail "PXE boot mutation did not complete"
power_task=$(rf -X POST -H 'Content-Type: application/json' -d '{"ResetType":"On"}' "${root}/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset")
task_uri=$(jq -r '.["@odata.id"]' <<<"${power_task}")
[[ $(rf "${root}${task_uri}" | jq -r .TaskState) == Completed ]] || fail "power task did not complete"
# A complete Ubuntu kernel boot under nested software QEMU can exceed the
# ordinary control-plane wait. Keep this proof bounded, but allow four minutes
# for the real VM to load the initramfs, configure DHCP, and call back.
for _ in $(seq 1 240); do
  grep -q '/pxe/52-54-00-aa-00-01' "${E2E_DIR}/virtual-bmc/evidence/pxe-callbacks.jsonl" 2>/dev/null && break
  sleep 1
done
grep -q '/pxe/52-54-00-aa-00-01' "${E2E_DIR}/virtual-bmc/evidence/pxe-callbacks.jsonl" 2>/dev/null || fail "timed out waiting for PXE callback for allocated MAC"
wait_for "PXE fixture poweroff in libvirt" "[[ \$(docker exec fleet-e2e-virtual-bmc virsh domstate allocated | tr -d '\\r') == 'shut off' ]]"
! grep -q '/pxe/52-54-00-aa-00-02' "${E2E_DIR}/virtual-bmc/evidence/pxe-callbacks.jsonl" || fail "unallocated VM produced a PXE callback"
jq -n --arg operationID "$(jq -r .Id <<<"${power_task}")" --arg mac 52:54:00:aa:00:01 \
  --argjson callback "$(grep '/pxe/52-54-00-aa-00-01' "${E2E_DIR}/virtual-bmc/evidence/pxe-callbacks.jsonl" | tail -1)" \
  '{operationID:$operationID,vmMAC:$mac,callback:$callback}' >"${E2E_DIR}/virtual-bmc/evidence/pxe-operation-correlation.json"

hdd_body='{"Boot":{"BootSourceOverrideEnabled":"Once","BootSourceOverrideTarget":"Hdd"}}'
rf -X PATCH -H 'Content-Type: application/json' -d "${hdd_body}" "${root}/redfish/v1/Systems/allocated" >/dev/null
[[ $(rf "${root}/redfish/v1/Systems/allocated" | jq -r .Boot.BootSourceOverrideTarget) == Hdd ]] || fail "local-disk target was not restored"
domain_xml=$(docker exec fleet-e2e-virtual-bmc virsh dumpxml allocated)
grep -A6 "<disk type='file' device='disk'>" <<<"${domain_xml}" | grep -q "<boot order='1'/>" || fail "libvirt did not retain the local-disk boot target"
if grep -A6 "<interface type='network'>" <<<"${domain_xml}" | grep -q '<boot order='; then
  fail "libvirt retained a network boot override after local-disk restoration"
fi

# Both pinned DMTF validators run against the live virtual service.
FLEET_CREDENTIAL_FILE="${E2E_DIR}/credentials.json" "${ROOT_DIR}/hack/e2e/validators.sh"

# The established data path survives loss of the central API, then fails closed
# after the bounded cached-authority deadline.
docker pause fleet-management-e2e-control-plane >/dev/null
management_paused=true
[[ $(rf_status "${root}/redfish/v1/Systems/allocated") == 200 ]] || fail "data path depended synchronously on the central API"
sleep 18
[[ $(rf_status "${root}/redfish/v1/Systems/allocated") == 503 ]] || fail "stale cached authority did not fail closed"
docker unpause fleet-management-e2e-control-plane >/dev/null
management_paused=false
wait_for "authority refresh" "[[ \$(rf_status '${root}/redfish/v1/Systems/allocated') == 200 ]]"

# Allocation release is a terminal fence for this admitted binding.
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system patch machineallocationbinding db-01-allocation --type=merge -p '{"spec":{"active":false}}'
wait_for "released stream closure" "[[ \$(rf_status '${root}/redfish/v1/Systems/allocated') != 200 ]]"
old_session_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machinemanagementsessions -o jsonpath='{.items[0].metadata.uid}')
old_claim_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machineclaim db-01 -o jsonpath='{.metadata.uid}')
stop_proxy

# Recreate the same claim name for the other real libvirt domain. The old
# admitted binding stays revoked and cannot inherit the new claim UID.
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default delete machineclaim db-01 --wait=true
kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f "${TEST_DIR}/seed/claim.yaml"
new_claim_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machineclaim db-01 -o jsonpath='{.metadata.uid}')
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system patch machineallocationbinding db-01-allocation --type=merge \
  -p "{\"metadata\":{\"labels\":{\"fleet.datumapis.com/claim-uid\":\"${new_claim_uid}\"}},\"spec\":{\"claimUID\":\"${new_claim_uid}\",\"machineName\":\"synthetic-reallocated\",\"machineUID\":\"66666666-6666-4666-8666-666666666666\",\"bindingID\":\"allocation-e2e-2\",\"bindingEpoch\":2,\"bareMetalMachineUID\":\"99999999-9999-4999-8999-999999999999\",\"bmcSystemID\":\"unallocated\",\"active\":true}}"
[[ "${new_claim_uid}" != "${old_claim_uid}" ]] || fail "recreated claim retained its old UID"
[[ $(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system get machinemanagementsessionbinding "${old_session_uid}" -o jsonpath='{.spec.claimUID}') == "${old_claim_uid}" ]] || fail "old session binding inherited the recreated claim"
old_binding_revoked=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system get machinemanagementsessionbinding "${old_session_uid}" -o jsonpath='{.metadata.annotations.fleet\.datumapis\.com/revoked-at}')
[[ -n "${old_binding_revoked}" ]] || fail "released old binding was not terminally revoked"

start_proxy
[[ $(rf "${root}/redfish/v1/Systems/allocated" | jq -r .Id) == allocated ]] || fail "virtual system identity was not stable"
# The adapter now targets the newly allocated physical domain; the old domain
# is never addressable through the customer's virtual ID.
rf -X POST -H 'Content-Type: application/json' -d '{"ResetType":"On"}' "${root}/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset" >/dev/null
wait_for "new allocation domain power-on" "[[ \$(docker exec fleet-e2e-virtual-bmc virsh domstate unallocated | tr -d '\\r') == running ]]"

# Fault-inject a lost BMC response. Retrying the identical admitted operation
# must recover its Unknown task without sending a second physical reset.
stop_proxy
wait_for "prior location projection teardown" \
  "! kubectl --kubeconfig '${LOCATION_KUBECONFIG}' -n machine-access-system get projectedmachinemanagementsessions.fleet.datumapis.com -o name 2>/dev/null | grep -q ."
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system patch deployment fleet-gateway --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--fault-drop-reset-response-once"}]'
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system rollout status deployment/fleet-gateway --timeout=120s
start_proxy
reset_path='/redfish/v1/Systems/unallocated/Actions/ComputerSystem.Reset'
reset_before=$(docker logs fleet-e2e-virtual-bmc 2>&1 | grep -c "POST ${reset_path}" || true)
lost_body='{"ResetType":"ForceRestart"}'
lost_first=$(rf -X POST -H 'Content-Type: application/json' -d "${lost_body}" "${root}/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset")
lost_second=$(rf -X POST -H 'Content-Type: application/json' -d "${lost_body}" "${root}/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset")
[[ $(jq -r .TaskState <<<"${lost_first}") == Exception && $(jq -r .TaskStatus <<<"${lost_first}") == Warning ]] || fail "lost response was not reported Unknown"
[[ $(jq -r .Id <<<"${lost_first}") == $(jq -r .Id <<<"${lost_second}") ]] || fail "retry created a second operation"
reset_after=$(docker logs fleet-e2e-virtual-bmc 2>&1 | grep -c "POST ${reset_path}" || true)
[[ $((reset_after - reset_before)) -eq 1 ]] || fail "lost-response retry sent $((reset_after - reset_before)) physical resets"

# The gateway schedules the admitted expiry and closes the stream rather than
# treating a still-live client or relay connection as authority.
stop_proxy
start_proxy 1m
sleep 65
[[ $(rf_status "${root}/redfish/v1/Systems/allocated") != 200 ]] || fail "expired session retained Redfish access"
stop_proxy
start_proxy

# Stop only the private sushy route. The customer receives a bounded error that
# does not disclose the private endpoint or credential.
gateway_pod=$(kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system get pod -l app.kubernetes.io/name=fleet-gateway -o jsonpath='{.items[0].metadata.name}')
docker exec fleet-e2e-virtual-bmc iptables -I INPUT 1 -p tcp --dport 8000 -s 172.30.0.20 -j REJECT
bmc_error=$(rf "${root}/redfish/v1/Systems/allocated")
[[ $(jq -r .error.code <<<"${bmc_error}") == Fleet.1.0.BMCUnavailable ]] || fail "BMC failure was not actionable"
! grep -Eq '172\.30\.0\.10|fleet-gateway|synthetic-e2e-only' <<<"${bmc_error}" || fail "BMC failure disclosed private data"
docker exec fleet-e2e-virtual-bmc iptables -D INPUT 1

# If the location cannot acknowledge explicit deletion, the public object may
# finish deleting only with a protected Unknown teardown outcome.
unknown_session_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machinemanagementsessions -o jsonpath='{.items[0].metadata.uid}')
docker pause fleet-location-e2e-control-plane >/dev/null
location_paused=true
stop_proxy
cleanup_status=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system get machinemanagementsessionbinding "${unknown_session_uid}" -o json | jq -r '.status.conditions[] | select(.type=="CleanupConfirmed") | .status + ":" + .reason')
[[ "${cleanup_status}" == "Unknown:LocationUnconfirmed" ]] || fail "unconfirmed teardown was not Unknown: ${cleanup_status}"
docker unpause fleet-location-e2e-control-plane >/dev/null
location_paused=false
[[ ! -e "${E2E_DIR}/credentials.json" && ! -e "${E2E_DIR}/ca.pem" ]] || fail "proxy secret files survived shutdown"
echo "machine-management end-to-end scenarios: PASS"
