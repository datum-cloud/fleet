# Local Milo, Karmada and PXE acceptance

This opt-in harness runs native Milo project routing, actual Karmada delivery and
status reflection, and the [local VM site](local-vm.md) through the allocator and
[Device custody](../development/device-custody.md#cutover-increment-c). On the
selected cell, execution runs as Flux-owned Kubernetes Deployments and
kro-created per-assignment Jobs; see [Kubernetes-native site execution](site-execution.md).
Every long-running service is installed by each cluster's own Flux from a source
artifact built from the checkout; see [Flux-reconciled local environment](gitops.md). It is not enabled by the production manager or any
production overlay. Production authority and deployment equivalence remain
**unqualified**.

Status: the run on Device custody is **implemented**. Verification of the local VM
run and of the [Redfish run](redfish-lab.md) on custody is pending. The run
records under [Verification and remaining gaps](#verification-and-remaining-gaps)
were observed on the path before the cutover, with the Secret-backed coordinator,
and are not evidence for the custody path.

Read the [writer placement contract](../architecture/README.md#control-plane-placement-and-writers),
[pinned design sources](../architecture/design.md#source-proposals), and
[local VM site](local-vm.md) first.

The [Redfish mode](redfish-lab.md) (`task multiplane:redfish-acceptance`) runs the
same composition with the Redfish site workers. Its workers have no libvirt socket
and reach two persistent lab VMs only through the DMTF Redfish Interface Emulator.

## Topology and versions

| Surface | Local implementation | Writer |
| --- | --- | --- |
| Provider project | Milo v0.32.5 native `/projects/provider/control-plane` route; dedicated API Deployment and etcd StatefulSet with its own PVC on the platform kind cluster, reconciled by Flux. It holds the inventory and custody CRDs, the Devices, `DeviceCustody`, the operations and the MachineAllocations. It enforces the custody CRD rules. It registers no creator webhook. | Bootstrap fixture supplies definitions and observations. Each component has its own token: the custody coordinator (the only custody writer), the Metal provider (allocations and installations), the release controller (wipes), and a discovery identity (fixture discovery). |
| Consumer A / B | Two independently restartable Milo v0.32.5 Deployments and etcds, one Flux Kustomization each; native project routes; both use namespace `default` and Machine name `allocation` | Separate customer credentials create/watch requests; management writes public status |
| Custody coordinator | A workstation process for the run, started by `hack/multiplane/coordinator.sh` | The coordinator's own token. It admits operations, delivers each assignment through Karmada, and accepts the cell's results by fence. |
| Coordination intent | Karmada v1.16.0 API (hub Flux `HelmRelease`), separate from the host Kubernetes API | The coordinator's credential creates immutable handoffs and labels them answered; it cannot write status |
| Selected cell | Separate kind Kubernetes API running its own Flux, the Flux-owned shared site services, kro v0.9.4 and the `HandoffDispatcher` graph | Karmada delivers intent; the site observer ServiceAccount is the only local status writer |
| Reflected results | Karmada interpreter aggregates each member's identity and observation | Karmada; management validates before projection |
| Physical effects | Privileged, Flux-owned VM runtime Deployment (libvirt/QEMU) on the cell; amd64 guest under native arm64 TCG | One bounded kro-created worker Job per assignment runs the site worker; the site store holds each Device's fence, journal and results, and the boot evidence, on a retained, Flux-owned PVC |

The platform integration choices were discovered in these connected sources:

- Fleet's `go.mod` pins Milo **v0.32.5**. The local Milo checkout reviewed was
  `3c2a160d6d5614d6dc7bf119b8d022fc641c5d4f` (v0.32.5 plus later commits).
  Its `pkg/server/filters/projects.go` implements project path routing and
  `internal/apiserver/storage/project/mux.go` uses `/projects/<project>` etcd
  prefixes. The harness runs the released v0.32.5 image, not that dirty checkout.
- `datum-cloud/test-infra` federation checkout
  `3619e71d4686c3d1ead01f08a1136f7d272b3344` pins the Karmada Helm chart to
  **v1.16.0**. Its Taskfile and `components/karmada/values.yaml` supplied the
  bootstrap model. This harness keeps fsync enabled; it does not adopt that
  checkout's `unsafe-no-fsync` tuning or insecure TLS kubeconfig rewrites.
- The connected Compute federation interpreter supplied the supported Karmada
  `statusReflection` / `statusAggregation` customization interface. Fleet's
  interpreter preserves *all* reflected replicas and their Karmada cluster
  names, rather than accepting the first result.
- kind nodes use **v1.35.0**, digest-pinned to the already exercised local VM
  node image. Karmada's chart pins its own API/controller images to v1.34.1 and
  its etcd to 3.6.0-0. Milo's local etcd uses 3.5.16-0. The Milo arm64 image is
  digest-pinned in the launcher. Some upstream Helm hook images still use
  floating tags; this is a remaining reproducibility seam.
- kro **v0.9.4** is installed on the cell by a Flux `HelmRelease` from its
  official OCI Helm chart, pinned by manifest and image digest. The newer v0.10.0 was five days old and is
  not used. See [versions](site-execution.md#versions).

- Flux **v2.9.6** runs on each of the three kind clusters; see
  [versions and pins](gitops.md#versions-and-pins) for why not v2.10.0.

Independent local Milo roots make project process/storage failures independently
injectable without a hand-written proxy. Each is a Flux-managed Deployment with
its own etcd StatefulSet and volume on a dedicated platform kind cluster, not a
bare Docker container. **This is still a local placement seam:** it is not the
platform's complete root/ProjectControlPlane provisioning machinery, Kyverno
orchestration, shared root IAM, or project retirement path. Milo's
native project routing and project storage are real. Separate roots and etcds
prevent namespace-based isolation shortcuts. The coordinator and the
management phases run on the host, each with its own credential, against the
provider project's API and the Karmada API.

## Run

Prerequisites: Go from `go.mod`, Python 3.12+, OpenSSL, Docker, kind, kubectl,
kustomize, curl, Git, Colima, network access to GitHub releases, `ghcr.io`,
`registry.k8s.io` and Docker Hub, and the task-owned native arm64 VZ Colima profile `fleet-acceptance-tcg`
with 4 CPUs, 8 GiB memory, amd64 container emulation, and at least 12 GiB free in
its Docker data root. Never use `fleet-e2e-x86`. The harness never changes the
global Docker context, never creates a Colima profile, and never stops or deletes
another run's containers. Stopped diagnostics from earlier runs are left alone;
only *running* `fleet-local-*`/`fleet-mp-*` containers block a launch.

One command runs preflight, a fresh topology, every acceptance phase, sanitized
evidence, and owned-resource teardown:

```sh
task multiplane:acceptance
# equivalent:
FLEET_MULTIPLANE_INOTIFY=1024 hack/multiplane/run.sh
```

`FLEET_MULTIPLANE_INOTIFY=1024` (set by the task) is the explicit opt-in for a
reversible, nonpersistent `fs.inotify.max_user_instances` adjustment inside the
task Colima VM only; the original value is restored at teardown or stop and both
values are recorded in `sysctl.json`. Options:

| Option | Effect |
| --- | --- |
| `--preflight-only` (`task multiplane:preflight`) | Run every check below, create nothing, exit |
| `--bootstrap` | If the existing `fleet-acceptance-tcg` profile is stopped, start it with its saved configuration |
| `--keep` | After a pass, stop instead of delete this run's containers |
| `--leave-running` | After a failure, leave containers running for live diagnosis instead of stopping them |
| `--skip-readiness-tests` | Skip the implementation test gate (diagnosis only) |

Preflight records `evidence/preflight.json`. Prerequisite checks cover tools,
the exact Docker context socket, reachability, architecture, memory, CPUs, free
disk, amd64 emulation, inotify, and concurrent Fleet runtimes. Readiness checks
require the CLI subcommands and location-denial error the harness drives, then
run `go test -race -count=1 ./internal/coordination ./internal/custody
./internal/site/... ./internal/multiplane ./cmd/fleet-multiplane`, plus
`./internal/redfish` in Redfish mode. Failing either blocks before any resource is
created.

Exit codes and `evidence/result.json` status:

| Exit | Status | Meaning | Resources |
| --- | --- | --- | --- |
| 0 | `passed` | Every phase and evidence assertion passed, and the run reached its last line | Owned containers, clusters and image tag deleted; private credentials removed |
| 0 | `preflight-passed` | `--preflight-only`: every prerequisite is present; no acceptance ran | None created |
| 1 | `failed` | A phase or assertion failed | Owned containers stopped, state retained, custody Unknown |
| 2 | `preflight-failed` | A local prerequisite is missing | None created |
| 3 | `blocked` | Implementation readiness gate failed | None created |
| 4 | `timeout` | A bounded phase expired; its process group was terminated | Stopped and retained as for 1 |
| 64 | `usage` | Invalid option | None created |
| 129, 130, 143 | `interrupted` | The run received HUP, INT or TERM. It stops every phase it started, records the phase it was in as `interrupted`, and names it as the failed phase | Stopped and retained as for 1 |
| 1 | `interrupted` | The run exited 0 before its last line, so nothing proves it finished | Stopped and retained as for 1 |

Only a run that reaches its last line writes the completion marker the result
requires for `passed`. A result recorded as `passed` before this rule, by a run
that was interrupted, proves nothing.

The full run uses three kind nodes (platform, hub, cell), a registry, Flux on
each node, three small Milo/etcd pairs, Karmada and kro. Node caps are
2300 MiB (platform), 2600 MiB (hub), 4400 MiB (cell) and 128 MiB (registry).
They total about 9.2 GiB and deliberately overcommit the 8 GiB VM, because
actual usage is much lower. In a debug run the cgroup peaks were 1.99 GiB for
the platform node (under reclaim pressure at the earlier 1900 MiB cap), 2.06 GiB
for the hub and 1.6 GiB for the cell after its restart; VM usage was about 4 GiB
after the install. Re-measure with `docker stats` and each node's
`memory.peak` if you change workloads. The cell holds the cell API, Flux, kro and the site, including the
VM runtime Pod, whose limit is 3 GiB. Each Milo API has a 384 MiB cap. The VM
guest is configured with 2 GiB and 2 vCPUs, using TCG. A 2.5 GiB provider cap
caused an installer OOM in an earlier fresh run. Evidence fails on any OOM kill
in the runtime Pod, any kind node, or the site, kro, Flux, Karmada or Milo containers. Expect 20 to 60 minutes,
mostly bootstrap, image pulls and builds. Every phase has an explicit bound.
Do not run another full acceptance topology concurrently on an 8 GiB VM.

### State and evidence layout

All containers have one fresh `fleet-mp-<random>` prefix. The command prints a
mode-0700 `/tmp/fleet-multiplane.*` state directory (override the parent with
`FLEET_MULTIPLANE_STATE_ROOT`). Only its `evidence/` subdirectory is shareable:

| File | Content |
| --- | --- |
| `result.json` | Final status, exit code, failed phase, every phase record, retained containers, custody statement |
| `phases.jsonl` | One record per bounded phase: name, status, exit code, bound, duration |
| `preflight.json` | Each prerequisite and readiness check |
| `summary.json` | Written when the evidence checks pass, with `acceptance: evidence-verified`: allowlisted scenario evidence, operations, digests, receipt hashes, qualification flags. It does not say the run passed; `result.json` does |
| `flux-verify.json`, `flux-initial.json`, `flux-rbac-restore.json`, `flux-drift.json`, `flux-graph-lifecycle.json`, `flux-restart.json`, `flux-fail-closed.json`, `flux-retention.json`, `flux-final.json`, `flux-ownership-final.json`, `flux-reconciles.json`, `flux-partition.json`, `flux-reflection-suspension.json`, `gitops-r<N>.SHA256SUMS` | Flux source revisions and digests, Ready/generation/revision of every Kustomization and HelmRelease, inventories and single-writer ownership, and each GitOps scenario ([details](gitops.md#acceptance-coverage)) |
| `kro-rbac.json` | kro's allowed and denied verbs and each admission probe's outcome |
| `custody-crd-rules.json` | Each forbidden custody or operation write and whether the provider project API refused it, and that the CRDs were removed |
| `kubernetes-execution.json` | kro version, image and RGD/instance state; Flux-owned site Deployments; retained volume identity; every worker Job's UID, conditions, Pods, exit codes and policies; per-operation RBAC |
| `interrupt.json`, `lifecycle.json`, `delivery-faults.json`, `wrong-location.json` | Scenario-specific observations, no secrets |
| `import-inert.json`, `staff-access.json`, `contention.json`, `release-waits.json`, `deadline-hold.json`, `deadline-settled.json`, `rebuild.json` | The #174 failure scenarios ([coverage](../../test/acceptance/coverage.md#device-custody-and-operations)) |
| `source-SHA256SUMS`, `binary-SHA256SUMS`, `image-id`, `sysctl.json`, `teardown.json` | Tested inputs and teardown record |

Before reporting, the harness scans `evidence/` for every retained static token,
ServiceAccount token (including the short-lived observer and worker probe
tokens), kubeconfig client key, fence and boot capability, and
redacts and fails on any match. Outside `evidence/`, `logs/<phase>.log` keeps
each phase's full output and the kubeconfigs, tokens, journals and binaries
remain private. Never publish the state directory itself.

### Acceptance phases

`hack/multiplane/acceptance.sh` lists the ordered phases; `steps.sh` implements
the harness-side ones and `k8s_evidence.py` records the Kubernetes facts. Before
acceptance starts, the run does the following:

- builds the current source and the site image, and verifies its binary checksum;
- verifies the pinned Flux CLI and install manifest;
- creates the run registry and the platform, hub and cell kind clusters, and
  pushes the site image (the cell pulls it by digest);
- publishes source revision 1 and bootstraps Flux on each cluster (the
  [bootstrap boundary](gitops.md#bootstrap-boundary)). Flux then installs
  Milo, Karmada, host RBAC, the Karmada API configuration, kro, the site base
  and the graph in dependency order;
- enrolls credentials, including one token per provider-project component, and
  seeds fixtures;
- starts the custody coordinator on the workstation (the `coordinator` phase,
  `hack/multiplane/coordinator.sh`), which also writes fixture discovery through
  its own discovery identity. Real BMC discovery is not part of this run;
- publishes revision 2, which adds the shared site services and the
  `HandoffDispatcher` with dispatch disabled. `site.sh check` requires the
  following:
  - the runtime, boot and observer Deployments Available from the published
    digest, each labelled as owned by the Flux `fleet-site-services` layer;
  - the boot Service at its fixed ClusterIP;
  - the dispatcher reporting the same worker image;
  - the volume `Retain`;
  - no worker Job;
- runs `flux-verify`.

The phases are then:

1. **Native Milo routing and RBAC.** Customer create/watch, with equal namespace and request
   names in separate project APIs. Exact Project and namespace UIDs distinguish
   lifetimes. `rbac` verifies against real API servers that the site observer
   ServiceAccount cannot create objects, write handoff specs, delete intent, read
   Secrets or create Jobs. It also checks forbidden project and hub writes, and
   that revoking the observer RoleBinding denies its reads. The cell's Flux,
   not the harness, then restores the binding. Finally, as kro's own identity,
   it proves the narrowed grant and the admission policies on kro's writes
   ([trust model](site-execution.md#ownership-and-trust-model)).
   `custody-crd-rules` then installs only the [Device custody](../development/device-custody.md)
   CRDs into the provider project. It requires the project API to refuse eight
   writes their CEL rules forbid: changing an operation's spec or creator,
   moving a phase backward or out of an ended phase, setting both authorities,
   lowering a custody fence, leaving `Retired`, and naming custody after
   another Device. It then removes the objects and the CRDs. Milo ignores
   admission policies stored in a project, so this proves the rules hold
   there. It sends nothing to any server.
   `import-inert` then requires that importing the Devices admitted,
   delivered and sent nothing: no operation, allocation, assignment, worker
   Job or journal entry, and custody with no operation, result or hold. In
   Redfish mode the BMC audit must hold no effect. `staff-access` requires
   the staff identity to read custody and list recoveries, and to be
   forbidden Secrets, every other operation, allocations and custody writes.
   Staff hold no consumer-project or hub credential. `commission` then takes
   the registered server from `Disabled` to `Available`
   ([Redfish lab](redfish-lab.md) describes the phase). Its burn-in runs in a
   worker Job, so `commission-dispatch` turns dispatch on (r3) while no
   assignment exists, and `commission-settle` records the commissioning's
   handoff as the only one and turns dispatch off again (r4) until kro has
   removed its Job. Later checks ignore exactly that handoff and Job.
2. **Contention.** Two consumers request one provider capacity. Custody grants one
   allocation; `verify-contention` independently reads Bound for A and
   Pending/InsufficientCapacity for B, and requires custody to name the one
   MachineAllocation, A's. `tenant-isolation` then requires that B, which
   holds nothing, cannot reach the server: its project serves no Device API,
   the allocator refuses to install for it, and an installation named for its
   unheld allocation is refused for `HolderMismatch`. The fence, the site
   store and, in Redfish mode, the BMC audit stay unchanged, and nothing is
   delivered.
3. **Admission before effects.** The coordinator admits the `DeviceInstallation`
   and raises the Device's fence, then writes one immutable hub handoff carrying
   that exact assignment, before any effect. `await-delivery` requires the
   following:
   - exactly one Karmada-delivered assignment handoff besides the
     commissioning's, which the cell retains after it succeeded;
   - kro reporting both handoffs in `HandoffDispatcher.status.deliveredHandoffs`;
   - no cell status for the install and no install worker Job.
4. **Partition.** Suspend consumer A's Flux Kustomization, stop its API and pause
   the hub. `dispatch` then publishes revision 5, which sets `dispatch: true`.
   The cell's own Flux reconciles it from the registry with no hub access. kro
   creates the per-operation ServiceAccount, Role, RoleBinding and Job from its
   local replica. It also recreates the completed commissioning's Job. The
   site already deleted that answered result, so the replay either loses the
   fence to the install worker (exit 65) or finds its deadline passed. Until
   the site runner answers a replay of an answered operation as a no-op,
   evidence accepts either outcome for that one Job, and only while the
   commissioning's journal and provider record, custody across the replay,
   and the answered hub handoff are unchanged, and no BMC effect came from its
   Pod's address while it ran. Every BMC effect in that time must come from
   another worker Pod, so an address the network rewrites fails the run. The
   graph lifecycle recreates that Job again and checks it the same way. Every
   other worker Job must be `Complete`.
5. **Pod interruption.** `interrupt-pod` waits until the Device's journal shows
   the install's installer boot requested *and* its libvirt domain running. It then
   force-deletes the worker Pod with no grace period. It requires:
   - no local result;
   - the Job controller to record the lost Pod and start a replacement in the
     same Job;
   - the same unfinished operation.
6. **Job interruption.** `interrupt-job` deletes the whole worker Job as soon as
   the replacement exists, still mid-install. kro must recreate the Job under
   a new UID for the same operation.
7. **Exact resume.** `resume` requires the recreated Job to be `Complete` with
   one succeeded Pod, and the observer to publish the stored `Succeeded` result.
   The Device's journal must show the installer boot requested exactly once. Evidence later proves exactly one
   firmware-network installer start followed by exactly one disk-only start.
   It uses libvirt's append-only QEMU log, plus one `sfdisk`, one installer
   receipt and one disk-boot receipt in the guest serial logs. `worker-rbac`
   then proves the worker token can read only its own handoff: no list, no
   other names, no status or spec writes, no Secrets, no site writes.
8. **Readiness under partition.** The platform's root sync first reconciles r5
   while consumer A stays suspended at the same UID and generation, with no
   Pods (`flux-partition.json`). Consumer A's Kustomization is then resumed and
   Flux restores its API. The install Job is
   `Complete` but public Ready must still be False while the hub is
   disconnected. Job completion is not readiness.
9. **Heal and project.** Restart the hub and the cell node; the cell restart
   recreates every site Pod from the retained volume. Flux on every cluster
   must return to Ready at the current revision. Karmada reflects the
   result, and the coordinator accepts it only if it is this cell's observation
   of exactly the assignment carried, with:
   - the assignment's digest, Device, operation and fence;
   - a result at the active operation's fence;
   - observation freshness.

   Public status then follows custody.

   Installation is repeated to confirm idempotent recovery.
10. **Pod and Job lifecycle.** `lifecycle` recreates the runtime, boot and
    observer Pods and deletes the completed install Job; kro recreates the Job,
    which replays a verified no-op. The following must be unchanged:
    - the site store's state;
    - QEMU start count;
    - PVC/PV identity and `Retain` policy;
    - domains;
    - consumer A's Ready/Bound allocation.

    The restarted observer must republish.

    The GitOps phases follow:
    - `flux-graph-lifecycle`: deleting the dispatcher removes only kro's
      per-operation objects, and shared services and storage are untouched;
    - `flux-drift`: Flux corrects drift, including the shared boot Service and
      observer;
    - `flux-restart`: controllers restart without duplicate effects;
    - `flux-fail-closed`: a broken kro release blocks only the dispatcher
      layers;
    - `flux-retention`: the site volume is retained when its claim is
      removed from the source.

    See [acceptance coverage](gitops.md#acceptance-coverage).
11. **Invalid and stale results.** With the Karmada `HelmRelease` suspended and its
    controller stopped, the harness reopens the answered install handoff and
    substitutes one attribute of the real reflected result at a time: wrong
    cluster name, foreign observer, duplicate replicas, a future time, wrong
    assignment digest, wrong Device, wrong operation, a stale fence, and missing
    reflection. The coordinator must ignore each: custody stays unchanged and the
    handoff stays unanswered. This is fault injection, not a replacement status
    relay.
12. **Release and reuse.** Release creates a `DeviceWipe` through the release
    controller. The wipe goes through the same federation path and kro-created
    Jobs. `release-status-a` requires consumer A's Machine to be Released with
    Ready False, no `currentInstallationRef`, and no Fleet finalizer, and its
    Installation Released. Custody then names consumer B's allocation, and B
    must show Bound under a higher fence. No mode here has Milo quota, so the quota
    claim is not exercised.
13. **Delivery faults.** `delivery-faults` injects three faults with cell
    bootstrap credentials:
    - the install assignment under a second name, which the API refuses;
    - an assignment naming another cell;
    - an assignment replayed below the Device's stored fence.

    Each of the last two creates a kro Job that must fail once with exit 65 by
    `podFailurePolicy`. The site store must be unchanged, no status may appear,
    consumer B must stay Bound, and kro must prune them once deleted. In
    Redfish mode the BMC audit must gain no effect.
14. **Deadline and Machine deletion.** `deadline-admit` arms a lab-only hook
    in the boot service, which then withholds every boot while staying Ready,
    and restarts the coordinator with a six-minute installation deadline for
    consumer B's installation only. kro still creates the worker, and the
    installer boot is requested but never served. `machine-delete` has the
    customer delete B's Machine mid-install. One release pass must leave
    custody holding the Device for release with the installation still active
    and no wipe. `deadline-hold` requires the installation to end
    `OutcomeUnknown` with `DeadlineExceeded`, custody to hold it at its fence,
    and its worker to have stopped. A staff `DeviceRecovery` naming the
    held operation at the wrong fence must be refused for `HoldMismatch`, with custody, the site
    store and, in Redfish mode, the BMC audit unchanged. The hook is then
    cleared. The release's wipe must be admitted above the held fence
    after the worker stopped, succeed, and clear the hold. `release-status-b`
    requires the deleted Machine to be gone.
15. **Custody rebuild.** `rebuild` stops the site observer, so the cell cannot
    report its fence, and deletes the free Device's custody as the provider
    bootstrap admin. The coordinator must rebuild it `Unverified`, and a new
    consumer A Machine must find no eligible capacity and record no
    allocation. Once Flux restores the observer, custody must become
    `Verified` with a fence above every fence the site stored.
16. **Wrong location.** With capacity free, the consumer A customer recreates its
    request with `locationRef: remote-elsewhere`. Allocation must fail with the
    location-not-served error. The custody records, the site store's state, and the
    Machine's finalizers and status must be unchanged. Free capacity rules out contention as the reason for denial.
17. **Closed boot and evidence.** Live HTTP probes from the runtime Pod to the
    boot Service are denied after closure. Evidence requires all of:
    - PXE mode, one install and two wipes, each `Succeeded` at its own fence,
      and B's installation `OutcomeUnknown` at its deadline, with the fences
      strictly increasing;
    - completed broker writes and a DHCP lease for the installer NIC;
    - network-only installer XML, and disk XML with no NIC or direct kernel;
    - closed boot admission, no remaining domain or disk;
    - kro-created, `Complete` worker Jobs for exactly these four operations,
      each with one successful Pod;
    - custody `Verified` with no holder at the rebuilt fence, and only the
      refused recovery;
    - zero OOM kills.
18. **Final convergence and teardown.** `flux-final` requires all three
    clusters at the last revision with nothing suspended, and repeats the
    single-writer ownership audit. After a pass, only exact-prefix resources are deleted, the
    inotify value is restored, and private credentials are removed.

### Failed or interrupted runs

Failures stop this run's coordinator process and its exact containers (never
delete them) and retain the full private state; execution and custody are **Unknown**. `result.json` names the
failed phase and the retained containers. Stopping is not release evidence:

```sh
hack/multiplane/stop.sh /tmp/fleet-multiplane.REPLACE      # stop only; idempotent
hack/multiplane/teardown.sh /tmp/fleet-multiplane.REPLACE --discard-unknown
```

`teardown.sh` without `--discard-unknown` refuses unless `summary.json` records
verified acceptance evidence. `--discard-unknown` is an explicit human decision after inspection: it
deletes the same exact-prefix resources, keeps every private file, and records
in `teardown.json` that it is not release evidence. Neither script touches other
prefixes. The original limit of 128 inotify instances was insufficient for this
API count: the real Karmada webhook exited with `too many open files`. The bounded
value 1024 resolves that local prerequisite.

Offline harness tests run in CI and locally with `task multiplane:test-harness`.
They use fake runtime tools to cover preflight outcomes, Flux source staging
(credential refusal, revision variants, rendering), push records, reconcile waits
(stale acknowledgements rejected, stripped requests reissued), fault-guard
suspension and root-revision receipts, bounded phase timeouts
that kill the process group, sanitized journal observation, secret detection,
result mapping, and effect-free teardown refusal.

## Implementation and trust boundaries

**The coordinator.** The custody coordinator (`fleet-multiplane coordinator`) is a
workstation process with its own token. Its loop (`internal/delivery`) reconciles
each Device's custody, builds the assignment for the active operation from custody
and the operation spec as admitted, creates its handoff, collects the cell's
results, and runs the retention sweep. The coordinator configuration names the
cell as the site serving each Device. It is the only custody writer. The Metal
provider creates allocations and installations, and the release controller
creates wipes, each with its own token. An operation's creator is the identity a
`SelfSubjectReview` reports. The local Milo enforces the custody CRD rules and
registers no creator webhook, so this run does not prove that rule.

**Handoffs.** `internal/coordination.Hub` creates an immutable
`MachineAllocationHandoff` under `coordination.fleet.datumapis.com/v1alpha1`,
labelled with the cell and a role. An assignment handoff carries one assignment
(`spec.device` and `spec.assignmentJSON`). A report handoff has no assignment and
asks for a Device's fence report. The assignment is stored as a JSON string so
API-server normalization cannot reorder it and change its digest. `spec` is
immutable through CRD CEL. The name derives from the Device, the operation UID and
the fence, so a changed assignment under the same name conflicts. kro creates a
worker Job only for an assignment handoff.

`internal/coordination.Cell` is split into two least-privilege roles. Both
validate that the delivered handoff names this cell, that its name is derived
from its assignment, and that the assignment is valid before anything else.

- **`Cell.Work`** runs in the per-assignment worker Job (`fleet-multiplane
  site-worker`). It runs the assignment through the site runner, which fences,
  checks and journals every step. A `DeviceRecovery` only raises the fence and
  confirms the older worker stopped. An assignment below the Device's stored fence
  is refused with exit 65. `Work` never writes status.
- **`Cell.Publish`/`Cell.Observe`** run in the observer Deployment
  (`fleet-multiplane site-observer`), which reads the site store read-only. Every
  10 seconds it lists this cell's handoffs and publishes, for each, the Device's
  seen and stopped fences and, for an assignment, its stored result. It deletes
  the site's copy of a result once the coordinator has labelled the handoff
  answered.

Retries, Pod replacement and dependency ordering belong to the Job controller
and kro. Neither role holds a hub, consumer or provider-project credential. The
coordinator accepts only this cell's observation of exactly the assignment the
handoff carries. This proves recoverable operation completion, not ongoing guest
health or live production hardware attestation.

Allocation serves exactly one location, `local`, bound to the configured cell.
`Runner.request` rejects any other Machine `locationRef` or an empty
`Config.Cell`. It returns `ErrLocationNotServed` before the quota claim, the
allocation or any finalizer. The CLI treats this rejection, conflicts, fencing
and invalid requests as terminal instead of retrying until timeout.

Release projection runs only after the allocation is `Released`, which needs every
release gate closed and custody back at `None`, so finalizer removal is safe. Each step is
retry-safe. The referenced Installation is retired first (`Released`, Ready
False), then `currentInstallationRef` is cleared and the Machine is published
`Released` with Bound and Ready False. The Fleet finalizer is removed last, from
the server's fresh copy, so the removal is not reverted. Reuse writes consumer B's
Bound status only from the allocation custody committed. The CRD types
Installation `status.phase` as a free-form string with no enum or CEL rule, so
`Released` is schema-valid. `release-status-a` reads it back from the real Milo
API.

The site enrolls each Device it serves. The local VM site's enrollment is
`config/site/base/enrollment.json`, and the Redfish site's is built by
`redfish-enroll`. Fixture access checks it against the admitted snapshot before
any effect. No Device, capacity, allocation or Installation identity is
substituted for another.

Cell site identities are as follows:

- **Observer** (static Role): get/list/watch handoffs and update handoff
  status only.
- **Workers** (one kro-created Role each): `get` on exactly one handoff by
  `resourceNames`, with an automounted short-lived projected token.
- **kro** (aggregated read-only ClusterRole plus two namespaced Roles): reads
  handoffs, the Flux-owned shared services and the PVC cluster-wide. It writes
  only Jobs and ServiceAccounts in `fleet-site`, and Roles and RoleBindings in
  `fleet-coordination`. Two admission policies pin those writes to the
  per-operation shape. It has no Deployment, Service, volume, Secret or
  handoff spec/status write.

The coordinator's hub Role grants handoff create, get, list, watch and patch (to
label a result answered, while the spec stays immutable), but no status writes. Karmada uses a separate member
ServiceAccount with namespace-scoped intent writes. It also has cluster-wide
read/watch for handoffs, because its member informer lists that resource across
namespaces, and it can read nodes/pods/namespaces and discovery/health endpoints.
It has no Secret-reading permission or local status-writing grant. Coordinator and
Karmada tokens are 24-hour local ServiceAccount tokens; the harness does not
refresh them for long-running deployments. Bootstrap administrator credentials
remain on the host. No cell credential leaves the cell.

Milo static-token RBAC grants live in the native root RBAC store, where its RBAC
authorizer reads them. Customer tokens cannot write status or read Secrets. The
coordinator, the Metal provider, the release controller and the discovery identity
each have a separate provider-project role (`hack/multiplane/milo.py`). The
management phases use separate project read/status roles. Tests also delete the
cell RoleBinding, verify denial, and restore that exact binding. These tests
establish Kubernetes RBAC behavior, not caller-preserving production Milo IAM.

The local runner's catalog, publication, hardware observations, source-use
admission and fixture cleaning policy are explicit synthetic fixtures, and no
quota system serves the run. The MachineAllocation, custody and operation records
are durable before effects. Networking is deliberately empty; no NSO/IPAM/routing
implementation is substituted. Adoption and production service readiness are never
enabled.

## Verification and remaining gaps

The records below, up to [Offline checks](#offline-checks), were observed on the
path before the cutover to Device custody. They used the Secret-backed
coordinator, a site-wide provider journal and a single-writer `executor.Client`.
They show what the earlier composition proved, and the Flux, kro and Karmada
findings there still describe the same infrastructure. They are not evidence for
the custody path. Verification of the local VM run on custody is pending.

### Flux-owned shared site acceptance (2026-11-16)

This run verifies the current ownership split: the cell's Flux owns the shared
site services and storage, and kro owns only per-operation workers (see the
[trust model](site-execution.md#ownership-and-trust-model)). It supersedes the
ownership claims of the runs below, which verified kro-owned shared
Deployments. It re-establishes every other scenario of the
[acknowledgement run](#flux-acknowledgement-acceptance-2026-11-15).

`task multiplane:acceptance` exited 0 at 00:07 UTC on November 16, 2026, with
the readiness test gate enabled. Run details:

- state `/tmp/fleet-multiplane.WkV4ul`, prefix `fleet-mp-ed5636ae`;
- all 54 bounded phases passed (one more than before: `flux-graph-lifecycle`),
  in 25.6 minutes from build to teardown with cached base images;
- SHA-256 of `source-SHA256SUMS` (155 entries):
  `7faa0832c7b543d2504398b462b117ff1bd00cbea721f550b56d23778a17d31e`;
- site binary `ff5c1b26…077a` and host binary `3e03201a…e4b4`, unchanged, because
  no Go code changed;
- site image `sha256:66cdc9d8c6cd010825de447f8aa3b108d35bcf93531e6e255e13c5a56dee48ad`,
  pulled by the cell by that registry digest;
- seven source revisions from the dirty checkout at HEAD `d7ff793b`, 58 files
  each: r1 `4d743778…`, r2 `8e4b05f8…` (shared services and dispatcher), r3
  `84ff7ff0…`, r4 `939fae00…` (bad kro chart), r5 `7f319b98…`, r6 `1afccbf1…`
  (claim removed), r7 `cc786e40…`. All three clusters ended at r7.

New claims, from the sanitized evidence:

- **Single writer per object** (`flux-verify.json`, `flux-ownership-final.json`).
  Before dispatch, 20 shared site objects each carried exactly one Flux layer
  label, were in that layer's inventory, and had no kro label or owner
  reference. The services, graph and dispatch inventories held exactly their 5,
  1 and 1 objects, and kro had written nothing. At the end, the same 20 objects
  passed again. kro had written exactly 12 objects: the Job, ServiceAccount,
  Role and RoleBinding for the install and both cleanups. Every shared
  Deployment was labelled `fleet-site-services` with no kro label
  (`kubernetes-execution.json`).
- **kro's narrowed authority** (`kro-rbac.json`). As kro's identity, 8 worker
  verbs were allowed and 15 others denied. Three worker shapes were admitted by
  server-side dry run, and 8 violations were each denied by a
  ValidatingAdmissionPolicy. Every real worker Job and its RBAC, including the
  three delivery-fault Jobs, passed the policies. The chart grants kro keeps
  are recorded: CRD create/delete and Leases.
- **Graph lifecycle does not touch shared infrastructure or storage**
  (`flux-graph-lifecycle.json`). Deleting `HandoffDispatcher/dispatcher`
  (UID `e4c7eb92…`) removed exactly the install's four `w-op-1325c74…`
  objects. All 22 tracked identities kept their UIDs: the shared objects, the RGD
  and the StorageClass. PVC `bb97aa91…`, the journal (`92ad2c9e…` before and
  after) and custody were unchanged, and the handoff stayed `Succeeded`. Flux
  recreated the instance (UID `65d41287…`), kro recreated the four objects,
  and the Job replayed a verified no-op with exit 0. One orphaned, `Succeeded`
  Pod from the deleted Job is recorded.
- **Flux drift repair of shared services** (`flux-drift.json`). The deleted
  boot Service was recreated by the services layer under a new UID
  (`ace32e5d…` → `26767a93…`), at the same ClusterIP `10.96.0.80` and with an
  unchanged spec. The closed-boot probes later ran through it. The observer
  Deployment scaled to 0 was restored with the same UID (generation 1 → 3). The
  earlier three cases, kro, the Karmada interpreter and the widened kro grant,
  were corrected as before.
- **kro failure does not gate shared infrastructure** (`flux-fail-closed.json`).
  With r4, the kro chart source and Kustomization failed and the graph and
  dispatcher reported `DependencyNotReady`; the dispatcher marker stayed r3.
  The base and services layers applied r4 with every shared UID and Deployment
  generation unchanged. r5 converged.
- **Bootstrap gating observed before apply** (`flux-verify.json`). Gating was
  observed on the hub (Karmada coordination, API and members) and on the cell
  (federation, site base, graph), each with nothing applied.

Re-established, as in the earlier run:

- **PXE.** Worker Pod force-deleted at `installer-started` with its domain
  running and replaced in the same Job. Job deleted mid-install and recreated
  by kro. One install operation recorded one network-installer start and one
  disk-only start.
- **Recovery.** Partition kept public Ready False with the Job Complete.
  Suspension receipts, heal, the idempotent install, lifecycle recreation and
  retention all passed (PVC `bb97aa91…`, PV `27b3cd4d…`, `Retain`).
- **Acknowledgement and restart.** All 21 convergence waits required fresh
  acknowledgements; 12 stripped requests were reissued. All 12 controller Pods
  were replaced, every object acknowledged a post-restart token, and both Helm
  releases stayed at version 1.
- **Rejections and denials.** Nine reflection faults were rejected, and release
  and reuse passed across epochs 1 and 2 with two cleanups. Three delivery
  faults failed by `PodFailurePolicy` at exit 65 and were pruned by kro. Wrong
  location was denied with free capacity, and the closed-boot denials passed.
- **Memory, secrets and teardown.** Zero OOM kills; the cell node recorded 89
  `memory.max` events, more than the 17 of the earlier run. 33 evidence files
  were clean against 23 retained secrets. Teardown removed every
  `fleet-mp-ed5636ae` resource and private credential, and inotify was
  restored from 1024 to 128.

Three earlier fresh attempts of this change failed before any allocation or
effect. Each was inspected and then discarded with `teardown.sh
--discard-unknown`, which is not release evidence:

1. `/tmp/fleet-multiplane.LpvtO5` (`fleet-mp-a84271ad`) failed in
   `flux-platform`. kro rejected the RGD because `runtime` is a reserved
   resource ID in kro v0.9.4. The ID is now `hypervisor`, and `test-harness.sh`
   checks every ID against kro's reserved list.
2. `/tmp/fleet-multiplane.XjeuF8` (`fleet-mp-39f70830`) failed in `flux-verify`,
   on a false positive in the bootstrap ordering check. Its ownership audit had
   passed. See
   [bootstrap gating evidence](gitops.md#review-follow-up-suspension-and-reconcile-evidence).
3. `/tmp/fleet-multiplane.jnCCQ9` (`fleet-mp-0e269a33`) failed in `rbac`. The
   live probe found that the kro chart grants cluster-wide ConfigMap writes,
   which kro does not use. The HelmRelease now removes them.

Before the live runs, the admission policies, the narrowed grants and the
scoped Flux identities were also exercised against a disposable envtest
kube-apiserver v1.36.2 with the kro v0.9.4 controller built from its tag
(`4ba0e410`). Valid worker objects were admitted and violations denied. The
RGD became `Active`, with readiness gating on the referenced Deployments.
Deleting the instance removed only worker objects. This was a manual
pre-check, not a repository test.

The run has not yet been independently reviewed.

### Flux acknowledgement acceptance (2026-11-15)

This run supersedes the partition and restart claims of the
[earlier Flux-reconciled run](#flux-reconciled-acceptance-2026-11-15). It
re-establishes every other scenario of that run.

`task multiplane:acceptance` exited 0 on November 15, 2026 at 21:40 UTC, with
the readiness test gate enabled. Run details:

- state `/tmp/fleet-multiplane.Ufjnxs`, prefix `fleet-mp-677f3c68`;
- all 53 bounded phases passed, in 25.6 minutes from build to teardown with
  cached base images;
- SHA-256 of `source-SHA256SUMS` (152 entries, now including
  `config/base/crd`): `a1a33d8944ba82f9fbd014c13092aca8151e2730210939cb2c6fb59fdf12f2f3`;
- site binary `ff5c1b26…077a` and host binary `3e03201a…e4b4`, unchanged from
  the earlier run;
- site image `sha256:efacf7ac9c17d83dab387683a3dc74561d5efce70447975e45b07cb72b31e2f3`,
  pulled by the cell by that registry digest;
- seven source revisions from the dirty checkout at HEAD `d7ff793b`, 55 files
  each: r1 `497fc276…`, r2 `31f19f68…`, r3 `63d46be3…`, r4 `8ee7a6b1…` (bad
  kro chart), r5 `79855353…`, r6 `8bfa826c…` (claim removed), r7 `8cdc7270…`.
  All three clusters ended at r7.

The sanitized evidence records the following new claims.

- **Partition suspension held** (`flux-partition.json`). Consumer A's
  Kustomization kept UID `c58c9f19…` at generation 2, suspended, with zero
  Deployment replicas and no Pods, from `partition` through
  `partition-readiness`. In between, the platform source and root sync
  acknowledged fresh requests and applied r3 (`rootReconciledDuringFault`).
  Flux made no concurrent repair. The harness then resumed it and Flux, not the
  harness, restored the API.
- **Reflection-fault suspension held** (`flux-reflection-suspension.json`).
  The Karmada HelmRelease kept UID `41638898…` at generation 2, suspended, with
  the controller at zero replicas and no Pods. That was checked before each of
  the nine injected faults and again before resuming.
- **Acknowledged convergence** (`flux-reconciles.json`). All 20 convergence
  waits, across bootstrap, revisions, heal, restart and final, required each
  source and Kustomization to report `lastHandledReconcileAt` equal to a fresh
  token, Ready at the current generation and revision. Fifteen requests were
  removed by a parent apply before being handled, and were reissued: the cell
  root six times, cell `kro` four, hub `karmada` three, platform root two. Every
  final acknowledgement matched the latest token.
- **Restart re-reconciled every object** (`flux-restart.json`). All 12
  controller Pods on the three clusters were replaced (no UID kept). Every
  source and Kustomization then acknowledged a post-restart token, and both
  HelmReleases acknowledged theirs with no reissue. Helm release versions
  stayed at 1; inventories, Jobs, the journal, PVC/PV and custody were
  unchanged.

The other scenarios passed as in the earlier run:

- **PXE.** The worker Pod was force-deleted at `installer-started` with its
  domain running, and replaced in the same Job. The Job was then deleted and
  kro recreated it under a new UID. One install operation recorded one
  network-installer start and one disk-only start.
- **Lifecycle and recovery.** Partition kept public Ready False with the Job
  Complete. Heal, the idempotent install and lifecycle recreation kept the
  journal and volume unchanged.
- **Flux.** Drift was corrected by Flux (kro same UID at release v1, Karmada
  interpreter recreated, kro grant reverted). The observer RoleBinding was
  restored under a new UID. r4 failed closed with `OCIArtifactPullFailed` and
  `DependencyNotReady`, and the FleetSite stayed at r3. r6 and r7 retained PVC
  `2c18ea7a…` and PV `45670d5d…` (`Retain`).
- **Rejections and denials.** All nine reflection faults were rejected. Release
  and reuse passed in epochs 1 and 2, with two cleanups. Three delivery faults
  failed by `PodFailurePolicy` at exit 65. Wrong location was denied with free
  capacity, and six closed-boot denials were recorded.
- **Memory.** There were zero OOM kills. The cell node recorded 17
  `memory.max` events.
- **Secret scan and teardown.** 30 evidence files were clean against 23
  retained secrets. Teardown removed every `fleet-mp-677f3c68` container,
  cluster, image tag and private credential. inotify was restored from 1024 to
  128.

The first fresh attempt, `/tmp/fleet-multiplane.Xk3Nc9` (`fleet-mp-34c74885`),
failed in `flux-platform` after 11 phases, before any credential, fixture or
effect. The cell root sync was Ready at r1, but its `lastHandledReconcileAt`
stayed null for the 1500-second bound. Inspection showed the request annotation
and its `kubectl-annotate` field manager were gone five seconds after the
request: the root's own apply had removed them. The harness fix is described
under [suspension and reconcile evidence](gitops.md#review-follow-up-suspension-and-reconcile-evidence),
and `test-flux.py` covers it. That topology was discarded with
`teardown.sh --discard-unknown`, which is not release evidence.

### Flux-reconciled acceptance (2026-11-15)

Superseded for the partition and restart claims by the
[acknowledgement run](#flux-acknowledgement-acceptance-2026-11-15). Its harness
suspended Kustomizations with a `kubectl`-prefixed field manager and accepted
existing Ready status after the restart. The record below is kept as it was
observed.

`task multiplane:acceptance` exited 0 on November 15, 2026 at 20:16 UTC, with
the readiness test gate enabled. Run details:

- state `/tmp/fleet-multiplane.POC1nn`, prefix `fleet-mp-cff2f712`;
- all 53 bounded phases passed, in 22.4 minutes from build to teardown with
  cached base images;
- SHA-256 of `source-SHA256SUMS` (149 entries): `f9cbb463949580c1b08216c3b7730a83a8a8faf63a6eff25f904672d08a4c2d2`;
- site binary `ff5c1b26…077a` and host binary `3e03201a…e4b4` (`binary-SHA256SUMS`);
- site image `sha256:cf6871facaefb2d577d5ece8cb7f94b7f6e3b43f3589eb8d22b16890f20b8112`,
  pulled by the cell by that registry digest.

The 39 scenarios of the earlier run are preserved, as the same phases or
documented equivalents:

| Earlier phase | Now |
| --- | --- |
| `milo-bootstrap` (Docker Milo/etcd) | `milo-bootstrap` (credential enrollment and fixtures) plus `flux-milo`, against Flux-managed Milo workloads |
| `federation-bootstrap` (`helm install` Karmada) | `topology`, `flux-bootstrap-*`, `flux-platform`, `federation-enrollment` |
| `site-image-load` (`kind load`) | `site-image-push` (registry, pulled by digest) |
| `kro-install` (`helm install`) | `flux-platform` (cell `HelmRelease kro`) |
| `site-install` (`kubectl apply`) | `flux-site` (revision 2) and `site-install` (checks only) |

The 14 new phases are `flux-tools`, `topology`, `flux-source`, three
`flux-bootstrap-*`, `flux-milo`, `flux-verify`, `flux-drift`, `flux-restart`,
`flux-fail-closed`, `flux-retention`, `flux-final` and
`federation-enrollment`. The sanitized evidence records the following.

- **Source.** Seven revisions were pushed from the dirty checkout at HEAD
  `d7ff793b`, each with its per-file SHA-256 list (55 files):
  - r1 `38e1c0b6…`, r2 `470cae7a…`, r3 `d26bf3d5…`;
  - r4 `a3931e63…` (bad kro chart), r5 `123ccb2c…`;
  - r6 `b826eb5b…` (claim removed), r7 `d3a9fdf8…`.

  All three clusters ended at `local@sha256:d3a9fdf8…3122dff82`.
- **Flux.** Flux v2.9.6 ran with the CLI and `install.yaml` digests verified,
  and the four controllers digest-pinned on each cluster.
  - Every Kustomization and HelmRelease was Ready at the current generation
    and revision.
  - The Karmada chart artifact digest was `773ab194…34fe`, release v1. The kro
    release recorded chart `0.9.4+4e8de4d34b7e`, release v1.
  - `DependencyNotReady` was observed for 13 Kustomization states across the
    cell and hub chains. Six bootstrap observations had no applied revision or
    inventory; seven during r2 retained their r1 revision and inventories.
- **Ownership.** The `fleet-site` inventory was exactly the FleetSite. The six
  generated site objects carried kro node labels and no Flux label or
  inventory entry. No Job, Pod, handoff or Secret appeared in any Flux
  inventory.
- **Drift.** These changes were corrected by Flux, not by the harness:
  - the revoked observer RoleBinding was restored under a new UID;
  - the kro Deployment scaled to 0 was restored by helm-controller, with the
    same UID, still release v1, and a drift event recorded;
  - the deleted Karmada interpreter was recreated over the remote kubeconfig;
  - a widened `fleet-site-kro` grant was reverted.

  The journal, Jobs, PVC/PV and custody did not change.
- **Restart.** Every Flux controller Pod on the three clusters was replaced
  and managed objects were observed Ready. That historical run did not check
  acknowledgement of each new reconcile request, so it does not prove every
  object re-reconciled after restart. Helm release versions stayed at 1;
  inventories, Jobs, the journal and custody were unchanged.
- **Fail-closed.** With r4, the kro chart source failed with
  `OCIArtifactPullFailed` and the kro Kustomization with `HealthCheckFailed`.
  The graph and FleetSite were `DependencyNotReady`. The FleetSite stayed at
  r3 with marker `r3`, and the kro Deployment and site were unchanged. r5
  converged.
- **Retention.** r6 removed the claim from the inventory without deleting it.
  The PVC `05b3b322…` and PV `37468f59…` (`Retain`), the journal and custody
  were unchanged, and r7 re-adopted the same claim.
- **PXE.** The worker Pod was force-deleted at `installer-started` with its
  domain running, and the Job controller replaced it in the same Job. The Job
  was then deleted and kro recreated it. The run recorded one install
  operation, with one network-installer QEMU start followed by one disk-only
  start. Contention, partition (public Ready False with the Job Complete), heal,
  idempotent install, lifecycle, nine reflection faults, release and reuse,
  three delivery faults, wrong-location denial and six closed-boot denials all
  passed as before.
- **Memory.** There were zero OOM kills. The cell node recorded 172
  `memory.max` reclaim events and no OOM.
- **Secret scan.** 27 evidence files were clean against 23 retained secrets.
  Teardown removed every `fleet-mp-cff2f712` container, including the registry,
  all three kind clusters, the image tags and private credentials. inotify was
  restored from 1024 to 128.

Two earlier attempts with this design never reached a pass:

- `/tmp/fleet-multiplane.SQ5zAp` (`fleet-mp-bfe2f43e`) failed in
  `flux-bootstrap-platform`: the sync objects were applied in the same batch
  as the Flux CRDs.
- `/tmp/fleet-multiplane.SSCrNg` (`fleet-mp-8f36be4c`) was a diagnostic run
  left running after a `flux-verify` check rejected Flux's digest-suffixed OCI
  chart version. Its remaining phases were then driven piecewise to find two
  more harness defects: a restart check that compared Deployment rather than
  Pod UIDs, and a reworded log marker. It also showed the platform node under
  reclaim pressure at a 1900 MiB cap.

Both were stopped or discarded with `teardown.sh --discard-unknown`, which is
not release evidence. The fixes are in the recorded run.

### Kubernetes and kro execution acceptance (2026-11-15, superseded installation path)

This run installed kro, Karmada and the site imperatively, and ran Milo as
Docker containers. Its execution findings are re-established by the
Flux-reconciled run above.

`FLEET_MULTIPLANE_INOTIFY=1024 hack/multiplane/run.sh` (the command behind
`task multiplane:acceptance`) exited 0 on November 15, 2026 at 18:08 UTC. Run
details:

- state `/tmp/fleet-multiplane.3TSkFC`, prefix `fleet-mp-b6b817c8`;
- all 39 bounded phases in `evidence/phases.jsonl` passed, in about 8.5
  minutes from build to teardown with cached base images;
- SHA-256 of `source-SHA256SUMS` itself: `20ce9da37d883eb223604c6a46e325dadba90a13dab8d1694a95c63b52feb9aa`;
- site image ID `sha256:ab24def82ee40395698845b9e491c31a9012371c0d914d7da1a0324600024cb0`.

The sanitized evidence records the following.

**kro and the site** (`kubernetes-execution.json`):

- kro v0.9.4 ran image `registry.k8s.io/kro/kro@sha256:eaf9fbad…`, chart
  archive `ceeb6307…`, in RBAC `aggregation` mode.
- RGD `fleet-site` was `Active`. `FleetSite/site` was `ACTIVE`, with runtime,
  boot and observer Available, boot origin a ClusterIP, and three delivered
  handoffs.
- The site volume was `fleet-site-retain`/`Retain` and not managed by kro.
- Exactly three kro-created worker Jobs remained: one install (epoch 1) and
  two cleanups (epochs 1 and 2). Each was `Complete` with one succeeded Pod at
  exit 0.

**Interruption** (`interrupt.json`):

- With the install `installer-started` and its domain running, the worker Pod
  was force-deleted. The Job controller recorded `failed: 1` and started a
  replacement Pod in the same Job UID.
- The Job was then deleted, still `installer-started`. kro recreated it under
  a new UID, and its worker completed the same operation.
- The journal holds one install. libvirt's QEMU log shows exactly one
  firmware-network installer start followed by one disk-only start. The guest
  logs show one partition, one installer receipt and one disk-boot receipt.

**Worker RBAC:** the per-operation token could read its own handoff only.
`get` on another name, list, status and spec patches, Secrets, ConfigMap
creation and Job reads were forbidden.

**Partition:** the install Job was `Complete` while consumer A's public Ready
stayed False. Heal, the repeated install and all nine reflected-result faults
were then rejected as before.

**Lifecycle** (`lifecycle.json`): the runtime, boot and observer Pods were
recreated, and the completed Job was recreated with a verified no-op replay
(one Pod, exit 0). Unchanged throughout:

- journal bytes;
- QEMU starts (2);
- PVC/PV UIDs and the Retain policy;
- domains (none);
- consumer A Ready/Bound.

The observer republished afterwards.

**Delivery faults** (`delivery-faults.json`): the duplicate, wrong-cell and
stale-epoch handoffs each produced one Pod at exit 65 and a Job `Failed` with
reason `PodFailurePolicy`. They wrote no status, left the journal unchanged and
left consumer B Bound. kro pruned them after deletion.

**Release, reuse and location:**

- Released projection for A and B, and reuse Bound for B in epoch 2.
- Wrong-location denial with free capacity: six custody records and the
  journal were unchanged.
- Six live closed-boot HTTP denials through the boot Service from the runtime
  Pod. No domains or disks remained.
- Zero OOM events in the runtime Pod cgroup, the cell node cgroup, and the
  site and kro containers.

The secret scan checked 12 evidence files against 22 retained secrets.
Teardown removed every `fleet-mp-b6b817c8` container, both kind clusters, the
site image tag and private credentials, and restored inotify from 1024 to 128.
This is local TCG and legacy-BIOS PXE evidence only. KVM, UEFI, production
authority and production equivalence remain false in `summary.json`.

The first attempt with this design, `/tmp/fleet-multiplane.e7atDk`
(`fleet-mp-ffab61d0`), passed every phase through `install-repeat`, including
both interruptions and exact resume. It then failed at `lifecycle` on a harness
defect: `k8s_evidence.get` rejected a `config` argument. The failure handler
stopped only that run's containers. Its stopped topology was then explicitly
discarded with `teardown.sh --discard-unknown`, which is not release evidence.
The defect is fixed, and `hack/multiplane/test-k8s-evidence.py` now runs every
helper command against a fake `kubectl` in the offline harness tests.

### Earlier runs (superseded execution model)

Runs before this change executed the cell as a `fleet-multiplane cell` process
started by `docker exec` in a privileged provider container. One passed on
2026-11-15 as `/tmp/fleet-multiplane.HkE6e5` (`fleet-mp-a6f50082`, 31 phases).
Others, `/tmp/fleet-multiplane.67Z4yk` and `/tmp/fleet-multiplane.CubymM`,
either passed or completed with manual bootstrap recovery. Their findings
(Milo routing, Karmada delivery and reflection, firmware PXE, disk boot,
reflection-fault rejection, and cleanup and reuse) are re-established by the
run above. Their process-launch and SIGKILL evidence no longer describes how
the cell executes and is not current evidence.

Two earlier failures inform the current bounds. In
`/tmp/fleet-multiplane.4vyisu`, a 2.5 GiB provider cap caused an installer OOM;
the runtime limit is now 3 GiB and OOM is checked. In
`/tmp/fleet-multiplane.omuBXA`, a shell builtin was executed through
`docker exec`; that path no longer exists. Both retained Unknown custody and
were never treated as release evidence.

### Offline checks

```sh
go test -race -count=1 ./...
go vet ./...
task multiplane:test-harness        # includes hack/multiplane/test-k8s-evidence.py
shellcheck -S warning hack/multiplane/*.sh hack/local-vm/start-libvirt hack/local-vm/site-runtime
git diff --check
```

Deterministic Go tests cover, among others:

- allocation, installation, release and reuse on custody against a real API server
  (`TestAllocateInstallReleaseReuse`, `TestConcurrentAllocatorsOneWinner`,
  `TestAnAllocationThatLostItsDeviceIsReplaced`);
- the federation layer: an assignment's round trip, only this cell's observation
  accepted, a stale fence report ignored, and a cell running only its own
  assignments (`internal/coordination`);
- the local VM site: install then wipe, a resumed boot observed and not repeated,
  and the boot server serving only an open admission at the stored fence
  (`internal/site/vmwork`);
- the site framework's fences, journal, deadlines and results (`internal/site`);
- location rejection before allocation (`internal/multiplane`).

They do not pretend to implement Karmada, kro or the Job controller in memory.
Only the live command establishes federation, kro, Job and PXE acceptance.

### Known limitations of the local MVP

Independent review on 2026-11-15 found no blocking safety issue for the
disposable local MVP of the earlier composition. These limits still apply, and
the harness does not hide them:

- **The site store is the fence.** Each Device's highest fence, the older-worker
  stop confirmation and its journal live in the site store on the retained
  volume. If the store is deleted or replaced, the site reports a lower fence than
  the coordinator last admitted, and it no longer refuses older assignments. That
  is acceptable only because every site volume here is disposable and created
  fresh per run. The volume uses `Retain` and is not managed by kro, but nothing
  prevents an operator from deleting the PV. Never reset, delete or replace the
  store of a site whose Device or disk is retained.
- **Location validation is local to this runner.** `ErrLocationNotServed` is
  enforced by `internal/multiplane.Runner.request` for its single `local`
  location and configured cell. The shared allocator and the production manager
  do not implement location binding.
- **Some crash windows leave an operation unknown.** These include an ambiguous
  `virsh create` after a boot step was journaled, a crash after disk creation but
  before the step was recorded (exclusive creation then refuses to overwrite), and
  a lost PXE network, lease or broker delivery. A worker does not start a possibly
  executed boot again, and custody holds the Device. A person settles the hold
  with a `DeviceRecovery`, which this run does not exercise. In the run, the only
  resolution is to discard the whole disposable topology after inspection
  (`teardown.sh --discard-unknown`).
- **Single-node site.** The runtime, boot service, observer and workers share
  one kind node and one `ReadWriteOnce` local-path volume; the per-Device lock
  depends on that. Losing the runtime Pod stops any running guest; the
  operation then stays unknown. Multi-node or HA execution is not designed.
- **kro is a broad reader, and graph authors are trusted.** kro must read
  delivered handoffs, including each assignment, to enumerate them. Its informers
  need cluster-wide get/list/watch on every referenced kind. Its writes are
  namespaced and admission-checked, but an admitted worker still reaches root
  libvirtd and the site store; see the
  [trust model](site-execution.md#ownership-and-trust-model). Disabling
  `dispatch` or deleting a handoff prunes worker Jobs without cancelling
  effects. See [site execution](site-execution.md#lifecycle-and-retention).
- **Shared-service revisions roll the runtime.** A services-layer change, such
  as a new site image, recreates the runtime Pod and stops any running guest.
  The operation then stays unknown. No migration from the earlier kro-owned
  composition is implemented. Retained historical Jobs also have immutable pod
  templates: changing the runtime-derived worker image may block kro dispatch.
  This upgrade risk has not been exercised; image upgrades require a fresh
  disposable topology until a tested upgrade lifecycle exists. See
  [site lifecycle limitations](site-execution.md#lifecycle-and-retention).
- **Interrupt timing.** An emulated install takes about a minute. The Job
  deletion is issued as soon as the replacement Pod exists; a much faster
  guest could finish first, and the phase would then fail rather than pass
  vacuously.
- **GitOps seams.** The Flux source is a plain-HTTP, unsigned registry on the
  kind network. Credentials are enrolled imperatively, not with SOPS or
  External Secrets. The Karmada API is reconciled with its admin kubeconfig,
  and the three clusters share one VM. Revisions are staged from the checkout;
  nothing is published. See [what is not production](gitops.md#what-is-not-production).
  The site volume's Flux policy (`prune: disabled`, `ssa: IfNotPresent`)
  protects it from reconciliation, but not from an operator deleting the PV.
- **No creator webhook, and no real BMC discovery.** The local Milo enforces the
  custody CRD rules but registers no creator webhook. Fixture discovery writes
  Device status through a separate discovery identity on the workstation; real
  BMC discovery is not part of the run.
- **Qualification scope.** A non-disposable provider, KVM, UEFI, real hardware/BMC,
  production Milo IAM, quota and networking remain out of scope.

The production gaps include caller-preserving admission, source-use IAM,
service-owned quota recovery/settlement, source publication custody, true project
provisioning/deletion, cell enrollment and revocation at the hardware effect
boundary, live hardware/network attestation, token rotation, sustained outage
behavior beyond configured freshness, and independent durable/HA failure domains.
There is no production-equivalence or production-readiness flag to enable.

### Automation notes

Source fingerprints include all internal Go packages, `config/multiplane`,
`config/site`, the harness, and module manifests, and exclude Python caches.
Each Flux source revision also has its own per-file SHA-256 list in evidence. Phase deadlines are
enforced even with partial output or closed stdout. Image-tag removal is
reported separately from container cleanup. Teardown does not verify that
every Docker volume is absent. Scenario booleans in the summary derive from
successful bounded phases and checked log markers. The partition check runs
with the host publisher stopped, as described above.
