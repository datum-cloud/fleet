---
status: provisional
stage: alpha
latest-milestone: "v0.1"
---

<!-- omit from toc -->
# Fleet inventory

Part of the [Fleet infrastructure management service](README.md). Inventory owns
physical installation intent, registered assets, and verified physical
topology. [Machine allocation](bare-metal-hardware.md) consumes those records;
[bare-metal provisioning](bare-metal-provisioning.md) executes authorized work
on the hardware. Separate controllers and permissions preserve these
responsibilities within Fleet.

- [Summary](#summary)
- [Goals](#goals)
- [Non-goals](#non-goals)
- [Proposal](#proposal)
  - [Resources and authority](#resources-and-authority)
  - [Plan a physical installation](#plan-a-physical-installation)
  - [Reserve physical resources](#reserve-physical-resources)
  - [Register and bind a device](#register-and-bind-a-device)
  - [Verify the installation](#verify-the-installation)
  - [Cabling, power, and network configuration](#cabling-power-and-network-configuration)
  - [Moves, replacements, and retirement](#moves-replacements-and-retirement)
- [Implementation](#implementation)
- [Rollout plan](#rollout-plan)
- [Open qualification questions](#open-qualification-questions)

## Summary

Operators declare a `DeviceClaim` before equipment is registered. It expresses
the expected hardware, approved rack placement, declared ports, and required
cables. Acceptance reserves the physical resources. A trusted registrar matches
the equipment to that accepted plan and creates an exclusively bound `Device`.
Controllers compare physical evidence with the plan before reporting readiness.

A `DeviceClaim` is a physical installation expectation. A `MachineClaim` is a
consumer's reservation of compute capacity. They have independent lifetimes.
Neither registering a Device nor making its DeviceClaim Ready authorizes power
changes, boot-based inspection, installation, cleaning, or consumer allocation.

This is the target Fleet inventory model, independent of the existing inventory
service's schema. The proposed group is `inventory.fleet.datumapis.com/v1alpha1`;
the API group, fields, and scopes remain provisional. Examples show operator
resources in `infra-ops`, not implemented CRDs or manifests to apply. UIDs,
hardware identifiers, and evidence handles are illustrative.

## Goals

- Record physical identity, placement, cabling, and power dependencies once.
- Reserve nonconflicting installations before hardware arrives.
- Admit equipment only through trusted registration against an accepted claim.
- Preserve expected and observed state separately, including incomplete work.
- Provide current, attributable evidence for allocation and operation gates.
- Support servers, network devices, power equipment, and passive infrastructure.
- Preserve asset history through repairs, moves, and changes of consumer.

## Non-goals

- Allocating compute to consumers, configuring an OS, or managing clusters.
- Storing BMC/switch credentials in broadly readable inventory objects.
- Owning VLANs, VRFs, IP allocations, routing, or workload network attachments.
- Treating discovery as authorization to adopt or modify hardware.
- Inferring physical completion from a YAML declaration or link reachability.
- Automating procurement, warehouse management, or every field-work process in
  the first implementation.

## Proposal

### Resources and authority

- **`Facility`:** Provider-neutral physical premises, associated with geographic Location.
  Provider codes do not define its identity.
- **`Rack`:** Facility membership, mounting capacity, and physical constraints.
- **`DeviceClaim`:** Operator's expected installation, physical reservations, and one
  committed Device binding.
- **`Device`:** Canonical registered physical asset: server, switch, router, PDU, console
  server, or patch panel.
- **`Cable`:** Sole authority for expected physical connections and their termination
  mappings.
- **`PowerFeed`:** Electrical supply connection, capacity, and upstream dependency
  information.

Ports and components are typed entries with stable IDs within a Device. They do
not require a separate public kind initially. Network interfaces, power
inlets/outlets, console ports, and pass-through terminations have different
connection rules. Component identity must remain distinct from mutable OS names
such as `eth0` and from logical interfaces such as a VLAN or bond.

For owned servers, `Machine` references the registered Device by UID. It is the
compute representation, with allocation and reclamation policy; it does not
independently edit the asset's serial number, rack placement, or cabling. A
switch or patch panel can be a Device without being a Machine. Inventory owns
physical observations; the provider owns management configuration and hardware
operations; networking owns network configuration. An inventory relationship
is not a Kubernetes garbage-collection ownership relationship.

A Device represents identifiable physical equipment. Do not fabricate a Device,
rack, or physical port for a provider VM or an offering that discloses no
physical asset identity. Such providers still participate through the generic
Machine contract and their own observed capabilities.

Operator authorization controls claims, physical plans, and manual evidence.
Only the trusted registration service creates registered Devices and writes
identity/placement bindings. Collectors write observations through validated
interfaces; they cannot change the approved plan. References crossing an
administrative scope require explicit authorization on both sides. A namespace
or matching label alone does not grant that authority.

### Plan a physical installation

The Facility describes physical premises. A Fleet `Site` can select eligible
Facilities as operational policy without moving their assets. Location remains
the geographic reference; neither Site nor Facility is a provider identifier.

```yaml
apiVersion: inventory.fleet.datumapis.com/v1alpha1
kind: Facility
metadata:
  name: iad1
  namespace: infra-ops
spec:
  displayName: Ashburn Facility 1
  locationRef:
    name: us-east-1
---
apiVersion: inventory.fleet.datumapis.com/v1alpha1
kind: Rack
metadata:
  name: iad1-r12
  namespace: infra-ops
spec:
  facilityRef: {name: iad1}
  heightUnits: 42
---
apiVersion: inventory.fleet.datumapis.com/v1alpha1
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
apiVersion: inventory.fleet.datumapis.com/v1alpha1
kind: Cable
metadata:
  name: iad1-r12-srv01-management
  namespace: infra-ops
spec:
  medium: CopperEthernet
  a: [{deviceClaimRef: {name: iad1-r12-u20-server}, portID: bmc}]
  b: [{deviceRef: {name: iad1-oob01}, componentID: port12}]
---
apiVersion: inventory.fleet.datumapis.com/v1alpha1
kind: Cable
metadata:
  name: iad1-r12-srv01-data
  namespace: infra-ops
spec:
  medium: DirectAttachCopper
  a: [{deviceClaimRef: {name: iad1-r12-u20-server}, portID: nic1-p1}]
  b: [{deviceRef: {name: iad1-r12-leaf-a}, componentID: ethernet20}]
---
apiVersion: inventory.fleet.datumapis.com/v1alpha1
kind: Cable
metadata:
  name: iad1-r12-srv01-power-a
  namespace: infra-ops
spec:
  medium: Power
  a: [{deviceRef: {name: iad1-r12-pdu-a}, componentID: outlet20}]
  b: [{deviceClaimRef: {name: iad1-r12-u20-server}, portID: psu1}]
---
apiVersion: inventory.fleet.datumapis.com/v1alpha1
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
component ID. Names are convenient authoring references. Acceptance resolves
them to exact UIDs and records the approved dependency revisions. Recreating an
object with the same name never inherits a reservation or binding.

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

### Reserve physical resources

Acceptance commits a retained plan covering the claim and its required Cable
inputs. Inventory must reserve the applicable declared rack footprint, physical
terminations, and power capacity without races. Rack checks include occupied units,
front/rear constraints, full-depth equipment, and pending installations. The
model also supports 0U mounting and equipment outside a rack; it must not assign
them fictitious rack-unit positions.

Endpoint reservations belong to the Cable UID and identify the Device or
DeviceClaim UID plus termination/position. Two cables cannot reserve the same
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

The registrar commits reciprocal claim-to-Device UID ownership and a durable
binding identity. One claim binds one Device UID for one installation
incarnation. One physical asset cannot acquire duplicate Device identities,
and one Device has at most one active placement authority. Recovery after a
lost response finds the committed registration instead of creating a second
asset. Ambiguous matches, unexpected identities, expired authorization, or
already-bound claims fail closed. Unknown equipment remains isolated discovery
evidence; it cannot manufacture an accepted claim or eligible capacity.

Registration does not require all physical verification to have succeeded. A
Device can exist in commissioning with a placement or wiring discrepancy:

```yaml
# Registrar-created snapshot, not an operator-authored apply input.
apiVersion: inventory.fleet.datumapis.com/v1alpha1
kind: Device
metadata:
  name: server-example-001
  namespace: infra-ops
  uid: d1111111-1111-4111-8111-111111111111
spec:
  # Registry-owned. A raw reference edit cannot move the asset.
  claimRef:
    name: iad1-r12-u20-server
    uid: c1111111-1111-4111-8111-111111111111
  identity:
    manufacturer: Dell
    model: PowerEdge-R650
    serialNumber: EXAMPLE-SERVER-001
status:
  observedPlacement:
    rackRef:
      name: iad1-r12
      uid: a1111111-1111-4111-8111-111111111111
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
whether it evaluated Facility or exact Rack placement. Missing or stale
required evidence is Unknown; a verified mismatch is False. Checks outside the
scope remain explicitly unevaluated. A policy may mark a check physically
inapplicable, such as power for a passive panel; absence of evidence must not
silently become an exemption. Downstream controllers require the specific
verified facts their operation or offering needs, not an unqualified Ready bit.

Each evaluation records the relevant claim/Cable/Rack/PowerFeed UIDs and
revisions, Device identity/component/trust revision, evaluated input digest,
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
apiVersion: inventory.fleet.datumapis.com/v1alpha1
kind: DeviceClaim
metadata:
  name: iad1-r12-u20-server
  namespace: infra-ops
  uid: c1111111-1111-4111-8111-111111111111
status:
  observedGeneration: 1
  evaluatedPlanDigest: "sha256:<claim and resolved dependency inputs>"
  evaluatedChecks: [Identity, RackPlacement, NetworkPaths, PowerPaths]
  evidenceRef: commissioning-evaluation-001
  deviceRef:
    name: server-example-001
    uid: d1111111-1111-4111-8111-111111111111
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

Cable status similarly reports resolved Device UIDs/component IDs and the
separate evidence for endpoint adjacency, cable identity, and any pass-through
path. It never rewrites spec to agree with discovery. Physical drift blocks
new allocations and operations that require the invalidated fact and alerts
operators. It does not independently authorize eviction, power-off, wiping, or
release of an existing consumer reservation.

### Cabling, power, and network configuration

A Cable connects typed physical terminations. Pass-through components on patch
panels retain explicit internal front/rear mappings. Breakout cables require
explicit position/lane mappings. Trace the complete supporting path; reject an
unsupported mapping rather than assuming a direct connection. NetBox's
[cable model](https://netboxlabs.com/docs/netbox/models/dcim/cable/) provides an
established physical-path precedent. It is not a requirement to use NetBox's
schema or implementation.

A PDU is a Device. Its component model maps each outlet to the input that
supplies it. Power cables connect feeds to PDU inputs and outlets to device
inlets; a feed termination uses a typed `powerFeedRef`, resolved to its UID.
PowerFeed records include rated/usable capacity and known upstream
sources; unknown dependencies remain explicit. Reservation calculations must
account for the declared redundancy/failure scenario, not assume that each of
two feeds supplies half the load. Two differently named PDUs can share the same
UPS or upstream circuit. See the
[power-feed](https://netboxlabs.com/docs/netbox/models/dcim/powerfeed/) and
[outlet](https://netboxlabs.com/docs/netbox/models/dcim/poweroutlet/) precedents.

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

### Moves, replacements, and retirement

- **Move the same asset:** create a successor DeviceClaim naming the existing
  Device UID and reserve the destination. A coordinated maintenance transition
  transfers active placement authority with exact predecessor/binding
  preconditions. The successor cannot become a second active placement claim.
  Preserve the Device and Machine identities; verify the new placement and
  paths before restoring eligibility. Release old physical reservations only
  after the affected space/terminations are confirmed free.
- **Replace the whole server:** create a new DeviceClaim, Device, and Machine.
  Do not transfer an existing MachineClaim or inherit identity from a reused
  name, BMC address, serial field, or rack position. Managed-node replacement
  uses the separate NodeClaim/fencing contract.
- **Repair a component:** retain Device identity where the asset remains the
  same, but record the replacement component identity and inventory/trust
  revision. Revalidate affected port, disk, power, and management bindings.
  Reusing a slot ID must not make old component evidence current.
- **Retire or remove equipment:** coordinate physical removal, active consumer
  obligations, and reference cleanup explicitly. Deleting an inventory object
  does not destroy equipment, release a MachineClaim, or prove a rack is empty.

Bound claims retain their committed Device UID as history. A placement handover
changes the registry-owned active relationship, not the historical binding.
Required Cable plans must be explicitly changed or replaced during a move;
they do not silently follow a Device into another rack. Retain fulfilled claims
while Cable references rely on them, or transfer those references through the
placement-change protocol. Protect active references and reservations during
deletion, including recovery from partial transitions.

## Implementation

Inventory requires admission/authorization, a durable reservation and binding
coordinator, trusted registration, and evidence evaluation. These are controller
responsibilities, not a requirement for another public type per process.
Implementations may share infrastructure with other Fleet subsystems while
retaining separate field ownership and credentials.

Hardware providers and network collectors submit authenticated observations;
authorized technicians submit physical-work evidence. Enrollment of passive
infrastructure follows the same claim and binding rules. Backfill existing
assets through explicit approved claims and non-destructive registration;
importing a record must not invoke boot, cleaning, or network changes.

Consumers read only the inventory projections they are authorized to see.
Privileged BMC/switch endpoints, credentials, and tenant allocation details
remain in their respective management domains. Physical evidence must be
available to the controllers enforcing prerequisites without requiring those
controllers to assume inventory ownership.

## Rollout plan

1. Implement Facility/Rack/DeviceClaim/Device/Cable/PowerFeed contracts, typed
   components, physical reservation validation, and trusted operator
   registration. Qualify one hardware/rack layout, including passive equipment.
2. Exercise one facility's management, data, console where used, and power
   paths. Demonstrate wrong identity, wrong port, wrong rack placement, missing
   evidence, and stale evidence remaining unavailable without destructive
   side effects. Verify patch-panel/breakout paths used by that facility.
3. Integrate current physical-readiness evidence with Machine eligibility and
   network attachment prerequisites. Test concurrent claims, duplicate
   registration, lost responses, controller restart, object-name reuse, and
   changes to dependencies during evaluation.
4. Qualify moves, component replacement, retirement, and a recovered reservation
   database. Demonstrate no duplicate active placement, premature capacity
   reuse, or implicit consumer eviction before expanding to more facilities.

## Open qualification questions

- Which platform scopes and IAM permissions govern shared Facilities, operator
  claims, registrar writes, evidence submission, and cross-scope references?
- Which durable reservation/commit mechanism enforces rack, termination,
  power-capacity, and identity uniqueness through crashes and restoration?
- What independently trusted enrollment mechanism and identity-deduplication
  rules apply to each active and passive hardware family?
- Which rack/cable/power checks require technician evidence, which have
  qualified instrumentation, and what invalidates or expires each result?
- What component, pass-through, breakout, and redundant-power layouts must the
  first facility support, and how is unsupported topology reported?
- What explicit maintenance and placement-handover protocol preserves active
  allocations and releases old physical reservations only after verification?

These are implementation qualification gates. None changes the ownership,
exclusive-binding, or no-implicit-destructive-action contracts above.
