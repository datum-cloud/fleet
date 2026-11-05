#!/usr/bin/env bash
set -euo pipefail

render="$(mktemp)"
trap 'rm -f "${render}"' EXIT
kustomize build config/overlays/synthetic-staging >"${render}"

grep -q -- '--synthetic-provider-url=http://127.0.0.1:8090' "${render}"
grep -q -- '--listen-address=0.0.0.0:8090' "${render}"
if grep -q -- '--synthetic-provider-url=http://127.0.0.1:8080' "${render}" ||
  grep -q -- '--listen-address=0.0.0.0:8080' "${render}"; then
  echo 'synthetic provider conflicts with controller-runtime metrics on port 8080' >&2
  exit 1
fi
