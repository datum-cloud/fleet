#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_DIR="${ROOT_DIR}/.e2e"
TEST_DIR="${ROOT_DIR}/test/e2e/machine-management"
export FLEET_E2E_STATE="${E2E_DIR}/virtual-bmc"
kind delete cluster --name fleet-management-e2e 2>/dev/null || true
kind delete cluster --name fleet-location-e2e 2>/dev/null || true
docker compose -p fleet-bmc-e2e -f "${TEST_DIR}/compose.yaml" down --remove-orphans 2>/dev/null || true
docker network rm fleet-management-e2e 2>/dev/null || true
case "${E2E_DIR}" in
  "${ROOT_DIR}/.e2e") rm -rf -- "${E2E_DIR}" ;;
  *) echo "refusing to remove unexpected path ${E2E_DIR}" >&2; exit 1 ;;
esac
