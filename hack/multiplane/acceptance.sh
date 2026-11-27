#!/usr/bin/env bash
# Ordered acceptance phases over an already bootstrapped topology. Each phase is
# bounded and recorded in evidence/phases.jsonl; the first failure stops the run.
set -euo pipefail
umask 077
state=${1:?private state directory}
export DOCKER_CONTEXT=${FLEET_MULTIPLANE_DOCKER_CONTEXT:-colima-fleet-acceptance-tcg}
host="$state/fleet-multiplane-host"
ph() { python3 hack/multiplane/harness.py phase --state "$state" --name "$1" --timeout "$2" --echo -- "${@:3}"; }
step() { ph "$1" "$2" bash hack/multiplane/steps.sh "$1" "$state"; }
flux() { ph "flux-$1" "$2" python3 hack/multiplane/flux.py "$1" "$state"; }
# Scenario group (docs/operations/multiplane.md#ci-mode). Every group runs the
# ungated scenarios and each gated block below that names it;
# all runs every block.
group=${FLEET_MULTIPLANE_GROUP:-all}
runs() { [[ $group == all || $group == "$1" ]]; }

ph rbac 600 python3 hack/multiplane/verify-rbac.py "$state"
ph custody-crd-rules 600 python3 hack/multiplane/custody_crd_rules.py "$state"
step import-inert 300
step staff-access 120
ph allocate 900 "$host" allocate --config "$state/acceptance.json"
step verify-contention 120
step tenant-isolation 300
ph admit-install 900 "$host" admit-install --config "$state/acceptance.json"
step await-delivery 300
step partition 300
step hold-installer 120
step dispatch 900
step interrupt-pod 1200
step interrupt-job 600
step release-installer 120
step resume 1800
step worker-rbac 120
step partition-readiness 900
step heal 1200
ph install 900 "$host" install --config "$state/acceptance.json"
ph install-repeat 900 "$host" install --config "$state/acceptance.json"
step lifecycle 900
if runs flux; then
 flux graph-lifecycle 1200
 flux drift 900
 flux restart 1200
 flux fail-closed 1500
 flux retention 1500
fi
if runs release; then
 ph reflection-faults 900 python3 hack/multiplane/reflection-faults.py "$state"
 ph release 1200 "$host" release --config "$state/acceptance.json"
 step release-status-a 120
 ph reuse 900 "$host" reuse --config "$state/acceptance.json"
 step reuse-status 120
 step delivery-faults 900
 step deadline-admit 1200
 step machine-delete 300
 step deadline-hold 1500
 ph release-b 2400 "$host" release-b --config "$state/acceptance.json"
 step deadline-settled 300
 step release-status-b 120
 step rebuild 1200
 step wrong-location 300
 step boot-denials 300
fi
step flux-final 900
ph evidence 300 python3 hack/multiplane/evidence.py "$state"
