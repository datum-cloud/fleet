# Metal and Fleet architecture guide

[Product overview](../README.md) · [Functionality](../functionality/README.md) ·
[Operations](../operations/README.md) · [Troubleshooting](../troubleshooting/README.md)

Fleet groups related infrastructure capabilities without making one controller
the owner of every resource. Physical inventory, allocation, installation, and
managed node identity remain separate. Standalone and managed consumers use the
same allocation and installation contracts.

## Start with the boundaries

Read the [design context](design.md) for the subsystem map, authority contracts,
and unresolved questions. Then read the
[detailed service architecture proposal](../proposals/fleet-management/architecture.md)
and [architecture diagrams](../proposals/fleet-management/diagrams/README.md).
These describe the intended service, not the deployed topology.

Key principles to carry into the detailed design:

- Each configuration surface has one authoritative writer. Networking/IPAM,
  Galactic routing, DNS, and consumer service policy stay with their owners.
- Publication and discovery are not authorization. Allocation needs retained
  consumer authority and exact source identity before effects.
- Hardware, allocation, installation, and logical node lifetimes are distinct.
  Replacement must not silently reuse an old incarnation's authority.
- Execution and cleanup can be uncertain. Unknown outcomes do not establish
  readiness or permit capacity reuse.

## Choose a deeper view

| Design question | Reference |
| --- | --- |
| Which API groups own which resources? | [API boundaries](#api-boundaries) |
| Where do controllers run and who writes each surface? | [Control-plane placement and writers](#control-plane-placement-and-writers) |
| How is Bare Metal registered and renamed safely? | [Service registration and rollout](#service-registration-and-rollout) |
| What are the machine resources and project boundaries? | [Machine lifecycle API design](../enhancements/machine-lifecycle/README.md#api-design) |
| How are reservations and concurrent operations coordinated? | [Durable coordination](../enhancements/machine-lifecycle/README.md#durable-coordination) |
| How are provider effects recovered safely? | [Provider execution and recovery](../enhancements/machine-lifecycle/README.md#provider-execution-and-recovery) |
| What happens on release or project deletion? | [Release and project lifecycle](../enhancements/machine-lifecycle/README.md#release-and-project-lifecycle) |
| Which internal interfaces and guarantees exist? | [Implementation status](../development/README.md) |
| Which milestones must precede rollout? | [Implementation roadmap](../development/roadmap.md) |
| Where did the design originate? | [Pinned source proposals](design.md#source-proposals), [migration record](../proposals/fleet-management/MIGRATION.md) |

For subsystem-specific contracts, use the [functionality map](../functionality/README.md).
For manifests and environment responsibilities rather than proposed topology,
use the [operations guide](../operations/README.md).
## Service and API model

The customer-facing **Bare Metal** service uses `metal.datumapis.com`. Fleet
manages physical infrastructure beneath it. The intended coordination API uses
`coordination.fleet.datumapis.com` for admitted work between management and
locations. API groups separate contracts and permissions, not necessarily
processes or service-catalog entries.

## API boundaries

| Group | Resources and responsibility | Status |
| --- | --- | --- |
| `metal.datumapis.com` | `MachineClass`: offerings and consumer references. `MachineProfile`: hardware requirements. `MachineCapacity`: provider supply. `Machine`: customer allocation and runtime intent. `MachineAllocation`: protected allocation identity. `Installation`: immutable OS installation or reinstall request. | Six alpha CRDs at `v1alpha1`. Production admission and real providers remain incomplete. |
| `fleet.datumapis.com` | Physical inventory: `Facility`, `Rack`, `DeviceClaim`, `Device`, `Cable`, and `PowerFeed`. Physical preparation and provider execution remain operator responsibilities. | Proposed inventory APIs; no physical inventory CRDs or real provider executor are implemented here. |
| `coordination.fleet.datumapis.com` | Intended management-to-location execution handoff, retaining exact allocation identity, location, admitted inputs, release intent, and execution evidence. `MachineAllocationHandoff` is a candidate resource. | Architecture decision only on the current main implementation: no coordination CRD or cell controller is packaged. Replaces the earlier prototype name `internal.fleet.datumapis.com`. |

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

Current main implements location resolution and event routing, not this complete
federation topology. Standalone namespaces are project stand-ins; synthetic
staging is a test mode. Neither establishes production federation or hardware
readiness. See the [implementation status](../development/README.md).

## Service registration and rollout

Only Bare Metal is registered with Milo's service catalog:

- Service and ServiceConfiguration object name: `metal`.
- Service identity and IAM permission prefix: `metal.datumapis.com`.
- Display name: `Bare Metal`.
- Allocation quota resource type: `metal.datumapis.com/machines`.

The Service and ServiceConfiguration remain Draft until environment-specific
readiness gates pass. Entitlement and location-availability readers use the same
`metal` reference. Fleet and coordination receive no separate Service,
ServiceConfiguration, or customer entitlement. The [deployment guide](../../config/README.md)
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
The [design context](design.md) and [source proposals](../proposals/fleet-management/README.md)
provide the lifecycle rationale; this document supersedes their provisional
API naming where broader resource placement remains unresolved.
