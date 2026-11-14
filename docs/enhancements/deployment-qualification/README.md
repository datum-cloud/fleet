---
status: provisional
stage: alpha
---

# Standalone deployment and qualification

- [Deployment and bootstrap](#independent-deployment-and-bootstrap)
- [Transaction boundaries](#owner-transaction-and-evidence-boundaries)
- [Durable custody](#durable-custody-and-backups)
- [Restore activation](#fencing-and-restore-activation)
- [Failure matrix](#partition-and-outage-failure-matrix)
- [Operator recovery](#operator-recovery-workflow)
- [Qualification gates](#qualification-profiles-and-activation-gates)
- [Acceptance scenarios](#acceptance-scenarios)

## Status and scope

This enhancement selects a **proposed deployment contract**, not deployed,
implemented, tested, or qualified behavior. The repository contains implementation
subsets and synthetic validation; none proves the production guarantees below.
Record names are design vocabulary, not existing APIs or operational commands.
No runtime evidence or independent review approval is claimed.

Scope is standalone Metal with optional Fleet supply. No NodeClaim, managed
cluster, node service, persistent guest agent, compatibility layer, or migration
machinery is required or added. Infrastructure running the service is distinct
from customer capacity. This directory alone changes; environment manifests and
credentials belong outside this proposal, with rollout configuration in
`datum-cloud/infra`. Existing authoritative protocols remain their owners' work.

Read the [repository guidance](../../../AGENTS.md),
[service overview](../../../README.md), [design context](../../architecture/design.md),
and [implementation status](../../development/README.md) alongside this proposal.

## Pinned contracts and precedence

Reviewed 2026-11-14. Branch base is fetched `origin/main` at
`8ebec19513a0cbc8f219d3acae46f89ab471510b`; no unmerged companion changes are inherited.
These exact source revisions are dependencies, not implied approvals or merged
implementations. Requalification is required for a changed dependency contract;
the proposed checks are not support for legacy protocol compatibility.

| Source | Exact head | Consumed contract |
| --- | --- | --- |
| [#41 BMC][bmc] | `f04e63ddd9d10cbf306daf3a0232961debd85e7f` | Allocation-bound access, protected credentials, common effect conflict domain. |
| [#51 management][management] | `fa07c2236801a33a58efc914b555586c8e146cc6` | Independent management, project tenancy, hub delivery and cell-local evidence writers. |
| [#58 offerings][offerings] | `a922ff1eb3275e26af6ae750a7515ddde245a754` | Current admission rights, retained accepted capabilities, no withdrawal-driven eviction. |
| [#59 PXE][pxe] and [lifecycle][lifecycle] | `d7ff793b03711203dbfc9fac87d43922cc649b6f` | Exact Installation source/driver and predecessor, managed versus delegated assurance. |
| [#60 allocation][allocation] | `2563b2a2f177a72d20c1d62265c89ff15688ba25` | One serializable management ledger, authority gates, quota escrow, recovery epoch. |
| [#61 networking][network] | `ed6edec5b056fd53343e11d58d979af495f1bd7c` | Owner-operated paths, effective isolation, disconnect and address disposition evidence. |
| [#62 execution][execution] | `7fa823050a443c1f5b85c56e85ed04ebaa5bd2cb` | Management and cell journals, S1 send ambiguity, physical stop, restore interlock. |
| [#63 inventory][inventory] | `8fe054101ac3bea822e5d16e40cb375a884e7e56` | Canonical identity, independent witnesses, physical holds, component continuity. |
| [#64 fulfillment][fulfillment] | `50e678ee20645ad0a4e340005b18432fe4633bec` | Exact external reservation, all-channel exclusion, provider journal and R3 finalization. |
| [#65 release/reuse][release] | `fae0cd3bf2a3a7e208c92c3e46c9b5670d877906` | Discovered companion: R1 closure, evidence-based R2 settlement, separate reuse gates. |

The release interface is available as a draft at this pin, not pending discovery.
Its owner acceptance, implementation, sanitization procedures and qualification
remain pending. Deployment cannot substitute its own cleaning or reuse guarantee.
The newer pinned contracts specialize earlier illustrative architecture; they do
not authorize a second allocator, direct cell-to-project status writes, or Fleet
ownership of switches, routing, IPAM or customer service policy.

## Decisions and safety model

1. Bootstrap management on independently procured infrastructure, never on supply
   whose installation, release or recovery requires this Fleet deployment.
2. Keep #60's single logical PostgreSQL authority domain. Co-locate #62 management
   operations and #65 release transactions there; do not add a deployment ledger
   that can bind, release, admit effects or rewrite their decisions.
3. Deploy #60/#62's independent recovery authority outside management and cell
   backup/administrative failure domains. Its epoch is an activation prerequisite,
   not proof that hardware stopped.
4. Authorize effects only after durable exact-input custody and finite authority.
   Lose a dependency: refuse new affected work, retain occupancy and obligations.
5. Treat every incomplete restore as disabled. Never recover permission from an
   old Open gate, absent row, successful process startup, elapsed lease or backup.
6. Qualify exact deployment/hardware/protocol combinations, not the product name.
   Evidence for safety, availability and customer installation assurance differs.

Assume crashes, partitions, delayed/duplicate messages, paused writers, stale
snapshots, lost replies, key loss, storage exhaustion and provider unavailability.
Bypassing all independent administrative controls, compromising every witness,
or hidden firmware behavior outside a qualified model defeats these assumptions.
Record those residual risks; signatures prove origin, not truthful physical state.

## Independent deployment and bootstrap

### Selected reference topology

The first qualifying topology uses the following concrete placement requirements.
Specific vendors, database operator, host sizes and facility choices are deliberately
not selected; the deployment manifest must name them before qualification.

| Domain | Placement and redundancy | Independence requirement |
| --- | --- | --- |
| Management M | Three independently powered/routed availability zones in one management region; replicated API/controller instances; one PostgreSQL primary plus two replicas in distinct zones | Capacity is independently procured and excluded from all Fleet offerings. No dependency on customer DNS, Networks, boot servers, credentials or installed OS. |
| Location C, per cell | Three dedicated control hosts in separate qualified power/storage domains, with a local PostgreSQL primary and two replicas; local execution, boot session and broker services | Hosts are excluded from customer supply. Loss of the entire facility may stop the cell; this is not claimed as regional HA. No control host may be reinstalled by an operation whose recovery it controls. |
| Recovery R | Three quorum members across three independent locations outside M and C; separate account, operator roles, storage, recovery signing custody and administrative credentials | M/C administrators and snapshot restore jobs cannot reset R or activate themselves. Network partitions cannot produce two writable quorums. |
| Archive A | Retention-protected encrypted object copies in two locations, one outside M/C; separately administered deletion controls | Preserve exact blobs, evidence, journal/WAL archives and manifests despite project deletion or loss of M. A is not an alternate admission database. |
| Fence F | Owner-managed BMC egress enforcement plus independent compute/storage fencing, and qualified out-of-band all-feed isolation | Reachable and operable without M/C workloads. Physical isolation cannot depend on the host/BMC being isolated. Networking/facilities retain their configuration authority. |

M and each C acknowledge authorizing PostgreSQL commits only after WAL is flushed
on the primary and at least one synchronous replica in a different failure domain.
No fallback to asynchronous acknowledgment is permitted. Promotion must prove the
candidate contains every acknowledged commit and exclude the former primary from
compute, storage and client paths; otherwise enter restore, even if another replica
looks healthy. A replication count or election majority alone does not prove this.
The storage qualification must inject loss of each primary/acknowledging replica
pair and demonstrate refusal when preservation cannot be established.

R uses a separate consensus-replicated append-only state machine: two of three
members must durably commit an epoch/lineage decision before its signing service
issues a receipt. Minority members cannot issue new receipts, and membership/key
changes require the same serialized quorum authority plus recovery approvals.
Receipt issuance binds the committed decision, never a proposed local counter.
The concrete consensus implementation and its storage/signing integration must
pass D12; this specifies the required construction, not an available service.

Services may share hosts where privilege boundaries remain enforced, but R, A's
deletion authority and F cannot collapse into M's service account or snapshot.
Map actual shared power, uplinks, identity providers, KMS, DNS, time sources,
virtualization/storage administrators and cloud-account roots. Logical namespaces
or availability-zone labels alone do not prove independence. A single-region M
loss stops new global admission; no automatic second-region allocator is enabled.
Another domain cannot advertise overlapping assets or Machine lifetimes.

External fulfillment may omit C and Karmada on the provider path, as #64 permits.
It still requires independently recoverable Metal custody and qualification of
the provider's journal, old-writer fencing and all-channel exclusion. Provider
assertions cannot silently inherit the reference topology's guarantees.

### Dependency-ordered bootstrap

1. Operators establish R, archive deletion controls, independent administrative
   identities and F using an out-of-band, two-person bootstrap ceremony. Retain
   signed initial lineage, trust roots, member identities and reachability tests.
   Genesis is allowed only for a demonstrably new empty authority domain. A lost
   R is a disaster, never another invocation of genesis for the same assets.
2. Provision M/C hosts through the independent infrastructure owner. Establish
   trusted time with a measured uncertainty bound, management DNS/resolution,
   certificates and secret custody without depending on this Metal service.
   Retain offline recovery addresses, root fingerprints and signed bundles;
   emergency access verifies pinned identity rather than disabling TLS or IAM.
3. Initialize empty journals with constraints, scoped roles, synchronous durability
   and backup capture. Register their lineages in R while admission and all effect
   boundaries remain disabled. Install pinned controller/adapter binaries and
   configuration by digest from the retained bootstrap bundle.
4. Bring up read-only project/IAM/quota integrations, provider guards, inventory
   observations and networking-owner endpoints. Import existing occupancy and
   unknown physical claims before publication; imports perform no preparation.
5. Register sole authority-gate writers and exact cell/provider routes; exercise
   close-before-change integration with IAM, project lifecycle, quota, offering,
   inventory, execution, network and release owners. Install explicit Karmada
   targeting for Fleet cells; delivery itself grants no effect authority.
6. Complete the qualification stages below on dedicated test assets. Only then
   activate scoped boundaries and ledger gates through their owners. Customer
   capacity cannot be used to bootstrap the system that must first qualify it.

An offline bundle includes signed binary/configuration digests, schema version,
trust roots, procedures and protected references, not committed credentials.
Security supplies separately recoverable keys and emergency identities. Loss of
ordinary IAM may permit audited recovery by these pre-provisioned identities,
but never consumer admission or a bypass of evidence/epoch checks.

## Owner, transaction and evidence boundaries

| Authority | Transaction or decision boundary | Evidence consumed; prohibited inference |
| --- | --- | --- |
| Metal allocation | #60 serializable ledger binds occupancy, Machine, escrow reference, decision and outbox | Current registered gate vector and quota receipt; no Kubernetes/federation transaction substitutes for it. |
| Management execution | #62 slot/predecessor/operation/outbox in that same ledger and lock order | Sealed inputs and current gates; deployment cannot allocate a second slot. |
| Cell execution | #62 selected-cell journal persists acceptance and S1 before physical send | Exact operation/fence and broker lineage; S1 without response is possible effect, not permission to resend. |
| Release coordinator | #65 R1, terminal-effects decision and R2 use existing ledger boundaries | Owner-validated closure/sanitization and quota acknowledgment; Released alone is not reusable. |
| Quota and fulfillment | Their own durable hold/decision/tombstone domains, #60/#64 keyed exchanges | Query exact identities after reply loss; no distributed transaction or expiry-based unlock is implied. |
| Inventory | #63 enrollment, component/placement evidence and physical reservation contracts | Independent observations and current trust; import does not grant effects or free capacity. |
| Networking | #61 owner writer and effective data-plane fence/disposition | Exact revisions, alternate paths and existing flows; desired configuration is not isolation. |
| Recovery R | Linearizable monotonic epoch and enabled-lineage record per affected domain, using #60/#62 recovery authority | Independent old-writer/fence receipts; a new epoch alone proves no hardware stop. |
| Qualification authority | Signed immutable profile/evidence decision; registered deployment dependency gate in #60 | Required owner approvals and scoped test receipts; cannot override an owner gate or manufacture readiness. |

The qualification adapter is a proposed new participant in #60's gate protocol,
not a new authorization engine. Its registration and inclusion in acquisition,
operation admission/renewal and boundary activation dependency vectors are
implementation prerequisites. It closes the gate before qualification becomes
ineffective, then publishes a new immutable revision before reopening. There is
no polling-cache approximation to close-before-change. Other owners must still
approve their own effective revisions. Gate closure denies new work; already
admitted finite work follows #62 stop/expiry semantics, not instant revocation.
For scheduled profile expiry, every derived decision/activation deadline is no
later than that expiry; commit and effect boundaries check it with bounded trusted
time even if the qualification controller is down. Expiry cannot depend solely on
a controller waking to close a row. Renewal requires fresh qualified authority.

Cleanup remains under the separately retained, finite #65 service authority and
#62 recovery lane, with target/epoch/safety checks. It is not authorized by an
ordinary customer gate or by a permanently valid emergency token. A closed
qualification gate must permit only explicitly approved observation, fencing
and qualified cleanup paths, never arbitrary effects under a cleanup label.

## Durable custody and backups

### Custody set

Protected custody survives consumer/provider project deletion. Ownership of a
provider project does not grant SQL administration, archive deletion or journal
rewriting. Retain at least the following, with sole writers unchanged:

- #60 gates and mutation history, guards, attempts/decisions, quota receipts,
  canonical enrollment/occupancy, immutable Machine bindings and outboxes.
- #62 management/selected-cell journals, steps and send markers, target/session
  bindings, driver/recipe/translations, executor generations and recovery holds.
- #63 plans, component mappings, evidence/challenges, physical/power reservations,
  tombstones and unfinished maintenance/transition permits.
- #61 network intent and owner receipt references, attachment/claim generations,
  isolation/disconnect and no-reuse barriers; owners preserve their own journals.
- #64 provider registration/capability pins, reserve/commit/abort and finalization
  receipts, remote task inventory and signed exact request payloads.
- #65 release cases, cleanup obligations, closure inventories, certificates,
  finalization obligations and quarantine/disposal decisions.
- Exact artifact/configuration bytes, credential/key versions, trust chains,
  verification evidence and recovery/profile approvals needed by those records.

A backup manifest binds domain and lineage, PostgreSQL base backup and WAL
high-water marks, journal ranges, object versions/digests, decryptability checks,
key dependencies, remote owner checkpoint receipts and retention obligations.
Capture a locally consistent ledger plus append-only subsequent journal history.
There is no globally atomic backup across M, C, quota, networking and providers.
Record each independent checkpoint and in-flight gap; recovery reconciles the
conservative union, not a fabricated common timestamp.

Synchronous acknowledgment gives RPO zero only inside the qualified storage
failure envelope. Archive shipping may lag and cannot extend that guarantee to
disaster restore. A profile declares archive-lag alarm limits and tests them;
incomplete archive knowledge means unavailable capacity, not lost custody erased.
Retain unsettled inputs and obligations without time-based expiry. Keep permanent
compact identity/replay tombstones wherever required by #60/#64/#65. Larger terminal
payloads may be compacted only under the owning protocol's proof requirements;
no retained snapshot or delayed message may revive an identity after compaction.
Capacity pressure backpressures new admission, never deletes
unsettled state. Admission, WAL, artifact and emergency-recovery headroom are
measured independently; each profile supplies tested numeric limits.

Restoration exercises must decrypt and hash every referenced unsettled input,
not just verify backup-job success. Detect missing keys, damaged WAL, object
version substitutions and incomplete owner checkpoint coverage. Evidence exports
are redacted and access-controlled; secret-bearing bytes remain encrypted.

## Exact artifacts, secrets and delivery

Admission seals the #59/#62 input manifest before committing effect authority:
image, kernel/initrd/boot script, recipe, translated provider request, rendered
configuration, driver, adapter binary, target disk/NIC mapping, origin policy and
protected secret-content versions. The custody service acknowledges retention of
bytes and decryption dependencies before a ledger reference becomes admissible.
Post-commit outbox delivery cannot fetch a mutable tag as a replacement.
For this reference topology, sealing requires verified durable immutable copies
in both A locations and recoverable envelope-key custody outside the lost-M
failure domain. Ledger/WAL archive shipping may lag as described above; admission
must not borrow that allowance for as-yet-unretained input bytes or keys. A
pre-admission upload without a committed reference grants no effect authority.

Artifact stores serve only digest/version-bound requests with audience-, target-,
operation- and epoch-scoped finite capabilities. Verify hashes at serving and
consuming boundaries. A cache cannot authorize a fetch from its own contents;
boot session tombstones and cutoff/drain rules are #62's. Do not put rendered
secrets in Kubernetes status, logs, PRs or this repository. Hub handoffs carry
protected references and exact digests, not reusable BMC credentials.

| Dependency lost or changed | Permitted response |
| --- | --- |
| Object service unreachable | Use a previously verified exact local copy only within still-valid admitted authority and session policy; otherwise block the next step. No stale authorization cache. |
| KMS/secret service unreachable | Existing authorized process may finish its bounded step with already-held inputs; no grant renewal, new credential release or substituted plaintext. Retain custody. |
| Exact bytes or content-secret version irretrievable | Do not replay or re-render. Quarantine the affected operation and use separately authorized settlement/release. |
| BMC transport credential rotated | #62 recovery amendment may replace access credentials only with proof of same target/scope and recorded revocation; it cannot change installer content, driver or endpoint trust. |
| Signing key lost/compromised | Close affected gates, preserve old verification material, fence paths and establish a verified new lineage/key under recovery approvals; new signatures do not rehabilitate missing old evidence. |
| Delegated PXE origin changed | Preserve initial endpoint/policy/content and observed digests; do not refetch changed content for uncertain replay or claim QualifiedImage. |

Delivery remains management commit/outbox, canonical hub handoff, explicit
selected-cell delivery, cell-local result, Karmada reflection, and management
validation on the Fleet path. Restored hub objects are delivery candidates only.
External fulfillment uses #64's authenticated durable query/results rather than
inventing cell credentials or bypassing Metal validation. Duplicate/reordered
delivery must retain original IDs and reject conflicting digests.

## Fencing and restore activation

### Distinct barriers

Do not conflate recovery epoch, database lineage, authority-gate epoch, allocation
binding, executor generation, network revision, component identity or boot
instance. Preserve the full #62 fence and the owners' epoch vectors in receipts.
R maintains the monotonic recovery epoch and allowed lineages; it does not mint
a competing allocation identity or consume an Installation predecessor.

R activation receipts bind scope, exact journal lineage/high-water reconciliation,
epoch, boundary identity, finite validity and approved recovery decision. No
controller or broker restart is effect-enabled by default. Before issuing an
activation receipt, R must have current independent fencing/reconciliation
evidence for that scope. Boundary activation also requires its owning service's
checks and current ledger authority; a signed receipt is necessary, not sufficient.
Loss of R prevents activation/renewal. Existing finite authority cannot expand;
physical effects already accepted remain obligations even after receipt expiry.

Independent controls must establish separate facts:

1. Old database/admission writers cannot regain writable storage or credentials.
2. Old broker/worker compute and egress are excluded, including established flows
   and provider queues. Credential rotation alone does not stop accepted work.
3. Boot/relay/console capabilities are revoked and serving replicas drained or
   removed. Previously downloaded installers are not recalled by this action.
4. BMC tasks, volatile installers and storage writes are settled under the exact
   hardware procedure. If necessary, independently isolate all mapped host/BMC
   power feeds and verify de-energization, queues and persistent device behavior.
5. Networking proves its own effective fences and old-writer exclusion. Network
   isolation alone does not stop local disk writes or prove sanitization.

If a remote path cannot be reached or fenced, its scope stays disabled. Discovering
more targets widens quarantine. If target enumeration is incomplete, fence the
whole enclosing cell/provider/authority domain, not merely the snapshot's rows.

### Restore cannot reopen authority

Routine no-loss failover may keep the lineage only after durable-commit
preservation and old-writer exclusion are proven. Any uncertain rollback, including
one cell journal or an owner-side provider/network journal, invokes restoration.
Storage administration must interlock snapshot attachment before writable service
exposure. Network and credential enforcement must prevent restored Open rows from
serving effects while the restored database is inspected. In-place rollback that
bypasses this interlock invalidates qualification and requires domain isolation.

Restore is a sequence of durable recovery decisions, not a single enable switch:

| Phase | Required evidence before leaving | Remaining prohibition |
| --- | --- | --- |
| Disable | Gate closures where reachable; independent ingress, credential and effect-path deny receipts; full affected-scope inventory | Unreachable owners are unknown; no automatic outbox/permit replay. |
| Fence | Old DB/admission writers excluded; #62 boundary inventory and stop/isolation receipts; unresolved targets quarantined and denied | Advancing an epoch cannot stand in for a missing fence. |
| Advance | R commits a strictly later epoch after applicable barriers, retaining old lineage and incident | New epoch alone authorizes neither recovery effects nor consumer work. |
| Reconcile | Restored custody union with surviving WAL, cell/provider/owner journals, quota anchors and physical inventory; exact inputs decrypt/hash | Missing rows become unknown obligations, never ProvenNotSent, a fresh predecessor or free supply. |
| Recover | Per-record finite recovery envelopes after owner validation; new boundary lineage acknowledgments; safe checkpoints only | No replay inside uncertain disk-writing steps or altered-plan execution. |
| Revalidate | Fresh owner evidence/authority snapshots, current identities, satisfied profile gates and reconciled obligations for the explicit scope | Old success, signature or certificate is not automatically current evidence. |
| Open | Two-person recovery approval plus each registered owner reopening its gate with fresh revision under #60 ordering | Qualification authority cannot override a Closed/Retired owner gate or clear occupancy. |

Unresolved records may remain permanently quarantined while disjoint, positively
proven scopes recover. This is allowed only when containment and dependency
partitioning are proved; unknown overlap blocks the enclosing scope. No deletion
of uncertainty is needed to reopen unrelated service.

If R itself loses quorum history, disable all affected lineages and freeze
activation. Two operators alone cannot invent a larger number or bless a random
epoch. Security/recovery owners must fence every old R signer and participating
authority/effect path, retain independent lineage/tombstone evidence, establish
a new externally verified trust lineage and enroll each boundary explicitly.
Unknown/offline boundaries remain denied; until this procedure is implemented
and qualified there is **no supported R-loss reactivation path**. Backup of R
helps investigation but cannot automatically recover activation authority.

## Partition and outage failure matrix

Existing tenant execution need not be destroyed by control-plane unavailability.
Continuity is not newly verified readiness; stale health is exposed as unknown.
Safety isolation may interrupt a tenant when separately authorized. No partition
expiry releases an allocation, quota hold, physical reservation or provider lock.

| Failure | New work and bounded in-flight behavior | Recovery and custody rule |
| --- | --- | --- |
| M ledger/quorum unavailable | No new allocation, operation, renewal or release commit; cell may finish only already admitted finite steps permitted by #62 | Preserve steps and charged holds; restore access and query exact decisions. |
| IAM/quota/project gate unavailable | Block affected acquisition/current authorization; retained cleanup uses its separately qualified lane | No stale allow or recreated same-name project substitution. |
| Hub/federation partition | No new delivery; delivered finite work may continue to its allowed boundary | Cell retains evidence; duplication on reconnect cannot create another operation. |
| M-to-C partition during cancellation | Central closure does not prove remote cutoff; C may start still-valid steps until closure/expiry | Keep slot and closure inventory; require actual remote barriers before successor/release. |
| C quorum lost or disk full | No new acceptance/S1/credential grants; accepted hardware actions may continue | Account for ambiguous sends; independent stop/isolation if needed. |
| R partition/loss | No activation or authority renewal depending on R; existing finite work stays bounded | No majority inferred from local cache; R-history loss follows disabled disaster procedure. |
| Fence service/onsite access unavailable | No takeover relying on that fence | Retain unknown custody indefinitely; failover is not permission to bypass F. |
| Network owner unavailable | Block new materialization/handoff/cleanup verification; tenant paths may persist | Retain no-reuse barriers and claims; connectivity alone is not owner evidence. |
| Artifact/key/archive failure | Block next step lacking exact bytes/authority and stop unsafe new admission at retention thresholds | Recover exact versions; missing evidence does not become readiness. |
| Time bound exceeded or clock rollback | Refuse new deadline-based steps/grants/renewals | Retain sends; reestablish trusted time and evidence freshness before reopening. |
| Lost provider reply or provider insolvency | Query same #64 key; no fresh reservation or automatic unlock | Keep independent Metal custody and #65 incident; unavailable hardware stays unavailable. |
| Old snapshot, old worker or delayed callback | Start observation-only; reject stale lineage/epoch/revision | Preserve conflicting history, fence and reconcile; do not adopt old Ready. |
| Site-wide power loss | No local HA claim; already-running data services may stop | Confirm all feeds and persistent queues before restore; loss is not sanitization. |
| Release evidence revoked after quota acknowledgment | #65 retains quarantine/occupancy until valid R2; no effects resume | Report accounting exception; deployment cannot reverse facts or synthesize a new hold. |

## Operator recovery workflow

Implementation must supply protected inspect, close, fence, reconcile and
per-record recovery operations; no working CLI is claimed here. Procedures must
not instruct operators to edit tables, delete finalizers or force Ready/Free.

1. **Open incident:** retain actor, reason, observed failure, expected versions,
   exact project/asset/allocation/Installation/operation/cell/provider identities,
   profile and complete epoch vector. Distinguish observation from authorization.
2. **Contain:** close affected admission through owner adapters and independent
   enforcement. Inventory every possible sender/target, including removed project
   objects, manual physical work, external workers, boot replicas and old snapshots.
3. **Choose procedure:** qualified failover, same-plan checkpoint recovery,
   snapshot restoration or physical isolation. Two independently authenticated
   authorized operators approve epoch/lineage changes and isolation/restoration.
   Approvals bind exact scope and expire; changed scope requires new approval.
4. **Execute and collect:** use #62/#65 finite recovery authority and exact inputs.
   Record durable before/after decisions, task IDs, fence receipts and independent
   observations. Do not use customer access credentials for service cleanup.
5. **Reconcile:** compare every owner's outstanding inventory, identity and
   tombstone high-water marks; retain both sides of a conflict. Validate exact
   bytes/keys, predecessor and component continuity. Unexplained effects stay held.
6. **Validate disposition:** management alone accepts operation/release results.
   #65 R2 requires its certificate, quota acknowledgment and gate checks; #64
   external R3 acknowledgment and fresh inventory/network/reuse gates remain
   separate. Disaster recovery is not an alternative release path.
7. **Reopen narrowly:** follow the restore table and record per-owner revisions
   and boundary acknowledgments. A second operator checks the evidence bundle.
   Rejected or missing proof leaves quarantine with named owner and next action.

Report unresolved-custody age, closed-gate reasons, last durable ledger sequence,
replication/archive lag, blob/key verification failures, fence completion latency,
pending provider R3, clock uncertainty and evidence freshness. Alert immediately
on a stale-lineage send, simultaneous writers, unexplained occupancy drop or
unauthorized reopening. Profile-specific queue/space/lag thresholds must be
measured before rollout; no numerical availability SLO or bounded disaster RTO
is claimed. Recovery may remain blocked indefinitely on inaccessible hardware.

## Qualification profiles and activation gates

### Immutable qualification profile

A profile binds exact protocol/source revisions, binaries and schema digests,
deployment failure-domain map, PostgreSQL replication/promotion implementation,
R/F trust and activation mechanism, IAM/quota/gate adapters, networking realization,
boot trust, provider adapter, firmware/BMC/storage/NIC versions, disk topology,
image/recipe and driver, supported assurance and release procedures. It includes
numeric authority/session lifetimes, clock uncertainty, owner evidence freshness,
retention/headroom/lag thresholds and any firmware queue-drain bounds with measured
justification. Unset values or untested bounds block activation, not defaults.

Each profile names exclusions (initially autonomous external storage and unproved
firmware queue behavior), accountable owners, evidence digest/location, validity
and revocation conditions. Approval binds that exact profile. Independent reviewers
must challenge isolation and restore assumptions; author self-review is not their
sign-off. No independent review has been performed for this draft.

| Profile | Required qualification | Permitted claim after successful evidence review |
| --- | --- | --- |
| Q0 synthetic safety | Deterministic fault harness and all owner transaction/gate races using synthetic targets | Protocol model exercised only; no hardware or production readiness. |
| Q1 dedicated Fleet lab | Real independent M/C/R/F/A; exact firmware and local disks; qualified non-Talos managed image; restore/stop/release tests | Only tested lab combination, including ManagedImage evidence; not general rollout approval. |
| Q2 bounded production Fleet | Q1 plus production identity/tenancy, operator drills, owner approvals, monitoring, capacity limits and independent adversarial review | Explicit allowlisted assets/cells/offering capabilities; gradual expansion requires evidence. |
| QD delegated PXE | Q1/Q2 infrastructure plus exact delegation/proxy policy, hostile installer and physical-stop tests | DelegatedContract only; not guest image correctness, security or persistent firmware trust. |
| QE external fulfillment | Applicable Q0/Q2 controls plus #64 provider journal, exclusion, decision races, independent evidence and #65 R3 recovery | Only registered provider capability/firmware/profile scope; no assumed Fleet cell topology. |

Q0 cannot substitute for Q1. QD/QE are additional scopes, not shortcuts to Q2.
No profile currently has a passing evidence bundle under this proposal.

### Activation sequence

| Gate | Required evidence and authority | Failure outcome |
| --- | --- | --- |
| G0 independent bootstrap | Named domains, offline access, no circular dependencies, tested R/F controls | Observation only. |
| G1 durable custody | Acknowledged-write preservation, old-primary fencing, exact blob/key restore and owner checkpoint coverage | No effect-authorizing admission. |
| G2 owner protocols | #60 close-before-change and quota; #61 network effects; #62 stop; #63 identity; #64 if external; #65 cleanup | Affected capability remains unpublished/disabled. |
| G3 qualification evidence | Exact profile, required scenarios, failed-run history, owner acceptance and independent adversarial review | No production enablement; missing evidence is unknown. |
| G4 scoped activation | R/current boundary receipts and owner Open revisions; explicit asset/cell/provider allowlist and bounded rollout | Never global enable by default. |
| G5 continuing validity | Current identity/firmware/configuration and trust, evidence freshness, no unresolved overlapping custody | Close affected gates before effective revocation; retain cleanup. |

Changing a critical dependency invalidates the affected profile before deploying
it with effect permission. A restart does not restore permission. Stop rollout by
closing gates and settling existing custody, not by rolling storage back or deleting
records. This is ongoing safety custody, not backwards compatibility machinery.

## Acceptance scenarios

These are **executable-in-future specifications**, not tests run by this PR.
The future harness must inject faults before and after each relevant durable
commit, reorder/duplicate deliveries and retain a machine-readable event trace.
Each run records profile/seed, exact identities and epochs, acknowledged journal
sequences, actual boundary sends, independent physical observations, gates,
occupancy/quota/reservations, input digests and accepted/rejected evidence.
Negative claims require boundary instrumentation, not absence of controller logs.

For every row, pass also requires no unauthorized send, no unexplained occupancy
or quota loss, no success from missing evidence, and no competing authoritative
writer. A missing observation is an inconclusive/failed gate, never a pass.

| ID | Setup and fault/action | Pass criteria |
| --- | --- | --- |
| D01 | Cold bootstrap while all customer supply, customer DNS and tenant networks are off | M/R/F/A and read-only C recover without allocating/installing customer supply; effect gates remain closed until G0–G4. |
| D02 | Admit/acknowledge a binding and S1; kill primary and pause old primary before promotion, then resume it | Qualified replica contains both commits; old primary cannot write/send. Without preservation proof, promotion refuses and restore is required. |
| D03 | Remove synchronous replicas, exhaust WAL disk, then submit new allocation/operation | No acknowledged authorizing commit falls back to async; existing ambiguity/custody survives; recovery reserve remains usable or independent containment is invoked. |
| D04 | Race owner gate closure/revocation and profile expiry with allocation and operation renewal | #60 transaction order admits only valid winning revisions; no stale receipt/watch/cache bypass; already admitted work follows finite #62 boundaries. |
| D05 | Partition hub/M/C before delivery and during cancellation; replay deliveries in reverse order | Same operation/inputs, bounded pre-closure authority only; central cancellation never claims physical settlement without C/F receipts. |
| D06 | Pause broker immediately after S1, lose response, start replacement and resume old process | Replacement cannot send until old egress/compute excluded and downstream work settled; uncertain non-deduplicated step is never blindly replayed. |
| D07 | Revoke boot token and stop proxy while hostile RAM installer keeps writing | No successor/readiness from fetch revocation; qualified host/all-feed stop and post-stop new boot evidence are required. |
| D08 | Rotate BMC password while an old accepted reset is queued; fail the network fence service | Rotation is not settlement; no takeover or reuse until independent queued-effect/fence proof. |
| D09 | Restore M before an acknowledged allocation/release while C, quota or provider retains later history | Effects start disabled; union includes missing commitment/holds/tombstones; epoch advances only after fencing; no blank predecessor/free capacity. |
| D10 | Restore only C or provider journal before S1; deliver old valid-looking hub envelope | Restored broker starts observation-only; old envelope cannot activate; independent histories mark missing send unknown and require recovery. |
| D11 | Replay pre-restore Open gates, qualification approval and Ready/release result into a new lineage | No owner gate opens automatically; fresh current evidence and explicit per-scope activation required. R2/R3 rules remain unchanged. |
| D12 | Partition R into minority/majority, then lose all R history and present an old backup | No two activation quorums; no cached/gratuitous genesis activation; history-loss scope stays disabled absent separately qualified external rebootstrap. |
| D13 | Delete a blob/key version, corrupt manifest bytes or change an image tag after admission | Exact verification blocks affected execution; no silent rerender/refetch; scope remains held for authorized settlement. |
| D14 | Disconnect KMS/artifact service mid-step; expire fetch authority with cached content present | Only already admitted bounded step may finish; cache cannot renew authority or start the next step. Missing stop evidence remains unknown. |
| D15 | Substitute NIC/BMC/disk or remove one feed mapping during physical recovery | #63 identity/continuity fails; all-feed isolation not accepted; no release/new execution until owners requalify exact mapping. |
| D16 | Delete consumer/provider projects during uncertain execution and release | Journals, exact secrets, holds, obligations and scoped cleanup authority survive; same-name replacement inherits nothing. |
| D17 | Partition network owner after desired detach, with old flows and delayed switch writes | No disconnect/no-reuse success until #61 effective fence and dispositions; Fleet never configures switches itself. |
| D18 | Restore archives with inconsistent per-owner checkpoints and missing newer physical permit | No fictitious common recovery point; conservative union reserves all possible targets/locations; incomplete enumeration quarantines enclosing domain. |
| D19 | Step trusted clock backward or exceed declared uncertainty | No new deadline-based admission/send; restoration of time alone does not settle existing effects or reopen gates. |
| D20 | Install qualified non-Talos ManagedImage, then run customer workload without a managed cluster or ongoing guest agent | Exact image/disk evidence, prior installer stop, new local boot and owner network handoff validate; customer workload check is recorded separately from control-plane health. |
| D21 | Run delegated PXE whose origin mutates and whose success callback is forged | No replay of substituted content, no callback-only success, no QualifiedImage; only verified stop/handoff plus DelegatedContract may be reported. |
| D22 | Lose external Reserve/Commit reply, then R3 acknowledgment; restart provider and replay old messages | Same identities resolved under #64; lock/unknown custody persists; supply stays non-reusable until qualified R3 and fresh gates. |
| D23 | Revoke release evidence after quota acknowledgment but before R2; restore old certificate | #65 quarantine/occupancy and accounting exception retained; no fabricated new hold, force-free or certificate resurrection. |
| D24 | Remove one independent management zone, then entire M region/facility C | Qualified single-domain failover preserves acknowledged history; wider failure halts admission and follows fenced restore, without claiming an unmeasured RTO. |
| D25 | One operator tries restore, changed-scope approval, expired approval or direct journal edit | Production roles deny bypass; two distinct authorized approvals bound to current scope/version required; even two approvals cannot replace evidence. |
| D26 | Tamper firmware/profile/adapter digest during canary; let evidence expire | Close affected qualification dependency before new effects; unaffected scope continues only with proven disjointness; cleanup remains explicitly gated. |
| D27 | Exhaust archive/retention headroom and request GC of an unsettled secret or replay tombstone | Admission backpressures; referenced recovery material survives; terminal compaction cannot enable stale snapshot/message resurrection. |
| D28 | Attempt reactivation while one old broker, network writer or provider is unreachable and target list is incomplete | No silent scope reduction; enclosing domain remains denied/unknown. Separately proven disjoint scope may reopen with recorded containment evidence. |

| D29 | Withdraw catalog/offering-assignment access while an existing allocation requests an otherwise authorized runtime action; then independently close its runtime suspension gate | Withdrawal rejects new acquisition but preserves retained runtime capabilities subject to current actor/input permissions and separate safety gates. The independent suspension rejects the runtime action. Neither event alone creates a ReleaseCase or authorizes destructive cleanup. |

For Q0, instrument synthetic boundaries and all race schedules. Q1/QD/QE must
repeat relevant rows on real qualified hardware/provider paths with independent
observers; all rows require an applicability decision signed by the relevant
owner, not omission. Q2 requires those evidence bundles, production-topology
restore drills and independent adversarial review. Documentation checks below
are not a substitute for any scenario.

## Prerequisites and tradeoffs

| Owner | Blocking deliverable |
| --- | --- |
| Platform/storage | Concrete synchronous PostgreSQL/promotion proof, restore interlock, protected service roles and ledger extension hooks. |
| Security/recovery | Independent R/KMS/PKI and finite activation mechanism, two-person recovery, old-signer fencing, offline identities, tested R-loss procedure before any reactivation claim. |
| Infrastructure/facilities | Independently procured M/C, real failure-domain map, F network/compute/power controls, exact all-feed mappings and emergency access. |
| IAM/project/quota/catalog | #60 ordered gate participation, protected escrow and deletion interlocks; no assumed Milo support. |
| Networking | #61 realization/fencing/claim disposition and independently operable enforcement with no alternate stale writer. |
| Execution/inventory | #62 physical settlement and immutable input custody; #63 trusted enrollment, component continuity and independent evidence. |
| Provider/release | #64 all-channel exclusion/restore evidence where used; #65 assurance recipes, settlement and R3. Draft text is not implementation. |
| Operators/qualification | Measured profile limits, executable harness, evidence retention, drills, scoped rollout and independent review. |

Availability is intentionally lower than designs that expire locks or trust stale
permission. Independent recovery infrastructure and archive retention cost money;
three local cell failure domains may exclude some sites. Those exclusions are
explicit rather than weakened qualification. RTO outside the tested synchronous
envelope is unbounded. Restore cannot guarantee reconstruction of erased history;
quarantine is the safe outcome when it cannot.

Rejected alternatives: self-hosting recovery on managed customer capacity;
Kubernetes leader leases as physical fencing; asynchronous replica promotion as
zero-loss failover; auto-reopening a restored Open gate; global readiness based
on HTTP health checks; a deployment-owned release/allocator; TTL-based freeing;
and treating a signature, backup completion or operator approval as physical proof.

## Documentation validation and implementation history

This directory contains only Markdown and no diagrams, generated manifests,
credentials or runtime tests. Validate repository-relative links, GitHub-style
local anchors, exact pinned source paths and whitespace before publication.
Acceptance D01–D28 is future work. No runtime, hardware or provider qualification
is claimed by documentation validation.

- 2026-11-14: Draft authored on isolated main-based branch; #65 discovered and
  pinned. Independent review and all owner/production approvals remain pending.

[bmc]: https://github.com/datum-cloud/fleet/blob/f04e63ddd9d10cbf306daf3a0232961debd85e7f/docs/enhancements/secure-bmc-access.md
[management]: https://github.com/datum-cloud/fleet/blob/fa07c2236801a33a58efc914b555586c8e146cc6/docs/enhancements/fleet-management/README.md
[offerings]: https://github.com/datum-cloud/fleet/blob/a922ff1eb3275e26af6ae750a7515ddde245a754/docs/enhancements/profile-access/README.md
[pxe]: https://github.com/datum-cloud/fleet/blob/d7ff793b03711203dbfc9fac87d43922cc649b6f/docs/enhancements/pxe-installation/README.md
[lifecycle]: https://github.com/datum-cloud/fleet/blob/d7ff793b03711203dbfc9fac87d43922cc649b6f/docs/enhancements/machine-lifecycle/README.md
[allocation]: https://github.com/datum-cloud/fleet/blob/2563b2a2f177a72d20c1d62265c89ff15688ba25/docs/enhancements/allocation-admission/README.md
[network]: https://github.com/datum-cloud/fleet/blob/ed6edec5b056fd53343e11d58d979af495f1bd7c/docs/enhancements/network-handoff/README.md
[execution]: https://github.com/datum-cloud/fleet/blob/7fa823050a443c1f5b85c56e85ed04ebaa5bd2cb/docs/enhancements/execution-recovery/README.md
[inventory]: https://github.com/datum-cloud/fleet/blob/8fe054101ac3bea822e5d16e40cb375a884e7e56/docs/enhancements/inventory-trust/README.md
[fulfillment]: https://github.com/datum-cloud/fleet/blob/50e678ee20645ad0a4e340005b18432fe4633bec/docs/enhancements/provider-fulfillment/README.md
[release]: https://github.com/datum-cloud/fleet/blob/fae0cd3bf2a3a7e208c92c3e46c9b5670d877906/docs/enhancements/release-reuse/README.md
