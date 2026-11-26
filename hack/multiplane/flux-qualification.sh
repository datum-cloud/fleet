#!/usr/bin/env bash
# The Flux qualification suite (run.sh --suite flux-qualification): the GitOps
# behavior scenarios that test Fleet's packaging, over an already bootstrapped topology. One installation
# runs to completion first, so each scenario proves Flux leaves a delivered
# handoff, its Job, the journal, the volume and custody untouched. The Device
# is then released, so the run ends with custody released and verified. Each
# phase is bounded and recorded; the first failure stops the run.
set -euo pipefail
umask 077
state=${1:?private state directory}
export DOCKER_CONTEXT=colima-fleet-acceptance-tcg
host="$state/fleet-multiplane-host"
ph() { python3 hack/multiplane/harness.py phase --state "$state" --name "$1" --timeout "$2" --echo -- "${@:3}"; }
step() { ph "$1" "$2" bash hack/multiplane/steps.sh "$1" "$state"; }
flux() { ph "flux-$1" "$2" python3 hack/multiplane/flux.py "$1" "$state"; }

ph allocate 900 "$host" allocate --config "$state/acceptance.json"
ph admit-install 900 "$host" admit-install --config "$state/acceptance.json"
step await-delivery 300
step dispatch 900
step complete 2400
ph install 900 "$host" install --config "$state/acceptance.json"
flux graph-lifecycle 1200
flux drift 900
flux restart 1200
flux fail-closed 1500
ph release 2400 "$host" release --config "$state/acceptance.json"
step release-status-a 120
step flux-final 900
ph evidence 300 python3 hack/multiplane/flux.py qualification-evidence "$state"
