# Device custody and operations

[Documentation overview](../README.md) · [Implementation status](README.md)

This page records how Fleet implements the [device custody and operations
proposal](https://github.com/datum-cloud/fleet/pull/174) (#174, pinned at
`3907eec`). The proposal's API names are provisional. Each piece below is
marked **implemented and verified**, **implemented and inert**, or
**proposed**.

## Status

| Increment | What it covers | Status |
| --- | --- | --- |
| A: control-plane foundation | API types and CRDs for `DeviceCustody`, `DeviceInstallation`, `DeviceWipe` and `DeviceRecovery`; CEL rules; IAM resources and roles; the creator webhook; the kind registry and admission rule table; the coordinator's custody reconciler, admission, results, recovery, rebuild and retention; the capacity projector | **Implemented and inert.** Verified with unit tests and with tests against a real API server. No deployment root, manager, or staging overlay includes it. The existing Secret-backed lifecycle is unchanged. |
| B: site execution | Per-Device fence store and journal, older-worker stop, deadlines, fenced results, per-Device credentials, pinned TLS, the new handoff payload | **Proposed** |
| C: cutover | MachineAllocations and quota claims named from the Machine UID, allocation and release on custody, the projector as the only condition writer, removal of the private Secret records | **Proposed.** Blocked on the decision about where an allocation's private facts live. |
| D: end-to-end failure scenarios | Harness phases and evidence checks for the #174 acceptance tests | **Proposed** |

## What Increment A adds

- **Kinds.** `DeviceCustody` and three operation kinds in
  `fleet.datumapis.com`, served only in provider project control planes. The
  component that registers them, with their ProtectedResources, roles and
  webhook, is not included by any root. A test fails if one includes it.
- **Custody.** One `DeviceCustody` per Device, with the same name. Its fields
  are in `spec`, like a Lease, and it has no status. The finalizer
  `fleet.datumapis.com/custody` stays until the holder is `Retired`. CEL keeps
  the fence from decreasing, keeps `Retired` terminal, and requires the active
  operation to carry the current fence.
- **Operations.** Every kind starts with one envelope: `creator`, `deviceRef`
  (name and UID) and `authority` (exactly one of `allocationRef` and
  `serviceRequest`). The spec is immutable after create. Status phases move
  forward only, and an ended phase is final.
- **The coordinator** (`internal/custody`) is the only writer of custody and of
  operation status. Every custody change is one update that carries the
  resourceVersion just read. On a conflict it reads again and decides again.
- **The creator webhook** (`internal/custody/creatorwebhook`) admits a create
  only when `spec.creator` is the authenticated caller's username and UID, and
  an update only when the creator is unchanged.
- **The capacity projector** is a pure function that derives `Prepared`,
  `Healthy` and `Available`. Nothing calls it outside tests.

Creating or importing a Device creates `Unverified` custody. That sends
nothing to a BMC and makes no capacity `Available`.

## Implementation decisions

Each decision lists the open point from the implementation brief it settles.

### Who created an operation (O14)

- The client sets `spec.creator` to its own username and UID. The webhook
  rejects any other value and any change. That is how #174 at `3907eec` reads,
  so "clients cannot set the creator" becomes "clients can set only
  themselves". CEL also makes the creator immutable, as part of the spec.
- Milo's root API server calls the webhook, because Milo v0.32.5 does not
  evaluate admission policies stored in a project. The webhook reads the
  original caller from the admission request, with the project in Milo's
  extras, and refuses a caller outside the configured provider projects. It
  reads nothing else and keeps no state.
- The webhook will be served by the existing `fleet-registration` server on
  its own path, `/validate-operation-creator`, when Increment C wires it. That
  server already has the API-server client CA and the Milo root route, and the
  new handler needs no project reads. In Increment A the handler exists and is
  tested, and the server does not serve it yet.
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
  another operation runs, while custody is `Unverified`, or while discovery is
  not current. All three can clear on their own. Staff rows never queue: those
  conditions end a staff operation `Failed`.
- **Phases for refusals (O5).** A refusal ends `Failed` with a reason, and
  nothing is sent. Expired, superseded and no-longer-matching operations end
  `Cancelled`. A `Pending` operation whose holder no longer matches is
  `Cancelled`. One that never matched is `Failed`. The shared reasons are
  `Unverified`, `NoMatchingRule`, `HolderMismatch`, `OperationActive`,
  `IdentityMismatch`, `IdentityNotCurrent`, `SharedPublicKey`, `RecoveryHold`,
  `NameMismatch`, `Expired`, `Superseded`, `Withdrawn`, `DeadlineExceeded` and
  `SiteNotConfirmed`. Kinds add their own.
- **Current identity (O9).** Identity is current when BMC discovery reports
  `IdentityMatched` true for the Device's current generation, its last complete
  inventory read is at most two hours old (twice the default discovery
  interval), and it recorded the BMC key at the current endpoint. Admission
  check 6 compares the observed key digest across Devices that are not
  retired. The snapshot carries the observed digest, and the site worker must
  check it against the operator-approved pin before sending anything
  (Increment B).
- **Checks 5 and 6 apply only to kinds that can reach the server.** The Device
  UID must always match. A `DeviceRecovery` sends nothing, and it must still
  work when a BMC is unreachable, for example to retire a dead server. This is
  listed under [Points for a person](#points-for-a-person).
- **Names.** Deterministic kinds must use their rule, or they end `Failed`
  with `NameMismatch`. A `DeviceInstallation` is named from its allocation and
  a digest of the Installation UID. A `DeviceWipe` is named from its allocation
  and step. Staff kinds must use a generated name.
- **Order.** Pending operations are admitted oldest first. Ties break on kind,
  then name, then UID, so every coordinator picks the same one.
- **Racing refusals.** Delivery must wait until operation status shows
  `Admitted`. If a racing coordinator ends an operation, or it is deleted,
  after custody admitted it but before the status copy, custody withdraws it
  (`Cancelled`, `Withdrawn`). Nothing was delivered, and the fence stays
  raised.

### Deadlines and clock skew (O4)

| Kind | Deadline |
| --- | --- |
| `DeviceInstallation` | 40 minutes, the Redfish executor's install bound |
| `DeviceWipe` | 40 minutes, the Redfish executor's cleanup bound |
| `DeviceRecovery` | 10 minutes to collect the sites' fence reports |

The worker stops at the deadline exactly (Increment B). The coordinator
declares `OutcomeUnknown` only after the deadline plus a two-minute skew
margin. A staff operation that is not admitted within five minutes expires.
Each value is configurable.

### Unknown outcomes and recovery (O2, O3, O6, O7)

- `OutcomeUnknown` moves the operation from `activeOperation` to
  `recoveryHold` in the same update, keeping its kind, name, UID and fence.
  The holder stays.
- A result whose evidence does not match the kind's schema proves nothing, and
  ends `OutcomeUnknown` with `InvalidEvidence`.
- A settling operation records the hold it settles. If it succeeds, the hold
  is cleared. If it also ends unknown, its hold replaces the old one.
- A `DeviceRecovery` is admitted like any operation and raises the fence. The
  coordinator applies it only after every site serving the Device reports a
  stored fence at least that high, so the held worker has stopped. If no site
  confirms before its deadline, it ends `Failed` with `SiteNotConfirmed`, and
  the hold stays. It does not end `OutcomeUnknown`, because it cannot change
  the server.
- `Settle` clears a hold only when the held kind provides a settle check, and
  that check accepts the evidence. Installation and wipe holds have no such
  check: later operations settle them.
- `Retry` marks the hold so that the held operation's requester can create one
  settling operation. Admitting that operation uses up the retry.
- `Retire` needs a hold and a `Service` or `Release` holder. Retiring from
  `Release` requires a disk disposal record.
- `Resolve` is the only operation `Unverified` custody admits. It records the
  holder (`None`, `Allocation`, `Release` or `Retired`), marks custody
  `Verified`, and adopts the current Device UID. A rebuilt hold stays, to be
  settled separately.
- Nothing retries an operation automatically.

### Rebuilding custody (O13, O15, O16)

- Missing custody is created `Unverified` from the Device's lifecycle and
  succeeded `Retire` recoveries, its MachineAllocations that are not
  `Released`, and its operations that have not ended. A releasing allocation
  rebuilds `Release` with every gate open.
- Every operation that has not ended is held, even if its status says
  `Pending`, because custody is written before status.
- Two live allocations, two unfinished operations, holder sources that
  disagree, a Device UID that differs from custody, or a missing Device each
  keep custody `Unverified` until a `Resolve`.
- Custody becomes `Verified` only after every site configured to serve the
  Device reports its stored fence. The new fence is above every fence those
  sites and the operations have used. A Device no site serves stays
  `Unverified`. Sites are configured explicitly and never inferred from
  placement.
- The site's fence store must be durable, beside the journal on its retained
  volume, so a site refuses older fences after a restart (Increment B).

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
- Bootstrap stays refused. A `DeviceInstallation` that names a bootstrap
  reference ends `Failed` with `BootstrapUnsupported`. No role can read a
  Secret, a sealed store, or a bootstrap payload, and a test checks the role
  definitions.

### Other decisions

- **Retention.** Finished operations are deleted 30 days after they end,
  unless custody still names them as active or as the hold.
- **Rejected results.** A result whose fence or operation does not match the
  active operation is refused, and custody counts it and records the last one
  for monitoring. It never changes the holder. A retry of a result already
  recorded is accepted without a second write.
- **Boot interface (O12).** `bootInterface` resolves by name against the
  Device's interfaces, and the snapshot records its MAC. Target disks stay a
  site-local enrollment fact that the worker checks (Increment B).
- **Cleaning policy (O11).** `DeviceWipe` requires the digest of the cleaning
  policy record pinned at allocation. The private fixture resolver that pins it
  arrives in Increment C.
- **Capacity (O1).** The projector stays a pure function. `Prepared` needs a
  succeeded commissioning under the current plan, `Healthy` needs current
  commissioning and discovery evidence, and `Available` also needs `Verified`
  custody, holder `None`, no hold and an enabled allocation policy. Wiring it
  as written would make every server unallocatable, because commissioning is
  not built.

## Points for a person

These need a decision before the increment that depends on them.

- **O1, before C: the interim source of `Prepared` and `Healthy`.** Until it
  is decided, the projector stays unwired.
- **O8, before C: where an allocation's private facts live.** The accepted
  requirements, hardware snapshot, network record and inputs, authority and
  external hold, and binding have no home once the private Secrets are
  deleted. The proposal is immutable MachineAllocation spec fields written at
  create, with no consumer secrets. Increment C cannot start until this is
  approved.
- **O10: the sealed store for bootstrap payloads.** Bootstrap stays refused in
  every increment until a store is chosen.
- **Admission checks 5 and 6 for `DeviceRecovery`.** This implementation skips
  the discovery half of check 5, and check 6, for kinds that cannot reach the
  server.
- **A `DeviceRecovery` that no site confirms** ends `Failed`, not
  `OutcomeUnknown`.
- **A second person for `DeviceRecovery`.** Not required yet.

## Adding an operation kind

A new kind, such as `DevicePowerOperation` or `DeviceCommissioning`, plugs into
four seams. It needs no change to the coordinator. The test-only kinds in
`internal/custody/testkinds_test.go` use every seam. Copy them.

1. **Register the kind.** Add a `custody.Kind` to the registry with:
   - its GroupVersionKind, constructors, and an `Items` adapter;
   - a digest of its exact inputs (`custody.SpecDigest` covers the whole
     spec);
   - its naming rule: `DeterministicName` with a `Name` function for provider
     and release kinds, or `GeneratedName` for staff kinds;
   - its `Deadline`, and `Effectful` if it can reach the server;
   - `Supersedes`, if a newer pending operation for the same authority should
     cancel an older one, as power needs;
   - `Check` for kind-specific admission checks. They run after the shared
     preconditions, before the custody update, and return a named refusal,
     such as power's `SystemUUIDMismatch` or `DiscoveryNotCurrent`;
   - `Settles`, the held kinds a success settles, with `RequiresRetry` when a
     `DeviceRecovery` `Retry` must come first.

   Embed `OperationEnvelope` inline in the spec, and use `OperationStatus` as
   the status. Add the CRD with the same envelope, the immutable-spec rule and
   the phase rule, plus a ProtectedResource and roles.
2. **Add its rows.** Add one `custody.Rule` per row of the #174 operations
   table: the creator class, the authority type, the holders it admits from,
   its hold behavior, and whether it queues. Staff rows never queue. To admit
   a new service principal, add its class to the coordinator's principal
   configuration. Never hard-code an identity.
3. **Implement its site worker** (Increment B). The site framework owns the
   fence, the journal, credential lookup, the TLS pin and identity checks, the
   deadline and result delivery. A kind supplies only its pre-effect checks,
   which refuse with a named reason and send nothing, its journaled steps, how
   it recovers by observation after a lost response, and its typed result
   evidence. The worker contract is proposed and lands in Increment B.
4. **Define its evidence.** Set `Evidence` to `custody.StrictEvidence` over a
   typed, bounded Go struct. Unknown fields, oversized evidence and trailing
   data are rejected, and the result then proves nothing. Evidence never
   carries a credential or a raw BMC response. If a `DeviceRecovery` `Settle`
   can settle a hold of the kind, set `RecoverySettle` to validate the staff
   evidence. Power, for example, accepts only a power state that discovery
   read after the operation ended.

Test the kind with the shared harness in `internal/custody`: register it
beside the defaults, and run its refusals, admission, results and holds
against a real API server.

## Tests

Run `task test:envtest`. It runs these tests with the race detector against
a real API server:

| Area | Tests |
| --- | --- |
| Every admission precondition, names, kind checks, bootstrap refusal, staff expiry | `TestAdmissionPreconditions` |
| Every holder transition, pending order, supersede, cancellation on holder change, stale fences, results, deadlines | `TestLifecycle`, `TestImportedDeviceGetsUnverifiedCustodyAndSendsNothing` |
| Holds and settlement through the test-only kind, install and wipe holds, `Retry`, `Retire`, retention | `TestRecovery` |
| Rebuild from each source, each conflict, `Resolve`, custody deleted and restored | `TestRebuild` |
| Two allocators, allocator against staff, two coordinators, admission against a holder change, two Devices, a lost claim response | `TestConcurrency` |
| A crash between each pair of custody and status writes, with dropped and lost responses | `TestWriteOrderCrashRecovery` |
| CEL rules | `TestCRDRules` |
| Seams, rows, principals, ordering, evidence bounds, capacity for every input | `TestRegistryRejectsIncompleteKindsAndRows`, `TestAddingRowsLeavesExistingRowsAlone`, `TestPrincipals`, `TestCandidatesOrderOldestFirstWithADeterministicTieBreak`, `TestEvidenceIsTypedAndBounded`, `TestCapacityProjection` |
| Creator recording and caller preservation | `TestHandler`, `TestCreatorAgainstAPIServer` |
| CRD contract, roles, and that nothing ships the component or the test-only kinds | `TestCRDContract`, `TestRolesKeepOneWriterAndKeepStaffFromCustomerSecrets`, `TestComponentIsInert` |

Increment A has no site, so "nothing was sent" means custody admitted nothing
and the fence did not rise. Increment D proves it with the lab BMC audit.
