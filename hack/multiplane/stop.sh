#!/usr/bin/env bash
# Stop only this run's exact containers. Preserve volumes and Unknown custody.
set -euo pipefail
state=${1:?private run state directory required}
prefix=$(cat "$state/prefix")
[[ $prefix =~ ^fleet-mp-[a-f0-9]{8}$ ]] || exit 1
# The coordinator is a workstation process: stop it before its clusters.
bash hack/multiplane/coordinator.sh stop "$state" >/dev/null
export DOCKER_CONTEXT=${FLEET_MULTIPLANE_DOCKER_CONTEXT:-colima-fleet-acceptance-tcg}
# The cell node holds the site runtime, journal volume, kro and worker Jobs; the
# platform node holds the Milo APIs and their etcd volumes; the registry holds
# the source revisions. Earlier-layout container names are kept for old runs.
for suffix in vm consumer-a consumer-b provider consumer-a-etcd consumer-b-etcd provider-etcd cell-control-plane platform-control-plane registry; do
 name="$prefix-$suffix"
 if docker inspect "$name" >/dev/null 2>&1; then
  if [[ $(docker inspect -f '{{.State.Paused}}' "$name") == true ]]; then docker unpause "$name" >/dev/null; fi
  docker stop --timeout 30 "$name" >/dev/null
 fi
done
if docker inspect "$prefix-hub-control-plane" >/dev/null 2>&1; then
 if [[ $(docker inspect -f '{{.State.Running}}' "$prefix-hub-control-plane") == true ]]; then
  if [[ $(docker inspect -f '{{.State.Paused}}' "$prefix-hub-control-plane") == true ]]; then docker unpause "$prefix-hub-control-plane" >/dev/null; fi
  if [[ -f "$state/sysctl.json" ]]; then python3 hack/multiplane/sysctl.py "$state" restore; fi
  docker stop --timeout 30 "$prefix-hub-control-plane" >/dev/null
 else
  echo 'Hub already stopped; inspect sysctl.json for prior restoration evidence.'
 fi
fi
printf 'Stopped %s; disks, journals and custody retained. Stopping does not prove release.\n' "$prefix"
