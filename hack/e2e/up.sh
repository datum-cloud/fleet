#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_DIR="${ROOT_DIR}/.e2e"
TEST_DIR="${ROOT_DIR}/test/e2e/machine-management"
MGMT_KUBECONFIG="${E2E_DIR}/management.kubeconfig"
LOCATION_KUBECONFIG="${E2E_DIR}/location.kubeconfig"
export FLEET_E2E_STATE="${E2E_DIR}/virtual-bmc"

for tool in docker kind kubectl go jq openssl; do
  command -v "${tool}" >/dev/null || { echo "missing required tool: ${tool}" >&2; exit 1; }
done
if [[ $(uname -s) != Linux ]]; then
  echo "warning: Linux is the qualified environment; attempting the same QEMU software-emulation path on $(uname -s)" >&2
  # Cross-architecture Lima guests can expose a nominal /dev/kvm even though
  # nesting it reboots the outer VM. Select the required software fallback.
  export FLEET_FORCE_QEMU=${FLEET_FORCE_QEMU:-1}
fi
mkdir -p "${E2E_DIR}/bin" "${FLEET_E2E_STATE}/evidence" "${FLEET_E2E_STATE}/images"
find "${FLEET_E2E_STATE}/evidence" -maxdepth 1 -type f -delete

docker compose -p fleet-bmc-e2e -f "${TEST_DIR}/compose.yaml" up -d --build
for _ in $(seq 1 60); do docker exec fleet-e2e-virtual-bmc virsh list --all >/dev/null 2>&1 && break; sleep 1; done
docker exec fleet-e2e-virtual-bmc virsh destroy allocated >/dev/null 2>&1 || true
docker exec fleet-e2e-virtual-bmc virsh destroy unallocated >/dev/null 2>&1 || true

if ! kind get clusters | grep -qx fleet-management-e2e; then
  kind create cluster --name fleet-management-e2e --config "${TEST_DIR}/kind-management.yaml"
fi
if ! kind get clusters | grep -qx fleet-location-e2e; then
  kind create cluster --name fleet-location-e2e --config "${TEST_DIR}/kind-location.yaml"
fi
kind get kubeconfig --name fleet-management-e2e >"${MGMT_KUBECONFIG}"
kind get kubeconfig --name fleet-location-e2e >"${LOCATION_KUBECONFIG}"
chmod 0600 "${MGMT_KUBECONFIG}" "${LOCATION_KUBECONFIG}"

if ! docker network inspect fleet-management-e2e | jq -e '.[0].Containers | to_entries[] | select(.value.Name=="fleet-location-e2e-control-plane")' >/dev/null; then
  docker network connect --ip 172.30.0.20 fleet-management-e2e fleet-location-e2e-control-plane
fi

build_image() {
  local image=$1 package=$2 binary=$3
  docker build -t "${image}:e2e" --build-arg "PACKAGE=${package}" --build-arg "BINARY=${binary}" "${ROOT_DIR}"
}
build_image fleet-controller ./cmd/fleet-controller fleet-controller
build_image fleet-gateway ./cmd/fleet-gateway fleet-gateway
build_image fleet-relay ./cmd/fleet-relay fleet-relay
docker build -t fleet-redfish-tools:e2e "${TEST_DIR}/tools"
kind load docker-image fleet-controller:e2e --name fleet-management-e2e
kind load docker-image fleet-gateway:e2e fleet-relay:e2e --name fleet-location-e2e

kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -k "${ROOT_DIR}/config/crd"
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" apply -k "${ROOT_DIR}/config/crd"
kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -k "${ROOT_DIR}/config/base"
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" apply -k "${ROOT_DIR}/config/base"
kubectl --kubeconfig "${MGMT_KUBECONFIG}" wait --for=condition=Established --timeout=60s crd/machinemanagementsessions.fleet.datumapis.com
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" wait --for=condition=Established --timeout=60s crd/projectedmachinemanagementsessions.fleet.datumapis.com
kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f "${ROOT_DIR}/config/rbac/controller.yaml"
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" apply -f "${ROOT_DIR}/config/rbac/controller.yaml"
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" apply -f "${ROOT_DIR}/config/rbac/projector-location.yaml"

relay_token=$(openssl rand -hex 32)
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system create secret generic fleet-relay-token \
  --from-literal=relay-token="${relay_token}" --dry-run=client -o yaml | kubectl --kubeconfig "${LOCATION_KUBECONFIG}" apply -f -
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system create secret generic fleet-bmc-credentials \
  --from-literal=bmc-username=fleet-gateway --from-literal=bmc-password=synthetic-e2e-only \
  --dry-run=client -o yaml | kubectl --kubeconfig "${LOCATION_KUBECONFIG}" apply -f -
unset relay_token

projector_token=$(kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system create token fleet-projector --duration=2h)
location_ca=$(kubectl config view --kubeconfig "${LOCATION_KUBECONFIG}" --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')
location_ip=$(docker inspect fleet-location-e2e-control-plane | jq -r '.[0].NetworkSettings.Networks.kind.IPAddress')
cat >"${E2E_DIR}/projector.kubeconfig" <<EOF
apiVersion: v1
kind: Config
clusters:
  - name: location
    cluster:
      server: https://${location_ip}:6443
      certificate-authority-data: ${location_ca}
users:
  - name: projector
    user:
      token: ${projector_token}
contexts:
  - name: location
    context: {cluster: location, user: projector, namespace: machine-access-system}
current-context: location
EOF
chmod 0600 "${E2E_DIR}/projector.kubeconfig"
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system create secret generic fleet-location-kubeconfig \
  --from-file=kubeconfig="${E2E_DIR}/projector.kubeconfig" --dry-run=client -o yaml | kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f -
unset projector_token location_ca

mkdir -p "${E2E_DIR}/webhook"
openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -keyout "${E2E_DIR}/webhook/tls.key" -out "${E2E_DIR}/webhook/tls.crt" \
  -subj /CN=fleet-webhook.fleet-system.svc \
  -addext subjectAltName=DNS:fleet-webhook.fleet-system.svc,DNS:fleet-webhook.fleet-system.svc.cluster.local >/dev/null 2>&1
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system create secret tls fleet-webhook-tls \
  --cert="${E2E_DIR}/webhook/tls.crt" --key="${E2E_DIR}/webhook/tls.key" --dry-run=client -o yaml | kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f -
kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f "${ROOT_DIR}/config/manager/controller.yaml"
kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f "${ROOT_DIR}/config/manager/networkpolicy.yaml"
webhook_ca=$(base64 -w0 <"${E2E_DIR}/webhook/tls.crt")
kubectl --kubeconfig "${MGMT_KUBECONFIG}" patch mutatingwebhookconfiguration fleet-machine-management-session --type=json \
  -p="[{\"op\":\"add\",\"path\":\"/webhooks/0/clientConfig/caBundle\",\"value\":\"${webhook_ca}\"}]"
unset webhook_ca

kubectl --kubeconfig "${LOCATION_KUBECONFIG}" apply -f "${ROOT_DIR}/config/location/access.yaml"
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" apply -f "${ROOT_DIR}/config/location/networkpolicy.yaml"
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system rollout status deployment/fleet-controller --timeout=120s
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system rollout status deployment/fleet-relay --timeout=120s
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system rollout status deployment/fleet-gateway --timeout=120s

# Reset only resources owned by this dedicated E2E environment so repeated up
# and test invocations begin from the same authority state.
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default delete machinemanagementsessions --all --timeout=40s --ignore-not-found
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system delete projectedmachinemanagementsessions --all --ignore-not-found
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system delete machinemanagementsessionbindings --all --ignore-not-found
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default delete machineclaims db-02 db-03 --ignore-not-found
kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system delete machineallocationbindings db-02-allocation db-03-allocation --ignore-not-found

kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f "${TEST_DIR}/seed/claim.yaml"
claim_uid=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default get machineclaim db-01 -o jsonpath='{.metadata.uid}')
sed "s/CLAIM_UID/${claim_uid}/g" "${TEST_DIR}/seed/allocation.yaml" | kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f -

kubectl --kubeconfig "${MGMT_KUBECONFIG}" apply -f "${TEST_DIR}/seed/test-identities.yaml"
authorized_token=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default create token fleet-authorized --duration=2h)
viewer_token=$(kubectl --kubeconfig "${MGMT_KUBECONFIG}" -n default create token fleet-viewer --duration=2h)

env GOBIN="${E2E_DIR}/bin" go install go.datum.net/datumctl@v0.15.0
env GOCACHE="${E2E_DIR}/go-cache" go build -o "${E2E_DIR}/bin/datumctl-fleet" "${ROOT_DIR}/cmd/datumctl-fleet"
api_server=$(kubectl config view --kubeconfig "${MGMT_KUBECONFIG}" --raw -o jsonpath='{.clusters[0].cluster.server}')
api_host=${api_server#https://}
kubectl config view --kubeconfig "${MGMT_KUBECONFIG}" --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d >"${E2E_DIR}/management-ca.crt"

write_datum_session() {
  local name=$1 token=$2
  local home_dir="${E2E_DIR}/home-${name}" user_key="${name}@kind"
  mkdir -p "${home_dir}/.datumctl"
  chmod 0700 "${home_dir}/.datumctl"
  cat >"${home_dir}/.datumctl/config" <<EOF
apiVersion: datumctl.config.datum.net/v1beta1
kind: DatumctlConfig
sessions:
  - name: e2e
    user-key: ${user_key}
    user-email: ${name}@example.test
    endpoint:
      server: ${api_server}
      auth-hostname: kind.invalid
contexts:
  - name: e2e/acme/project
    session: e2e
    organization-id: acme
    project-id: project
    namespace: default
current-context: e2e/acme/project
active-session: e2e
EOF
  local expiry creds
  if date -u -d '+2 hours' '+%Y-%m-%dT%H:%M:%SZ' >/dev/null 2>&1; then
    expiry=$(date -u -d '+2 hours' '+%Y-%m-%dT%H:%M:%SZ')
  else
    expiry=$(date -u -v+2H '+%Y-%m-%dT%H:%M:%SZ')
  fi
  creds=$(jq -cn --arg token "${token}" --arg expiry "${expiry}" --arg host "${api_host}" --arg email "${name}@example.test" \
    '{hostname:"kind.invalid",api_hostname:$host,client_id:"fleet-e2e",endpoint_auth_url:"https://kind.invalid/auth",endpoint_token_url:"https://kind.invalid/token",scopes:[],token:{access_token:$token,token_type:"Bearer",expiry:$expiry},user_name:$email,user_email:$email,subject:$email}')
  jq -n --arg key "${user_key}" --arg creds "${creds}" '{"datumctl-auth":{($key):$creds,"active_user":$key,"known_users":([$key]|tojson)}}' >"${home_dir}/.datumctl/credentials.json"
  chmod 0600 "${home_dir}/.datumctl/config" "${home_dir}/.datumctl/credentials.json"
}
write_datum_session authorized "${authorized_token}"
write_datum_session viewer "${viewer_token}"
unset authorized_token viewer_token
for identity in authorized viewer; do
  HOME="${E2E_DIR}/home-${identity}" PATH="${E2E_DIR}/bin:${PATH}" \
    "${E2E_DIR}/bin/datumctl" plugin trust fleet >/dev/null
done

echo "Fleet machine-management e2e environment is ready in ${E2E_DIR}"
