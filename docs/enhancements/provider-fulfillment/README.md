---
status: provisional
stage: alpha
---

# Provider-neutral fulfillment protocol

## Summary

Metal owns admission, allocation, operation authority and validated public status.
Fleet is an optional fulfillment implementation. An external provider may offer a
pool without exposing Fleet Device objects or one MachineCapacity per asset. It
must nevertheless bind an exact verified asset, hold it exclusively, execute only
admitted operations, and retain cleanup and replay obligations. Pool availability
is discovery, never a reservation or an alternate Metal allocator.

This document selects a **proposed protocol**, not implemented APIs, tested
hardware guarantees or production readiness. It changes documentation only. The
repository has a synthetic implementation, described in the
[implementation status](../../development/README.md); this enhancement does not
claim that implementation implements this protocol. No compatibility layer for
earlier illustrative APIs is proposed. Scope is standalone Fleet/Metal only:
no nodes, clusters, procurement, guest agents or managed-service orchestration.

Select authenticated HTTPS with durable request identities and signed receipts,
not distributed transactions between providers and Metal. A provider reservation
is bounded preparation with **no consumer effects**. Its acceptance deadline is
finite, but deadline expiry alone cannot free uncertain custody. Metal's single
commit-or-abort decision resolves that uncertainty. After allocation commit,
reservation custody is non-expiring until the release owner's settlement.

## Sources and ownership

Read on 2026-11-14, starting a new branch from fetched `origin/main` at
`8ebec19513a0cbc8f219d3acae46f89ab471510b`. None of the following branches or files
is merged or copied. References are pinned, not references to moving PR heads.

| Source | Reviewed revision | Consumed contract |
| --- | --- | --- |
| [#41 BMC][bmc] | `f04e63ddd9d10cbf306daf3a0232961debd85e7f` | Allocation-scoped sessions, current permissions, shared effect domain. |
| [#51 management][management] | `fa07c2236801a33a58efc914b555586c8e146cc6` | Physical identities, management/hub/cell ownership and validated status. |
| [#58 offerings][offerings] | `a922ff1eb3275e26af6ae750a7515ddde245a754` | External providers, accepted capabilities, one-hop consumer catalog. |
| [#59 PXE][pxe] and [lifecycle][lifecycle] | `d7ff793b03711203dbfc9fac87d43922cc649b6f` | Exact Installation inputs, immutable driver and source, retained cleanup. |
| [#60 allocation admission][admission] | `2563b2a2f177a72d20c1d62265c89ff15688ba25` | Global ledger, quota escrow, authority gates, commit/abort and retirement. |
| [#61 network handoff][network] | `ed6edec5b056fd53343e11d58d979af495f1bd7c` | Network-owner realization, isolation and disconnect evidence. |
| [#62 execution recovery][execution] | `7fa823050a443c1f5b85c56e85ed04ebaa5bd2cb` | Exact operation journal, gate-ordered runtime admission/renewal, effect fencing, unknown outcomes and restore epochs. |

#60 and #61 are the architecturally signed-off inputs supplied for this task;
that does not confer sign-off on this draft. #62 is its fetched current head,
not an approved implementation. Read also the [architecture context](../../architecture/design.md),
[original provisioning proposal](../../proposals/fleet-management/bare-metal-provisioning.md),
and [source provenance](../../proposals/fleet-management/MIGRATION.md).
Earlier embedded references do not supersede the pins above.

| Owner | This protocol consumes; it does not replace |
| --- | --- |
| Allocation/admission #60 | Sole authoritative allocation transaction and decision query, global canonical occupancy, charged escrow, gates, guards and retirement interlocks. |
| Execution #62 and OS/PXE #59 | Operation admission/slot, step budgets, driver, input custody, replay safety, physical fencing and settlement predicates. |
| Networking #61 | Feasibility before selection, exact physical mapping, owner-executed network requests and teardown receipts; no provider permission to write Datum IPAM or routes. |
| Inventory trust | Canonical physical identity, anti-alias verification, mapping and safety eligibility; not serial-number trust invented here. |
| Release/reuse | Cleaning policy, assurance levels and settlement certificate; not a provider's boolean `released`. |
| Deployment | Independent management storage, signing/key recovery, provider connectivity, trust roots and isolation infrastructure. |

## Registration and trust

### Provider identity and qualification

The service registrar creates a protected registration containing an immutable
provider UID, provider Project UID, allocation-domain UID, registration generation,
verified legal/operator identity, approved endpoint origins, and independent
recovery contacts. A name, DNS ownership, marketplace entry or self-signed
capability document is insufficient. Recreating a project creates a different
provider identity; retries never substitute it.

Register exact mTLS workload identities and receipt-signing key IDs by role:
reservation service, execution service, observation signer and recovery service.
Record certificate issuers, allowed SAN identities, endpoint server names, key
validity/revocation history and approved successor-key procedures. A provider
administrator cannot sign Metal decisions, change canonical enrollment or edit
Metal custody. Tenant credentials never authenticate the provider transport.

An immutable, digest-addressed capability revision lists:

- Protocol/schema and adapter binary digests; supported operation/driver/source
  combinations and hardware/firmware/region coverage, not just operation names.
- Exact supply selection, all-sales-channel local exclusion, keyed query and
  cancellation tombstones, no-loss journals, terminal replay rejection, and the
  offline stop/fence mechanisms actually qualified.
- Supported network-owner handoffs, image assurance, finite installation steps,
  BMC profile subset, cleaning and retirement recovery obligations.
- Evidence issuers/predicates, independent verifier requirements, measured time
  uncertainty, finite deadlines, payload/rate limits and retained-input support.

Metal's qualification authority signs acceptance of that revision and scope after
the [qualification tests](#qualification-tests). Provider advertisements cannot
self-enable a capability. A capability revision unable to implement reservation,
query, cancellation, exclusive allocation, fenced operations and service-owned
cleanup is unsupported, not a lower-consistency fallback.

| Registration state | Authority and transition |
| --- | --- |
| Pending | Identity/qualification/lifecycle interlocks incomplete; observation only. |
| Active | Registrar opens #60 gates after verified enrollment and qualification. |
| Suspended | New affected work blocked through gates; keep custody and limited cleanup. Does not automatically power off customers. |
| Closing | #60 retirement guard blocks new acquisition and ordinary operations; service settlement remains possible. |
| Retired | Only after all obligations settle and replay history is retained; never a timeout transition. |

Key compromise or qualification regression closes the appropriate authority gate
before new admission. Historical evidence is assessed using compromise time and
provenance, not made trustworthy merely because a signature still verifies.
Uncertain key history quarantines affected transitions. Rotation retains old
verification material, but revoked keys cannot authorize new responses or effects.

### Transport and wire rules

Select HTTP/2 HTTPS using [TLS 1.3][tls], mutual certificate authentication, no
0-RTT, no redirects and an allowlisted origin per registration revision. Metal
initiates requests to the provider. Provider decision lookups use a separately
registered, scoped Metal endpoint; providers receive neither project API clients
nor hub credentials. All request/response bodies are bounded JSON; unknown fields,
duplicate keys, nonfinite numbers and unsupported versions are rejected. Counters
and identifiers are strings; timestamps are UTC with declared clock uncertainty.

Canonicalize the complete immutable payload using [JCS][jcs], hash with SHA-256,
and retain the canonical bytes. Use [JWS][jws] with a pinned Ed25519 key and a
fixed [EdDSA algorithm profile][eddsa] to sign payload digest, issuer, audience, key ID, protocol
version and validity. Do not accept algorithm negotiation or embedded new trust
roots. mTLS identifies the current caller; a signature preserves provenance after
delivery. Neither is a substitute for authorization or physical verification.
Signing services serialize only committed journal decisions, never pre-commit
intents. Receipt keys and journals require independent protected recovery.

The following paths are **proposed private contracts**, not existing endpoints:

| Interface | Semantics |
| --- | --- |
| `POST /fulfillment/v1/reservations:reserve` | Idempotently lock one exact eligible asset under a Metal attempt key. |
| `GET /fulfillment/v1/reservations/by-request/{key}` | Linearizable query including terminal tombstones; exact inputs digest returned. |
| `POST /fulfillment/v1/reservations/{id}:commit` | Associate the existing hold with the sole durable Metal Bound decision. No consumer effect. |
| `POST /fulfillment/v1/reservations:cancel` | Keyed AbortDecided receipt; works even if Reserve has not arrived. |
| `POST /fulfillment/v1/operations:submit` | Persist exact admitted #62 operation/step intent before effects. |
| `GET /fulfillment/v1/operations/{id}` | Query retained inputs digest, state, step inventory, evidence and result sequence. |
| `POST /fulfillment/v1/operations/{id}:cancel` | Commit local stop/tombstone for Metal's stop revision; not proof of physical settlement. |
| `POST /fulfillment/v1/operations/{id}:recover` | Explicit #62 recovery envelope, same operation/plan, fenced executor generation. |
| `POST /fulfillment/v1/reservations/{id}:finalize-release` | Install Released decision and old-incarnation tombstone before local unlock. |
| `POST /fulfillment/v1/decisions:resolve` on Metal | Provider-scoped linearizable attempt decision lookup; optional `abortIfUnbound` as specified below. |

`200` returns the durable current record, `202` means accepted but incomplete,
`409` means digest/state/precondition conflict, and `403` means denied authority.
`404` is only a missing query observation, never cancellation or no-effect proof.
`429` and `503` permit bounded backoff using the same key. An interrupted request
has Unknown outcome regardless of HTTP status. Responses include record revision,
digest, signer, state, decision identity and evidence references. Transport success
does not mean effects succeeded. Decision responses contain no cross-tenant data.

No webhook is authoritative. A provider may send an authenticated wakeup hint;
Metal then queries a durable exact result. Bounded retry and reconciliation scans
recover lost wakeups; polling never grants authority. Retain result sequences
until Metal acknowledges their durable ingestion. Paginated query cursors bind a
snapshot revision and cannot silently skip unsettled operations.

## Identity and exclusive supply

| Identity | Invariant |
| --- | --- |
| Canonical asset key | Inventory-trust-issued global physical identity, shared across all offerings, resellers and provider projects for the same asset. |
| Provider supply handle and generation | Provider-private exact asset route and incarnation; not an offering, pool, Machine or allocation. |
| Reservation request key | Metal-generated attempt/subrequest identity, persisted before send; never reused or reassigned. |
| Provider reservation UID | Provider-generated immutable ID, durable with local lock, discoverable by request key; unique within provider UID forever. |
| Metal allocation UID | Created only by #60 Bound transaction; immutable binding to one Machine lifetime, canonical asset and exact provider reservation. |
| Installation and operation UIDs | Distinct OS incarnation and admitted effect; no reservation implicitly creates either. |

Provider local allocation must serialize Reserve against every offering, reseller,
manual tool, other customer and non-Metal sales channel using the same asset lock.
It cannot acknowledge a reservation before persisting that lock. The pool must
resolve to an exact asset and mapping before returning Reserved; deferred choice,
substitution after commit or a product-order number without a physical binding
does not qualify. A hardware replacement requires release and a new allocation,
not mutation of a retained supply handle.

Metal validates the returned identity against inventory's protected enrollment
and eligibility evidence. External enrollment may be private and lazy, completed
while the reservation is held, but must precede global commit. It is not a Fleet
Device or public per-asset capacity requirement. One active enrollment across
provider projects and global occupancy uniqueness on the canonical key prevent
overlapping offerings from creating two Metal bindings. Offerings reference that
domain, not independent per-offering allocators. Pool counts remain advisory.

These guarantees compose: provider locks prevent reuse outside Metal; Metal's
canonical uniqueness prevents a second Metal route to the same hardware. Neither
discovers a lying hardware identity. Inventory must reject alias enrollments or
quarantine them; providers unwilling to supply verifiable canonical identity
cannot offer this contract. Cross-domain advertising and provider-to-provider
custody transfer are disabled initially. Imported occupied/unknown assets stay
occupied/unknown; registration and adoption confer no provisioning authority.

## Reservation, admission and expiry

### Boundary with non-consumer custody

The proposed `custody-subjects-v1` alignment in #60–#63/#65 adds Fleet-owned
Preparation and AdoptedCustody subjects in the same global asset conflict domain.
They are not consumer reservation attempts: no synthetic Machine, quota hold,
provider Reserve/Commit receipt or Installation is created for them. All global
selection/enrollment checks include their exact and unresolved-domain exclusions.
Use #60's total lock order including ProviderReservation children immediately
after attempts, physical-resource rows before custody/asset-slot rows, and
operation/release children afterward; no adapter-specific suffix order.

This external wire protocol remains Allocation-bound: its operation envelope and
R3 require the **real** M1/R2 reservation binding. Fleet-owned service custody uses
the Fleet selected-cell path, not `reservations:reserve` with fake consumer fields.
External stock must already be qualified before Reserve; read-only reservation
preparation never authorizes commissioning. Dispatch of non-consumer custody
effects to a non-Fleet supplier is unsupported by this version, not inferred from
an advertised cleanup capability. It would require explicitly qualified exact
custody/all-channel exclusion and finalization bindings, not omitting allocation
fields or synthesizing a quota acknowledgment. Imported external occupied stock
therefore stays held until its owner can supply the qualified applicable protocol;
this alignment does not silently grant new external maintenance/wipe APIs.

### Bounded preparation

Extend #60's Held attempt with a protected `ProviderReservation` subrecord and
outbox. It lives in the same service-owned ledger, outside consumer/provider
project garbage collection. The extension is not a second admission database.
Each exact Machine has one nonterminal attempt and each attempt exactly one
reservation request key once selection starts. There is no per-attempt key
replacement. Trying another provider/asset after a failed reservation requires
the old attempt Aborted with provider and quota acknowledgments, then #60's
freshly authorized next attempt for the still-unbound Machine. Registration
rate limits and an admission retry policy also bound these new attempts; an
automatic infinite sequence of new attempts cannot bypass preparation limits.

Before any Reserve, #60 establishes provider/consumer lifetime guards, resolves
the one-hop offering/profile and network feasibility, checks original caller and
input authority, and acquires protected charged quota escrow. Persist the exact
reserve key, provider/capability revision, constraints digest, attempt UID, guard
obligation, deadline and outbox before transmission. Failed/unknown quota means
no Reserve. Quota is not charged again at provider commit.

Initial proposed service limits are one asset per request, a maximum 120-second
decision-admission window measured from ledger preparation, one reserve key per
attempt, and no deadline renewal. Registration
must additionally set finite per-provider and per-project outstanding-hold caps,
request rates and evidence-size limits before activation. Those caps count
ExpiredUnresolved and all unknown requests, not only active leases. Admission
backpressures before journal/retention capacity is exhausted. Providers may
negotiate a shorter deadline, never extend the signed attempt deadline. Clock
uncertainty over the qualified finite bound denies preparation/commit.

Reserve may perform read-only inventory checks and a local bookkeeping lock.
It may not order new hardware, bill an irreversible procurement, power/reset,
erase, install, change boot, create customer credentials, expose console, attach
customer networks or activate shared identity. Already-qualified maintenance is
a separate owner-admitted preparation operation on nonallocatable inventory,
not an implicit side effect of Reserve. Reserve and Commit signatures have
different audiences from effect authority and are rejected by effect brokers.

The finite window bounds **permission to commit**, request fanout and normal
reservation time. It does not promise time-bounded release of uncertain physical
custody during a partition. Strict finite auto-release and guaranteed durable
allocation cannot both be promised across a lost commit response. This protocol
chooses fail-closed custody: a provider requiring unconditional timed reclamation
is unsupported. Unresolved holds consume the fixed caps and stop new admissions;
they cannot accumulate through retries or automatic renewal.

### Commit and abort ordering

1. Provider Reserve checks its exact finite Metal reserve authority, authenticated
   issuer/audience and deadline before creating a new hold; it cannot create a new
   hold from an expired replay. It atomically stores request digest, reservation UID, exact asset
   handle/generation, local exclusive lock, capability revision and deadline.
   **R1** is that durable local transaction. Identical key/input returns the same
   receipt, even after expiry; differing input conflicts. Provider journaling must
   preserve acknowledged writes across qualified failover and reject old writers.
2. Metal validates receipt provenance, exact constraints, identity enrollment,
   trusted mapping, capability qualification, deadline and local exclusion
   guarantee. Retain it, with a reservation eligibility gate, under the attempt.
   Receipt acceptance is not allocation. Provider changes that could invalidate
   a live receipt must participate through the scoped #60 gate adapter in
   close-before-change and resolve the decision; they cannot revoke a committed
   lock unilaterally. Unexpected physical failures close eligibility when observed
   and become failure evidence; no protocol can promise that hardware never fails.
3. In #60's serializable transaction and existing lock order, Metal rechecks every
   authority gate/guard, original caller decision validity/invalidation history,
   quota escrow, receipt and enrollment revision, deadline and Machine/asset
   uniqueness. Add the reservation subrecord to the locked attempt. Bind its UID
   once while atomically committing allocation, occupancy, permanent MachineBinding,
   quota ownership, AdmissionDecision and Commit/BindHold outboxes. **M1** is the
   sole global allocation linearization point. No remote calls occur in it.
4. Provider Commit verifies a signed Bound decision for the exact request digest,
   reservation UID, canonical asset, capability revision, allocation UID, quota
   custody reference, ledger lineage/restore epoch and decision sequence. Persist
   the association and non-expiring local custody at **R2**. Duplicate Commit
   returns it. Commit may arrive after the deadline only if M1 passed its deadline
   gate and won decision serialization, and the unresolved reservation is still
   held. It never performs boot, network,
   installation, power or consumer activation. Metal retains R2 before admitting
   any consumer effect against that provider binding.
5. A client cancel, selection failure, authority denial or deadline expiry invokes
   #60's attempt decision transaction. `abortIfUnbound` locks the same attempt and
   gates as M1: persist AbortDecided if no Bound exists, otherwise return Bound.
   A provider may request this only for its exact attempt at/after the deadline
   (using Metal trusted time), or under an explicit Metal cancellation request.
   Lookup without this flag returns Pending and cannot authorize unlock.
   **M0** is the durable abort decision; it and M1 are mutually exclusive.
6. Provider Cancel requires the signed M0 receipt for the exact key/digest and
   registration, and atomically installs the permanent tombstone before unlocking
   at **R0**. Cancel may precede Reserve: create the tombstone even without a local
   reservation. Metal settles unused quota only after verified R0, then marks the
   attempt Aborted after quota acknowledgment. No unresolved key is forgotten.

The deadline is a decision-admission cutoff, not a claim about the exact physical
WAL-flush instant. While holding the attempt lock, sample trusted ledger time
immediately before the final decision writes and retain `decisionCheckedAt`.
The upper bound of that time's uncertainty must be before `acceptBefore`; a
transaction missing this gate aborts. Require a qualified maximum five-second
decision transaction lifetime; retrying a timed-out transaction rechecks time.
M1 is still the durable commit, which may occur just after the cutoff for a
transaction that already passed the gate. Deadline resolution waits on the same
lock and cannot issue M0 if that transaction commits. Provider wall-clock expiry
merely asks for M0/M1; it never establishes their ordering or permits local reuse.
If database timeout/failover leaves outcome unknown, query the decision and keep
custody rather than promise a hard wall-clock release bound.

On lost Reserve response, query by original key or replay identical Reserve;
do not select another asset. Query NotFound cannot prove absence of an in-flight
Reserve. On lost M1 reply, query the authoritative ledger, not an asynchronous
replica. On lost Commit reply, query/replay exact Commit; occupancy and quota stay
bound. On lost Cancel reply, query/replay Cancel until the signed tombstone is
confirmed. A delay, missing object or provider restart never counts as canceled.

### Reservation state table

| Provider state | Legal input and successor | Metal custody implication |
| --- | --- | --- |
| Absent | Reserve → Reserved at R1; M0 Cancel → Cancelled at R0 | Missing query alone is Unknown. |
| Reserved | M1 Commit → Committed at R2; M0 Cancel → Cancelled at R0 | Holds exact asset, no consumer effect. |
| Reserved past deadline | → ExpiredUnresolved; query/abortIfUnbound only | Cannot accept new commit decisions or free by clock. |
| ExpiredUnresolved | Earlier M1 → Committed; M0 → Cancelled | Old deadline is not permission to substitute supply. |
| Committed | Exact operations; release owner → Closing | Non-expiring exclusive custody; Cancel-for-attempt conflicts. |
| Closing | Stop and cleanup; → LockedClean when release evidence qualifies | Ordinary effects closed, local lock retained. |
| LockedClean | Metal Released receipt → Released at R3 | No local reuse until allocator/ quota settlement. |
| Cancelled / Released | Queries/replays return terminal receipt | Commit/Reserve/old effects rejected forever. |
| Any contradictory or lost history | → Quarantined | No effects/reuse; independent recovery required. |

After M0, no later M1 can exist. After R0, Reserve or delayed Commit cannot
resurrect supply. A supposedly valid Bound receipt conflicting with a terminal
tombstone is an integrity incident; quarantine and reconcile signed decisions,
not last-write-wins. After M1, cancellation is release, never an abort that frees
quota while a provider could execute. Failure to deliver R2 leaves Bound with a
FulfillmentUnknown condition, not Ready and not automatically replaced.

## Operation authority and recovery

The accepted allocation pins capability document and qualification policy digests,
offering/profile incarnations and content, supply/asset/reservation identity,
provider route generation and cleanup policy. Publication withdrawal stops new
acquisitions, not retained capabilities or service cleanup. New consumer actions
require current action and exact input-use permissions and applicable explicit
safety restrictions; do not recheck offering assignments as runtime entitlement.
Retained capability is a contract, not a perpetual bearer credential.

Use #62's sole asset conflict slot under the exact Allocation subject and operation admission transaction.
The external adapter transports its committed envelope; it cannot independently
consume a predecessor, pick a driver or allocate another execution slot. One
Installation has one immutable ProviderAutomatic or CustomerRedfish driver.
CustomerRedfish sessions submit bounded steps into that same journal; they are
not a second provisioning channel. Provider APIs inaccessible to Metal tenants
must not offer an uncontrolled BMC or console bypass.

Both runtime admission and renewal lock #60's complete sorted authority-gate
vector, ProviderGuard and applicable binding/occupancy rows before the operation
slot. Recheck Open epochs, current action/input decision, deadline and recovery
lineage inside the transaction that commits authority and its outbox. A valid
cached receipt without those checks cannot authorize either admission or renewal.
Cleanup follows the dedicated retained-recovery lane, not a consumer-gate bypass
that can create new customer effects.

| Retained operation envelope | Required exact binding |
| --- | --- |
| Authority | Metal issuer, audience, caller/delegation decision, provider/consumer Project UIDs, allocation, Machine and reservation UIDs, decision/restore epoch. |
| Contract | Accepted offering/profile/capability/qualification digests and registration generation; selected action and finite allowed steps. |
| Incarnation | Installation/source/driver when required, predecessor, operation UID and sequence, step ID, executor generation and selected provider execution identity. |
| Inputs | Canonical request/plan digest, exact artifact and configuration bytes/digests, secret version handles, disk/NIC map, recipe and adapter digests, origin/network policy revisions. |
| Execution | Expected binding/result revisions, not-before/deadline, finite step budget, network-owner prerequisite receipts and exact cleanup obligation. |
| Translation | Retained provider-native request bytes/digest and upstream task key; not regenerated from a newer adapter on retry. |

Metal and provider verify availability of retained exact inputs before admission
and before dispatch respectively. Secret-bearing bytes use protected encrypted
custody with scoped, short-lived retrieval capabilities, never Git, public status
or provider-selected arbitrary URLs. Missing/deleted input denies dispatch;
recovery cannot fetch mutable replacements. Credential rotation is a separately
authorized rebinding to the same target and scope, not a changed input hidden in
an old digest. Shared addresses, hostnames, disks or credentials do not transfer
with a pool label: their owning services must prove predecessor fencing and issue
new exact-identity authority. No shared identity activates from Reserve or Commit.

Provider submit deduplicates `(providerUID, operationUID, stepID)` against the full
digest. Persist a durable inbox and closed/high-water markers before a worker can
act. Use #62's write-ahead send marker, upstream task correlation and physical
effect-boundary qualification; HTTP deduplication cannot make a lost BMC reset
response safe to retry. A repeated request returns the same remote task or
OutcomeUnknown, never another reset/install. Ordinary power, console mutations,
installation and cleanup share the same conflict/fencing domain.

Query distinguishes NotAccepted, Accepted, Executing, StopRequested,
OutcomeUnknown and locally Settled, with a complete in-flight step inventory.
Only Metal validates the terminal #62 outcome. A query of NotAccepted has the
same late-delivery hazard as Reserve; cancel must install a durable stop tombstone
even if the original submit has not arrived. Old Submit/Recover revisions lose
to that tombstone. Requesting cancellation closes new work, not downstream tasks.

| Provider operation observation | Allowed Metal interpretation |
| --- | --- |
| NotAccepted | Unknown delivery; query/retry exact submit or install a stop tombstone. |
| Accepted | Durable inbox; no inference that any physical step ran. |
| Executing | Correlated step progress within the admitted finite plan. |
| StopRequested | New local steps closed; still retain all possible in-flight effects. |
| OutcomeUnknown | Keep execution/recovery hold; observe or explicitly fence/recover, no automatic retry. |
| Settled | Candidate evidence only; management validates #62 success/failure/cancellation and remaining custody before its terminal commit. |

Terminal operation state never frees the allocation. Failed/cancelled partial
state retains #62's recovery hold; an empty operation slot cannot enable an
unqualified successor Installation.

New action admission and authority renewal require current Metal permissions.
During a partition, a provider may execute only already-admitted finite steps
within their valid scope/budget, subject to locally installed stop/fence records.
Expiry blocks new sends and grants, not already-running hardware or RAM installers.
Central revocation is not an instantaneous physical stop; #62 boundary receipts
prove its effective cutoff. Unknown time, restored stale state or missing current
authority denies new sends. Existing workload operation is not automatically
destroyed merely because control-plane observation became stale.

Recover requires an explicit Metal recovery decision, expected old revision,
same operation/plan/driver, successor executor generation, old-writer and queued
effect settlement, retained input proof and current recovery authority. A new
worker/endpoint/epoch is not itself a fence. Provider retirement, key loss or
adapter upgrade never allows blind redispatch. If physical fences cannot be
proved, keep OutcomeUnknown and release/recovery custody. Service cleanup uses
separately retained responsibility and freshly minted, narrow authority; it
survives consumer permission loss and project deletion without becoming an
unbounded credential. Cleanup itself is serialized work, not a competing writer.

## Observation, evidence and authority

### Delivery paths

```text
Fleet fulfillment:
Metal management admission and retained outbox
  -> management writes protected hub intent and exact cell targeting
  -> federation delivers to selected cell
  -> selected executor journals effects and writes LOCAL cell status
  -> Karmada reflects status to hub
  -> management validates and durably retains evidence
  -> management alone projects sanitized Metal status

External fulfillment:
same Metal admission and retained outbox
  -> qualified external HTTPS adapter and provider journal
  -> provider supplies signed, queryable evidence
  -> management validates and durably retains evidence
  -> same management status projector
```

External providers do not need Karmada, a Fleet cell or Fleet inventory CRDs.
The external adapter is a transport/evidence adapter, not a new authority.
Fleet cells receive no direct hub or consumer project credentials and cannot
use the external result ingress as an alternate status path. Both paths require
the same retained identities and validated evidence. Delivery acknowledgment,
provider Ready, Karmada reflection and Job completion are not readiness.

### Qualified attestations and independent checks

An evidence envelope binds issuer/key and qualification revision, provider and
execution identity, asset mapping, reservation/allocation/Installation/operation,
request/plan digest, step, restore epoch/fence, monotonic result revision, measured
event time, observed time, validity, clock uncertainty and predicate coverage.
Predicates are True/False/Unknown with method, scope, raw-proof digest and reason.
Distinguish desired state, provider observation, independently verified outcome
and irreversible settlement barrier. Timestamps cannot refresh old physical data.

| Claim | Provider may attest | Metal must verify independently of that assertion |
| --- | --- | --- |
| Exclusive reservation | Local lock transaction/lineage, asset and no competing local owner | Trusted canonical enrollment, global uniqueness, signature scope, receipt/attempt digest and local-exclusion qualification. Metal cannot remotely prove absence of a secret provider tenant. |
| Operation completion | Native task result, measured disk/boot/stop facts | Exact admitted operation, current lineage, full step inventory, driver/source assurance and required independent verifier receipts. A task success flag alone fails. |
| Network prepared/disconnected | Provider-owned uplink facts and its own domain isolation | #61 networking-owner receipts and required dataplane verification; no provider substitution for Datum IPAM/routing authority. |
| Installer stopped | Qualified hardware/boot-instance/physical isolation measurements | #62 stop/no-late-effect coverage, trusted asset/outlet mapping and independent witness where policy requires; expiry or network isolation alone is insufficient. |
| Clean/reusable | Device-specific sanitization and credential-revocation evidence | Release owner's assurance predicate set, all boundary barriers, quota disposition and allocator decision. Provider cannot clear global occupancy. |

Metal's verifier uses finite per-predicate freshness/skew limits from the pinned
qualification policy, verifies signature/audience/identity/revisions/digests,
rejects gaps/conflicts/replays, and joins receipts from their actual owners.
Cryptographic authenticity, semantic consistency and physical truth are separate
checks. Durable causal history may arrive late, but current-state readiness needs
fresh evidence after the relevant fence. Accepted evidence and derived decisions
are append-only; late old results do not regress terminal state or revive effects.

ManagedImage can claim QualifiedImage only for a qualified hardware/image/recipe
combination and its required evidence. DelegatedPXE claims DelegatedContract, not
verification of an opaque customer OS. Unsupported predicates remain Unknown;
do not label lower evidence as equivalent success to make a provider compatible.

### Threat model

Assume crash/restart, reordered/duplicated messages, partitions, stale restores,
compromised tenant credentials, faulty provider software and possible dishonest
provider evidence. TLS/signatures resist network substitution, not a malicious
physical operator. Provider-root access can secretly copy disks, mislabel hardware
or bypass its own locks. Neither SQL uniqueness nor a signed claim proves that
such activity did not occur. Initial admission therefore requires an explicitly
approved provider trust boundary, audit rights, verified identity mapping and
qualified independent witnesses for safety-critical transitions. Providers that
cannot meet that trust policy are excluded; no cryptographic Byzantine-provider
safety guarantee is claimed.

A compromised provider signer can fabricate its observations, but cannot mint
Metal admission, networking-owner or independent verifier signatures. Verifiers
must be independent in credentials, control and measurement path, not merely
another process operated by the same provider. The trust owner must document
remaining shared failure domains. Disagreement, missing witness or impossible
sequence closes affected gates and quarantines custody. Never majority-vote a
missing required safety predicate into True. Protect logs from provider-supplied
text/URLs and cross-tenant leakage; store raw evidence privately with access audit.

## Release, retirement and unavailable providers

Release closes normal admission in the shared slot, settles all possible sends,
revokes sessions/boot/credential capabilities and obtains #61 disconnect plus
release-owner cleaning/restoration evidence. The provider enters LockedClean
with an irreversible no-old-effects barrier but **keeps its local exclusive lock**.
The release owner validates the settlement certificate; #60 moves to terminal
effects, settles quota, then atomically records Released and clears occupancy
only after quota acknowledgment. That transaction emits finalization work.

At **R3**, provider `finalize-release` verifies the exact signed Released decision,
original reservation/asset generation, release certificate and restore lineage;
atomically tombstones the old binding and unlocks. An unseen finalization response
means recover/query R3, not assume free. Metal may have cleared occupancy before
R3, but any new provider Reserve is denied by the old local lock until R3; this
is a capacity delay, not overlapping ownership. After R3, a new request gets a
new reservation UID and generation and must pass fresh eligibility/qualification.
Old Commit, cleanup or finalization cannot touch the new incarnation. Return to
availability remains the release/inventory owners' decision, not a pool counter.

Maintain permanent compact tombstones for reserve keys, reservation UIDs, Machine
bindings, operation IDs and epoch high-water marks. Store full unsettled inputs
and evidence without time-based GC. Terminal payloads may follow owner retention
policy only after custody/restore obligations are satisfied; compact replay
rejection must survive indefinitely in this initial protocol. Key rotation does
not make old identifiers reusable. Storage pressure blocks new work, not history
retention. Restored journals lacking tombstones are quarantined until independently
reconciled and fenced; backups cannot reset authority epochs.

For planned retirement, use #60's Closing guard before dependency deletion. Drain
attempts using M0/M1, stop new ordinary operations, and reconcile every reservation,
allocation, outbox, input/key dependency and quota obligation. Withdrawal of the
offering alone is not retirement or permission to wipe active customer workloads.
Normal deletion remains blocked until obligations and finalization are acknowledged.

For bankruptcy, outage or a provider that refuses cleanup:

1. Close acquisition and affected execution gates; retain original allocation,
   quota, reservations, unsettled effects and last signed evidence in independent
   Metal service custody. Do not relabel them Released or move the Machine to
   another supplier. No unqualified automatic provider failover is allowed.
2. Use retained recovery responsibility and qualified independent network/power
   or credential isolation paths where available. Respect each owner's evidence
   boundary: disconnected is not stopped, stopped is not sanitized.
3. Record an incident, responsible service and recovery contacts, legal access
   constraints, required evidence and next actions. Two authorized operators
   approve exceptional isolation/restore actions under #62. No force-free button.
4. If access or proof cannot be recovered, keep Unavailable/Quarantined custody
   indefinitely. Escrow/accounting remains subject to #60; business refunds are
   separate from technical release and cannot make occupancy free. An eventual
   write-off/disposal policy belongs to release/accounting owners and is not
   silently added here.

This is durable service recovery responsibility, not a provider-to-provider
custody transfer forbidden by the initial #60 protocol. Independent retention
protects knowledge but does not create physical access. Require contractual
emergency access, key/input recovery and qualified out-of-band witnesses before
production enablement; their absence is a visible deployment blocker. A forced
provider-project purge is a disaster, not successful retirement. External actors
can violate the contract; Metal must not assert safe reuse after they do.

## Request and result examples

Synthetic abbreviated payloads below omit signatures and repeated envelope fields
for readability; they are not deployable requests or permission tokens. Identifiers
such as `sha256:constraints-example` stand for complete digests in real schemas.
All wire messages must include the full required fields specified above.

```json
{
  "protocol": "fulfillment-v1",
  "requestKey": "reserve-attempt-42-1",
  "attemptUID": "attempt-42",
  "providerUID": "provider-example",
  "providerProjectUID": "project-provider-7",
  "consumerProjectUID": "project-consumer-9",
  "machineUID": "machine-12",
  "capabilityDigest": "sha256:capability-example",
  "constraintsDigest": "sha256:constraints-example",
  "quotaCustodyRef": "escrow-42",
  "authorityRef": "reserve-authority-42",
  "decisionEpoch": "8",
  "acceptBefore": "2026-11-14T18:02:00Z",
  "maximumAssets": "1",
  "allowedEffects": []
}
```

```json
{
  "requestKey": "reserve-attempt-42-1",
  "requestDigest": "sha256:reserve-example",
  "reservationUID": "provider-reservation-91",
  "recordRevision": "1",
  "state": "Reserved",
  "canonicalAssetKey": "asset-canonical-31",
  "supplyHandle": "private-server-31",
  "supplyGeneration": "6",
  "mappingDigest": "sha256:mapping-example",
  "capabilityDigest": "sha256:capability-example",
  "acceptBefore": "2026-11-14T18:02:00Z",
  "expiryDisposition": "ResolveMetalDecisionBeforeUnlock"
}
```

```json
{
  "decisionUID": "decision-711",
  "decisionSequence": "711",
  "decisionEpoch": "8",
  "state": "Bound",
  "attemptUID": "attempt-42",
  "requestDigest": "sha256:reserve-example",
  "reservationUID": "provider-reservation-91",
  "canonicalAssetKey": "asset-canonical-31",
  "allocationUID": "allocation-55",
  "decisionCheckedAt": "2026-11-14T18:01:09Z",
  "committedAt": "2026-11-14T18:01:10Z",
  "effectAuthority": false
}
```

Operation submit refers to `allocation-55`, `provider-reservation-91`, the accepted
capability digest, immutable Installation/driver and input manifest. A timeout
returns no terminal promise. Query might return the following retained observation:

```json
{
  "operationUID": "operation-66",
  "planDigest": "sha256:plan-example",
  "resultSequence": "19",
  "state": "OutcomeUnknown",
  "stepID": "operation-66-step-3",
  "sendState": "SendMarked",
  "remoteTaskID": null,
  "reason": "PowerResponseLost",
  "requiredRecovery": "FenceAndResolveRemoteEffect",
  "mayReplayPhysicalSend": false
}
```

For an unbound alternate attempt, `decisions:resolve` with `abortIfUnbound: true`
returns signed AbortDecided; Cancel installs Cancelled even if Reserve is delayed.
If it instead returns Bound, the provider retains custody and Metal starts the
release path. A later contradictory Commit returns `409 TerminalConflict`, not
`200 Committed`. A differing request digest returns `409 IdempotencyConflict`.

## Crash and race matrix

| Crash, race or attack | Required result and recovery |
| --- | --- |
| Quota acquired, worker dies before Reserve | Retained key/outbox and guard recover; abort settles keyed provider tombstone before quota. |
| Reserve arrives twice or response disappears | R1 returns same UID/asset/digest; no second local hold or procurement. |
| Cancel reaches provider before Reserve | M0-backed tombstone wins; delayed Reserve rejected. |
| Two offerings/providers alias one asset | Local all-channel lock plus trusted enrollment/global occupancy reject overlap; unprovable identity quarantines. |
| Receipt valid but IAM/offering/provider gate closes | #60 commit loses to closure; M0 and provider cancel, no consumer effects. |
| M1 races deadline resolution | Same ledger transaction domain chooses one decision; no provider-clock unlock. |
| M1 committed, reply and Commit delivery lost | Local hold becomes ExpiredUnresolved; decision query recovers Bound and R2. |
| Provider isolated before decision is known | No new effects; retain hold and count against caps, never TTL reuse. |
| Provider loses acknowledged reservation journal | Disqualify/fence affected domain; no fresh reserve or execution until custody restored. |
| Metal restored before M1 or M0 | Independent restore epoch closes old paths; reconcile provider receipts/quota and tombstones before reopening. |
| Old Commit after cancellation or asset reuse | Terminal ID/epoch/generation rejects it; contradictory signed decisions trigger incident. |
| Operation submit lost after physical send marker | Query retained task/evidence; unknown means fence/settle, never blind replay. |
| Cancel first, old Submit or Recover later | Persist high-water stop even with no inbox; reject stale authority. |
| Consumer loses runtime permission mid-operation | New admissions/renewals denied; explicit stop/fencing settles existing finite authority. |
| CustomerRedfish and automatic worker race | Same immutable driver/slot rejects the second writer. |
| Signed Ready lies or networking evidence is stale | Verify independent predicates; Unknown/quarantine, no public success. |
| Shared IP or credential reused while old writer runs | Owner no-reuse/fence barrier blocks activation even if provider says complete. |
| Cleanup succeeded, quota release acknowledgment lost | LockedClean, occupancy/charge retained under #60; query quota, no premature R3. |
| Metal Released, R3 acknowledgment lost | Provider lock/tombstone query determines local availability; identical finalization only. |
| Provider project removed or operator bankrupt | Independent service custody survives; no timeout-based release or transfer. |
| Qualification/key revoked; cleanup still needed | Restricted fresh service recovery authority via qualified path; no ordinary work or deletion of obligations. |

## Qualification tests

These are required adversarial tests, **not tests executed by this documentation
change**. Qualification is specific to capability/adapter/firmware/hardware and
trust-policy revisions; a mock transport or happy-path vendor demo is insufficient.

1. Reserve across two offerings and every provider sales channel for the same
   hardware; prove one local hold and one global allocation, including aliases.
2. Crash before/after each R1, M0, M1, R0, R2 and R3 durable point; lose every
   reply and reorder retries. Verify identical identities and conserved custody.
3. Partition at the deadline with M1 committed but unseen; no provider unlock.
   With M0 committed, replay delayed Reserve/Commit and verify permanent rejection.
4. Saturate reserve caps with ExpiredUnresolved and quota-unknown requests; prove
   bounded admission, no hidden renewal, fanout or unaccounted selection retries.
5. Race IAM revocation/restoration, provider retirement and eligibility changes
   against receipt validation and M1 using #60 invalidation-history semantics.
6. Capture all network/BMC/provider audit effects before R2 and before separate
   operation admission; Reserve/Commit must produce only bookkeeping/read effects.
7. Retry same operation key with changed driver/input/adapter/asset and with an
   old executor generation; reject without sending hardware commands.
8. Lose a physical response after send-mark; verify Unknown and qualified fencing,
   not automatic rerun. Exercise RAM installer and queued BMC task persistence.
9. Cancel before Submit and recover after terminal release; prove stop tombstones
   survive object deletion, failover, certificate rotation and backup restoration.
10. Forge provider Ready with valid signer but missing networking/independent
    witness evidence; verify it cannot become Metal success or release.
11. Reuse addresses or secrets while old effects remain; owner barriers must block
    activation, and old cleanup must not touch the successor incarnation.
12. Exercise provider outage/bankruptcy, consumer/project deletion and forced
    purge. Verify independent custody, charged escrow and actionable blockers.
13. Restore stale Metal/provider journals independently, including lost signing
    keys and tombstones; old execution paths must remain fenced until reconciliation.
14. Run the same standalone non-Talos machine lifecycle through Fleet and a
    qualified non-Fleet provider. Compare admission/assurance/release outcomes;
    verify external flow needs no Karmada and Fleet cell has no hub/project client.
15. Verify mTLS identity/key revocation, bad audiences, digest conflicts, clock
    skew, replay, oversized evidence, cross-tenant query and SSRF rejection.
16. Complete planned retirement with all terminal receipts, then replay the full
    old message log. No asset, quota, credential or network identity resurrects.
17. Race external selection/global enrollment with Fleet Preparation or an
    intersecting adopted-domain hold. Global binding must reject overlap. Submit
    a non-consumer subject without real external M1/R2: reject before effects,
    never manufacture a provider reservation, charge or R3 receipt.

## Operations and prerequisites

Report per-provider counts/oldest age for pending reservations, ExpiredUnresolved,
commit delivery lag, cancel/finalize lag, quota custody, unknown sends, evidence
rejections and retirement blockers. Measure reserve latency, decision resolution,
physical settlement time and ingestion lag separately; transport latency is not
fulfillment success. Alert immediately on contradictory decisions, alias identity,
lost durable history, stale restore epochs and attempted terminal resurrection.
Alert at configured age/cap thresholds for unresolved work. No successful-release
SLO may hide unknown holds by timing them out.

Audit registration/qualification/gate changes, exact request and decision digests,
linearization sequence/epoch, actors, result validation and recovery decisions.
Keep allocation/request IDs in protected traces, not unbounded metrics labels;
public errors redact upstream ports, physical routes, raw payloads and secrets.
Recovery tooling must query authoritative decisions and all outstanding keys,
show independent evidence gaps and resume identical outboxes. It must not offer
delete-history, substitute-asset, force-success or mark-free actions.

Enable each registration/capability only after independent adversarial review and
owner acceptance. Disable by closing gates and draining custody, not deleting
adapters or signing keys needed to settle old work. Protocol upgrades require a
new capability revision and retained old decoders/inputs for live obligations;
this is ongoing custody, not compatibility with earlier illustrative APIs.

| Unresolved external prerequisite | Required owner acceptance before implementation/production |
| --- | --- |
| Allocation and quota | #60 ledger extension, scoped linearizable decision/abort interface, deadline qualification, protected escrow and provider lifecycle interlocks. No claim Milo supplies these now. |
| Provider/vendor | Durable all-channel exclusion, no effects on reserve/commit, permanent tombstones, exact task query, qualified fences and cleanup rights; signed contract and failure testing. |
| Inventory/trust | Canonical identity/enrollment across resellers, observed-to-physical mapping, independent witness trust roots and admission policy for dishonest-provider risk. |
| Execution | #62 journal/slot integration, qualified provider-native translation and stop boundaries, retained bytes/keys, immutable driver and explicit recovery. |
| Networking | #61 physical attachment and effective disconnect/no-reuse evidence; the historical unverified cloud attachment reference is not an available integration. |
| Release/accounting | Assurance predicate sets, settlement certificate and write-off/disposal handling; unavailable custody cannot be declared reusable by this proposal. |
| Security/deployment | mTLS PKI, registered receipt keys, independent restore authority, bounded trusted time, durable evidence/input custody and offline isolation access. |
| Operations/legal | Emergency physical access, provider insolvency plan, audit rights, independent recovery staffing and retention funding. |

Concrete schema identifiers, capability-specific freshness values, hold-cap numbers
and hardware qualification evidence need owner agreement; they must be finite
and verified before registration activation. The algorithm and fail-closed choices
are selected here, not postponed to a vendor adapter. No external prerequisite
may be implemented as a permissive default to make this draft runnable.

## Alternatives and tradeoffs

- Requiring Fleet Device/MachineCapacity objects and Karmada for every supplier
  couples provider implementation to Fleet; reject in favor of protected exact
  identity and equivalent semantics.
- Automatic reservation TTL reuse improves availability but loses to unseen M1;
  reject. Finite admission authority plus unresolved custody is the explicit cost.
- Distributed two-phase commit across Metal, quota and vendors requires prepared
  transaction support everywhere; choose #60 escrow plus signed decisions and
  idempotent durable provider holds instead.
- Treating a provider's Ready/released bit as authority hides trust and cleanup
  gaps; reject. Required independent evidence can exclude otherwise useful vendors.
- Provider substitution on failure improves apparent fulfillment but mutates
  immutable bindings and hides outstanding effects; require explicit release and
  a new Machine/allocation instead.

## Implementation and review status

2026-11-14: new standalone design, documentation only. No runtime behavior, vendor
integration or qualification is implemented or verified by this change. Submitted
as a draft for independent adversarial sign-off; the author does not self-sign
off. Owner prerequisites remain unresolved as listed above.

[bmc]: https://github.com/datum-cloud/fleet/blob/f04e63ddd9d10cbf306daf3a0232961debd85e7f/docs/enhancements/secure-bmc-access.md
[management]: https://github.com/datum-cloud/fleet/blob/fa07c2236801a33a58efc914b555586c8e146cc6/docs/enhancements/fleet-management/README.md
[offerings]: https://github.com/datum-cloud/fleet/blob/a922ff1eb3275e26af6ae750a7515ddde245a754/docs/enhancements/profile-access/README.md
[pxe]: https://github.com/datum-cloud/fleet/blob/d7ff793b03711203dbfc9fac87d43922cc649b6f/docs/enhancements/pxe-installation/README.md
[lifecycle]: https://github.com/datum-cloud/fleet/blob/d7ff793b03711203dbfc9fac87d43922cc649b6f/docs/enhancements/machine-lifecycle/README.md
[admission]: https://github.com/datum-cloud/fleet/blob/2563b2a2f177a72d20c1d62265c89ff15688ba25/docs/enhancements/allocation-admission/README.md
[network]: https://github.com/datum-cloud/fleet/blob/ed6edec5b056fd53343e11d58d979af495f1bd7c/docs/enhancements/network-handoff/README.md
[execution]: https://github.com/datum-cloud/fleet/blob/7fa823050a443c1f5b85c56e85ed04ebaa5bd2cb/docs/enhancements/execution-recovery/README.md
[tls]: https://www.rfc-editor.org/rfc/rfc8446
[jcs]: https://www.rfc-editor.org/rfc/rfc8785
[jws]: https://www.rfc-editor.org/rfc/rfc7515
[eddsa]: https://www.rfc-editor.org/rfc/rfc8037
