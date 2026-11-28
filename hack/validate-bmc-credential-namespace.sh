#!/bin/sh
# Fails when a Secret other than a BMC credential Secret renders into a BMC
# credential namespace. See docs/operations/bmc-discovery.md.
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/fleet-bmc-credential-namespace-go-cache}
export GOCACHE
mkdir -p "$GOCACHE"
exec go run "$repo_root/hack/bmc-credential-namespace-validator.go" "${1:-config}"
