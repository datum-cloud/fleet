#!/usr/bin/env bash
# Delete only this run's exact containers, kind clusters and provider image tag.
# Default: only after evidence/summary.json records passed acceptance (verified
# cleanup and release), then remove private credentials and keep evidence.
# --discard-unknown is an explicit human decision to abandon an Unknown run; it
# deletes the same exact resources, keeps all private state, and never claims release.
set -euo pipefail
cd "$(dirname "$0")/../.."
umask 077
state=${1:?private run state directory required}
mode=${2:-passed}
[[ $mode == passed || $mode == --discard-unknown ]] || { echo 'usage: teardown.sh STATE [--discard-unknown]' >&2; exit 64; }
prefix=$(cat "$state/prefix")
[[ $prefix =~ ^fleet-mp-[a-f0-9]{8}$ ]] || { echo 'unexpected prefix' >&2; exit 1; }
export DOCKER_CONTEXT=${FLEET_MULTIPLANE_DOCKER_CONTEXT:-colima-fleet-acceptance-tcg} KIND_EXPERIMENTAL_PROVIDER=docker
if [[ $mode == passed ]]; then
 python3 -c 'import json,sys; sys.exit(json.load(open(sys.argv[1])).get("acceptance")!="passed")' "$state/evidence/summary.json" 2>/dev/null || {
  echo 'No passed acceptance summary; refusing teardown. Use --discard-unknown only after inspection.' >&2; exit 1; }
fi
hub="$prefix-hub-control-plane"
if docker inspect "$hub" >/dev/null 2>&1; then
 [[ $(docker inspect -f '{{.State.Paused}}' "$hub") != true ]] || docker unpause "$hub" >/dev/null
 if [[ $(docker inspect -f '{{.State.Running}}' "$hub") == true && -f $state/sysctl.json ]] && ! grep -q '"restored": true' "$state/sysctl.json"; then
  python3 hack/multiplane/sysctl.py "$state" restore
 fi
fi
for cluster in hub cell platform; do
 if docker inspect "$prefix-$cluster-control-plane" >/dev/null 2>&1; then
  # A private throwaway kubeconfig keeps kind from editing any user kubeconfig.
  kind delete cluster --name "$prefix-$cluster" --kubeconfig "$state/kind-delete.kubeconfig"
 fi
done
for suffix in registry vm consumer-a consumer-b provider consumer-a-etcd consumer-b-etcd provider-etcd; do
 if docker inspect "$prefix-$suffix" >/dev/null 2>&1; then docker rm -f -v "$prefix-$suffix" >/dev/null; fi
done
image_tag_removed=true
pushed=()
if [[ -f $state/gitops-environment.json ]]; then
 port=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["registryPort"])' "$state/gitops-environment.json")
 # Redfish mode also built and pushed the lab BMC image.
 pushed=("127.0.0.1:$port/fleet-site:$prefix" "127.0.0.1:$port/fleet-bmc:$prefix")
fi
for image in "$prefix-site-image" "$prefix-provider-image" "$prefix-bmc-image" ${pushed[@]+"${pushed[@]}"}; do
 docker image rm "$image" >/dev/null 2>&1 || true
 if docker image inspect "$image" >/dev/null 2>&1; then image_tag_removed=false; fi
done
remaining=$(docker ps -a --filter "name=^$prefix-" --format '{{.Names}}')
[[ -z $remaining ]] || { echo "owned containers remain: $remaining" >&2; exit 1; }
rm -f "$state/kind-delete.kubeconfig"
if [[ $mode == passed ]]; then
 # Credentials and raw journals are no longer needed once the run has passed.
 rm -rf "$state/provider" "$state/consumer-a" "$state/consumer-b" "$state/provider-private" "$state/redfish"
 rm -f "$state"/*.yaml "$state"/*-hub.json "$state"/private.json "$state"/cell-worker*.json "$state"/federation-cell.json "$state"/*.token "$state"/*-kubeconfig.json \
  "$state"/status-fault.json "$state"/acceptance.json "$state"/fleet-multiplane "$state"/fleet-multiplane-host
 rm -rf "$state/site-bin" "$state/kro-chart" "$state/flux-tools/flux" "$state"/flux-tools/*.tar.gz
fi
[[ ! -f $state/sysctl.json ]] || cp "$state/sysctl.json" "$state/evidence/sysctl.json"
python3 - "$state" "$prefix" "$mode" "$image_tag_removed" <<'PY'
import datetime,json,pathlib,sys
s,prefix,mode=pathlib.Path(sys.argv[1]),sys.argv[2],sys.argv[3]
record={'observedAt':datetime.datetime.now(datetime.timezone.utc).isoformat(),'prefix':prefix,'deleted':'exact prefix containers (including the run registry) and platform, hub and cell kind clusters', 'providerImageTagRemoved':sys.argv[4]=='true', 'volumesVerifiedAbsent':False,
 'mode':'after passed acceptance' if mode=='passed' else 'discarded Unknown run by explicit request; not release evidence',
 'privateCredentialsRemoved':mode=='passed','remainingContainers':[]}
(s/'evidence'/'teardown.json').write_text(json.dumps(record,indent=2)+'\n')
PY
printf 'teardown: removed %s resources (%s)\n' "$prefix" "$mode"
