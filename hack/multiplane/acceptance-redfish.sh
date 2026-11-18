#!/usr/bin/env bash
# Ordered Redfish-mode acceptance phases (run.sh --redfish). The same
# allocator, installation, Karmada, kro and Flux path as acceptance.sh; the
# site worker drives lab targets only through the DMTF Redfish emulator.
# Each phase is bounded and recorded; the first failure stops the run.
set -euo pipefail
umask 077
state=${1:?private state directory}
export DOCKER_CONTEXT=colima-fleet-acceptance-tcg FLEET_SITE_PROVIDER=redfish
host="$state/fleet-multiplane-host"
ph() { python3 hack/multiplane/harness.py phase --state "$state" --name "$1" --timeout "$2" --echo -- "${@:3}"; }
step() { ph "$1" "$2" bash hack/multiplane/steps.sh "$1" "$state"; }
rf() { ph "redfish-$1" "$2" python3 hack/multiplane/redfish_steps.py "$1" "$state"; }
flux() { ph "flux-$1" "$2" python3 hack/multiplane/flux.py "$1" "$state"; }

ph rbac 600 python3 hack/multiplane/verify-rbac.py "$state"
rf baseline 300
rf discovery-faults 600
ph allocate 900 "$host" allocate --config "$state/acceptance.json"
step verify-contention 120
ph admit-install 900 "$host" admit-install --config "$state/acceptance.json"
step await-delivery 300
rf arm-lost-response 120
step partition 300
step dispatch 900
rf bmc-interrupt 600
step interrupt-pod 1200
step interrupt-job 600
rf lost-response 1200
step resume 2400
step worker-rbac 120
step partition-readiness 900
step heal 1200
ph install 900 "$host" install --config "$state/acceptance.json"
ph install-repeat 900 "$host" install --config "$state/acceptance.json"
step lifecycle 900
flux graph-lifecycle 1200
flux drift 900
flux restart 1200
flux fail-closed 1500
flux retention 1500
ph reflection-faults 900 python3 hack/multiplane/reflection-faults.py "$state"
ph release 2400 "$host" release --config "$state/acceptance.json"
step release-status-a 120
ph reuse 900 "$host" reuse --config "$state/acceptance.json"
step reuse-status 120
step delivery-faults 900
ph install-b 2400 "$host" install-b --config "$state/acceptance.json"
ph release-b 2400 "$host" release-b --config "$state/acceptance.json"
step release-status-b 120
step wrong-location 300
step boot-denials 300
step flux-final 900
rf isolation 600
ph evidence 600 python3 hack/multiplane/evidence.py "$state"
