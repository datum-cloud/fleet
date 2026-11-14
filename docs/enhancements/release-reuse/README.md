---
status: provisional
stage: alpha
latest-milestone: null
---

# Release, reuse, and sanitization custody

## Summary

**Proposed protocol, not implemented or qualified behavior.** Release is an
irreversible closure of a subject's admitted authority followed by durable,
evidence-driven settlement. It is not deletion, a power command, or a backend
cleaning flag. Extend the shared admission ledger in [allocation #60][allocation];
do not add an allocator, independent occupancy lock, or second hardware writer.
Use [execution #62][execution] for every effect and [networking #61][network] for
every network barrier. Only a validated release decision with acknowledged real
accounting settlement or proved non-applicability can clear occupancy. Allocation
release always requires its original quota acknowledgment. Free occupancy is necessary, not sufficient,
for new allocation.

Scope is standalone Metal, including Fleet-owned and qualified rented supply.
There is no managed node/cluster behavior, compatibility layer, runtime change,
supplier cancellation automation, or general-purpose custody-transfer mechanism.
The original submission called its supplied #60 baseline signed off; that is
historical provenance only. Revised dependencies and this complete set require
fresh review and do not prove any external integration exists.

## Motivation and intended guarantees

A deleted Machine can leave a running RAM installer, an active console, forgotten
storage, or a delayed BMC reset. A successful erase response can refer to the
wrong disk. Losing a provider must not lose the record of who still holds a
customer's data. The protocol selects these guarantees under its explicit
qualification assumptions:

- A committed release admits no new customer effects; previously admitted work
  remains accounted for until physical and downstream boundaries settle.
- Unknown cleanup, identity, revocation, or accounting retains occupied custody.
- Data treatment, firmware trust, credential invalidation, network isolation,
  and execution stop are independent predicates; none substitutes for another.
- Cleanup authority survives customer permission loss but cannot install a
  customer workload, purchase capacity, or target a different asset.
- Reuse has a new Allocation and Machine incarnation, never resurrection of the
  released binding. Inventory and provider eligibility remain independently gated.

These are proposed safety properties, not unconditional promises against malicious
hardware, physical theft, dishonest providers, or missing owner agreements.

## Baseline and source contracts

Created on 2026-11-14 from fetched `origin/main` at
`8ebec19513a0cbc8f219d3acae46f89ab471510b`. Companion files are read at pinned
revisions, not imported or merged. In particular, this branch inherits no #59
files. Read the [architecture](../../architecture/design.md), [provisioning
source](../../proposals/fleet-management/bare-metal-provisioning.md#release-and-reuse),
[hardware source](../../proposals/fleet-management/bare-metal-hardware.md), and
[source review](../machine-lifecycle/source-review.md) for the earlier intent.

| Source | Pinned head | Consumed contract |
| --- | --- | --- |
| [#41 BMC access][bmc] | `f04e63ddd9d10cbf306daf3a0232961debd85e7f` | Allocation-scoped gateway, relay and console revocation; no direct customer BMC credentials. |
| [#51 management][management] | `fa07c2236801a33a58efc914b555586c8e146cc6` | Separate physical, allocation and installation identity; management/cell writers. |
| [#58 offerings][offerings] | `a922ff1eb3275e26af6ae750a7515ddde245a754` | Retained accepted capabilities; catalog withdrawal is not eviction. |
| [#59 lifecycle][lifecycle] and [PXE][pxe] | `d7ff793b03711203dbfc9fac87d43922cc649b6f` | Release, quota and retirement obligations; exact boot and installation inputs. |
| [#60 allocation][allocation] | `d072f3b5ded5d212e59ecc511c9282037d48c9f1` | Shared ledger, ProviderGuard, AuthorityGates, charged escrow and permanent MachineBinding. |
| [#61 networking][network] | `04adf7013de42c40086502918dbfe05302ec00e8` | DisconnectAcknowledgment, DisposeClaims and no-reuse barriers. |
| [#62 execution][execution] | `6a886b415edb5ba9c1295e4d88db9cd86bd51f0e` | Journal-before-effect, shared slot, no-late-effect settlement and restoration epochs. |
| [#63 inventory trust][inventory] | `d79495be33d7c0ab126b497dac6a5874924eee81` | Discovered companion draft: exact enrollment/component continuity, invalidation and physical reservations. |
| [#64 provider fulfillment][fulfillment] | `8e21b043bc853d9d81f5aa54b0dc3c52465fc4a5` | Discovered companion draft: LockedClean, finalization R3, independent Metal custody on supplier loss. |

#63 and #64 are draft interfaces, not owner approval. Their future revisions
require explicit compatibility review. This proposal narrows the earlier generic
cleaning contract; it does not weaken #60's settlement or no-transfer requirements.

## Records and authoritative writers

All names are proposed internal records. Protected service storage and encrypted
input/key custody live outside consumer and provider namespace garbage collection.
Provider ownership is responsibility, not permission to rewrite custody. No API
owner reference can cascade-delete these records. Public objects are projections.

| Record | Required retained contents | Sole writer |
| --- | --- | --- |
| ReleaseCase | Release ID; immutable tagged subject and Project UIDs, Allocation/Machine only for Allocation; original custody/resolution history otherwise; exact supply, enrollment and component manifest; reason and destructive authorization; pinned policy/recipe; closure sequence; phase/version; provider guard; dependency vector; remaining obligations | Management release coordinator, guarded ledger transitions |
| CleanupObligation | Stable obligation ID; release ID; kind and exact target; required predicate; predecessor dependencies; operation/step IDs; input digests and protected references; owner; status and evidence references | Release coordinator defines/accepts; owner reports evidence, not acceptance |
| ClosureManifest | Ledger high-water sequence; complete admitted operation, session, credential, boot grant, network binding and preparation set; outbox IDs; selected-cell/provider incarnations; closure receipts and unresolved sends | Release coordinator snapshots central inventory; execution owner appends authenticated boundary reconciliation |
| CleanupAuthority | Service principal/audience; exact obligation and permitted verbs/targets; release and execution fences; input digest; finite dispatch validity; independently renewable authority revision; revocation high-water mark | Management authority issuer under shared ledger admission |
| EvidenceEnvelope | Issuer identity and qualification; exact record/component IDs; causal operation/step; dependency revisions; fences; observations and uncertainty; lineage/restore epochs; digest and protected payload reference | Respective evidence producer appends; management validates |
| ReleaseCertificate | Accepted obligation/evidence closure, final effect fences, assurance vector, network dispositions, terminal-effects sequence, policy digest, restore lineage; no secret material | Management release coordinator |
| FinalizationObligation | Quota key and decision, provider reservation/generation, finalization request/receipt, terminal outboxes and replay barriers | Quota/provider adapters report; coordinator accepts under ledger transaction |
| AccountingDisposition | Exact subject, required accounting authority/policy revisions, coverage and decision cutoffs, real obligation IDs/terminal receipts or positive NotApplicable proof, unresolved gaps, signature and restore lineage | Accounting owners report; release coordinator validates, never fabricates quota acknowledgments |
| CustodyIncident | Unknown/mismatch reason, affected scope, responsible service, retained original identities, recovery contacts, approvals and next evidence required | Service incident/recovery authority; cannot mark supply free |

Reuse #60's Enrollment, Occupancy, MachineAllocation, MachineBinding, QuotaCustody,
AuthorityGate, ProviderGuard and RecoveryOutbox; do not shadow them. #62 alone
writes execution journals and manages the shared conflict slot. Inventory alone
writes identity/eligibility; networking alone writes switches, routing, IPAM and
binding tombstones. A cell writes local results, Karmada reflects them, and
management validates before accepting a certificate or projecting public status.
For a non-Fleet provider, use #64's authenticated result path with the same
management validation, not invented direct cell-to-project credentials.

## Non-consumer custody settlement

The proposed `custody-subjects-v1` alignment adds explicit `Preparation(custodyUID)`
and `AdoptedCustody(custodyUID)` ReleaseCase subjects alongside
`Allocation(allocationUID)`. Each uses #60 Occupancy/AssetCustody, ProviderGuard
and #62's **same persistent asset slot**. No Machine, MachineAllocation, quota
hold or Installation predecessor is synthesized. Release is the sole clearance
writer; inventory resolves identity and publishes eligibility, execution owns
effects, networking owns configuration, accounting owners attest their obligations.
The allocation-only references below remain requirements for Allocation, not
implicit requirements to invent those objects on a non-consumer path.

The `physical-maintenance-v1` extension also admits
`PhysicalMaintenance(custodyUID)` to this same ReleaseCase union. Its clearance
is defined below; it does not inherit commissioning, installation or consumer
sanitization prerequisites merely by using the common release coordinator.

### Adopted authorization and policy provenance

An observational adopted record can begin with only a custody UID and conservative
asset/alias/physical-domain exclusion, without trusted Device or known accounting.
It grants zero mutation authority. Keep the original UID and origin/observer
evidence throughout resolution and settlement. #63's witnessed read-only process
must establish exact canonical asset, enrollment UID/epoch, Device and component
incarnations, physical placement, authenticated BMC route, all NICs and feeds.
Its atomic resolution installs canonical exclusion before narrowing broader
holds and retains overlapping claims. No boot, credential rotation, power or
network mutation may be used to bootstrap this prerequisite. Unresolved targets
or conflicts remain occupied; a free-text operator assertion cannot resolve them.

Before R1, seal a proposed `AdoptionSettlementAuthorization` containing:

- Original custody/domain and exact resolution revision; provider Project UID,
  prior-custodian identities and current independently verified approval evidence.
- Active-use obligations and every affected tenant/custodian's consent or a
  separately authenticated, policy-authorized termination decision; absence of a
  public Machine is not evidence of no active user or legal destruction right.
- Explicit transfer of management/cleanup authority for this exact asset into
  the same Fleet custody domain, effective scope, conditions and destructive
  limits. This does not transfer a live allocation between providers/domains.
- Versioned provider cleanup and onboarding policy, original prior-owner data
  treatment commitments, source provenance/signatures and immutable accepted
  content. Choose a qualified CleanupProfile satisfying their combined obligations;
  conflicting/unknown commitments block, rather than default to a weaker profile.
- Complete known and potentially missing media/component history, old credential,
  network, installer/task and manual/external-writer inventory; responsible owners
  and exact predicates for proving coverage. Importing an inventory is not proof
  that it is complete or that any old authority has stopped.
- Current service-principal action/input rights, exact recipe/image/adapter,
  trusted targets, secret versions, network/origin policy, finite budgets/deadlines,
  complete gate vector, restore lineage and required accounting authorities.

Prior-custodian approval, provider approval and service authority are separate
checks. Authenticate their rights and scope, not just signatures, at commit.
Observational adoption, later inventory Ready, retirement, physical possession,
offering withdrawal or an operator incident never grants wipe authority. If proof
is missing, retain Unknown. Already-authorized obligations retain narrow settlement
authority after customer deletion; a Closing provider cannot originate a new
adoption cleanup grant. Revoked service/target-safety authority blocks dispatch.

Adopted R1 locks #60's full total order and compares original occupied custody,
resolution/approval/input digests, current gates and expected epoch. It records
one ReleaseCase per subject, permanently closes new non-settlement authority,
captures all central outboxes and old-authority inventory, registers obligations
and records a stop request without stealing the shared slot. The origin hold and
all real accounting remain. For a case with no prior Fleet operation, a verified
external quiescence/closure manifest takes the place of operation history, **not**
an Installation predecessor or an invented zero-effect receipt.

Proceed through Closing → Settling → Cleaning → Verifying → EffectsClosed using
the same evidence predicates and #62 journal/broker. Old-owner paths, BMC queues,
RAM code and independent/manual execution authorities need effective exclusion
and accepted no-late-effect proof before cleaning. A ledger closure cannot fence
an external administrator. Missing prior-owner network bindings require owner
resolution and disposition, not Fleet IPAM edits. #61 admits a new ServiceCleanup
binding keyed to AdoptedCustody, exact release obligation and full operation fence;
no consumer session or customer connectivity is permitted. Cleanup itself must
stop, drain and dispose its own temporary paths/credentials before EffectsClosed.

Use the full sanitization/restoration assurance vector below. For adopted media,
the provenance-backed prior-custodian coverage manifest plus independently verified
current enumeration replaces the unavailable pre-allocation manifest. An unknown
or missing original disk is still an unresolved data-bearing obligation; current
enumeration alone cannot prove it never existed. Data history insufficient for
cryptographic erase cannot be repaired by enabling encryption during adoption.
Qualified alternative treatment must meet all retained commitments; otherwise
quarantine. Evidence binds original custody UID, resolution, enrollment/component
epochs, authorization and policy digests, operation/fence and release lineage.

### Accounting without a fabricated consumer hold

AccountingDisposition is a tagged, owner-attested result, not a boolean waiver:

| Kind | Required proof before adopted R2 |
| --- | --- |
| `ObligationsSettled` | Reconcile the union of #60 decisions/hold keys, prior-custodian ledgers, provider records and required accounting authorities. Retain each real obligation's identity, applicable policy and terminal disposition/acknowledgment from its owner. Real Metal holds use #60's original keyed release/tombstone protocol after EffectsClosed. Never transfer another allocation's hold into this case. |
| `NotApplicable` | Positive accounting-owner decision that this exact custody never incurred an applicable machine-count/other required settlement obligation: issuer/authority incarnation, policy revision and reason, subject/resolution scope, custody origin and searched journal/archive ranges/checkpoints, closed acquisition paths and reconciliation provenance. Empty API listings, missing quota IDs, a restored empty ledger or provider assertion alone are insufficient. No quota ReleaseHold call or fabricated Released acknowledgment. |
| `Unknown` | Any unresolved owner, scope, history gap, possible late Acquire or contradictory record retains custody/quarantine. A new dummy charge cannot repair missing history. |

For mixed cases retain per-authority results: every required owner must either
settle its real obligations or positively establish non-applicability. Capture
these dependency gates at R1, fence future acquisition against the closing subject,
and query outside SQL. At certificate acceptance and R2 validate current authority,
exact coverage/cutoffs and gate revisions under locks. A possible delayed Acquire
requires the actual #60 cancellation tombstone, never NotApplicable. Unknown
origin or accounting scope blocks proof. Billing/refunds and business write-offs
remain distinct from technical custody settlement. An overlapping real allocation
settles through its own release case; this case cannot clear its occupancy.

Use `AccountingSettling` for these subjects rather than a fictitious QuotaSettling
step. After EffectsClosed and accepted AccountingDisposition, R2 atomically records
the adopted case/AssetCustody Settled, clears **only** that exact Occupancy revision,
writes permanent subject/operation/authority tombstones and terminal outboxes, and
leaves eligibility Disabled. Recheck full release predicate, current resolution,
policy, accounting and restore lineage. No MachineBinding is written. Lost R2
reply recovers the same decision; no new release key. A revoked proof between
accounting completion and R2 retains occupied quarantine and actual accounting
facts, never synthesizes a replacement charge. After R2, any defect creates a
new safety incident under current ownership, not revival of old wipe authority.

### Preparation clearance and transition to reuse

At Preparation L1, retain the release-policy obligations for that exact service
plan and verified first-use/previous-clearance provenance. They cover all possible
sends, verifier/installer stop, boot/credential revocation, exact network disposal,
data/firmware baseline postconditions and physical interests. After completion or
cancellation, preparation R1 closes further dispatch and reconciles that manifest;
L5 evidence does not itself clear custody. No blanket consumer-data erase is
implied. Additional repair/cleaning needs separately approved exact scope and a
settled predecessor under the same subject/slot.

The release owner validates every applicable predicate (with qualified policy
reasons for non-applicable ones), no-late-effect evidence and AccountingDisposition.
New enrolled, never-consumer commissioning uses accounting-owner policy and origin
history to prove NotApplicable; any real discovered obligation must reconcile.
R2 clears only Preparation occupancy and writes the same replay barriers. It
leaves enrollment Disabled; failed/cancelled plans do not become prepared by
virtue of clearance. Physical placement reservations remain held unless their
inventory owner independently proves clearance of all interests.

Following adopted R2, fresh separately authorized Preparation on non-conflicting
supply establishes boot/preparation evidence; following successful Preparation R2,
#63 alone validates fresh required identity/placement/network/power/trust predicates
and publishes eligibility. #60 then independently admits a new consumer allocation
with real quota and current permissions. No automatic install from adoption, no
synthetic initial Installation and no readiness from a release certificate.

### Physical maintenance clearance

At #60/#62 maintenance L1, retain a scoped clearance policy and obligation manifest
for the exact custody, inventory transition, field procedure and old/new physical
plans. Bind canonical Device/enrollment and component identities, optional supply,
permit/step and crew inventory, dependency-owner barriers and accounting provenance.
This is explicit current custodian/service/technician authority to perform those
physical actions and settle them, not implied authority from creating inventory.
For passive equipment policy positively marks BMC, boot, media/data treatment,
firmware restoration and compute quota NotApplicable with qualified scope/reason.
It must still require identity, physical safety, no-late-work, authority revocation,
dependent-owner disposition and physical reservation-interest accounting. A
missing field or absent Machine is never a non-applicability decision.

Maintenance R1 uses the full existing lock order and closes this subject's further
ordinary field steps, records stop requests on the same slot and reconciles every
outbox/permit/start, including unacknowledged deliveries. It does not cancel a
dependent active Allocation or acquire its cleanup authority. #62 supplies actual
worker cessation, independent old-crew/site exclusion and journal reconciliation;
permit expiry or a returned form alone is not cessation. Missing start records
after possible delivery remain Unknown. If work cannot be stopped/proven, retain
occupancy, the recovery hold and both possible physical locations.

Use Closing -> Settling -> Verifying -> EffectsClosed -> AccountingSettling -> R2.
Cleaning is omitted only by the retained qualified field policy; a new remedy
requires fresh explicit scope and predecessor settlement, not a fabricated erase
or boot. Verifying consumes #63's exact placement/removed-component disposition,
old-resource clearance and retained destination-interest receipts, plus #61 and
other dependent owners' scoped barriers/dispositions or positive no-dependency
evidence. Fresh observations must follow the final physical work. Returning to
the old rack/reconnecting altered wiring requires a separately admitted successor
field plan after cessation, never an automatic inverse operation.

The certificate binds transition/custody UID/epoch, canonical Device/enrollment,
full execution fence, accepted outcome (completed, verified no-work cancellation,
or safely stopped but unverified), all permit and owner barriers, physical receipts,
policy digests and current lineage. Real prior obligations use their original
accounting owners and receipts; NotApplicable requires the same positive policy,
origin, closed acquisition paths and decision-range proof defined above. No dummy
quota hold, ReleaseHold call or synthetic acknowledgment is allowed. Unknown
accounting or conflicting custody prevents R2.

R2 rechecks that complete predicate and expected intentional inventory closure
under #60's ordered locks, then atomically marks only this AssetCustody Settled,
clears its occupancy, and retains subject/permit/fence tombstones and guard
obligations still owed to other owners. It neither releases another subject nor
frees active destination reservations. Outstanding unsafe location/removal/late-work
obligations prevent R2; safely stopped but unverified equipment can clear only
with inventory retaining conservative physical/domain exclusion and eligibility
Disabled. No transient allocatable window is permitted.

#63 alone then revalidates applicable placement/trust/path predicates. Passive
equipment gains no compute eligibility. Idle compute stays Disabled until all
current qualification is satisfied, with new Preparation/clearance if required;
maintenance is not proof of disk cleaning or boot qualification. Dependent owners
alone resume their active devices/connections under fresh mappings and current
rights. Their barriers may remain held beyond target R2 with explicit retained
owner obligations; target clearance is not a reconnect instruction.

Retirement and restore include all physical-maintenance subjects even with no
MachineCapacity or consumer objects. Restore reconciles field journals, site
access and permit history, old/new reservations and owner barriers before any
replay. A pre-L1 backup cannot discard a later field permit. Missing work inputs,
approval, keys, target enumeration or independent cessation evidence leaves
Unknown/domain quarantine. A newly issued epoch or two-person approval alone
cannot revoke an old crew's physical ability to act.

### Retirement and partial restoration

ProviderGuard includes all non-consumer subjects, approvals, resolution history,
physical-domain holds, cleanup operations, network paths, accounting provenance,
clearance certificates and unsettled outboxes even when Machine lists are empty.
Retirement cannot purge their credentials, inputs or evidence. Closing admits only
already-retained scoped settlement, not new ownership/destructive authority.

Restore conservatively unions pre-resolution domains, resolved aliases/components,
operation/network/physical journals, prior-owner records and real accounting
histories. Fence old writers and reestablish the independent lineage before
per-record recovery. Missing resolution, original approval, policy bytes, secret
keys, media history or accounting coverage keeps the affected scope occupied and
effects disabled. Old NotApplicable proofs and clearance certificates require
revalidation; backup absence cannot prove no consumer or no send ever existed.
If the affected asset set is incomplete, quarantine its enclosing domain. Replay
of old cleanup after a later consumer allocation must fail subject/fence/tombstone
checks, never target by reused name, port or address.

## Admission closure and ordering

### Release authorization and atomic closure

Machine/namespace deletion and consumer project termination enter release through
the retained lifecycle contract. Persist the exact deletion authorization and
incarnation before effects; a missing object alone grants no wipe authority.
An explicit customer release needs current authorization. A service lifecycle
trigger needs its authenticated termination decision and the release authority
retained at allocation. An explicit runtime suspension or provider retirement
closes the applicable ordinary admission gates but does **not** by itself authorize
erasure. Catalog or offering-assignment withdrawal blocks new acquisition only;
existing allocations retain their accepted runtime capabilities, subject to current
actor/input permissions and separate safety or suspension gates. Withdrawal alone
closes no existing allocation runtime gate and creates no ReleaseCase. A safety
isolation obligation is not destructive-release authority.

R1 is one SERIALIZABLE #60 transaction, using its complete total order: gates,
ProviderGuard, attempt/provider-reservation children, QuotaCustody, MachineBinding,
Enrollment, Occupancy including domain holds, inventory-domain/identity/plan and
physical-resource rows, custody, asset slot, Operation, ReleaseCase, then its
obligation/closure/accounting children and append-only decisions/outboxes. Skip
inapplicable classes and sort exact keys within each class. All competing operation, inventory invalidation,
release, retirement and allocator writers must use that same order. Integrating
the appended slot/release locks is an owner prerequisite, not another mutex.
Do no remote calls while locks are held. Retry serialization failures against
fresh revisions; after an uncertain commit, inspect the original release key
and decision on the current durable writer.

For Allocation, R1 atomically (non-consumer substitutions are specified above):

1. Validates the destructive trigger, exact binding and retained cleanup contract.
   Inserts one ReleaseCase per Allocation; same key/different inputs conflicts.
2. Permanently closes ordinary operation/session/boot admission for this Allocation,
   records a stop request on the shared execution slot, and makes its state
   Releasing. It does not steal or clear an active/unknown operation slot.
3. Captures all admitted central operations and grants through the closure
   sequence, including not-yet-delivered outboxes, prepared work, failed partial
   operations, gateway tasks, consoles, virtual media and network requests.
4. Registers durable revocation, settlement, cleaning, credential, networking,
   accounting and replay obligations with ProviderGuard, retained exact inputs,
   policy digests and recovery outboxes. Occupancy and charged quota remain held.

Admission of every session/grant and its outbox must already participate in this
ledger transaction hook. Listing current Kubernetes Jobs or sessions is not a
complete inventory. A pre-R1 operation may still dispatch valid work until the
boundary closes. #62 requires a local closure acknowledgment with journal
high-water marks and every SendMarked/Accepted/unknown step; include sends paused
between marking and socket write. Retained operation plans bound the possible
effect set even if dispatch has not yet been reported centrally. Unexplained
boundary work, an omitted authority path or lost journal quarantines the affected
asset/domain; it does not become an empty manifest.

The central closure is not physical quiescence. Delivery records only transport
progress. Execution issues its successor fence only through #62 takeover and
external stale-writer fencing. A release cleanup operation acquires the same slot
after predecessor settlement or its explicit recovery-hold transfer, never an
independent release lock. Emergency isolation may interrupt unknown work only
through #62's journaled recovery protocol.

### Ordering with reuse and retirement

```text
customer install/session admission ----+---- R1 release closure
                                      |       |
            same gates/guard/slot -----+       +-- revoke and inventory sends
                                              +-- settle predecessor effects
                                              +-- clean and close cleanup effects
                                              +-- certificate + accounting proof
                                              +-- R2 Released + clear occupancy
                                              +-- provider R3, if applicable
                                              +-- fresh eligibility + new allocation
```

If an operation wins before R1 it is inventoried; if R1 wins it cannot commit.
An allocation cannot race release to occupied supply: it locks the same occupancy
and enrollment rows. R2 clears occupancy only after the release predicate below;
it leaves reuse eligibility Closed until any external R3 and fresh inventory
checks complete. The next allocator transaction still checks every #60 gate,
current provider guard, quota, trust and policy. It creates a new allocation ID
for a new Machine incarnation. Released MachineBinding never retargets.

ProviderGuard Closing prevents new acquisition and ordinary work, but permits
the retained release/recovery obligation branch. That branch validates cleanup
authority rather than requiring customer gates to be Open. Safety, exact identity,
cleanup-principal and restore gates still apply; this is not a blanket gate bypass.
A retirement/closure race therefore either retains the new obligation or rejects
it before effects. No release completion reopens a retired provider gate.

## Concrete release state machine

MachineAllocation, or the exact non-consumer AssetCustody, remains Releasing (or Quarantined) through the nonterminal
ReleaseCase phases. Phase changes use expected version, dependency vector and
accepted evidence sequence. The phase is a projection of obligations, not proof.

| ReleaseCase phase | Entry/exit predicate and permitted work |
| --- | --- |
| Closing | R1 committed. Only revoke, observe, stop and scoped recovery; enumerate all boundary sends. Exit after central/boundary closure inventories reconcile and required authority paths are revoked. |
| Settling | #62 settles all predecessor effects, remote queues, RAM installers and provider writers. No cleaning yet. Exit with accepted no-late-effect/host-stop evidence for the exact manifest and #61 customer/provisioning disconnect barriers. |
| Cleaning | Acquire shared execution slot for the pinned cleanup plan in an isolated service environment. Execute exact disk, credential, management and firmware obligations. Any partial or uncertain step remains retained. |
| Verifying | Close cleanup dispatch, settle cleanup itself, remove service boot/media/network access and temporary credentials, verify the entire assurance vector. May schedule a new explicitly authorized repair plan through Cleaning only after the previous attempt is settled; never mutate/replay a partial step in place. |
| EffectsClosed | Accepted ReleaseCertificate and irreversible rejection of old consumer and cleanup effects. No hardware mutation remains authorized. All network dispositions are acknowledged. Only accounting, provider finalization and observation remain. |
| QuotaSettling | Durable terminal-effects decision and same-key quota release outbox; inspect lost replies. Unknown quota disposition blocks R2. |
| AccountingSettling | Non-consumer subjects validate every real obligation or positive non-applicability proof; no invented hold/acknowledgment. Unknown blocks R2. |
| Released / Settled | R2 atomically records terminal subject disposition and clears only its occupancy with required accounting proof, certificate and dependency checks. Existing MachineBinding and all authority tombstones remain. External finalization may still block eligibility/retirement. |
| Quarantined | Any missing, contradictory, unqualified or irrecoverable obligation. Record blocked phase, full custody and incident. Only inspect/fence/qualified repair. Return to the blocked phase only after reviewed new proof; never reopen customer admission. |

```text
Closing -> Settling -> Cleaning -> Verifying -> EffectsClosed -> Accounting
   |           |          |           |              |               |
   +-----------+----------+-----------+--------------+---------------+
                          | any failed/unknown proof                  |
                          v                                          v
                     Quarantined                           R2 Released/Settled
                    (occupied hold)                              (tombstone)
                          |                                           |
              evidence-based recovery                      external R3 if needed
              to recorded blocked phase                    + fresh reuse gates
```

Accounting is QuotaSettling for Allocation and AccountingSettling otherwise.
Preparation and physical-maintenance clearance may omit Cleaning only when their retained qualified policy
and verified postconditions require no additional cleanup effects, never because
an operation happened to return success.

Each CleanupObligation progresses Required → Admitted → Running → EvidencePending
→ Satisfied; possible effects with lost evidence become OutcomeUnknown, never
Satisfied. Failed is not waived: it requires a qualified successor remedy or
continued quarantine. NotApplicable requires a prequalified policy reason tied
to the exact hardware profile, not an operator checkbox. Requirements may be
strengthened after a vulnerability/qualification revocation; pin a successor
policy and retain predecessor obligations. Never lower assurance after failure
just to complete a release. R1 cannot be cancelled into customer use.

If an accepted certificate is invalidated before R2, quarantine and supersede it
explicitly. A new remedial cleanup plan may enter Cleaning only under new scoped
authority after #62 settles/fences all prior work; old authority remains permanently
closed. Preserve the old certificate and any already-acknowledged quota disposition.
EffectsClosed therefore closes that exact effect set, not the possibility of an
explicitly admitted later safety remedy. If invalidation is discovered after R2,
never reopen the terminal Allocation or act through its cleanup credential. Close
current asset eligibility, identify any successor occupancy, and open a new safety
incident under current authority. Do not erase a successor customer's disks using
the predecessor's release authorization.

## Revocation, execution and network barriers

Closing inventories and revokes the whole allocation access graph, not just the
public session: local connector proofs, relay routes/streams, gateway sessions,
BMC tasks, console channels, virtual media, one-time boot grants, DHCP/relay/RA
authorization, PXE HTTP/TFTP/proxy paths, artifact URLs and delegated fetch
capabilities. Owners return identity-bound effective revocation evidence and
permanent reject-old-authority barriers; finite TTLs alone are insufficient.
Already downloaded artifacts or an in-memory installer require host-stop proof.
Customer-controlled external PXE origins cannot be erased by Fleet; revoke their
reachability/authority to this asset and avoid claiming remote deletion.

Accept #62 settlement of every inventoried operation, not just its last reported
step. Require no unresolved possible send, persistent BMC/RAID/storage task or
other provider writer, independently verified host stop under the qualified
hardware procedure, and fencing of paused/restored brokers. A terminal failed
installation with PartialStateRequiresRecovery transfers its recovery hold to
release; it never proves the disk or predecessor OS intact. Use a release-specific
stop certificate, not Installation Succeeded: release need not boot the old OS
to manufacture an installation-success claim.

Accept #61 DisconnectAcknowledgment for exact binding/claim/assignment
incarnations, ports, routing domains, realization revision, old/successor fences,
all path classes and no-reuse barrier. It must cover queued writers, active
streams, DHCP and boot authorization, flows/routes/translations or qualified
upstream isolation, and dataplane verification. A switch desired-state change
is not a barrier. An upstream fence can prove traffic isolation but not host
stop, sanitization, or physical cable clearance.

The cleaner may need a **new**, explicitly admitted service-only network/boot
binding after old tenant/provisioning paths are fenced. Its exact capability,
identity, image and temporary credentials join the obligation manifest. It must
not reuse or reopen the old customer binding. Before EffectsClosed, settle its
execution and revoke its paths too. Issue #61 DisposeClaims only with required
settlement: Delete returns exact assignments; Retain detaches into owner custody.
Shared Networks/VPCs remain untouched. Retain is not permission for future
attachment; new binding incarnation and permission are mandatory. Provider address
facts require provider disposition, not a Datum claim deletion. Missing response
uses the same request's durable receipt, never a new detach by IP address.

## Sanitization and restoration assurance

### Qualification boundary

The retained contract selects a versioned hardware-specific CleanupProfile,
recipe/image digests, coverage manifest, trusted execution path, required witness
types and postconditions. Qualification is by chassis/controller/media/firmware
combination, not merely vendor or erase-command support. Initial automatic scope
is verified local attached storage on qualified hardware. SAN, NVMe-oF, removable
or inaccessible storage, unenumerated persistent devices, and autonomous external
jobs are unsupported until their owners provide equivalent inventory, stop and
data-treatment evidence. Unsupported supply is not offered for automatic reuse.
If discovered after allocation, retain quarantine and arrange qualified physical
service; never report an unqualified erase assurance.

Do not claim all physical bits are erased, standards certification, or protection
against arbitrary compromised firmware. The accepted vector states what was
tested, coverage exclusions, threat assumptions and each independently satisfied
predicate. Customer PXE completion contributes no sanitization evidence. A
trusted cleaner cannot by itself prove an opaque controller or firmware honest.

| Predicate | Concrete required evidence | Insufficient evidence |
| --- | --- | --- |
| AssetAndMediaBound | Verified enrollment/chassis/BMC continuity, current component incarnations, controller-to-physical disk/namespace mapping, retained pre-allocation and current media manifests | Hostname, BMC IP, serial alone, guessed `/dev/sda`, or only mounted volumes |
| DataTreatmentQualified | Recipe-specific completion and independent verification for every required persistent component, exact firmware/capacity/method, start/end state, errors and scope | Generic backend success, quick format, filesystem deletion or sampled zeroes as proof of inaccessible regions |
| FirmwareBaselineQualified | Approved boot/UEFI/BMC/controller/NIC firmware and settings manifest; authenticated recovery/reflash and verification procedure where required; disabled unwanted boot/virtual-media paths | Disk cleaning, resetting defaults, or a firmware version string alone |
| ManagementStateRestored | Exact BMC accounts/roles, certificate trust, management network, boot order, virtual media, event/task queues and platform security settings restored to the provider baseline | Reachability, power-off, factory reset without credential/trust repair |
| CredentialsInvalidated | All allocation/service-cleaning tokens, sessions, bootstrap secrets, virtual-media credentials and exposed management credentials revoked at their enforcement owners; verification of old-access denial | Deleting a Kubernetes Secret or waiting for token expiry |
| NoResidualExecution | Accepted predecessor and cleanup settlement, future command queues accounted for, old broker/provider paths fenced | Callback, TTL, power observation alone, or terminated worker Pod |
| NetworkDisposed | Customer and cleanup disconnect barriers and exact Delete/Retain/provider address dispositions | Unreachable host or missing NetworkInterfaceClaim |

### Exact media and data-treatment procedures

Capture media before customer access and re-enumerate through a qualified trusted
path before cleaning. Include all physical drives behind RAID, namespaces and
their underlying media, non-OS/boot drives, controller persistent cache, BMC
virtual-media storage and other qualified customer-writable persistent surfaces.
Keep serial/WWN plus enrollment/component evidence; serials are not cryptographic
identity. No wildcard disk selector and no target substitution are permitted.

For each media class select a qualified sanitize/erase procedure that covers the
profile's advertised regions and remapping behavior. The profile must explain
completion detection, power-loss behavior, controller caches and inaccessible
regions. An overwrite procedure may assert only its verified addressable coverage;
it is not evidence about remapped/unaddressable areas and cannot satisfy a profile
requiring those areas. A cryptographic-erase profile is allowed only with verified
encryption from before customer writes, full required media coverage, unique keys,
all key/escrow copies accounted for, and qualified key invalidation semantics.
Turning encryption on during release is not that evidence. Otherwise require
another qualified method or quarantine.

A missing original disk creates an unresolved data-bearing-component obligation,
even if a clean replacement is present. Retain both manifests and component
custody; replacing the disk cannot settle the missing one. A qualified physical
destruction remedy may satisfy its data obligation only with exact component
chain-of-custody, independent witnesses and verified destruction scope. The
destroyed component is permanently retired. Whole-asset destruction is not
automatic release: settle remote authority, network and quota as well, with a
permanent no-reuse enrollment gate. Irretrievably lost media/evidence stays
quarantined; a business write-off is not sanitization or technical settlement.

After firmware or BMC repair, inventory must re-establish identity and baseline
trust; do not silently change retained effect targets to the replacement endpoint.
If chassis continuity is unprovable, treat it as new hardware and retain old
custody. A compromised or unverifiable persistent execution surface needs a
qualified recovery/replacement path, otherwise no reuse. A disk erase cannot
restore trust in that surface.

### Credential custody during restoration

Retain required service recovery secrets in protected key storage until their
obligations settle, independently of tenant Secret lifetime. Delete customer
access before cleaning; preserve only the minimum service recovery path. Rotate
allocation-specific or possibly exposed BMC/provider credentials, invalidate old
accounts/sessions/certificates at the device or issuer, and verify both old-access
denial and new service access. A credential shared across assets needs an owner
coordinated rotation covering every dependent asset, not unilateral revocation
that strands other allocations. Prefer scoped credentials as a qualification
prerequisite. Logs and certificates retain versions/digests, never raw secrets.

A lost rotation response is unknown: recover the same step through protected
old/new key custody and qualified device inspection. Do not test by blindly
repeating reset/rotation or discard the only working recovery credential. Revoke
the cleaner's temporary credentials after its execution stops; destroy retained
secret-bearing payloads only after all recovery references are settled and owner
retention rules permit. Non-secret replay tombstones survive that destruction.

## Evidence binding and recovery

Each accepted envelope binds ReleaseCase, Allocation, Machine/Project UID, asset,
enrollment and component incarnations, provider reservation/generation (if any),
cell identity, operation/step/input digest, CleanupProfile revision, exact network
bindings, issuer identity/key revision, execution fence and journal sequence.
It includes management/cell/provider/owner restore lineages, dependency gate
epochs/revisions, observation start/end, clock uncertainty, verification time,
valid-until for mutable claims and independent proof references. Receipts are
append-only; conflicting evidence creates an incident, not last-write-wins.

Management validates against trusted ledger time and qualification-specific finite
freshness limits. Each profile must supply numerical limits and allowable clock
uncertainty before enablement; unset limits deny enablement. A current-state claim
must be observed after the final relevant effect/fence and remain fresh at commit.
A causal historical completion may arrive late only with authenticated scope and
ordering; it cannot substitute for fresh current identity or no-late-writer proof.
Durable owner barriers can outlive observation freshness only if their protected
tombstones/high-water marks remain valid in the current lineage. Certificate
acceptance and R2 recheck every relevant gate and policy revocation under locks.
Expiry or invalidation requires fresh evidence, not timestamp renewal.
The expected vector distinguishes gates intentionally Closed/Retired by release
or project deletion from cleanup-safety gates that must remain valid. Rechecking
does not reopen customer gates or demand their old Open state. Any newer safety
closure or unknown dependency blocks the commit until explicitly reconciled.

Lost cleaning responses use #62's original operation/step and provider task identity
for query and evidence reconciliation. A missing task, generic success or NotFound
is not no-effect proof. Do not replay an uncertain destructive step. If qualification
can independently establish completion and no future writer, accept that narrowly
scoped proof; otherwise fence/stop, retain partial-state custody, and admit a new
explicit remedy only after predecessor settlement. Completing a remedy cannot
rewrite the original unknown history.

Restores follow #60/#62's independent monotonic recovery authority. Before a
restored ledger, cell, broker, provider or network writer activates: close the
affected gates, fence old writers, advance the appropriate restoration epoch,
reconcile the union of current remote/independent journals and tombstones, then
revalidate custody and evidence. An epoch is not a hardware fence. If the possible
asset set is incomplete, quarantine the enclosing failure domain. If the independent
epoch authority is also lost, no automatic activation until a separately verified
lineage and external fences exist. A pre-release backup cannot resurrect customer
access or erase a newer cleanup obligation. Historical erase evidence alone does
not prove that restored stale writers could not write afterward.

## Settlement, accounting and reuse

The release predicate is the conjunction of exact identity, reconciled effect
inventory, predecessor and cleaner settlement, every required assurance predicate,
effective session/boot/credential revocation, network disposition, durable replay
barriers, valid evidence and no unresolved physical clearance obligation required
by this release. Active accepted placement reservations are not clearance failures
and are not freed by R2; removal/movement interests remain inventory-owned. Persist its
ReleaseCertificate and terminal-effects decision before quota release. The
certificate does not itself clear occupancy.

For Allocation, QuotaSettling uses #60's original non-expiring charged hold, same request identity,
monotonic custody revision and queryable terminal decision. Unknown/lost replies
retain the local hold and occupancy. A delayed Bind cannot resurrect a released
hold. Once the authenticated Released acknowledgment is retained, R2 locks the
same #60 rows and rechecks the predicate, hold disposition and gate vector; it
atomically closes MachineAllocation, clears Occupancy and writes terminal
tombstones/finalization outboxes. A lost R2 reply is resolved by its decision ID,
not a new release. If evidence invalidates after quota acknowledgment but before
R2, keep occupancy/quarantine despite the already-settled hold; raise an accounting
incident and do not invent a second charge or pretend the acknowledgment vanished.
This is explicitly accounted non-reusable custody, not free supply.

For #64 providers, LockedClean retains the provider lock and irreversible
no-old-effects barrier while Metal settles quota/R2. R2 emits the exact R3
finalize-release request; provider verifies the signed decision, reservation and
generation before tombstoning/unlocking. Recover a lost R3 receipt with the same
identity. Keep the supply reuse gate Closed and ProviderGuard obligation present
until acknowledgment. The provider lock also rejects new Reserve before R3.
Neither a late cleanup nor a late finalization may address a successor by asset
name alone. New supply reservation has a new UID/generation and fresh trust.

Quota capacity, customer billing and physical availability are different:

- #60 charged escrow counts until its explicit settlement acknowledgment, not
  until an invoice stops or a customer deletes a Machine.
- Billing may record service cessation/refund under its own policy; it consumes
  release progress but cannot release holds, erase obligations or mark disks safe.
- Occupancy clearing follows R2, and reuse additionally needs fresh inventory,
  provider Open/eligibility, intact replay barriers and a new #60 admission.
- Destroyed/retired assets can settle their allocation only with complete proof
  and a permanent no-reuse gate; they never become allocatable capacity.

Keep permanent compact tombstones for Allocation/MachineBinding, release and
operation IDs, provider reservation generations, canonical asset/component
retirement and reject-old fences. Owners retain their network/quota tombstones.
Unsettled evidence/inputs have no time-based GC. Full terminal payload retention
can be policy-bound only after recovery, legal and replay obligations settle;
compact replay rejection is permanent in this initial protocol. Storage pressure
blocks admission rather than deletes custody. No force-free or TTL-based release.

## Deletion, provider loss and cleanup authority

Before admission, #60's lifecycle interlock must protect individual namespaces,
input artifacts, credentials, quota anchors and control planes, not merely a root
Project finalizer. Consumer deletion closes and retains cleanup outside its API
lifetime. Provider deletion/retirement first closes its gate/ProviderGuard, then
enumerates attempts, allocations, preparations, operations, grants, obligations,
holds and outboxes from the ledger. It cannot rely on public Machine listings.
Release still needs destructive authority; retirement alone cannot wipe tenants.

Retirement completion requires all allocations Released, all holds terminal and
acknowledged, all provider finalizations complete, remote authority fenced, and
every outbox delivered or durably rejected with replay proof. Retain the final
guard sequence and custody manifest outside the purged API. Unknown/quarantined
obligations block purge indefinitely. Same-project provider/consumer uses the
same rules. Forced purge is disaster recovery, not a supported force-free path.

Cleanup execution uses a narrowly scoped service authority retained at allocation
or explicitly established by the adopted/preparation contract above,
and instantiated per obligation after R1. Permission revocation or suspension of
the original customer does not revoke the service's duty to settle previously
authorized effects. It permits only observe, stop/fence, revoke, qualified clean,
restore the pinned provider baseline, and settle exact claims/holds. Each new
dispatch still needs valid service identity, finite authority and all exact-input,
target-safety and restoration checks. It cannot expand an installation plan,
read arbitrary customer data, open a console for the former tenant, change the
asset, purchase/cancel subscriptions or acquire capacity. Renewing the capability
revalidates the existing obligation; it does not inherit expired customer rights.
Service-key compromise closes the cleanup issuer gate; independent authorized
recovery may reissue narrow authority only after fencing the compromised path.

External provider outage, refusal or insolvency leaves independent Metal custody,
quota state, original reservation, data obligations and evidence intact. Emergency
network/power isolation uses qualified owner paths and proves only its actual
boundary. Contracts must provide recovery access, key/input retention, witness
availability and protection against unilateral timed reclamation. Missing access
is a blocker, not permission to assert sanitization or transfer the allocation.

Cross-provider, cross-domain and legal custody transfer of unsettled allocations
is **disabled**, matching #60/#64. Operator approval cannot enable it. #62's
two-person transfer of a narrowly retained isolation task to a durable recovery
executor is allowed: same asset/allocation/guard and obligations, recorded old/new
executor authority, exact scope, independent journal and old-writer fences. It
does not transfer occupancy, responsibility, quota, ownership or reusable supply.
A future broader transfer requires a separate reviewed protocol and owner
agreements, not an exception checkbox here.

## Human recovery and irrecoverable cases

Provide protected inspection of case version, phase, obligation graph, exact
targets, original sends, boundary receipts, evidence rejection and quota/provider
decisions. No operational CLI is implemented by this proposal. An operator action
records incident ID, actor, reason, expected versions, proposed exact steps and
evidence. Two authorized operators approve physical isolation/restoration, epoch
reset and the limited executor transfer described above, as required by #62.

Permitted actions are observe, fence/isolate, recover a known receipt, qualified
physical service/destruction, or resume a settled checkpoint with new exact
authority. Technicians verify chassis/component/outlet mapping independently,
document time and witnesses, and reconcile inventory before effect authorization.
A photograph can support identity/chain-of-custody, not prove erasure. Unplugging
a cable proves neither host stop nor disk cleaning. A power-cut receipt needs
queue/storage follow-up before power restoration. A vendor statement without
qualified coverage is insufficient. No override may mark an unknown predicate
Satisfied. Irrecoverable evidence retains a named recovery owner and quarantined
custody indefinitely; alerts and business write-offs do not change that invariant.

## Failure and race matrix

| Fault or race | Required result / recovery boundary |
| --- | --- |
| R1 versus install, power, console or PXE grant admission | One ledger order; winner before R1 appears in manifest, loser cannot commit. |
| Pre-R1 outbox delivered after boundary closure | Exact old authority rejected; retained send-marker inventory accounts for any earlier acceptance. |
| Broker paused after SendMarked; TTL expires | Unknown retained; externally fence broker and settle possible hardware command, never assume expiry stopped it. |
| PXE revoked, installer still writes in RAM | Network receipt accepted only for network; host execution-stop and storage-task proof still required. |
| BMC reset queued across cold power loss | Independently inhibit host until qualified persistent queue clearance; otherwise onsite quarantine. |
| Console/relay closed centrally but edge partitioned | Closing incomplete until effective stream/access cutoff and old-path fence are proved. |
| Cleaner succeeds; response and task URI lost | Same-step recovery or independent qualified completion proof; no blind erase replay. |
| Cleaning failed halfway; alternate driver offered | No driver takeover until predecessor settlement; new explicit remedy, original history retained. |
| Disk swapped/missing or hidden RAID member | Manifest mismatch and data obligation; replacement's erase cannot satisfy missing original media. |
| Firmware compromised; disks erased | Data predicate alone insufficient; qualified firmware recovery or unavailable quarantine. |
| BMC credential rotation response lost | Preserve protected recovery key versions, inspect exact step; no secret deletion or reset loop. |
| Cleanup service network left active after wipe | Verifying blocked; revoke and settle cleaner paths/credentials before EffectsClosed. |
| Stale switch worker reopens old binding | Owner barrier invalid; close reuse gate and incident, no accepted disconnect certificate. |
| Delete/Retain receipt lost; IP reused elsewhere | Query exact request/assignment; stale detach cannot target successor by address. |
| Quota release response lost or late Bind arrives | Retain occupancy; same-key inspection and terminal revision/tombstone prevent resurrection. |
| Evidence revoked after quota acknowledgment before R2 | Occupied quarantine with explicit settled-quota discrepancy; no free capacity or fabricated charge. |
| R2 committed but response lost | Inspect durable decision; never clear another allocation or repeat with new identity. |
| External R3 lost; successor requests same supply | Reuse gate and provider local lock block until terminal receipt recovered; late old cleanup rejected. |
| Project/namespace purged, provider unreachable | Independent protected custody survives; block retirement/reuse, recover exact prior identities. |
| Catalog or offering-assignment withdrawal alone | Block new acquisition; preserve existing allocations’ retained runtime capabilities subject to current actor/input rights and separate safety gates; no runtime closure or ReleaseCase. |
| Explicit runtime suspension or provider retirement alone | Close applicable ordinary admission gates; no implicit destructive-release authorization. |
| Restore one journal before a newer wipe/reallocation | Independent epoch interlock, union reconciliation and old-writer fencing; no replay into successor. |
| Provider unilaterally reclaims despite contract | Qualification violation; close domain gates, incident and unknown custody, not a fabricated Released receipt. |
| Freshness deadline, clock uncertainty or disk mapping changes at R2 | Recheck under ledger locks; refresh evidence or quarantine. |
| Operator requests force-free or business write-off | Reject technical state change; retain custody and recovery ownership. |

## Qualification tests and operations

The following are **required future tests**, not passing implementation tests.
Synthetic tests can establish transaction/serialization behavior, not physical
erase, BMC trust, dataplane isolation or supplier truthfulness.

Exercise catalog and offering-assignment withdrawal with an existing allocation:
new acquisition must fail, while a runtime action within retained capabilities
with current actor/input permission must still admit. Then independently close a
runtime suspension gate and assert that action is rejected. Neither withdrawal nor
suspension alone may create a ReleaseCase or authorize destructive cleanup.

1. Model-check/transaction-test R1, operation hook, gate invalidation, retirement,
   quota acknowledgment and R2/new admission races; inject lost commit replies.
   Assert one occupied asset, one immutable MachineBinding and no untracked grant.
2. Crash each obligation before journal, after send marker, after device acceptance,
   after evidence persistence and before projection. Duplicate/reorder every
   outbox/result; changed digest must conflict and late results cannot regress stop.
3. Partition each gateway/relay/boot replica, cell and provider. Test active
   consoles, cached PXE, stale broker resurrection and downloaded RAM installer.
   Prove stop and revocation separately; record unsupported boundaries explicitly.
4. On each supported physical profile, test sanitize scope, RAID/namespace/cache
   coverage, remapped-region limitations, missing/swap media, power interruption,
   persistent queues and independent completion verification. Establish exact
   method-specific claims; do not generalize from a simulator or one disk model.
5. Exercise firmware/BMC recovery, trust re-enrollment, credential rotation loss,
   old-access denial, controller persistent state and cold-start auto-power rules.
   Verify no customer or cleaner path survives final settlement.
6. Exercise #61 disconnect/DisposeClaims with stale switch writers, live flows,
   wrong assignment incarnation, retained addresses, provider addresses and lost
   owner receipts. No address return without the owner's no-reuse barrier.
7. Force consumer/project/namespace deletion and planned provider retirement at
   each phase, including provider=consumer. Prove protected key/input/quota custody
   remains and deletion cannot revoke the last required cleanup authority.
8. Test quota hold/Bind/Release duplicates, unknown responses, certificate
   invalidation between acknowledgment and R2, R2 response loss and external R3
   partitions. Billing refunds must not alter occupancy or escrow transitions.
9. Restore management, one cell, provider and networking journals independently;
   roll back the epoch authority in a disaster exercise. Missing tombstones or
   incomplete affected-asset inventory must disable the relevant domain.
10. Test forged/wrong-cell evidence, replayed old component proof, expired freshness,
    clock skew and policy revocation. Verify two-person recovery approvals cannot
    override missing proof, transfer custody, or enable arbitrary tenant effects.
11. Test terminal tombstone retention under storage pressure, new Machine name
    reuse, new Allocation/provider generation, delayed old cleanup and destruction
    remedies. Destroyed or unresolved assets must never publish as available.
12. Start with observational adopted occupancy and no Allocation/hold/trusted
    Device. Assert zero effects. Resolve identity read-only, obtain exact prior-
    custodian/provider approvals and current service authority, settle old paths,
    execute qualified cleanup, close the cleaner, obtain positive accounting
    NotApplicable provenance and commit R2. Fresh Preparation and inventory
    evaluation, not R2 alone, enable the first consumer allocation.
13. Repeat with real discovered holds/external obligations, lost acknowledgments,
    unknown media history, overlapping allocation, revoked approval, or an empty
    restored accounting ledger. No dummy hold or invented quota acknowledgment;
    missing identity/authority/evidence or real accounting blocks destructive
    admission or R2 as appropriate. Narrowing a domain never clears another case.
14. Race Preparation/adopted R1/R2 with allocation, maintenance, adoption and
    retirement under the full lock order, including physical reservations and
    operation/release children. Restore before resolution or after cleanup send
    but before receipt; retain union custody, no blind wipe replay or stale proof.
15. Enroll and commission without consumer objects. Close/cancel at every boundary;
    verify no-late-effect/network disposal and release-owned clearance before
    eligibility. Replay the original service envelope after new allocation:
    subject/fence/tombstone checks must reject every old effect.
16. Move a witnessed passive panel through PhysicalMaintenance with no compute,
    boot or media objects. Require field cessation, exact physical disposition,
    dependency-owner evidence and positive accounting non-applicability before
    R2; destination reservations remain and inventory revalidation is separate.
17. Lose permit/start/completion acknowledgment; restore before maintenance L1;
    race R1/R2 with retirement, another transition and idle-server allocation.
    Retain Unknown and both positions until reconciled; no late crew action or
    old permit can reach a successor. Safely stopped failure stays ineligible.
18. Revoke a dependent connection fence or find an unapproved active custodian.
    Block field progression/clearance as appropriate. Target maintenance R2
    neither clears dependent occupancy nor authorizes reconnect, reset or wipe.

Roll out observation-only obligation inventories first, with zero provisioning
effects from adoption. Then qualify one owned-metal profile on isolated lab
hardware, complete standalone install/release/new-allocation cycles, and expand
only with recorded owner approvals and physical qualification reports. External
providers remain disabled without #64 qualification and emergency custody agreements.
Disablement closes admissions but retains recovery controllers, journals and
authority; rollback cannot delete new obligations or weaken barriers.

Expose phase/obligation age and counts, unknown sends, rejected/stale evidence,
manifest mismatches, revoked-but-live sessions, cleanup coverage failures, firmware
and credential restoration failures, network barrier/disposition lag, quota
discrepancies, R3 lag, retirement blockers and restoration-epoch violations.
Page immediately on overlapping custody, stale effects after a barrier, missing
protected records, or accepted evidence from a wrong incarnation. Age alerts
route to a named recovery owner; they never expire obligations. Use bounded metric
labels; keep IDs and detailed manifests in access-controlled audit traces.
Customer status exposes phase, safe blocker category and assurance summary, not
provider credentials, physical topology or sensitive tenant data. Provider/service
operators can inspect the retained proof graph according to their scopes.

Measure release latency by qualified profile and phase, excluding no unknowns
from safety reporting. Numeric latency objectives follow lab data; no bounded
completion promise exists for irrecoverable physical evidence or provider outage.
Proposed safety SLO: zero premature physical reuse; this is an objective, not a
measured production result. Backups must retain unsettled evidence and key versions
together and demonstrate independently fenced restore before production.

## Prerequisites, alternatives and review status

| Owner | Unresolved agreement / implementation prerequisite |
| --- | --- |
| Allocation/execution | Same-ledger operation/grant hooks, lock order and release child rows; serializable durable commits; #62 no-late-effect and partial-state transfers. |
| Networking/IPAM | Effective all-path fences, durable binding/assignment tombstones, service-cleaner isolation and per-claim dispositions. |
| Inventory/hardware | Exact trusted component coverage, qualification freshness values, firmware trust/restoration and physical stop/sanitize witness procedures. |
| Milo/IAM/quota | Close-before-change deletion/suspension, dependency-level purge interlocks, independent cleanup authority and non-expiring escrow settlement semantics. |
| Providers | No autonomous reuse/expiry, LockedClean/R3 protocol, independent protected history, qualified evidence, emergency physical/key access and contractual custody obligations. |
| Storage/security/operations | Protected journal/blob/key retention, independent restore epochs, old-writer fencing, two-person recovery, staffing and incident ownership. |
| Billing/legal/data governance | Separate service-charge decisions, evidence retention, authorized destruction and supplier obligations; none may relax technical release predicates. |

Rejected alternatives: backend `clean=true` as proof; deleting the Allocation to
free capacity; TTL-based cleanup; a separate release allocator; automatically
replaying an ambiguous erase; general service credentials replacing exact cleanup
authority; and provider transfers to unblock deletion. Each loses either the
effect inventory, physical proof or globally ordered custody. The cost of the
selected design is potentially indefinite occupied/quarantined supply, protected
storage growth and demanding hardware/provider qualification. That cost is explicit
rather than hidden by optimistic release.

Implementation history: 2026-11-14, separate enhancement draft. This proposal adds
no runtime behavior and claims no hardware qualification, owner acceptance or
independent adversarial sign-off. Independent review remains required. Diagrams
are plain-text protocol diagrams, not generated SVGs. Documentation validation
and author self-review do not satisfy the qualification tests above.

[bmc]: https://github.com/datum-cloud/fleet/blob/f04e63ddd9d10cbf306daf3a0232961debd85e7f/docs/enhancements/secure-bmc-access.md
[management]: https://github.com/datum-cloud/fleet/blob/fa07c2236801a33a58efc914b555586c8e146cc6/docs/enhancements/fleet-management/README.md
[offerings]: https://github.com/datum-cloud/fleet/blob/a922ff1eb3275e26af6ae750a7515ddde245a754/docs/enhancements/profile-access/README.md
[lifecycle]: https://github.com/datum-cloud/fleet/blob/d7ff793b03711203dbfc9fac87d43922cc649b6f/docs/enhancements/machine-lifecycle/README.md
[pxe]: https://github.com/datum-cloud/fleet/blob/d7ff793b03711203dbfc9fac87d43922cc649b6f/docs/enhancements/pxe-installation/README.md
[allocation]: https://github.com/datum-cloud/fleet/blob/d072f3b5ded5d212e59ecc511c9282037d48c9f1/docs/enhancements/allocation-admission/README.md
[network]: https://github.com/datum-cloud/fleet/blob/04adf7013de42c40086502918dbfe05302ec00e8/docs/enhancements/network-handoff/README.md
[execution]: https://github.com/datum-cloud/fleet/blob/6a886b415edb5ba9c1295e4d88db9cd86bd51f0e/docs/enhancements/execution-recovery/README.md
[inventory]: https://github.com/datum-cloud/fleet/blob/d79495be33d7c0ab126b497dac6a5874924eee81/docs/enhancements/inventory-trust/README.md
[fulfillment]: https://github.com/datum-cloud/fleet/blob/8e21b043bc853d9d81f5aa54b0dc3c52465fc4a5/docs/enhancements/provider-fulfillment/README.md

## Alignment revision provenance

On 2026-11-14 the [complete-set integration review][custody-review] identified
missing commissioning and adopted-settlement joins. This `custody-subjects-v1`
alignment is proposed documentation, not runtime qualification or sign-off.
This proposal's contract-bearing revision is `96ca49da7d0753be47ea164b7008e90d3a81d2aa`.
The companion revisions below are immutable **contract-bearing snapshots**, not
claims about final PR heads or merge dependencies. Subsequent commits only refresh
pins and provenance. Read this aligned set together: older embedded companion
pins inside those snapshots describe their drafting baseline, not permission to
substitute an older contract for a join defined here. #66 records the final-head
complete-set review manifest. No circular exact-final-head dependency is required.
Any later normative change requires a new manifest and fresh complete-set review.

| Companion | Exact dependency revision |
| --- | --- |
| [#60 allocation-admission][custody-60] | `d072f3b5ded5d212e59ecc511c9282037d48c9f1` |
| [#61 network-handoff][custody-61] | `04adf7013de42c40086502918dbfe05302ec00e8` |
| [#62 execution-recovery][custody-62] | `6a886b415edb5ba9c1295e4d88db9cd86bd51f0e` |
| [#63 inventory-trust][custody-63] | `d79495be33d7c0ab126b497dac6a5874924eee81` |
| [#64 provider-fulfillment][custody-64] | `8e21b043bc853d9d81f5aa54b0dc3c52465fc4a5` |

[custody-review]: https://github.com/datum-cloud/fleet/pull/66#issuecomment-6883007682
[custody-60]: https://github.com/datum-cloud/fleet/blob/d072f3b5ded5d212e59ecc511c9282037d48c9f1/docs/enhancements/allocation-admission/README.md
[custody-61]: https://github.com/datum-cloud/fleet/blob/04adf7013de42c40086502918dbfe05302ec00e8/docs/enhancements/network-handoff/README.md
[custody-62]: https://github.com/datum-cloud/fleet/blob/6a886b415edb5ba9c1295e4d88db9cd86bd51f0e/docs/enhancements/execution-recovery/README.md
[custody-63]: https://github.com/datum-cloud/fleet/blob/d79495be33d7c0ab126b497dac6a5874924eee81/docs/enhancements/inventory-trust/README.md
[custody-64]: https://github.com/datum-cloud/fleet/blob/8e21b043bc853d9d81f5aa54b0dc3c52465fc4a5/docs/enhancements/provider-fulfillment/README.md
