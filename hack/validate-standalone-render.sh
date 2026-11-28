#!/bin/sh
set -eu

rendered=$(mktemp)
trap 'rm -f "$rendered"' EXIT
kustomize build config/standalone >"$rendered"

require_count() {
  expected=$1
  pattern=$2
  actual=$(grep -c -- "$pattern" "$rendered" || true)
  if [ "$actual" -ne "$expected" ]; then
    echo "rendered manifest count for $pattern = $actual, want $expected" >&2
    exit 1
  fi
}

require_count 7 'runAsUser: 65532'
require_count 7 'runAsGroup: 65532'
require_count 7 'fsGroup: 65532'
require_count 17 'defaultMode: 288'
require_count 7 'imagePullPolicy: Never'
require_count 5 'suspend: true'
require_count 1 'name: fleet-smoke-reinstall'
require_count 1 '--phase=reinstall'
require_count 1 '--phase=commission'
require_count 1 'fsGroupChangePolicy: OnRootMismatch'
require_count 1 'mountPath: /var/lib/fleet'
require_count 1 'volumeClaimTemplates:'

if grep -Eq ':latest|admin-listen-address|standalone:latest' "$rendered"; then
  echo "rendered standalone workload contains a forbidden mutable/admin setting" >&2
  exit 1
fi
