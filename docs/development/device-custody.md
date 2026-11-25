# Device custody and operations

[Documentation overview](../README.md) · [Implementation status](README.md)

This page records how Fleet implements the [device custody and operations
proposal](https://github.com/datum-cloud/fleet/pull/174) (#174, pinned at
`d4d17f9`). The proposal's API names are provisional. Each piece below is
marked **implemented and verified**, **implemented and inert**, or
**proposed**.

## Status

| Increment | What it covers | Status |
| --- | --- | --- |
| A: control-plane foundation | API types and CRDs for `DeviceCustody`, `DeviceInstallation`, `DeviceWipe` and `DeviceRecovery`; CEL rules; IAM resources and roles; the creator webhook; the kind registry and admission rule table; the coordinator's custody reconciler, admission, results, recovery, rebuild and retention; the capacity projector | **Implemented and verified**, except the capacity projector, which is **implemented and inert**. Verified with unit tests and with tests against a real API server. Increment C wires the rest into every mode. |
| B: site execution | Per-Device fence store and journal, older-worker stop with a confirmation, deadlines, fenced typed results, per-Device credentials, pinned TLS, identity checks, the handoff payload, the site fence report, and the Redfish install and wipe phases behind the worker contract | **Implemented and verified.** Unit tested, including against TLS BMC stubs. Increment C delivers to it. |
| C: cutover | MachineAllocations and quota claims named from the Machine UID, allocation and release on custody, the delivery loop, the simulator, local VM and Redfish sites, the creator webhook in the production Milo root, the manager's power projector, and deletion of the Secret-backed records and executors. See [Cutover](#cutover-increment-c). | **Implemented.** Verified by unit tests, tests against a real API server, and the standalone smoke (`task standalone:test`). Verification of the two multi-plane runs, local VM and Redfish, is pending. |
| D: end-to-end failure scenarios | Harness phases and evidence checks for the #174 acceptance tests | **Proposed** |
| Power P1: control plane | `DevicePowerOperation`, its registry entry, rows, checks, evidence and settle hook, its roles, and the Device field for offered reset types. See [Device power operations](#device-power-operations). | **Implemented.** The kind ships with the component in every root C wires, and the creator webhook, standalone's creator policy and each coordinator's access cover it. Nothing creates power operations yet. Discovery fills the reset-type field in a separate change. |
| Power P2 to P4 | The site worker (on B), customer power from `Machine.spec.powerState` (on C), and the UI | **Proposed** |

## What Increment A adds

- **Kinds.** `DeviceCustody` and the operation kinds in
  `fleet.datumapis.com`, served only in provider project control planes. The
  component that registers them, with their ProtectedResources, roles and
  webhook, is in the production Milo root. The synthetic Milo root has it
  without the webhook, and the standalone cluster has only the CRDs.
  `TestComponentRoots` pins this, and no root ships the test-only kinds.
- **Custody.** One `DeviceCustody` per Device, with the same name. Its fields
  are in `spec`, like a Lease, and it has no status. The finalizer
  `fleet.datumapis.com/custody` stays until the holder is `Retired`. CEL keeps
  the fence from decreasing, keeps `Retired` terminal, and requires the active
  operation to carry the current fence.
- **Operations.** Every kind starts with one envelope: `creator`, `deviceRef`
  (the Device's name) and `authority` (exactly one of `allocationRef` and
  `serviceRequest`). The spec is immutable after create. Status phases move
  forward only, and an ended phase is final. `Pending` may end
  `OutcomeUnknown`, because rebuild holds an operation whose status still
  says `Pending`.
- **The coordinator** (`internal/custody`) is the only writer of custody and of
  operation status. Every custody change is one update that carries the
  resourceVersion just read. On a conflict it reads again and decides again.
- **The creator webhook** (`internal/custody/creatorwebhook`) admits a create
  only when `spec.creator` is the authenticated caller's username and UID, and
  an update only when the creator is unchanged.
- **The capacity projector** is a pure function that derives `Prepared`,
  `Healthy` and `Available`. Nothing calls it outside tests (**implemented and
  inert**; see [Capacity](#other-decisions)).

Creating or importing a Device creates `Unverified` custody. That sends
nothing to a BMC and makes no capacity `Available`.

## Site execution (Increment B)

Increment B adds the site framework (`internal/site`) and the Redfish
install and wipe workers (`internal/site/redfishwork`). It is **implemented and
verified**. Increment C delivers assignments to it and deletes the old
executors and their site-wide journals. Every site, simulator, local VM and
Redfish, now runs on it.

### Where a site stores fences and its journal

Each site keeps one directory per Device on its retained volume, under an
owner-only root. A Device's directory holds one state file and its lock. The
state file records the highest fence the site has seen, the highest fence for
which older workers are confirmed stopped, and the journal. The directory
also holds per-operation records, such as boot admissions, and the site's
copy of results. Every write replaces the file atomically and syncs it, so the
site still refuses older fences after a restart. Each Device has its own lock,
so work on one Device never waits for another.

### What the framework owns

- **Fences.** An assignment below the stored fence is refused and stores no
  result. An equal fence is accepted again, because delivery is replayed from
  custody after a crash. The fence is checked when a step is journaled and
  again just before it is sent.
- **Older-worker stop.** One worker runs per Device. A newer assignment raises
  the fence, then waits for the run lock. The older worker sees the higher
  fence within a second, stops, and releases the lock, and a worker that dies
  releases it with its process. The newer worker then records the stop
  confirmation, and the coordinator reads it as the site's `Stopped` fence. A
  `DeviceRecovery` is delivered the same way and runs nothing else.
- **The journal.** Each step is journaled before it is sent and marked done
  after. The journal refuses any step below the stored fence. A step found
  journaled but not done is observed instead of sent again. If the observation
  cannot show it took effect, the outcome is unknown. Only a step that is safe
  to repeat, such as powering off before a boot was requested, is sent again.
- **Deadlines and shutdown.** A worker stops at its deadline exactly. If the
  journal holds nothing for the operation, it reports `Failed`. Otherwise it
  reports `OutcomeUnknown`. A worker stopped from outside, such as a Pod
  being shut down, stores no result, so its replacement resumes from the
  journal.
- **One worker per assignment.** A redelivered assignment that already has a
  result returns it. One that finds a worker for the same assignment holding
  the run lock stores nothing and leaves the result to that worker. A copy
  that waits for the lock checks for a stored result again once it holds the
  lock, and returns that result without running, so it never sends after the
  first worker recorded the outcome. A newer
  assignment that cannot stop an older worker before its deadline ends
  `Failed`, or `OutcomeUnknown` if the journal holds steps for it.
- **Checks before any effect.** The inputs must match the admitted digest. The
  site's enrollment must match the snapshot's BMC endpoint, system UUID and boot
  MAC. The per-Device credential Secret must exist, its operator-approved pin
  must equal the key discovery recorded at admission (O9), and the BMC is read
  only over a connection pinned to that key. A different key aborts the TLS
  handshake before any request, so no credential is sent. Each failure is a
  named refusal. It ends the operation `Failed` when the journal holds
  nothing for it, and `OutcomeUnknown` when an earlier worker for the same
  operation journaled steps.
- **Results.** Each result carries the operation's fence and typed evidence,
  and messages never carry a BMC response or an endpoint. A stored result is
  final: nothing replaces it. The site keeps its copy until the coordinator
  accepts it or rejects it for a stale fence.
- **The fence report.** Each site reports, for each Device it is configured to
  serve, the fence it has seen and the fence for which it confirmed the stop.
- **Boot admissions.** A network boot is served only while the operation's
  admission is open, unexpired, and at the Device's stored fence, so a
  superseded operation can never boot the server. The boot server reads
  admissions from the site store.

### The Redfish workers

The install and wipe workers port the executor's phases: power off, arm a
one-time boot, power on once, then wait for exactly one console receipt and,
for a network boot, the boot server's delivery evidence. Power-on is the only
step that is not repeated after a crash; it is observed instead. Each site
enrolls one disk per server, named `disk-0`, with its size (O12). An
installation or wipe that names other disks is refused before anything is
sent. Each verified phase is journaled, so a resumed worker continues from
the next phase.

The port differs from the old executor in three ways:

- After a crash or a lost response, a power-on is observed and never sent
  again. The old executor sent `On` again, up to a small budget, when the
  server was still off and armed after a settle window.
- Synchronized DHCP reservations are supported from Increment C. The feed
  reads the site store, and the worker waits for the DHCP owner's reports. See
  [Boot network](../operations/boot-network.md).
- Its timing values match the old executor's, including the five-minute
  power-off bound and the 30-second wait for a receipt after power-off, and
  each is configurable.

### Access to the server

How the framework reaches a server is behind an `Access` interface.
`RedfishAccess` performs the credential, pin and identity checks above. The
simulator and local VM sites use `fixture.Access`, which checks the site's
enrollment against the snapshot and has no BMC. Each has its own workers. That
added no change to the worker contract.

## Cutover (Increment C)

Increment C moves every mode onto custody and deletes the old path. All of it
is **implemented**. The standalone smoke (`task standalone:test`) verifies it
end to end. Verification of the multi-plane local VM and Redfish runs is
pending. Increment C adds no migration and no compatibility code. The
Secret-backed coordinator and its records are gone, and a person re-imports
records where an environment needs them.

| Piece | Status |
| --- | --- |
| MachineAllocation, allocation, installation and release | **Implemented and verified** by tests against a real API server and the standalone smoke |
| Quota claim | **Implemented and verified** by unit tests. **Inert** end to end: no run exercises it |
| Delivery and the coordinator loop | **Implemented and verified** by the standalone smoke. The multi-plane runs use it too, and their verification is pending |
| Federation handoffs | **Implemented and verified** by unit tests. The multi-plane run is pending |
| Creator webhook in the production Milo root | **Implemented and verified** by unit tests and a test against a real API server. The root registers it, and nothing runs the coordinator there |
| Simulator site | **Implemented and verified** by the standalone smoke |
| Local VM site | **Implemented and verified** by unit tests. The multi-plane run is pending |
| Redfish site workers | **Implemented and verified** by unit tests. The multi-plane run is pending |
| Synthetic staging mode | **Implemented.** The manager's flag handling is unit tested, and no run exercises the mode |
| Power projector | **Implemented and verified** by the standalone smoke |
| Synchronized DHCP feed on the site store | **Implemented and verified** by a unit test. Not exercised at a site |

### MachineAllocation

A `MachineAllocation` holds the allocation's private facts (O8). It lives in the
provider project, and only the Metal provider writes it.

- **Spec (immutable).** The consumer as a typed reference, the Machine by
  namespace and name, the capacity, the Device by name, the accepted class and profile pinned by
  generation and spec digest, the CPU and memory, the hardware snapshot, the
  admission authority, an optional quota claim, the pinned cleaning policy
  digest, the boot interface and the target disks.
- **Labels.** `fleet.datumapis.com/machine-uid`,
  `fleet.datumapis.com/consumer-uid` and
  `fleet.datumapis.com/device-uid` record the incarnations the allocation was
  made for. See [No object UIDs in spec](#no-object-uids-in-spec).
- **Status.** A phase, `Active`, `Releasing` or `Released`, and the latest wipe.
- **Name.** `alloc-` and the first 16 hex characters of the SHA-256 of
  `<consumer UID>/<Machine UID>`. See [the decision](#cutover-decisions).
- **Secrets.** None. Staff recovery operators may read it.

### Consumers

An allocation names its consumer with `consumerRef`, a typed reference of API
group, kind and name in the shape of Milo's quota consumer reference. The
consumer's UID is never in the spec; the allocation's name and
`consumer-uid` label carry it.

| Consumer | `consumerRef` | Served by |
| --- | --- | --- |
| Milo project | `{apiGroup: resourcemanager.miloapis.com, kind: Project, name: consumer-a}` | The managed and multi-plane modes, through project discovery |
| Standalone namespace | `{apiGroup: "", kind: Namespace, name: fleet-project-a}` | The standalone mode, where each namespace stands in for a project |

The allocator and the API both refuse any other kind. The allocator refuses
it before it reserves quota or looks for capacity.

A published MachineClass names its source the same way:
`source: {publisherRef: {apiGroup, kind, name}, name}`, the provider that
publishes the class and the source class's name. The same kinds are allowed.
A claim allocates through a published class only when its `publisherRef`
resolves to the provider incarnation that published it.

One tenancy interface resolves a reference to the consumer's current
incarnation, its UID, and the client of its API. The allocator, release,
quota claims, the Machine power projector, staging and the standalone and
multi-plane runners reach a consumer only through it. The managed mode
resolves a project through the engaged project runtimes, so a project that is
not engaged is unavailable, never assumed gone. Only a Milo project has
quota. A quota claim is never made in a project recreated under the same
name, and releasing the Machine's claim in a recreated project finds nothing,
because the claim went with the earlier project.

### Allocation, installation and release

`internal/allocation` writes in this order: the quota claim, then the
MachineAllocation (create only), then the custody claim (a conditional
update). A retry finds the same allocation by its name.

- **A lost race.** An allocation whose Device another holder took, and which
  never held it, is deleted, and the allocator selects again.
- **Lost custody.** An `Active` allocation that no longer holds its Device is
  not repaired. The allocator returns `ErrCustodyLost`, the Machine reports
  `Pending` with reason `CustodyLost`, and an operator resolves it.
- **Installation.** The `DeviceInstallation` is named from the allocation and the
  Installation UID, which its `fleet.datumapis.com/installation-uid` label
  records. Its predecessor must be the Machine's current installation.
  A bootstrap reference is refused.
- **Release.** The allocation's holder moves to `Release` with all six gates
  open. Then:
  - Access, Network and Address close at once.
  - Operations closes when no installation of the allocation is active or
    unended.
  - Cleaning creates a `DeviceWipe`, step 1. A wipe that ended `Failed` sent
    nothing, so it gets a new step, up to step 3. Then the release stops with
    `ErrWipeFailed`. A wipe that ended `OutcomeUnknown` gets a new wipe only
    after a `DeviceRecovery` `Retry`, under the name custody gives it.
  - Quota deletes the claim and observes it gone.
- **Done.** Custody returns the Device to `None` only when no gate, hold or
  operation remains. Then the allocation becomes `Released`.

### Quota claim

`internal/milo` creates a Milo `ResourceClaim` named `fleet-machine-` and the same
16 hex characters, in the Machine's namespace. Release deletes it and waits until
it is gone. No mode in this repository has Milo quota. The claim is **implemented
and verified** by unit tests, but **implemented and inert** end to end: no run
exercises it.

### Delivery and the coordinator loop

`internal/delivery` builds each assignment from custody's active operation and
the operation spec exactly as admitted. It delivers the assignment to the sites
serving the Device once the admission is copied to operation status. It collects
results and accepts them by fence. The coordinator loop runs reconcile, deliver,
collect and the retention sweep. The coordinator configuration names the sites
that serve each Device.

### Identities

Every mode uses three identities, one per component:

| Identity | Writes |
| --- | --- |
| The custody coordinator | Custody, operation status. It is the only custody writer. |
| The Metal provider | MachineAllocations and DeviceInstallations |
| The release controller | DeviceWipes |

An operation's creator is the identity a `SelfSubjectReview` reports.

- **Standalone** enforces the creator rule with a ValidatingAdmissionPolicy
  (`config/standalone/site.yaml`).
- **The production Milo root** (`config/milo`) includes the device-operations
  component, with the creator webhook served by `fleet-registration`.
- **The synthetic Milo root** (`config/milo-synthetic`) includes the kinds,
  ProtectedResources and roles, but not the webhook, because it installs no
  registration webhook.
- **The multi-plane local Milo** enforces the CRD rules but registers no creator
  webhook. This is a gap. See [Points for a person](#points-for-a-person).

### Sites

- **The simulator is a site.** `cmd/fleet-simulator` serves a site HTTP API
  (`internal/site/siteapi`). Its workers (`internal/site/simwork`) keep the
  simulated server in fenced records in the site store.
- **The local VM site** (`internal/site/vmwork`) ports the old executor. It
  gives each installation an exclusive disk. It proves the installer boot with one
  console receipt, and in PXE mode also with boot deliveries and a DHCP lease. It
  then does a disk boot. A boot is started once and is observed after a crash.
  The boot server serves only an open admission at the Device's stored fence. A
  wipe deletes every installation disk of the Device.
- **The Redfish workers** (`internal/site/redfishwork`) are live. Each reads the
  Device's own credential Secret by the [BMC discovery](../operations/bmc-discovery.md)
  contract. There is no shared credential. The boot server and the synchronized
  DHCP feed read the site store.
- **Fixture access and discovery** (`internal/site/fixture`) serve synthetic
  Devices that have no BMC. Access checks the enrollment against the snapshot.
  Fixture discovery writes Device status the way BMC discovery would. Who writes
  it depends on the mode; see [Modes](#modes).

### Federation

`internal/coordination` carries one site assignment per `MachineAllocationHandoff`
(`spec.device` and `assignmentJSON`), or a request for a Device's fence report.
The cell observer writes the fence report and the stored result to the handoff's
status. The coordinator accepts only this cell's observation of exactly the
assignment the handoff carries. It then labels the handoff answered, and the cell
deletes its copy of the result.

Worker Jobs are created by kro as before. A worker exits 65 for a handoff that
is not for this cell, or for an assignment below the Device's stored fence. The
private custody namespace on the hub host is gone.

### Modes

| Mode | Where the coordinator runs | Site | Fixture discovery writer | Status |
| --- | --- | --- | --- | --- |
| Standalone smoke | A Deployment | The simulator | The simulator site | **Implemented and verified** by `task standalone:test`. The phases run as the Metal provider, with the coordinator's and release controller's tokens. |
| Synthetic staging | `fleet-manager`, under its leader lease | The simulator | The manager | **Implemented.** The manager's flag handling is unit tested. No run in this repository exercises it. |
| Multi-plane, local VM and Redfish | A workstation process for the run (`hack/multiplane/coordinator.sh`) | The cell, over Karmada | A separate discovery identity on the workstation | **Implemented.** Verification of both runs is pending. |

- **Synthetic staging.** The manager runs the coordinator loop and fixture
  discovery. Its flags are `--synthetic-coordinator-token-file`,
  `--synthetic-release-token-file`, `--synthetic-site-enrollment-file` and
  `--synthetic-device-namespace`. The overlay mounts two environment-supplied
  Secrets, `fleet-custody-coordinator-auth` and `fleet-release-controller-auth`,
  and a `fleet-synthetic-site-enrollment` ConfigMap. The ConfigMap in the overlay
  holds a synthetic placeholder, and an environment replaces it. An environment
  must also re-import its records and create the two credentials; see [Points for
  a person](#points-for-a-person).
- **Multi-plane.** The provider project gets the inventory and custody CRDs and one
  token per component. The lab BMC serves TLS with a key generated per run, and
  each Device's credential carries that key's digest as its pin. Real BMC
  discovery is not part of the run. This is a lab gap. One lab BMC serves both
  targets with one key, and custody refuses a key that two active Devices
  record, so only target A is a Device. Target B stays enrolled at the site,
  with its own credential, to prove isolation.

### Machine power

The manager's `PowerProjector` is the only writer of `Machine.status.power`. It
reports `Unknown` while a recovery hold exists or custody is unverified, and
otherwise the power state discovery last observed. A Machine with no active
allocation has no power. The harness runners call the same projection. In the
manager, custody, allocation and Device events in the provider project wake
the consumer's Machines, so a new hold shows as `Unknown` without a Machine
event.

### Deleted, with no compatibility

- The Secret-backed coordinator and its records, the private-record RBAC, the
  private namespace and its admission policy, and the manager's private-namespace
  plumbing (`--private-namespace` is removed).
- The executor client, the simulator store, the local VM and Redfish executors and
  their journals, and the shared Redfish credential.
- The old handoff `requestJSON` shape.
- The single-plane local VM harness (`hack/local-vm/run.sh`,
  `task local-vm:acceptance`, `cmd/fleet-local-vm`). The multi-plane local VM
  mode runs the same workers. This is a decision for a person; see [Points for a
  person](#points-for-a-person).

### Unchanged and still open

- The capacity projector stays unwired (O1), so the synthetic fixtures still write
  `Prepared`, `Healthy` and `Available`.
- Bootstrap stays refused (O10).
- `DeviceRecovery` needs no second person.

## Implementation decisions

Each decision lists the open point from the implementation brief it settles.

### Who created an operation (O14)

- The client sets `spec.creator` to its own username and UID. The webhook
  rejects any other value and any change. That is how #174 at `d4d17f9` reads,
  so "clients cannot set the creator" becomes "clients can set only
  themselves". CEL also makes the creator immutable, as part of the spec.
- Milo's root API server calls the webhook, because Milo v0.32.5 does not
  evaluate admission policies stored in a project. The webhook reads the
  original caller from the admission request, with the project in Milo's
  extras, and refuses a caller outside the configured provider projects. It
  reads nothing else and keeps no state.
- The existing `fleet-registration` server serves the webhook on its own
  path, `/validate-operation-creator`. It shares that server because the caller,
  the transport and the provider-project scope are the same, and the handler
  keeps no state. The production Milo root registers it (Increment C).
- The coordinator classifies a creator by configuration. Each service class,
  such as the Fleet provider for Metal or the release controller, maps to
  configured principals by username and, optionally, UID. Any other creator is
  staff. IAM decides which staff may create which kind, and the API server has
  already authorized the create. No identity is hard-coded.

### Admission

- **The rule table is data.** Each row names a kind, an optional variant (such
  as a recovery decision), a creator class, an authority type, the holders and
  custody state it admits from, how it treats a recovery hold, and whether it
  queues. A kind's rows are matched only against operations of that kind, so
  adding a row never changes how another row is evaluated.
- **Queueing.** Provider and release rows queue. They wait in `Pending` while
  another operation runs, while custody is `Unverified`, while discovery is
  not current, or while a recovery hold that they cannot settle exists. Each
  of these can clear, and a queued operation's name is deterministic, so it
  could not be created again. Staff rows never queue: those conditions end a
  staff operation `Failed`.
- **Phases for refusals (O5).** A refusal ends `Failed` with a reason, and
  nothing is sent. Expired, superseded and no-longer-matching operations end
  `Cancelled`. A `Pending` operation whose holder no longer matches is
  `Cancelled`. One that never matched is `Failed`. Kinds add their own
  reasons.
- **Shared reasons.** The admission reasons use the names of #174's table:

  | Reason | Refused by |
  | --- | --- |
  | `CustodyUnverified` | Rule 1 |
  | `CreatorNotPermitted` | Rule 2 |
  | `HolderMismatch` | Rule 2 or 3 |
  | `OperationInProgress` | Rule 4 |
  | `DeviceIdentityMismatch` | Rule 5, both a mismatched identity and discovery that is not current |
  | `SharedPublicKey` | Rule 6 |
  | `RecoveryHold` | A recovery hold |

  Rule 7 is a kind's own checks of its inputs, with its own reasons. They run
  before the shared identity and key checks of rules 5 and 6, so a kind's more
  specific reason, such as power's `SystemUUIDNotRecorded`, is the one
  reported. All rules must hold either way; the order decides only the
  reason. A staff
  operation not admitted within five minutes ends `Cancelled` with reason
  `Expired`. This implementation adds `NameMismatch`, for an operation not
  named by its rule, and outcome reasons: `Superseded`, `Withdrawn`,
  `DeadlineExceeded`, `SiteNotConfirmed`, `SiteCouldNotProve`,
  `InvalidEvidence`, `SnapshotInvalid` and `UndeclaredReason`.
- **Retries.** An operation created after a `Retry` is named from the held
  operation: its name, `-retry-`, and the number of `Retry` decisions for it,
  which custody records with the hold. This applies to every kind whose
  settlement needs a `Retry`, such as a wipe, and the kind's own naming rule
  does not apply to it. Until the `Retry` is applied, the retried operation
  waits in `Pending`, a staff one included, and does not expire while it
  waits. The `Retry` is used when that operation is admitted. If
  the retried operation also ends unknown, its hold keeps the original name
  and count, so the next retry is named `-retry-2`, not `-retry-1-retry-1`.
  Rebuild reads that lineage back from the names.
- **Replacement.** Only an operation that passes the creator, authority,
  holder and allocation checks on a row that queues can replace an older
  pending one, of the same kind under the same allocation. A request refused
  its permission check therefore replaces nothing, and staff requests, whose
  rows never queue, never replace one another.
- **Settling operations come first.** Pending replacement never cancels an
  operation that settles the hold. Other operations wait in `Pending` until the
  hold is settled, and they do not hold back the operation that settles it.
- **Holders under a hold.** A row can admit from a holder only to settle a
  hold of its kind. The staff power row, "`None`, or `Service` under a power
  hold", is two rows: one from `None`, and one from `Service` that requires a
  hold the operation settles.
- **Current identity (O9).** Identity is current when BMC discovery reports
  `IdentityMatched` true for the Device's current generation, its last complete
  inventory read is at most two hours old (twice the default discovery
  interval), and it recorded the BMC key at the current endpoint. Admission
  check 6 compares the observed key digest across Devices that are not
  retired. The snapshot carries the observed digest, and the site worker must
  check it against the operator-approved pin before sending anything
  (Increment B).
- **Only `Retire` and `Resolve` skip checks 5 and 6.** A `DeviceRecovery`
  with `Retire` or `Resolve` skips current discovery and the shared-key check,
  because it must work when a BMC is unreachable, for example to retire a dead
  server. The operation's `device-uid` label is always checked against the
  Device's UID. `Settle` still needs current
  discovery: settling a power hold needs fresh discovery evidence of the power
  state, as the #174 settlement table says. `Retry` needs no evidence of its
  own, and it still passes both checks. The settling operation it authorizes
  goes through normal admission as well. Only a
  kind that cannot reach the server may skip these checks, and the registry
  refuses any other.
- **Names.** A row may set its own naming rule, such as generated names for
  staff power and generation-based names for provider power; otherwise the
  kind's rule applies. An operation that does not follow its rule ends
  `Failed` with `NameMismatch`. A `DeviceInstallation` is named from its allocation and
  a digest of the Installation UID in its `installation-uid` label. A `DeviceWipe` is named from its allocation
  and step. Staff kinds must use a generated name.
- **Order.** Pending operations are admitted oldest first. Ties break on kind,
  then name, then the object UID, so every coordinator picks the same one.
- **Refusals use current custody.** The coordinator reads custody, lists the
  operations, and reads custody again. It decides only if custody did not
  change in between. A claim followed by its installation's create is
  therefore seen before the installation could be refused.
- **Racing refusals.** Delivery must wait until operation status shows
  `Admitted`. If a racing coordinator ends an operation, or it is deleted,
  after custody admitted it but before the status copy, custody withdraws it
  (`Cancelled`, `Withdrawn`). Nothing was delivered, and the fence stays
  raised.
- **Fences.** Admission raises the fence above both custody's fence and every
  fence a serving site reports having seen. Each site reports two fences: the
  highest it has seen, and the highest for which it confirmed that older
  workers stopped. A site report at or above an admission's fence therefore
  proves the site received that admission.

### Deadlines and clock skew (O4)

| Kind | Deadline |
| --- | --- |
| `DeviceInstallation` | 40 minutes, the Redfish executor's install bound |
| `DeviceWipe` | 40 minutes, the Redfish executor's cleanup bound |
| `DevicePowerOperation` | 8 minutes: the two-minute start-by window, four BMC requests of at most 30 seconds, the three-minute observation, and a minute of margin |
| `DeviceRecovery` | 10 minutes to collect the sites' fence reports |

The worker stops at the deadline exactly (Increment B). The coordinator
declares `OutcomeUnknown` only after the deadline plus a two-minute skew
margin. A staff operation that is not admitted within five minutes expires.
Each value is configurable.

### Unknown outcomes and recovery (O2, O3, O6, O7)

- `OutcomeUnknown` moves the operation from `activeOperation` to
  `recoveryHold` in the same update, keeping its kind, name and fence.
  The holder stays.
- A result whose evidence does not match the kind's schema proves nothing, and
  ends `OutcomeUnknown` with `InvalidEvidence`.
- A settling operation records the hold it settles. If it succeeds, the hold
  is cleared. If it also ends unknown, its hold replaces the old one.
- A `DeviceRecovery` is admitted like any operation and raises the fence. The
  coordinator applies it only after every site serving the Device confirms
  that every worker below that fence stopped. If no site
  confirms before its deadline, it ends `Failed` with `SiteNotConfirmed`, and
  the hold stays. It does not end `OutcomeUnknown`, because it cannot change
  the server.
- `Settle` clears a hold only when the held kind provides a settle check, and
  that check accepts the evidence. Installation and wipe holds have no such
  check: later operations settle them.
- `Retry` marks the hold so that the held operation's requester can create one
  settling operation. The hold carries what #174 asks of it: the held
  operation, its `Retry` count (`retries`), and whether a `Retry` is unused
  (`retryAuthorizedBy`). Admission accepts a `Retry` only for a hold whose
  settlement row requires one, meaning the held kind is `Retryable` and a kind
  settles it after a `Retry`, and refuses one while an earlier `Retry` is
  unused. Applying a `Retry` raises the count, and admitting the settling
  operation uses it.
- `Retire` needs a hold and a `Service` or `Release` holder. Retiring from
  `Release` requires a disk disposal record.
- `Resolve` is the only operation `Unverified` custody admits. It records the
  holder (`None`, `Allocation`, `Release` or `Retired`), marks custody
  `Verified`, and adopts the current Device UID in custody's `device-uid`
  label. The holder must agree with
  the Device's live MachineAllocations, both at admission and when it is
  applied: `None` or `Retired` when none names the Device, and `Allocation`
  or `Release` with the one that does. A rebuilt hold stays, to be settled
  separately.
- Nothing retries an operation automatically.

### Rebuilding custody (O13, O15, O16)

- Missing custody is created `Unverified` from the Device's lifecycle and
  succeeded `Retire` recoveries, its MachineAllocations that are not
  `Released`, and its operations. A releasing allocation rebuilds `Release`
  with every gate open.
- Every admitted operation that has not ended is held, even if its status
  says `Pending`, because custody is written before the rest of its status.
  Its status is ended `OutcomeUnknown` with reason `Rebuilt`, and it is never
  admitted again. An operation records its admission before custody names
  it, so one without an admission record was never admitted and is not held.
- An operation that ended `OutcomeUnknown` is held again unless something
  later settled it: a succeeded operation with a higher fence whose kind
  settles the held kind, or a succeeded recovery that cleared that exact
  hold.
- If several operations would be held, the one with the highest fence is
  held and custody stays `Unverified`, so a `Resolve` must decide and the hold
  is never dropped.
- Two live allocations, holder sources that disagree, a Device UID that
  differs from custody's `device-uid` label, or a missing Device also keep
  custody `Unverified`
  until a `Resolve`.
- Custody becomes `Verified` only after every site configured to serve the
  Device reports its stored fence. The new fence is above every fence those
  sites and the operations have used. A Device no site serves stays
  `Unverified`. Sites are configured explicitly and never inferred from
  placement.
- The site's fence store must be durable, beside the journal on its retained
  volume, so a site refuses older fences after a restart (Increment B).

### Where an allocation's private facts live (O8)

Decided: the allocation's facts live on its MachineAllocation, in the provider
project, and only Fleet writes it. This follows the [MachineAllocation
record](../enhancements/machine-lifecycle/README.md#machineallocation-record)
of the machine lifecycle: identity, the accepted contract, the hardware
snapshot, authority and quota evidence, and lifecycle and release obligations.
Immutable accepted inputs go in spec. Lifecycle goes in status. Each
allocation is named from its Machine UID, per #174. Nothing secret goes on it,
and staff roles may read it. Increment C implements this (**implemented**; see
[MachineAllocation](#machineallocation)). It replaces the private `AssetRecord`
and the other Secret records, which are deleted.

### Release gates (O17)

The gates are the machine lifecycle's release obligations: `Operations`,
`Access`, `Network`, `Address`, `Cleaning` and `Quota`. The release
controller closes each one with evidence. A gate the coordinator does not know
stays open. The Device returns to `None` only when no gate, hold or operation
remains.

### Staff access and customer data (O10)

- The **device operations viewer** reads custody and operations, but not
  MachineAllocations, so it does not see which customer holds a server.
- The **device recovery operator** inherits the viewer, creates
  `DeviceRecovery`, and reads MachineAllocations, because a `Resolve` must
  name an allocation and staff identify the customer through it.
- A `DeviceRecovery` does not need a second person in this increment. The
  creator is recorded and the role is narrow. This is listed under [Points for
  a person](#points-for-a-person).
- Staff roles may read MachineAllocations, because nothing secret is on them.
  Only the recovery operator role grants that read today.
- Bootstrap stays refused. A `DeviceInstallation` that names a bootstrap
  reference ends `Failed` with `BootstrapUnsupported`. No role can read a
  Secret, a sealed store, or a bootstrap payload, and a test checks the role
  definitions.

### Other decisions

- **Retention.** Finished operations are deleted 30 days after they end,
  unless custody still names them as active or as the hold. A succeeded
  retirement is kept until inventory retires the Device, because until then
  it is the only record rebuild can read.
- **Rejected results.** A result whose fence or operation does not match the
  active operation is refused, and custody counts it and records the last one
  for monitoring. It never changes the holder. A retry of a result already
  recorded is accepted without a second write.
- **Boot interface (O12).** `bootInterface` resolves by name against the
  Device's interfaces, and the snapshot records its MAC. Target disks stay a
  site-local enrollment fact that the worker checks (Increment B).
- **Cleaning policy (O11).** `DeviceWipe` requires the digest of the cleaning
  policy record pinned at allocation. A resolver maps the capacity's cleaning
  policy reference to that digest when the allocator creates the allocation. The
  standalone, staging and multi-plane modes supply a fixture resolver.
- **Capacity (O1).** The projector stays a pure function. `Prepared` needs a
  succeeded commissioning under the current plan, `Healthy` needs current
  commissioning and discovery evidence, and `Available` also needs `Verified`
  custody, holder `None`, no hold and an enabled allocation policy. Wiring it
  as written would make every server unallocatable, because commissioning is
  not built.

### No object UIDs in spec

No resource spec holds an object UID (#211). A deterministic name, a label or
status pins each incarnation. The one exception is an operation's
`creator.uid`, the authenticated caller's UID, as in a Kubernetes
CertificateSigningRequest. Each same-name recreation is guarded, and a test
fails if its guard is removed:

| Recreated | Guard | Test |
| --- | --- | --- |
| Machine or consumer | The allocation's name hashes both UIDs, and its `machine-uid` and `consumer-uid` labels must match | `TestAnAllocationForAnotherMachineIncarnationIsAConflict` |
| Machine or consumer, against the authority | The allocator refuses an admission authority that records the Machine or consumer UID, so the decision is named by name | `TestTheAllocationAuthorityNamesNoIncarnation` |
| Consumer, under another kind or name | The allocation's `consumerRef` must name the Machine's consumer | `TestAnAllocationForAnotherConsumerIsAConflict` |
| Publisher, against a published class | A published class's `publisherRef` must resolve to the provider incarnation that published it | `TestAClassFromAnotherPublisherIsRejected` |
| Consumer project, against quota | Quota resolves the project and requires the incarnation the Machine was admitted in | `TestQuotaServesOnlyTheMachinesProjectIncarnation` |
| Device | Custody's `device-uid` label must match the Device, or custody stays `Unverified` until a `Resolve` | `TestARecreatedDeviceLeavesCustodyUnverified` |
| Device, against free capacity | Custody for another Device incarnation is never free | `TestCustodyForAnotherDeviceIncarnationIsNotFree` |
| Device, against an operation | An operation's `device-uid` label must match the Device at admission | `TestAdmissionPreconditions` |
| Device, against an existing operation | The Metal provider and release controller require an existing same-name operation to carry the same labels | `TestAnOperationForAnotherDeviceIncarnationIsAConflict` |
| Installation | The `DeviceInstallation` name hashes the Installation UID, so a same-name Installation gets a new operation | `TestAllocateInstallReleaseReuse` |
| Operation, as the active operation | Custody names an operation by kind and name with the fence it admitted it at, and an operation matches only if its `status.admission.fence` carries that fence | `TestARecreatedActiveOperationNeverGetsItsResult` |
| Operation, as the last result | The same match: the result is never copied to a same-name operation created later | `TestARecreatedOperationNeverInheritsItsLastResult` |
| Operation, as the recovery hold | The same match: a same-name operation is neither held nor excluded as the held one | `TestARecreatedHeldOperationNeverTakesTheHold` |
| Operation, as a Retry's settling operation | The same match: a same-name operation is never admitted or settled as the Retry's | `TestARecreatedRetryIsNeverCountedAsTheRetry` |
| Operation, as a later retry at rebuild | A later retry supersedes a held operation only if it carries an admission record | `TestARecreatedRetryNeverSupersedesARebuiltHold` |

The coordinator writes `status.admission.fence` to the operation before it
writes custody, so custody never names an operation that cannot prove it is
the one admitted. A same-name object created later has no admission record,
so it never matches custody's active operation, last result, hold or Retry
lineage. An operation without an admission record was never admitted, so
rebuild holds only operations that carry one.

The site keys its journal, results and boot records by the admission, not by
an object UID: `<lowercased kind>.<name>.<fence>`, with the name replaced by
its digest if the key would exceed the site store's 128-character limit. A
same-name operation admitted later has a higher fence and so another key.

### Cutover decisions

Each decision was made while building Increment C. Each is open for review by a
person.

- **Allocation names.** An allocation is named from the Machine UID within its
  consumer project, and its labels record both UIDs. A retried create then
  collides with the first, and equal Machine UIDs in two consumer projects
  never share an allocation.
- **Lost custody is not repaired.** An allocation that never held its Device is
  deleted and the allocator selects again, because it exposed nothing. An `Active`
  allocation that lost custody means the records disagree about who holds a
  server a customer may be using. Fleet leaves it to an operator.
- **Release gates.** Access, Network and Address close at once, because no mode
  provisions them. There is nothing to wait for.
- **Wipe retries.** A wipe that ended `Failed` sent nothing, so a new step is safe.
  A wipe that ended `OutcomeUnknown` may have written to disks, so only a
  `DeviceRecovery` `Retry` allows another.
- **Sites are configuration.** The sites that serve a Device are explicit
  coordinator configuration, because inventory has no sites field yet. They are
  never inferred from placement.
- **Three identities in every mode.** The coordinator is the only custody writer.
  The provider and the release controller each create only their own operation
  kinds. One identity per component keeps one writer per surface and lets the
  creator rule tell the callers apart.
- **The creator webhook shares `fleet-registration`.** The caller, transport and
  provider scope are the same, and the handler keeps no state.
- **Fixture discovery writer by mode.** In standalone the simulator site writes it.
  In synthetic staging the manager writes it, because the staging site has no API
  credential. In multi-plane a separate discovery identity on the workstation
  writes it, in both modes.
- **No migration.** The old records are deleted and nothing reads them. Fleet
  keeps no legacy compatibility, and re-importing records is acceptable.
- **Quota follows the Machine.** The quota claim is named from the Machine, as
  its allocation is. A Machine deleted before its allocation existed, such as
  one still waiting for capacity, releases its claim by that name.
- **Each worker reads only its Device's credential.** Each Device has its own
  BMC credential Secret, named from the Device (`bmc-` and 20 hex digits of
  the Device's SHA-256), bound to it by the discovery contract and its
  approved pin; there is no shared credential. The coordinator writes that
  name into the operation's handoff, and kro grants the operation's worker
  `get` on exactly that Secret, with no list or watch. The worker refuses a
  handoff whose name is not its Device's, reads the Secret with one get, and
  still requires the contract's annotations and pin. Admission lets kro create
  a credential Role only of that shape.
- **The observer deletes answered results.** The site observer is the only
  handoff status writer, and it deletes the site's copy of a result once the
  coordinator answered it. Its mount of the site store is therefore writable.
- **One staging process, three identities.** Synthetic staging runs the
  coordinator, the Metal provider and the release controller in the manager
  process with three credentials. Each write carries its component's identity,
  but one process can act as all three. This is the synthetic seam, not
  production separation.

### Extension points for power and commissioning

The agent rebuilding `DevicePowerOperation` on this foundation asked for
these seams. Each is decided here.

| # | Seam | Decision | Status |
| --- | --- | --- | --- |
| 1 | Kind checks see the Device the snapshot records, an injected clock and the discovery interval | Checks get the Device object the snapshot is built from, `Admission.Now` and `Admission.DiscoveryInterval`. Before the custody update, the coordinator reads the Device again. If its resourceVersion changed, it decides again, so discovery cannot change between the check and the update unseen. | Implemented and inert |
| 2 | Pending replacement is scoped to the kind and the authority | A newer pending operation replaces only an older one of the same kind under the same authority. A power request never cancels a pending installation. | Implemented and inert |
| 3 | Kinds add bounded snapshot fields | `SnapshotExtra` adds up to 16 camelCase fields of at most 256 bytes each. The snapshot goes to the operation, the active operation, the hold, and the site. | Implemented and inert |
| 4 | The settle hook sees the hold's end time, its deadline and current Device status | The hold records when it ended (`Since`), the operation's deadline and its snapshot. The hook gets the Device as read when the recovery is admitted and again when it is applied. A hold that rebuild created has `Since` set to the rebuild time, and copies the deadline and snapshot from the operation's status, so both are absent for an operation whose status never recorded them. A hook checks for that. | Implemented and inert |
| 5 | Kinds register their refusal reasons | Each reason is declared with where it is raised. The registry refuses a malformed name, a shared name, a duplicate, or a missing source. A coordinator refusal, or a site result, with an undeclared reason is recorded as `UndeclaredReason`. A recovery's refusal may carry the held kind's declared reason only when it comes from that kind's settle hook. The site framework's own reasons are shared, and the Redfish workers' reasons are declared on their kinds. | Implemented and inert |
| 6 | A kind may settle no hold, and declares whether `Retry` applies | `Settles` may be empty. `Retryable` marks a kind whose holds a `Retry` may unblock: a wipe, and power, whose hold settles with `Settle` and fresh discovery evidence or with `Retry` and a later power operation. A hold counts the `Retry` decisions applied to it, and the operation created after a `Retry` is named from the held operation and that count, as [Retries](#admission) describes. | Implemented and inert |
| 7 | End-to-end proof that Milo enforces the CRD rules | The `custody-crd-rules` phase of both local end-to-end runs installs only these CRDs into the provider project, requires the project API to refuse eight forbidden writes, and removes them. | Implemented and verified in the local end-to-end runs |
| 8 | Journal steps carry a kind's payload | A step records a payload of at most 1 KiB, such as the send time or the power state before, journaled before it is sent. Observation after a crash reads it back. | Implemented and inert |
| 9 | Delivery carries the admission time, and kinds declare a start-by window | A kind that implements `StartBy` refuses as `Stale` when its worker starts later than the window after admission, with nothing sent. A worker resuming steps an earlier worker sent is not stale. | Implemented and inert |
| 10 | The TLS and identity check is separate from install capabilities | The framework verifies the pin and the System only, through the Redfish client's identity walk. Each kind checks the capabilities it uses, such as the Pxe and Hdd boot targets, and drives the System through a connection bound to it. | Implemented and inert |
| 11 | A worker refusal with nothing sent ends `Failed` | It ends `Failed` under the operation's fence and returns a `Service` holder to `None`. It never ends `OutcomeUnknown`. Whether anything was sent comes from the journal. | Implemented and inert |
| 12 | The single writer of `Machine.status.power` | The manager's `PowerProjector` is the only writer. It reads the custody of the Machine's allocation: while a recovery hold exists, or custody is unverified, it reports power `Unknown`. Otherwise it reports the power state discovery last observed. A Machine with no active allocation has no power. The harness runners call the same projection. A power operation's evidence becomes a second source when the power kind exists. | Implemented and verified by the standalone smoke, which reads the projected state |

Rows 1 to 11 keep the status recorded when Increment A added them. The
install and wipe kinds use some of them, such as step payloads and the refusal
rule. No power or commissioning kind exists yet, so no shipped kind uses the
settle hook or `Supersedes`. None of these conflicts with #174.

## Points for a person

These need a decision before the increment that depends on them.

- **O1: the interim source of `Prepared` and `Healthy`.** Still open after C.
  The projector stays unwired, so the synthetic fixtures write `Prepared`,
  `Healthy` and `Available` themselves.
- **O10: the sealed store for bootstrap payloads.** Bootstrap stays refused in
  every increment until a store is chosen.
- **A second person for `DeviceRecovery`.** Not required yet. A person
  accepted this for now, and it stays open for a later decision.
- **Synthetic staging needs its records imported again, and two new
  credentials.** The cutover deletes the old records and reads none of them.
  Before the new manager runs in an environment, a person must re-import the
  Devices with their BMC endpoints and identities and the capacities, create two
  credentials with the `fleet.datumapis.com-device-coordinator` and
  `fleet.datumapis.com-device-wipe-requester` roles, and supply
  the site's enrollment. See [Modes](#modes).
- **Retiring the single-plane local VM harness.** C removed
  `hack/local-vm/run.sh`, `task local-vm:acceptance` and `cmd/fleet-local-vm`,
  because the multi-plane local VM mode runs the same workers. A person should
  confirm that the single-plane run is not needed.
- **The multi-plane creator webhook gap.** The local Milo in the multi-plane run
  enforces the CRD rules but registers no creator webhook. A forged
  `spec.creator` is therefore not refused in that run, and the run does not
  prove the webhook. A person decides whether the run must install it.

Already decided by a person: [where an allocation's private facts
live](#where-an-allocations-private-facts-live-o8), that [only `Retire` and
`Resolve` skip checks 5 and 6](#admission), and that a `DeviceRecovery` that
no site confirms ends `Failed`, not `OutcomeUnknown`. The [cutover
decisions](#cutover-decisions) are recorded for review and are not yet
confirmed by a person.

## Adding an operation kind

A new kind, such as `DevicePowerOperation` or `DeviceCommissioning`, plugs into
four seams. It needs no change to the coordinator. The test-only kinds in
`internal/custody/testkinds_test.go` use every seam. Copy them.

1. **Register the kind.** Add a `custody.Kind` to the registry with:
   - its GroupVersionKind, constructors, and an `Items` adapter;
   - a digest of its exact inputs (`custody.SpecDigest` covers the whole
     spec);
   - its naming rule: `DeterministicName` with a `Name` function for provider
     and release kinds, or `GeneratedName` for staff kinds. A row can override
     it with its own `Naming` and `Name`. An operation created after a `Retry`
     is named by the coordinator's retry rule instead;
   - its `Deadline`, and `Effectful` if it can reach the server;
   - `Supersedes`, if a newer pending operation of the same kind and the
     same authority should cancel an older one, as power needs. It never
     cancels another kind's operation;
   - `Check` for kind-specific admission checks. They run after the shared
     preconditions, before the custody update, and return a named refusal,
     such as power's `SystemUUIDMismatch` or `DiscoveryNotCurrent`. A check
     sees the same Device object the snapshot records, the coordinator's
     injected clock as `Admission.Now`, and `Admission.DiscoveryInterval`;
   - `Reasons`, every refusal reason the kind raises, each marked as raised
     by the coordinator, the worker, or both. A refusal with a reason that is
     neither shared nor declared is recorded as `UndeclaredReason`;
   - `SnapshotExtra`, for bounded fields the kind adds to the snapshot, such
     as the reset types discovery offered. The worker and a recovery see
     what admission saw;
   - `Settles`, the held kinds a success settles, with `RequiresRetry` when a
     `DeviceRecovery` `Retry` must come first. It may be empty;
   - `Retryable`, if a `DeviceRecovery` with `Retry` applies to the kind's
     holds.

   Embed `OperationEnvelope` inline in the spec, and use `OperationStatus` as
   the status. Add the CRD with the same envelope, the immutable-spec rule and
   the phase rule, plus a ProtectedResource and roles.
2. **Add its rows.** Add one `custody.Rule` per row of the #174 operations
   table: the creator class, the authority type, the holders it admits from,
   its hold behavior, and whether it queues. A holder admitted only under the
   kind's own hold, such as `Service` under a power hold, is a separate row
   with `HoldSettlesRequired`. Staff rows never queue. To admit
   a new service principal, add its class to the coordinator's principal
   configuration. Never hard-code an identity.
3. **Implement its site worker.** Implement `site.Kind`: `Check` runs after the
   framework's checks and must not change the server, and checks the
   capabilities the kind uses. `Run` performs every effect through
   `Work.Step` with a `site.Step`. Give each step a bounded `Payload` for what
   its observation needs, and an `Observe` that reads the server and that
   payload to show whether the step took effect, for replay after a crash or
   a lost response. Mark a step `Idempotent` only if sending it twice is
   harmless. Implement `site.StartBy` if the work must begin soon after
   admission. Return the
   typed evidence. The framework owns the fence, the journal, the credential
   lookup, the TLS pin and identity checks, the deadline, and result delivery,
   and a kind never implements them. A Redfish kind reads its connection from
   `Work.Connection` as a `*site.RedfishConnection`. Register the worker in
   the site's `Runner.Kinds` under the kind's name. See
   `internal/site/redfishwork` and its tests.
4. **Define its evidence.** Set `Evidence` to `custody.StrictEvidence` over a
   typed, bounded Go struct. Its validator receives the operation, so it can
   require what that operation must prove, such as reset proof for a
   restart. Set `EvidenceRequired` if a success without evidence proves
   nothing. Unknown fields, oversized evidence and trailing
   data are rejected, and the result then proves nothing. Evidence never
   carries a credential or a raw BMC response. If a `DeviceRecovery` `Settle`
   can settle a hold of the kind, set `RecoverySettle` to validate the staff
   evidence. The hook sees the hold, with when the held operation ended
   (`Since`), its deadline and its admission snapshot, and the Device as read
   when the hook runs. A hold that rebuild created may lack the deadline and
   snapshot, and its `Since` is the rebuild time. Power, for example, accepts only a power state that
   discovery read after the operation ended.

Test the kind with the shared harness in `internal/custody`: register it
beside the defaults, and run its refusals, admission, results and holds
against a real API server.

## Device power operations

This section records how Fleet implements the power kind of the [device power
operations proposal](https://github.com/datum-cloud/fleet/pull/173) (#173,
pinned at `d6744ab`) on this foundation. Step P1, the control plane, is
**implemented**: the coordinator admits, holds and settles power operations in
every mode the cutover wires. Step P2, the site worker, is **implemented**
behind the site worker contract. Nothing creates power operations yet. It
follows [Adding an operation kind](#adding-an-operation-kind). It changes
neither the coordinator nor the site framework, and the worker adds only
reasons to the kind's reason table. The kind joins the creator webhook's
governed kinds, standalone's creator policy and each coordinator's access, and
`TestEveryOperationKindIsGoverned` requires every operation kind to be
governed.

| Piece | Decision |
| --- | --- |
| Inputs | `action` is `On`, `ForceOff`, `ForceRestart` or `PowerCycle`. A staff operation requires `expectedSystemUUID`, in lowercase canonical form. Its reason is the envelope's `serviceRequest.reason`, which is already required, rather than a second field. A provider operation omits `expectedSystemUUID` and requires `machineGeneration`. CEL enforces both. |
| Names | The kind's rule is a generated name, which the staff rows use. The provider row sets its own rule: `<allocation>-power-<Machine generation>`, so its allocation name is at most 200 characters. A retry takes the foundation's retry name, and a staff retry created before its `Retry` waits in `Pending`. |
| Rows | The Fleet provider for Metal, with `allocationRef`, from holder `Allocation`, queued. Staff, with `serviceRequest`, from holder `None`, and from `Service` only to settle a power hold. |
| Admission checks | `SystemUUIDMismatch` compares `expectedSystemUUID` with the recorded UUID. `DiscoveryNotCurrent` requires the inventory read to come from the current BMC endpoint, within twice the discovery interval on the injected clock, and to include the recorded system. `ResetTypeNotOffered` requires that system to offer the action. `SystemUUIDNotRecorded` refuses a staff request for a Device with no recorded UUID. These run before rules 5 and 6, so their reasons are the ones reported. |
| Snapshot | `resetTypes`, the power actions the system offered; `systemID`; and `resetTypesObservedAt`, when discovery read them. |
| Replacement | The kind sets `Supersedes`, so a newer pending provider operation for the same allocation replaces an older one. It never replaces another kind's operation or the operation that settles a hold. Under a hold, newer operations wait in `Pending`. Only an operation the provider row admits replaces anything, so staff requests, and a staff request naming a customer's allocation, replace nothing. |
| Holds | A power success settles only a power hold, and only after a `DeviceRecovery` with `Retry`. Power is `Retryable`. |
| Settle | The Device's discovery must have read the BMC after the held operation ended, from its current endpoint, and read the recorded system `On` or `Off`. The recovery must cite that state in `evidence.data.powerState`, with an `observedAt` after the operation ended. Otherwise the recovery ends `Failed` with `PowerEvidenceNotCurrent`, `PowerEvidenceWrongEndpoint`, `PowerStateNotSettled` or `PowerEvidenceMismatch`. |
| Evidence | `requestSentAt`, `power.before`, `power.after` and `resetEvidence`, typed in the CRD and decoded strictly, at most 1 KiB, and required for a `Succeeded` result. Observations are in time order. `resetEvidence` needs a sent command, and only a restart or power cycle may carry it. They are recorded in `status.evidence`, where every kind's evidence lives, not at the top level of status as #173's example shows. |
| Roles | The **device power operator** inherits the viewer and creates only `DevicePowerOperation`. It does not read MachineAllocations. The **device power requester** is the Fleet provider for Metal's role. The viewer and coordinator cover the new kind. |
| Discovery | Power reads the `ComputerSystem.Reset` types the Device's observed System offers. This change adds only that field, in the Go type and the CRD. BMC discovery starts recording it, with GETs only, in a separate change. Until then every action is refused with `ResetTypeNotOffered`. |

### The power worker

| Piece | Decision |
| --- | --- |
| Before the command | The framework checks the credential, the pin and the System's identity. The worker then checks the expected system UUID against that System (`SystemUUIDMismatch`) and that the BMC still offers the action (`ResetTypeNotOffered`). It reads the power state once: a transition state refuses with `PowerStateNotSettled`, and a restart or power cycle of a server that is off refuses with `NotPoweredOn`. Each refusal sends nothing and ends `Failed`. |
| Start-by window | Two minutes after admission. A later start with nothing sent ends `Failed` with the shared reason `Stale`. |
| Already in the requested state | `On` of a server that is on and `ForceOff` of one that is off send nothing and succeed, with no `requestSentAt`. |
| The one reset | One journaled step. Its payload records when the command was sent, the power state read before it, and the BMC's `LastResetTime` as the baseline. A response the BMC returns refusing the command is an error. A lost response is not: the worker watches for the effect. |
| Success | `On` and `ForceOff`: the requested state appears within three minutes of the command. A restart or power cycle: the BMC's `LastResetTime` moves past the baseline, or the worker sees the server off, and then the server is on again. `resetEvidence` records which. When the BMC reported no reset time before the command, only seeing the server off proves a restart. |
| Crash or lost response | A resumed worker reads the journaled payload instead of reading the server again, and watches for the effect. It never sends a second reset. |
| Not proven | A reset whose effect does not appear in time, a BMC that refused the command, and a BMC that starts reporting another system after the command all end `OutcomeUnknown`, which places a recovery hold. |
| Registration | A Redfish site registers the worker as `DevicePowerOperation`. The local VM and simulator sites have no BMC and no power worker, so they refuse a power operation as an unsupported kind, with nothing sent. |

The lab BMC records `LastResetTime` and adds faults for a withheld request, a
hidden reset time and an identity swap, as [the Redfish
lab](../operations/redfish-lab.md) describes.

### Gaps that remain

Each needs a change in the foundation or the harness, and none is worked
around here.

- **No end-to-end run can admit a power operation yet.** Both multi-plane runs
  and the synthetic staging write fixture discovery, which reports every
  server off and offers no reset types, so admission refuses every action as
  `ResetTypeNotOffered`, and a `Settle` has no real power state to cite. The
  power phases need real BMC discovery in the Redfish run, with discovery
  recording reset types, or fixture discovery that reports them.

- **A kind check cannot wait.** Rule 7 now runs before rule 5, and a kind
  check can only refuse. A provider power operation whose discovery is not
  current therefore ends `Failed` with `DiscoveryNotCurrent`. #174 says it
  waits in `Pending`, as it did when rule 5 refused it first. Because a
  provider operation is named from the Machine generation, a short discovery
  lapse fails that generation's power change for good: it is not created
  again until the generation changes.
- **The evidence hook does not see the result's phase.** It also validates
  `Failed` and `OutcomeUnknown` evidence, where the server did not change. So
  it cannot require what only a success must prove: reset proof for a restart
  or power cycle, and `power.after` in the requested state. A `Succeeded`
  restart with no reset proof is accepted, and so is a `Succeeded` result
  whose evidence is the empty object `{}`. The worker never reports such a
  success, so this costs only the coordinator's second check.
- **A swap before the command is reported as `DeviceIdentityMismatch`.** The
  framework verifies the System's identity before a kind runs, with the
  shared reason. #173 names this refusal `SystemUUIDMismatch` at the worker.
  Power raises `SystemUUIDMismatch` only for a swap after the framework's
  check.
- **A command the BMC refused ends `OutcomeUnknown`, not `Failed`.** Once a
  step is journaled, the framework ends any refusal as `OutcomeUnknown`.
  #173 asks for `Failed` when the BMC refused and the power state did not
  change. The result is safe, but it places a hold that a recovery must
  settle.

## Tests

Run `task test:envtest`. It runs these tests with the race detector against
a real API server:

| Area | Tests |
| --- | --- |
| Every admission precondition, names, kind checks, bootstrap refusal, staff expiry | `TestAdmissionPreconditions` |
| Every holder transition, pending order, supersede, cancellation on holder change, stale fences, results, deadlines | `TestLifecycle`, `TestImportedDeviceGetsUnverifiedCustodyAndSendsNothing` |
| Holds and settlement through the test-only kind, install and wipe holds, `Retry`, `Retire`, retention | `TestRecovery` |
| Rebuild from each source, each conflict, `Resolve`, custody deleted and restored | `TestRebuild` |
| Rebuild holds unproven and unfinished installations and wipes without admitting them again, settled outcomes, several unproven outcomes, `Resolve` against live allocations, fences above the site's, retirements kept past retention | `TestRebuildKeepsUnprovenOutcomes` |
| Two allocators, allocator against staff, two coordinators, admission against a holder change, two Devices, a lost claim response | `TestConcurrency` |
| A claim and its installation landing between the custody read and the operation list | `TestARefusalNeverUsesCustodyReadBeforeAHolderChange` |
| Admission above the fence a site has seen; a recovery confirmed only by the stopped fence; a held operation never a candidate | `TestFencesFollowTheSiteReport`, `TestAHeldOperationIsNeverACandidate` |
| Replacement only after the permission checks, staff never replacing, naming per row, a kind's reason before the shared identity check, a staff retry waiting for its Retry, evidence checked against its operation and required | `TestRowsAndReplacement` |
| The extension points: the Device the checks see, scoped replacement, snapshot fields, the settle hook's hold fields, declared reasons, `Retryable`, a worker refusal returning a staff holder to `None` | `TestExtensionSeams`, `TestRegistryValidatesDeclaredReasons` |
| A crash between each pair of custody and status writes, with dropped and lost responses | `TestWriteOrderCrashRecovery` |
| CEL rules | `TestCRDRules` |
| Seams, rows, principals, ordering, evidence bounds, capacity for every input | `TestRegistryRejectsIncompleteKindsAndRows`, `TestAddingRowsLeavesExistingRowsAlone`, `TestPrincipals`, `TestCandidatesOrderOldestFirstWithADeterministicTieBreak`, `TestEvidenceIsTypedAndBounded`, `TestCapacityProjection` |
| Creator recording and caller preservation | `TestHandler`, `TestCreatorAgainstAPIServer` |
| CRD contract, roles, and that nothing ships the component or the test-only kinds | `TestCRDContract`, `TestRolesKeepOneWriterAndKeepStaffFromCustomerSecrets`, `TestComponentIsInert` |
| Power: each refusal with custody unchanged, both rows and every other pair, staff racing an allocation, replacement only by the provider row and waiting under a hold, expiry, an early staff retry settling after `Retry`, `Settle` evidence, required evidence | `TestPowerOperations` |
| Power: CEL inputs and evidence pruning, checks, input digest, evidence bounds, registration | `TestPowerCRDRules`, `TestPowerChecks`, `TestPowerInputDigestIsStable`, `TestPowerEvidenceIsTypedAndBounded`, `TestPowerRegistration` |
| Power worker: one reset per action and its proof, sending nothing when already in the requested state, each refusal with nothing sent, a lost response, a request never applied, an unprovable restart, a swap after the command, a resumed worker, two workers for one assignment | `go test ./internal/site/powerwork` |
| The BMC's reset time and the lab's power faults | `TestObserveReadsTheLastResetTime`; the lab adapter self-test |

Increment A has no site, so "nothing was sent" means custody admitted nothing
and the fence did not rise. Increment D proves it with the lab BMC audit.

Both local end-to-end runs include the `custody-crd-rules` phase. It proves
that a Milo project API refuses every write the CRD rules forbid, which
envtest alone cannot show, because Milo ignores admission policies stored in a
project.

Increment B's site tests run with `go test -race ./internal/site/...`:

| Area | Tests |
| --- | --- |
| Stale fences and steps refused, also after a restart; replay observed | `TestStoreRefusesOlderFencesAndSurvivesARestart`, `TestStoreReplaysAJournaledStepAsAnObservation` |
| Two workers on one Device, two Devices in parallel | `TestTwoWorkersOnOneDevice`, `TestTwoDevicesInParallel`, `TestANewerFenceStopsTheOlderWorker` |
| A TLS pin mismatch and an identity mismatch send nothing, against TLS servers | `TestPinMismatchSendsNothing`, `TestIdentityMismatchSendsNothing` |
| Every pre-effect refusal, stale assignments, deadlines, result deletion, payload validation | `TestPreEffectRefusalsSendNothing`, `TestAStaleAssignmentIsRefusedAndStoresNothing`, `TestAWorkerStopsAtItsDeadline`, `TestResultsAreDeletedOnceTheCoordinatorAnswers`, `TestAssignmentValidation` |
| One worker per assignment, final results, shutdown at each stage, an older worker that does not stop, a fence raised between journal and send | `TestARedeliveryWhileTheSameOperationRunsStoresNothing`, `TestACopyWaitingForTheLockReturnsTheStoredResult`, `TestAStoredResultSurvivesARedelivery`, `TestAWorkerStoppedFromOutsideStoresNoResult`, `TestAShutdownWhileWaitingForTheRunLockStoresNothing`, `TestAShutdownDuringTheChecksStoresNothing`, `TestANewerAssignmentThatCannotStopTheOlderWorkerSendsNothing`, `TestALostLockWithJournaledStepsIsUnknown`, `TestAFenceRaisedBetweenJournalAndSendStopsTheStep` |
| Per-Device credentials, the fence report | `TestSecretCredentialsUseThePerDeviceContract`, `TestReportGivesTheCoordinatorSeenAndStoppedFences` |
| Step payloads, the start-by window, identity separate from capabilities, a resumed worker reporting unknown | `TestAStepPayloadIsJournaledAndReadBack`, `TestALateStartIsStaleAndSendsNothing`, `TestIdentityIsSeparateFromInstallCapabilities`, `TestAResumedWorkerThatCannotReachTheBMCReportsUnknown` |
| Install and wipe against a simulated BMC, one power-on per phase after an interruption in either phase, the power-off bound, fenced boot admission, bootstrap refused | `TestAWorkerInterruptedInTheDiskPhaseResumes`, `TestAServerThatNeverPowersOffEndsTheStep`, `TestInstallationRunsBothPhasesWithOneOnEach`, `TestWipeChecksTheEnrolledDisk`, `TestAnInterruptedWorkerResumesWithoutASecondOn`, `TestOnlyTheCurrentFenceHasAnOpenBootAdmission`, `TestBootstrapIsRefusedAtTheSiteToo` |

Increment C's tests run with `task test:envtest`, and the smoke runs with
`task standalone:test`:

| Area | Tests |
| --- | --- |
| Allocation, a losing contender, a retried allocation, installation, reinstallation, release with a wipe, and reuse under a higher fence, against a real API server and a simulator site | `TestAllocateInstallReleaseReuse` |
| Six allocators race for one Device; one allocation remains and custody names it | `TestConcurrentAllocatorsOneWinner` |
| An allocation that never held its Device is deleted and the allocator selects again | `TestAnAllocationThatLostItsDeviceIsReplaced` |
| The quota claim is named from the Machine and granted once; release is observed, not assumed | `TestTheQuotaClaimIsNamedFromTheMachineAndGrantedOnce`, `TestReleaseIsObservedNotAssumed` |
| Federation: an assignment's round trip, only this cell's observation accepted, a stale fence report ignored, a cell runs only its own assignments | `TestAssignmentRoundTrip`, `TestReflectionIsRejectedUnlessItIsThisCellsObservation`, `TestAStaleFenceReportIsNoReport`, `TestTheCellRunsOnlyItsOwnAssignments` |
| The local VM site: install then wipe, a wipe with a running domain not proven, a resumed boot observed and not repeated, the boot server serving only an open admission at the stored fence | `TestInstallThenWipe`, `TestAWipeWithARunningDomainIsNotProven`, `TestAResumedBootIsObservedNotRepeated`, `TestTheBootServerServesOnlyAnOpenAdmissionAtTheStoredFence` |
| Synchronized DHCP follows the open admission; a worker waits for the reservation and for its withdrawal | `TestSyncedDHCPFollowsTheOpenAdmission` |
| The simulator site's token, enrollment and server bounds | `TestReadToken`, `TestReadEnrollmentRequiresCompleteTargets`, `TestNewServerHasFiniteResourceBounds` |
| The manager's synthetic flags, and the creator webhook's trusted caller | `TestSyntheticFlagsRequireAndScopeStaging`, `TestRemovedSingleProjectFlagsAreRejected`, `TestWebhookRequiresTrustedAPIServerClient` |
| Where the component ships | `TestComponentRoots` |
| The standalone smoke: contention, install, a simulator restart and recovery, reinstall, release with a wipe, reuse, and the role checks that keep one writer per kind | `task standalone:test` |

The multi-plane local VM and Redfish runs (`task multiplane:acceptance` and
`task multiplane:redfish-acceptance`) run the same workers. Their verification
is pending. The acceptance state of each #174 test is in the [coverage
inventory](../../test/acceptance/coverage.md#device-custody-and-operations).
