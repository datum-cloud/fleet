#!/usr/bin/env bash
# End-to-end acceptance of DevicePowerOperation against emulated BMCs in a
# disposable kind cluster in the task-owned Colima profile. Exit 0 pass, 1
# fail (the cluster is kept for inspection). See docs/operations/device-power.md.
set -euo pipefail
umask 077
cd "$(dirname "$0")/../.."
exec python3 hack/device-power/e2e.py "$@"
