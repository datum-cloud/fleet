#!/usr/bin/env bash

fail() { echo "FAIL: $*" >&2; exit 1; }

wait_for() {
  local description=$1 command=$2 process_id=${3:-}
  local deadline=$((SECONDS + ${FLEET_E2E_WAIT_SECONDS:-240}))
  while (( SECONDS < deadline )); do
    if [[ -n "${process_id}" ]] && ! kill -0 "${process_id}" 2>/dev/null; then
      fail "${description}: process ${process_id} exited before readiness; inspect proxy logs"
    fi
    eval "${command}" && return 0
    sleep 1
  done
  fail "timed out waiting for ${description}"
}

run_timeout() {
  local seconds=$1
  shift
  if command -v timeout >/dev/null; then timeout "${seconds}" "$@"; return; fi
  if command -v gtimeout >/dev/null; then gtimeout "${seconds}" "$@"; return; fi
  perl -e '$seconds = shift; alarm $seconds; exec @ARGV' "${seconds}" "$@"
}

checkpoint() {
  printf '%s PASS: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*" | tee -a "${E2E_DIR}/checkpoints.log"
}

sessions_absent() {
  local kubeconfig=$1 namespace=$2 resource=$3 objects
  objects=$(kubectl --request-timeout=10s --kubeconfig "${kubeconfig}" -n "${namespace}" get "${resource}" -o json) || return 1
  jq -e '(.items | type == "array") and (.items | length == 0)' <<<"${objects}" >/dev/null
}

validator_coverage() {
  awk -F '[:,]' '
    /^  - Pass:/ { resources++; passed += $2; warnings += $4; failed += $6 }
    END { exit !(resources > 0 && passed > 0 && warnings == 0 && failed == 0) }
  ' "$1"
}
