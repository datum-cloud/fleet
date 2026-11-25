# Kubernetes-native site execution

The [multi-plane acceptance](multiplane.md) runs selected-cell execution as
ordinary Kubernetes workloads, as a site of [Device custody](../development/device-custody.md#cutover-increment-c). The cell's Flux owns the long-lived site services
directly, and an installed [kro](https://kro.run) controller creates only the
per-operation worker Jobs and their RBAC. Before Kubernetes execution, a hand-written cell
process was started with `docker exec` inside a privileged libvirt container.
That process listed and watched handoffs, executed them, retried them, and
wrote status. Now Kubernetes controllers own scheduling, retries, Pod
replacement and dependency ordering. Fleet code is left with only the parts
that carry authority:

- per-Device fences and a journal of each step, from the
  [site framework](../development/device-custody.md#site-execution-increment-b);
- fenced, typed results;
- observation of what the site store holds.

This is an **implemented local acceptance composition**, not a production
deployment. The [PXE installation proposal](../enhancements/pxe-installation/README.md#location-execution)
still defines the target contract. Physical Redfish, KVM, UEFI, production
IAM and HA remain out of scope.

## Versions

| Component | Pin | Why |
| --- | --- | --- |
| kro | v0.9.4, chart `oci://registry.k8s.io/kro/charts/kro` manifest digest `sha256:4e8de4d34b7e2c3cede958e140e26cd6d34a377062e63caf355a636d2bf4a67c` (archive layer SHA-256 `ceeb630764eb79ca4220dfdcc2907b819a27c30f68cb60d09c1ae3fa2c58b5cd`), image `registry.k8s.io/kro/kro:v0.9.4@sha256:eaf9fbaddd9d7f6cf400a8d47ab066b14cbef22b92a79be7bbdc7b3673376fb9`, installed by the cell's Flux `HelmRelease kro` | Newest patch of the newest minor with a settled release. v0.10.0 was published on 2026-11-10, five days before this work, and is deliberately not used. |
| Kubernetes | kind node v1.35.0 (digest-pinned in `topology.sh`) | Job `podFailurePolicy` and `podReplacementPolicy: Failed` are both available. |
| Site image | Built per run from `hack/local-vm/Containerfile` target `site`; the binary comes from a named build context that holds only the checksum-verified `fleet-multiplane`. Pushed to the run registry and referenced by digest in the source. | One image for runtime, boot service, observer and workers |

Version sources were read at the pinned tag: the kro installation, schema,
external reference, collection and readiness documentation; the kro Helm chart
values; and kro's `aggregation` RBAC mode. Flux pulls the chart by manifest
digest, and `flux-verify` checks the release history records that digest. See
[Flux-reconciled local environment](gitops.md) for the installation path.

## Topology on the selected cell

```text
                 cell Flux (one source revision, scoped identity per layer)
   ┌──────────────────┬───────────────────────┬──────────────────────────┐
   ▼                  ▼                       ▼                          ▼
fleet-site-base    fleet-site-services      fleet-site-graph          fleet-site-dispatch
ns, Retain PVC,    runtime, boot and        ResourceGraphDefinition   HandoffDispatcher/dispatcher
identities, RBAC,  observer Deployments;    fleet-handoff-dispatcher  (cell, dispatch)
kro grants,        boot Service (fixed                                      │
admission policy   ClusterIP); policy param                                 │ kro
                          ▲  externalRef (read only, readiness-gated)       ▼
                          └──────────────────────────────────── per delivered handoff (forEach):
Karmada ──delivers──▶ MachineAllocationHandoff ─ external collection ─▶ ServiceAccount, Role
                      (fleet-coordination, label-selected by cell)      (resourceNames: that handoff,
                                                                        get), RoleBinding, bounded Job

VM runtime (libvirtd, privileged) ◀─ libvirt socket ─ worker Job ── runs one assignment
   └─ firmware DHCP/iPXE ─NAT─▶ boot Service ─▶ boot Deployment (site store read-only)
observer Deployment (site store read-only; only handoff status writer)
            ╲_____________ fleet-site-state PVC (Retain), Flux-owned __________╱
```

| Resource | Owner (single writer) | Privileges | Volume subpaths | Role |
| --- | --- | --- | --- | --- |
| `fleet-site` namespace, `fleet-site-retain` StorageClass, `fleet-site-state` PVC | Flux `fleet-site-base` (`config/site/base`; never pruned; class and claim created once) | none | none | The site store (each Device's fence and journal, under the `journal` subpath), disks, boot evidence, libvirt socket and QEMU logs. Never in any graph. |
| `fleet-site-runtime`, `fleet-site-boot`, `fleet-site-observer` ServiceAccounts; observer Role and RoleBinding | Flux `fleet-site-base` | Observer: get/list/watch handoffs; update handoff status | — | The observer is the only cell identity that writes handoff status |
| `fleet-site-kro` ClusterRole, `fleet-site-kro-workers` and `fleet-site-kro-worker-rbac` Roles and bindings, two `ValidatingAdmissionPolicies` | Flux `fleet-site-base` | — | — | kro's narrowed grant and the shape of its writes ([trust model](#ownership-and-trust-model)) |
| `fleet-site-runtime` Deployment | Flux `fleet-site-services` | **privileged** (bridge, dnsmasq, tap, iptables) | `journal` rw, `libvirt-socket` rw, `libvirt-log` rw, `boot-evidence` ro | libvirtd and QEMU only. No API token, no TCP listener. |
| `fleet-site-boot` Service and Deployment | Flux `fleet-site-services` | uid 0 with all capabilities dropped, read-only root filesystem, no API token | `journal` **ro**, `boot-evidence` rw | Boot decision point and artifact delivery. The Service has a fixed ClusterIP (`10.96.0.80`). |
| `fleet-site-observer` Deployment | Flux `fleet-site-services` | uid 0 with all capabilities dropped, read-only root filesystem | `journal` **ro** | Publishes each Device's stored fences, and the stored result of each assignment, every 10 seconds |
| `fleet-site-worker-policy` ConfigMap | Flux `fleet-site-services` | — | — | Admission parameter: the image worker Jobs must run |
| `fleet-handoff-dispatcher` ResourceGraphDefinition, `HandoffDispatcher/dispatcher` | Flux `fleet-site-graph`, `fleet-site-dispatch` (each a scoped identity; never pruned) | — | — | Orders per-operation work and projects non-authoritative status |
| `w-<handoff>` ServiceAccount, Role, RoleBinding, Job | kro `forEach` over delivered handoffs | get on exactly one handoff; uid 0 with all capabilities dropped; read-only root filesystem | `journal` rw, `libvirt-socket` rw, `boot-evidence` ro | Runs or resumes that one assignment |

## Ownership and trust model

This split is **implemented**, and is exercised by the acceptance phases named
below; see [verification](multiplane.md#verification-and-remaining-gaps) for
the run record. It supersedes the earlier composition, in which the graph
also created the three shared Deployments, the boot Service and their
ServiceAccounts.

**One writer per object.** Each shared site object carries exactly one Flux
`kustomize.toolkit.fluxcd.io/name` label, appears in exactly that layer's
inventory, and carries no kro label or owner reference. Every object kro writes
is a `w-<handoff>` Job, ServiceAccount, Role or RoleBinding, has a kro node
label, and is in no Flux inventory. `flux-verify` (before dispatch),
`flux-graph-lifecycle` (with workers present), `flux-drift` and `flux-final`
each run this audit (`flux.py ownership`). This audit uses labels, owner
references, RBAC and inventories, not server-recorded `managedFields`. Shared
inventory membership is matched by namespace/name prefix rather than kind, so
same-named resources can satisfy each other; the base inventory is not checked
for exactness. Independent review confirmed the expected kinds in the saved
live inventory. These checks are evidence of the intended ownership split, not
a complete proof that no other field manager has written an object.

**Dependency and health ordering.**

1. `fleet-site-base` depends only on the handoff CRD (`fleet-coordination`).
2. `fleet-site-services` depends on the base. It waits for all three
   Deployments to be Available; the runtime's readiness probe requires libvirtd.
3. `fleet-site-graph` depends on the kro release and the base, and is healthy
   when the RGD is `Active`.
4. `fleet-site-dispatch` depends on the graph, the services and the federation
   member identity. It is healthy only when kro reports the instance `ACTIVE`.

Inside the graph, every worker template references the runtime, boot and
observer Deployments and the boot Service through `externalRef` with
`readyWhen`. kro v0.9.4 treats a missing or unready reference as pending data.
It then creates no new worker object and **disables pruning** for that
reconcile, so a shared-service outage never deletes an existing Job. Shared
infrastructure does not depend on kro: in `flux-fail-closed`, a broken kro
release blocks only the graph and the dispatcher, while the base and services
layers apply the same revision without restarting any service.

**kro's narrowed authority.** kro v0.9.4 watches every kind it manages or
references with cluster-wide metadata informers, so its aggregated
`fleet-site-kro` ClusterRole keeps cluster-wide get/list/watch. That covers
Deployments, Services, PVCs, ServiceAccounts, Jobs, Roles, RoleBindings and
handoffs. Its only cluster-wide write is to its own `HandoffDispatcher`
instances. Writes are namespaced:

- Jobs and ServiceAccounts in `fleet-site`;
- Roles and RoleBindings in `fleet-coordination`.

kro has no Deployment, Service, PVC, ConfigMap or Secret write, and no handoff
spec or status write. The kro chart's own static role also granted
cluster-wide ConfigMap read/write, which kro v0.9.4 never uses. A live `rbac`
probe found it, and the `HelmRelease` now removes it with a post-renderer
patch. A JSON-patch `test` guards that patch, so a changed chart fails the
release rather than removing another rule. The chart grants kro keeps are
cluster-wide CustomResourceDefinition management, which it needs to create
each graph's kind, plus Leases and Events. `kro-rbac.json` records them as
remaining authority. RBAC alone would still let a graph revision create any
Job in `fleet-site`, including one that runs as the observer (the status writer)
or as a privileged container. Two `ValidatingAdmissionPolicies` apply only to
kro's own requests and pin those writes to the worker shape:

- a `w-<handoff>` name matching the Job's handoff label;
- the Job's own ServiceAccount;
- one container from the image the shared services run (a parameter written
  by the services layer);
- no privilege or added capability;
- no host namespaces;
- only the site state claim and `emptyDir` volumes.

A Role may grant only `get` on its own handoff, and a RoleBinding may bind only
its own Role to its own ServiceAccount. The `rbac` phase proves this with real
`auth can-i` checks as kro's identity and server-side dry runs. Valid worker
shapes are admitted, and eight violations are each denied by policy
(`kro-rbac.json`). The policies cover CREATE and UPDATE, not DELETE. kro
can still delete any ServiceAccount in `fleet-site` and any Role or RoleBinding
in `fleet-coordination`, including Flux-owned identities and observer or
federation RBAC. Normal pruning targets only tracked worker objects, and the
policies prevent adopting these shared objects, but a controller bug or
compromise could interrupt delivery or status until Flux repairs them. Delete
denials are not covered by the current probes.

**What graph authors are still trusted with.** This is not a sandbox for
untrusted graph revisions:

- A worker mounts the site store read-write and reaches root libvirtd through
  its socket. A graph author who changes the worker command, within the
  admitted image, can therefore drive the hypervisor and rewrite the site store.
- kro must read every delivered handoff, including its assignment, to
  enumerate them.
- kro keeps cluster-wide CRD create/update/delete from its chart, so a
  compromised kro controller could alter or remove CRDs it does not own.
- The services layer chooses the privileged runtime image and its
  configuration.
- The same source owns the admin-applied base, including these policies.

Source review remains the control for all layers. The admission policies remove
the paths a graph revision would otherwise have to the observer's
status-writing token, to a privileged Pod, to another image or volume, and to
other namespaces. Production still needs a narrower action broker in place of
the libvirt socket; see [practical separation decisions](#practical-separation-decisions).

**Migration from the earlier composition** is not implemented. Every acceptance
run creates a fresh cell. An existing site with kro-owned shared Deployments
would need these steps:

1. Quiesce: no unfinished operation, and dispatch disabled.
2. Publish a graph revision without the shared resources, and confirm kro
   removed them. A gap stops new boots, which is safe but not cancellation.
3. Publish the services layer.

Doing it in one revision would leave two writers contending for the same
objects. This sequence is proposed only.

### Practical separation decisions

- **Only the VM runtime sets `privileged: true`.** Guests run under libvirtd in
  that Pod, not under a worker. Losing a worker Pod or Job therefore loses only
  an observer process; the installer continues. Losing the runtime Pod stops
  any guest. The operation then stays Unknown and capacity stays occupied.
- **Workers reach libvirt through a Unix socket on the site volume**
  (`qemu:///system?socket=/run/fleet-libvirt/libvirt-sock`). They hold no
  network credential, and nothing listens on TCP. The local VM site accepts only
  that URI shape. This socket still grants broad control over root libvirtd,
  including domain definitions backed by paths in the runtime filesystem.
  Dropped Linux capabilities do not isolate workers from the runtime's power.
  The fixed worker image and graph are trusted in this disposable proof;
  production requires a narrower action broker.
- **The boot service is a separate Deployment behind a ClusterIP Service.** The
  provisioning bridge uses libvirt NAT forwarding. DHCP hands firmware an
  `http://<ClusterIP>:8080/...` boot file. The Service's ClusterIP is fixed
  (`10.96.0.80`, in the static band of kind's default `10.96.0.0/16` Service
  range) and comes from the environment's `FLEET_BOOT_SERVICE_IP`. Flux
  substitutes it into the runtime and boot Deployments, and kro reads it from
  the Service for the workers. A recreated Service therefore keeps the boot
  origin that running Pods were configured with; `flux-drift` deletes it and
  proves this. The runtime
  entrypoint adds a `mangle` chain: traffic from the bridge may reach only
  local addresses (DHCP), broadcast, and the boot Service port. Everything
  else is dropped. The boot service reads the site store read-only and writes
  only its own delivery evidence in a separate subpath. Workers read that
  evidence when they verify network boot.
- **One shared volume, separate subpaths and mount modes.** All site Pods run
  on the single kind node, so one `ReadWriteOnce` local-path volume serves
  them. Each Device's run lock is effective because every writer shares one
  kernel and inode.
- **The observer is a Deployment, not part of the Job.** Management requires
  reflected observations to be fresher than one minute. Refreshing them is a
  persistent site service, so freshness does not depend on any Job's
  lifetime. It also keeps one status writer per cell.

## Authority and failure contract

- **Exact assignment before effect.** A worker reads only its handoff. It checks
  that the handoff is an assignment for this cell, that its name is derived from
  the assignment, and that the assignment is valid. The site framework then
  checks the inputs against the admitted digest, the enrollment against the
  snapshot, the fence against the Device's stored fence, and, for Redfish, the
  Device's credential and TLS pin. Any failed check sends nothing. See [device
  custody](../development/device-custody.md#site-execution-increment-b).
- **Worker exit codes are the Job contract.**
  - `0`: the assignment's result is stored, or another worker for the same
    assignment holds the Device's run lock and owns the result.
  - `65`: the handoff is not an assignment for this cell, or the assignment is
    below the Device's stored fence. `podFailurePolicy` fails the Job with no
    retry.
  - `75`: any other error. The Job controller retries, up to
    `backoffLimit: 6` and `activeDeadlineSeconds: 3600`.

  Disruption-target Pod conditions are ignored, not counted.
- **Each Device's run lock serializes effects.** `podReplacementPolicy: Failed`
  delays ordinary replacement until the old Pod is terminal. Force deletion
  can remove its API object while the old process still runs, and Job recreation
  can also overlap workers. A newer assignment raises the fence and waits for the
  lock, and the older worker stops within a second. A redelivered assignment
  that finds the same assignment running stores nothing. A recreated worker
  resumes from the journal.
- **Job Complete and kro Ready grant nothing.** The worker never writes
  status. The observer publishes only what the site store holds: the Device's
  fences, and the stored result of the assignment. The coordinator accepts only
  this cell's observation of exactly the assignment the handoff carries, by its
  digest and fence, and then labels the handoff answered so the cell deletes its
  copy. A result below the active operation's fence is rejected.

  The acceptance shows this directly: during the hub partition the install Job
  is `Complete` while public Machine `Ready` stays `False`. A failed Job leaves
  the operation unknown; it never releases capacity.
- **Delivery faults fail closed.** kro selects assignment handoffs by cell label
  only for convenience. The acceptance injects three faults: a duplicate handoff
  under a second name, which the API refuses; an assignment naming another cell;
  and an assignment replayed below the Device's stored fence. The last two each
  get a Job that fails once at exit 65 by policy, with the site store unchanged
  and no status written.

## Lifecycle and retention

- `fleet-site-state` uses a StorageClass with `reclaimPolicy: Retain`. Only
  `fleet-site-base` declares it. The services mount it and the RGD references
  it through `externalRef`, so neither the services layer nor the dispatcher
  instance or graph can delete it. kro v0.9.4 skips external references when it
  deletes an instance.
- **Operation graph lifecycle cannot remove shared infrastructure.**
  `flux-graph-lifecycle` suspends the dispatcher's Flux layer and deletes the
  `HandoffDispatcher` instance. kro removes only that handoff's Job,
  ServiceAccount, Role and RoleBinding. Every shared object keeps its UID,
  including the Deployments, Service, ServiceAccounts, RBAC, admission policies,
  PVC and StorageClass. The site store, PV, guests and custody are unchanged, and
  the handoff keeps its terminal observation. Resuming lets Flux recreate the
  instance; kro recreates the workers and the Job replays a verified no-op.
  kro deletes Jobs without a propagation policy, so batch/v1 orphans their
  terminal Pods; the evidence lists them.
- **Never delete or replace the site store** of a site whose disk or Device is
  retained. It holds the fences that refuse older assignments, and a fresh store
  would report a lower fence than the one the coordinator last admitted; see
  [known limitations](multiplane.md#known-limitations-of-the-local-mvp).
- After the installation is terminal, recreating the Flux-owned runtime, boot or
  observer Pods does not change the site store, guest starts, volume identity or
  public allocation. Neither does
  recreating a completed Job, which only replays a verified no-op. The
  `lifecycle` phase asserts all of this.
- **Pruning is not cancellation.** Setting `spec.dispatch` false, deleting a
  handoff, or deleting the `HandoffDispatcher` can prune worker Jobs. kro v0.9.4 does not
  specify cascading deletion for pruned Jobs, so dependent worker Pods may be
  orphaned and continue to completion, as may their guests. The observer can
  still publish a completed result if its handoff exists. Re-enabling dispatch
  can create a competing worker that waits on the Device's run lock. The harness
  does not exercise pruning an active Job; its explicit Job deletion uses
  background propagation. Neither dispatch nor object deletion is an authorized
  cancellation or fencing protocol.
- **Shared-service changes are reviewed source revisions.** Changing the
  services layer, such as a new site image, rolls the Deployments. The runtime
  uses `Recreate`, so a rollout stops any running guest. The operation then stays
  Unknown and capacity occupied; schedule such revisions with no operation in
  flight. Dispatch changes are also source revisions, and kro remains the
  only writer of the per-operation objects. Flux's `fleet-site-dispatch`
  inventory is exactly the `HandoffDispatcher`; see [ownership](gitops.md#ownership).
  **Image upgrades are not qualified:** historical Jobs are retained, and the
  graph derives their image from the runtime Deployment. A changed image can
  therefore cause kro to update an immutable Job pod template and block later
  dispatch. This is a review-identified risk, not an exercised upgrade failure.
  Instance recreation has verified no-op replay with the same image; it does
  not qualify upgrades. Use a fresh disposable topology for image changes until
  an upgrade lifecycle is implemented and tested.
- kro's controller is not in the authority path. If kro is down, no new
  worker is created, existing Jobs continue, and the Flux-owned shared services
  keep running and reconciling. Recovery is kro reconciliation followed by
  replay from the journal.

## Secrets

A handoff carries one assignment: the Device, the operation, the fence, the
admitted snapshot and the exact inputs. It holds no credential. A bootstrap
payload is refused, so none is carried. A Redfish worker reads each Device's own
BMC credential Secret in the site's cluster, never from a handoff. Cell readers
of handoffs are:

- each worker, for its one handoff only;
- the observer;
- Karmada's member identity;
- kro, which must read handoffs to enumerate them.

Job specs, Pod arguments, kro instance status and labels carry only handoff
names and the cell name. Worker and observer logs print an outcome class, never
request bodies. The harness creates short-lived tokens for RBAC probes with names
ending in `.token`. It scans every evidence file against all retained tokens and
boot capabilities.

## What replaced what

| Removed | Replaced by |
| --- | --- |
| `fleet-multiplane cell` process launched with `docker exec`, its PID file and `kill` signalling | kro-created Jobs and the Job controller |
| `coordination.Cell.Run` list/watch loop and `Reconcile` (execute plus publish) | `Cell.Work` (worker; no status writes) and `Cell.Publish`/`Cell.Observe` (observer; reads the site store only) |
| `config/multiplane/cell-rbac.yaml` and an exported cell kubeconfig | Static observer RBAC and kro per-operation RBAC; no cell credential leaves the cell |
| Dedicated privileged provider container and `docker exec` probes | Runtime, boot and observer Deployments; probes through the cell API |
| kro-created runtime, boot and observer Deployments, boot Service and their ServiceAccounts (`FleetSite` graph), with a cluster-wide kro write grant | Flux layer `fleet-site-services` (scoped identity) and identities in `fleet-site-base`; the `HandoffDispatcher` graph references them; kro writes are namespaced and admission-checked |
| `helm install` of kro, `kubectl apply` of the site base and RGD, a `kubectl patch` of `dispatch`, and `kind load` of the image | Cell Flux `HelmRelease`/Kustomizations from a pushed source revision; the image pulled by digest from the run registry ([details](gitops.md)) |

The Increment C cutover also removed the site-wide provider journal, the
`requestJSON` handoff and the single-plane [local VM harness](local-vm.md). A
handoff now carries one assignment, and each Device has its own fence and
journal in the site store.

## Implemented versus proposed

Rows marked "exercised" were observed in runs on the path before the cutover to
Device custody. The same phases now run on custody. Verification of that run is
pending.

| Behavior | Status |
| --- | --- |
| Flux-owned shared site services and storage; kro-created per-operation RBAC and bounded worker Jobs on a real cell API | Implemented and exercised by `task multiplane:acceptance` |
| Single-writer ownership audit, shared Service drift repair by Flux, graph lifecycle leaving shared services and storage untouched | Implemented and exercised (`flux-verify`, `flux-drift`, `flux-graph-lifecycle`, `flux-final`) |
| Narrowed kro RBAC and admission policies on kro's writes | Implemented and exercised (`rbac`, `kro-rbac.json`); every dispatched Job and its RBAC passed them |
| In-place migration of an existing site from kro-owned shared services | Proposed only ([trust model](#ownership-and-trust-model)) |
| Pod loss and Job deletion mid-install resuming the same operation with one installer start | Implemented and exercised |
| A duplicate handoff refused, and wrong-cell and below-fence assignments failing by `podFailurePolicy` before effects | Implemented; the earlier phase was exercised, and the current one awaits verification |
| Pod/Job lifecycle not changing the site store, guest or allocation | Implemented and exercised |
| Facility-edge DaemonSet, multi-node volumes, HA runtime, live hardware fencing | Proposed only |
| A credential broker so no worker holds a long-lived BMC credential | Proposed only ([open questions](../enhancements/pxe-installation/README.md#open-design-questions)) |
| Redfish workers with no libvirt path, against the DMTF Redfish Interface Emulator and lab VMs | Implemented as a separate mode; see [Redfish-mode acceptance](redfish-lab.md). Verification of the run on custody is pending. |
| Synchronized DHCP boot reservations with served and withdrawn evidence, read from the site store | Implemented and unit tested; not exercised at a site ([Boot network](boot-network.md)) |
| Physical Redfish/BMC execution Jobs | Not qualified (lab emulator only; see the [hardware readiness checklist](redfish-lab.md#limitations-and-physical-hardware-readiness)) |
| Per-Device fence store and journal, older-worker stop with a confirmation, worker deadlines, fenced typed results, per-Device credentials with the approved TLS pin, and the operation handoff payload ([device custody](../development/device-custody.md#site-execution-increment-b)) | Implemented: unit tested, and every site runs it since the cutover. The multi-plane run on it awaits verification. |

## Independent review of the recorded run

This review covered a run before Flux installation and before the cutover to
Device custody, so it describes the retired provider journal and its lock. The
[Flux-reconciled run](multiplane.md#flux-reconciled-acceptance-2026-11-15)
re-established the same execution findings and has not yet been independently
reviewed.

The reviewer reproduced both binaries byte for byte, verified all 113 source
fingerprint entries and all 39 phase results, and passed the full Go race suite,
vet, offline harness tests and ShellCheck. An adversarial concurrency probe
confirmed that a second worker cannot act while the first holds the journal lock.
No blocker was found for the disposable single-node proof.

The Job-deletion phase establishes that installation was still at
`installer-started` and that a replacement Job resumed that exact operation. It
does not establish that the replacement worker Pod was already Running or held
the journal lock at the instant its predecessor Job was deleted. Pod interruption
and Job recreation evidence should not be read as a stronger timing guarantee.
That review found that RGD and instance authors remained trusted, because kro's
grant could create workloads using any namespace ServiceAccount, including the
observer's. The current composition closes that path with namespaced kro writes
and admission policies; the remaining trust is listed under
[ownership and trust model](#ownership-and-trust-model).
