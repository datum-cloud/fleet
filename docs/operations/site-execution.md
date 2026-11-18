# Kubernetes-native site execution

The [multi-plane acceptance](multiplane.md) runs selected-cell execution as
ordinary Kubernetes workloads. The cell's Flux owns the long-lived site services
directly, and an installed [kro](https://kro.run) controller creates only the
per-operation worker Jobs and their RBAC. Before Kubernetes execution, a hand-written cell
process was started with `docker exec` inside a privileged libvirt container.
That process listed and watched handoffs, executed them, retried them, and
wrote status. Now Kubernetes controllers own scheduling, retries, Pod
replacement and dependency ordering. Fleet code is left with only the parts
that carry authority:

- exact-request replay against the provider journal;
- fencing;
- journal-derived observation.

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
identities, RBAC,  observer Deployments;    fleet-handoff-dispatcher  (cell, scoped asset, dispatch)
kro grants,        boot Service (fixed                                      │
admission policy   ClusterIP); policy param                                 │ kro
                          ▲  externalRef (read only, readiness-gated)       ▼
                          └──────────────────────────────────── per delivered handoff (forEach):
Karmada ──delivers──▶ MachineAllocationHandoff ─ external collection ─▶ ServiceAccount, Role
                      (fleet-coordination, label-selected by cell)      (resourceNames: that handoff,
                                                                        get), RoleBinding, bounded Job

VM runtime (libvirtd, privileged) ◀─ libvirt socket ─ worker Job ── replays one operation
   └─ firmware DHCP/iPXE ─NAT─▶ boot Service ─▶ boot Deployment (journal read-only)
observer Deployment (journal read-only; only handoff status writer)
            ╲_____________ fleet-site-state PVC (Retain), Flux-owned __________╱
```

| Resource | Owner (single writer) | Privileges | Volume subpaths | Role |
| --- | --- | --- | --- | --- |
| `fleet-site` namespace, `fleet-site-retain` StorageClass, `fleet-site-state` PVC | Flux `fleet-site-base` (`config/site/base`; never pruned; class and claim created once) | — | — | Durable journal, disks, boot evidence, libvirt socket and QEMU logs. Never in any graph. |
| `fleet-site-runtime`, `fleet-site-boot`, `fleet-site-observer` ServiceAccounts; observer Role and RoleBinding | Flux `fleet-site-base` | Observer: get/list/watch handoffs; update handoff status | — | The observer is the only cell identity that writes handoff status |
| `fleet-site-kro` ClusterRole, `fleet-site-kro-workers` and `fleet-site-kro-worker-rbac` Roles and bindings, two `ValidatingAdmissionPolicies` | Flux `fleet-site-base` | — | — | kro's narrowed grant and the shape of its writes ([trust model](#ownership-and-trust-model)) |
| `fleet-site-runtime` Deployment | Flux `fleet-site-services` | **privileged** (bridge, dnsmasq, tap, iptables) | `journal` rw, `libvirt-socket` rw, `libvirt-log` rw, `boot-evidence` ro | libvirtd and QEMU only. No API token, no TCP listener. |
| `fleet-site-boot` Service and Deployment | Flux `fleet-site-services` | uid 0 with all capabilities dropped, read-only root filesystem, no API token | `journal` **ro**, `boot-evidence` rw | Boot decision point and artifact delivery. The Service has a fixed ClusterIP (`10.96.0.80`). |
| `fleet-site-observer` Deployment | Flux `fleet-site-services` | uid 0 with all capabilities dropped, read-only root filesystem | `journal` **ro** | Publishes validated terminal journal results every 10 seconds |
| `fleet-site-worker-policy` ConfigMap | Flux `fleet-site-services` | — | — | Admission parameter: the image worker Jobs must run |
| `fleet-handoff-dispatcher` ResourceGraphDefinition, `HandoffDispatcher/dispatcher` | Flux `fleet-site-graph`, `fleet-site-dispatch` (each a scoped identity; never pruned) | — | — | Orders per-operation work and projects non-authoritative status |
| `w-<handoff>` ServiceAccount, Role, RoleBinding, Job | kro `forEach` over delivered handoffs | get on exactly one handoff; uid 0 with all capabilities dropped; read-only root filesystem | `journal` rw, `libvirt-socket` rw, `boot-evidence` ro | Executes or resumes that one retained operation |

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
- only the journal claim and `emptyDir` volumes.

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

- A worker mounts the journal read-write and reaches root libvirtd through
  its socket. A graph author who changes the worker command, within the
  admitted image, can therefore drive the hypervisor and rewrite the journal.
- kro must read every delivered handoff, including the fence token in
  `requestJSON`, to enumerate them.
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
  network credential, and nothing listens on TCP. The provider accepts only
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
  else is dropped. The boot service reads the journal read-only and writes
  only its own delivery evidence in a separate subpath. Workers read that
  evidence when they verify network boot.
- **One shared volume, separate subpaths and mount modes.** All site Pods run
  on the single kind node, so one `ReadWriteOnce` local-path volume serves
  them. The journal `flock` is effective because every writer shares one
  kernel and inode.
- **The observer is a Deployment, not part of the Job.** Management requires
  reflected observations to be fresher than one minute. Refreshing them is a
  persistent site service, so freshness does not depend on any Job's
  lifetime. It also keeps one status writer per cell.

## Authority and failure contract

- **Exact operation before effect.** A worker reads only its handoff. It
  checks the cell, checks that the name is derived from the operation ID, and
  validates the intent. `LocalAssetMapping` re-reads the same handoff and
  compares digests. The provider then replays the journal under the exact
  operation ID. A digest conflict, fence mismatch, stale epoch, or an
  unfinished prior operation stops before any effect.
- **Worker exit codes are the Job contract.**
  - `0`: a terminal result was validated against the journal.
  - `65`: fenced, conflicting or invalid authority. `podFailurePolicy` fails
    the Job with no retry.
  - `75`: Unknown or transport error. The Job controller retries, up to
    `backoffLimit: 6` and `activeDeadlineSeconds: 3600`.

  Disruption-target Pod conditions are ignored, not counted.
- **The journal lock serializes effects.** `podReplacementPolicy: Failed`
  delays ordinary replacement until the old Pod is terminal. Force deletion
  can remove its API object while the old process still runs, and Job recreation
  can also overlap workers. The provider's non-blocking journal lock is the
  actual execution guard in these cases; a contender exits 75. A recreated
  worker replays the same retained operation.
- **Job Complete and kro Ready grant nothing.** The worker never writes
  status. The observer publishes only a terminal journal result that passes
  `ValidateResult`. Management accepts only the Karmada-reflected observation
  that passes `ValidateReflection`, which checks:
  - exact cell, operation and intent/input digests;
  - freshness;
  - a single replica.

  The acceptance shows this directly: during the hub partition the install Job
  is `Complete` while public Machine `Ready` stays `False`. A failed Job leaves
  the operation Unknown; it never releases capacity.
- **Delivery faults fail closed.** kro selects handoffs by cell label only for
  convenience. A duplicate under another name, a wrong-cell intent and a
  stale-epoch replay each get a Job. The acceptance injects these faults with
  no pending operation: each fails once by policy without touching the journal.
  While another operation is unfinished, the provider may reject a stale
  request as Unknown before checking its fence. That worker exits 75 and may
  exhaust its retry budget instead of failing once with exit 65; no effect is
  authorized in either case.

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
  PVC and StorageClass. The journal, PV, guests and custody are unchanged, and
  the handoff keeps its terminal observation. Resuming lets Flux recreate the
  instance; kro recreates the workers and the Job replays a verified no-op.
  kro deletes Jobs without a propagation policy, so batch/v1 orphans their
  terminal Pods; the evidence lists them.
- **Never delete or replace the journal** of a provider whose disk or device
  is retained. A fresh journal would admit replayed handoffs as new
  operations; see [known limitations](multiplane.md#known-limitations-of-the-local-mvp).
- After the installation is terminal, recreating the Flux-owned runtime, boot or
  observer Pods does not change journal bytes, guest starts, volume identity or
  public allocation. Neither does
  recreating a completed Job, which only replays a verified no-op. The
  `lifecycle` phase asserts all of this.
- **Pruning is not cancellation.** Setting `spec.dispatch` false, deleting a
  handoff, or deleting the `HandoffDispatcher` can prune worker Jobs. kro v0.9.4 does not
  specify cascading deletion for pruned Jobs, so dependent worker Pods may be
  orphaned and continue to completion, as may their guests. The observer can
  still publish a completed result if its handoff exists. Re-enabling dispatch
  can create a competing worker that exits 75 on the journal lock. The harness
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
  journal replay.

## Secrets

The fence token remains inside the handoff's `requestJSON`; this is the existing
federation contract. Cell readers of handoffs are:

- each worker, for its one handoff only;
- the observer;
- Karmada's member identity;
- kro, which must read handoffs to enumerate them.

Job specs, Pod arguments, kro instance status and labels carry only handoff
names, the cell name and the scoped asset ID. Worker and observer logs print an
outcome class, never request bodies. The harness creates short-lived tokens for
RBAC probes with names ending in `.token`. It scans every evidence file against
all retained tokens, fences and boot capabilities.

## What replaced what

| Removed | Replaced by |
| --- | --- |
| `fleet-multiplane cell` process launched with `docker exec`, its PID file and `kill` signalling | kro-created Jobs and the Job controller |
| `coordination.Cell.Run` list/watch loop and `Reconcile` (execute plus publish) | `Cell.Execute` (worker; no status writes) and `Cell.Publish`/`Cell.Observe` (observer; lookup only) |
| `config/multiplane/cell-rbac.yaml` and an exported cell kubeconfig | Static observer RBAC and kro per-operation RBAC; no cell credential leaves the cell |
| Dedicated privileged provider container and `docker exec` probes | Runtime, boot and observer Deployments; probes through the cell API |
| kro-created runtime, boot and observer Deployments, boot Service and their ServiceAccounts (`FleetSite` graph), with a cluster-wide kro write grant | Flux layer `fleet-site-services` (scoped identity) and identities in `fleet-site-base`; the `HandoffDispatcher` graph references them; kro writes are namespaced and admission-checked |
| `helm install` of kro, `kubectl apply` of the site base and RGD, a `kubectl patch` of `dispatch`, and `kind load` of the image | Cell Flux `HelmRelease`/Kustomizations from a pushed source revision; the image pulled by digest from the run registry ([details](gitops.md)) |

The single-plane [local VM harness](local-vm.md) still uses its own disposable
container and is unchanged.

## Implemented versus proposed

| Behavior | Status |
| --- | --- |
| Flux-owned shared site services and storage; kro-created per-operation RBAC and bounded worker Jobs on a real cell API | Implemented and exercised by `task multiplane:acceptance` |
| Single-writer ownership audit, shared Service drift repair by Flux, graph lifecycle leaving shared services and storage untouched | Implemented and exercised (`flux-verify`, `flux-drift`, `flux-graph-lifecycle`, `flux-final`) |
| Narrowed kro RBAC and admission policies on kro's writes | Implemented and exercised (`rbac`, `kro-rbac.json`); every dispatched Job and its RBAC passed them |
| In-place migration of an existing site from kro-owned shared services | Proposed only ([trust model](#ownership-and-trust-model)) |
| Pod loss and Job deletion mid-install resuming the same operation with one installer start | Implemented and exercised |
| Duplicate, wrong-cell and stale-epoch delivery failing by `podFailurePolicy` before effects | Implemented and exercised |
| Pod/Job lifecycle not changing journal, guest or allocation | Implemented and exercised |
| Facility-edge DaemonSet, multi-node volumes, HA runtime, live hardware fencing | Proposed only |
| Removing the fence token from cell-readable handoff fields (credential broker) | Proposed only ([open questions](../enhancements/pxe-installation/README.md#open-design-questions)) |
| Redfish/BMC execution Jobs | Not implemented |

## Independent review of the recorded run

This review covered the run before Flux installation. The
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
