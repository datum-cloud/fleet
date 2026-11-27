#!/usr/bin/env bash
# One-command local Milo/Karmada/PXE acceptance. Creates only fresh resources with
# one fleet-mp-<random> prefix. Failures stop (never delete) this run's containers
# and retain exact state as Unknown custody. See docs/operations/multiplane.md.
#
# Exit codes: 0 passed, 1 failed, 2 preflight failed, 3 blocked by implementation
# readiness, 4 bounded wait expired, 64 usage, 128 plus the signal number when
# interrupted. Only a run that reached its last line records passed.
set -euo pipefail
cd "$(dirname "$0")/../.."
umask 077
usage() { echo 'usage: hack/multiplane/run.sh [--redfish [--synced-dhcp | --virtual-media]] [--preflight-only] [--bootstrap] [--keep] [--leave-running] [--skip-readiness-tests]' >&2; exit 64; }
preflight_only=0 bootstrap=() keep=0 leave=0 skip_tests=() provider=libvirt lab_dhcp=static media=0
for arg in "$@"; do
 case "$arg" in
  --preflight-only) preflight_only=1 ;;
  --bootstrap) bootstrap=(--bootstrap) ;;
  --keep) keep=1 ;;
  --leave-running) leave=1 ;;
  --skip-readiness-tests) skip_tests=(--skip-tests) ;;
  # Redfish mode: workers drive lab VMs only through the DMTF Redfish emulator
  # and its lab adapter (docs/operations/redfish-lab.md).
  --redfish) provider=redfish ;;
  # Synchronized DHCP: the site's Kea, fed by Fleet's reservation feed, is the
  # lab bridge's only DHCP server, and boot addresses come from IPAM
  # (milo-os/ipam) aggregated by the provider Milo API
  # (docs/operations/redfish-lab.md).
  --synced-dhcp) lab_dhcp=synced ;;
  # Virtual media: the Redfish lab's suite that installs over Redfish
  # VirtualMedia and the site's media service (docs/operations/redfish-lab.md).
  --virtual-media) media=1 ;;
  *) usage ;;
 esac
done
[[ $lab_dhcp == static || $provider == redfish ]] || usage
[[ $media == 0 || ($provider == redfish && $lab_dhcp == static) ]] || usage
# IPAM runs with synchronized DHCP, for boot addresses, and in the virtual
# media suite, for each install's report address.
lab_ipam=0
[[ $lab_dhcp == static && $media == 0 ]] || lab_ipam=1
# Run parameters (docs/operations/multiplane.md#ci-mode). The defaults are the
# task-owned Colima profile: arm64 runtime, TCG guest. Preflight validates them
# before any resource exists.
export FLEET_MULTIPLANE_RUNTIME=${FLEET_MULTIPLANE_RUNTIME:-colima} FLEET_MULTIPLANE_DOCKER_CONTEXT=${FLEET_MULTIPLANE_DOCKER_CONTEXT:-colima-fleet-acceptance-tcg}
export FLEET_MULTIPLANE_ARCH=${FLEET_MULTIPLANE_ARCH:-arm64} FLEET_MULTIPLANE_ACCEL=${FLEET_MULTIPLANE_ACCEL:-tcg}
# Scenario group: all by default; CI shards each suite into flux and release.
export FLEET_MULTIPLANE_GROUP=${FLEET_MULTIPLANE_GROUP:-all}
export DOCKER_CONTEXT=$FLEET_MULTIPLANE_DOCKER_CONTEXT KIND_EXPERIMENTAL_PROVIDER=docker FLEET_SITE_PROVIDER=$provider FLEET_LAB_DHCP=$lab_dhcp FLEET_LAB_IPAM=$lab_ipam
harness=(python3 hack/multiplane/harness.py)
state=$(mktemp -d "${FLEET_MULTIPLANE_STATE_ROOT:-/tmp}/fleet-multiplane.XXXXXX")
mkdir -m 0700 "$state/evidence"
printf '%s\n' "$provider" > "$state/site-provider"
printf '%s\n' "$lab_dhcp" > "$state/lab-dhcp"
printf 'Private run state: %s\nShareable evidence: %s/evidence\nSite provider: %s\nLab DHCP: %s\nScenario group: %s\n' "$state" "$state" "$provider" "$lab_dhcp" "$FLEET_MULTIPLANE_GROUP"

finish() {
 code=$1
 trap - EXIT
 if [[ $code -ne 0 && -f $state/prefix ]]; then
  if [[ $leave == 1 ]]; then
   echo "Leaving this run's containers running for diagnosis. Stop only them with: hack/multiplane/stop.sh $state"
  else
   bash hack/multiplane/stop.sh "$state" || echo "stop.sh failed; inspect $(cat "$state/prefix")-* containers" >&2
  fi
  echo "Execution and custody are Unknown. Retained private state: $state. Discard only after inspection: hack/multiplane/teardown.sh $state --discard-unknown"
 fi
 [[ $code -eq 0 ]] || "${harness[@]}" scan --state "$state" >/dev/null 2>&1 || true
 # The result decides the exit code: exit 0 without the completion marker is
 # an interrupted run.
 "${harness[@]}" result --state "$state" --exit-code "$code" && exit 0
 exit $?
}
# Every descendant. Parents are signalled before their children, so a phase
# stops its own command and records the interruption; the children are
# listed first, so none is missed when its parent exits.
stop_tree() {
 local child children
 children=$(pgrep -P "$1" || true)
 [[ $1 == "$$" ]] || kill -TERM "$1" 2>/dev/null || true
 for child in $children; do stop_tree "$child"; done
}
interrupt() {
 trap '' TERM INT HUP
 stop_tree $$
 wait || true
 finish "$1"
}
trap 'finish $?' EXIT
trap 'interrupt 143' TERM
trap 'interrupt 130' INT
trap 'interrupt 129' HUP
# Phases and acceptance scripts run in the background and are waited for, so
# a signal reaches the traps at once rather than after the current phase.
ph() { "${harness[@]}" phase --state "$state" --name "$1" --timeout "$2" -- "${@:3}" & wait $!; }

"${harness[@]}" preflight --state "$state" --root . "${bootstrap[@]}" "${skip_tests[@]}" & wait $!
if [[ $preflight_only == 1 ]]; then
 : > "$state/preflight-completed"
 exit 0
fi

prefix="fleet-mp-$(openssl rand -hex 4)"
printf '%s\n' "$prefix" > "$state/prefix"
# Source identity covers uncommitted files as well as tracked files.
python3 - "$state" <<'PY'
import hashlib,pathlib,sys
paths=[]
for name in ('internal','cmd/fleet-multiplane','hack/multiplane','hack/local-vm','hack/redfish-lab','config/multiplane','config/site','config/base/crd'):
 paths += [p for p in pathlib.Path(name).rglob('*') if p.is_file() and '__pycache__' not in p.parts and p.suffix not in ('.pyc', '.pyo')]
paths += [pathlib.Path(name) for name in ('go.mod', 'go.sum')]
(pathlib.Path(sys.argv[1])/'source-SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+str(p)+'\n' for p in sorted(paths)))
PY
flux=(python3 hack/multiplane/flux.py)
# The site binary is built into its own directory: it is the only build context
# input besides hack/local-vm, so no private state can enter the image.
ph build 600 sh -c 'mkdir -m 0700 "$1/site-bin" && go build -o "$1/fleet-multiplane-host" ./cmd/fleet-multiplane && CGO_ENABLED=0 GOOS=linux GOARCH="$FLEET_MULTIPLANE_ARCH" go build -o "$1/site-bin/fleet-multiplane" ./cmd/fleet-multiplane && CGO_ENABLED=0 GOOS=linux GOARCH="$FLEET_MULTIPLANE_ARCH" go build -o "$1/site-bin/fleet-dhcp-sync" ./cmd/fleet-dhcp-sync && cp "$1/site-bin/fleet-multiplane" "$1/fleet-multiplane"' sh "$state"
ph site-image 2400 docker build --target site --build-context "fleetbin=$state/site-bin" -t "$prefix-site-image" -f hack/local-vm/Containerfile hack/local-vm
ph site-image-verify 120 sh -c '
 sum=$(shasum -a 256 "$2/site-bin/fleet-multiplane" | cut -d" " -f1)
 sync=$(shasum -a 256 "$2/site-bin/fleet-dhcp-sync" | cut -d" " -f1)
 printf "%s  /usr/local/bin/fleet-multiplane\n%s  /usr/local/bin/fleet-dhcp-sync\n" "$sum" "$sync" | docker run --rm -i --entrypoint sha256sum "$1-site-image" -c - >/dev/null
 docker run --rm -e "FLEET_LOCAL_VM_ACCEL=$FLEET_MULTIPLANE_ACCEL" --entrypoint fleet-multiplane "$1-site-image" image > "$2/image"
 echo "site image: checksum-verified executable; artifact reference recorded"' sh "$prefix" "$state"
if [[ $provider == redfish ]]; then
 # The unmodified upstream emulator, verified before extraction, plus the
 # lab-only adapter; its self-test runs the real emulator with a fake virsh.
 ph bmc-upstream 300 sh -c 'bash hack/redfish-lab/upstream.sh "$1/redfish-upstream" > "$1/evidence/redfish-upstream.json" && echo "bmc-upstream: DMTF Redfish-Interface-Emulator 1.2.2 archive verified"' sh "$state"
 ph bmc-image 1800 docker build --build-context "upstream=$state/redfish-upstream/src/Redfish-Interface-Emulator-1.2.2" -t "$prefix-bmc-image" -f hack/redfish-lab/Containerfile hack/redfish-lab
 ph bmc-self-test 300 sh -c 'docker run --rm --network none --entrypoint python3 "$1-bmc-image" /opt/fleet-lab/test_adapter.py >/dev/null 2>&1 && echo "bmc-self-test: upstream emulator and lab adapter self-test passed"' sh "$prefix"
fi
ph flux-tools 300 "${flux[@]}" tools "$state"
docker network inspect kind >/dev/null 2>&1 || docker network create kind >/dev/null
# Disposable infrastructure only: registry and three kind clusters, no services.
ph topology 900 bash hack/multiplane/topology.sh "$state"
# Where lab Pods reach this host's central media service, and the network
# their connections arrive from: Colima's host gateway, which arrives on
# loopback, or on a CI runner the kind network's gateway and subnet.
if [[ $FLEET_MULTIPLANE_RUNTIME == ci ]]; then
 docker network inspect kind -f '{{range .IPAM.Config}}{{.Gateway}} {{.Subnet}}{{"\n"}}{{end}}' |
  awk -v host="$state/media-host" -v clients="$state/media-clients" '$1 ~ /^[0-9.]+$/ {print $1 > host; print $2 > clients; exit}'
 [[ -s $state/media-host && -s $state/media-clients ]] || { echo 'the kind network has no IPv4 gateway' >&2; exit 1; }
else
 printf '192.168.5.2\n' > "$state/media-host"
 printf '127.0.0.0/8\n' > "$state/media-clients"
fi
ph site-image-push 600 bash hack/multiplane/site.sh push "$state"
# Revision 1: platform services only. The bootstrap boundary applies each
# cluster's Flux components and sync objects once; Flux then owns them too.
ph flux-source 300 "${flux[@]}" publish "$state" --revision 1 --purpose 'platform services'
for c in platform hub cell; do ph "flux-bootstrap-$c" 600 "${flux[@]}" bootstrap "$state" --cluster "$c"; done
# Waiting from the moment of bootstrap records dependency gating as it happens.
# The member registration becomes Ready only after credential enrollment.
ph flux-platform 1800 "${flux[@]}" wait "$state" --cluster cell --cluster hub --observe hub --observe cell --except karmada-system/fleet-karmada-members --timeout 1500
ph milo-bootstrap 1200 python3 hack/multiplane/milo.py "$state" "$prefix"
ph flux-milo 900 "${flux[@]}" wait "$state" --cluster platform --timeout 600
ph federation-enrollment 600 python3 hack/multiplane/roles.py "$state"
# Redfish mode enrolls the per-Device BMC credentials and the lab BMC's TLS
# key first: fixture discovery reports that key's digest.
[[ $provider != redfish ]] || ph redfish-enroll 120 python3 hack/multiplane/redfish_steps.py enroll "$state"
python3 - "$state" "$provider" "$lab_ipam" <<'PY'
import json,pathlib,sys
s=pathlib.Path(sys.argv[1]).resolve()
enrollment=json.loads((s/'redfish'/'fixture-enrollment.json').read_text() if sys.argv[2]=='redfish' else pathlib.Path('config/site/base/enrollment.json').read_text())
c={'State':str(s),'HubKubeconfig':str(s/'management-hub.json'),'Cell':(s/'prefix').read_text().strip()+'-cell','Image':(s/'image').read_text().strip(),'Enrollment':enrollment,
   # The central media service, as lab Pods and guests reach the workstation.
   'MediaService':'https://%s:28443'%(s/'media-host').read_text().strip()}
if sys.argv[3]=='1':c['BootAddressClass']='lab-provisioning'
(s/'acceptance.json').write_text(json.dumps(c))
PY
ph seed 600 "$state/fleet-multiplane-host" seed --config "$state/acceptance.json"
ph coordinator 120 bash hack/multiplane/coordinator.sh start "$state"
# Revision 2: the Flux-owned shared site services and the HandoffDispatcher
# instance, dispatch disabled.
ph flux-site 1200 "${flux[@]}" publish "$state" --revision 2 --site off --purpose 'shared site services and dispatcher, dispatch disabled' --cluster cell --cluster hub --cluster platform --timeout 900
ph site-install 900 bash hack/multiplane/site.sh check "$state"
ph flux-verify 300 "${flux[@]}" verify "$state"
if [[ $media == 1 ]]; then
 bash hack/multiplane/acceptance-media.sh "$state" & wait $!
elif [[ $provider == redfish ]]; then
 bash hack/multiplane/acceptance-redfish.sh "$state" & wait $!
else
 bash hack/multiplane/acceptance.sh "$state" & wait $!
fi
ph secret-scan 120 "${harness[@]}" scan --state "$state"
if [[ $keep == 1 ]]; then
 ph stop 300 bash hack/multiplane/stop.sh "$state"
else
 ph teardown 900 bash hack/multiplane/teardown.sh "$state"
fi
# The last act of a completed run: only now may the result record passed.
: > "$state/completed"
printf 'Local topology/PXE acceptance (%s provider) passed. Production authority and physical hardware remain unqualified.\n' "$provider"
