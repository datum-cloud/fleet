#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/fleet-overlay-isolation-go-cache}
export GOCACHE
mkdir -p "$GOCACHE"
exec go run "$repo_root/hack/overlay-isolation-validator.go" "${1:-config}" "${2:-}"
