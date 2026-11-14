---
status: provisional
stage: alpha
latest-milestone: null
---

# Inventory lifecycle and trust

Uses the [Datum enhancement template][template]. This is a **concrete proposed
protocol**, not implemented APIs, verified hardware support, or production
readiness. Internal record and operation names are conceptual. Existing alpha
code and synthetic environments do not implement or qualify this protocol.

- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
- [Design details](#design-details)
- [Acceptance tests](#acceptance-tests)
- [Production readiness review questionnaire](#production-readiness-review-questionnaire)
- [Implementation history](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure needed](#infrastructure-needed)

## Summary

Fleet inventory owns physical identity, installation plans, physical reservations,
and attributable observations. Select one protected inventory transaction module
in the management-side PostgreSQL ledger selected by [allocation admission
#60][allocation]. Extend that ledger; do not introduce a second enrollment,
occupancy, allocation, or provider-retirement authority. Project API objects remain
the canonical operator intent surface; retained accepted snapshots and decisions
are the commit authority, not another editable desired-state store.

Accept an entire immutable installation graph atomically, enroll actual equipment
against an exact accepted revision, and evaluate separate identity, placement,
network-path and power-path predicates. Resource names, serials, reachability and
discovery alone establish none of those predicates. Physical reservations persist
through failure, maintenance and uncertainty. Admission and evidence invalidation
serialize on #60's authority gates before controlled changes become effective.

Use qualified two-person technician enrollment for passive equipment and a
separate technician-bootstrapped, authenticated BMC/commissioning profile for
servers. Neither profile presumes TPM, manufacturer attestation, trustworthy
SMBIOS, or a BMC capable of signing arbitrary challenges. Unsupported hardware or
missing evidence stays unavailable; an operator cannot override it to Ready.

## Motivation

Plans can double-book a rack or circuit, a forged serial can enroll another
project's server, and a stale cabling observation can route an operation to the
wrong hardware. Treating observed location as approved placement also conceals
unauthorized moves. These are custody and trust failures, not status formatting
problems. The protocol needs a durable winner before work and a recoverable
unknown state afterward.

### Goals

- One canonical physical asset and one active placement across provider projects.
- Atomic rack, termination and upstream power reservations before field work.
- Explicit trust bootstrap, identity replacement and evidence invalidation.
- Safe moves, repairs and retirement without losing existing consumer custody.
- Narrow inventory evidence contracts for the allocator and networking owner.

### Non-goals

Only standalone Fleet-backed Metal machines and their supporting physical
equipment are in scope. No managed nodes, clusters or node services; no API
compatibility, migration or legacy dual-write machinery. Observational adoption
of occupied equipment remains required, but is not a migration architecture.
Allocation/quota, executor fencing, networking, provider fulfillment, release and
deployment retain their own contracts. This proposal neither procures equipment
nor chooses an installer, network control plane, or deployment topology.

## Proposal

### User stories

A technician can discover a wrongly wired server without rewriting its approved
plan. Two operators cannot reserve the same rack space through different project
names. A maintenance coordinator can move an allocated server while preserving
its allocation and both old and new physical reservations. After a restore,
unknown equipment remains occupied until custody is reconciled.

### Baseline and precedence

Drafted on 2026-11-14 from fetched `origin/main` at
`8ebec19513a0cbc8f219d3acae46f89ab471510b`. Companion heads were read through GitHub
at the revisions below. These are immutable source references, not imported files
or dependencies on merging their branches. In particular, this branch contains
no inherited PXE changes.

| Source | Pinned revision | Contract used |
| --- | --- | --- |
| [Original inventory proposal][inventory-source] | `51781e2456367be82281e1443ca56d0686311133` in infra | Six physical kinds, trusted binding, observational adoption and full physical paths. |
| [Fleet management #51][management] | `fa07c2236801a33a58efc914b555586c8e146cc6` | Project ownership, control-plane writers and distinct identities. |
| [BMC access #41][bmc] | `f04e63ddd9d10cbf306daf3a0232961debd85e7f` | Private management endpoints and allocation-scoped effect authority. |
| [Offerings #58][offerings] | `a922ff1eb3275e26af6ae750a7515ddde245a754` | MachineOffering product policy; one-hop consumer publication. |
| [PXE #59][pxe] | `d7ff793b03711203dbfc9fac87d43922cc649b6f` | Exact Installation inputs, qualified boot mapping, admitted preparation and operations. |
| [Allocation admission #60][allocation] | `2563b2a2f177a72d20c1d62265c89ff15688ba25` | Supplied signed-off architectural contract: ledger, gates, enrollment, occupancy, custody and restore epochs. |
| [Network handoff #61][network] | `ed6edec5b056fd53343e11d58d979af495f1bd7c` | Current reviewed head: physical mapping, owner corroboration, disconnect and disposal evidence. |

The [repository design](../../architecture/design.md), [inventory background](../../proposals/fleet-management/inventory.md)
and [provenance](../../proposals/fleet-management/MIGRATION.md) provide context.
Use the current pinned #51/#58 naming over the original proposal's MachineClaim
and provider MachineClass shapes. #60 governs all allocation and custody ordering;
this proposal supplies its inventory adapter. #61 governs network effects.
Calling #60 signed-off describes the supplied architectural baseline, not runtime
qualification or independent review of this new draft.

### Constraints and risks

Metal is the public machine service; Fleet is an optional provider. MachineOffering
owns public capability policy, not physical topology or another inventory pool.
Non-Fleet providers need equivalent provider-fulfillment guarantees, not fabricated
Fleet Devices. Private inventory, serials, cable paths, evidence, BMC endpoints
and credentials are never published into consumer catalogs or status.

The trust boundary includes vetted technicians, inventory service identities,
qualified collectors, protected management paths, storage and platform lifecycle
interlocks. Two colluding enrollment approvers or a compromised root authority can
lie about physical identity; software uniqueness does not defeat that threat.
Require separated roles, audit, periodic physical inspection and quarantine on
conflict. Do not claim unforgeable hardware identity on equipment without it.

## Design details

### Identities and sole writers

`Device != MachineCapacity != MachineAllocation != Machine != Installation`.
The first is a physical asset, the second supply, the third exclusive accepted
custody, the fourth consumer intent, and the fifth an OS incarnation. A DeviceClaim
is a physical installation plan, not any of those identities.

Exact references retain API authority, Project UID, resource kind, namespace UID
where applicable, name and object incarnation. Human-authored references use names;
the service resolves and authorizes both ends before retaining exact identities.
Recreated objects never inherit authority. Each physical graph initially belongs
to one provider project; cross-project physical graph sharing and ownership
transfer are disabled. Global duplicate detection is nevertheless mandatory.

| Surface | Sole writer and responsibility |
| --- | --- |
| Facility | Authorized facilities planner owns premises identity proposal, Location reference and constraints. Registry accepts canonical premises identity; verifier owns observations. |
| Rack | Facilities planner owns geometry, Facility reference, mounting zones and limits. Reservation coordinator alone accepts occupancy; verifier records actual placement. |
| DeviceClaim | Installation planner owns candidate expected identity, placement, ports, required Cable references and policy selection. Coordinator accepts revisions; registrar commits binding; verifier owns predicates. |
| Device | Registrar alone creates canonical asset identity, component incarnations and active placement relation. Collectors submit observations, never identity/spec edits. |
| Cable | Cabling planner alone owns expected typed endpoints, lanes and pass-through mappings. Coordinator reserves endpoints; verifier owns observed paths. Device and network controllers cannot rewrite this intent. |
| PowerFeed | Qualified facilities electrical planner owns rated/derated limits, upstream graph and failure scenarios. Coordinator owns reservation accounting; electrical verifier owns measurements and trace evidence. |
| MachineCapacity | Provider capacity controller owns supply and immutable Device association; reads inventory qualification. #60 owns enrollment acceptance and occupancy, not this controller. |
| Network and management configuration | Networking owns logical attachment, IPAM, routing and switch policy. Provider executor owns BMC configuration and hardware effects under separately admitted operations. |

Facility/Rack/DeviceClaim/Device/Cable/PowerFeed are operator-only project resources
in `fleet.datumapis.com`, with an operator namespace as in #51. Planner writes are
intent, not acceptance. IAM separates plan author, approver, registrar, evidence
issuer, verifier and hardware executor. No principal may approve its own enrollment
or write its own successful verification. Status is a protected projection.

### Protected records and transaction boundary

Use #60's service-controlled ledger, synchronous durable commit and SERIALIZABLE
transactions with enforced constraints on every writer, including recovery tools.
Records survive either project's API deletion. Kubernetes finalizers, watches,
cell leases and multiple resource-version checks are not commit protocols.

| Record | Retained content; sole transition writer |
| --- | --- |
| Existing AuthorityGate / ProviderGuard | Reuse #60. Inventory adapter owns scoped inventory/trust gates; retirement coordinator owns ProviderGuard. No duplicate guard. |
| CanonicalIdentity | Opaque global asset or premises key, qualified matching aliases, issuer domain, custody origin, tombstone and duplicate case; registrar. |
| PhysicalPlanRevision | Immutable canonical input digest, exact dependency snapshots, complete geometry/endpoints/power vector, expected predecessor and approval; reservation coordinator. |
| ReservationDecision / PhysicalReservation | Attempt/digest, commit sequence, resource keys, retained/successor interests and state; reservation coordinator. All members commit together. |
| EnrollmentChallenge / EnrollmentBinding | Nonce digest, target, profile, approvers, expiry, consumed decision and reciprocal DeviceClaim/Device binding; registrar. |
| Existing Enrollment | Same #60 enrollment row, extended with trust/placement/evaluation revisions. Inventory adapter writes its evidence fields; no parallel enrollment table can authorize supply. |
| Evidence / Evaluation | Immutable issuer report and verifier conclusion, complete dependency vector, deadlines and invalidations; evidence ingress appends reports, verifier appends evaluations. |
| InventoryTransition | Move/repair/removal state, expected revisions, allocation/operation references and obligations; inventory transition coordinator. No independent execution authority. |
| Existing RecoveryOutbox / AdmissionDecision | Same ledger mechanisms as #60, including retained work and append-only decisions. Transaction owner inserts; publisher changes delivery acknowledgment only. |

Opaque CanonicalIdentity is an identity registry, not a second supply occupancy
table. EnrollmentBinding records a physical claim relationship; existing Enrollment
remains the sole allocatable asset-to-supply association. Enforce unique live
canonical asset, unique bound claim, unique Device incarnation, and at most one
active placement per asset. Components have stable slot IDs plus replacement
incarnations; reusing a slot never reuses its evidence.

Every transaction follows #60's lock order: sorted complete authority gates,
ProviderGuard, attempt, quota custody, Machine binding, enrollment, occupancy,
skipping inapplicable classes. Append one inventory-domain lock, then sorted
canonical identity, plan and physical-resource keys. No code locks an inventory
row and then an earlier #60 row. The initial domain lock serializes physical
acceptance and graph mutations; allocation commits still only need their complete
gate/enrollment/occupancy checks. Never make remote calls while holding SQL locks.
Unique indexes arbitrate absent keys; retry serialization failures from fresh inputs.

Inventory uses #60 close-before-change for every controlled identity, topology,
limit, trust-policy and accepted-plan mutation. Initially use a coarse inventory
domain gate plus the issuer/trust gates, rather than an incomplete dependency index.
Close and record the mutation before changing effective inputs; commit accepted
snapshots/evaluations and reopen with that exact mutation/epoch only. Incomplete
reevaluation leaves eligibility Unknown. Candidate edits do not change accepted
authority. A pending revocation cannot be reported effective without its closure
receipt. Expiry is checked with trusted ledger time at commit even without a watch.
Every eligible Enrollment references the current inventory gate epoch; reopening
a coarse gate cannot make evaluations from its preceding epoch valid. The adapter
must replace those evaluations or leave the corresponding supply ineligible.
Only the adapter holding the matching mutation ID may finish inventory writes
under its Closed gate; this is not an exception for allocation or effect admission.
For planned effectful maintenance, the operation owner's ledger-local hook must
admit and retain the exact maintenance operation while the prerequisite gates are
still Open, atomically with closing the inventory gate and recording the transition.
The retained operation may subsequently execute only its approved steps, after
reservation, quiescence and field-work prerequisites are satisfied. Closure is not
cancellation of that already-admitted operation, as specified by #60. This avoids
inventing a general-purpose admission bypass for Closed gates. If unexpected drift
closed a gate first, only the execution owner's qualified recovery/settlement path
may admit necessary safety work; absent that contract the transition stays blocked.

### Canonical uniqueness and duplicate handling

The registrar issues a random immutable asset key only after adjudicating a
canonical identity case in the shared ledger. It indexes normalized manufacturer
and qualified chassis serial, procurement/custody record, operator asset tag,
bootstrap BMC key fingerprint where available, and physical inspection evidence.
Normalization is profile-versioned; empty/default serials cannot create a unique
identity. Serial, SMBIOS UUID, MAC, hostname, IP and labels are matching evidence,
not authenticators. Component serials belong to component incarnations, not an
alternative chassis key. A component cannot be live in two enrolled chassis.

Search and lock candidate aliases globally, including retired identities and
unknown adopted custody, before creation. An existing canonical match returns
the existing decision only for the same request and permitted owner. Otherwise
open `DuplicateIdentity`, close affected eligibility gates, and retain all custody.
Ambiguous matches require two authorized registry reviewers independent of the
requester. They may associate an observation with the existing asset or reject
the new claim; they cannot merge allocations, retarget Devices or clear occupancy.
A legitimately reused manufacturer serial needs documented provenance and a
qualified distinguishing identity, not a force-create flag. If distinction cannot
be demonstrated, enrollment is denied. Mere malicious duplicate submissions do
not suspend another project: quarantine its candidate immediately, and invalidate
the existing asset only after authenticated conflicting evidence is verified.

Facility premises and Rack identities also have one canonical custodian and alias
registry. A second project cannot create another capacity island by naming the
same premises/rack differently. Importers must resolve existing canonical keys;
unresolved premises identity blocks acceptance. One physical graph owner may use
read-only mirrors with provenance, but mirrors cannot reserve. Global registry
queries return a generic conflict to unauthorized callers, not another tenant's
identity or location. Independent allocation domains and cross-domain asset moves
remain disabled as required by #60.

### Atomic physical plans and reservations

`AcceptPlan(attemptID, digest, expectedRevision, dependencyVector)` is idempotent;
the same ID with different content returns `IdempotencyConflict`. The coordinator
resolves the whole graph and permissions outside SQL, then validates exact gate
epochs, source revisions, provider state and all resource constraints inside one
transaction. Retain immutable inputs, the acceptance decision, every reservation
and projection outbox together. Durable commit is the only `Accepted=True` point.
Lost acknowledgment queries the original decision on the current writer.

```text
planner candidate -> approve exact graph -> coordinator checks trusted revisions
                                            |
                     one SERIALIZABLE commit
                     [plan + rack + endpoints + power + decision + outbox]
                                            |
                        Accepted projection (not field-work authority)
                                            |
                  separate admitted work -> enrollment -> verification
```

Resource keys and constraints are concrete:

- Rack mounting uses canonical Rack, mounting zone, half-open U interval, face
  and depth envelope. Full-depth overlaps either face; partial-depth placement
  requires a qualified non-overlap geometry. Include weight and cooling envelopes
  where required by facility policy. 0U uses exclusive named rail zones; floor
  equipment uses explicit Facility zones, not fabricated rack units.
- Each exclusive termination/lane has a stable reservation key. Claim ports get
  provisional keys before a Device exists. Binding atomically aliases those keys
  to exact component-incarnation keys and checks direct Device reservations too.
  Conflict aborts the whole binding; neither reservation is silently overwritten.
- Cable revisions own reservations at both ends. Multiple claims may reference
  the same exact Cable reservation via retained interests; they do not reserve
  it twice. Release requires all interests settled. Opposite unbound claims may
  be accepted in one graph batch, without requiring either Device Ready. Changing
  a referenced unbound plan requires atomic readmission of every affected plan
  and Cable interest, or rejection. No stale revision can later bind.
- Patch-panel internal paths and breakout lanes are explicit immutable mappings
  with typed compatibility and fanout limits. Physical paths must terminate
  without ambiguous cycles. Logical LAGs/VLANs are not Cable endpoints. Unsupported
  sharing, multiplexing or geometry returns `TopologyUnsupported`.
- Power reservations include every upstream edge and node described below, not
  only outlets. All connected pending plans, occupied equipment and unknown
  adopted load count. Missing limits or upstream paths are a blocker.

Accepted revisions are immutable. Editing an API spec creates a candidate, not
an in-place change to accepted placement. A successor plan references its exact
predecessor. Retain the union of old and new reservations until each relinquished
resource is proven free; a readmission cannot pretend the old hardware vanished.
For unchanged keys, transfer an interest atomically without double charging it.
For changed locations/paths, hold both; any lower transitional power estimate
requires an explicitly qualified scenario, otherwise use conservative sums.

Worker lease expiry and request deletion never release physical resources.
Cancelling an unused plan may release its reservations only after all potential
field-work authorities are durably terminal and authoritative records prove no
work could have occurred. Otherwise require physical clearance evidence. A
timeout is `OutcomeUnknown`, not evidence of an unused reservation.

### Power capacity and redundancy accounting

PowerFeed is the authority for a versioned directed acyclic supply graph, covering
facility source, UPS/transfer equipment, circuits, breakers, PDU inputs and outlet
paths. A PDU is a Device whose component map links outlet to upstream input.
Qualified electrical personnel approve continuous-load derating, per-phase limits,
voltage/power-factor bounds, fault domains and supported transfer behavior.
Separate names or labels do not establish independent feeds.

For each required failure scenario `s` and each supply element `e`, calculate
`reserved(e,s) = sum(loadEnvelope(reservation,e,s))`. Admission requires every
result at or below its approved usable limit, including phase current and apparent
power limits where relevant. Envelopes include startup/inrush constraints and
worst-case surviving-PSU demand; observed idle draw is never admission headroom.
Reject unknown conversions, unqualified automatic transfer, cycles, and unknown
upstream dependencies. Keep arithmetic units, rounding-up rules and graph digest
in the retained plan for reproducibility.

The initial redundant profile requires normal operation plus loss of each one
of the declared independent source domains. Trace every inlet to a qualified
source boundary and prove surviving paths can carry the full load. For a 600 W
server on A/B feeds, reserve a possible 600 W on each surviving path, not 300 W
each. Two PDUs behind the same UPS cannot satisfy UPS-loss redundancy. A common
upstream element must also carry the aggregate possible demand of all devices;
per-outlet headroom alone is insufficient. Multiple simultaneous failures or
unqualified multi-input transfer topologies are unsupported, not implicitly safe.

Upstream limit reductions or dependency corrections close the domain gate before
becoming effective. Existing overcommitted reservations stay held and report
`PowerConstraintViolation`; new admissions stop. Do not reduce accounting to match
the new limit or power off tenants. An observed physical failure is recorded as
drift immediately; safety procedures may require separately authorized action,
but inventory readiness never supplies power-control authority.

### Trust roots and qualification profiles

Security operators install an inventory enrollment trust bundle through an
authenticated, two-person approved management bootstrap. Pin its version/digest
and issuer identities in protected custody. Separate roots authorize registrar
workload identities, technician identities and collectors; TLS transport identity
does not grant approval or evidence authority. Credentials live in secret custody,
not Device specs, logs, Git or public status. Collectors receive narrow subjects,
checks, sites and validity; a cell has local scoped credentials, not project/hub
write credentials. Trust rotation closes relevant gates, retains old verification
keys for audit and revokes their ability to authorize new evidence.

Qualification is a versioned, security-approved capability matrix per equipment
family and facility workflow. A plan cannot select a weaker profile than facility
or intended offering policy requires. Each profile lists supported checks,
trust assumptions, evidence issuers, freshness and invalidating events. All
profiles below need implementation and real-hardware qualification before use.

| Profile | Required enrollment proof | What it cannot claim |
| --- | --- | --- |
| `PassiveWitnessed-v1` | Two distinct authorized technicians, one independent approver, fresh physical challenge and exact accepted plan; chassis/tag/port labels, position and cable trace evidence | No host agent, BMC, boot authentication, or active compute eligibility. |
| `ServerWitnessedBMC-v1` | Two-person physical chassis-to-BMC bootstrap; qualified TLS endpoint pinned out of band; fresh read-only BMC channel probe; isolated commissioning boot binding before boot eligibility | No hardware-rooted firmware attestation, trusted SMBIOS or proof against colluding technicians/BMC compromise. |
| `ObservedOnly-v1` | Authorized observer provenance and available asset/placement facts, explicitly incomplete | No trusted binding, free capacity, ownership transfer, preparation or effects. |

PassiveWitnessed covers passive panels and unpowered accessories. An active switch
or PDU may have its physical identity enrolled by witnessed inspection, but its
telemetry or control endpoint is not trusted through that fact. Such endpoints
require an additional qualified authenticated collector bootstrap; absent support
leaves automated adjacency/power predicates unavailable. A separate qualified
manual trace can prove physical cabling, not switch configuration or live power.

For ServerWitnessedBMC, the technician verifies a chassis asset tag and serial,
physical management port and exact endpoint key fingerprint at the equipment using
a qualified local workflow. Default shared credentials, unauthenticated HTTP,
TOFU over an unknown network and matching an IP are insufficient. The initial
profile requires an already authenticated endpoint with credentials/key provenance
established out of band. Hardware needing unauthenticated bootstrap writes is
not qualified by this protocol; registration cannot use preparation to bypass its
own identity prerequisite. Subsequent credential/certificate changes belong to
the provider executor under the maintenance and secret-rotation protocol, not the
registrar. Until authenticated read-only probes are supported, enrollment cannot
qualify as a server profile. Manufacturer certificates or TPM quotes may support a future
stronger profile; they are not prerequisites presumed present or implemented.

### Enrollment sequence and anti-replay

1. Record a proposed EnrollmentChallenge under an accepted plan and complete
   current authorization. Generate a cryptographically random 256-bit nonce;
   retain its digest, challenge ID, plan/dependency digest, target asset candidates,
   issuer/audience, expected enrollment epoch, recovery epoch and ten-minute
   deadline. A challenge grants observation only, never boot/power permission.
2. Technicians authenticate with individual phishing-resistant credentials on
   managed devices. Each signs the exact challenge/plan and evidence digests.
   The independent approver performs a separate onsite identity/placement check,
   not a blind approval of the first person's upload. The qualification procedure
   requires fresh challenge-bearing inspection and asset/port scans; a photograph
   alone or authentication alone does not prove physical presence. Recheck both
   roles and separation at commit. Unqualified remote attestations are denied.
3. For servers, a qualified collector connects using the approved pinned BMC TLS
   identity and records a read-only identity probe bound to this challenge and
   fresh channel session. The collector signs its report. This is a collector
   attestation of a fresh authenticated connection, not a fictional BMC-signed
   nonce or proof of trustworthy firmware. Unexpected key or chassis facts block.
4. Registrar validates profile, evidence signatures, candidates and original
   authority; the serializable transaction consumes the challenge once, creates
   or resolves CanonicalIdentity, commits reciprocal claim/Device binding and
   endpoint aliases, and updates #60 Enrollment with eligibility disabled. It
   commits a decision and outbox together. A stale plan, expired nonce, changed
   trust epoch or conflict aborts without partially binding.
5. Same challenge/request/digest retries return that exact committed binding.
   Reuse with other input is rejected. Expired unconsumed challenges are terminal;
   a successor has a new ID and cannot consume the predecessor's attestations.
   Consumed nonces and enrollment tombstones survive restarts and restoration.

Binding authenticates the physical relationship, not a complete installation.
Before a server can supply boot-dependent capability, the operation owner admits
explicit commissioning/preparation against disabled, non-conflicting supply using
#60 occupancy and ProviderGuard. Boot inspection is an effect and cannot run on
occupied adopted equipment merely to finish enrollment.

Networking must first prove an isolated commissioning path with exact port/NIC
mapping under #61. Establish that initial mapping from qualified physical port
trace and authenticated inventory facts before boot, not from the boot callback
being authenticated. A family that cannot supply this pre-boot mapping is blocked;
commissioning cannot bootstrap its own trust by reporting a matching MAC.
The boot service issues a single-use capability bound to asset,
component incarnation, plan, commissioning operation/fence, selected cell and
expiry. The qualified commissioning image establishes an ephemeral key and returns
a challenge response over that bound path; the service consumes it once and
retains the measured inventory and transcript digest. Distribution and response
must be confidential and integrity protected by a qualified boot/path profile.
MAC, DHCP source, LLDP, an iPXE callback or agent self-report alone cannot bind a
host to a chassis. If protected delivery and port isolation cannot be established,
boot identity is Unknown and boot-dependent offerings stay disabled. No ongoing
guest agent is required. This proves the qualified physical/path association, not
hardware-rooted measured boot; offerings requiring that stronger property cannot
use this profile.

### Evidence and accepted versus observed placement

Reports contain issuer/key and profile version, evidence ID and monotonic issuer
sequence, exact subject/component and placement incarnations, challenge or admitted
operation binding, input/dependency digests, method/vantage, raw artifact digest,
observed time, receipt time, deadline, trust/recovery epoch and explicit checked
predicates. Protected evidence storage is immutable and authenticated; missing
bytes or an unverifiable signature cannot be replaced by a cached summary.
Verifier acceptance is a separate append-only Evaluation, never issuer-written
Ready. Conflicting qualified observations yield `EvidenceConflict`, not newest-wins.

Initial policy ceilings are 24 hours for witnessed placement/cable/electrical
trace, five minutes for live BMC/adjacency/telemetry reports, and ten minutes for
enrollment challenges. Enrollment identity binding itself is durable, not refreshed
by repeatedly consuming a nonce; fresh operation-time predicates are still required.
Longer static inspection windows require a separately qualified profile with
documented tamper controls. No indefinite static Ready assumption is selected.
Policy may shorten ceilings, never extend an existing report's validity. Require
bounded trusted time (maximum 30-second uncertainty for these profiles); otherwise
block new evaluations/admissions. The usable deadline is the minimum issuer,
profile, dependency and authority deadline, conservatively reduced by clock error.
Late receipt does not reset observation age; future-dated evidence is rejected.

Invalidate dependent predicates on plan/Cable/PowerFeed revision, component or
BMC key replacement, trust revocation, detected move, port mismatch, upstream
change, recovery epoch change or expiry. A broad gate closure safely covers all
affected supply until dependency-specific reevaluation completes. Observation
ingress durably records a verified conflict and closes that gate in the same
ledger transaction. Controlled physical work must close gates before work;
unannounced physical tampering cannot be retroactively serialized. The remaining
detection window is bounded by qualification freshness and independent effect-time
checks; no claim of instant detection is made.

Accepted placement is the retained approved plan and registrar's single active
placement relation. Observed placement is evidence, even when it differs. Never
rewrite accepted rack/port mappings to make evidence agree. Report independent
`Accepted`, `Bound`, `IdentityVerified`, `PlacementVerified`, `NetworkPathVerified`
and `PowerPathVerified` predicates. Missing/stale evidence is Unknown, proven
mismatch False, accepted unfinished work Pending. A check can be NotApplicable
only through explicit qualified policy (for example power for a passive panel).
An aggregate Ready requires all applicable required predicates but is not itself
an admission token. Physical drift blocks dependent new work; existing allocations,
network connectivity and reservations do not disappear when Ready becomes False.

### Allocator and network handoff

These are service-internal semantic interfaces, not public routes. Only the
inventory adapter writes #60's inventory gate and Enrollment fields; callers
cannot submit their own Ready flag. Inventory emits no boot or power commands.

| Interface | Required contract |
| --- | --- |
| `InspectInventory(subject, predicates)` | Exact asset/Device/optional capacity and binding identities, accepted plan, trust/component/placement revisions, evaluation digest, predicate results, dependency gate vector, recovery epoch and conservative validity. Read-only; no reservation. |
| `PublishEligibility(evaluation)` | Verify all required predicates and exact Device/capacity association; update existing #60 Enrollment and reopen its gate only at the expected mutation/epoch. Allocation rechecks deadlines and complete gates at its own commit. Eligibility says nothing about unoccupied/free custody. |
| `PhysicalMapping(subject, slots)` | Authenticated evidence for all external NICs: component incarnation, stable identifier, qualified PCI/physical label mapping, permanent/observed boot MAC, firmware selector, peer/peer-port, Cable/path revisions and expiry. Unknown fields are explicit and block predicates that require them. |
| `BeginInventoryChange(transition, expectedVector)` | Close relevant gates and retain provider obligation; planned effectful work also uses the operation owner's atomic admission hook before closure in the same #60 transaction. Returns closure/transition receipt, not an execution capability. |
| `InspectTransition(id, digest)` | Durable decision, retained reservations, known observations and unsettled obligations; missing or unreachable history is Unknown, not an abort certificate. |

Networking independently corroborates PhysicalMapping and owns attachment/IPAM
identities. Inventory cannot attest that a NetworkBinding is programmed or a
path isolated. The operation owner retains both inventory evidence and #61
network evidence as exact inputs. Mismatch/expiry requires blocked admission or
an exact successor operation after predecessor settlement, not picking another
NIC. Mapping must cover unrequested usable NICs too. Bond/LAG, SR-IOV or shared
uplink support is disabled until the corresponding mapping/fencing unit qualifies.

For operation delivery, management writes exact intent to the hub; the selected
cell writes local results; Karmada reflects; management validates the retained
target, issuer, epoch, revisions and freshness. Reflected status is transport,
not inventory truth. Inventory gate closure does not cancel previously admitted
effects: the executor and network owners must fence them before movement or trust
replacement. #41 sessions use the same effect domain, never independent BMC authority.

### Adoption without effects

Discovery creates ObservedOnly evidence, not a Device, accepted plan, free supply
or work request. An authorized operator explicitly accepts an observational
custody case: first close the inventory gate and retain its obligation, then
commit conservative physical reservations before reopening any affected admissions.
If an allocation already won before closure, retain it as an overlapping custody
conflict for owner reconciliation; never overwrite it. Import unknown rack/port
occupancy and upstream load before allowing any competing plan; if geometry/load
is unknown, quarantine the containing rack/feed domain instead of assuming zero.

The #60 allocator/custody owner records `AdoptedOccupied` or `Unknown` occupancy
from that case before the inventory adapter can enable publication eligibility.
Inventory never writes or clears allocation occupancy. This durable adoption
hold may exist before a trusted Device binding, so a duplicate later enrollment
cannot route around it. Registration then follows explicit witnessed observation;
no reboot, credential replacement, cleaning, network change or host agent install
is implied. Existing tenant/provider authority is recorded, not usurped.

An explicit ownership transition requires authorized prior custodian and provider
approval, retained active-use obligations, compatible management authority and
release-owner settlement of adopted occupancy before reuse. If those proofs are
unavailable, keep Unknown indefinitely. Boot qualification waits for separately
authorized preparation on non-conflicting supply. Importing records never briefly
publishes occupied equipment as free, even if physical verification succeeds.

### Movement and maintenance

Only one nonterminal InventoryTransition may change a given asset/placement.
Shared switch/PDU/rack work enumerates all dependent assets and obligations; the
coarse inventory gate covers dependency additions racing that enumeration. The
operation owner must recheck the frozen dependency set before admitting effects.
An unenumerated dependent or new reservation rejects progression.

```text
Requested -> GatesClosed -> DestinationReserved -> EffectsQuiesced
                |                                    |
                |                         FieldWorkAuthorized -> InProgress
                |                                               |
                +-> cancel only after settled no-work proof      v
                           PlacementCommitted <- NewPlacementVerified
                                  |
                         OldResourcesCleared -> Complete

Any uncertain effect/clearance -> BlockedUnknown (retain both reservation sets)
```

1. Retain transition ID, exact asset/allocation set, predecessor placement, scope,
   approved disruption consent/window and immutable candidate destination graph.
   Have the operation owner admit the exact bounded maintenance operation and
   close gates/create the ProviderGuard obligation in one transaction as above.
   Destination changes require a new admission, not editing that operation.
   Reject new allocations/ordinary work;
   do not clear active occupancy or change the accepted offering contract.
2. Atomically accept a successor DeviceClaim and reserve the destination plus its
   Cable/power graph. It is a pending placement, not a second active claim. Keep
   the old placement and reservations, including unknown paths.
3. The operation owner rechecks the retained maintenance consent covering
   disruption to every active consumer, fences conflicting executor/BMC sessions
   and requests #61 disconnect where required. Retain operation/network receipts
   and exact fences. Missing consent, isolation or quiescence blocks physical
   movement. A device-level inventory permission is not tenant power permission.
4. Only then issue a one-use field-work permit through the operation owner,
   bound to transition, asset, exact old/new plans and recovery epoch. Technicians
   record start before touching equipment and attributable completion afterward.
   A permit's deadline prevents starting new work, but cannot prove work stopped;
   the executor's settlement rules and physical inspection resolve uncertainty.
5. Verify actual destination, identity, all required cables and power dependencies
   at the new revisions. In one transaction CAS the old active placement to the
   successor and record the new evaluation. The predecessor remains history and
   its physical reservations remain held pending explicit clearance.
6. Exact rack vacancy, disconnected terminations and load-removal evidence allow
   the coordinator to release each old reservation once all retained interests
   settle. Transfer Cable references explicitly; they do not follow a Device name.
   The operation owner resumes the same allocation only with new mapping/network
   evidence and settled predecessor effects. Reopen required gates and settle
   the provider obligation after all transition obligations complete.

A same-cell/location move preserves Device, MachineCapacity and MachineAllocation.
It does not mutate an Installation's retained inputs; any subsequent operation
must be freshly admitted against valid placement. A move requiring another
allocation target cell/location is blocked in this initial protocol because #60
retains that identity. It requires explicit release and later new allocation,
not inventory-level retargeting. Failure after field work cannot roll the database
back to an assumed physical position. Returning to the old rack is another
authorized movement with fresh verification; both reservations remain until proven.

### Component repair and whole-asset replacement

Maintenance without movement uses the same closure, consent, quiescence and
settlement protocol. For a NIC/disk/PSU replacement, preserve chassis Device identity
only with verified continuity; create a new component incarnation and invalidate
all dependent mapping, hardware-profile and power evidence. The provider must
revalidate retained MachineProfile requirements before resuming the allocation.
A disk replacement does not assert the previous Installation/data remains intact;
the installation/release owners decide permitted recovery, never inventory.

A BMC key, motherboard or root identity change closes trust gates and revokes old
management credentials, boot capabilities and sessions through their owners.
Retain both identity histories. Two-person chassis-continuity evidence plus a new
enrollment challenge must bind the replacement; the old BMC cannot authorize its
successor alone. Old component keys/nonces remain tombstoned. Until the executor
proves old paths fenced and new probes qualify, no operations resume. If chassis
continuity cannot be proved, handle it as whole-asset replacement, not an alias edit.

Whole-server replacement always creates a new DeviceClaim, Device and
MachineCapacity. Never retarget an existing MachineAllocation or transfer its
MachineBinding to that hardware. Release/settlement of the predecessor and an
explicit new Machine request remain the respective owners' work. Shared serial
or reused rack/BMC address triggers identity review, not inherited authority.

### Removal and retirement

`RetireRequested` first closes eligibility and adds a retirement obligation under
the same ProviderGuard used by #60. Public deletion is a request, not completion.
Check all dependent claims, reservations, active/adopted/unknown occupancy,
allocations, sessions, preparations, network bindings, outboxes and transitions.
An active allocation blocks asset retirement unless its owner explicitly enters
the release protocol. Inventory cannot force eviction or mint a cleaning result.

After execution/release owners prove fencing and terminal settlement, #60 alone
settles quota and clears occupancy. #61 alone proves disconnect and per-claim
disposition. Physical removal is separately admitted; verify rack vacancy, cable
termination clearance and upstream load removal before releasing physical holds.
Logical network deletion is not proof that a port is unplugged. Physical removal
is not proof that remote authority, data obligations or quota have settled.

One final inventory transaction requires all obligations terminal, no active
placement/reference interests, acknowledged or tombstoned outbox work and exact
clearance evidence. It marks the asset Retired and retains canonical identity,
binding, nonce and component tombstones. Tombstones do not expire into reusable
identity; restrict/anonymize nonessential evidence separately under retention policy.
A returned retired asset needs explicit reactivation review, fresh enrollment epoch
and new placement, and cannot resurrect old capacity/allocation authority.
Reactivation is limited to the same surviving provider-project incarnation and
canonical Device identity. It requires a new DeviceClaim and supply incarnation,
with #60 confirming the predecessor enrollment has no unsettled custody before
updating the sole enrollment association. It is not identity reuse or permission
to retarget an active allocation. A deleted owner or different project needs a
future custody-transfer contract and remains blocked here.
Facility/Rack/PowerFeed/Cable retirement similarly waits for every dependent
reservation/reference and clearance obligation, including shared upstream loads.

Provider project retirement uses #60's existing Closing guard and lifecycle
interlocks, extended to include these physical obligations. No second inventory
retirement coordinator can authorize project purge. Preserve credentials and
evidence required for settlement; unknown or forced deletion enters disaster
recovery. No cross-project custody transfer is enabled to evade a blocked purge.

### Recovery and restoration

On ordinary crash, read authoritative decisions and resume the same IDs/digests.
Transaction loss means no committed acceptance; response loss is resolved by
decision lookup, not retries with new identity. Outbox duplication changes neither
reservation ownership nor effects. Never infer freedom from a missing API object,
stale replica, collector lease, or absent consumer Machine.

Include inventory plans, physical holds, canonical aliases, challenges, evidence
digests/bytes, transition permits and tombstones in #60's consistent custody backup.
Use its independent monotonic recovery epoch, not a new counter in this backup.
Deployment must fence the old writer and executor owners must fence old effects
before recovery opens any gates. Disable automatic outbox/permit replay and mark
all uncertain supply and physical domains unavailable. An old enrollment challenge
or evidence signature cannot regain validity after restoration.

Reconcile WAL/decisions, protected identity journals, remote operation histories,
network custody and qualified onsite surveys. Unmatched physical work or a newer
remote permit becomes an unknown retained reservation; do not free either possible
location. Reconcile upstream power accounting conservatively for that union.
Restore exact project/object incarnations or keep blocked; same-name replacements
cannot inherit. Reopen only after current roots, new evidence, old-writer fencing
and all custody decisions agree. Irretrievably missing history is quarantine, not
an operator's declaration of an empty database.

## Acceptance tests

These are proposed executable qualification scenarios, **not tests implemented or
run by this documentation PR**. Use synthetic identities in automation, plus a
controlled real-hardware qualification facility. Assert ledger decisions, all
reservation rows, admitted effects and consumer privacy, not just Ready status.
Inject crashes immediately before/after every durable commit and acknowledgment.

| ID | Adversarial setup or interleaving | Required result |
| --- | --- | --- |
| I1 | Concurrent plans reserve overlapping full-depth/front/rear U ranges | Exactly one acceptance; loser has no partial endpoint/power holds. Unsupported partial-depth geometry denies. |
| I2 | Crash after acceptance commit but before API status/outbox acknowledgment | Retry original ID returns identical plan and reservations; no second field-work permit. |
| I3 | Claim endpoint binds while another plan reserves its direct Device port | Alias uniqueness serializes both; at most one Cable owns the termination. Failed binding leaves original reservations intact. |
| I4 | Two unbound claims connected by one Cable; race a referenced plan edit | Atomic batch accepts without readiness deadlock; edit either readmits all interests or fails. No stale challenge binds. |
| I5 | Two individually valid power plans together exceed one upstream breaker | Only one commit, across all normal/failure vectors and per-phase limits; no outlet-only admission. |
| I6 | Dual 600 W server feeds share UPS, or surviving path supports only 400 W | Redundancy denies; distinct labels and current 200 W draw do not repair qualification. |
| I7 | Concurrent provider projects enroll the same qualified asset aliases | One canonical Device/enrollment; second is blocked without revealing the other project. No duplicate capacity occupancy island. |
| I8 | Enroll with copied serial/MAC, old photo, expired challenge or same approver twice | No trusted binding. Readiness and effects remain unavailable; challenge cannot cross plan, asset, issuer or recovery epoch. |
| I9 | Replay successful challenge after lost response, then change its digest | Exact retry returns original Device; changed digest conflicts; only one nonce consumption and reciprocal binding. |
| I10 | BMC address reused with different TLS key; no TPM or BMC nonce signer available | Wrong key denies. Qualified witnessed profile can use collector probe without claiming hardware attestation; insecure BMC cannot qualify. |
| I11 | Spoof boot MAC/callback, connect extra NIC or break protected bootstrap delivery | Boot identity/path gate fails; no first-NIC fallback, broad commissioning access or successful guest self-attestation. |
| I12 | Passive panel registers correctly but live switch telemetry lacks trusted endpoint | Passive identity may verify; live network/control predicates remain Unknown. No invented agent/BMC requirement for the panel. |
| I13 | Close inventory gate concurrently with allocation commit | #60 determines one order: prior allocation remains held, or allocation fails. No admission using an older reopened epoch. |
| I14 | Evidence expires at commit; issuer supplies future timestamp or delayed replay | Conservative deadline/sequence/recovery checks reject; receipt time cannot refresh old evidence. Clock uncertainty blocks. |
| I15 | Observed rack/peer differs while approved plan remains unchanged | Preserve both facts, report mismatch and invalidate dependent eligibility. No plan rewrite, eviction or occupancy release. |
| I16 | Move allocated server, lose disconnect acknowledgment, then retry | No field-work permit until query proves exact fence/disconnect; allocation and old/new physical holds persist. |
| I17 | Crash after physical move but before placement commit; old rack claimed by another plan | Both reservation sets remain; new contender denied; recovery surveys and completes same transition, not optimistic rollback. |
| I18 | Repair NIC/BMC with reused slot/IP and replay pre-repair evidence/session | Component/trust epochs reject old proof; executor must fence old sessions before fresh qualification/resume. |
| I19 | Attempt whole-server replacement or cross-cell move under existing allocation | Reject retargeting; require explicit release/new consumer allocation for new asset/target. |
| I20 | Import occupied server with incomplete power/rack facts | Zero provisioning/network effects; adopted/unknown custody and containing-domain holds precede any eligibility. |
| I21 | Delete Machine/provider project during physical maintenance or quota settlement | Protected guard, reservations, secrets and evidence survive; purge blocked until all owners acknowledge settlement. |
| I22 | Network disposal succeeds but rack vacancy remains unknown, or vice versa | Retirement remains blocked; neither evidence substitutes for the other owner. |
| I23 | Restore old backup while a newer permit and enrollment exist remotely | Old writer/credentials fenced; epoch advances; unknown union reservations retained; old challenge and outbox replay rejected. |
| I24 | Shared PDU removal races admission of another dependent claim | Gate/domain lock orders admission; dependency set is frozen/rechecked; missing consent blocks all disruptive work. |
| I25 | Duplicate allegation from unauthorized project; real authenticated conflict later | First submission cannot deny another tenant's service; verified conflict closes affected gates and preserves custody. |
| I26 | Correct profile references but missing independent evidence bytes or issuer authorization revoked | Evaluation fails closed; signature validity alone cannot bypass current issuer/trust gates. |
| I27 | Consumer reads offering, capacity summary or duplicate error | No serial, private endpoint, physical path, evidence handle or another project's allocation leaks. |
| I28 | Retire and recreate same object names; reactivate returned chassis | Old bindings/permits never revive; explicit identity review and new enrollment/placement epoch required. |
| I29 | Cable interest shared by two claims; one is cancelled/removed | Physical endpoint remains reserved until all interests and actual clearance settle. |
| I30 | Lower upstream limit below existing commitments; worker lease expires | Report violation, close admissions and retain full reservations; no auto-power-off or timeout-driven capacity release. |
| I31 | Race planned maintenance admission with trust revocation; gate already Closed on retry | Either the exact operation commits with closure before revocation, or ordinary admission fails. A mutation token never authorizes effects; unexpected drift needs qualified recovery authority. |

## Production readiness review questionnaire

### Enablement and rollback

Enable per qualified facility/equipment/profile, initially with gates Closed.
Require identity registry bootstrap, #60 interlocks, independent evidence issuers,
all required #61 paths and successful acceptance scenarios before opening.
Disabling closes new enrollment/plan/admission gates while retaining custody and
authorized cleanup. Rollback may not drop table constraints, epochs, retained
snapshots or tombstones. There is no legacy compatibility mode.

### Monitoring and troubleshooting

Monitor aged unknown reservations, duplicate cases, expired evaluations, trust
revocations, unconsumed/uncertain permits, power scenario utilization/violations,
closed-gate duration, transaction conflicts and restoration backlog. Alert before
freshness ceilings expire. Inspect exact decision, dependency vector, physical
hold, evidence provenance and owner receipt in that order. No operator runbook
may recommend deleting reservations or forcing Ready. Implementation must supply
protected inspection/recovery commands; none are invented here.

### Scaling and dependencies

One physical-domain lock favors correctness over write throughput. Bound graph
size, component count, evidence upload size and per-issuer rates; reject over-limit
plans before acceptance, never truncate a reservation set. Immutable raw evidence
can live in protected object storage with digest and durable retention committed
before referencing it. It cannot become another mutable authority. Eventual
projections may lag without weakening admissions. Qualification must measure
inspection workload, gate fanout, SQL contention and evidence retention costs.

### Recovery expectations

No acknowledged-write-loss failover is required as in #60; older snapshot restore
is a distinct fenced workflow. Production enablement requires an exercised restore
with lost acknowledgments, offline cells and incomplete physical surveys. Recovery
time may be unbounded when physical custody cannot be established; safety takes
precedence over promising a Ready deadline.

## Implementation history

- 2026-11-14: Standalone draft authored against the pinned sources. Documentation
  checks and author self-review do not constitute independent adversarial sign-off,
  security/facilities owner approval, hardware qualification or merge approval.

## Drawbacks

Two-person inspection and daily static-evidence ceilings are expensive, and the
conservative power model reduces sellable capacity. Global uniqueness requires
protected cross-project matching metadata. Coarse gate closure can pause unrelated
new work. The protocol deliberately accepts these costs rather than using stale
evidence, split reservation authorities or weak identity as readiness.

## Alternatives

- Kubernetes-only reservation/status updates cannot atomically accept a complete
  physical graph or order with #60 admission; rejected as the commit mechanism.
- A separate inventory database with asynchronous eligibility replication creates
  a second enrollment/gate ordering problem. Reuse #60's ledger with separate
  field writers instead; public project intent does not move into a new API.
- Serial/MAC auto-enrollment and unsigned technician uploads cannot authenticate
  chassis identity; rejected. Hardware-rooted attestation may qualify a future
  profile but is not assumed on today's equipment.
- Automatically editing expected placement to match observations hides drift;
  reject in favor of explicit successor plans and maintenance consent.
- Expiring physical reservations improves utilization only by assuming equipment
  disappeared. Explicit clearance and retained unknown custody are required.

## Infrastructure needed

These are unresolved **enablement prerequisites**, not deferred choices about
which protocol to implement. Missing support means fail closed.

| Owner | Required approval and qualification |
| --- | --- |
| Ledger/admission | Inventory module, shared lock order, gate adapters, global identity/endpoint uniqueness, physical reservations and ProviderGuard obligations within #60's ledger. |
| Milo/IAM | Service-only writers, two-person role separation, exact cross-project references, no-bypass source mutation/deletion interlocks and private projections. |
| Security/facilities | Trust roots and managed technician workflow; qualified BMC TLS bootstrap, physical identity continuity and passive evidence procedures; bounded clocks and evidence retention. |
| Electrical/facilities | Canonical premises/racks, full upstream source graph, derating/phase/inrush envelopes, failure scenarios and qualified inspection/clearance. |
| Executor/BMC | Explicit preparation, atomic maintenance-admission/closure hook, qualified recovery admission under invalid inventory, one effect-fencing domain, replay-safe field-work permits, session/key revocation, quiescence and uncertain-work settlement. |
| Networking | #61 qualified commissioning isolation, independent complete NIC mapping corroboration, protected boot delivery, disconnect and claim disposition receipts. |
| Provider/release | Adopted occupancy settlement, retained profile revalidation, hardware identity continuity, consumer maintenance consent and release certificates; inventory cannot substitute for them. |
| Deployment/recovery | Protected evidence/secret custody, synchronous ledger durability, old-writer fencing and #60 independent recovery epochs with demonstrated restoration. |

[template]: https://github.com/datum-cloud/enhancements/blob/b5fe372ca85b50c674a9ce41419ce5210cf33f6d/enhancements/template/README.md
[inventory-source]: https://github.com/datum-cloud/infra/blob/51781e2456367be82281e1443ca56d0686311133/docs/enhancements/fleet-management/inventory.md
[management]: https://github.com/datum-cloud/fleet/blob/fa07c2236801a33a58efc914b555586c8e146cc6/docs/enhancements/fleet-management/README.md
[bmc]: https://github.com/datum-cloud/fleet/blob/f04e63ddd9d10cbf306daf3a0232961debd85e7f/docs/enhancements/secure-bmc-access.md
[offerings]: https://github.com/datum-cloud/fleet/blob/a922ff1eb3275e26af6ae750a7515ddde245a754/docs/enhancements/profile-access/README.md
[pxe]: https://github.com/datum-cloud/fleet/blob/d7ff793b03711203dbfc9fac87d43922cc649b6f/docs/enhancements/pxe-installation/README.md
[allocation]: https://github.com/datum-cloud/fleet/blob/2563b2a2f177a72d20c1d62265c89ff15688ba25/docs/enhancements/allocation-admission/README.md
[network]: https://github.com/datum-cloud/fleet/blob/ed6edec5b056fd53343e11d58d979af495f1bd7c/docs/enhancements/network-handoff/README.md
