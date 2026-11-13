#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_DIR="${ROOT_DIR}/.e2e"
TEST_DIR="${ROOT_DIR}/test/e2e/machine-management"
MGMT_KUBECONFIG="${E2E_DIR}/management.kubeconfig"
LOCATION_KUBECONFIG="${E2E_DIR}/location.kubeconfig"
KARMADA_KUBECONFIG="${E2E_DIR}/karmada.kubeconfig"
PATH="${E2E_DIR}/bin:${PATH}"
export PATH
source "${ROOT_DIR}/hack/e2e/common.sh"
rm -f "${E2E_DIR}/checkpoints.log" "${E2E_DIR}/viewer-ready.json" "${E2E_DIR}/ready-2.json"

for file in "${MGMT_KUBECONFIG}" "${LOCATION_KUBECONFIG}" "${KARMADA_KUBECONFIG}" "${E2E_DIR}/management-ca.crt"; do
  [[ -f "${file}" ]] || { echo "missing ${file}; run task e2e:up" >&2; exit 1; }
done
# Each run must prove a new boot rather than accepting evidence left by an
# earlier invocation against the same idempotent environment.
rm -f "${E2E_DIR}/virtual-bmc/evidence/pxe-callbacks.jsonl" \
  "${E2E_DIR}/virtual-bmc/evidence/pxe-operation-correlation.json" \
  "${E2E_DIR}/virtual-bmc/evidence/allocated-console.log"

# Docker may run in a VM whose bind mount is not mirrored to this client.
# Reset evidence at its writer as well as the local exported copy.
docker exec fleet-e2e-virtual-bmc rm -f /evidence/pxe-callbacks.jsonl /evidence/pxe-operation-correlation.json /evidence/allocated-console.log

proxy_pid=
second_proxy_pid=
management_paused=false
location_paused=false
bmc_blocked=false
cleanup() {
  local result=$?
  if [[ ${result} -ne 0 ]]; then run_timeout 90 "${ROOT_DIR}/hack/e2e/diagnostics.sh" || true; fi
  if [[ -n "${proxy_pid}" ]]; then kill -TERM "${proxy_pid}" 2>/dev/null || true; wait "${proxy_pid}" 2>/dev/null || true; fi
  if [[ -n "${second_proxy_pid}" ]]; then kill -TERM "${second_proxy_pid}" 2>/dev/null || true; wait "${second_proxy_pid}" 2>/dev/null || true; fi
  if [[ "${management_paused}" == true ]]; then docker unpause fleet-management-e2e-control-plane >/dev/null 2>&1 || true; fi
  if [[ "${location_paused}" == true ]]; then docker unpause fleet-location-e2e-control-plane >/dev/null 2>&1 || true; fi
  if [[ "${bmc_blocked}" == true ]]; then docker exec fleet-e2e-virtual-bmc iptables -D INPUT -p tcp --dport 8000 -s 172.30.0.20 -j REJECT >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
file_mode() {
  if stat -c %a "$1" >/dev/null 2>&1; then stat -c %a "$1"; else stat -f %Lp "$1"; fi
}

api_server=$(kubectl config view --kubeconfig "${MGMT_KUBECONFIG}" --raw -o jsonpath='{.clusters[0].cluster.server}')

# A Machine reader cannot create a management session. This invokes datumctl's
# actual plugin discovery/dispatch and credential-helper path.
set +e
HOME="${E2E_DIR}/home-viewer" datumctl fleet machine-management proxy db-01 -- \
  --api-server "${api_server}" --api-ca-file "${E2E_DIR}/management-ca.crt" \
  --ready-file "${E2E_DIR}/viewer-ready.json" >"${E2E_DIR}/viewer.log" 2>&1
viewer_status=$?
set -e
[[ ${viewer_status} -eq 11 ]] || fail "Machine-read-only principal exit=${viewer_status}, expected permission exit 11"
[[ ! -e "${E2E_DIR}/viewer-ready.json" ]] || fail "unauthorized principal received a local endpoint"

start_proxy() {
  local ttl=${1:-15m}
  local attempt
  attempt=$(date -u '+%Y%m%dT%H%M%SZ')
  [[ ! -f "${E2E_DIR}/proxy.log" ]] || cp "${E2E_DIR}/proxy.log" "${E2E_DIR}/proxy-${attempt}.log"
  rm -f "${E2E_DIR}/ready.json" "${E2E_DIR}/credentials.json" "${E2E_DIR}/ca.pem"
  DATUM_TOKEN=must-not-be-used HOME="${E2E_DIR}/home-authorized" datumctl fleet machine-management proxy db-01 -- \
    --api-server "${api_server}" --api-ca-file "${E2E_DIR}/management-ca.crt" \
    --listen 127.0.0.1:9443 --ttl "${ttl}" \
    --ready-file "${E2E_DIR}/ready.json" --credential-file "${E2E_DIR}/credentials.json" \
    --ca-file "${E2E_DIR}/ca.pem" >"${E2E_DIR}/proxy.log" 2>&1 &
  proxy_pid=$!
  wait_for "datumctl fleet proxy readiness" "jq -e '.ready == true' '${E2E_DIR}/ready.json' >/dev/null 2>&1" "${proxy_pid}"
  endpoint=$(jq -r .endpoint "${E2E_DIR}/credentials.json")
  username=$(jq -r .username "${E2E_DIR}/credentials.json")
  password=$(jq -r .password "${E2E_DIR}/credentials.json")
  root=${endpoint%/redfish/v1/}
}

stop_proxy() {
  kill -TERM "${proxy_pid}" 2>/dev/null || true
  wait "${proxy_pid}" 2>/dev/null || true
  proxy_pid=
  wait_for "session deletion" "sessions_absent '${MGMT_KUBECONFIG}' default machinemanagementsessions.metal.datumapis.com"
}

# Nested x86 QEMU on non-x86 developer hosts can take tens of seconds to apply
# a real libvirt boot mutation. Keep the deadline bounded while allowing the
# required software-emulation fallback to complete without manufacturing a
# client-side lost-response outcome.
rf() { curl --silent --show-error --max-time 90 --cacert "${E2E_DIR}/ca.pem" --user "${username}:${password}" "$@"; }
rf_status() { curl --silent --max-time 90 --output /dev/null --write-out '%{http_code}' --cacert "${E2E_DIR}/ca.pem" --user "${username}:${password}" "$@"; }

checkpoint "Machine reader denied session creation"
start_proxy
[[ $(file_mode "${E2E_DIR}/credentials.json") == 600 ]] || fail "credential file is not 0600"
[[ $(file_mode "${E2E_DIR}/ca.pem") == 600 ]] || fail "CA file is not 0600"
if curl --fail --silent --connect-timeout 2 http://172.30.0.10:8000/redfish/v1/ >/dev/null 2>&1; then
  fail "customer test process reached the private sushy endpoint"
fi
session_name=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machinemanagementsessions -o jsonpath='{.items[0].metadata.name}')
session_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machinemanagementsession "${session_name}" -o jsonpath='{.metadata.uid}')
binding_name=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system get machinemanagementsessionbindings -l "fleet.datumapis.com/session-uid=${session_uid}" -o jsonpath='{.items[0].metadata.name}')
# This is an actual Karmada Work proof, not a direct controller write to the
# member API: the selected cell owns the one Work containing this projection,
# and status has returned through the hub copy before the public session readied.
work_clusters=$(kubectl --kubeconfig "${KARMADA_KUBECONFIG}" get works.work.karmada.io -A -o json | jq -r --arg name "${binding_name}" '
  .items[] | select(any(.spec.workload.manifests[]?; .kind == "ProjectedMachineManagementSession" and .metadata.name == $name)) | .metadata.namespace' | sort -u)
[[ "${work_clusters}" == karmada-es-fleet-location-e2e ]] || fail "projection Work was not isolated to fleet-location-e2e: ${work_clusters}"
[[ $(kubectl --kubeconfig "${KARMADA_KUBECONFIG}" -n machine-access-system get projectedmachinemanagementsession "${binding_name}" -o jsonpath='{.metadata.labels.fleet\.datumapis\.com/member-cluster}') == fleet-location-e2e ]] || fail "hub projection lost its admitted member identity"
[[ -n $(kubectl --kubeconfig "${KARMADA_KUBECONFIG}" -n machine-access-system get projectedmachinemanagementsession "${binding_name}" -o jsonpath='{.status.connection.routeID}') ]] || fail "cell status was not reflected through Karmada"
if kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system get secret fleet-location-kubeconfig >/dev/null 2>&1; then
  fail "management cluster retained a direct location credential"
fi
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
checkpoint "generic DMTF client and Karmada projection"

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

# Per-Machine admission limit remains distinct from Machine read permission.
set +e
run_timeout 20 env HOME="${E2E_DIR}/home-authorized" datumctl fleet machine-management proxy db-01 -- \
  --api-server "${api_server}" --api-ca-file "${E2E_DIR}/management-ca.crt" >"${E2E_DIR}/second-session.log" 2>&1
second_status=$?
set -e
[[ ${second_status} -ne 0 ]] || fail "second concurrent Machine session was admitted"
grep -q SessionLimitExceeded "${E2E_DIR}/second-session.log" || fail "session limit rejection was not actionable"

# The E2E controller uses a test-only project limit of two. A second distinct,
# pre-bound Machine is admitted; a third is rejected by the real plugin workflow.
for machine in db-02 db-03; do
  kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f - <<EOF
apiVersion: metal.datumapis.com/v1alpha1
kind: Machine
metadata: {name: ${machine}, namespace: default}
spec:
  machineClassRef: {name: synthetic-libvirt}
  machineProfileRef: {name: synthetic-libvirt}
  locationRef: {name: location-e2e}
  powerState: "Off"
EOF
done
db02_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machine db-02 -o jsonpath='{.metadata.uid}')
db03_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machine db-03 -o jsonpath='{.metadata.uid}')
for pair in "db-02:${db02_uid}:db02-capacity:device-unallocated" "db-03:${db03_uid}:db03-capacity:device-rejected"; do
  IFS=: read -r machine machine_uid capacity device <<<"${pair}"
  kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f - <<EOF
apiVersion: metal.datumapis.com/v1alpha1
kind: MachineCapacity
metadata:
  name: ${capacity}
  namespace: fleet-provider
spec:
  deviceRef: {name: ${device}}
  allocationPolicy: Enabled
  cleaningPolicyRef: {name: synthetic-e2e}
  reclaimPolicy: Retain
---
apiVersion: metal.datumapis.com/v1alpha1
kind: MachineAllocation
metadata:
  name: ${machine}-allocation
  namespace: fleet-system
  annotations:
    fleet.datumapis.com/machine-uid: "${machine_uid}"
    fleet.datumapis.com/member-cluster: fleet-location-e2e
    fleet.datumapis.com/location-name: location-e2e
    fleet.datumapis.com/provider-project: datum-owned-hardware
spec:
  machineRef: {project: project, namespace: default, name: ${machine}}
  machineCapacityRef: {namespace: fleet-provider, name: ${capacity}}
EOF
  kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system patch machineallocation "${machine}-allocation" --subresource=status --type=merge -p '{"status":{"phase":"Active"}}'
done
HOME="${E2E_DIR}/home-authorized" datumctl fleet machine-management proxy db-02 -- \
  --api-server "${api_server}" --api-ca-file "${E2E_DIR}/management-ca.crt" \
  --listen 127.0.0.1:9444 --ready-file "${E2E_DIR}/ready-2.json" \
  --credential-file "${E2E_DIR}/credentials-2.json" --ca-file "${E2E_DIR}/ca-2.pem" \
  >"${E2E_DIR}/proxy-2.log" 2>&1 &
second_proxy_pid=$!
wait_for "second project session" "jq -e '.ready == true' '${E2E_DIR}/ready-2.json' >/dev/null 2>&1" "${second_proxy_pid}"
set +e
run_timeout 20 env HOME="${E2E_DIR}/home-authorized" datumctl fleet machine-management proxy db-03 -- \
  --api-server "${api_server}" --api-ca-file "${E2E_DIR}/management-ca.crt" >"${E2E_DIR}/project-limit.log" 2>&1
project_status=$?
set -e
[[ ${project_status} -ne 0 ]] || fail "third project session was admitted over the limit"
grep -q SessionLimitExceeded "${E2E_DIR}/project-limit.log" || fail "project limit rejection was not actionable"
kill -TERM "${second_proxy_pid}"; wait "${second_proxy_pid}" || true; second_proxy_pid=
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system patch machineallocation db-02-allocation --subresource=status --type=merge -p '{"status":{"phase":"Released"}}'

checkpoint "local login revocation, route isolation, and session limits"

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
  docker exec fleet-e2e-virtual-bmc grep -q '/pxe/52-54-00-aa-00-01' /evidence/pxe-callbacks.jsonl 2>/dev/null && break
  sleep 1
done
docker cp fleet-e2e-virtual-bmc:/evidence/pxe-callbacks.jsonl "${E2E_DIR}/virtual-bmc/evidence/pxe-callbacks.jsonl"
grep -q '/pxe/52-54-00-aa-00-01' "${E2E_DIR}/virtual-bmc/evidence/pxe-callbacks.jsonl" 2>/dev/null || fail "timed out waiting for PXE callback for allocated MAC"
wait_for "PXE fixture poweroff in libvirt" "[[ \$(docker exec fleet-e2e-virtual-bmc virsh domstate allocated | tr -d '\\r') == 'shut off' ]]"
! grep -q '/pxe/52-54-00-aa-00-02' "${E2E_DIR}/virtual-bmc/evidence/pxe-callbacks.jsonl" || fail "unallocated VM produced a PXE callback"
jq -n --arg operationID "$(jq -r .Id <<<"${power_task}")" --arg mac 52:54:00:aa:00:01 \
  --argjson callback "$(grep '/pxe/52-54-00-aa-00-01' "${E2E_DIR}/virtual-bmc/evidence/pxe-callbacks.jsonl" | tail -1)" \
  '{operationID:$operationID,vmMAC:$mac,callback:$callback}' >"${E2E_DIR}/virtual-bmc/evidence/pxe-operation-correlation.json"
checkpoint "fresh allocated PXE callback and libvirt poweroff"

wait_for "authority after PXE boot" "[[ \$(rf_status '${root}/redfish/v1/Systems/allocated') == 200 ]]"

hdd_body='{"Boot":{"BootSourceOverrideEnabled":"Once","BootSourceOverrideTarget":"Hdd"}}'
hdd_task=$(rf -X PATCH -H 'Content-Type: application/json' -d "${hdd_body}" "${root}/redfish/v1/Systems/allocated")
[[ $(jq -r .TaskState <<<"${hdd_task}") == Completed ]] || fail "local-disk mutation did not complete: ${hdd_task}"
[[ $(rf "${root}/redfish/v1/Systems/allocated" | jq -r .Boot.BootSourceOverrideTarget) == Hdd ]] || fail "local-disk target was not restored"
domain_xml=$(docker exec fleet-e2e-virtual-bmc virsh dumpxml allocated)
grep -A6 "<disk type='file' device='disk'>" <<<"${domain_xml}" | grep -q "<boot order='1'/>" || fail "libvirt did not retain the local-disk boot target"
if grep -A6 "<interface type='network'>" <<<"${domain_xml}" | grep -q '<boot order='; then
  fail "libvirt retained a network boot override after local-disk restoration"
fi
checkpoint "Hdd restoration in Redfish and libvirt XML"

# Both pinned DMTF validators run against the live virtual service.
FLEET_CREDENTIAL_FILE="${E2E_DIR}/credentials.json" "${ROOT_DIR}/hack/e2e/validators.sh"
checkpoint "DMTF service and interoperability validators"

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
checkpoint "central API outage, stale-authority rejection, and recovery"

# Allocation release is a terminal fence for this admitted binding.
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system patch machineallocation db-01-allocation --subresource=status --type=merge -p '{"status":{"phase":"Released"}}'
wait_for "released stream closure" "[[ \$(rf_status '${root}/redfish/v1/Systems/allocated') != 200 ]]"
old_session_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machinemanagementsessions -o jsonpath='{.items[0].metadata.uid}')
old_machine_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machine db-01 -o jsonpath='{.metadata.uid}')
old_binding_name=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system get machinemanagementsessionbindings -l "fleet.datumapis.com/session-uid=${old_session_uid}" -o jsonpath='{.items[0].metadata.name}')
stop_proxy

# Recreate the same Machine name for the other real libvirt domain. The old
# terminal allocation and admitted binding remain pinned to the old incarnation.
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default delete machine db-01 --wait=true
kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f "${TEST_DIR}/seed/machine.yaml"
new_machine_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machine db-01 -o jsonpath='{.metadata.uid}')
kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f - <<EOF
apiVersion: metal.datumapis.com/v1alpha1
kind: MachineAllocation
metadata:
  name: db-01-allocation-2
  namespace: fleet-system
  annotations:
    fleet.datumapis.com/machine-uid: "${new_machine_uid}"
    fleet.datumapis.com/member-cluster: fleet-location-e2e
    fleet.datumapis.com/location-name: location-e2e
    fleet.datumapis.com/provider-project: datum-owned-hardware
spec:
  machineRef: {project: project, namespace: default, name: db-01}
  machineCapacityRef: {namespace: fleet-provider, name: db02-capacity}
EOF
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system patch machineallocation db-01-allocation-2 --subresource=status --type=merge -p '{"status":{"phase":"Active"}}'
[[ "${new_machine_uid}" != "${old_machine_uid}" ]] || fail "recreated Machine retained its old UID"
[[ $(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system get machinemanagementsessionbinding "${old_binding_name}" -o jsonpath='{.spec.machineUID}') == "${old_machine_uid}" ]] || fail "old session binding inherited the recreated Machine"
old_binding_revoked=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system get machinemanagementsessionbinding "${old_binding_name}" -o jsonpath='{.metadata.annotations.fleet\.datumapis\.com/revoked-at}')
[[ -n "${old_binding_revoked}" ]] || fail "released old binding was not terminally revoked"

start_proxy
[[ $(rf "${root}/redfish/v1/Systems/allocated" | jq -r .Id) == allocated ]] || fail "virtual system identity was not stable"
# The adapter now targets the newly allocated physical domain; the old domain
# is never addressable through the customer's virtual ID.
rf -X POST -H 'Content-Type: application/json' -d '{"ResetType":"On"}' "${root}/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset" >/dev/null
wait_for "new allocation domain power-on" "[[ \$(docker exec fleet-e2e-virtual-bmc virsh domstate unallocated | tr -d '\\r') == running ]]"
checkpoint "release fence and same-name Machine recreation"

# Fault-inject a lost BMC response. Retrying the identical admitted operation
# must recover its Unknown task without sending a second physical reset.
stop_proxy
wait_for "prior location projection teardown" \
  "sessions_absent '${LOCATION_KUBECONFIG}' machine-access-system projectedmachinemanagementsessions.coordination.fleet.datumapis.com"
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
checkpoint "lost response remains Unknown and retry sends no second reset"

# The gateway schedules the admitted expiry and closes the stream rather than
# treating a still-live client or relay connection as authority.
stop_proxy
start_proxy 1m
sleep 65
[[ $(rf_status "${root}/redfish/v1/Systems/allocated") != 200 ]] || fail "expired session retained Redfish access"
checkpoint "admitted expiry closes access"
stop_proxy
start_proxy

# Stop only the private sushy route. The customer receives a bounded error that
# does not disclose the private endpoint or credential.
docker exec fleet-e2e-virtual-bmc iptables -I INPUT 1 -p tcp --dport 8000 -s 172.30.0.20 -j REJECT
bmc_blocked=true
bmc_error=$(rf "${root}/redfish/v1/Systems/allocated")
[[ $(jq -r .error.code <<<"${bmc_error}") == Fleet.1.0.BMCUnavailable ]] || fail "BMC failure was not actionable"
! grep -Eq '172\.30\.0\.10|fleet-gateway|synthetic-e2e-only' <<<"${bmc_error}" || fail "BMC failure disclosed private data"
docker exec fleet-e2e-virtual-bmc iptables -D INPUT -p tcp --dport 8000 -s 172.30.0.20 -j REJECT
bmc_blocked=false
checkpoint "bounded private BMC outage without credential disclosure"

# If the location cannot acknowledge explicit deletion, the public object may
# finish deleting only with a protected Unknown teardown outcome.
unknown_session_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machinemanagementsessions -o jsonpath='{.items[0].metadata.uid}')
unknown_binding_name=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system get machinemanagementsessionbindings -l "fleet.datumapis.com/session-uid=${unknown_session_uid}" -o jsonpath='{.items[0].metadata.name}')
docker pause fleet-location-e2e-control-plane >/dev/null
location_paused=true
stop_proxy
cleanup_status=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system get machinemanagementsessionbinding "${unknown_binding_name}" -o json | jq -r '.status.conditions[] | select(.type=="CleanupConfirmed") | .status + ":" + .reason')
[[ "${cleanup_status}" == "Unknown:LocationUnconfirmed" ]] || fail "unconfirmed teardown was not Unknown: ${cleanup_status}"
docker unpause fleet-location-e2e-control-plane >/dev/null
location_paused=false
[[ ! -e "${E2E_DIR}/credentials.json" && ! -e "${E2E_DIR}/ca.pem" ]] || fail "proxy secret files survived shutdown"
checkpoint "unconfirmed location teardown remains Unknown; local secrets removed"
echo "machine-management end-to-end scenarios: PASS"
