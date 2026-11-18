#!/usr/bin/env bash
# Creates only fresh disposable resources; retains failed state for recovery.
set -euo pipefail
cd "$(dirname "$0")/../.."
umask 077
[[ $# == 0 ]] || { echo 'This local harness accepts no arguments' >&2; exit 1; }
export KIND_EXPERIMENTAL_PROVIDER=docker
accelerator=${FLEET_LOCAL_VM_ACCEL:-kvm}
case "$accelerator" in
 kvm|tcg) ;;
 *) echo 'FLEET_LOCAL_VM_ACCEL must be kvm or tcg' >&2; exit 1;;
esac
case $(docker context inspect --format '{{.Endpoints.docker.Host}}') in
 unix://*) ;;
 *) echo 'Local Unix Docker endpoint required; remote runtimes are not authorized' >&2; exit 1;;
esac
case $(docker info --format '{{.Architecture}}') in
 x86_64|amd64) runtime_arch=amd64 ;;
 aarch64|arm64) runtime_arch=arm64 ;;
 *) echo 'Unsupported Docker runtime architecture' >&2; exit 1;;
esac
[[ $accelerator == tcg || $runtime_arch == amd64 ]] || { echo 'KVM requires an amd64 runtime for this guest' >&2; exit 1; }
run_id="fleet-local-$(python3 -c 'import uuid; print(uuid.uuid4().hex[:12])')"
evidence=$(mktemp -d /tmp/fleet-local-evidence.XXXXXX)
export KUBECONFIG="$evidence/kubeconfig"
printf '%s\n' "$run_id" > "$evidence/resource-name"
printf '%s\n' "$accelerator" > "$evidence/accelerator"
printf '%s\n' "$runtime_arch" > "$evidence/runtime-architecture"
printf 'Evidence: %s\n' "$evidence"
trap 'echo "Resources retained for inspection: $run_id; evidence: $evidence"' EXIT
python3 - "$evidence/source-SHA256SUMS" <<'PYHASH'
import hashlib,pathlib,sys
paths=[*pathlib.Path('internal/localvm').glob('*.go'),*pathlib.Path('cmd/fleet-local-vm').glob('*.go'),*pathlib.Path('hack/local-vm').glob('*')]
with open(sys.argv[1],'w') as out:
 for p in sorted(paths):
  if p.is_file(): out.write(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+str(p)+'\n')
PYHASH
CGO_ENABLED=0 GOOS=linux GOARCH="$runtime_arch" go build -o "$evidence/fleet-local-vm" ./cmd/fleet-local-vm
docker build --target "$accelerator" -t fleet-local-install -f hack/local-vm/Containerfile hack/local-vm
kind create cluster --name "$run_id" --kubeconfig "$KUBECONFIG" --image kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f --wait 120s
kubectl apply -f config/base/crd/metal.datumapis.com.yaml
kubectl wait --for=condition=Established crd --all --timeout=60s
for ns in fleet-system-private fleet-provider fleet-project-a fleet-project-b; do kubectl create namespace "$ns"; done
docker run -d --name "$run_id" --network kind --privileged --user root -e "FLEET_LOCAL_VM_ACCEL=$accelerator" --entrypoint /usr/local/bin/fleet-start-libvirt fleet-local-install
# No host filesystem mounts, host network, or host libvirt socket.
docker exec "$run_id" mkdir -p /run/fleet
binary_digest=$(python3 - "$evidence/fleet-local-vm" <<'PY'
import hashlib,sys
print(hashlib.sha256(open(sys.argv[1],'rb').read()).hexdigest())
PY
)
printf '%s  fleet-local-vm\n' "$binary_digest" > "$evidence/binary-SHA256SUMS"
docker cp "$evidence/fleet-local-vm" "$run_id:/usr/local/bin/fleet-local-vm.pending"
printf '%s  /usr/local/bin/fleet-local-vm.pending\n' "$binary_digest" | docker exec -i "$run_id" sha256sum -c -
docker exec "$run_id" mv /usr/local/bin/fleet-local-vm.pending /usr/local/bin/fleet-local-vm
kind get kubeconfig --name "$run_id" --internal > "$evidence/internal-kubeconfig"
docker cp "$evidence/internal-kubeconfig" "$run_id:/run/fleet/kubeconfig"
docker exec -d "$run_id" fleet-local-vm boot-service
image=$(docker exec "$run_id" fleet-local-vm image)
docker cp "$run_id:/opt/fleet-local/artifacts/SHA256SUMS" "$evidence/SHA256SUMS"
python3 - "$image" > "$evidence/fixtures.yaml" <<'PY'
import sys
s=open('config/standalone/fixtures.yaml').read()
s=s.replace('min: 24','min: 2').replace('min: 128Gi','min: 2Gi')
s=s.replace('debian-12@sha256:9f4ad8d8b8a6f6fc64d2cf2c7e70b9184b74f089adf8c36d84f4fcb99892f3cb',sys.argv[1])
print(s)
PY
kubectl apply -f "$evidence/fixtures.yaml"
docker exec "$run_id" fleet-local-vm allocate | tee -a "$evidence/phases.log"
set +e
docker exec "$run_id" fleet-local-vm install-crash >> "$evidence/phases.log" 2>&1
crash_status=$?
set -e
[[ $crash_status == 75 ]] || { echo "Expected post-effect crash 75; got $crash_status"; exit 1; }
printf "post-effect process crash verified (75)\n" >> "$evidence/phases.log"
for phase in install install boot-denials reinstall reinstall boot-denials release boot-denials evidence; do
 docker exec "$run_id" fleet-local-vm "$phase" | tee -a "$evidence/phases.log"
done
docker cp "$run_id:/var/lib/fleet-local" "$evidence/provider"
docker exec "$run_id" virsh list --all --name > "$evidence/domains-after.txt"
docker image inspect fleet-local-install --format '{{.Id}}' > "$evidence/image-id"
# Retain receipts but omit fence tokens and all kubeconfigs from shareable evidence.
python3 - "$evidence/provider/journal.json" <<'PY'
import json,sys,pathlib
p=sys.argv[1];j=json.load(open(p));j.pop('fence',None)
tokens=[r.get('bootToken','') for r in j['operations'].values() if r.get('bootToken')]
for r in j['operations'].values():
 r['request'].pop('fenceToken',None)
 r.pop('bootToken',None)
json.dump(j,open(p,'w'),indent=2)
root=pathlib.Path(p).parent
(root/'network-private.xml').unlink(missing_ok=True)
for file in root.rglob('*'):
 if file.is_file() and any(token.encode() in file.read_bytes() for token in tokens):
  raise SystemExit('credential found in exported evidence')
assert j['bootMode']=='pxe'
for r in j['operations'].values():
 if r['request']['kind']!='install': continue
 import hashlib
 d=root/hashlib.sha256(r['request']['id'].encode()).hexdigest()
 installer=(d/'installer.xml').read_text(); disk=(d/'disk.xml').read_text()
 assert '<kernel>' not in installer and '<initrd>' not in installer and "boot dev='network'" in installer
 assert '<interface' not in disk and '<kernel>' not in disk and '<initrd>' not in disk
 assert r['bootClosed']
 for name in ['boot.ipxe','kernel','installer.gz']: assert 'write-complete '+name+' bytes=' in (d/'boot-delivery.log').read_text()
 assert '52:54:00:77:00:02' in (d/'dhcp-leases.txt').read_text()
 assert 'FLEET-DISK-BOOT:'+r['receipt'] in (d/'disk.log').read_text()
 assert not (d/'disk.raw').exists()
PY
rm -f "$evidence/provider/network-private.xml"
docker rm -f "$run_id"
kind delete cluster --name "$run_id"
rm -f "$evidence/kubeconfig" "$evidence/internal-kubeconfig" "$evidence/fleet-local-vm"
trap - EXIT
printf 'PASS: real local installation, retry, reinstall, cleanup and reuse; evidence %s\n' "$evidence"
