# Fleet implementation status

[Documentation overview](../README.md) · [Operations guide](../operations/README.md)

This development guide records the implemented subset and its limitations.
See [build, test, and run](#build-test-and-run) for local commands and the
[implementation roadmap](roadmap.md) for proposed milestones and required evidence.

Status: public API foundation, the persistent simulator site, an
allocation and release library on Device custody, an unwired NSO network
lifecycle adapter, a Milo quota claim, and an event-only manager wiring scaffold
with an injected Machine lifecycle handler. The public allocation API uses
customer `Machine`, provider `MachineCapacity`, protected immutable
`MachineAllocation`, and `Installation.machineRef`. Authored references contain
names, and a MachineAllocation records the exact incarnations. A packaged
standalone synthetic smoke scenario exercises local-scope contention, install
recovery, release with a wipe, and capacity reuse on Device custody. An explicit
Milo-routed synthetic staging overlay exercises event-driven Machine allocation,
contention, release, and reuse through the public project API. Production
caller-preserving allocation admission, replayable Milo event adapters, and
production provider integration are not implemented.
Inert [inventory-backed capacity registration](inventory-capacity-registration.md)
now has a stateless caller-preserving admission service that checks live
Devices, one capacity per Device through a Device-derived name, a Device
lifecycle (`Active`, `Decommissioning`, terminal `Retired`), an opt-in Device
deletion protection component that admits deletes only for Retired Devices or
a deleting namespace, and a session-fenced default-namespace Device picker. It admits only Disabled/Retain records with cleaning explicitly
unconfigured, and governs only capacity that carries the registration marker;
synthetic, hand-made and legacy capacity and all capacity status writes are not
routed to it. Deployment trust inputs and live Milo acceptance remain required.

[Device custody and operations](device-custody.md) is **implemented** through
Increment C, the cutover. Every mode allocates, installs and releases on
`DeviceCustody`, with the custody coordinator as its only writer. The
Secret-backed coordinator, its private records and the old executors are deleted,
and an allocation's private facts live on its MachineAllocation. The standalone
smoke verifies it. The end-to-end failure scenarios (Increment D) are proposed.

The opt-in [multi-plane acceptance harness](../operations/multiplane.md) composes
the [local VM site](../operations/local-vm.md) with native Milo project APIs, the
custody coordinator, and actual Karmada delivery. On the selected cell the site
runs as [Flux-owned Deployments and kro-created per-assignment worker Jobs](../operations/site-execution.md).
Each local cluster's [Flux](../operations/gitops.md) installs Milo, Karmada, kro and the shared site services from
an OCI artifact staged from the checkout. Run it
with `task multiplane:acceptance`. Its exit codes, bounded phases and sanitized
`evidence/` directory are documented there; `task multiplane:test-harness` runs its
offline tests, which CI also runs. Verification of that run on custody is
pending. The Redfish site workers (`internal/site/redfishwork`) read each
Device's own credential and connect only to its approved TLS key.
[Redfish-mode acceptance](../operations/redfish-lab.md)
(`task multiplane:redfish-acceptance`) exercises them against the unmodified DMTF
Redfish Interface Emulator and a lab-only libvirt adapter, and its verification is
pending too. They are not qualified against a physical BMC or server, and the
production manager does not use them.

Production Kustomize roots now package the implemented manager, admission, API,
Milo IAM, service, and quota boundaries; packaging is not runtime readiness.

## API group naming

The additive [shared hardware catalog](hardware-catalog.md) implements
Manufacturer/DeviceType records and denial-aware staff readers under the
provenance-free inventory contract. New registrations can select catalog models;
external import and existing-device migration are deferred. Templates never modify
installed assets.

The allocation APIs now use `metal.datumapis.com/v1alpha1`. Milo registers the
Bare Metal service as `metal`; entitlement and location-availability readers use
that reference. Physical Device references use `fleet.datumapis.com`; that group
implements only a minimal, operator-written `DeviceClaim` and `Device` today. The
[API architecture](../architecture/README.md) documents the proposed coordination group,
control-plane placement, and rollout constraints. Group renaming does not migrate
stored allocations or establish live acceptance.

## Allocation API migration

The implemented alpha surface follows the current machine-allocation proposal:

- consumer projects create `Machine` objects with required
  `machineClassRef`, `machineProfileRef`, and `locationRef` names;
- provider projects publish `MachineCapacity` objects whose immutable
  `deviceRef` names the separately owned physical inventory record;
- Fleet owns `MachineAllocation` records in the provider project. Each is named
  `alloc-` and 16 hex characters of the SHA-256 of the consumer project UID and
  the Machine UID, its spec is immutable, and `Released` is terminal; and
- `Installation` names its Machine and, for reinstalls, the preceding
  Installation. A MachineAllocation records the exact Machine, project and Device
  incarnations, so authored specs and status expose no Kubernetes UIDs.

CRDs, Go types, scheme registration, IAM ProtectedResources and roles, manager
watches and routes, quota labels, standalone fixtures, and smoke assertions use
those names. The MachineAllocation is the stored record, and no customer role can
write it. Fences are Device custody's, not an epoch or token in a record; see
[Device custody](device-custody.md#cutover-increment-c).

## Requested hardware

MachineProfile `cpu.cores` and `memory.capacity` are exact values: a positive
integer and a positive integer byte quantity. The former min/max shapes were
removed as a breaking `v1alpha1` change with no conversion or migration. Stored
profiles with the old shape must be recreated before upgrading. The allocator
matches capacity only when the observed cores and memory equal the request.

A MachineAllocation records the accepted class and profile references, with the
exact CPU and memory it allocated against. Fleet keeps no decoder or release
path for older record shapes, because the Secret-backed records are deleted. See
the [MachineProfile notes](../enhancements/machine-lifecycle/README.md#machineprofile).

`requestedhardware.Project` is the one projection that writes
`status.requestedHardware` and `RequestResolved`, and
`manager.RequestedHardwareProjector` is its only caller. `cmd/fleet-manager`
builds one projector per engaged project, bound to that project's identity and
client, in every configuration, including the default one whose lifecycle
handlers are blocked. There is no second writer and no switch to disable it.

The projector reads the Machine and its profile from the project cache. The first exact resolution writes
`status.requestedHardware` with Machine generation, profile generation, and
spec-digest provenance, and sets `RequestResolved=True`. After that the snapshot
is never re-resolved, and a CRD transition rule rejects status writes that
change or remove it. A missing, deleting, or inexact profile leaves the snapshot
absent and sets `RequestResolved=False` (`ProfileNotFound`, `ProfileDeleting`,
or `ProfileInvalid`). Unknown read results are retried.

The synthetic staging handler and the standalone runner carry the snapshot
forward. Staging rejects a conflicting recreated profile with
`RequestedHardwareConflict`. The behaviour needs only an engaged project; allocation,
providers, and quota are not required. Tests cover CRD
validation and CEL pinning with the API server's validators, resolver
provenance, missing and invalid profiles, post-acceptance profile changes,
inert-mode wiring, and staging and standalone carry-forward. See
[requested hardware](../enhancements/machine-lifecycle/README.md#requested-hardware).

## Multi-project discovery

`cmd/fleet-manager` runs one set of controllers over every project that
consumes the metal service, through `sigs.k8s.io/multicluster-runtime` and the
service catalog's consumer provider (`internal/projectdiscovery`). There is no
`--project-name`; `--config` names a `FleetManager` file.

- **Discovery.** `discovery.mode: milo` requires `consumerScopedProjection`
  with exactly `serviceNames: [metal.datumapis.com]`; watching every project is
  rejected. A project is engaged while it has an Active, not-deleting
  ServiceConsumer. `discovery.mode: single` with `discovery.project` serves one
  named project through the same wiring, for standalone use and tests. Unknown
  configuration fields are rejected.
- **Per-project runtime.** `manager.Projects` is the Aware that discovery
  engages through. On engagement it resolves the project's identity from Milo
  (`ResolveProjectIdentity`), refuses a second cluster name for the same
  project lifetime, builds the project's runtime (client, cache reader,
  verifier, mappers, lifecycle owners, handlers), registers it, and only then
  engages the controllers. When the engagement context ends it unregisters the
  runtime.
- **Requests.** `ProjectRequest`, `RouteRequest`, and `ProviderEventRequest`
  carry the cluster name. `ClusterProjectReconciler` and
  `ClusterRouteReconciler` look up the runtime for that name, require
  `SameProject` against its identity, run its verifier, and delegate to the
  existing `ProjectReconciler` and `RouteReconciler`. A request for a project
  that is not engaged is dropped with no effect.
- **Allocations.** A MachineAllocation lives in the provider project and is named
  from the consumer project UID and the Machine UID, so equal Machine UIDs in two
  consumer projects never share one. Exclusivity belongs to Device custody, which
  is global to the Device and so spans consumers.
- **Handlers.** `manager.DefaultHandlers` gives each project blocked lifecycle
  handlers and the requested-hardware projector. `--synthetic-staging` uses
  `staging.ConsumerHandlers`, which installs the staging handler only for
  `--synthetic-consumer-project-name`, and only when that name is engaged as
  the exact consumer lifetime resolved at startup. The publisher writes only
  into that project. Provider MachineCapacity events wake only that project.
- **Provider as consumer.** The synthetic provider project may also be an
  Active metal consumer. It engages with `DefaultHandlers`; its runtime maps
  only its own Machines, so
  provider MachineCapacity events cannot reach the synthetic consumer's claims
  through it, and its blocked handlers never write MachineCapacity status. The
  synthetic consumer itself must still differ from the provider.
- **ServiceConsumer filtering.** The consumer provider enqueues every
  ServiceConsumer in the producer project under its own key, filters by
  service only inside its reconcile, and returns an error on the first failed
  engagement. One failing project was therefore retried by every key.
  ServiceConsumers carry no service label and declare no selectable fields, so
  the provider-project cache wraps ServiceConsumer informers and drops events
  for other Services (failing open while the Service is unresolved). An
  engagement failure is retried by the metal consumers' keys with
  controller-runtime's per-key exponential backoff.
- **Departure.** The manager stops reconciling a project that leaves the
  consumer set. It does not release custody, call a provider, delete records,
  or rewrite status, including for a project with active custody. Its
  deadlines and queued events are dropped, and re-engagement resumes from the
  retained allocations. Unbinding drains direct and dependency-route
  reconciliations that already acquired the runtime before removing it from the
  registry; a retained runtime pointer cannot start work after that barrier.
  A blocked in-flight handler therefore delays completed departure rather than
  falsely reporting it. This fixes an inherited race where informer removal and
  registry removal could finish while a handler could still write. It does not
  roll back effects already admitted before departure. Regression tests block a
  handler explicitly and assert the barrier, in addition to the active-custody
  no-write/no-release test.
  Re-engagement also drains the previous runtime before publishing either
  registry entry, so a new lifecycle owner's independent request locks cannot
  overlap old work. The wait never holds the registry mutex. After waiting,
  engagement rechecks both the cluster pointer and project-lifetime ownership;
  a concurrent successor must itself drain, and a conflicting route is refused.
  Cancellation aborts the waiting engagement without publishing its runtime or
  engaging its controllers. An already initiated drain continues to completion;
  it is not rolled back into an active runtime. Cancellation retires the prior
  entry after its drain completes, without removing a concurrent successor.
  A handler that never returns still prevents safe replacement; cancellation is
  not evidence that its effects have stopped. Deterministic direct/routed replacement,
  cancellation, stale-pointer, registry-revalidation and concurrent engagement
  tests cover these process-local barriers, not distributed site fencing.
  A per-cluster-route engagement gate also serializes publication through the
  entire downstream controller setup call and failure cleanup. Draining alone
  was insufficient: a paused predecessor's setup could otherwise resume after
  its successor and replace the successor's controller watches. The gate is
  acquired after effect-free runtime construction; waiting is context-cancellable
  and never holds the registry mutex. Cancelling a waiter does not stop the
  current runtime. Gates are reference-counted and removed after their last
  holder or waiter leaves; unrelated routes remain independent. Project-lifetime
  conflicts across routes are still checked under the registry mutex.
  Each published runtime owns a child engagement context, cancelled when its
  drain begins. This cancels partial downstream engagements on failure and invalidates
  the old context even when replacement uses the same cluster implementation.
  Cancellation starts draining immediately, but cleanup cannot remove lifetime
  ownership or admit a successor until downstream setup returns and admitted
  handlers finish. A downstream call that ignores cancellation therefore blocks
  that route rather than allowing an unfenced successor. A setup that returns
  nil after cancellation is reported as cancelled, not successful engagement.
  Cleanup under an already-held gate uses a separate internal path, avoiding
  recursive locking; stale cleanup can only cancel and remove its own runtime.
  Downstream setup may read the registries and engage another route. Synchronous
  recursion back into a held route with the supplied context is rejected with
  `ErrProjectEngagementReentrant`, including cycles through another route.
  Nested setup also refuses to wait on any other occupied route in this registry,
  preventing two concurrent callbacks from deadlocking by requesting each
  other's routes. Ordinary independent callers still wait cancellably.
  Callbacks must propagate that context rather than create a detached context
  and synchronously wait on their own engagement. Deterministic regressions
  cover blocked setup, direct/dependency handlers, failed/cancelled setup,
  cancelled waiters, cross-route lifetime transfer, stale cleanup, unrelated
  progress, reentrant setup and same-implementation context replacement.
- **Observational discovery.** The provider declares no managed resources,
  teardowns, or suspend hooks, so it adds no ServiceConsumer finalizer. Its
  discovery clients refuse every write. The library would otherwise patch
  `Paused=True` on a suspended ServiceConsumer even with no hooks; the manager
  drops that patch, because Fleet implements no suspension.
- **Leader election and engagement.** One host-cluster lease
  (`fleet-manager.fleet.datumapis.com`); only the holder reconciles. Project
  engagement runs on every replica. As in Compute, the provider's `Start` is
  hidden from `mcmanager` (which would leader-gate it) and re-added once to the
  always-running group. Unlike Compute, this is done for every
  `ProviderRunnable`, so the consumer provider is not started twice.
- **Readiness.** `/readyz` requires discovery to have synced, every consuming
  project to be engaged, and every engaged project cache to have synced. It fails with `ErrDiscoveryMisconfigured`
  while the metal Service is missing or its producer project differs from
  `consumerScopedProjection.providerProject`. The former integration gate, which
  failed unless synthetic staging was on, is removed. Removing it enables no
  effect: without a provider every lifecycle handler stays blocked.
- **Module versions.** Fleet uses the versions Compute uses:
  `sigs.k8s.io/multicluster-runtime` v0.25.2, `go.miloapis.com/milo` v0.36.9,
  `go.miloapis.com/service-catalog` v0.14.6-0.20261023171130-8c647a6f552b,
  `sigs.k8s.io/controller-runtime` v0.25.2, and Kubernetes libraries v0.37.1.
  This raised Fleet's controller-runtime (v0.24.1), Kubernetes (v0.36.1), Milo
  (v0.32.5), and service-catalog (v0.13.1) versions. Milo v0.36.9 itself
  requires controller-runtime v0.25.2 and Kubernetes v0.37.1.

Tests run the production wiring (`projectdiscovery.NewManager`,
`manager.NewProjects`, `manager.Setup`) over a fake provider and fake project
API servers (`internal/multiclustertest`) whose writes produce watch events.
They cover:

- no provider: two projects with identical Machine names and UIDs each pin from
  their own profile, with no allocation, finalizer, lifecycle condition,
  or Installation change;
- project isolation: an event wakes only its own project, and scoped reads cannot
  cross projects;
- departure with active custody, re-engagement, restart with a replaced
  profile, and identity failures (unknown project, two clusters for one
  lifetime, project recreated while engaged);
- synthetic staging end to end: allocation only in the publication consumer,
  with another engaged project untouched.

Allocation tests cover custody and installation scoping with equal Machine UIDs
in separate project lifetimes. An envtest test runs the real consumer provider
against separate API servers that serve every watched kind, behind a Milo-shaped
project router, with a real host lease. It needs envtest binaries and is skipped
without them.

## Location authority

`internal/location` uses the existing Milo location contract shared by Compute,
NSO, and DNS. It resolves a requested project-visible `Location` only when that
projection has `Ready=True` and exactly one Fleet `ServiceAvailability` names
the location with `Available=True`. Missing, deleting, unavailable, or ambiguous
inputs fail closed. The retained MVP authority contains the project-visible
Location UID and a semantic digest of both projections.

Milo legitimately omits `observedGeneration` from mirrored Location conditions,
and mirrored ServiceAvailability status can retain its root object's generation;
neither value is compared with the project-local generation. Canonical root UID
provenance, expiring health receipts, and a platform-wide runtime availability
contract are production-hardening opportunities. They are not prerequisites for
the MVP because the existing platform contract treats ServiceAvailability as a
service rollout declaration rather than a renewable health lease.

The event-only manager indexes Machines by requested location and watches
both Location and ServiceAvailability objects. Semantic updates and deletes
therefore route every affected claim through its existing durable work key;
there is no periodic location resync. This implements resolution and event
routing, not the production claim handler that invokes it. Without those
production handlers and authorities, the manager's lifecycle handlers stay
blocked; readiness reports only discovery and cache health.

The Milo bundle declares a Draft Fleet `ServiceConfiguration` supporting the
platform `datum-managed` location class. Draft is intentional: an environment
must explicitly promote both the Service and its configuration before a
ServiceAvailability can project a Location into entitled projects.

## Standalone synthetic smoke environment

`cmd/fleet-standalone-smoke` is a test-only, phase-oriented harness around the
allocation library on Device custody. The packaged fixture uses two Kubernetes
namespaces as project stand-ins and one synthetic `us-central-1` machine with 24
cores and 128Gi of memory. A coordinator Deployment runs the custody loop, and
the simulator is the only site. The allocate phase waits for the Device's custody
to be `Verified`, allocates for one claim, and leaves the competing claim pending.
The install phase admits one exact digest-pinned `DeviceInstallation` through the
simulator and is idempotent across worker and simulator restart. A bounded
reinstall phase uses a distinct Installation naming the initial Installation as
its predecessor, retains the same allocation, and recovers the same successor
operation across retries and restart. An unknown result keeps readiness unknown
and the Device held. The release phase wipes the Device with a `DeviceWipe`, under
a fence above both installations, before the waiting claim is allocated.

The phases run as the Metal provider, with the custody coordinator's and the
release controller's tokens in their own clients, so each write carries its
component's identity. The harness is the designated writer for the synthetic
capacity conditions and the public fixture status, because the capacity projector
is not wired. It does not patch custody or operation status, strip finalizers, or
use public status as authority. The simulator runs under an explicit non-root
UID/GID with a writable `fsGroup` PVC and group-readable runtime-only bearer
Secret; its admin listener is off. Docker bases are digest pinned, and the smoke
script derives a source-specific local image tag and verifies the running
workloads use it with pulls disabled. That identity commits to HEAD, its tree,
the tracked diff, and untracked file contents; a CI regression test proves
distinct clean commits produce distinct IDs.

This is not the proposal acceptance environment. Namespace stand-ins do not
model Milo project APIs, caller identity, IAM, entitlement, quota, purge, or
same-named `default` namespaces. No interface is requested, so network and
address output is blank and no NSO/IPAM/Galactic behavior is claimed. See
[`docs/operations/standalone-smoke.md`](../operations/standalone-smoke.md) for the runnable scenario.

Standalone-only Kustomize roots mark every rendered object with
`fleet.datumapis.com/standalone-only: "true"`. CI discovers and builds every
Kustomization below `config/`. For every root except the explicitly designated
standalone and simulator definition roots, it resolves the complete local
resource/component graph, including deprecated bases and external generators,
and rejects any transitive reference to either protected source tree. Remote
resource-producing references and unauditable Helm generation fail closed.
References must use plain local relative-path syntax and remain inside the
audited config tree both before and after symlink resolution, preventing remote
repo grammar from being disguised by a local shadow path. It also parses
rendered objects structurally: annotations are checked by GVK and metadata,
Secret data is decoded before comparison, and effective RBAC
permission and subject sets are compared without relying on rule layout,
duplicates, or mutable object and subject names.
ConfigMap payload text is not treated as a credential. Hermetic adversarial
fixtures cover direct, nested, generated, copied, renamed, and malformed inputs,
including paths containing spaces. This is a packaging boundary, not an
authorization mechanism; production overlays must never consume these fixtures.

## Event-only manager scaffold

`internal/manager` defines project-lifetime work keys, exact reverse indexes,
semantic predicates, dependency event mappers, thin claim/installation/projector
reconciler surfaces, a replayable external-event contract, and a one-shot
freshness deadline scheduler. Queue keys include the Milo authority ID, root
Project UID, namespace, name, and object UID. Equal names or UIDs in separate
project lifetimes cannot share work.

The manager uses a controller-runtime cache with an explicitly non-nil zero
`SyncPeriod`. It is scoped by Milo's project control-plane URL and contains
public Fleet, NSO, quota, entitlement, and Namespace resources. Reverse indexes
are registered on that cache. Leader election uses the management-cluster
configuration. Dependency mappers use exact field indexes and do not perform
unqualified application Lists. Public status-only updates, including status written by an
external actor, do not feed the lifecycle coordinator; external facts that must
drive lifecycle work require an owned dependency resource and route.
Claim and Installation spec generations, labels, annotations, finalizers, and
deletion changes do feed it. Provider observations belong on dependency
resources: semantic Machine status changes use the dependency route rather than
turning public claim status into authority. Freshness expiry uses a replaceable
one-shot deadline, not a periodic resync.
Dependency events enter a typed route workqueue before reverse-index lookup, so
a cache read or List failure is retried as the same source event instead of being
mistaken for an empty target set. Every direct and routed handler also verifies
the configured Project lifetime through an injected trusted verifier.

The manager has no production lifecycle handler. Allocation, installation and
release are the `internal/allocation` library on Device custody, which the
synthetic staging handler, the standalone smoke and the multi-plane runner call.
The Machine handler and Milo lifecycle bridge of the Secret-backed design are
deleted. A production handler needs the inputs the manager cannot get from
watched objects: the create-time caller identity, protected publication
provenance, and a trusted Machine observation time and location. The manager
binary therefore instantiates no handler, and lifecycle handlers stay blocked. An
environment must provide those inputs plus the Milo admission fence and quota
custodian before enabling effects; controller credentials, annotations, public
status, and synthetic timestamps are not accepted substitutes.

Every dependency route now retains the source object's UID and exact routing
snapshot. Non-delete work checks that UID against the current named object; if a
queued name has been recreated, the old route is discarded and the replacement's
Add event owns its new lifetime. Delete/tombstone work instead resolves the
retained old-UID edges from its snapshot even if same-name recreation occurs
before reconciliation. Update events enqueue the deduplicated union of old and
new snapshots, ensuring both lost and gained selector or label relationships are
reconciled. Missing
project wiring is returned as an error so controller-runtime retains the exact
queue item under its bounded rate limiter. It is never converted into successful
pending work and silently dropped.

Claim work and Installation work each have one keyed lifecycle owner shared by
their direct, dependency-route, and deadline controllers. For an exact
ProjectRequest, that owner serializes the coordinator before the projector. The
coordinator alone may schedule or cancel a one-shot evidence deadline; expiry is
fed back through a typed controller source to the same owner. Projectors cannot
schedule work, and no separate projector controller can race the coordinator.
Both schedulers are manager-owned runnables: cancellation atomically stops and
drains timers, waits for any in-flight emission, and prevents post-stop work.
Informer initial Add processing reconstructs still-relevant deadlines after a
fresh process starts.

The setup watches the implemented Fleet resources, NSO Network,
NetworkInterfaceClaim and NetworkInterface resources, Milo quota claims, and the
reviewed ServiceEntitlement shape. External IAM, project-lifecycle, catalog, and
site sources must provide durable cursor/replay or materialize watched state;
a lossy channel is not sufficient. Business pending states wait for their next
dependency event and do not return a timed requeue.

External replay persists the entire returned batch before committing the
source-provided next checkpoint; event sequence identities are never treated as
cursors. Empty batches may still advance a source checkpoint. Watched deliveries
likewise carry an explicit post-event checkpoint. A crash after any persistence
but before the batch commit replays the same idempotent inbox writes. Pending
inbox work remains replayable until a consumer durably processes it and
acknowledges its receipt. This increment implements and tests those runner and
drain contracts; the production durable inbox, consumer, and live stream adapters
remain integration-owner responsibilities.

`cmd/fleet-manager` discovers every consuming project, resolves each project's
exact lifetime from Milo, derives each routed Milo REST host from an
environment-owned project kubeconfig template, keeps leader election on the
management cluster, and starts the event graph. Its
readiness reports discovery and cache health only. Its
production handlers deliberately perform no lifecycle or provider effects until trusted create-time
caller/finalizer admission and live, replayable Milo adapters are supplied. A
watch controller cannot recover the original `user.Info` or atomically capture
it after object creation, so this increment does not fabricate that authority
with annotations or controller credentials.

The opt-in `--synthetic-staging` mode is a temporary acceptance boundary. It
accepts only powered-off, networkless claims in `us-central-1`, synthesizes
clearly labelled admission and provider evidence, and uses the same allocation
library on Device custody as the standalone smoke. The manager also runs the
custody coordinator loop and fixture discovery under its leader lease. It is
event-driven: claim, class, profile, Machine, namespace, and deletion events
route exact work; it adds no periodic re-sync. One synthetic Machine is
exclusively bound, competing claims remain pending, release must complete before
finalizer removal, and release wakes waiting claims for reuse.
Public conditions say `SyntheticStagingAdmission`, so this result cannot be
mistaken for Milo caller-preserving authorization.

`config/overlays/synthetic-staging` is the only packaged activation path. It
packages the simulator site as a separate single-replica Deployment and
ClusterIP Service using the same immutable Fleet release image. The manager
reaches it through Service DNS with an environment-owned site token; the provider
has no Kubernetes API credential or RBAC grants. A NetworkPolicy restricts access
to same-namespace manager pods, and the admin listener stays disabled.
The site store resides on a retained PVC and survives manager and provider pod
replacement. A `Recreate` rollout avoids overlapping store writers. Custody and
the site store retain exact operation inputs, and recovery is idempotent. Site
store persistence is covered by local tests. This remains synthetic API
validation, not production provider qualification. The overlay needs imported
records and two new credentials; see the [deployment and migration
contract](../../config/README.md#moving-from-the-secret-backed-staging).


`config/overlays/management-plane` now packages the manager Deployment so its
identity, RBAC, admission, image, and readiness boundary can be reviewed and
rolled out fail-closed. Its service account receives only leader-election access
in `fleet-system` and has no host ClusterRole. The environment must
provide the authority ID and Project name in `fleet-manager-config`, mount the
generated `milo-configuration-kubeconfig` Secret, and pin the image. The
manager resolves the immutable Project UID from Milo at startup and fails
closed when the named Project cannot be read, is being deleted, or has no UID;
operators do not copy an opaque Project UID into deployment configuration. The
default packaged deployment is intentionally inert and unready because
caller-preserving admission, the live verifier, replay integrations, and production handlers are still
absent. Installing these manifests is not evidence of functional allocation.

`config/milo` separately packages the Fleet CRDs, ProtectedResources, roles,
Fleet Service, and `metal.datumapis.com/machines` quota registration for the
Milo root control plane. The Service remains `Draft`; the manifest does not
advertise the inert manager as customer-ready. It does not include
project-specific entitlements,
grants, PolicyBindings, provider inventory, or the proposal's incomplete,
source-specific publication example. Those remain environment/provider state.
The two production roots target different API servers and must not be combined
into one Flux reconciliation.

## Allocation on Device custody

`internal/allocation` is the recovery-critical core. It allocates, installs and
releases through Device custody and creates no Secret record. The allocator takes
an admitted request: the Machine's exact project and Machine incarnation, the
class and profile, and the admission decision. It writes in a fixed order: the
quota claim, then the MachineAllocation (create only), then the custody claim (a
conditional update). A retry finds the same allocation by its name. An allocation
whose Device another holder took, and which never held it, is deleted, and the
allocator selects again. An `Active` allocation that lost custody is not repaired
(`ErrCustodyLost`).

Installation admission creates a `DeviceInstallation` named from the allocation
and the Installation UID. The Installation's predecessor must be the Machine's
current installation, and a bootstrap reference is refused. Response-loss
recovery is an exact lookup by name, not a second effect. Release moves the
holder to `Release`, closes the gates, wipes the Device, deletes the quota claim,
and only then returns the Device to `None`. The allocation, installations and
wipes remain as the fence history. See [Cutover](device-custody.md#cutover-increment-c)
for the gates, wipe steps and recovery.

The component identities are separate: the Metal provider creates allocations
and installations, the release controller creates wipes, and the custody
coordinator is the only writer of custody and operation status. Provider health,
availability and observation timestamps supplied to the library must come from a
trusted, authenticated provider-status path; this increment does not establish
that path.

## Network lifecycle adapter

Status: **implemented and inert**. The allocation library on Device custody does
not call this adapter, because no mode provisions Network or Address. Those
release gates close at once. The private asset and operation records this section
names were deleted in the cutover. The retention they describe is the contract a
future wiring must keep. Where it keeps it is **proposed**.

`internal/network` defines an NSO-independent lifecycle contract. Allocation
preflights every requested interface before hardware selection, requiring exact
namespace and location lifetime authority plus a caller-preserving network-use
decision. The accepted Network UIDs, normalized request, decisions, and request
digest must be retained by the caller before any NSO effect.
Fleet validates NSO's attachment shape before preflight and physical selection:
families are unique `IPv4`/`IPv6` values with at most two entries, and address
classes are unique with at most four entries. The Fleet CRD carries the same
cardinality constraints where OpenAPI can express them.

`internal/network/nso` implements that contract against the proposal's reviewed
NSO commit `d7fdc43f852450c447d4b7bb44b564c970582b1a`. It creates deterministic
NetworkInterfaceClaims only after binding and rejects name, UID, protected
metadata, or normalized-spec conflicts. A separate durable `ObjectGuard` must
prove Fleet reserved a claim name before an existing object can be recovered;
object annotations alone are not provenance. There is intentionally no default
authorizer or object guard.
The guard binds the created Kubernetes UID, so deleting and recreating the same
name with copied annotations or token is rejected. Cluster admission must reserve
Fleet's metadata keys and deterministic claim-name prefix against non-Fleet
writers. Guarded create/recovery and UID binding must be serialized atomically
against guard closure; a separate pre-create check is insufficient. Immediately before creation, production wiring must also verify the
retained Network UID and generation are currently Ready and every retained
address-class name still has the exact authorized lifetime UID.

The contract has the caller retain exact claim and interface UIDs plus
current-generation `Allocated`, `Prepared`, and `Programmed` observations.
Installation admission requires fresh `Allocated` and `Prepared` evidence and
copies the exact network revision and inputs into the operation's inputs. Public projection is a
pure helper that publishes only Network names, MTU, addresses, and normalized
conditions only while the observation is within the configured evidence age;
stale evidence projects `NetworkReady=Unknown`. Internal UIDs, NSO claim names,
and raw provider messages remain private.

Create intent for every interface is durably returned before any NSO create.
Release first durably admits an exact per-interface delete vector without effects,
then retries execute and verify individual deletes. Provider cleanup and finalizer release remain blocked while
network deletion, retained-interface detachment, or outcome evidence is pending
or unknown. Object absence counts only after the caller's retained record proves
deletion was admitted for the exact retained UID. `Retain` additionally requires the
same retained interface UID to report `Available` with no claim reference.
For `Retain`, an absent claim completes only when the closed guard proves the
claim was never created, or when an exact interface name and UID retained before
deletion later reports `Available` with no claim. A created claim whose binding
was never observed remains pending; a nil reference from one claim snapshot is
not durable proof that NSO could not bind it before deletion.

This package has no manager, watches, Milo integration, or deployment wiring.
Production wiring must supply a location-scoped client, caller-preserving
authorizer, and durable object guard; otherwise networked allocations fail
closed before selection.

## Milo quota claim

`internal/milo` holds Fleet's Milo service identity (`metal.datumapis.com`) and one
quota claim. The authority contracts of the Secret-backed design, which retained
project, source-use, entitlement and admission-fence evidence in private records,
are deleted. Production source-use, entitlement and admission-fence integration
is not implemented.

The quota claim is **implemented and verified** by unit tests, and **implemented
and inert** end to end: no mode in this repository has Milo quota. The allocator
creates a Milo `ResourceClaim` named `fleet-machine-` and 16 hex characters of the
SHA-256 of the consumer project UID and the Machine UID, in the Machine's
namespace, before it creates the MachineAllocation. A retried create finds the
claim by name. Only a granted claim reserves quota, so a pending claim returns
`ErrQuotaPending` and the allocator retries. A claim that is not the claim Fleet
would create for that Machine is `ErrQuotaInvalid`. Release deletes the claim and
reports it released only once it is gone (`TestReleaseIsObservedNotAssumed`).

Milo v0.32.5 supplies ResourceClaim evidence but no service-owned quota custody
anchor or explicit release acknowledgment, so deletion is observed, not
acknowledged.

## Simulator site

`cmd/fleet-simulator` is a test-only site for exercising the custody delivery and
recovery paths before a real hardware adapter exists. It serves a site HTTP API
(`internal/site/siteapi`) with a bearer token: it takes an assignment
(`PUT /v1/assignments`), reports a Device's fences (`GET /v1/fences/{namespace}/{name}`),
lists the results it holds (`GET /v1/results`) and forgets a result the
coordinator answered (`DELETE /v1/results`). It never contacts, enrolls or
authorizes real hardware.

Its workers (`internal/site/simwork`) run on the [site
framework](device-custody.md#site-execution-increment-b). They keep a simulated
server in fenced records of the site store, so an older fence is refused, a
newer assignment stops an older worker, and a result is fenced and typed like a
real site's. With `--discover`, the process also writes fixture discovery for its
enrolled Devices, as BMC discovery would. Fixture access checks the enrollment
against the snapshot, with no BMC. The site store is an owner-only directory, and
every write replaces a file atomically and syncs it, so the simulator refuses
older fences after a restart.

The runtime listener defaults to loopback, and the admin listener of the earlier
simulator is gone. Request bodies, identifiers, headers, and server timeouts are
bounded. The simulator refuses to start without a token and an enrollment.
Bearer authentication over plain HTTP is not a production site identity protocol.
The persisted store and the HTTP endpoints are internal alpha interfaces with no
compatibility promise.

## Physical inventory subset

`internal/api/inventory/v1alpha1` defines Facility, Rack, DeviceClaim, Device,
Cable and PowerFeed records. Each DeviceClaim is an observational installation
claim with typed, immutable placement, and Cable records are the only topology
authority; no move or replacement handover is automated. Records live
in the provider project in Milo. `config/components/inventory` holds their
CRDs, ProtectedResources, and the `fleet.datumapis.com-inventory-viewer` and
`-editor` roles. Only `config/milo` includes it.

Records carry no attribution fields: resource audit logs record who changed a
record and `metadata.creationTimestamp` records when it was created. CEL requires
a Device serial or system UUID, or the explicit `identity.unidentified` marker
with a reason and neither, and keeps a Device's `claimRef` and identity immutable,
rejects duplicate or ambiguous MACs, and accepts only credential-free `https`
BMC endpoints. Status is a reserved subresource with no fields and no writer.
No metal role grants inventory access.

The staff portal plugin's read-only Inventory tab pages through all six kinds,
shows physical relationships and components. Multiple historical bindings remain ambiguous rather than being
resolved by edit time. The primary workflow is [manual entry and review](../operations/physical-inventory.md#manually-record-a-small-installation)
through the existing Flux writer. Fleet reads no external inventory source; a
separate writer that copies records in uses the same API and validation. Unit
tests run API-server validation offline, including pruning of retired fields,
and check IAM. No deployment of this physical extension, physical verification, concurrent
reservation, deletion safety, adoption or new hardware-effect path is established.

## Build, test, and run

Run the implemented checks from the repository root:

```sh
go test ./...
go test -race ./...
go vet ./...
```

The manager's multi-project envtest test needs envtest binaries. CI installs
them; locally, run:

```sh
export KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.25.2 use 1.37.0 -p path)"
go test -race ./...
```

`task test:envtest` runs the same command.

For an isolated local simulator site, create a synthetic token file and a
synthetic enrollment, and keep the default loopback listener:

```sh
state_dir=$(mktemp -d)
runtime_token_file=$(mktemp)
chmod 600 "$runtime_token_file"
openssl rand -hex 32 >"$runtime_token_file"
cat >"$state_dir/enrollment.json" <<'JSON'
{"default/server-example-001": {"bmcEndpoint": "https://[2001:db8::31]", "systemUUID": "00000000-0000-4000-8000-000000000031", "bootMACAddress": "02:00:00:00:00:31", "bmcPublicKeySHA256": "3131313131313131313131313131313131313131313131313131313131313131", "disks": ["disk-0"]}}
JSON
go run ./cmd/fleet-simulator \
  --runtime-token-file="$runtime_token_file" \
  --enrollment-file="$state_dir/enrollment.json" \
  --state-dir="$state_dir/site"
```

Add `--discover` to write fixture discovery with the in-cluster credential. Remove
the synthetic token and state files when the local run is complete.

## Explicit gaps

### Milo acceptance harness

`test/acceptance/milo` implements a non-effectful acceptance boundary. It
requires exact Project lifetimes, Fleet and Milo revisions, digest-pinned
manager and simulator images, and explicit credential paths. Its evidence
encoder allow-lists non-secret environment identity and writes restricted,
atomic JSON and JUnit artifacts through a fresh staged three-file allowlist.
The config boundary walks from a held root descriptor with component-by-component
no-follow `openat`, requires an owner-only final file, and performs a bounded read before
JSON decoding. Phase reasons
and blockers use closed reviewed code vocabularies rather than external error
text. The staged summary must validate as the closed Result schema, and the
environment and JUnit files must exactly match canonical projections from it.
Verification retains the root and safely traversed parent descriptor identities, opens the evidence
basename with no-follow `openat`, and opens every allowlisted file once with
no-follow `openat`, validates bounded reads from the held descriptor set, then
securely rewalks and rechecks the complete absolute ancestor chain, the
parent-relative directory identity, and every path against the held
inode, mode, and size.
Invalid configuration cannot copy caller fields into evidence. A central result
validator enforces a passed prefix, exactly one outcome-matching terminal phase,
and a not-run suffix; invalid configuration is exactly the configuration phase.
Workflow logs emit only reviewed failure codes, never raw execution errors.
The workflow uses an absent run-ID/run-attempt-specific output directory so a
failed fresh write cannot upload artifacts left by an earlier run.
Credential paths are canonicalized, and
the mandatory pre-access check rejects symlinks, non-regular files, lexical
aliases, and files that share operating-system identity.
The credential boundary returns already-open no-follow descriptors and rechecks
their identity and owner-only permissions; later consumers must not reopen
paths. Evidence staging rejects symlinks in every existing parent component.
This is sufficient for the current non-concurrent blocked harness; a concurrent
live writer will require held directory descriptors and no-follow `openat`
traversal.

The current live command always stops after structural validation with
`Blocked` because the production protocol probes and live scenario executor do
not exist. It does not load a kubeconfig, contact a cluster, or mutate an API.
The protected manual workflow therefore demonstrates fail-closed behavior, not
Milo acceptance. Offline validation and tests cannot satisfy the proposal's live
acceptance inventory.

- The event manager exposes claim, installation, and sole-writer projector
  surfaces, but production lifecycle handlers and public status writes remain
  unwired. Trusted create-time caller capture and protected finalizer admission
  cannot be reconstructed from informer events.
- Live Milo project/IAM/catalog/quota event adapters and asynchronous site
  completion sources are not implemented. They must support durable replay;
  polling is intentionally not a fallback.
- Cross-project class sources and bootstrap secret retention are rejected rather
  than partially executed by the production and standalone entry points. The
  explicit synthetic-staging mode can publish and allocate one configured class
  and profile across two exact Milo project lifetimes. It uses synthetic
  admission, quota-hold, and source-use evidence, labels that boundary in public
  status, refuses publication collisions or profile drift, and is not production
  authorization. The network adapter remains deliberately unwired until
  production location, authorization, and object-guard owners are available.
- Milo v0.32.5 supplies ResourceClaim evidence but no service-owned quota custody
  anchor or explicit release acknowledgment. It also does not supply Fleet's
  caller-preserving source/provenance receipt, atomic invalidation-versus-bind
  fence, consumer-project deletion recovery, or provider-project purge guard.
  Production cross-project allocation therefore fails closed, and the quota claim
  has no end-to-end run.
- `LocalDigestImageResolver` is test-only. Production image authority,
  namespace authorization, catalog lifetime identity, and digest resolution are
  not implemented.
- No container image or Kubernetes deployment is delivered in this increment.
- There is no mTLS workload identity, target-level authorization, or production
  site credential protocol.
- The capacity projector is unwired (O1) and bootstrap is refused (O10); see
  [Device custody](device-custody.md#points-for-a-person).
- The site store is intended for a single site process per Device. Replication,
  migration, backup, and upgrade/downgrade compatibility are not implemented.
