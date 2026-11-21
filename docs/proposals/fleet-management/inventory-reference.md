---
status: provisional
stage: alpha
latest-milestone: "v0.1"
---

# Fleet inventory reference appendix

Technical detail supporting the [Fleet inventory product proposal](inventory.md).
Start there for operator needs, the proposed experience and open decisions.
This appendix keeps field definitions, synthetic examples, source findings,
migration steps and test requirements together for implementers and reviewers.
It is part of the same enhancement, not a separate design.

All contracts and field names remain provisional. Examples are not implemented
CRDs or manifests to apply; illustrative identifiers and evidence do not establish
physical truth. Local object incarnations are retained privately rather than
used as authored cross-control-plane references.

For the implemented local-only Site membership and provider-qualified premises
identifier subset, see [Site–Facility relationship](../../development/site-facility-relationship.md).
Its name/UID references are same-project, same-namespace incarnation pins, not
authored cross-control-plane IDs or allocation policy.

- [Contract details](#contract-details)
  - [Metadata and evidence](#shared-metadata-references-and-evidence)
  - [Facility and Rack](#facility-and-rack-contracts)
  - [Assets and components](#asset-identity-and-typed-components)
  - [Installation examples](#plan-a-physical-installation)
  - [Reservations](#reserve-physical-resources)
  - [Registration](#register-and-bind-a-device)
  - [Verification](#verify-the-installation)
  - [Cabling and power](#cabling-power-and-network-configuration)
  - [Circuit boundary](#circuit-demarcations-and-external-ownership)
  - [Lifecycle transitions](#moves-replacements-and-retirement)
- [Existing records and migration](#existing-records-and-migration)
- [Source evidence and limits](#source-evidence-and-limits)
- [Validation and qualification matrix](#validation-and-qualification-matrix)

## Contract details

### Shared metadata, references, and evidence

The following are proposed contract semantics, not an assertion that these fields
are available in the current API. Keep the initial six kinds; use structured
entries and protected transition records rather than a kind for each work order,
photo, model, approval, or verification run.

| Surface | Authoritative writer | Contract |
| --- | --- | --- |
| Facility/Rack definitions, DeviceClaim intent, Cable topology, PowerFeed supply plans | Authorized inventory planner | Changes produce reviewed revisions; they cannot rewrite registered observations or prove physical work. |
| Accepted plans, reservations, active placement and historical bindings | Inventory coordinator/registrar | Conditional durable transitions retain approval, exact inputs, and predecessor identities. Planner and collector writes cannot bypass them. |
| Device identity, asset tags and canonical component inventory | Registrar | Adjudicates authenticated observations and corrections; collectors never directly replace canonical identity. |
| Observations and evaluated conditions | Authenticated evidence submission; inventory verifier owns the projection | Preserve source observations separately from approved intent; only the verifier writes verification results. |
| Management endpoints and access | Provider management authority | Restricted references/projections only; inventory does not allocate OOB addresses, issue credentials, or authorize BMC actions. |
| Provider host IDs, service/circuit identifiers and commercial facts | Respective provider/service authority | Inventory retains sourced identifiers and physical associations, not orders, invoices, pricing, or routing intent. |

All kinds need a human-readable label, optional description, provenance, and
restricted external references. Names and labels are not durable asset identity.
For each imported fact retain source system, source object type and ID, source
revision or snapshot digest when available, observation time, importer identity
and mapping revision. Keep claimed source authors separate from the authenticated
submitter. Conflicting sources require adjudication; last import wins is not a
trust rule. Absence, undisclosed information, a mismatch, and policy-approved
inapplicability are distinct states, never encoded as invented values or zero.

Typed references identify the allowed kind/group, administrative scope, and name.
A kind-specific field such as `rackRef` fixes kind/group; examples omit scope only
for same-scope references. Cross-scope references require explicit scope and
authorization; they never resolve by a global name search. Acceptance pins the
resolved local incarnation and relevant revision privately. Name reuse, dangling
references, unauthorized targets, and wrong-kind targets cannot inherit evidence.
Foreign source IDs are provenance, not directly resolvable Fleet references.

`documentRefs` refer to photos, as-built sheets, installation attestations, LOAs,
or contracts in their existing document system. Retain document ID, category,
revision/digest when available, capture time, and the exact asset/plan or path
being supported. Documents keep their own ACLs; a Fleet reference grants no
access and contains no document body, token, signed download URL, or credential.
A mutable URL alone cannot establish evidence for a prior evaluation. A contract
or LOA is not proof of installed cabling; a photo requires an attributed
interpretation and qualified check. Unavailable evidence leaves dependent checks
Unknown rather than copying protected material into public status.

Audit history retains who requested, approved, and committed a change, its reason,
before/after revisions, supporting references, and affected identities. Retain
supersession, identity corrections, component removals, and retirement events;
do not rewrite history to match the present plan. Retention and deletion policy
must preserve evidence needed by active reservations and consumer obligations.

### Facility and Rack contracts

| Kind | Proposed minimum and optional physical metadata | Validation and lifecycle |
| --- | --- | --- |
| Facility | Stable identity, display name, geographic `locationRef`, and physical-premises description. Optional structured postal address, building/room labels, coordinates with source/precision, operator identity reference, external facility-code mappings and document references. Sensitive access instructions stay external. | A metro code alone is insufficient to establish premises. Operator approval establishes or corrects a premises mapping. Closing a Facility blocks new plans; it neither relocates equipment nor releases occupancy. Preserve historical references. |
| Rack | Immutable `facilityRef` after accepted use, local rack label, optional room/row, mounting geometry and capacity, supported faces, depth/width limits, optional load limit with units, 0U mounting locations, and document references. Unknown dimensions remain absent with provenance. | Rack labels are unique within an agreed Facility scope, not globally. Known rack-unit numbering has a documented origin/direction; position denotes the lowest occupied unit. Reject out-of-range footprints, face/depth conflicts, and incompatible mounts. Do not infer usable space from omitted geometry. |

A Facility is not a provider account, provider metro grouping, or Fleet policy
Site. One physical premises may have several provider codes; one imported metro
grouping may refer to several premises. Mapping records retain both directions
and unresolved candidates without creating a false one-to-one relationship.
Site policy selects eligible Facilities as described in
[Site rules for choosing eligible facilities](cluster-lifecycle.md#site-and-cluster-resources).

Rack geometry changes that affect reservations require revalidation under the
coordinator, not a metadata edit that evicts occupants. Correcting a mistaken
Facility association requires an audited migration of dependent references and
evidence; physically moving a rack requires coordinated placement transitions
for its occupants. Deletion is blocked while active claims, Devices, Cables, or
feeds depend on the object. An empty inventory list does not prove an empty rack.

### Asset identity and typed components

Device identity includes manufacturer/model, independently evidenced chassis
serial when available, and optional SMBIOS UUID. Asset tags are separate
operator-assigned identifiers with issuer/scope and assignment history; replacing
a tag is not replacing a chassis. Provider host IDs belong in namespaced external
identifiers, never `serialNumber`. Serial, UUID, MAC, model, label, or asset tag
alone is not authentication or universally unique. Missing serials on passive
equipment require qualified technician identity evidence, not a fabricated
serial. Duplicate or conflicting matches remain quarantined for adjudication.

An expected serial or asset tag may be unknown when planning. The accepted claim
pins its matching constraints and qualified hardware shape; registration records
actual identity, records discrepancies, and does not silently relax constraints.
Device role describes physical use, not cluster membership or allocation state.

Repeated models justify a reusable, versioned **model/port shape**, initially a
reviewed document or configuration artifact rather than a new public kind. It
describes manufacturer/model/variant, dimensions, component slots, labels,
connector capabilities and internal pass-through or power mappings. Claims pin
the shape revision/digest and explicit deviations; updating a template never
retrofits an accepted plan or invents observed components. Provider marketing
models and VM flavors cannot establish chassis geometry. Qualification must
compare the shape to actual hardware variants.

Each canonical component entry has a Device-local stable ID, a discriminated
type, physical label, optional parent/slot, relevant capabilities, provenance,
and retained incarnation/revision evidence. The registrar owns the canonical
entry; mutable observations and OS aliases are separate. Reject duplicate IDs,
parent cycles, incompatible fields, and references to removed incarnations.

| Component type | Physical contract | Explicit exclusion or restriction |
| --- | --- | --- |
| Network port | Connector/media family, supported rates with units, lane/position capabilities, optional MAC observations and replaceable transceiver association. Management/Data is a role, not a separate identity system. | No VLAN, bond, IP allocation, BGP peer, or switch configuration. An OS name is only an observed alias. |
| Power inlet/outlet | Input/output direction, connector and AC/DC compatibility, supported voltage/current limits with units; outlet-to-input dependency for a PDU. | No assumption that two inlets or outlets are independent. Unsupported transfer-switch mappings remain unknown. |
| Console port | Serial/USB/Ethernet connector and host/server direction, with physical compatibility parameters where required. | No session credentials or console access authorization. A networked console server's Ethernet port remains a network port. |
| Pass-through termination | Front/rear side, connector, numbered positions, and explicit internal position mappings for a passive panel. | Never infer a direct link through a panel from LLDP alone. |
| Replaceable NIC, optic, PSU or disk | Parent slot, actual component identity/serial where disclosed, model/capabilities and insertion/removal history. Disks may retain observed WWIDs and capacity with units. | A disk is not a Cable endpoint; installation pins exact disk targets separately. A slot reused after repair is not the same component incarnation. |

Only fields relevant to a component type are allowed. Supported type/connector
vocabularies and required units must be fixed by the first qualified schema;
unknown vendor strings may be retained as observations but cannot pass typed
connection validation. Claim port IDs express intended attachment points and
resolve explicitly to canonical component IDs, never by a guessed array index.

For example, a synthetic observed panel shape can contain:

```yaml
# Proposed component excerpt, not a complete Device manifest.
components:
  - id: panel-front-01
    type: PassThroughTermination
    side: Front
    connector: LC
    positions: [1, 2]
  - id: panel-rear-01
    type: PassThroughTermination
    side: Rear
    connector: LC
    positions: [1, 2]
passThroughMappings:
  - from: {componentID: panel-front-01, position: 1}
    to: {componentID: panel-rear-01, position: 1}
  - from: {componentID: panel-front-01, position: 2}
    to: {componentID: panel-rear-01, position: 2}
```

These are internal device mappings, not a second expected-cable graph. Cables
remain the only declarations connecting different assets.

### Plan a physical installation

The DeviceClaim contract retains role, expected identity constraints, placement
scope, declared attachment points, required Cable references, verification
profile/checks, provenance and approval revision. Where power is reserved, the
plan also pins a maximum load budget with units and the required failure scenario
per supply path. Missing load assumptions block power-capacity acceptance; the
number of PSUs does not supply them. Model-shape references and predecessor/asset
references for moves or replacements are optional inputs that become immutable
within an accepted revision. They do not grant permission to evict a predecessor.

The Facility describes physical premises. A Fleet `Site` can select eligible
Facilities as operational policy without moving their assets. Location remains
the geographic reference; neither Site nor Facility is a provider identifier.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Facility
metadata:
  name: iad1
  namespace: infra-ops
spec:
  displayName: Ashburn Facility 1
  locationRef:
    name: us-east-1
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: Rack
metadata:
  name: iad1-r12
  namespace: infra-ops
spec:
  facilityRef: {name: iad1}
  heightUnits: 42
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: DeviceClaim
metadata:
  name: iad1-r12-u20-server
  namespace: infra-ops
spec:
  role: Server
  expectedIdentity:
    manufacturer: Dell
    model: PowerEdge-R650
    serialNumber: EXAMPLE-SERVER-001
    assetTag: {issuer: datum-infra, value: EXAMPLE-ASSET-001}
  placement:
    rackRef: {name: iad1-r12}
    position: 20
    heightUnits: 1
    face: Front
    fullDepth: true
  verification:
    requiredChecks: [Identity, RackPlacement, NetworkPaths, PowerPaths]
  ports:
    - {id: bmc, type: EthernetPort, role: Management, physicalLabel: BMC}
    - {id: nic1-p1, type: EthernetPort, role: Data, physicalLabel: NIC1/P1}
    - {id: psu1, type: PowerInlet, physicalLabel: PSU1}
    - {id: psu2, type: PowerInlet, physicalLabel: PSU2}
  cableRefs:
    - {name: iad1-r12-srv01-management}
    - {name: iad1-r12-srv01-data}
    - {name: iad1-r12-srv01-power-a}
    - {name: iad1-r12-srv01-power-b}
```

The example assumes an existing management switch `iad1-oob01`, data switch
`iad1-r12-leaf-a`, and PDUs `iad1-r12-pdu-a`/`iad1-r12-pdu-b`, each registered
through its own claim. Their referenced components and upstream power paths
must exist and be verified. These four Cable plans describe the server's
required connections before the server Device exists:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Cable
metadata:
  name: iad1-r12-srv01-management
  namespace: infra-ops
spec:
  medium: CopperEthernet
  a: [{deviceClaimRef: {name: iad1-r12-u20-server}, portID: bmc}]
  b: [{deviceRef: {name: iad1-oob01}, componentID: port12}]
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: Cable
metadata:
  name: iad1-r12-srv01-data
  namespace: infra-ops
spec:
  medium: DirectAttachCopper
  a: [{deviceClaimRef: {name: iad1-r12-u20-server}, portID: nic1-p1}]
  b: [{deviceRef: {name: iad1-r12-leaf-a}, componentID: ethernet20}]
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: Cable
metadata:
  name: iad1-r12-srv01-power-a
  namespace: infra-ops
spec:
  medium: Power
  a: [{deviceRef: {name: iad1-r12-pdu-a}, componentID: outlet20}]
  b: [{deviceClaimRef: {name: iad1-r12-u20-server}, portID: psu1}]
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: Cable
metadata:
  name: iad1-r12-srv01-power-b
  namespace: infra-ops
spec:
  medium: Power
  a: [{deviceRef: {name: iad1-r12-pdu-b}, componentID: outlet20}]
  b: [{deviceClaimRef: {name: iad1-r12-u20-server}, portID: psu2}]
```

`cableRefs` identifies prerequisites; it does not repeat expected peers. Cable
owns the endpoint declarations. A planned endpoint references a DeviceClaim
and its declared port ID; a registered endpoint references a Device and stable
component ID. Names are convenient authoring references. Acceptance retains the
exact local object incarnations privately and records approved dependency
revisions. Recreating an object with the same name never inherits a reservation.

Placement may instead name `placement.facilityRef` without a rack. This covers
unracked equipment and rented bare metal whose physical Facility is known but
whose rack/patching is not disclosed. The claim must state its verification
requirements explicitly; for example:

```yaml
# Excerpt for an identified physical server with facility-level visibility.
spec:
  placement:
    facilityRef: {name: iad1}
  verification:
    requiredChecks: [Identity, FacilityPlacement]
```

Admission constrains required checks using operator policy and the intended
offering. Authors cannot weaken verification to make a failed installation
pass. Rack placement derives Facility through the Rack; if both references are
supplied they must agree. Undisclosed rack/cable facts remain unknown, not
invented or marked physically inapplicable. `Ready` for a facility-scoped claim
does not imply verified rack position, cabling, power redundancy, or eligibility
for an offering that requires those facts. Historical UUID/IP/disk lists are
migration inputs, not sufficient verification evidence.

**Rack known, position undisclosed** is a separate supported placement scope,
not facility-only placement and not a fictitious 1U footprint:

```yaml
# Proposed excerpt for identified rented hardware; all identifiers are synthetic.
spec:
  placement:
    rackRef: {name: iad1-provider-rack-7}
    scope: RackMembership
  verification:
    requiredChecks: [Identity, RackMembership]
```

The proposed placement discriminant distinguishes `Facility`, `RackMembership`,
`RackUnits`, and `NonRackUnitMount`. Existing exact-position examples imply
`RackUnits`; facility-only examples imply `Facility`. The eventual schema must
make this choice unambiguous. RackMembership forbids position, face, height and
full-depth assertions unless a subsequent approved revision establishes an
exact mount. It permits known asset dimensions in the model, but dimensions
alone cannot establish occupied units. NonRackUnitMount requires a qualified
mounting-location identifier for 0U or other non-unit rack mounts. Facility
placement describes unracked or undisclosed-rack equipment without such a mount.

Acceptance for RackMembership records the known occupancy and may reserve known
terminations/power, but makes **no rack-unit reservation**. An unresolved
footprint prevents declaring the rack's unaccounted space free: block new exact
unit reservations in that rack until occupancy is reconciled, unless a qualified
operator-approved exclusion boundary proves a disjoint area safe. Do not guess
the exclusion boundary. Facility-only imports likewise cannot establish free
rack capacity. A plan requiring exact placement remains Pending until an exact
footprint is approved and reserved. `PlacementVerified` reports Facility,
RackMembership, or exact mount scope; RackMembership readiness cannot satisfy a
RackUnits prerequisite. Imported invented positions never satisfy any placement
check merely because they appear in a source DCIM object.

### Reserve physical resources

Acceptance commits a retained plan covering the claim and its required Cable
inputs. Inventory must reserve the applicable declared rack footprint, physical
terminations, and power capacity without races. Rack checks include occupied units,
front/rear constraints, full-depth equipment, and pending installations. The
model also supports 0U mounting and equipment outside a rack; it must not assign
them fictitious rack-unit positions.

Endpoint reservations belong to one exact Cable incarnation and identify one
exact Device or DeviceClaim incarnation plus termination/position. Local object
identity is retained as private same-API-server evidence. Two cables cannot reserve the same
exclusive termination. Resolving a claim endpoint to a registered component
must preserve that reservation, including conflicts with direct Device
references. A successor placement cannot reserve an occupied resource merely
because the previous object's readiness is False.

Use durable conditional commits or an equivalent serialized reservation
protocol, not an admission-time list check or controller-local lock. Partial
acquisition stays Pending and is recovered or explicitly unwound. Report
`Accepted=True` only after the complete current plan is committed. Hold
reservations through controller restarts and uncertainty; timeouts and expired
worker leases do not prove that space, cabling, or power is physically free.

Referenced Cable plans may target other unbound claims. Validate and reserve
the intended endpoints without requiring those Devices to be Ready. Otherwise
two devices awaiting a connection could never be commissioned. Editing an
unbound plan requires revalidation and a new committed plan revision; stale
acceptance or registration requests cannot use the previous revision.

### Register and bind a device

A registration request must authenticate its enrollment channel and identify
an existing accepted DeviceClaim and approved plan revision. Match expected
identity against independently trusted evidence. Serial numbers, MAC
addresses, and SMBIOS UUIDs are matching attributes, not authentication.
Passive devices register through an authorized technician or integration;
they do not need to run a host agent.

The registrar commits one reciprocal DeviceClaim-to-Device relationship and
retains both local object incarnations as private same-API-server evidence. One
claim binds one Device for one installation incarnation. One physical asset
cannot acquire duplicate Device identities,
and one Device has at most one active placement authority. Recovery after a
lost response finds the committed registration instead of creating a second
asset. Ambiguous matches, unexpected identities, expired authorization, or
already-bound claims fail closed. Unknown equipment remains isolated discovery
evidence; it cannot manufacture an accepted claim or eligible capacity.

Registration does not require all physical verification to have succeeded. A
Device can exist in commissioning with a placement or wiring discrepancy:

```yaml
# Registrar-created snapshot, not an operator-authored apply input.
apiVersion: fleet.datumapis.com/v1alpha1
kind: Device
metadata:
  name: server-example-001
  namespace: infra-ops
spec:
  # Registry-owned. A raw reference edit cannot move the asset.
  claimRef:
    name: iad1-r12-u20-server
  identity:
    manufacturer: Dell
    model: PowerEdge-R650
    serialNumber: EXAMPLE-SERVER-001
status:
  observedPlacement:
    rackRef:
      name: iad1-r12
    position: 20
    face: Front
    evidenceRef: installation-scan-001
  components:
    - {id: management-port-1, type: EthernetPort, physicalLabel: BMC}
    - id: nic-slot2-port1
      type: EthernetPort
      physicalLabel: NIC1/P1
      macAddress: "02:00:00:00:12:01"
    - {id: power-supply-1, type: PowerInlet, physicalLabel: PSU1}
    - {id: power-supply-2, type: PowerInlet, physicalLabel: PSU2}
```

Declared claim ports resolve using trusted chassis labels, qualified hardware
layout, and physical component identity. A controller cannot swap the mapping
of `nic1-p1` to a different NIC merely to make the observed neighbor match.
The Device records actual components; the claim records the approved mapping
from its intended ports to those components.

### Verify the installation

Commissioning has an ordered dependency graph:

```text
Accepted plan and physical reservations
  -> trusted registration and exclusive Device binding
  -> claim-port mapping and Cable endpoint resolution
  -> identity, placement, network, and power evidence evaluated
  -> physical Ready
```

Cable resolution depends on binding, not Device Ready. Protected enrollment
connectivity must exist before complete production-path verification. Any
inspection that boots or modifies a device requires a separately authorized
hardware operation; inventory readiness cannot authorize its own evidence
collection effects.

- **Hardware identity and capabilities:** Authenticated BMC/qualified agent observations
  or trusted installation evidence matched to the enrolled asset. Claimed identifiers
  alone are insufficient.
- **Switch and port adjacency:** Trusted switch-side observations corroborated with the
  enrolled NIC mapping. MAC learning or agent-reported LLDP alone does not prove asset
  identity.
- **Link capability:** Current port state, negotiated parameters, and relevant hardware
  observations; link-up is not proof of a complete passive path.
- **Exact rack position/orientation:** Authorized installation scans/attestations or
  qualified rack instrumentation. Management-network reachability does not establish a
  rack unit.
- **Passive cable identity and patch path:** Verified endpoint/cable-label scans or
  qualified instrumentation. LLDP does not identify each passive segment.
- **PSU, outlet, and upstream feed:** Verified physical mappings and suitable
  observations/instrumentation. Two healthy PSUs do not establish independent supplies.

Link discovery can supply switch/port observations through qualified collectors;
it does not establish every physical fact above. Redfish's rack/offset fields
are writable metadata, so copying the intended position into a BMC and reading
it back is not independent placement evidence. See the
[Redfish data model](https://redfish.dmtf.org/schemas/v1/DSP0268_2025.3.pdf).

Conditions report `Accepted`, `Bound`, `IdentityVerified`, `PlacementVerified`,
`NetworkPathVerified`, `PowerPathVerified`, and `Ready` separately. Readiness
requires every admitted required check for the current approved plan and
records the evaluated verification scope. `PlacementVerified` identifies
whether it evaluated Facility, RackMembership, or exact mount placement. Missing
or stale required evidence is Unknown; a verified mismatch is False. Checks outside the
scope remain explicitly unevaluated. A policy may mark a check physically
inapplicable, such as power for a passive panel; absence of evidence must not
silently become an exemption. Downstream controllers require the specific
verified facts their operation or offering needs, not an unqualified Ready bit.

Each evaluation privately retains the exact local claim, Cable, Rack, PowerFeed,
and Device incarnations and records their revisions, Device
identity/component/trust revision, evaluated input digest,
evidence provenance, and verification time. Standard condition fields include
`observedGeneration`, reason, message, and transition time. The owning object's
generation alone is insufficient: recabling, a NIC replacement, or new trust
material can invalidate evidence without changing that object's spec.

Keep bounded summaries and protected evidence references in status. Retain the
evaluated plan and evidence needed to explain the result. Evidence freshness
is check-specific: a current adjacency observation and an installation scan
have different validity rules. Relevant topology or hardware changes invalidate
the applicable result; an old `Ready=True` is not blanket authorization.

```yaml
# Abbreviated controller-written verification snapshot after successful checks.
# Routine condition timestamps/messages are omitted for readability.
apiVersion: fleet.datumapis.com/v1alpha1
kind: DeviceClaim
metadata:
  name: iad1-r12-u20-server
  namespace: infra-ops
status:
  observedGeneration: 1
  evaluatedPlanDigest: "sha256:<claim and resolved dependency inputs>"
  evaluatedChecks: [Identity, RackPlacement, NetworkPaths, PowerPaths]
  evidenceRef: commissioning-evaluation-001
  deviceRef:
    name: server-example-001
  portBindings:
    - {claimPortID: bmc, deviceComponentID: management-port-1}
    - {claimPortID: nic1-p1, deviceComponentID: nic-slot2-port1}
    - {claimPortID: psu1, deviceComponentID: power-supply-1}
    - {claimPortID: psu2, deviceComponentID: power-supply-2}
  conditions:
    - {type: Accepted, status: "True", reason: PhysicalReservationsCommitted, observedGeneration: 1}
    - {type: Bound, status: "True", reason: ExclusiveDeviceBindingCommitted, observedGeneration: 1}
    - {type: IdentityVerified, status: "True", reason: TrustedEnrollmentMatched, observedGeneration: 1}
    - {type: PlacementVerified, status: "True", reason: InstallationScanVerified, observedGeneration: 1}
    - {type: NetworkPathVerified, status: "True", reason: TrustedPeerEvidenceMatched, observedGeneration: 1}
    - {type: PowerPathVerified, status: "True", reason: PowerPathEvidenceVerified, observedGeneration: 1}
    - {type: Ready, status: "True", reason: RequiredEvidenceCurrent, observedGeneration: 1}
```

Cable status similarly reports resolved Device names/component IDs; protected
evidence privately retains the exact local Device incarnations. It separately
reports evidence for endpoint adjacency, cable identity, and any pass-through
path. It never rewrites spec to agree with discovery. Physical drift blocks
new allocations and operations that require the invalidated fact and alerts
operators. It does not independently authorize eviction, power-off, wiping, or
release of an existing consumer reservation.

### Cabling, power, and network configuration

Cable metadata includes a stable record identity, physical label/asset tag where
present, medium, connector information, and optional measured length with units,
color and external evidence references. Distinguish a planned label from an
observed one. Reusing a cable label does not transfer the old cable's verification.
Replacing a physical cable creates a successor Cable identity; repatching the
same cable retains identity but creates an approved endpoint revision and
invalidates affected paths. Preserve disconnected endpoints in history.

Every Cable endpoint is a discriminated reference to exactly one DeviceClaim
port, Device component, or PowerFeed termination, with lane/position where
needed. Validate type, connector, direction, multiplicity and exclusive
termination use. A simple cable has one endpoint at each end; a qualified
breakout has explicit one-to-many lane mappings, not an unconstrained list that
implies all-to-all connectivity. Reject repeated lanes, unmodeled adapters and
incompatible media. Derive occupied terminations from the retained Cable plan,
not an independently editable peer list on Devices.

A Cable connects typed physical terminations. Pass-through components on patch
panels retain explicit internal front/rear mappings. Breakout cables require
explicit position/lane mappings. Trace the complete supporting path; reject an
unsupported mapping rather than assuming a direct connection. NetBox's
[physical cable model](https://netboxlabs.com/docs/netbox/models/dcim/cable/) provides an
established physical-path precedent. It is not a requirement to use NetBox's
schema or implementation.

A PDU is a Device. Its component model maps each outlet to the input that
supplies it. Power cables connect feeds to PDU inputs and outlets to device
inlets; a feed termination uses a typed `powerFeedRef`, resolved by name with its
exact local incarnation retained privately.
PowerFeed records include rated/usable capacity and known upstream
sources; unknown dependencies remain explicit. Reservation calculations must
account for the declared redundancy/failure scenario, not assume that each of
two feeds supplies half the load. Two differently named PDUs can share the same
UPS or upstream circuit. See the
[power feed model](https://netboxlabs.com/docs/netbox/models/dcim/powerfeed/) and
[power outlet model](https://netboxlabs.com/docs/netbox/models/dcim/poweroutlet/) precedents.

PowerFeed is the physical supply boundary, not a utility bill or a substitute
for a PDU Device. Its proposed contract includes Facility reference, operator
label, optional panel/circuit label, typed termination IDs, AC/DC supply,
nominal voltage, phase count where applicable, rated current/capacity with
units, usable budget and the derating-policy revision that produced it. Unknown
ratings cannot become zero demand or unlimited capacity. Declared reservations
and measured draw remain separate; a low instantaneous draw cannot free a
reserved budget.

Upstream dependencies identify other PowerFeeds or scoped external electrical
source identities with provenance. Equal external identities mean a shared
dependency; different labels without evidence do not prove independence.
Unresolved dependencies remain Unknown, and cycles are invalid. An electrical
panel/circuit label is distinct from a network service circuit identifier.
Inventory owns the approved dependency graph; qualified measurements and
technician attestations verify it without overwriting the plan.

```yaml
# Synthetic proposed supply example, not a verified electrical design.
apiVersion: fleet.datumapis.com/v1alpha1
kind: PowerFeed
metadata:
  name: iad1-r12-feed-a
  namespace: infra-ops
spec:
  facilityRef: {name: iad1}
  label: EXAMPLE-PANEL-A-12
  supply: {type: AC, nominalVoltageV: 208, phases: 1, ratedCurrentA: 30}
  budget: {usableCurrentA: 24, policyRevision: example-derating-v1}
  terminations:
    - {id: output-1, type: PowerOutlet, connector: IEC60309}
  upstreamSources:
    - externalRef: {system: example-facilities, type: UPS, id: EXAMPLE-UPS-1}
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: Cable
metadata:
  name: iad1-r12-pdu-a-supply
  namespace: infra-ops
spec:
  medium: Power
  a: [{powerFeedRef: {name: iad1-r12-feed-a}, terminationID: output-1}]
  b: [{deviceRef: {name: iad1-r12-pdu-a}, componentID: inlet-1}]
```

This example assumes a compatible, separately registered PDU inlet. It proves
neither usable capacity nor redundancy. If feeds A and B both depend on
`EXAMPLE-UPS-1`, two working cables do not qualify as UPS-independent supply.
Qualification pins voltage/phase/connector compatibility, load units and a
supported derating/failure model. Converting watts to amperes requires the
necessary electrical assumptions, not implicit arithmetic across unlike units.
Reserve the full admitted failover load on each surviving path required by that
model; reject insufficient headroom or report Unknown for undisclosed inputs.

Feed decommissioning blocks new dependent plans. Changing a breaker rating,
upstream source or usable budget re-evaluates all affected reservations and
conditions; it never silently sheds load, releases occupied capacity, or powers
off equipment. Retire a feed only after dependent paths are safely removed and
their physical release is evidenced.

Rack, switch, and power failure domains are derived from verified dependency
information, including shared upstream resources. Labels can expose validated
domains for selection; different labels do not establish physical independence.

Physical connectivity does not select the VLAN, VRF, IP address, routing
adjacency, or tenant network carried over a link. Network management resources
reference registered Devices/components and own those behaviors. The
bare-metal provider requests attachment or isolation through the networking
authority, which remains the sole writer of the relevant switch configuration.
This physical/logical separation is consistent with
[RFC 8348's hardware model](https://www.rfc-editor.org/rfc/rfc8348.html) and
[RFC 8345's layered topology model](https://www.rfc-editor.org/rfc/rfc8345.html).

### Circuit demarcations and external ownership

Transit, transport/interconnection, and OOB circuits need durable identifiers and
physical demarcation records. A Cable alone does not identify the service, and a
BGP session does not prove its cross-connect. **Proposed decision:** keep the six
inventory kinds initially and reference the owning network/provider service
record; do not assume approval for a seventh `Circuit` kind. Selecting that
record's API/owner, or approving a dedicated physical Circuit kind later, remains
an explicit design decision before circuit-dependent commissioning is automated.

The proposed minimum association is an owner-qualified external circuit ID,
use category (transit, transport or OOB), and named A/Z demarcations as disclosed.
Each physical demarcation records a resolved Facility reference, room/panel label,
and a typed Device/component/position reference when known, with source and
evidence references. A local cross-connect is a Cable or traced Cable path to
the registered demarcation termination. Do not create a fake far-end Device or
Facility for an undisclosed carrier endpoint. Store a reported label as evidence
until the termination is resolved; it cannot satisfy Cable endpoint reservations.

For example, a synthetic proposed association on a panel termination is:

```yaml
externalCircuitRef:
  system: example-network-services
  type: Circuit
  id: EXAMPLE-OOB-0042
demarcation: A
facilityRef: {name: iad1}
termination: {deviceRef: {name: iad1-demarc-panel}, componentID: rear-07, position: 1}
documentRefs:
  - {system: example-drive, id: EXAMPLE-LOA-42, revision: "3", category: LOA}
```

This is an association excerpt, not a new resource or an implemented Circuit
API. Circuit identifiers are unique only within their provider/service scope.
The service owner owns identifier and delivery facts; inventory owns their
approved association to physical terminations and the local Cable topology.
Network services consume that association rather than authoring another
expected-peer graph. Prices, commitments, billing, orders and contract state
stay with provider/commercial owners. Addresses, VLANs, BGP, routing, service
bandwidth policy and activation stay with networking/IPAM/Galactic.

Commissioning evaluates physical demarcation/path verification independently
from provider delivery and routing/service readiness. Missing external contract
support is an explicit unresolved prerequisite, not success or an exemption.
Circuit cancellation does not prove a cross-connect removed; removal of a cable
does not cancel the service. Retain retired associations, circuit IDs and LOA/
as-built evidence without releasing terminations until physical disconnection
is established.

### Moves, replacements, and retirement

- **Move the same asset:** create a successor DeviceClaim naming the existing
  Device and reserve the destination. A coordinated maintenance transition
  transfers active placement authority with exact predecessor/binding
  preconditions. The successor cannot become a second active placement claim.
  Preserve the Device and MachineCapacity identities; verify the new placement and
  paths before restoring eligibility. Release old physical reservations only
  after the affected space/terminations are confirmed free.
- **Replace the whole server:** create a new DeviceClaim, Device, and MachineCapacity.
  Do not transfer an existing customer Machine or inherit identity from a reused
  name, BMC address, serial field, or rack position. Managed-node replacement
  uses the separate NodeClaim/fencing contract.
- **Repair a component:** retain Device identity where the asset remains the
  same, but record the replacement component identity and inventory/trust
  revision. Revalidate affected port, disk, power, and management bindings.
  Reusing a slot ID must not make old component evidence current.
- **Retire or remove equipment:** coordinate physical removal, active consumer
  obligations, and reference cleanup explicitly. Deleting an inventory object
  does not destroy equipment, release a customer Machine, or prove a rack is empty.

Bound claims retain their committed Device name and private incarnation evidence
as history. A placement handover
changes the registry-owned active relationship, not the historical binding.
Required Cable plans must be explicitly changed or replaced during a move;
they do not silently follow a Device into another rack. Retain fulfilled claims
while Cable references rely on them, or transfer those references through the
placement-change protocol. Protect active references and reservations during
deletion, including recovery from partial transitions.

The proposed lifecycle distinguishes Draft, Accepted, Bound, Superseded,
Cancelled and Retired intent/history without requiring one overloaded status
enum. Verification conditions remain independent: a Bound asset can have Unknown
placement evidence. Cancelling an unbound plan releases only reservations proven
unused; uncertain installation work keeps a hold. Superseded/retired records
remain referenceable history and cannot be rebound. Marking an asset retired
does not prove physical removal, secure disposal, tenant cleanup, or provider
release. Each obligation retains its owner and evidence.

For replacement in an occupied rack slot, a successor plan may be retained as
pending contingent on the predecessor; it cannot obtain a second exclusive
reservation of that footprint. The transition must prove removal/fencing and
conditionally transfer the physical reservation, or use a separately reserved
destination. A crash between steps preserves the old hold or an explicit
transition hold. It never makes the slot free merely because readiness is False.

## Existing records and migration

### Manual Fleet records: implemented subset versus target

The comparison below is pinned to Fleet PR #90, branch
`feat/manual-inventory-records`, commit
`08d6dde8563b7c999ecf839565db2bd37b4dda7e`: its [actual types][manual-types]
and [operations guide][manual-guide]. It is not a claim about merge status or
what is deployed. That revision implements operator-written DeviceClaim/Device
CRDs and access controls, not reservation, enrollment, verification, retirement,
or capacity-publication controllers. Its empty status types do not establish
any of the conditions proposed here.

| Reviewed implementation | Proposed target and migration constraint |
| --- | --- |
| DeviceClaim describes a long-lived slot; a replacement Device names the same claim. Device identity and `claimRef` are immutable. | Retain this proposal's per-installation claim identity: each move/replacement has a successor claim. Keep a stable slot label or provenance association for operator navigation, not a second binding authority or new Slot kind. This is a proposed resolution, not a schema change already approved in #90. |
| Placement requires string `facility`/`rack`, numeric position, height and face. | Resolve approved Facility/Rack references with source mapping. Convert to explicit placement scope; do not treat strings as global IDs or retain fabricated positions as authoritative. |
| Claimed ports have name/type/speed and embedded `expectedPeer`; Device interfaces may contain `peer`. | Convert approved expected connections into Cable plans once; preserve actual peer reports as sourced observations. Reconcile reciprocal/contradictory entries before creating a single Cable authority. Keep old inline fields read-only during cutover, then remove/deprecate through a versioned migration. |
| Device carries model, serial/system UUID, interfaces and unmapped MACs. | Preserve original values and provenance; adjudicate canonical physical identity and component mappings. Do not coerce provider IDs into chassis serials, or turn interface names/MAC lists into trusted component identity. |
| Optional BMC endpoint and manual provenance live on restricted provider-project objects in Milo. | Preserve access restrictions. Endpoint discovery does not authorize access. Transfer endpoint configuration authority explicitly to the provider management surface before exposing only references/read projections; never run two writable copies. |
| No controller checks binding exclusivity, evidence or `MachineCapacity.deviceRef` resolution. | Resolve duplicate/multiple bindings and protect existing occupancy before enabling automation. Do not infer Accepted, Bound, Ready or allocator eligibility from schema admission. |

**Claim-lifetime decision gate.** The target remains one committed Device binding
per installation claim, with one active placement authority per Device. A
long-lived slot is useful for naming and history but must not replace an
installation incarnation: otherwise a serial swap can inherit old cabling,
acceptance or verification. Before implementation, approve the successor-claim
protocol and versioned conversion of the manual API, or explicitly amend this
proposal to provide equivalent immutable incarnation semantics. Do not automate
the long-lived-slot convention while presenting it as the target contract.

For a legacy slot with several Devices, retain the original claim and all Device
records, their immutable bindings, and available registration/retirement history.
Adjudicate the active asset and create explicit installation-history mappings;
unknown order or occupancy remains unresolved. Do not choose the newest timestamp
as proof of physical replacement. The migration must select a versioned storage/
conversion mechanism that preserves original bindings while representing the
registry-owned active relationship. A raw patch of immutable `claimRef`, deleting
and recreating Devices, or renaming them to fit a successor claim is not a safe
conversion. Existing MachineCapacity/asset-key references must continue to resolve
to the same asset; a move is not grounds to manufacture a new capacity identity.

**Home and authority decision gate.** The reviewed manual API deliberately stores
records in the provider project's Milo control plane, protected by inventory
roles. This proposal does not move them into a second management-cluster copy.
Choose the scopes for shared Facility/Rack/Cable/PowerFeed references and the
private coordinator ledger before implementation. Internal ledgers retain exact
bindings/evidence; they are not independently writable asset catalogs. Qualify
cross-project reads, status writers, reference authorization and consumer redaction
before broadening access or adding cross-scope relationships.

### NetBox and provider imports

Import is observational until an explicit ownership transition. These rules are
derived from the pinned source workflows, not from a live NetBox database audit:

| Source convention or gap | Required treatment |
| --- | --- |
| Loader Sites group provider metros and can union several provider facility codes. | Preserve source Site ID and provider-code mapping. Do not map NetBox Site one-to-one to Fleet Site or Facility. Unresolved premises stay unresolved and block prerequisites needing a physical Facility. |
| Loaders store provider host IDs in NetBox `serial`. | Retain them as owner-qualified external host IDs with the original field provenance. Never claim a verified chassis serial without independent physical evidence. |
| NetActuate VMs are DCIM Devices. | Classify virtual supply through provider/MachineCapacity contracts. Do not create Fleet physical Devices, ports or racks merely because NetBox uses DCIM. |
| servers.com assigns lowest-free 1U positions because physical positions are undisclosed. | Retain those source values only as synthetic display history. Import known rack association as RackMembership, subject to approved premises mapping; neither reserve nor verify invented units, faces or heights. |
| Commissioning uses asset tags, cable labels, circuit IDs, power feeds, photos and as-built records. | Preserve those distinct identifiers and restricted evidence references; do not flatten everything into serial or a free-form note. Address plans remain IPAM-owned. |
| Provisioning joins serial/model/OOB endpoint with networking data. | Preserve authorized consumer projections and explicit joins, not a duplicate networking API. Imported joins authorize no hardware actions, adoption, capacity eligibility or release. |
| Source lifecycle says active, decommissioning, vanished or decommissioned. | Retain as source observations. None proves physical verification, ownership transfer, secure cleanup, released consumer allocations or a free rack/termination. |

Import batches retain source snapshot identity, mapping decisions, per-record
outcomes and conflicts for repeatable dry runs. The same source object/revision
must not create duplicate assets after a retry. Missing source observations do
not delete records or release occupancy. Never copy local provider tokens, BMC
password custom fields, kubeconfigs or secret-bearing generated configuration.

Use a staged, reversible handoff rather than destructive in-place normalization:

1. Snapshot protected records and audit references; classify physical versus
   virtual supply, identifier semantics, known occupancy and placement certainty.
   Produce a read-only mapping/conflict report with no hardware effects.
2. Approve Facility mappings, model/component shapes and per-installation history.
   Create proposed plans without treating imported occupancy as free. Preserve
   current allocator reservations and consumer obligations under their owners.
3. Reconcile expected peer pairs into Cable plans and electrical dependencies into
   PowerFeeds. Resolve contradictions and unsupported paths explicitly. Validate
   partial placement and scoped external circuit associations without inventing
   absent endpoints or upstream sources.
4. Shadow-read and compare authorized provisioning inputs (identity, model,
   restricted OOB endpoint and network-owned joins). Establish source parity for
   the selected cohort, not an assertion of exhaustive NetBox equivalence.
5. Transfer one writable surface at a time with an explicit cutover revision and
   predecessor preconditions. Disable its previous writer before enabling the
   replacement. Keep compatibility projections read-only and retain source history.
6. Qualify current evidence and separately authorize custody/adoption and consumer
   integrations. Never seed Ready from an old active flag. Recovery/rollback uses
   retained cutover state; it cannot reactivate a stale writer, erase new history,
   or release a reservation whose physical state is uncertain.

## Source evidence and limits

This amendment reviewed repository sources and read-only snapshots of infra
`main` at `d9198940a499be4e744ee0564cf3dc0b3bfcee34`. No live NetBox database audit,
provider call, electrical inspection, or hardware qualification was performed.
The sources motivate contract and migration requirements, not verified fleet-wide
data quality or proof that every production consumer has been found.

| Pinned source | Evidence used |
| --- | --- |
| [Inventory sync guide][sync-guide], [NetActuate loader][na-loader], [servers.com loader][sc-loader] | Metro/provider mappings, host-ID serial semantics, virtual DCIM records, synthetic rack positions, and provider lifecycle observations. |
| [NetBox server guide][netbox-guide] | Operator inventory and provisioning inputs; existing networking/IPAM associations. |
| [Site commissioning][site-build], [rack work][rack-build], [network hardware][network-build], [compute hardware][compute-build] | Racks, serials, asset tags, labels, feeds, circuit IDs, address plans and external photos/LOAs/as-built artifacts. |
| [Out-of-band access commissioning][oob-build], [transit circuit commissioning][transit-build], [transport circuit commissioning][transport-build] | Physical delivery/demarcation and circuit identifiers distinct from service/routing readiness. |
| [Provisioning joins][provisioning-joins], [IPAM validation][ipam-validation] | Serial/model/OOB and network joins; address/routing validation stays with its current owner. |
| [Fleet manual types][manual-types], [Fleet manual operations][manual-guide] | The implemented manual subset and explicit slot-lifetime/storage divergences at the reviewed PR #90 revision. |

The [infrastructure provider responsibilities](bare-metal-hardware.md#infrastructure-providers),
[capacity readiness reporting](bare-metal-hardware.md#status), and
[ownership of records replaced by Fleet](bare-metal-hardware.md#replaced-artifacts) retain provider
host IDs and commercial observations with provider authorities and logical
networking with its owners. This amendment does not complete those separate APIs.

## Validation and qualification matrix

These are required future admission, concurrency, authorization and hardware
qualification tests, **not tests implemented or passed by this documentation
change**. Offline schema tests cannot establish physical truth.

| Area | Cases to qualify | Required outcome |
| --- | --- | --- |
| References and scope | Wrong kind, missing scope, cross-project denial, dangling reference, name recreation, stale revision | Reject unauthorized/ambiguous resolution; no inherited binding or evidence. |
| Facility and Rack | Multi-premises provider Site, unknown dimensions, conflicting faces/depth, 0U mount, known rack/unknown position | Preserve uncertainty; no invented geometry or false free-space assertion; exact reservations require accounted occupancy. |
| Identity and components | Duplicate serials, provider ID in serial, VM in DCIM, swapped asset tag, OS rename, replaced NIC/PSU/disk in the same slot | Adjudicate identity; retain source history; invalidate affected mappings without changing asset identity on ordinary repairs. |
| Binding and reservations | Concurrent claims, claim versus direct-Device cable endpoints, lost registration response, crash during handover, restored reservation database | One active placement and exclusive termination/footprint ownership; recover exact committed inputs; uncertainty keeps holds. |
| Cable topology | Copper/optical mismatch, console direction, unresolved demarcation, patch-panel polarity, breakout lane collision, repatch/replacement | Reject unsupported paths; verify each required segment and invalidate changed dependencies, not just end-to-end link state. |
| Power | Shared UPS behind distinct PDUs, unknown rating, mixed units, feed cycles, failover overbudget, stale upstream evidence | No false redundancy or capacity; independent sources and full failover budget proven under a qualified electrical model. |
| Evidence and authority | Forged submitter, copied BMC location, stale scan, inaccessible document, unknown external circuit owner, attempted collector spec write | Fail closed for required checks; no self-attestation from copied intent, no status forgery or automatic exemption. |
| Lifecycle | Occupied-slot replacement, move with active allocation, component removal, retirement with active references, cancelled partially installed plan | Retain history and consumer obligations; no premature physical release, destructive side effect or second active claim. |
| Import/cutover | Duplicate batch, conflicting inline peers, invented rack positions, source disappearance, rollback after new writes | Idempotent mapping, single writer, no Ready backfill or free-capacity window; keep unresolved facts unknown. |
| Access/projections | Inventory roles versus consumer roles, OOB/console endpoint reads, document ACLs, secret-bearing source fields | Restricted management details remain restricted; credentials never enter inventory projections or repository fixtures. |

Before a facility cohort becomes eligible, record the qualified model variants,
supported connector/patch/breakout/electrical layouts, evidence issuers and
freshness policy, verified failure scenarios and recovery results. Exercise the
actual consumer projection against its retained input revisions. Unsupported
hardware or topology stays unavailable for offerings requiring those checks;
registration alone does not satisfy this gate.

[manual-types]: https://github.com/datum-cloud/fleet/blob/08d6dde8563b7c999ecf839565db2bd37b4dda7e/internal/api/inventory/v1alpha1/types.go
[manual-guide]: https://github.com/datum-cloud/fleet/blob/08d6dde8563b7c999ecf839565db2bd37b4dda7e/docs/operations/physical-inventory.md
[sync-guide]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/docs/guides/sync-inventory.md
[na-loader]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/bin/sync-netactuate-inventory
[sc-loader]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/bin/sync-serversdotcom-inventory
[netbox-guide]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/docs/guides/netbox-server.md
[site-build]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/.claude/skills/site-builds/phases/00-site.md
[oob-build]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/.claude/skills/site-builds/phases/04-oob.md
[transit-build]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/.claude/skills/site-builds/phases/05-transit.md
[transport-build]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/.claude/skills/site-builds/phases/06-transport.md
[network-build]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/.claude/skills/site-builds/phases/07-nethw.md
[compute-build]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/.claude/skills/site-builds/phases/08-computehw.md
[rack-build]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/.claude/skills/site-builds/phases/09-rack.md
[provisioning-joins]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/provisioning/baremetal/lib/common.sh
[ipam-validation]: https://github.com/datum-cloud/infra/blob/d9198940a499be4e744ee0564cf3dc0b3bfcee34/bin/validate-netbox-ipam
