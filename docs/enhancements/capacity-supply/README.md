---
status: provisional
stage: alpha
---

# Offer ready machines for allocation

[Documentation overview](../../README.md) · [Functionality guide](../../functionality/README.md)

- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
- [Design details](#design-details)
  - [Supply states](#supply-states)
  - [Actions](#actions)
  - [Who may act](#who-may-act)
  - [Where the view is computed and stored](#where-the-view-is-computed-and-stored)
  - [Provider view](#provider-view)
  - [Acceptance tests](#acceptance-tests)
- [Out of scope](#out-of-scope)
- [Open questions](#open-questions)
- [Implementation history](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)

## Summary

A provider operator needs to see, for each site and pool, how many machines can
be allocated now, and why every other machine cannot. This enhancement adds
that view, plus three actions on a machine: offer, withdraw, and quarantine.
Quarantine is a supply-side block on the capacity record. It never holds the
server's custody.

No action can make a machine allocatable. A machine is allocatable only when
evidence says it is prepared, healthy, free, and cleaned. Actions can only
remove a machine from supply or allow evidence to count.

This enhancement is a proposal. It tracks [issue #120][issue]. Where it
mentions behavior that exists, that behavior is in open pull requests
([#200][pr-status] and [#201][pr-enable]), not in a released build. The release
gates, the pinned cleaning policy, and commissioning that the states rely on
come from the open proposals [#183][pr-cleaning] and [#185][pr-commissioning].
Those states are proposed until those land.

## Motivation

Provider operators run owned machines through registration, commissioning,
allocation, cleaning, and recovery. Each step already records its own state, but
no place shows the result. To answer "how many machines can we sell?", an
operator reads capacity records, custody records, and operation history by
hand, and still cannot tell a machine that is waiting for cleaning from one
that needs a person.

### Goals

- Show supply per site, class, and pool, with every unavailable machine counted
  once under one reason.
- Show what check returns each unavailable machine to service.
- Let an operator withdraw a machine from new allocations without touching its
  current customer.
- Let an operator quarantine a machine that cannot be trusted, and return it
  only after named checks pass.
- Keep one writer for every field, and no new database.

### Non-goals

See [Out of scope](#out-of-scope).

## Proposal

Four parts, each using records Fleet already has or proposes:

1. **Supply state.** The capacity projector classifies each registered machine
   into exactly one state with a reason and the checks that remain. It already
   writes the capacity conditions from evidence.
2. **Actions.** Offer and withdraw change the capacity's allocation policy.
   Quarantine adds a block with required checks to the capacity.
3. **Summary.** The projector also writes a small count per site and pool.
4. **Provider view.** Staff read the summary and drill into machines.

Customers never see this view. They keep seeing the class-level availability
that capacity publication already provides.

### User stories

- An operator sees why a machine is quarantined and which checks return it to
  service.
- An operator withdraws a machine for planned work. Its customer keeps running.
- A capacity planner reports available supply and separates it from machines
  that are being cleaned or commissioned.
- An operator quarantines a machine after a failed wipe, and the machine stays
  out of supply after the customer releases it.

## Design details

Names marked provisional may change. Examples use synthetic data.

### Supply states

Each registered machine is in the first state below that matches. Evaluating in
this order counts each machine once.

| State | Meaning | Evidence | Returns to service when |
| --- | --- | --- | --- |
| `Retired` | The Device is `Retired`, or custody is `Retired`. | Inventory lifecycle, custody holder | Never. Register a new Device. |
| `Recovery` | An operation outcome is unknown, or custody is `Unverified`. | Recovery hold, custody state | A recovery decision settles the hold, or an operator resolves custody. |
| `Allocated` | A customer Machine holds the server. | Custody holder `Allocation` | The customer releases it. |
| `Cleaning` | The allocation ended and release gates are open. | Custody holder `Release`, open gates | Every gate has evidence, including a wipe that meets the pinned cleaning policy. |
| `Quarantined` | Staff blocked the capacity from supply. | Quarantine block on the capacity | Every check on the block has evidence. |
| `Withdrawn` | Allocation policy is `Disabled`. | Capacity spec | An operator offers it again. |
| `Commissioning` | The server is not `Prepared` or `Healthy`: never commissioned, commissioning running or failed, plan changed, or evidence expired. A withdrawn machine that was never commissioned shows `Withdrawn`, because that state is evaluated first. | Commissioning result, discovery | A commissioning under the current plan succeeds. |
| `Unknown` | Evidence is missing, such as an unreachable cleaning policy owner. | The capacity's `Available` condition | The evidence returns. |
| `Available` | The capacity's `Available` condition is true. | Capacity conditions | Not applicable. |

An `Allocated` or `Cleaning` machine that an operator also withdrew or
quarantined keeps its current state. The reason lists the pending block or
withdrawal, so the operator sees that the machine will not return to supply on
release.

Every state has a reason code drawn from the existing capacity condition
reasons (for example `NotCommissioned`, `PlanChanged`, `EvidenceExpired`,
`DeviceReplaced`, `RecoveryHold`), plus the open release gates and quarantine
checks. The state never contains customer identity.

### Actions

Offer and withdraw move a machine between `Withdrawn` and the evidence-derived
states. Quarantine keeps a machine out of supply until named checks have
evidence. Quarantine can be recorded at any time, and it never changes custody.

#### Offer

Offer sets the capacity's allocation policy to `Enabled`. Registered capacity
starts `Disabled`, and the [registration rules][registration] already require
an `Active` Device, a pool label, and a resolving cleaning policy before the
update is admitted. Offering does not require commissioning.

Offer changes intent only. The machine is `Available` only after the projector
finds it `Prepared`, `Healthy`, held by nobody, and not quarantined. An offered
machine that was never commissioned shows `Commissioning`.

#### Withdraw

Withdraw sets the allocation policy to `Disabled`. It stops new allocations
only. The current customer's allocation, installation, power control, and
release continue unchanged. When that customer releases the machine, cleaning
runs as usual and the machine then shows `Withdrawn`.

An allocator that selected the capacity but has not yet claimed its server
withdraws that selection, as it does today.

#### Quarantine

Quarantine records that a machine must not be offered until named checks have
evidence. It is proposed. It lives on the capacity record, which already decides
whether a machine is offered, and it never holds custody. Custody keeps its
current holder, its fence, and its admission rules, so quarantine adds no
holder, no transition, and no change to the [device operations
contract][device-ops].

A staff recovery operator records the block on the capacity, with a reason code
and a set of required checks. The capacity operator may not set it. There is no
separate request kind: the block is part of the capacity record, and each part
has one writer.

```yaml
# Capacity spec, written by a staff recovery operator (provisional)
quarantine:
  reason: SuspectedHardwareFault     # provisional reason codes
  setBy: ops-example@provider.example
  setAt: "2026-11-27T10:00:00Z"
  checks: [Recommissioned, IdentityReverified, PersonReview]
  reviewedBy: null                   # a different staff recovery operator, for PersonReview
  removedChecks: []                  # each entry: check, removedBy, reason, approvedBy
```

Re-wiping a machine that is in supply and not being released is out of scope
here. It would need a new admitted wipe in the device operations contract, which
this proposal does not change. `WipeVerified` is for a machine that is being
released.

The projector reports progress in the capacity status as `openChecks`, the
checks that still lack evidence. Webhook admission stamps `setBy` from the
caller of the write that sets it, and `reviewedBy` from the caller of the write
that sets that field. It refuses a value that differs from the caller, refuses
`reviewedBy` equal to `setBy`, and refuses any change to either once set. A
block with no checks, an unknown check, or a check that cannot be satisfied for
this machine is refused.

Setting or editing a quarantine, like withdraw, does not require the
registration checks that offering requires: an Active Device, a pool label, and
a resolving cleaning policy. A machine that fails registration can still be
quarantined.

Each check is satisfied by an operation or observation that the device
operations contract already admits. Evidence counts only if it is
recorded after `setAt`. The one exception is a release wipe already in progress
when the block is recorded: it counts if it finishes with verified evidence
after `setAt`.

| Check | Satisfied by | Refused at admission when |
| --- | --- | --- |
| `WipeVerified` | The verified wipe of the release that ends the current allocation, including one already in progress, or a later wipe the contract admits, which is a settling wipe for a held operation. The wipe meets the cleaning policy pinned for the server and finishes after `setAt`. | The holder is `None` or `Service`, or the release wipe has already finished, because the contract admits no wipe from those holders and a finished wipe cannot count. A block set while the machine is allocated or being released is accepted. |
| `Recommissioned` | A `DeviceCommissioning` that succeeds under the current plan. It is admitted from holder `None`, after the release finishes. | The Device is Retired, because the contract admits no commissioning there. |
| `IdentityReverified` | Discovery matches the Device's serial, system UUID, and BMC key. | Never. |
| `PersonReview` | A staff recovery operator other than `setBy` records `reviewedBy`. | Never. |

The projector marks a check closed from that evidence and keeps the machine
`Quarantined` while any check is open. A machine with all checks closed is no
longer blocked by the quarantine, and its state follows from the rest of the
evidence. The allocator reads the open checks: a block with none open does not
block supply. Removing the finished block is housekeeping and changes no
allocation outcome.

A check can become impossible to meet, for example when the Device is later
retired. The person who may clear a quarantine may then remove that check. The
removal is recorded in `removedChecks` with who removed it, why, and the second
person who approved it, under the same rule as `PersonReview`. A block on
capacity whose Device is Retired or removed ends with the capacity record,
because capacity of a Retired Device is removed ([#201][capacity-retired]).

If the machine is allocated, the customer is undisturbed. The release runs as
normal, including its wipe, and custody finishes at `None` as it does today.
The machine stays out of supply after release because the capacity still
carries the block. The release wipe closes `WipeVerified` when it finishes
with verified evidence after the block is recorded, so a wipe already running
at that moment counts. Commissioning, wipe, and recovery then run under the
admission rules the contract already has: staff commission from `None`, and
recovery settles any hold. If the release wipe finished before the block was
recorded, or the holder is `None` or `Service`, `WipeVerified` is refused at
admission, and the block uses the other checks.

#### Safety rules

- No action writes a capacity condition. `Prepared`, `Healthy`, and `Available`
  stay derived from evidence by the projector, and the allocator evaluates the
  evidence itself.
- Offering never skips commissioning. Clearing a quarantine never skips a
  required wipe or release gate.
- Quarantine never writes custody, never adds a holder, and never admits an
  operation the contract refuses.
- A finished quarantine returns a server to the state its evidence supports. It
  can still be `Cleaning`, `Commissioning`, or `Withdrawn`.
- An action that cannot be recorded leaves the server out of supply. An
  unknown outcome is never treated as ready.

### Who may act

Each action maps to one permission, so IAM can grant them separately. Role names
are provisional.

| Action | Record changed | Role | Notes |
| --- | --- | --- | --- |
| Offer | Capacity allocation policy | Capacity operator | Existing update permission on capacity. Subject to the registration checks. |
| Withdraw | Capacity allocation policy | Capacity operator | Same permission. Always allowed. |
| Quarantine | Capacity quarantine block (spec) | Staff recovery operator | Separate permission on the capacity's quarantine part, so IAM can grant it without offer. The capacity operator does not hold it unless IAM grants it. A server that has a customer or a recovery hold is still accepted. Registration checks do not apply. |
| Review a quarantine | Capacity quarantine `reviewedBy` | Staff recovery operator other than `setBy` | Satisfies `PersonReview`. |
| Close a quarantine check | Capacity status `openChecks` | Projector, from evidence | Staff cannot write it. |
| Remove a finished quarantine | Capacity quarantine block (spec) | Same permission as quarantine | Allowed once `openChecks` is empty. Removing a check that can no longer be met needs a second person. |
| Read the supply view | None | Staff roles that read capacity | Customer identity needs the separate role that reads MachineAllocations. |

Withdraw and quarantine must always be possible, even for a person who cannot
offer. Reducing supply is the safe direction.

### Where the view is computed and stored

The capacity projector already computes capacity conditions from evidence and
is the only writer of registered capacity status. Custody is not written by
anything in this proposal. This proposal extends it
rather than adding a second computing component.

| Surface | Kind | Writer | Readers |
| --- | --- | --- | --- |
| Allocation policy, pool label | Capacity spec | Capacity operator | Projector, allocator |
| Quarantine block | Capacity spec, quarantine part | Staff recovery operator | Projector, allocator |
| Per-machine supply state, reason, `openChecks` | Capacity status | Projector | Staff, summary |
| Per-site and pool counts | `CapacitySummary` status (provisional) | Projector | Staff |

`CapacitySummary` is a new kind in the provider project, one object per site
and pool. It has no spec that anyone edits. The projector rebuilds each object
from the current capacity records on every pass instead of adding and
subtracting, so a missed event cannot cause lasting drift. No count is stored
anywhere else, and no SQL or other database is involved.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: CapacitySummary
metadata:
  name: site-example-a-pool-standard
  namespace: default
status:
  site: site-example-a
  pool: standard
  observedAt: "2026-11-27T10:05:00Z"
  total: 40
  counts:
    Available: 22
    Allocated: 11
    Cleaning: 2
    Recovery: 1
    Commissioning: 2
    Quarantined: 1
    Withdrawn: 1
    Retired: 0
    Unknown: 0
  reasons:
    Cleaning: {ReleaseGateOpen: 2}
    Commissioning: {NotCommissioned: 1, EvidenceExpired: 1}
```

Rules for the summary:

- The allocator never reads it. A stale or edited summary cannot affect which
  machine a customer receives.
- Counts add up to `total`. A machine whose evidence cannot be read counts as
  `Unknown`, not `Available`.
- A summary older than a stated freshness limit is shown as stale in the view.
  Stale numbers are not shown as current.
- The projector writes status only. A webhook admits status writes only from the
  projector's identity, as it does for capacity status today.

Classes overlap. A MachineClass selects capacity by label, so one machine can
match several classes. The summary is keyed by pool, which is a single label on
the capacity, so pools never double count. A class view lists the pools its
selector matches and does not sum across overlapping classes without warning.
This preserves the rule that overlapping classes must not duplicate supply.

Site comes from the Device the capacity names.

### Provider view

The provider portal and CLI show:

1. A table of site and pool rows with one column per state.
2. For each row, the machines in each unavailable state with reason, who or
   what is blocking, how long, and the checks that remain.
3. For a quarantined machine, the reason, who set it, and `openChecks`.

The view reads the summary for counts and lists capacity records for machines.
It does not read customer records.

### Acceptance tests

These tests are required and not implemented:

- A machine in each state is counted once, and the counts equal the machine
  list.
- Withdrawing an allocated machine leaves the customer running. After release,
  the machine is cleaned and shows `Withdrawn`.
- Offering a never-commissioned machine shows `Commissioning`, not `Available`.
- A hand-edited summary or capacity status does not change allocation.
- Quarantining an allocated machine leaves the customer running and custody
  unchanged. After release, custody finishes at `None`, and the machine stays
  out of supply while any check is open.
- A quarantine with `WipeVerified` set during an allocation closes that check
  from the release wipe and from no earlier evidence.
- A quarantine with `WipeVerified` set while the release wipe is in progress
  closes that check when the wipe finishes with verified evidence. The same
  request is refused after the wipe has finished or when the holder is `None`
  or `Service`.
- A quarantine requiring `Recommissioned` on a Retired Device is refused.
- A check that can no longer be met is removed only with who and why recorded
  and a second person, and a block on capacity of a Retired Device ends with the
  capacity record.
- A machine that fails offer registration can still be quarantined.
- A block with no open checks does not block the allocator before staff remove
  it.
- A quarantine with `Recommissioned` set on a machine in `None` closes only
  after a commissioning admitted from `None` succeeds under the current plan.
- A quarantine requiring `WipeVerified` on a machine in `None` with no hold is
  refused, because the contract admits no wipe there.
- Every check can be satisfied without a custody holder change or an operation
  the device operations contract refuses.
- A quarantine with no checks, or with an unknown check, is refused.
- A caller cannot set `setBy` or `reviewedBy` to another identity, set
  `reviewedBy` equal to `setBy`, or change either once set.
- The requester of a quarantine cannot satisfy its `PersonReview` check.
- An unreachable cleaning policy owner makes machines `Unknown`, not `Available`.
- Deleting and rebuilding a summary reproduces the same counts.
- Two classes selecting one pool never count a machine twice.

## Out of scope

- Purchasing machines and ordering hardware.
- Demand forecasting and capacity planning models.
- Rented-provider integrations and procurement policy.
- Power and feed planning ([#117][power-issue]).
- Customer-facing availability. Capacity publication owns it.
- Changing how commissioning, cleaning, or recovery decide success.

## Open questions

These need a person's decision.

1. **Who may clear quarantine?** Is the projector closing evidence checks
   automatically enough, or must a person always confirm? Does the
   `PersonReview` check need a second person, as commissioning approval may?
2. **Does evidence alone end a quarantine?** Quarantine lives on the capacity
   record, not in custody. The remaining question is whether a block with every
   check closed stops blocking by itself, as drafted (the allocator reads open
   checks, and removal is housekeeping), or stays until staff remove it. Staff
   removal gives a person the last say but lets a stale block hide supply.
3. **Withdrawn versus never offered.** Should the view separate machines that
   were withdrawn from machines never offered? The policy value cannot tell
   them apart without recording history.
4. **Who may quarantine?** Drafted as the staff recovery operator only. Should
   the capacity operator role also be able to?
5. **Summary freshness limit.** What age makes counts stale in the view?
6. **Class view.** Should the view list classes at all, given overlap, or only
   pools?
7. **Quarantine reasons.** Is a fixed reason list enough, or do operators need
   free text? Free text may carry sensitive detail.

## Implementation history

- 2026-11-27: Proposed.

## Drawbacks

- The projector gains another output and must stay correct as states are added.
- Quarantine adds a field to the capacity spec, which the allocator and
  projector must both honor.
- A pool-keyed summary cannot answer class questions exactly where classes
  overlap.

## Alternatives

- **Compute counts in the portal.** Each client would repeat the rules and could
  disagree. Automation could not read the result.
- **A separate component for the summary.** It would need its own credentials and
  would duplicate the projector's evidence reads.
- **Store counts in a database.** The design already avoids one, and a database
  adds a second source of truth that can drift.
- **Quarantine as a custody hold.** A hold would need a new holder or a move
  out of `Release`, and the device operations contract admits commissioning
  only from `None`. That would change a contract this proposal does not own.
- **Let an offer set `Available`.** A person could then make an unprepared
  machine allocatable.

[issue]: https://github.com/datum-cloud/fleet/issues/120
[pr-status]: https://github.com/datum-cloud/fleet/pull/200
[pr-enable]: https://github.com/datum-cloud/fleet/pull/201
[pr-cleaning]: https://github.com/datum-cloud/fleet/pull/183
[pr-commissioning]: https://github.com/datum-cloud/fleet/pull/185
[power-issue]: https://github.com/datum-cloud/fleet/issues/117
[registration]: https://github.com/datum-cloud/fleet/pull/201
[device-ops]: ../device-operations/README.md
[capacity-retired]: https://github.com/datum-cloud/fleet/pull/201
