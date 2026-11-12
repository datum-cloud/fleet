#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_DIR="${ROOT_DIR}/.e2e"
TEST_DIR="${ROOT_DIR}/test/e2e/machine-management"
LOCATION_KUBECONFIG="${E2E_DIR}/location.kubeconfig"
export FLEET_E2E_STATE="${E2E_DIR}/virtual-bmc"
[[ $(uname -s) == Linux ]] || { echo "OpenVPN qualification requires Linux" >&2; exit 1; }
[[ -f "${LOCATION_KUBECONFIG}" ]] || { echo "run task e2e:up first" >&2; exit 1; }

docker compose -p fleet-bmc-e2e -f "${TEST_DIR}/compose.yaml" -f "${TEST_DIR}/compose.openvpn.yaml" up -d --build --force-recreate virtual-bmc
for _ in $(seq 1 60); do [[ -s "${FLEET_E2E_STATE}/evidence/openvpn.key" ]] && break; sleep 1; done
[[ -s "${FLEET_E2E_STATE}/evidence/openvpn.key" ]] || { echo "OpenVPN server key was not created" >&2; exit 1; }
docker build -t fleet-openvpn-client:e2e "${TEST_DIR}/openvpn"
kind load docker-image fleet-openvpn-client:e2e --name fleet-location-e2e
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system create secret generic fleet-openvpn-key \
  --from-file=openvpn.key="${FLEET_E2E_STATE}/evidence/openvpn.key" --dry-run=client -o yaml | kubectl --kubeconfig "${LOCATION_KUBECONFIG}" apply -f -
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" apply -f "${TEST_DIR}/openvpn/client.yaml"
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system rollout status daemonset/fleet-openvpn-client --timeout=120s
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system patch deployment fleet-gateway --type=json \
  -p='[{"op":"replace","path":"/spec/template/spec/containers/0/args/5","value":"--bmc-url=http://10.88.0.1:8000"}]'
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system rollout status deployment/fleet-gateway --timeout=120s
pod=$(kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system get pod -l app.kubernetes.io/name=fleet-openvpn-client -o jsonpath='{.items[0].metadata.name}')
kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system exec "${pod}" -- curl --fail --silent http://10.88.0.1:8000/redfish/v1/Systems >/dev/null
if kubectl --kubeconfig "${LOCATION_KUBECONFIG}" -n machine-access-system exec "${pod}" -- curl --fail --silent --connect-timeout 2 http://172.30.0.10:8000/redfish/v1/Systems >/dev/null 2>&1; then
  echo "direct management route remained reachable in OpenVPN profile" >&2
  exit 1
fi
echo "OpenVPN management boundary: PASS"
