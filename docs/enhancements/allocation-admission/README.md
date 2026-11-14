---
status: provisional
stage: alpha
latest-milestone: null
---

# Allocation admission and custody

Uses the [Datum enhancement template][template]. This document selects a
**proposed protocol**, not implemented or verified production behavior. API and
record names below describe required contracts, not existing platform endpoints.
There is no compatibility requirement for earlier provisional APIs.

- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
- [Design details](#design-details)
- [Production readiness review questionnaire](#production-readiness-review-questionnaire)
- [Implementation history](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure needed](#infrastructure-needed)

## Summary

Metal owns consumer admission; Fleet is an optional provider implementation.
Use one management-side PostgreSQL admission ledger with SERIALIZABLE transactions,
database-enforced uniqueness, durable authority gates, protected quota escrow,
and transactional outboxes. There is no transaction spanning Kubernetes API
servers, Milo, a cell, or Karmada. External authorities participate by closing
ledger gates **before** changing effective authority, and reopening them only
after publishing a verifiable new revision. Quota reserves durable escrow before
binding; binding atomically consumes the local escrow reference with occupancy,
Machine uniqueness, retained contract, and recovery work.

The selected tradeoff is fail-closed admission during dependency partitions,
rather than allowing stale grants or expiring holds to create unaccounted machines.
Existing workloads are not automatically destroyed by admission unavailability.

## Motivation

Two consumers can acquire overlapping offerings for one physical server. A
controller may lose a quota response, crash after reserving capacity, or commit
after a caller's permission was revoked. Independent API updates, controller
leases, finalizers, and compensating deletes do not define which action won.
We need an auditable commit decision and recoverable custody, not an eventual
approximation of exclusive occupancy.

### Goals

- Order acquisition, authorization invalidation, quota custody, and retirement.
- Preserve exclusive supply and one allocation for an exact Machine lifetime.
- Recover lost responses without duplicate holds, bindings, or dispatch authority.
- Define concrete prerequisite contracts and refuse enablement without them.

### Non-goals

Only standalone Fleet/Metal allocation is in scope. This proposal adds no managed
node, cluster, or service behavior. Operation execution, network handoff, inventory
trust, external fulfillment, release/reuse policy, and deployment remain companion
work. Their narrow interfaces are stated here; their policy is not redefined.
No runtime code, API types, dependencies, or shared documentation indexes change.

## Proposal

### User stories

A customer retries a timed-out Machine creation and finds the same allocation,
not another reservation or quota charge. A provider withdraws an offering and
can identify the last acquisition ordered before withdrawal. An operator recovers
after a coordinator crash without treating missing public objects as free supply.

### Source baseline and precedence

On 2026-11-14 all four source PRs were refreshed through GitHub metadata: each was
open, targeted `main`, and had the exact head below. This proposal starts from
`origin/main` at `8ebec19513a0cbc8f219d3acae46f89ab471510b`, not their branches.
Links pin source content, not moving PR heads; no source PR is merged or copied.

| Source | Exact reviewed head | Contract consumed |
| --- | --- | --- |
| [Fleet #41: secure BMC access][bmc] | `f04e63ddd9d10cbf306daf3a0232961debd85e7f` | Allocation-scoped sessions; current permissions; one effect serialization domain. |
| [Fleet #51: Fleet management][management] | `fa07c2236801a33a58efc914b555586c8e146cc6` | Distinct inventory identities, hub/cell writers, shared A1–A7 acceptance scenarios. |
| [Fleet #58: provider offerings][offerings] | `a922ff1eb3275e26af6ae750a7515ddde245a754` | Canonical offering, one-hop class, acquisition grants, retained runtime capabilities. |
| [Fleet #59: PXE][pxe] and [lifecycle][lifecycle] | `d7ff793b03711203dbfc9fac87d43922cc649b6f` | Exact Installation inputs, quota custody, admission fence, retirement guard. |

The [main design context](../../architecture/design.md),
[publication background](../../proposals/fleet-management/capacity-publication.md),
and [source provenance](../../proposals/fleet-management/MIGRATION.md) explain the
earlier architecture. Where their provisional provider MachineClass definition
differs, this proposal uses #58's MachineOffering and one-hop consumer class.
It resolves the storage and admission-ordering choices deliberately left open in
the pinned lifecycle; it does not claim those choices already exist in Milo.

### Notes, constraints, and caveats

Public names are authored references. Private exact identities include API
authority, Project UID, resource type, name, and incarnation evidence; namespace
UID is included only for namespace-scoped inputs. A project name or a UID from
another API authority is not interchangeable. All examples are conceptual and
contain no credentials. Secrets and rendered secret-bearing plans stay in protected
custody outside Git and outside consumer status.

### Risks and mitigations

A shared serialization domain limits availability and throughput. Narrow indexed
transactions, bounded admission queues, and no network calls inside transactions
limit contention; do not shard away global uniqueness to improve availability.
An authority that bypasses gate closure invalidates the safety argument. Require
owner qualification and deny production enablement rather than accepting a watch
as a substitute. Stalled escrow consumes quota intentionally until disposition is
proven. Expose age and blockers, not an unsafe automatic expiration.

## Design details

### Identity and ownership

Device identifies a physical asset; MachineCapacity identifies supply; Machine
identifies consumer intent; MachineAllocation is its exclusive accepted binding;
Installation identifies an OS incarnation. None is an alias for another.
An offering selects eligible supply but does not own a separate capacity pool.

Inventory's proposed trusted enrollment interface supplies a globally unique
canonical asset key and an exact Device/MachineCapacity association. The ledger
enforces one active enrollment for that asset key across provider projects.
Conflicting enrollment is quarantined, not independently allocatable. Recognizing
that two physical records are the same asset remains inventory trust's job; SQL
uniqueness cannot discover a forged hardware identity. Adoption imports occupied
or unknown claims before publication, grants no effects, and never defaults to free.

The first implementation uses one logical ledger for all admitted providers and
consumers in its allocation domain. Independent domains may not advertise the
same asset or accept the same Machine lifetime; cross-domain moves require an
explicit fenced custody transfer and are disabled in the initial protocol.
Non-Fleet providers need not create Fleet Device or MachineCapacity resources.
They must supply equivalent exact supply identity and exclusive custody through
the external-fulfillment contract; that path remains disabled until qualified.

### Durable records and sole writers

Provider-owned records are stored in a service-controlled ledger outside the
lifetime of either project's public API. Logical ownership does not permit an
ordinary provider administrator to edit custody. No cascading owner reference
can delete an unsettled record.

| Record | Retained fields and constraints | Sole transition authority |
| --- | --- | --- |
| AuthorityGate | dependency key, authority incarnation, monotonic epoch, Open/Closed/Retired, effective revision, mutation ID, recovery epoch | Authenticated adapter for that dependency; ledger validates its scope. |
| ProviderGuard | provider Project UID, Open/Closing/Retired, outstanding obligations and custody manifest | Retirement coordinator through ledger transactions. |
| AdmissionAttempt | server ID, exact Machine key, request digest, original caller/delegation evidence, dependency vector, resolved source identities and digests, attempt sequence, state | Metal admission coordinator. |
| QuotaCustody | globally unique hold key, consumer Project UID, provider guard, one machine-count unit, quota anchor/claim incarnation, grant receipt, disposition and acknowledgment | Quota adapter; allocation transaction alone changes local ownership to Bound. |
| Enrollment and Occupancy | canonical asset key, exact Device and optional supply keys, eligibility revision when supply exists, tagged Allocation/Preparation/AdoptedCustody/PhysicalMaintenance or unknown domain custody, reservation epoch | Inventory adapter writes enrollment evidence; shared allocation/custody coordinator alone acquires occupancy; release coordinator clears it only with settlement proof. |
| MachineAllocation | immutable allocation ID, exact Machine/supply/asset, selected cell incarnation, accepted offering/profile content, acquisition evidence, hold, release authority, lifecycle state | Admission coordinator creates; lifecycle coordinator performs guarded transitions. |
| MachineBinding | unique exact Machine key, allocation ID, terminal tombstone | Allocator inserts once; never retargets. |
| RecoveryOutbox | unique kind/record/revision, immutable payload digest and target, delivery state, retry metadata | Transaction creating work inserts; publisher only updates transport acknowledgment. |
| AdmissionDecision | commit/abort identity, attempt, relevant gate epochs, hold disposition, ledger recovery epoch, transaction sequence | Ledger transaction; append-only. |

An operation owner uses the ledger-local atomic hook against exactly one tagged
subject: `Allocation`, `Preparation`, `AdoptedCustody`, or `PhysicalMaintenance`. Allocation retains its
existing binding and Installation predecessor rules; the other subjects use the
asset-custody contract below, not a synthetic Machine, quota hold or Installation.
The operation owner owns operation schema, effect conflicts and executor fences;
it cannot maintain a second independent asset lock.

Use unique constraints on the canonical asset's enrollment, occupied supply,
occupied asset, exact MachineBinding, allocation ID, hold key, and outbox identity.
Occupied includes Bound, Preparation, AdoptedOccupied, PhysicalMaintenance, Releasing, Quarantined,
and unknown execution/domain custody. Keep the
MachineBinding tombstone after release: the same Machine incarnation cannot obtain
a replacement allocation. A new Machine incarnation requires a new request and
allocation. Physical reuse retains history but can acquire new occupancy only
after the release owner proves settlement. A reused name is never a retry key.

### Storage and transaction boundaries

Select PostgreSQL SERIALIZABLE isolation plus explicit row locks, unique indexes,
and compare-and-swap transitions. Require durable WAL and synchronous replicated
commit with qualified no-acknowledged-write-loss failover. Only the service can
write these tables; constraints remain active for every writer and recovery tool.
This is a proposed backend requirement, not a claim about current deployment.

Every transaction uses this total class order, sorting exact keys within each
class and skipping only inapplicable classes:

1. Complete authority gates (including coarse inventory/identity-domain gates).
2. ProviderGuard rows.
3. AdmissionAttempt, then its ProviderReservation children, if applicable.
4. QuotaCustody, then MachineBinding keys, if applicable.
5. Enrollment, then Occupancy keys, including unresolved adopted-domain holds.
6. Inventory-domain locks, canonical identity/alias keys, immutable plan keys,
   then physical-resource/reservation keys (rack, termination and power).
7. AssetCustody records, then the persistent asset execution slot.
8. Operation rows, ReleaseCase rows, then obligation/closure/accounting children
   in that class order and sorted ID order within each class.
9. Append-only decisions, input pins and outbox inserts.

Discover the complete key/dependency set before locking; a changed or incomplete
set aborts and restarts from fresh resolution, never acquires an earlier lock
after a later one. Absent-key uniqueness and SERIALIZABLE predicate checks cover
concurrent inserts. All allocation, adoption, physical-plan, maintenance, release,
settlement and recovery writers use this order. No owner may append a conflicting
private suffix order or acquire a second mutex to evade it. Missing gate or
unregistered dependency means denial. A concurrent gate insertion or occupancy
race must yield a uniqueness/serialization failure, never a best-effort result.
Retry the entire transaction with fresh resolution where revisions changed.
Never perform remote authorization, quota, or provider calls under these locks.

The allocation linearization point is the durable commit that jointly inserts
MachineAllocation, MachineBinding, occupancy, consumed local quota ownership,
AdmissionDecision, and recovery outbox entries. All are present or none are.
Database response loss is handled by querying the attempt/decision on the current
writer, not by allocating another ID. Reads from asynchronous replicas are never
used to prove absence, commit, quota release, or retirement completion.

### Non-consumer asset custody and effect admission

This proposed `custody-subjects-v1` alignment closes the commissioning and adopted
settlement joins identified by the complete-set review. It extends this ledger,
not the consumer allocation algorithm or a second reservation service.

`AssetCustody` retains custody UID, kind, provider Project UID, authority domain,
origin decision, exact enrollment UID/epoch and canonical asset when resolved,
Device/supply incarnations, alias/domain exclusion set, occupancy revision,
custody epoch, policy/authority/input digests, guard obligations and disposition.
It survives public inventory/project deletion. The custody coordinator creates
the record and acquires its Occupancy through the same ledger hook; inventory
alone resolves identity and writes trust evidence; execution alone writes the
slot/operations; release alone authorizes occupancy clearance. Custody records
are not editable physical identities or alternative consumer bindings.

| Subject | Admission and exclusion | Settlement destination |
| --- | --- | --- |
| `Preparation(custodyUID)` | Exact trusted enrollment/Device/supply, allocation explicitly Disabled, no allocation/adopted/unknown occupancy, maintenance transition, release hold or unresolved alias/domain conflict. Atomically install Preparation occupancy, guard obligation, slot sequence, operation/input pins and outbox. | Execution L5 settles all effects; release validates custody-clearance obligations; inventory separately publishes fresh eligibility. |
| `AdoptedCustody(custodyUID)` | Durable AdoptedOccupied/Unknown record may initially identify only a conservative physical domain. Observation grants no effects. Trusted exact resolution and a separately approved ownership/cleanup transition are required before any mutation. | Release R1/R2 use this custody identity and real accounting evidence or proved non-applicability; never manufacture an allocation. |
| `Allocation(allocationUID)` | Existing immutable consumer binding, charged escrow, current action/input authority and Installation rules remain unchanged. | Existing allocation release, quota acknowledgment and permanent MachineBinding. |
| `PhysicalMaintenance(custodyUID)` | Trusted registered Device, exact physical transition and current maintenance authority; no conflicting target custody, transition or domain hold. Acquire occupancy and operation atomically with inventory closure as specified below. Supply is optional. | #62 settles field-work authority/effects; #65 clears only this custody after exact physical disposition and accounting proof; #63 independently revalidates inventory. |

There is one persistent execution slot per canonical asset in its enrollment
lineage, not one competing slot per subject. It retains a monotonically increasing
operation sequence across subject changes; subject UID/kind, custody/occupancy
epoch and restore lineage are checked with the slot. Subject records reference
that slot. An unresolved adopted-domain hold blocks every intersecting enrollment,
plan and allocation; exact resolution installs the canonical exclusion before
removing any broader hold. It cannot overwrite an existing allocation or erase
an overlapping case. Retain the union and quarantine until all owners reconcile.

Preparation uses current service-principal action/input authorization, explicit
provider approval of commissioning (including exact destructive disk steps), and
an immutable qualified recipe, component/NIC/BMC mapping, permitted endpoints,
secret versions, selected cell and finite budget/deadline. Required gates cover
provider lifetime, custody ownership, service IAM, input use, enrollment identity,
physical safety, commissioning qualification, networking and recovery lineage.
**Allocation Disabled and boot qualification Pending are expected states**, not
Closed safety gates to bypass. A distinct commissioning capability validates
pre-boot identity/path/safety; it does not require the boot result it will produce.
If that initial trust is unavailable, commissioning cannot run. All required
service gates must be Open; retirement rejects new commissioning. Admission and
renewal validate exact revisions/deadlines under the total order above.

The operation key is `(providerProjectUID, subjectKind, subjectUID, requestKey)`;
same key/digest recovers the original operation, differing input conflicts. The
execution fence binds restore epoch, canonical asset, enrollment epoch, custody
epoch, subject kind/UID, operation sequence and executor generation. #62 applies
its unchanged L1–L5, S1, input-retention, outbox/inbox, broker and no-late-effect
rules to all subjects. Preparation has a prior settled custody receipt (or a
verified first-use quiescence manifest), **not** an Installation predecessor;
initial host/queue/port state still needs proof. A signed preparation request is
not permission for customer networking, a management session or a consumer OS.

#61 binds commissioning paths to that exact Preparation subject, operation/fence,
enrollment, mappings and service Network inputs. Network materialization is an
admitted step, not an L1 prerequisite requiring already-effectful setup. Its
receipt must precede boot. After inspection/disk preparation, stop verifier/RAM
code and queued BMC/storage work, revoke boot/proxy/credential grants, disconnect
and dispose exact commissioning bindings, and settle the preparation's own sends.
Missing evidence retains Preparation occupancy/Unknown, even with a successful
callback. Execution L5 alone does not clear occupancy or publish eligibility.

The release coordinator accepts a preparation clearance certificate over its
complete operation/network/data/physical obligation manifest, current identity,
no-late-effect barriers and accounting disposition. New enrolled, never-consumer
commissioning has no machine-count charge: retain quota-owner policy revision
and ledger origin/decision-range proof of non-applicability, not an invented hold
or Released acknowledgment. Any discovered real charge/conflict requires owner
reconciliation. Release atomically records Settled, tombstones old authority and
clears only that custody's Occupancy; enrollment stays Disabled. Inventory then
validates the settled receipt plus fresh independent boot, placement, network,
power and trust evidence before publishing eligibility at a new revision. Actual
allocation rechecks all gates and absence of every conflicting hold. Cancellation
or partial failure never implies prepared supply; safe settlement may end in
Disabled/quarantine instead. Physical placement reservations are not freed by
this occupancy transition.

Adopted settlement uses #65's explicit authorization, policy and obligation
capture rather than an allocation-retained contract. Its accounting union includes
any real retained holds and external obligations. Proven non-applicability must
identify the accounting authority, policy revision, scope, searched decision
range and provenance; an empty local query is insufficient. Unknown accounting
retains custody. R2 can close an adopted record without a MachineBinding or quota
outbox only with that proof and the release certificate. It clears no overlapping
allocation/domain hold and leaves eligibility Disabled pending fresh preparation.

Backup/restore and retirement enumerate these subjects even when no Machine ever
existed. Preserve origin/approval, resolution history, gates, slot sequence,
authority tombstones, exact inputs, network receipts and accounting provenance.
Reconcile the union with cell/boot/network/physical and accounting histories;
a snapshot lacking a preparation or adopted case cannot prove free supply.
Fence old writers before explicit per-record recovery in the new lineage. Missing
mapping/authority/keys keeps effects disabled; missing target enumeration blocks
the enclosing physical domain. Closing ProviderGuard permits only previously
retained, narrowly reauthorized settlement obligations, never new commissioning
or a new adoption-derived wipe permission. No custody path bypasses retirement.

### Physical maintenance without consumer custody

`physical-maintenance-v1` adds one tag to the existing AssetCustody and operation
union, not a second allocator, executor or public API. It covers an explicitly
requested move, bounded component repair or removal of a trusted registered
Device with no current target custody. A passive panel needs neither
MachineCapacity nor Machine, quota hold, Installation, BMC, bootstrap image or
boot qualification. Enrollment for this path retains the canonical Device/trust
lineage with supply explicitly NotApplicable; it does not create compute supply.
Rack, Cable, termination and PowerFeed resources are exact plan dependencies and
reservation interests, not synthetic Machines. Unresolved discovery uses
AdoptedCustody instead; registration never schedules maintenance.

Retain current provider/custodian approval and authenticated requester,
service-principal and assigned technician action/input decisions, separated
approver/witness roles, exact permitted physical actions and forbidden actions,
disruption window, qualified field procedure, finite step budget and deadlines.
Ownership/maintenance permission is distinct from inventory-plan edit permission.
Retain canonical asset, Device and enrollment incarnations, custody UID/epoch,
transition UID, predecessor and candidate plan digests, component/port identities,
old/destination physical-resource keys and interests, dependency graph revision,
authority vector, selected cell and independent prior-work settlement proof.
Exact procedure/evidence bytes and any secret versions use #62 protected custody.
No image or credential is required when the procedure has neither.

The prior-work proof is the last settled custody/operation receipt, or a qualified
first-use quiescence manifest reconciling custodian/site access and manual-worker
authority, issued permits and independent physical inspection. An empty slot or
missing consumer record is not that manifest; unknown prior field authority blocks
L1 until its owner proves cessation and excludes later use.

Before admission, #63 resolves the complete impact graph and #61 plus each active
custody owner supplies either attributable no-dependency evidence or exact scoped
consent and effective prerequisite fences. A panel's maintenance authority grants
no right to reset, disconnect, drain, rewire live connections to, or erase its
dependent servers/switches. Required dependent actions are separately admitted
under those devices' existing Allocation/AdoptedCustody or other qualified owner
authority, never borrowed from this new subject. Their owner-controlled barriers
must already be effective before maintenance L1 and remain held until explicit
owner revalidation. Unknown/unowned dependencies block L1. Initial scope excludes
cross-provider physical graphs and dependent owners without this interlock.

L1 uses the complete total order above, including all impacted gates, custody
and physical keys. With no remote calls it:

1. Revalidates current Open maintenance/action/input/trust/qualification/recovery
   gates and Open ProviderGuard; separately requires dependent ordinary-effect
   gates to match their owner-issued closed barrier revisions. Those barriers
   are preconditions, not permission to bypass a closed maintenance safety gate.
2. Checks exact target identity, unchanged complete dependency graph, prior-work
   settlement and no target allocation, Preparation, adopted/unknown occupancy,
   release/recovery hold or other nonterminal InventoryTransition. An allocated
   target must use its existing Allocation authority; an adopted target must use
   its explicit adoption transition. Neither may be relabeled maintenance.
3. Atomically creates this custody and occupancy, accepts/reserves the exact
   destination graph while retaining old interests, records this transition and
   ProviderGuard obligations, invalidates dependent evaluations and closes the
   inventory mutation gate, advances the persistent asset slot, and commits the
   operation/input pins/decision/outbox. This is one transaction, not an interval
   in which a free asset is visible between closure and custody acquisition.

The matching transition UID, custody UID, mutation ID and expected post-closure
epoch identify **this** admitted transition; they cannot waive a different
maintenance transition. Predicate/absent-key checks cover a concurrently inserted
hold, alias, dependency or plan. Same request/digest recovers the same decision;
different input conflicts. An inventory mutation token alone has no effect
authority. Renewal under this operation validates its exact intentional closure,
current remaining authority and owner barriers; another closure/revocation denies
renewal. Unexpected drift before L1 permits only separately qualified safety
recovery, not this ordinary maintenance path.

The occupied target and retained old/new physical interests exclude allocation,
commissioning, conflicting plans and maintenance. Observational adoption may
record a newly discovered overlap, but must preserve this occupancy, close safety
gates and retain the union; it cannot replace the winning subject. Release and
retirement close or settle retained work, never steal the slot. A physical plan
winning destination reservation first makes L1 fail without any partial custody
or dispatch. Unchanged reservation interests transfer without double charging.

#62's field-work steps use the same L2/L3/S1 journal and one-use permit boundary.
Cancel, expiry, missing completion or an offline technician never frees custody.
#65 R1/L5/R2 clearance requires no-late-work and inventory-owned physical
disposition plus real accounting settlement or positive policy/provenance-backed
non-applicability. It creates no quota acknowledgment. Active destination
reservations remain after R2; failed work may settle only into Disabled/quarantine,
not verified placement. Inventory alone revalidates new identity/placement/path
evidence; an idle server additionally needs all current compute qualification,
including separately admitted Preparation if invalidated. A passive panel never
acquires compute eligibility. Dependent owners decide their own resumption.

Restore and retirement retain maintenance authority, dependency barriers,
old/destination plans, permit delivery/start/stop inventory, slot and accounting
history even without consumer records. A pre-L1 backup cannot erase a later
technician permit. Reconcile the conservative union with independent field/owner
journals, fence old writers and field access, then establish the new lineage.
Missing scope, bytes, identity or physical cessation evidence blocks replay and
clearance; incomplete target enumeration quarantines the enclosing domain.

### Authority fencing and revocation ordering

This proposal requires a **close-before-change** integration for IAM, delegation,
entitlement, consumer/project lifecycle, applicable namespaces, provider retirement,
offering policy, assignment membership, source-use permissions, publication
provenance, quota revocation, eligibility, and explicit suspension. Coarse gates
(for example one IAM realm gate) are valid initially and safer than incomplete
dependency tracking. An evaluator must include broad gates for policy additions,
denies, group changes, and newly applicable restrictions, not only objects that
contributed a positive grant.

For every effective authority mutation, including deletion and identity recreation:

1. The authority durably records a mutation ID and calls the proposed gate-close
   contract. The ledger locks the gate, increments its epoch, marks Closed, and
   returns a durable closure receipt. Repeated closure with the same mutation ID
   returns the same result. A different mutation queues behind the current one.
2. Only after closure acknowledgment may the owner change its effective source
   state. It retains a versioned decision snapshot and revision/incarnation proof.
   Closure itself blocks all affected new commits; it does not change an accepted
   allocation contract or cancel already admitted operations.
3. The owner supplies that proof to reopen the gate at the new epoch/revision, or
   leaves it permanently Retired. Reopen compares mutation ID and epoch, so a late
   response from an earlier update cannot undo a newer closure. An invalid candidate
   may reopen the previous valid content with a new epoch after authoritative proof;
   a suspension/deletion cannot be silently rolled back this way.
4. The authority reports revocation effective only after the closure receipt and
   its own durable mutation. Public status distinguishes requested from effective.
   If the ledger is unreachable, revocation remains pending and admission cannot
   safely rely on that authority's connectivity; emergency effect fencing is a
   separate operation contract, not a false claim of completed revocation.

Evaluation occurs outside the transaction against exact Open epochs and committed
authority revisions. The authority returns a service-authenticated decision bound
to caller, action, exact inputs, dependency vector, request digest, and validity
deadline. The commit transaction checks equality of every epoch/revision, Open
state, recovery epoch, and deadline with trusted ledger time. Closing a gate and
allocation commit contend on the same row: if closure wins, no older decision can
commit; if allocation wins, it remains recorded before closure. There is no window
where a post-check permission change can be ignored because a cached answer says
allowed. Expiry also fails closed; reopening requires reevaluation, not token reuse.

Authority owners must prevent bypass writes and supply linearizable revisioned
evaluation, including a complete dependency vector. Gate watches only accelerate
reconciliation; they confer no authority. Initial onboarding keeps a broad parent
gate Closed until all source state and writer interlocks are installed. Wall-clock
based permission expiry requires bounded trusted time; uncertain time blocks new
commits. Crash recovery for a closed mutation queries its durable ID and source
revision, never reopens by timeout.

If authorization becomes explicitly denied while an attempt is unbound, mark the
request Rejected and settle its hold; returning access does not resurrect it.
The customer must submit a new request/Machine incarnation. A temporarily Closed
gate or unavailable evaluator yields Unknown and may resume the same exact request
after current reevaluation. Confirmed deletion or incarnation replacement rejects
it rather than silently rebinding the reference.

Authorities retain invalidation history through each live attempt's lifetime.
Reevaluation must report whether the exact caller/request lost required access
since its captured decision, even if a later grant restored it before the worker
observed the denial. Such an attempt is Rejected; a new positive snapshot cannot
erase that history. An unavailable history produces Unknown, not permission.
Unrelated policy changes and temporary closure alone do not imply loss of access.

### Offering resolution and current caller checks

Before quota acquisition, resolve the local MachineClass exactly one hop through
protected publication provenance to its provider MachineOffering. Retain both
incarnations, provider/consumer Project UIDs, offering content/revision/digest,
assignment evaluation revision, local/source profile identities and immutable
requirements, location/configuration compatibility, and prerequisite evidence.
Same-project acquisition still checks the source and caller; it skips only copying.
Names, labels, status, copied content, and a service controller's credentials are
not publication provenance or source-use authority.

Evaluate the union of applicable assignments, intersected with offering policy,
active service entitlement and current caller permissions. Selected profile grants
cannot be bypassed with a customer copy; custom profiles require the offering and
an applicable AllSupported grant. Revoking one overlapping grant need not remove
another, but the combined decision must be reevaluated at the new revision.

Capture authenticated caller and delegation context at ingress, including the
exact public Machine incarnation and immutable intent digest. A protected ingress
receipt and service finalizer are created with accepted public intent; without
that proposed API integration an orphan object cannot progress. Every asynchronous
admission reevaluates **that caller's** current Machine-create/local-profile-use,
source-use, entitlement and referenced-input permissions. Credential/session
expiration is not renewed by a controller; pending work needs valid delegation or
fresh authenticated resubmission. No bearer credential is stored in audit records.
The intent's digest cannot change under an idempotency key. Failed status projection
does not undo admission; public deletion closes its lifetime gate and initiates
the appropriate abort or release path.

Resolve initial installation requirements and network feasibility before quota
and hardware selection. These are read-only prerequisites, not permission to make
network/provider effects. Actual machine-specific network and installation plans
are admitted later by their owners. Allocation retains accepted product modes and
management capabilities, not an implicit initial Installation authorization.

Runtime install, reinstall, power, and session admissions use the retained contract
and **the current action caller and exact input permissions**, plus explicit
suspension and safety restrictions. They do not reacquire offering/assignment
access or inherit the original creator's permission. Offering withdrawal alone
neither disables retained capabilities nor adds new ones. Project/service
suspension is a separate effective authority gate. Service-authorized cleanup
uses a dedicated recovery lane, never a consumer grant or an implicit bypass for
new customer effects. In-flight revocation/fencing belongs to operation execution.

### Quota escrow and preparation protocol

Milo remains the quota accounting owner. The proposed adapter requests exactly
one machine-count unit charged to the consumer Project UID, held by a protected
service-owned provider custody anchor. Quota IDs are globally unique within the
quota authority; receipts include authority/claim/anchor incarnations and amount.
The existing name-based ResourceClaim is not assumed to provide this protocol.

Required logical operations are `AcquireHold(key, exactInputs)`,
`InspectHold(key)`, `BindHold(key, allocationDecision)`, and
`ReleaseHold(key, terminalDecision)`. These are specifications, not shipped APIs:

- Acquire atomically checks available quota and persists an idempotent, non-expiring
  charged escrow. Same key/different inputs conflicts. Pending, denied, held, bound,
  released, and unknown are distinct. Both held and bound count against quota.
- Before calling Acquire, the ledger durably records the key, inputs, provider
  guard obligation, a Closed hold-specific quota gate, and retry outbox. A verified
  escrow receipt opens that gate through the quota adapter at the receipt revision;
  subsequent invalidation follows close-before-change. A lost reply uses Inspect or Acquire
  with that key. A linearizable NotFound observation alone is not terminal if an
  Acquire can still arrive late.
- Release on an unused key atomically installs a permanent cancellation tombstone;
  a late Acquire then fails. Release on a held key settles exactly once. Keys and
  tombstones cannot be garbage-collected while replay is possible. Thus confirmed
  Released or confirmed CancelledBeforeAcquire, not a timeout, permits Aborted.
- Holds cannot expire, be garbage-collected with a consumer, or be reclaimed
  unilaterally after policy reduction. A quota revocation first closes the quota
  gate, then queries the ledger decision: unbound attempts are atomically aborted;
  bound allocations retain charge/custody and enter the explicit restriction or
  release protocol. Uncertain disposition blocks quota reuse.
- Bind is an idempotent accounting annotation of an already charged hold, not a
  second quota check. A valid durable allocation decision cannot be refused due
  to a later limit change. Its response may be lost without invalidating binding.
  The hold receipt and closed-before-change quota gate make local commit safe;
  acknowledgment catches the quota view up asynchronously.
- Release requires a ledger-authenticated terminal decision, never a consumer
  delete. Bind and Release messages carry monotonic custody revisions and queryable
  decisions. A delayed Bind after terminal release cannot resurrect a hold.
  Unknown or contradictory accounting evidence stops new affected work and raises
  a custody incident; it never makes occupied capacity free.

The local ledger serializes commit versus abort under the attempt/hold locks.
Once AbortDecided is durable, no allocation can consume that hold. Once Bound is
durable, an abort cannot release it; only the release owner's settlement can do so.
This avoids distributed two-phase commit by requiring quota escrow to remain valid
across local commit and by forbidding autonomous quota revocation. An authority
unable to guarantee those semantics is unsupported, not eventually consistent.

### Admission algorithm and state machine

1. Resolve provider and exact intent without effects. Establish ProviderGuard and
   its lifecycle interlock before creating preparation or acquiring quota. In a
   ledger transaction verify Open provider/consumer lifetime gates, insert an
   AdmissionAttempt, and record its guard obligation and resolution work.
2. Resolve feasibility, exact sources and current caller evidence. Persist the
   dependency vector and quota acquisition work. Acquire/recover escrow as above.
3. Select eligible trusted supply. Selection is advisory until commit. In one
   serializable transaction recheck gate vector and deadline, provider Open,
   current eligibility and evidence validity, exact Machine intent, held quota,
   enrollment identity and all uniqueness constraints. Insert the allocation,
   occupancy, permanent MachineBinding, decision, BindHold outbox and non-effectful
   allocation-continuation outbox. The hold is now locally Bound.
4. Recover/publish these records. Subsequent effects require separately admitted
   operations with exact inputs and an atomic operation/dispatch-outbox commit.
   There is no executable boot or power command in the allocation-continuation
   event. Public Machine status is a projection, not the commit record.

| State | Legal transition and retained obligations |
| --- | --- |
| Preparing | Exact resolution and current authorization; no physical effects. May request quota or decide abort. |
| QuotaPending | Durable key exists, response unresolved; query/retry same key or decide abort. |
| Held | Verified charged escrow; selection and fenced commit, or AbortDecided. |
| AbortDecided | No future binding; quota cancellation/release outbox must settle. |
| Aborted | Terminal attempt with quota cancellation/release acknowledgment; no occupancy. |
| Bound | Allocation decision, occupancy and charge durable; resume continuation, never return to Preparing. |
| Releasing | Release owner closes effects and gathers its required evidence; occupancy/charge remain. |
| Quarantined | Divergent or unknown custody/effects; no acquisition or ordinary execution. Recovery or verified release only. |
| Released | Terminal allocation with release and quota settlement proof; old MachineBinding remains terminal. |

Preparing through Aborted describe attempts; Bound onward describe allocations.
Transient observations such as AuthorizationUnavailable or QuotaOutcomeUnknown
are conditions, not success states. If a contender loses occupancy, it can select
another eligible asset within the same bounded Held attempt after fresh checks.
If no supply remains, report InsufficientCapacity, decide abort and settle quota
before another attempt. A new attempt sequence for that same still-unbound Machine
requires the previous one Aborted and current authority. One nonterminal attempt
per exact Machine key prevents parallel retry holds. Selection timeout triggers
abort, never releases quota or occupancy by clock alone.

### Reservation and dispatch outbox recovery

An outbox publisher claims rows with renewable worker leases solely for efficiency;
leases are not hardware fences. Every message uses the original immutable record
ID/revision/digest and exact destination. A timeout before acknowledgment retries
or queries that identity; a transport receipt marks delivery only. Never generate
a new allocation, operation, hold, or target because acknowledgment was lost.

For Fleet operations the existing route remains:

```text
ledger operation + outbox commit
  -> management writer creates exact hub intent / selected-cell targeting
  -> selected cell writes attributable LOCAL results
  -> Karmada reflects results onto the hub copy
  -> management validates identity, revision, fence, freshness and evidence
  -> management projects sanitized public status
```

The execution owner must provide idempotent create-or-observe by operation ID,
digest conflict rejection, and effect-boundary fencing. A recreated hub object
requires exact retained identity validation, not name adoption. A partition or
wrong-cell result cannot grant new admission or prove completion. Competing cells
cannot independently reserve capacity. Retargeting requires the execution owner's
explicit fenced transition; this outbox never invents it. Failed continuation
keeps occupancy and produces a blocker; release is explicit, not compensation
that assumes no message was delivered.

### Provider retirement and deletion ordering

Provider registration must install the Milo lifecycle interlock before its guard
can become Open. Retirement locks its authority gate and then its guard in the
standard lock order, closes the gate, marks Closing, and records reconciliation
work in one transaction. Every preparation,
allocation, and later operation admission locks the same guard. Work either wins
before closure and appears in the obligation set, or loses and performs no effect.
Do not accept preparations under a different provider on retry.

Milo's proposed retirement protocol queries this protected obligation set before
any early deletion of dependent artifacts, quota anchors, namespaces, credentials,
or control plane. A root finalizer alone is insufficient. Closing rejects new
acquisition and ordinary operation admission, but permits narrowly authorized
settlement, fencing and cleanup under retained custody. Retirement grants no
automatic wipe or reclaim authority over active allocations.

Retire only when all attempts are settled, holds acknowledged terminal, allocations
released, outboxes drained or durably tombstoned, and the execution/release owners
prove no live remote authority remains. Enrollment and replay history remain
protected for their required retention. Unknown state is a blocker. The retirement
receipt records the final ledger sequence and retained custody manifest; Milo may
then purge only dependencies proven no longer needed. No custody transfer is
supported in the initial protocol: unfinished obligations block purge. Same-project
provider/consumer cases use the same ordering. Forced deletion bypasses the
contract and becomes disaster recovery, never a successful release.

### Minimal companion interfaces

| Owner | Required input/result; boundary |
| --- | --- |
| Operation execution | Ledger-local hook for exact tagged subject, current action/input decisions, subject-specific predecessor/settlement precondition and operation plus outbox; one asset slot and queryable fences. Owns effect ordering, retries and completion policy. |
| Network handoff | Versioned feasibility evidence before allocation; exact retained network prerequisites before effect admission; queryable release evidence. Owns attachments, IPAM, routing and network safety. |
| Inventory trust | Canonical asset/enrollment identity, eligibility revision, freshness deadline, gate participation and occupied adoption evidence. Owns verification, deduplication evidence and trust policy. |
| External fulfillment | Exclusive exact supply reservation, queryable idempotency and retained custody equivalent to this ledger's guarantees. Its transport/procurement policy is not defined here; enablement remains blocked. |
| Release/reuse | Authenticated subject-scoped certificate, all obligations, effect/replay barriers and real accounting settlement or proved non-applicability. Release owner alone clears exact occupancy under the ledger protocol. Owns cleaning and reuse policy. |
| Deployment | No-loss ledger failover, protected storage, independent restore authority and compatible dependency qualification. Owns topology, packaging and operations deployment. |

For allocation release, the settlement certificate first moves custody to a terminal-effects
state while occupancy remains held. Its transaction emits the quota release work.
That state is recorded as a substate of Releasing and rejects all ordinary
operation admissions, including those competing with the settlement transaction.
After a confirmed quota acknowledgment, one transaction records Released and
clears occupancy. Unknown quota settlement keeps occupancy even if hardware is
clean. A certificate replay for another allocation, asset epoch, or restoration
epoch is rejected. This is ordering, not a new definition of cleaning assurance.

### Disaster restoration

Back up the ledger, authority mutation journals, exact source snapshots, quota
receipts, outboxes, retired identities, and protected input references as a
consistent custody set. Do not reconstruct occupancy from public Machines,
Karmada copies or provider listings. Restoring an older snapshot is not ordinary
leader failover and cannot assert that an absent record never committed.

Before serving a restore, use an independently durable monotonic recovery epoch
authority to fence the old ledger writer and all old admission credentials.
It must outlive the restored backup and be consulted by quota and effect owners;
a database sequence inside that backup is insufficient. Close all new admission,
mark potentially occupied supply unavailable, and stop automatic outbox replay.
The execution owner must fence old effect paths or report them unknown; the
deployment owner must prove the old primary cannot return as a writer.

Reconcile ledger decisions/WAL against quota anchors, custody manifests, canonical
inventory and independently retained remote operation histories. Import unmatched
holds and reservations as unknown obligations, never free resources. Recover the
same Project UIDs and exact incarnations; a recreated project cannot inherit them.
Only reissue a specific retained dispatch after its original commitment and current
executor/recovery fences are proven. Reopen gates with fresh authority snapshots
and epochs after reconciliation. If evidence was irretrievably lost, keep affected
supply quarantined and projects blocked; operator optimism is not evidence.
Recovery epoch infrastructure and this exercise are enablement prerequisites.

### Crash and race matrix

| Fault or race | Durable winner / required recovery |
| --- | --- |
| Two offerings select one Device through different capacities/projects | Canonical enrollment and asset occupancy constraints permit one binding; other contender reselects or settles quota. |
| Two requests or workers bind the same Machine | Exact Machine key and attempt constraint admit one; identical retry reads original decision; changed digest conflicts. |
| IAM revoke/assignment withdrawal races commit | Gate closure or binding wins the shared lock; no old decision commits after closure acknowledgment. |
| Mutation crashes after closure, before source update/reopen | Gate stays Closed; query mutation journal and revision; no timeout reopening. |
| Source/class/profile/project recreated under same name | Exact identity mismatch rejects the attempt; no substitution with new source. |
| Crash before Acquire send / lost Acquire reply / delayed Acquire after abort | Durable key drives recovery; cancellation tombstone prevents late acquisition; Aborted waits for acknowledgment. |
| Quota reduction/revocation races binding | Quota gate closes first; abort if unbound, otherwise retained charge and explicit lifecycle handling. |
| Database commit succeeds but response is lost | Query exact attempt/decision on authoritative writer; do not release escrow or choose new allocation. |
| Crash after binding before BindHold/public projection | Charged escrow and occupancy remain; outboxes recover annotations and projections. |
| Crash or partition after hub delivery before acknowledgment | Retry same ID/digest/target; remote dedupe/fence required; delivery is not readiness. |
| Retirement races preparation or operation admission | Guard transaction orders them; existing obligation blocks purge; loser performs no effect. |
| Consumer API/project disappears or offering is withdrawn after binding | Retained provider custody survives; offering edits do not alter runtime contract; explicit suspension/release remains separate. |
| Release acknowledgment lost or Bind arrives after Release | Inspect same quota key and revision; terminal tombstone wins; occupancy remains until proof. |
| Backup predates a commit; old primary/cell remains alive | New recovery epoch, fence and reconcile; unknown occupancy and holds remain unavailable. |
| Untrusted/absent/regressive reflected status | Management rejects projection; it cannot clear occupancy or recreate admission authority. |

### Acceptance tests

These are required future implementation tests, not executed tests. Build a
deterministic state-machine model plus real PostgreSQL multi-connection tests and
qualified Milo/IAM/quota integration fault injection. Simulator-only success is
not platform qualification. Record histories and verify a serial order consistent
with acknowledged gate closures and allocation decisions.

1. Race hundreds of acquisitions across overlapping offerings and provider aliases
   for one canonical asset: exactly one occupancy and Machine binding; no duplicate
   escrow per attempt. Race two assets for one Machine and assert the inverse.
2. Inject revocation at each point between evaluation and commit for IAM groups,
   source-use, entitlement, project/namespace deletion, offering/profile policy,
   eligibility and suspension. Acknowledged invalidation forbids older commits.
   Test negative-policy additions and concurrent invalid candidate updates too.
3. Forge local publication, recreate every named source, and attempt custom-profile
   bypass under Selected grants: reject without leaking other tenants' inventory.
4. Drop each quota response, delay Acquire until after abort, duplicate Bind/Release,
   and crash after every ledger write/send. Count remains charged until proof;
   terminal keys never resurrect; no second attempt starts before settlement.
5. Force serialization retries, deadlocks, lost commit replies and qualified primary
   failover. Recover the same exact decision and outboxes; no acknowledged commit
   disappears and no transaction retries with stale epoch evidence.
6. Kill publishers before/after hub write, duplicate delivery and inject wrong-cell,
   stale incarnation and stale fence results. Only validated LOCAL-to-reflected
   evidence projects; no duplicate execution authority or inferred readiness.
7. Race provider retirement with preparation, quota acquisition, binding and
   operation admission. Destroy the consumer API. Purge stays blocked by every
   unsettled hold/operation/outbox even if all public Machine lists are empty.
8. Withdraw an offering after binding: retained modes remain usable by a currently
   authorized caller. Revoke that caller or an input, or impose explicit suspension:
   new affected operation admission fails; service cleanup remains possible.
   For an unbound request, revoke and restore access while its worker is stopped:
   invalidation history rejects it rather than resurrecting its prior request.
9. Restore a backup predating a live allocation while old workers remain reachable.
   Admission stays closed until epoch fencing and reconciliation; unmatched remote
   holds/operations become unknown custody, never free stock or replayed effects.
10. Import occupied inventory without trusted ownership transition: no provisioning,
    no free capacity publication. Exercise unavailable identity and time authorities,
    indefinite Closed gates, and quota custody loss: fail closed and alert.
11. Enroll fresh owned supply Disabled; admit Preparation without Machine, hold or
    Installation. Race allocation, duplicate enrollment/adoption and maintenance
    at L1 and clearance: one asset slot/occupancy, no boot until network receipt,
    no eligibility until all preparation effects and owner clearance settle.
12. Import unresolved adopted custody, resolve its identity observationally, then
    authorize #65 cleanup. Race R1/R2 with preparation and provider retirement;
    real accounting or provenance-backed non-applicability is mandatory. Restore
    before either subject existed while the cell retains a send: recover Unknown
    custody, never free stock, synthetic quota acknowledgment or automatic replay.

13. Admit a witnessed passive panel move under PhysicalMaintenance without any
    compute or boot objects. Race destination reservations, another transition,
    adoption, commissioning/allocation of idle supply, R1/R2 and retirement.
    Assert atomic closure/custody/slot/outbox, exact transition self-match, no
    effects by losers and no clearance from timeout or missing consumer records.
14. Restore before maintenance L1 with a delivered field permit; retain both
    locations and reject replay. Dependent active-owner fence loss/addition must
    block work; a panel approval cannot authorize that owner's disruptive action.

These specialize shared A1 (exclusive acquisition), A2 (retained contract), A3
(current authority), A5 (federation), A6 (custody), and A7 (observational adoption)
from the pinned #51/#58. A4 source-mode and execution assurance remain #59's tests.

## Production readiness review questionnaire

### Feature enablement and rollback

Observation is allowed without admission. Production allocation stays disabled
until every dependency in [Infrastructure needed](#infrastructure-needed) passes
its qualification tests and required owners approve the contract. Enable one
qualified provider cohort without changing existing workloads. Rollback closes
new-admission gates while keeping journals, quota custody, retirement blockers,
recovery and service cleanup running. Reenable only after reconciliation; do not
downgrade storage semantics or discard unknown work. No upgrade or rollback test
has run for this proposed protocol, and there is no provisional-API migration duty.

### Monitoring requirements

Expose bounded-label metrics for admission decision latency, denial reason,
serialization retries, occupancy conflicts, authority-gate closed age, unresolved
mutation age, held quota age, quota/ledger discrepancy, unpublished outbox age,
retirement blocker count/age, quarantine count and recovery-epoch divergence.
Page on duplicate occupancy/charge invariant violations, custody loss, stale
writer acceptance, or replay after terminal disposition. Alert on configured
pending-work age budgets; benchmarks must establish numerical SLOs before rollout.

Audit each attempt and transition with caller identity, request digest, exact
source keys, gate revisions, hold/allocation IDs, commit sequence, recovery epoch,
selected cell and reason codes. Keep high-cardinality IDs in protected audit,
not metric labels. Never log tokens, rendered secrets, provider credentials or
private inventory in customer-visible diagnostics. Sanitized customer conditions
distinguish InsufficientCapacity, AuthorizationUnavailable, AuthorizationDenied,
QuotaOutcomeUnknown, ProviderRetiring and AdmissionRecoveryRequired.

### Dependencies, scalability, and troubleshooting

The ledger and authority owners are critical admission dependencies. Their outage
stops new affected work, not guest operation. Bound queue sizes per consumer and
provider, cap unresolved holds, enforce fair selection retries and backpressure
before acquiring more escrow. Measure broad IAM-gate churn and lock contention;
no capacity or performance numbers are asserted here. Never evict custody records
to solve storage pressure. Retention ends only after explicit replay-safe archival.

Diagnose by attempt ID: inspect authoritative decision, gate mutation, quota key,
guard obligations and outbox identity in that order. Restore dependency access and
resume the retained transition. Do not delete locks, finalizers or quota anchors
to make a stuck Machine appear healthy. There are no runtime commands for this
proposal; implementation must provide protected inspection and recovery tooling.

## Implementation history

- 2026-11-14: Separate admission/custody design drafted against the exact source
  heads listed above. Documentation validation is not runtime verification,
  independent review sign-off, or platform-owner approval.

## Drawbacks

Cross-authority gate integration is substantial work. Coarse gates may interrupt
unrelated acquisitions, and a durable non-expiring hold can reduce quota availability
for an extended outage. A single logical ledger and independent recovery authority
add infrastructure cost. These costs preserve exclusivity and custody instead of
silently weakening correctness during failure.

## Alternatives

- **Kubernetes resource-version checks across APIs:** cannot atomically order
  occupancy, Machine uniqueness and external authorization; rejected.
- **Check IAM immediately before SQL commit:** still admits a post-check revocation
  race; rejected without close-before-change authority participation.
- **Saga that releases quota on timeout:** delayed Acquire or committed allocation
  may outlive compensation; rejected without cancellation tombstones and decisions.
- **Expiring quota leases:** expiry during a partition could remove accounting for
  occupied hardware; rejected in favor of charged escrow with explicit settlement.
- **Distributed two-phase commit across Milo and project APIs:** requires prepared
  transaction support throughout and creates in-doubt participants; choose a local
  commit plus non-revocable escrow and gate interlocks instead.
- **Per-offering/per-cell allocators:** overlapping supply loses global exclusivity;
  reject. Future partitioning must retain a single authoritative owner per global key.

## Infrastructure needed

All entries are **unresolved platform prerequisites**, not alternate algorithm
choices. The algorithm above is selected; owner agreement, concrete API schemas,
implementation and qualification remain required. Missing support gates production.

| Owner | Required contract before enablement |
| --- | --- |
| IAM and service entitlement | Revisioned caller/input evaluation, complete dependency gates, durable mutation IDs and request-lifetime invalidation history, close-before-change enforcement on every effective mutation, validity/time rules and no bypass writes. |
| Milo project lifecycle and API ingress | Exact project/request incarnation and authenticated provenance; protected ingress/finalizer integration; retirement interlock before deleting any dependency; protected provider custody outside consumer GC. |
| Milo quota | Non-expiring charged escrow, idempotent acquire/inspect/bind/release, cancellation tombstones, gate-ordered revocation, protected anchors and authoritative disposition queries. |
| Catalog/publication and provider policy | Protected one-hop mappings, immutable profiles, effective revision proof and gate participation; publication alone grants nothing. |
| Inventory and provider integration | Trusted canonical asset deduplication, single enrollment, eligibility revisions and freshness; external providers additionally require a qualified equivalent exclusive reservation contract. |
| Storage/deployment/recovery | Serializable PostgreSQL, synchronous durable failover and old-writer fencing, independent monotonic recovery epochs, coordinated custody backup and proven restoration. |
| Execution, network and release owners | Ledger-local operation/outbox hook, exact-input fences and validated result interface, feasibility evidence, retained cleanup authority and replay-safe settlement certificates. |

No invented ResourceClaim field, Milo endpoint, federation transaction, or
production guarantee is implied. Platform owners must demonstrate the proposed
contracts under the acceptance races before this enhancement can be implementable.

[template]: https://github.com/datum-cloud/enhancements/blob/b5fe372ca85b50c674a9ce41419ce5210cf33f6d/enhancements/template/README.md
[bmc]: https://github.com/datum-cloud/fleet/blob/f04e63ddd9d10cbf306daf3a0232961debd85e7f/docs/enhancements/secure-bmc-access.md
[management]: https://github.com/datum-cloud/fleet/blob/fa07c2236801a33a58efc914b555586c8e146cc6/docs/enhancements/fleet-management/README.md
[offerings]: https://github.com/datum-cloud/fleet/blob/a922ff1eb3275e26af6ae750a7515ddde245a754/docs/enhancements/profile-access/README.md
[pxe]: https://github.com/datum-cloud/fleet/blob/d7ff793b03711203dbfc9fac87d43922cc649b6f/docs/enhancements/pxe-installation/README.md
[lifecycle]: https://github.com/datum-cloud/fleet/blob/d7ff793b03711203dbfc9fac87d43922cc649b6f/docs/enhancements/machine-lifecycle/README.md

## Alignment revision provenance

On 2026-11-14 the [prior integration review][custody-review] identified
commissioning and adopted-settlement gaps, addressed by `custody-subjects-v1`.
The [fresh complete-set review][maintenance-review] accepted those joins but
identified the remaining passive-maintenance execution-subject gap. This
`physical-maintenance-v1` alignment extends the same custody/slot/journal/clearance
protocol without changing those joins or catalog-withdrawal/runtime semantics.
All contracts remain proposed; fresh independent complete-set review and later
implementation/owner/hardware qualification are required. No self-signoff.
This proposal's contract-bearing revision is `4bf770db8bd2b065748c3b9384d4c72f9b8ec0e1`.
The companion revisions below are immutable **contract-bearing snapshots**, not
claims about final PR heads or merge dependencies. Subsequent commits only refresh
pins and provenance. Read this aligned set together: older embedded companion
pins inside those snapshots describe their drafting baseline, not permission to
substitute an older contract for a join defined here. #66 records the final-head
complete-set review manifest. No circular exact-final-head dependency is required.
Any later normative change requires a new manifest and fresh complete-set review.

| Companion | Exact dependency revision |
| --- | --- |
| [#61 network-handoff][custody-61] | `25fef48790b13da95900de64ffd2d1b36e0510eb` |
| [#62 execution-recovery][custody-62] | `4f9934980b3f441c1d777157fa0ba1dcfce372d7` |
| [#63 inventory-trust][custody-63] | `2b384517fd08c04cf078923ebda2cddcfa1dc424` |
| [#64 provider-fulfillment][custody-64] | `58a6596d30b3819d0b3799192730626439ef9885` |
| [#65 release-reuse][custody-65] | `310a55d97394bb296c579bd3ad01177455c15cc4` |

[custody-review]: https://github.com/datum-cloud/fleet/pull/66#issuecomment-6883007682
[custody-61]: https://github.com/datum-cloud/fleet/blob/25fef48790b13da95900de64ffd2d1b36e0510eb/docs/enhancements/network-handoff/README.md
[custody-62]: https://github.com/datum-cloud/fleet/blob/4f9934980b3f441c1d777157fa0ba1dcfce372d7/docs/enhancements/execution-recovery/README.md
[custody-63]: https://github.com/datum-cloud/fleet/blob/2b384517fd08c04cf078923ebda2cddcfa1dc424/docs/enhancements/inventory-trust/README.md
[custody-64]: https://github.com/datum-cloud/fleet/blob/58a6596d30b3819d0b3799192730626439ef9885/docs/enhancements/provider-fulfillment/README.md
[custody-65]: https://github.com/datum-cloud/fleet/blob/310a55d97394bb296c579bd3ad01177455c15cc4/docs/enhancements/release-reuse/README.md

[maintenance-review]: https://github.com/datum-cloud/fleet/pull/66#issuecomment-6883682145
