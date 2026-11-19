#!/usr/bin/env bash
# Site image publication and post-reconcile checks for the selected cell.
#   site.sh push STATE    push the built site image to the run registry; record its digest
#   site.sh check STATE   after Flux applied the site layers: shared services Available
#                         and Flux-owned, dispatcher ready, volume Retain, no Job
# Installs nothing: kro, the site base, the shared services, the RGD and the
# HandoffDispatcher are reconciled by the cell's Flux (config/multiplane/clusters).
set -euo pipefail
umask 077
step=${1:?step required}
state=${2:?private state directory required}
prefix=$(cat "$state/prefix")
[[ $prefix =~ ^fleet-mp-[a-f0-9]{8}$ ]] || { echo "unexpected prefix" >&2; exit 1; }
export DOCKER_CONTEXT=${FLEET_MULTIPLANE_DOCKER_CONTEXT:-colima-fleet-acceptance-tcg}
cell="$state/cell-admin.yaml"
provider=$(cat "$state/site-provider" 2>/dev/null || echo libvirt)
deployments=(fleet-site-runtime fleet-site-boot fleet-site-observer)
[[ $provider != redfish ]] || deployments+=(fleet-site-bmc)
k() { kubectl --kubeconfig "$cell" --request-timeout=30s "$@"; }

case "$step" in
 push)
  port=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["registryPort"])' "$state/gitops-environment.json")
  docker tag "$prefix-site-image" "127.0.0.1:$port/fleet-site:$prefix" >/dev/null
  docker push "127.0.0.1:$port/fleet-site:$prefix" >/dev/null
  digest=$(docker image inspect "127.0.0.1:$port/fleet-site:$prefix" --format '{{range .RepoDigests}}{{println .}}{{end}}' | grep "^127.0.0.1:$port/fleet-site@" | cut -d@ -f2)
  [[ $digest =~ ^sha256:[a-f0-9]{64}$ ]] || { echo "no registry digest for the site image" >&2; exit 1; }
  python3 - "$state" "$prefix" "$digest" <<'PY'
import json, pathlib, sys
s, prefix, digest = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
env = json.loads((s / 'gitops-environment.json').read_text())
env['siteImage'] = '%s-registry:5000/fleet-site@%s' % (prefix, digest)
env['siteImageDigest'] = digest
(s / 'gitops-environment.json').write_text(json.dumps(env, indent=2) + '\n')
PY
  printf 'site image: pushed to the run registry as fleet-site@%s; the cell pulls it by digest\n' "$digest"
  if [[ $provider == redfish ]]; then
   docker tag "$prefix-bmc-image" "127.0.0.1:$port/fleet-bmc:$prefix" >/dev/null
   docker push "127.0.0.1:$port/fleet-bmc:$prefix" >/dev/null
   bmc=$(docker image inspect "127.0.0.1:$port/fleet-bmc:$prefix" --format '{{range .RepoDigests}}{{println .}}{{end}}' | grep "^127.0.0.1:$port/fleet-bmc@" | cut -d@ -f2)
   [[ $bmc =~ ^sha256:[a-f0-9]{64}$ ]] || { echo "no registry digest for the BMC image" >&2; exit 1; }
   python3 - "$state" "$prefix" "$bmc" <<'PY'
import json, pathlib, sys
s, prefix, digest = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
env = json.loads((s / 'gitops-environment.json').read_text())
env['bmcImage'] = '%s-registry:5000/fleet-bmc@%s' % (prefix, digest)
env['bmcImageDigest'] = digest
(s / 'gitops-environment.json').write_text(json.dumps(env, indent=2) + '\n')
PY
   printf 'bmc image: pushed the DMTF emulator lab BMC as fleet-bmc@%s\n' "$bmc"
  fi
  ;;
 check)
  for d in "${deployments[@]}"; do
   for _ in $(seq 1 120); do k -n fleet-site get deployment "$d" >/dev/null 2>&1 && break; sleep 2; done
   k -n fleet-site wait --for=condition=Available "deployment/$d" --timeout=300s >/dev/null
  done
  k -n fleet-site wait --for=jsonpath='{.status.runtimeAvailable}'=1 handoffdispatcher/dispatcher --timeout=120s >/dev/null
  [[ -z $(k -n fleet-site get jobs -o name) ]] || { echo 'worker Jobs exist before dispatch' >&2; exit 1; }
  pv=$(k -n fleet-site get pvc fleet-site-state -o jsonpath='{.spec.volumeName}')
  policy=$(k get pv "$pv" -o jsonpath='{.spec.persistentVolumeReclaimPolicy}')
  [[ $policy == Retain ]] || { echo "site volume reclaim policy $policy" >&2; exit 1; }
  want=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["siteImage"])' "$state/gitops-environment.json")
  if [[ $provider == redfish ]]; then
   bmc=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["bmcImage"])' "$state/gitops-environment.json")
   [[ $(k -n fleet-site get deployment fleet-site-bmc -o jsonpath='{.spec.template.spec.containers[0].image}') == "$bmc" ]] || { echo 'BMC does not run the published digest' >&2; exit 1; }
   [[ $(k -n fleet-site get deployment fleet-site-bmc -o jsonpath='{.metadata.labels.kustomize\.toolkit\.fluxcd\.io/name}') == fleet-site-services ]] || { echo 'BMC is not owned by the Flux services layer' >&2; exit 1; }
  fi
  for d in fleet-site-runtime fleet-site-boot fleet-site-observer; do
   image=$(k -n fleet-site get deployment "$d" -o jsonpath='{.spec.template.spec.containers[0].image}')
   [[ $image == "$want" ]] || { echo "$d runs $image, not the published digest" >&2; exit 1; }
   owner=$(k -n fleet-site get deployment "$d" -o jsonpath='{.metadata.labels.kustomize\.toolkit\.fluxcd\.io/name}')
   [[ $owner == fleet-site-services ]] || { echo "$d is not owned by the Flux services layer" >&2; exit 1; }
  done
  [[ $(k -n fleet-site get handoffdispatcher dispatcher -o jsonpath='{.status.workerImage}') == "$want" ]] || { echo 'dispatcher worker image is not the services image' >&2; exit 1; }
  [[ $(k -n fleet-site get service fleet-site-boot -o jsonpath='{.spec.clusterIP}') == "$(python3 -c 'import sys; sys.path.insert(0, "hack/multiplane"); import flux; print(flux.BOOT_SERVICE_IP)')" ]] || { echo 'boot Service is not at its fixed ClusterIP' >&2; exit 1; }
  printf 'site: Flux-owned runtime, boot service and observer Available from the digest-pinned image; boot Service at its fixed ClusterIP; dispatcher ready; volume Retain; dispatch disabled\n'
  ;;
 *) echo "unknown step: $step" >&2; exit 64 ;;
esac
