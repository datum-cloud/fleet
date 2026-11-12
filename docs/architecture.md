# Metal and Fleet API architecture

The customer-facing **Bare Metal** service uses `metal.datumapis.com`. Fleet
manages physical infrastructure beneath it. The intended coordination API uses
`coordination.fleet.datumapis.com` for admitted work between management and
locations. API groups separate contracts and permissions, not necessarily
processes or service-catalog entries.

## API boundaries

| Group | Resources and responsibility | Status |
| --- | --- | --- |
| `metal.datumapis.com` | `MachineClass`: offerings and consumer references. `MachineProfile`: hardware requirements. `MachineCapacity`: provider supply. `Machine`: customer allocation and runtime intent. `MachineAllocation`: protected allocation identity. `Installation`: immutable OS installation or reinstall request. | Six lifecycle alpha CRDs at `v1alpha1`, plus separately packaged prototype `MachineManagementSession` and protected `MachineManagementSessionBinding` CRDs. Production admission and real providers remain incomplete. |
| `fleet.datumapis.com` | Physical inventory: `Facility`, `Rack`, `DeviceClaim`, `Device`, `Cable`, and `PowerFeed`. Physical preparation and provider execution remain operator responsibilities. | Proposed inventory APIs; no physical inventory CRDs or real provider executor are implemented here. |
| `coordination.fleet.datumapis.com` | Intended management-to-location execution handoff, retaining exact allocation identity, location, admitted inputs, release intent, and execution evidence. `MachineAllocationHandoff` is a candidate resource. | The separate BMC prototype implements `ProjectedMachineManagementSession` with a Karmada projector and location gateway. Allocation handoff remains proposed. Replaces `internal.fleet.datumapis.com`. |

MachineCapacity and MachineAllocation belong to the Metal allocation contract
but remain provider- or service-owned. Membership in the customer service's API
group does not make every resource customer-accessible. The lifecycle still
stores protected allocation custody in service-owned Secrets; the
MachineAllocation CRD does not replace that storage automatically.

Device identifies hardware, MachineCapacity offers its capacity, Machine is the
customer's durable handle, MachineAllocation identifies the exclusive binding,
and Installation identifies an OS incarnation. Managed NodeClaim identity stays
above allocation. These identities and lifetimes remain separate. Standalone
and managed consumers use the same allocator and installation contract.

The broader proposals describe managed clusters, node services, image catalogs,
and OS policy. Their implementation and final group placement remain separate
design work. Vendor and OS adapters may retain typed groups; this document does
not create additional service registrations for them.

## Control-plane placement and writers

| Surface | Control plane | Authoritative writer |
| --- | --- | --- |
| Metal provider definitions and capacity | Provider Milo project control plane | Authorized providers define offerings and capacity policy; the shared allocator owns allocation transitions. |
| Metal customer Machines and Installations | Consumer Milo project control plane | Authorized customers write requests; lifecycle controllers publish status. Published offering references retain publisher ownership. |
| Protected allocation custody | Service-owned private storage | The allocator retains exact identities, authority, and cleanup obligations before effects. |
| Fleet physical inventory | Intended operator/provider infrastructure control planes | Inventory and provider controllers own their respective facts and execution contracts; exact scopes and deployment remain proposed. |
| Coordination intent | Intended Karmada federation hub | Management writes admitted handoff intent. |
| Coordination replicas and execution | Intended selected location/cell | Federation delivers intent; the cell executes location work and reports status directly to the hub. |

The coordination design requires its CRD on the federation hub and participating
cells, not in customer Milo project control planes. Management owns handoff
intent and the selected cell owns execution status. The management process's
deployment cluster is not implicitly the authoritative handoff store.

Management retains admission and reservation authority before creating a
handoff. Federation targets the selected location, and the cell asks networking
to prepare the admitted interfaces. Current location evidence is projected into
the customer's Metal resource. Release requires verified location cleanup before
provider cleanup and quota settlement complete. Missing, stale, or uncertain
execution evidence remains unknown.

Networking/IPAM and Galactic retain authority over attachments, addresses,
switch configuration, and routing. A coordination reference is neither a
customer source-use grant nor independent provider execution authority.

The lifecycle manager implements location resolution and event routing, not this complete
federation topology. The separately packaged BMC prototype exercises session
federation with pre-bound synthetic allocations; it is not wired to lifecycle
admission or release custody. Standalone namespaces are project stand-ins; synthetic
staging is a test mode. Neither establishes production federation or hardware
readiness. See the [implementation status](implementation.md).

## Service registration and rollout

Only Bare Metal is registered with Milo's service catalog:

- Service and ServiceConfiguration object name: `metal`.
- Service identity and IAM permission prefix: `metal.datumapis.com`.
- Display name: `Bare Metal`.
- Allocation quota resource type: `metal.datumapis.com/machines`.

The Service and ServiceConfiguration remain Draft until environment-specific
readiness gates pass. Entitlement and location-availability readers use the same
`metal` reference. Fleet and coordination receive no separate Service,
ServiceConfiguration, or customer entitlement. The [deployment guide](../config/README.md)
describes bundle installation and environment-owned configuration.

This is a breaking alpha rename, not a Kubernetes storage migration. Old CRs,
IAM grants, service enablement, quota registrations, and location availability
do not automatically move to the new names. The physical Device group also
participates in canonical asset keys. Keep the old stack responsible for active
allocations until verified release and cleanup; do not recreate occupied supply
under new identities or delete old authority records to bypass cleanup.
Environment-owned overlays must update service references and publication rules
before enabling the renamed service. No live environment is changed by this PR.

Repository, Go module, binaries, private record labels and Secret types, and
release finalizers retain Fleet names. They are implementation identities.
The [design context](design.md) and [source proposals](proposals/fleet-management/README.md)
provide the lifecycle rationale; this document supersedes their provisional
API naming where broader resource placement remains unresolved.
