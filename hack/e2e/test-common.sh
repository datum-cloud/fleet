#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
source "${ROOT_DIR}/hack/e2e/common.sh"
source "${ROOT_DIR}/hack/e2e/lifecycle.sh"

wait_for "immediate success" true
if (FLEET_E2E_WAIT_SECONDS=1 wait_for "bounded failure" false) 2>/dev/null; then
  fail "failed predicate passed"
fi
if (wait_for "dead proxy with stale ready file" true 2147483647) 2>/dev/null; then
  fail "dead proxy accepted stale readiness"
fi
kubectl() { return 1; }
if sessions_absent ignored default sessions; then
  fail "API failure counted as confirmed deletion"
fi
kubectl() { printf '%s\n' '{"items":[{"metadata":{"name":"retained"}}]}'; }
if sessions_absent ignored default sessions; then
  fail "existing session counted as deleted"
fi
kubectl() { printf '%s\n' '{"items":[]}'; }
sessions_absent ignored default sessions
kubectl() { printf '%s\n' '{}'; }
if sessions_absent ignored default sessions; then
  fail "missing list evidence counted as confirmed deletion"
fi
run_timeout 2 bash -c 'exit 0'
if run_timeout 1 bash -c 'sleep 5'; then
  fail "timeout failed to stop command"
fi
report=$(mktemp)
trap 'rm -f "${report}"' EXIT
for summary in '' '  - Pass: 0, Warn: 0, Fail: 0, Skip: 0' '  - Pass: 0, Warn: 1, Fail: 0, Skip: 0' '  - Pass: 1, Warn: 1, Fail: 0, Skip: 0' '  - Pass: 1, Warn: 0, Fail: 1, Skip: 0'; do
  printf '%s\n' "${summary}" >"${report}"
  if validator_coverage "${report}"; then
    fail "incomplete validator coverage accepted: ${summary}"
  fi
done
printf '%s\n' '  - Pass: 12, Warn: 0, Fail: 0, Skip: 35' >"${report}"
validator_coverage "${report}"
MGMT_KUBECONFIG=ignored
kubectl() { return 1; }
if binding_clean binding; then fail "API failure counted as verified teardown"; fi
kubectl() { printf '%s\n' '{"metadata":{"generation":2},"status":{"conditions":[{"type":"CleanupConfirmed","status":"True","observedGeneration":1}]}}'; }
if binding_clean binding; then fail "old generation counted as verified teardown"; fi
kubectl() { printf '%s\n' '{"metadata":{"generation":2},"status":{"conditions":[{"type":"CleanupConfirmed","status":"Unknown","observedGeneration":2}]}}'; }
if binding_clean binding; then fail "unknown counted as verified teardown"; fi
kubectl() { printf '%s\n' '{"metadata":{"generation":2},"status":{"conditions":[{"type":"CleanupConfirmed","status":"True","observedGeneration":2}]}}'; }
binding_clean binding
kubectl() { printf '%s\n' '{"items":[]}'; }
if asset_state uid Releasing; then fail "missing custody counted as occupied"; fi
kubectl() { return 1; }
if asset_state uid Releasing; then fail "API failure counted as occupied custody"; fi
echo 'E2E harness helpers: PASS'
