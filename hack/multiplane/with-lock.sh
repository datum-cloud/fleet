#!/usr/bin/env bash
# Run one command while holding the profile's runtime lock, queued in arrival
# order with every run that takes it. For a script that starts runs without
# the lock, such as an older checkout's run.sh or a queue of them, so it gets
# a fair turn instead of racing lock-aware runs. A run.sh started under it
# uses the lock it already holds.
set -euo pipefail
[[ $# -gt 0 ]] || { echo 'usage: hack/multiplane/with-lock.sh COMMAND [ARGS...]' >&2; exit 64; }
harness=$(cd "$(dirname "$0")" && pwd)/harness.py
# Records the wait; kept outside the run state directories cleanup manages.
state=$(mktemp -d "${TMPDIR:-/tmp}/fleet-multiplane-with-lock.XXXXXX")
python3 "$harness" lock --state "$state" --owner $$
export FLEET_MULTIPLANE_LOCK_OWNER=$$
# exec keeps this process, so the lock lasts exactly as long as the command.
exec "$@"
