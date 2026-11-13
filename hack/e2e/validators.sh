#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_DIR="${ROOT_DIR}/.e2e"
CREDENTIAL_FILE=${FLEET_CREDENTIAL_FILE:-${E2E_DIR}/credentials.json}
REPORT_DIR="${E2E_DIR}/validators"
SCHEMA_DIR="${E2E_DIR}/redfish-schemas"
source "${ROOT_DIR}/hack/e2e/common.sh"
[[ -f "${CREDENTIAL_FILE}" ]] || { echo "missing proxy credential file: ${CREDENTIAL_FILE}" >&2; exit 1; }
for schema in ServiceRoot SessionService Session SessionCollection ComputerSystem ComputerSystemCollection TaskService Task TaskCollection Resource; do
  [[ -s "${SCHEMA_DIR}/${schema}_v1.xml" || -s "${SCHEMA_DIR}/${schema}.xml" ]] || fail "missing pinned ${schema} schema; run task e2e:up"
done
mkdir -p "${REPORT_DIR}/service" "${REPORT_DIR}/interop"
cp "${SCHEMA_DIR}/provenance.txt" "${REPORT_DIR}/schema-provenance.txt"

endpoint=$(jq -r .endpoint "${CREDENTIAL_FILE}")
username=$(jq -r .username "${CREDENTIAL_FILE}")
password=$(jq -r .password "${CREDENTIAL_FILE}")
ca_bundle=$(jq -r .caBundle "${CREDENTIAL_FILE}")

if [[ $(uname -s) == Darwin ]]; then
  validator_bin=${FLEET_VALIDATOR_BIN:-/private/tmp/fleet-validator-venv/bin}
  REQUESTS_CA_BUNDLE="${ca_bundle}" "${validator_bin}/rf_service_validator" \
    --rhost "${endpoint%/redfish/v1/}" --user "${username}" --password "${password}" \
    --authtype Session --nooemcheck --logdir "${REPORT_DIR}/service" \
    --skipschema --schema_directory "${SCHEMA_DIR}" 2>&1 | tee "${REPORT_DIR}/service-summary.log"
  validator_coverage "${REPORT_DIR}/service-summary.log" || fail "service validator did not complete warning-free schema validation"
  REQUESTS_CA_BUNDLE="${ca_bundle}" "${validator_bin}/rf_interop_validator" \
    --rhost "${endpoint%/redfish/v1/}" --user "${username}" --password "${password}" \
    --authtype Session --nooemcheck --logdir "${REPORT_DIR}/interop" \
    "${ROOT_DIR}/config/redfish/FleetProvisioning.v1_0_0.json" 2>&1 | tee "${REPORT_DIR}/interop-summary.log"
else
  docker run --rm --network host --user "$(id -u):$(id -g)" --workdir /state/validators/service \
    -e REQUESTS_CA_BUNDLE=/state/ca.pem \
    -v "${ROOT_DIR}:/work:ro" -v "${E2E_DIR}:/state" \
    fleet-redfish-tools:e2e \
    "rf_service_validator --rhost '${endpoint%/redfish/v1/}' --user '${username}' --password '${password}' --authtype Session --nooemcheck --logdir /state/validators/service --skipschema --schema_directory /state/redfish-schemas" 2>&1 | tee "${REPORT_DIR}/service-summary.log"
  validator_coverage "${REPORT_DIR}/service-summary.log" || fail "service validator did not complete warning-free schema validation"

  docker run --rm --network host --user "$(id -u):$(id -g)" --workdir /state/validators/interop \
    -e REQUESTS_CA_BUNDLE=/state/ca.pem \
    -v "${ROOT_DIR}:/work:ro" -v "${E2E_DIR}:/state" \
    fleet-redfish-tools:e2e \
    "rf_interop_validator --rhost '${endpoint%/redfish/v1/}' --user '${username}' --password '${password}' --authtype Session --nooemcheck --logdir /state/validators/interop /work/config/redfish/FleetProvisioning.v1_0_0.json" 2>&1 | tee "${REPORT_DIR}/interop-summary.log"
fi
validator_coverage "${REPORT_DIR}/interop-summary.log" || fail "interop validator did not complete warning-free profile validation"

echo "DMTF validator reports: ${REPORT_DIR}"
