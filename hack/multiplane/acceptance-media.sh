#!/usr/bin/env bash
# Ordered virtual media acceptance phases (run.sh --redfish --virtual-media).
# The same allocator, Karmada, kro and Flux path as acceptance-redfish.sh; the
# worker delivers the installer as virtual media through the lab BMC's
# Redfish VirtualMedia and the central media service, and the installed OS
# posts its report there at first boot. Each phase is bounded and recorded;
# the first failure stops the run.
set -euo pipefail
umask 077
state=${1:?private state directory}
export DOCKER_CONTEXT=colima-fleet-acceptance-tcg FLEET_SITE_PROVIDER=redfish
host="$state/fleet-multiplane-host"
ph() { python3 hack/multiplane/harness.py phase --state "$state" --name "$1" --timeout "$2" --echo -- "${@:3}"; }
rf() { ph "redfish-$1" "$2" python3 hack/multiplane/redfish_steps.py "$1" "$state"; }
md() { ph "media-$1" "$2" python3 hack/multiplane/media_steps.py "$1" "$state"; }
run() { ph "$1" "$2" "$host" "$1" --config "$state/acceptance.json"; }

ph rbac 600 python3 hack/multiplane/verify-rbac.py "$state"
rf baseline 300
run allocate 900
# The cell declares network boot only: a virtual media installation is
# refused at admission and nothing is delivered.
run media-not-offered 600
ph media-central 300 bash hack/multiplane/media-central.sh start "$state"
md offer 300
# An installer download by tag, not digest, is refused at admission.
run media-unpinned 600
# Dispatch is a source revision the cell's Flux reconciles, as in the
# Redfish suite; kro then creates a worker Job per delivered operation.
ph dispatch 900 python3 hack/multiplane/flux.py publish "$state" --revision 3 --site dispatch --purpose 'enable dispatch' --cluster cell --timeout 600
# The first install's report carries another token: it is held.
md arm-corrupt-token 120
run media-install-held 3000
md held-report 300
# The second install settles that hold, but its config medium gains a
# download the operation never pinned: its report names it, and it is held.
md arm-add-download 120
run media-install-download 3000
md held-download 300
# The third install settles that hold, then loses an eject response: held
# again.
md arm-lost-eject 120
run media-install-lost 3000
md held-eject 300
# The fourth install settles that hold, proves release and clears it. Its OS
# first posts the right token from another address in the site's ranges,
# which is refused; only its IPAM address may report.
md arm-stray-source 120
run media-install 3000
md stray-address 300
md release-proof 300
md stale-fence 120
md evidence 600
