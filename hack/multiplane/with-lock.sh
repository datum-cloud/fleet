#!/usr/bin/env bash
# Run one command while holding the profile's runtime lock, queued in arrival
# order with every run that takes it. For a script that starts runs without
# the lock, such as an older checkout's run.sh or a queue of them, so it gets
# a fair turn instead of racing lock-aware runs. A run.sh, or another
# with-lock.sh, started under it uses the lock it already holds; start such
# runs one after another, never in parallel.
set -euo pipefail
[[ $# -gt 0 ]] || { echo 'usage: hack/multiplane/with-lock.sh COMMAND [ARGS...]' >&2; exit 64; }
harness=$(cd "$(dirname "$0")" && pwd)/harness.py
# Records the wait, outside the run state directories cleanup manages.
find "${TMPDIR:-/tmp}" -maxdepth 1 -name 'fleet-multiplane-with-lock.*' -mtime +1 -exec rm -rf {} + 2>/dev/null || true
state=$(mktemp -d "${TMPDIR:-/tmp}/fleet-multiplane-with-lock.XXXXXX")
python3 "$harness" lock --state "$state" --owner $$
# Under an outer wrapper the lock is its; otherwise this process holds it.
grep -q '"heldBy"' "$state/evidence/lock.json" || export FLEET_MULTIPLANE_LOCK_OWNER=$$
# exec keeps this process, so the lock lasts exactly as long as the command.
exec "$@"
