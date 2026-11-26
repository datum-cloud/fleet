#!/usr/bin/env bash
# Individual acceptance steps. Each runs inside a bounded harness phase, prints
# only non-secret marker lines on stdout, and exits nonzero on any violation.
# Site execution is observed and disturbed only through the cell Kubernetes API.
set -euo pipefail
umask 077
step=${1:?step required}
state=${2:?private state directory required}
prefix=$(cat "$state/prefix")
[[ $prefix =~ ^fleet-mp-[a-f0-9]{8}$ ]] || { echo "unexpected prefix" >&2; exit 1; }
export DOCKER_CONTEXT=colima-fleet-acceptance-tcg
harness=(python3 hack/multiplane/harness.py)
host="$state/fleet-multiplane-host"
ns=fleet-coordination
helpers=hack/multiplane/k8s_evidence.py

k() { kubectl --kubeconfig "$1" --request-timeout=20s "${@:2}"; }
cell() { k "$state/cell-admin.yaml" "$@"; }
platform() { k "$state/platform-admin.yaml" "$@"; }
flux() { python3 hack/multiplane/flux.py "$@"; }
# Explicit, recorded ownership transfer for fault injection: suspend the Flux
# object first so the harness is never a second concurrent writer.
suspend() { platform -n flux-system patch "kustomization/$1" --field-manager=fleet-fault-injection --type=merge -p "{\"spec\":{\"suspend\":$2}}" >/dev/null; }
site() { cell -n fleet-site "$@"; }
runtime() { site exec deploy/fleet-site-runtime -c libvirt -- "$@"; }
py() { python3 "$helpers" "$@"; }
wait_api() {
 for _ in $(seq 1 120); do
  if k "$1" get --raw /readyz >/dev/null 2>&1; then return; fi
  sleep 1
 done
 echo "API did not recover: $(basename "$1")" >&2; return 1
}
assignments=coordination.fleet.datumapis.com/role=assignment
# The site results the observer publishes: only it writes handoff status.
local_phase() { cell -n "$ns" get machineallocationhandoffs -l "$assignments" -o jsonpath='{.items[*].status.observation.result.phase}'; }
# Provider-project custody digest: the spec, phase and fence of every custody
# record, operation and allocation; the records never leave memory.
custody_digest() { "${harness[@]}" custody-digest --state "$state"; }
# The Device's site store state: its fences and journal.
store_state=$(python3 -c 'import sys; sys.path.insert(0, "hack/multiplane"); import harness; print(harness.STATE_PATH)')
# A store with no state file yet, before any assignment, digests as empty.
journal_digest() {
 runtime sh -c "cat '$store_state' 2>/dev/null || true" | python3 -c '
import hashlib,sys
print(hashlib.sha256(sys.stdin.buffer.read()).hexdigest())'
}
# The step the install has journaled once its installer boot was requested.
installer_step=installer-start
[[ $(cat "$state/site-provider" 2>/dev/null) != redfish ]] || installer_step=installer-power-on
scenario() { python3 hack/multiplane/scenarios.py "$@"; }
redfish() { [[ $(cat "$state/site-provider" 2>/dev/null) == redfish ]]; }
# Effects on servers, independent of the site journal: the hypervisor's
# running domains and its append-only QEMU logs, in both modes, and the lab
# BMC audit in Redfish mode. Mark them, then require none after the mark.
server_effects() {
 runtime sh -c 'virsh --connect "qemu:///system?socket=/run/fleet-libvirt/libvirt-sock" list --name && { ls -ln /var/log/libvirt/qemu 2>/dev/null || true; }' | python3 -c '
import hashlib,sys
print(hashlib.sha256(sys.stdin.buffer.read()).hexdigest())'
}
audit_mark() {
 server_effects > "$state/server-effects-mark"
 ! redfish || python3 hack/multiplane/redfish_steps.py audit-mark "$state"
}
audit_unchanged() {
 [[ $(server_effects) == "$(cat "$state/server-effects-mark")" ]] || { echo 'a domain started or a QEMU log changed' >&2; return 1; }
 ! redfish || python3 hack/multiplane/redfish_steps.py audit-unchanged "$state"
}
# A cell site Deployment stopped for a fault. Its Flux layer is suspended
# first, so the harness is never a second writer, and resumed to restore it.
site_stop() {
 cell -n flux-system patch kustomization/fleet-site-services --field-manager=fleet-fault-injection --type=merge -p '{"spec":{"suspend":true}}' >/dev/null
 site scale "deployment/$1" --replicas=0 >/dev/null
 for _ in $(seq 1 120); do [[ -z $(site get pods -l "app.kubernetes.io/name=$1" -o name) ]] && return; sleep 1; done
 echo "$1 did not stop" >&2; return 1
}
site_restore() {
 cell -n flux-system patch kustomization/fleet-site-services --field-manager=fleet-fault-injection --type=merge -p '{"spec":{"suspend":false}}' >/dev/null
 cell -n flux-system annotate --overwrite kustomization/fleet-site-services "reconcile.fluxcd.io/requestedAt=$(date +%s)" >/dev/null
 site wait --for=jsonpath='{.status.availableReplicas}'=1 "deployment/$1" --timeout=300s >/dev/null
}
wait_running_pod() {
 for _ in $(seq 1 "${2:-120}"); do
  uid=$(py running-pod "$state" "$1")
  [[ -z $uid ]] || { printf '%s\n' "$uid"; return; }
  sleep 1
 done
 echo "no single running worker Pod for $1" >&2; return 1
}

case "$step" in
 verify-contention)
  # Independently read public status: one Bound winner, one Pending loser.
  a=$(k "$state/consumer-a/management-project.json" -n default get machine allocation -o jsonpath='{.status.phase}/{.status.conditions[?(@.type=="Bound")].reason}')
  b=$(k "$state/consumer-b/management-project.json" -n default get machine allocation -o jsonpath='{.status.phase}/{.status.conditions[?(@.type=="Bound")].reason}')
  [[ $a == Bound/* && $b == Pending/InsufficientCapacity ]] || { echo "unexpected contention status: A=$a B=$b" >&2; exit 1; }
  printf 'contention: consumer-a %s; consumer-b %s\n' "$a" "$b"
  scenario contention "$state"
  ;;
 tenant-isolation)
  # Consumer A holds the Device and consumer B holds nothing. Nothing B's
  # side asks for reaches the site or the server.
  before=$(journal_digest)
  audit_mark
  "$host" tenant-isolation --config "$state/acceptance.json"
  [[ $before == "$(journal_digest)" ]] || { echo 'site store changed for an unheld tenant' >&2; exit 1; }
  [[ -z $(cell -n "$ns" get machineallocationhandoffs -l "$assignments" -o name) ]] || { echo 'an assignment was delivered for an unheld tenant' >&2; exit 1; }
  audit_unchanged
  ;;
 import-inert)
  scenario import-inert "$state"
  ;;
 staff-access)
  scenario staff-access "$state"
  ;;
 deadline-admit)
  # Consumer B's installation is admitted under a six-minute deadline with
  # the lab boot hook armed: the boot service stays Ready, so kro creates
  # the worker, and withholds every boot, so the installer never runs and
  # the worker waits for its receipt until the deadline. Only this admission
  # uses the short deadline. A failure here restores the default
  # coordinator and clears the hook, so a rerun starts clean.
  trap 'bash hack/multiplane/coordinator.sh restart "$state" >/dev/null; site exec deploy/fleet-site-boot -c boot -- fleet-multiplane boot-withhold clear' ERR
  site exec deploy/fleet-site-boot -c boot -- fleet-multiplane boot-withhold arm
  bash hack/multiplane/coordinator.sh restart "$state" --installation-deadline=6m >/dev/null
  "$host" admit-install-b --config "$state/acceptance.json" >/dev/null
  bash hack/multiplane/coordinator.sh restart "$state" >/dev/null
  trap 'site exec deploy/fleet-site-boot -c boot -- fleet-multiplane boot-withhold clear' ERR
  scenario wait-installer "$state" "$installer_step"
  trap - ERR
  printf 'deadline-admit: the second installation was admitted under a six-minute deadline and journaled its installer boot with the lab boot hook withholding it\n'
  ;;
 machine-delete)
  # The customer deletes Machine B mid-install. One release pass begins the
  # release, which must wait for the installation.
  k "$state/consumer-b/customer-project.json" -n default delete machine allocation --wait=false >/dev/null
  "$host" release-b --config "$state/acceptance.json" --once >/dev/null 2>&1 || true
  scenario release-waits "$state"
  ;;
 deadline-hold)
  trap 'site exec deploy/fleet-site-boot -c boot -- fleet-multiplane boot-withhold clear' ERR
  scenario deadline-hold "$state"
  # A staff recovery naming the wrong hold is refused before anything is
  # delivered or sent.
  before=$(journal_digest)
  audit_mark
  "$host" recovery-mismatch --config "$state/acceptance.json"
  [[ $before == "$(journal_digest)" ]] || { echo 'site store changed for a refused recovery' >&2; exit 1; }
  audit_unchanged
  # The release's wipe boots normally.
  trap - ERR
  site exec deploy/fleet-site-boot -c boot -- fleet-multiplane boot-withhold clear
  ;;
 deadline-settled)
  scenario deadline-settled "$state"
  ;;
 rebuild)
  # Custody of the free Device is deleted while the site cannot report its
  # fence: the observer answers fence reports, and the hub accepts none
  # older than a minute.
  scenario rebuild-before "$state"
  # A failure hands the observer back to Flux, so a rerun starts clean.
  trap 'site_restore fleet-site-observer' ERR
  site_stop fleet-site-observer
  sleep 75
  bootstrap="$state/provider/bootstrap-project.json"
  k "$bootstrap" -n fleet-provider delete devicecustody synthetic-device-central-01 --wait=false >/dev/null
  k "$bootstrap" -n fleet-provider patch devicecustody synthetic-device-central-01 --type=merge -p '{"metadata":{"finalizers":null}}' >/dev/null
  scenario rebuild-unverified "$state"
  # A new consumer-a Machine finds no eligible capacity while custody is
  # Unverified.
  customer="$state/consumer-a/customer-project.json"
  k "$customer" -n default delete machine allocation --wait --timeout=60s >/dev/null
  k "$customer" -n default create -f - >/dev/null <<'JSON'
{"apiVersion":"metal.datumapis.com/v1alpha1","kind":"Machine","metadata":{"name":"allocation","namespace":"default"},"spec":{"machineClassRef":{"name":"local-class"},"machineProfileRef":{"name":"local-profile"},"locationRef":{"name":"local"},"powerState":"Off"}}
JSON
  "$host" probe-unallocatable --config "$state/acceptance.json" --once
  trap - ERR
  site_restore fleet-site-observer
  scenario rebuild-verified "$state"
  ;;
 await-delivery)
  for _ in $(seq 1 120); do
   names=$(cell -n "$ns" get machineallocationhandoffs -l "$assignments" -o jsonpath='{.items[*].metadata.name}')
   [[ -z $names ]] || break
   sleep 1
  done
  [[ -n $names ]] || { echo 'Karmada did not deliver retained intent' >&2; exit 1; }
  [[ $(wc -w <<<"$names") -eq 1 ]] || { echo 'more than one delivered assignment' >&2; exit 1; }
  printf '%s\n' "$names" > "$state/install-handoff"
  [[ -z $(local_phase) ]] || { echo 'cell status existed before any worker started' >&2; exit 1; }
  site wait --for=jsonpath='{.status.deliveredHandoffs}'=1 handoffdispatcher/dispatcher --timeout=120s >/dev/null
  [[ -z $(site get jobs -o name) ]] || { echo 'worker Job exists before dispatch' >&2; exit 1; }
  printf 'delivery: Karmada delivered exactly one retained assignment; kro observed it; no worker Job before dispatch\n'
  ;;
 partition)
  # Consumer A's API is Flux-managed: suspend its Kustomization, then stop it.
  suspend milo-consumer-a true
  platform -n milo-consumer-a scale deployment/milo-apiserver --replicas=0 >/dev/null
  platform -n milo-consumer-a wait --for=delete pod -l app.kubernetes.io/name=milo-apiserver --timeout=120s >/dev/null
  python3 hack/multiplane/fault_guard.py record "$state" platform-admin.yaml kustomization milo-consumer-a milo-consumer-a milo-apiserver flux-partition
  docker pause "$prefix-hub-control-plane" >/dev/null
  printf 'partition: consumer-a API stopped (its Flux Kustomization suspended) and hub paused before any worker Job exists\n'
  ;;
 dispatch)
  # Dispatch is a source revision, reconciled by the cell's own Flux from the
  # registry while the hub is paused; kro then creates per-operation RBAC and the Job.
  flux publish "$state" --revision 3 --site dispatch --purpose 'enable dispatch' --cluster cell --timeout 600 >/dev/null
  [[ $(site get handoffdispatcher dispatcher -o jsonpath='{.spec.dispatch}') == true ]] || { echo 'dispatch revision not applied' >&2; exit 1; }
  name=$(cat "$state/install-handoff")
  for _ in $(seq 1 120); do site get job "w-$name" >/dev/null 2>&1 && break; sleep 1; done
  py job "$state" "$name" > "$state/k8s-dispatch-job.json"
  printf 'dispatch: cell Flux applied the dispatch revision; kro created the worker Job and per-operation RBAC from the delivered handoff with no hub access\n'
  ;;
 worker-rbac)
  name=$(cat "$state/install-handoff")
  site create token "w-$name" --duration=10m > "$state/worker.token"
  py worker-rbac "$state" "$name"
  rm -f "$state/worker-kubeconfig.json"
  ;;
 interrupt-pod)
  name=$(cat "$state/install-handoff")
  "${harness[@]}" wait-stage --state "$state" --stage "$installer_step" --timeout 900 --interval 0.5 > "$state/interrupt-observed.json"
  before=$(wait_running_pod "$name" 30)
  py job "$state" "$name" > "$state/interrupt-job-before-pod.json"
  # Abrupt Pod loss with no grace period. The guest runs under the runtime Pod,
  # so the in-flight installer continues; only its worker process is lost.
  site delete pod -l "fleet.datumapis.com/handoff=$name" --grace-period=0 --force >/dev/null 2>&1
  [[ -z $(local_phase) ]] || { echo 'local result published for an interrupted operation' >&2; exit 1; }
  after=
  for _ in $(seq 1 180); do
   after=$(py live-pod "$state" "$name")
   [[ -z $after || $after == "$before" ]] || break
   sleep 1
  done
  [[ -n $after && $after != "$before" ]] || { echo 'no replacement worker Pod' >&2; exit 1; }
  py job "$state" "$name" > "$state/interrupt-job-after-pod.json"
  "${harness[@]}" journal --state "$state" > "$state/interrupt-after.json"
  py record-pod-interrupt "$state" "$before" "$after"
  printf 'interrupt-pod: worker Pod force-deleted after the installer boot was journaled with its domain running; Job controller replaced it; no local result\n'
  ;;
 interrupt-job)
  name=$(cat "$state/install-handoff")
  old_job=$(site get job "w-$name" -o jsonpath='{.metadata.uid}')
  # The whole emulated install takes about a minute: delete the Job as soon as
  # its replacement worker exists, while the operation is still unfinished.
  py live-pod "$state" "$name" > "$state/interrupt-job-pod-before"
  [[ -s $state/interrupt-job-pod-before ]] || { echo 'no live worker Pod to interrupt' >&2; exit 1; }
  "${harness[@]}" journal --state "$state" > "$state/interrupt-job-before.json"
  site delete job "w-$name" --wait=false >/dev/null
  new_job=
  for _ in $(seq 1 120); do
   new_job=$(site get job "w-$name" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)
   [[ -z $new_job || $new_job == "$old_job" ]] || break
   sleep 1
  done
  [[ -n $new_job && $new_job != "$old_job" ]] || { echo 'kro did not recreate the deleted Job' >&2; exit 1; }
  pod=
  for _ in $(seq 1 240); do pod=$(py job-pod "$state" "$name" "$new_job"); [[ -z $pod ]] || break; sleep 1; done
  [[ -n $pod ]] || { echo 'recreated Job created no worker Pod' >&2; exit 1; }
  "${harness[@]}" journal --state "$state" > "$state/interrupt-job-after.json"
  py record-job-interrupt "$state" "$old_job" "$new_job" "$(cat "$state/interrupt-job-pod-before")" "$pod"
  printf 'interrupt-job: running worker Job deleted mid-install; kro recreated it under a new UID for the same operation\n'
  ;;
 complete)
  # The Flux qualification suite: the one install runs to completion without
  # interruption before the GitOps scenarios disturb its surroundings.
  name=$(cat "$state/install-handoff")
  site wait --for=condition=Complete "job/w-$name" --timeout=30m >/dev/null
  cell -n "$ns" wait --for=jsonpath='{.status.observation.result.phase}'=Succeeded machineallocationhandoff "$name" --timeout=5m >/dev/null
  printf 'complete: the worker Job completed the install and the observer published Succeeded\n'
  ;;
 resume)
  name=$(cat "$state/install-handoff")
  site wait --for=condition=Complete "job/w-$name" --timeout=20m >/dev/null
  cell -n "$ns" wait --for=jsonpath='{.status.observation.result.phase}'=Succeeded machineallocationhandoff "$name" --timeout=5m >/dev/null
  "${harness[@]}" journal --state "$state" > "$state/resume-after.json"
  py record-resume "$state" "$name"
  printf 'resume: replacement worker completed the exact interrupted operation at its fence; one install operation; observer published the stored result\n'
  ;;
 partition-readiness)
  # The platform's root sync must apply r3 (pushed during the partition) with
  # consumer A still suspended, and the suspension must survive it unchanged.
  flux wait "$state" --cluster platform --timeout 300 >/dev/null
  rev=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[-1]["fluxRevision"])' "$state/flux-revisions.json")
  python3 hack/multiplane/fault_guard.py check "$state" platform-admin.yaml kustomization milo-consumer-a milo-consumer-a milo-apiserver flux-partition "$rev"
  # Resuming hands ownership back: Flux, not the harness, restores replicas.
  suspend milo-consumer-a false
  platform -n flux-system annotate --overwrite kustomization/milo-consumer-a "reconcile.fluxcd.io/requestedAt=$(date +%s)" >/dev/null
  platform -n milo-consumer-a wait --for=jsonpath='{.status.availableReplicas}'=1 deployment/milo-apiserver --timeout=240s >/dev/null
  wait_api "$state/consumer-a/bootstrap.json"
  ready=$(k "$state/consumer-a/management-project.json" -n default get machine allocation -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
  [[ $ready == False ]] || { echo 'Public readiness advanced during partition' >&2; exit 1; }
  name=$(cat "$state/install-handoff")
  [[ $(site get job "w-$name" -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}') == True ]] || { echo 'install Job not Complete' >&2; exit 1; }
  printf 'PXE succeeded cell-locally and the worker Job is Complete while hub/consumer disconnected; public Ready stayed False; Flux restored consumer-a\n'
  ;;
 heal)
  docker unpause "$prefix-hub-control-plane" >/dev/null
  docker restart --timeout 30 "$prefix-hub-control-plane" >/dev/null
  docker restart --timeout 30 "$prefix-cell-control-plane" >/dev/null
  wait_api "$state/host.yaml"
  wait_api "$state/cell-admin.yaml"
  k "$state/host.yaml" -n karmada-system wait --for=condition=Available deployment --all --timeout=300s >/dev/null
  # The cell node restart recreated every site Pod; the Flux-owned Deployments
  # and the retained volume must bring the site back with the same journal.
  for d in fleet-site-runtime fleet-site-boot fleet-site-observer; do site wait --for=condition=Available "deployment/$d" --timeout=300s >/dev/null; done
  cell -n kro-system wait --for=condition=Available deployment --all --timeout=300s >/dev/null
  # Restarted Flux on both nodes; the hub catches up with the dispatch revision.
  flux wait "$state" --cluster hub --cluster cell --cluster platform --timeout 600 >/dev/null
  printf 'heal: hub and cell APIs restarted; Karmada, kro and site Deployments available; Flux on every cluster Ready at the current revision\n'
  ;;
 lifecycle)
  # Pod and Job lifecycle are not effects: recreating site Pods or a completed
  # Job must not change the journal, start a guest, or release capacity.
  name=$(cat "$state/install-handoff")
  py lifecycle-snapshot "$state" "$name" > "$state/lifecycle-before.json"
  started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  old_job=$(site get job "w-$name" -o jsonpath='{.metadata.uid}')
  site delete pod -l app.kubernetes.io/name=fleet-site-runtime --wait=true >/dev/null
  site delete pod -l app.kubernetes.io/name=fleet-site-boot --wait=true >/dev/null
  site delete pod -l app.kubernetes.io/name=fleet-site-observer --wait=true >/dev/null
  site delete job "w-$name" --wait=true >/dev/null
  for d in fleet-site-runtime fleet-site-boot fleet-site-observer; do
   site rollout status "deployment/$d" --timeout=300s >/dev/null
   site wait --for=condition=Available "deployment/$d" --timeout=300s >/dev/null
  done
  for _ in $(seq 1 120); do
   new_job=$(site get job "w-$name" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)
   [[ -z $new_job || $new_job == "$old_job" ]] || break
   sleep 1
  done
  [[ -n $new_job && $new_job != "$old_job" ]] || { echo 'kro did not recreate the completed Job' >&2; exit 1; }
  site wait --for=condition=Complete "job/w-$name" --timeout=300s >/dev/null
  # The restarted observer must republish from the unchanged journal.
  for _ in $(seq 1 60); do
   at=$(cell -n "$ns" get machineallocationhandoff "$name" -o jsonpath='{.status.observation.observedAt}')
   [[ $at > $started ]] && break
   sleep 2
  done
  [[ $at > $started ]] || { echo 'observer did not republish after restart' >&2; exit 1; }
  py lifecycle-snapshot "$state" "$name" > "$state/lifecycle-after.json"
  py record-lifecycle "$state" "$old_job" "$new_job"
  printf 'lifecycle: runtime, boot and observer Pods and the completed Job recreated; journal, guest starts and volume unchanged; capacity still occupied\n'
  ;;
 delivery-faults)
  # Cell-local delivery faults injected with cell bootstrap credentials: a
  # duplicate the API refuses, an assignment naming another cell, and a
  # replay of an admitted operation below the Device's stored fence.
  before=$(journal_digest)
  audit_mark
  py delivery-faults "$state" create
  for fault in wrong-cell stale-fence; do
   job=$(cat "$state/fault-$fault-job")
   for _ in $(seq 1 120); do site get job "$job" >/dev/null 2>&1 && break; sleep 1; done
   site wait --for=condition=Failed "job/$job" --timeout=300s >/dev/null
  done
  after=$(journal_digest)
  [[ $before == "$after" ]] || { echo 'site store changed during delivery faults' >&2; exit 1; }
  audit_unchanged
  py delivery-faults "$state" verify
  py delivery-faults "$state" delete
  printf 'delivery-faults: duplicate refused by the API; wrong-cell and stale-fence deliveries failed their Jobs by podFailurePolicy before effects; site store unchanged; kro pruned them\n'
  ;;
 release-status-b)
  # The customer deleted Machine B; its release removed the finalizer last.
  r=$(k "$state/consumer-b/management-project.json" -n default get machine allocation 2>&1 || true)
  [[ $r == *NotFound* ]] || { echo "deleted consumer-b Machine remains: $r" >&2; exit 1; }
  printf 'release-status-b: consumer-b Machine deleted after its release finished\n'
  ;;
 release-status-a)
  # Released claims must not keep public readiness or an installation reference.
  project=consumer-a
  v=$(k "$state/$project/management-project.json" -n default get machine allocation -o jsonpath='{.status.phase}/{.status.conditions[?(@.type=="Bound")].status}/{.status.conditions[?(@.type=="Ready")].status}/{.status.currentInstallationRef.name}/{.metadata.finalizers}')
  [[ $v == Released/False/False// ]] || { echo "released $project Machine phase/Bound/Ready/currentInstallationRef/finalizers: $v" >&2; exit 1; }
  if [[ $step == release-status-a ]]; then
   # The referenced Installation is retired through the real Milo API status subresource.
   i=$(k "$state/$project/management-project.json" -n default get installation debian-pxe -o jsonpath='{.status.phase}/{.status.conditions[?(@.type=="Ready")].status}')
   [[ $i == Released/False ]] || { echo "released $project Installation phase/Ready: $i" >&2; exit 1; }
  fi
  printf '%s: %s Machine Released, Bound False, Ready False, no current installation, no finalizer%s\n' "$step" "$project" "$([[ $step == release-status-a ]] && echo '; Installation Released, Ready False')"
  ;;
 reuse-status)
  b=$(k "$state/consumer-b/management-project.json" -n default get machine allocation -o jsonpath='{.status.phase}/{.status.conditions[?(@.type=="Bound")].status}')
  [[ $b == Bound/True ]] || { echo "reused consumer-b status: $b" >&2; exit 1; }
  printf 'reuse-status: consumer-b Bound after the verified wipe\n'
  ;;
 wrong-location)
  # Runs after both releases, so capacity is free: denial cannot be
  # explained by contention. The customer recreates its request elsewhere.
  before_custody=$(custody_digest); before_journal=$(journal_digest)
  customer="$state/consumer-a/customer-project.json"
  k "$customer" -n default delete machine allocation --wait --timeout=60s >/dev/null
  k "$customer" -n default create -f - >/dev/null <<'JSON'
{"apiVersion":"metal.datumapis.com/v1alpha1","kind":"Machine","metadata":{"name":"allocation","namespace":"default"},"spec":{"machineClassRef":{"name":"local-class"},"machineProfileRef":{"name":"local-profile"},"locationRef":{"name":"remote-elsewhere"},"powerState":"Off"}}
JSON
  set +e
  "$host" allocate --config "$state/acceptance.json" > "$state/wrong-location.out" 2> "$state/wrong-location.err"
  code=$?
  set -e
  [[ $code -ne 0 ]] || { echo 'wrong-location Machine was allocated' >&2; exit 1; }
  grep -q 'not served by the configured cell' "$state/wrong-location.err" || { echo "allocation failed for another reason: $(tail -c 300 "$state/wrong-location.err")" >&2; exit 1; }
  after_custody=$(custody_digest); after_journal=$(journal_digest)
  [[ $before_custody == "$after_custody" ]] || { echo 'custody changed during wrong-location attempt' >&2; exit 1; }
  [[ $before_journal == "$after_journal" ]] || { echo 'site store changed during wrong-location attempt' >&2; exit 1; }
  k "$state/consumer-a/management-project.json" -n default get machine allocation -o json > "$state/wrong-location-machine.json"
  python3 - "$state" "$code" "$before_custody" <<'PY'
import json,sys,pathlib,datetime
m=json.loads((pathlib.Path(sys.argv[1])/'wrong-location-machine.json').read_text())
if m['metadata'].get('finalizers') or m.get('status'): raise SystemExit('wrong-location Machine gained finalizers or status')
record={'observedAt':datetime.datetime.now(datetime.timezone.utc).isoformat(),'requestedLocation':m['spec']['locationRef']['name'],'servedLocation':'local',
 'capacityFree':True,'exitCode':int(sys.argv[2]),'error':'ErrLocationNotServed','custodyUnchanged':True,'custodyRecords':int(sys.argv[3].split()[0]),
 'siteStoreUnchanged':True,'machineFinalizers':[],'machineStatus':None}
(pathlib.Path(sys.argv[1])/'evidence'/'wrong-location.json').write_text(json.dumps(record,indent=2,sort_keys=True)+'\n')
PY
  printf 'wrong-location: denied before allocation with free capacity; custody and site store unchanged\n'
  ;;
 boot-denials)
  # Probe the boot Service from the runtime Pod: the same path firmware uses.
  # The origin comes from the Flux-owned Service.
  origin="http://$(site get service fleet-site-boot -o jsonpath='{.spec.clusterIP}'):8080"
  [[ $origin == "$(site get handoffdispatcher dispatcher -o jsonpath='{.status.bootOrigin}')" ]] || { echo 'dispatcher boot origin differs from the boot Service' >&2; exit 1; }
  if [[ $(cat "$state/site-provider" 2>/dev/null) == redfish ]]; then
   # Redfish mode also probes the never-allocated target B's MAC.
   runtime env FLEET_BOOT_ORIGIN="$origin" FLEET_BOOT_EVIDENCE=/var/lib/fleet-boot fleet-multiplane boot-denials --origin "$origin" --extra-mac 52:54:00:77:01:0b > "$state/boot-denials.txt"
  else
   runtime fleet-multiplane boot-denials --origin "$origin" > "$state/boot-denials.txt"
  fi
  grep -q '^PASS ' "$state/boot-denials.txt" || { echo 'closed-boot probe failed' >&2; exit 1; }
  printf 'boot-denials: %s through the boot Service\n' "$(cat "$state/boot-denials.txt")"
  ;;
 flux-final)
  # Every cluster converged on the last revision with nothing suspended.
  flux wait "$state" --cluster platform --cluster hub --cluster cell --timeout 600 >/dev/null
  python3 - "$state" <<'PY'
import json, pathlib, subprocess, sys
s = pathlib.Path(sys.argv[1])
for c, cfg in (('platform', 'platform-admin.yaml'), ('hub', 'host.yaml'), ('cell', 'cell-admin.yaml')):
    items = json.loads(subprocess.check_output(['kubectl', '--kubeconfig', str(s / cfg), 'get', 'kustomizations,helmreleases', '-A', '-o', 'json']))['items']
    held = [i['metadata']['name'] for i in items if i['spec'].get('suspend')]
    if held:
        raise SystemExit('%s still suspended: %s' % (c, held))
PY
  flux record "$state" --cluster platform --cluster hub --cluster cell --name final >/dev/null
  flux ownership "$state" --name final >/dev/null
  printf 'flux-final: platform, hub and cell Ready at the final revision with nothing suspended; one writer per site object\n'
  ;;
 *) echo "unknown step: $step" >&2; exit 64 ;;
esac
