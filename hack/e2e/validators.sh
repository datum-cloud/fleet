#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_DIR="${ROOT_DIR}/.e2e"
CREDENTIAL_FILE=${FLEET_CREDENTIAL_FILE:-${E2E_DIR}/credentials.json}
REPORT_DIR="${E2E_DIR}/validators"
[[ -f "${CREDENTIAL_FILE}" ]] || { echo "missing proxy credential file: ${CREDENTIAL_FILE}" >&2; exit 1; }
mkdir -p "${REPORT_DIR}/service" "${REPORT_DIR}/interop"

endpoint=$(jq -r .endpoint "${CREDENTIAL_FILE}")
username=$(jq -r .username "${CREDENTIAL_FILE}")
password=$(jq -r .password "${CREDENTIAL_FILE}")
ca_bundle=$(jq -r .caBundle "${CREDENTIAL_FILE}")

if [[ $(uname -s) == Darwin ]]; then
  validator_bin=${FLEET_VALIDATOR_BIN:-/private/tmp/fleet-validator-venv/bin}
  REQUESTS_CA_BUNDLE="${ca_bundle}" "${validator_bin}/rf_service_validator" \
    --rhost "${endpoint%/redfish/v1/}" --user "${username}" --password "${password}" \
    --authtype Session --nooemcheck --logdir "${REPORT_DIR}/service"
  REQUESTS_CA_BUNDLE="${ca_bundle}" "${validator_bin}/rf_interop_validator" \
    --rhost "${endpoint%/redfish/v1/}" --user "${username}" --password "${password}" \
    --authtype Session --nooemcheck --logdir "${REPORT_DIR}/interop" \
    "${ROOT_DIR}/config/redfish/FleetProvisioning.v1_0_0.json"
else
  docker run --rm --network host \
    -e REQUESTS_CA_BUNDLE=/state/ca.pem \
    -v "${ROOT_DIR}:/work:ro" -v "${E2E_DIR}:/state" \
    fleet-redfish-tools:e2e \
    "rf_service_validator --rhost '${endpoint%/redfish/v1/}' --user '${username}' --password '${password}' --authtype Session --nooemcheck --logdir /state/validators/service"

  docker run --rm --network host \
    -e REQUESTS_CA_BUNDLE=/state/ca.pem \
    -v "${ROOT_DIR}:/work:ro" -v "${E2E_DIR}:/state" \
    fleet-redfish-tools:e2e \
    "rf_interop_validator --rhost '${endpoint%/redfish/v1/}' --user '${username}' --password '${password}' --authtype Session --nooemcheck --logdir /state/validators/interop /work/config/redfish/FleetProvisioning.v1_0_0.json"
fi

echo "DMTF validator reports: ${REPORT_DIR}"
