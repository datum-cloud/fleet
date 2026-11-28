#!/usr/bin/env bash
# Create this run's disposable infrastructure: the local OCI registry and three
# kind clusters (platform for Milo, hub for management/Karmada, cell for the
# site). Installs no service: every service is reconciled by each cluster's
# Flux from the published source (hack/multiplane/flux.py, docs/operations/gitops.md).
set -euo pipefail
umask 077
state=${1:?private state directory required}
prefix=$(cat "$state/prefix")
[[ $prefix =~ ^fleet-mp-[a-f0-9]{8}$ ]] || { echo "unexpected prefix" >&2; exit 1; }
export DOCKER_CONTEXT=${FLEET_MULTIPLANE_DOCKER_CONTEXT:-colima-fleet-acceptance-tcg} KIND_EXPERIMENTAL_PROVIDER=docker
node='kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f'
registry_image=$(python3 -c 'import sys; sys.path.insert(0, "hack/multiplane"); import flux; print(flux.REGISTRY_IMAGE)')
# kind resolves host ports before it starts a node, so a port can be taken in
# between; kind_create.py retries only that failure with fresh ports.
kind_create() { python3 hack/multiplane/kind_create.py --name "$1" --template "$2" --config "${2%.template}" --ports "$3" --kubeconfig "$4" --image "$node"; }
port_of() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$1" "$2"; }

# The source and image registry: plain HTTP on the kind network (local seam).
# Its memory limit also counts the page cache of the layers it writes, so a
# fast runner's push of the site image needs room beyond the process itself.
# Docker assigns its host port as it binds it, so no other bind can take it.
docker run -d --name "$prefix-registry" --network kind --memory 512m --memory-swap 512m --cpus 0.5 \
 -p "127.0.0.1::5000" "$registry_image" >/dev/null
reg_port=$(docker port "$prefix-registry" 5000/tcp | sed -n 's/^127\.0\.0\.1:\([0-9][0-9]*\)$/\1/p' | head -1)
[[ $reg_port =~ ^[0-9]+$ ]] || { echo 'registry has no published host port' >&2; exit 1; }
for _ in $(seq 1 30); do curl -fsS "http://127.0.0.1:$reg_port/v2/" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "http://127.0.0.1:$reg_port/v2/" >/dev/null || { echo 'registry did not start' >&2; exit 1; }

# Platform: three Milo project APIs, each published on its own NodePort.
cat > "$state/platform-kind.yaml.template" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - {containerPort: 30441, hostPort: @port:provider@, listenAddress: 127.0.0.1}
      - {containerPort: 30442, hostPort: @port:consumer-a@, listenAddress: 127.0.0.1}
      - {containerPort: 30443, hostPort: @port:consumer-b@, listenAddress: 127.0.0.1}
YAML
kind_create "$prefix-platform" "$state/platform-kind.yaml.template" "$state/platform-ports.json" "$state/platform-admin.yaml"
# IPAM (synchronized DHCP or virtual media) adds its PostgreSQL beside the
# Milo APIs.
platform_memory=2300m
[[ ${FLEET_LAB_IPAM:-0} != 1 ]] || platform_memory=2800m
docker update --memory "$platform_memory" --memory-swap "$platform_memory" --cpus 2 "$prefix-platform-control-plane" >/dev/null

cat > "$state/hub-kind.yaml.template" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 32443
        hostPort: @port:karmada@
        listenAddress: 127.0.0.1
YAML
kind_create "$prefix-hub" "$state/hub-kind.yaml.template" "$state/hub-ports.json" "$state/host.yaml"
api_port=$(port_of "$state/hub-ports.json" karmada)
docker update --memory 2600m --memory-swap 2600m --cpus 2 "$prefix-hub-control-plane" >/dev/null

# The cell pulls the site image by digest from the run registry.
cat > "$state/cell-kind.yaml.template" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry]
      config_path = "/etc/containerd/certs.d"
YAML
kind_create "$prefix-cell" "$state/cell-kind.yaml.template" "$state/cell-ports.json" "$state/cell-admin.yaml"
docker exec "$prefix-cell-control-plane" mkdir -p "/etc/containerd/certs.d/$prefix-registry:5000"
printf '[host."http://%s-registry:5000"]\n  capabilities = ["pull", "resolve"]\n' "$prefix" |
 docker exec -i "$prefix-cell-control-plane" tee "/etc/containerd/certs.d/$prefix-registry:5000/hosts.toml" >/dev/null
# The cell node hosts the cell API, Flux, kro and the site, including the 3 GiB
# VM runtime Pod that runs the TCG guest.
docker update --memory 4400m --memory-swap 4400m --cpus 2 "$prefix-cell-control-plane" >/dev/null
python3 hack/multiplane/sysctl.py "$state" set

python3 - "$state" "$prefix" "$reg_port" "$api_port" <<'PY'
import json, pathlib, sys
s, prefix, reg, api = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
env = {'registryHost': prefix + '-registry', 'registryPort': int(reg), 'karmadaPort': int(api),
       'cell': prefix + '-cell', 'cellEndpoint': 'https://%s-cell-control-plane:6443' % prefix}
(s / 'gitops-environment.json').write_text(json.dumps(env, indent=2) + '\n')
PY
printf 'topology: registry, platform, hub and cell clusters created; no service installed\n'
