---
status: provisional
stage: alpha
---

# Fleet management service

## Summary

Fleet is Datum's operator-facing infrastructure management service. It provides
an authoritative view of physical premises, installation plans, equipment, and
dependencies, and coordinates the authorized work that turns infrastructure into
usable capacity. Facilities and devices belong to one physical infrastructure
domain and share the proposed `fleet.datumapis.com` API group as distinct kinds.

Service Provider Projects own infrastructure and supply. Consumer Projects own
requests for that supply. The Fleet control plane reconciles these contracts and
retains operation authority; the Federation Control Plane delivers admitted work
to selected location control planes. These are distinct ownership, coordination,
and execution boundaries, not four interchangeable stores of the same objects.

This enhancement scopes the service and proposes its project and control-plane
placement. It builds on the [design context](../../architecture/design.md), the
[API architecture](../../architecture/README.md), and the imported
[source proposal](../../proposals/fleet-management/README.md), whose reviewed
revisions are recorded in the [migration record](../../proposals/fleet-management/MIGRATION.md).
The detailed [inventory contract](../../proposals/fleet-management/inventory.md)
and [machine lifecycle enhancement](../machine-lifecycle/README.md) remain the
contracts for their respective lifecycles; this document does not replace them.

### Implementation status

The repository contains six alpha Metal CRDs, a lifecycle library, synthetic
provider and staging fixtures, and partially wired management components.
Physical inventory CRDs, the federation handoff CRD, and real provider execution
are not implemented. Production authority integrations remain incomplete. See
[implementation status](../../development/README.md) for the actual supported paths.

Every new API, control-plane assignment, workflow, and acceptance criterion below
is proposed, not a claim of deployed behavior. This document adds no CRDs,
service-catalog registration, or production deployment.

## Motivation

Fleet deployments need more than a geographic label on a server. Operators need
to know which premises and racks are available, what is reserved or installed,
which power and network paths support equipment, and which facts have actually
been verified. Consumers need usable capacity without access to those private
physical and management details.

Keeping this information in unrelated configuration files makes it difficult to
distinguish a plan from a completed installation, prevent conflicting physical
reservations, diagnose shared dependencies, or adopt existing equipment safely.
Combining all of it into a Device would instead couple facility lifetime to an
individual asset and duplicate shared infrastructure facts.

### Goals

- Establish a coherent facility, rack, device, cabling, and power inventory.
- Separate approved plans, physical reservations, actual assets, and evidence.
- Make project ownership and each control plane's authority explicit.
- Integrate verified supply with the existing shared Metal allocator.
- Support owned infrastructure and the facts actually disclosed for rented supply.
- Preserve recoverable operation authority across failures and project deletion.
- Give operators actionable progress without exposing privileged data to consumers.

### Non-goals

- A second allocator or a replacement for the Metal consumer API.
- A full building-management, procurement, lease, billing, or warehouse system.
- Owning geographic Location, IPAM, logical networks, routing, or service policy.
- Selecting a provisioning backend, database, or new federation technology.
- Implementing managed clusters, node services, or automatic procurement in the
  first delivery; those remain later consumers and extensions.
- Making facility registration, discovery, or inventory import authorize effects.

## Service functionality

### Facilities and physical capacity

Operators register physical premises independently of their provider codes and
commercial relationships. A Facility references a geographic Location; a Rack
belongs to a Facility. Optional rooms, suites, and cages can be added when a
qualified installation requires them rather than imposing a universal hierarchy.

Fleet records mounting constraints, power supply dependencies, and known physical
connectivity. It distinguishes planned, reserved, installed, and verified usable
capacity. Free rack units alone do not establish that another server can be
installed. Power accounting must consider shared upstream limits and the required
redundancy scenario rather than adding nameplate ratings or double-counting feeds.
Cooling limits may be recorded as operator-supplied constraints; sensor integration
and automatic cooling control are outside the first delivery.

Facility identity is not a provider account, a region, or a failure-domain label.
Multiple suppliers at the same premises should reference one authorized canonical
Facility when that relationship is established, rather than create competing
authorities merely because their provider codes differ.

### Installation planning and inventory

An operator creates a DeviceClaim describing expected equipment, placement, and
required connections. Acceptance reserves conflicting rack space, terminations,
and required power capacity durably before equipment arrives. Registration binds
one actual Device to the accepted claim through a trusted registrar. Verification
compares independent observations or authorized technician evidence with the plan.

Servers, switches, routers, PDUs, console servers, and passive panels can all be
Devices; only appropriate supply becomes MachineCapacity. Ports and components
have stable IDs within their Device. Cables exclusively own expected connection
mappings; Devices do not maintain a second editable peer map.

Rack-mounted placement references a Rack, from which Facility membership is
derived. Facility-only placement is allowed when rack placement is unavailable.
Neither path implies more verification than the evidence supports. Rented supply
with no disclosed physical identity must not acquire a fabricated Device or Rack.

### Operations and maintenance

Operators can inspect discrepancies, identify assets affected by a physical
dependency, and plan installation, repair, movement, and retirement. Planned work
and completion evidence remain separate. The initial workflow can reference an
external work ticket and authorized manual evidence; a general-purpose work-order
API is not required to introduce Fleet.

Maintenance policy can block new reservations or admission of affected work.
It cannot by itself authorize tenant eviction, power-off, reinstall, or wiping.
Effects on active allocations require their lifecycle authority and applicable
disruption gates. Moving equipment preserves Device identity, but requires an
approved placement transition and re-verification; replacing hardware creates a
new Device. Deleting a record does not prove physical removal or free capacity.

### Supply, provisioning, and consumers

Once registration, verification, provider management policy, and preparation
requirements permit it, provider-owned MachineCapacity can offer a Device through
Metal. Inventory readiness is a prerequisite, not allocation or execution authority.
MachineClass publication exposes approved offerings and sanitized summaries to
eligible Consumer Projects, not raw physical inventory.

The shared allocator handles both standalone customers and internal managed
platforms. Provider adapters execute admitted preparation, installation, power,
console, and cleanup operations. Backend-specific capabilities stay behind those
contracts. Managed clusters and node services later add their own policy above
allocation without taking over physical identity or creating a second allocator.

## API design

### API groups and service identity

| Group | Contract | Intended audience |
| --- | --- | --- |
| `fleet.datumapis.com/v1alpha1` | Facility, Rack, DeviceClaim, Device, Cable, PowerFeed | Authorized infrastructure operators and Fleet components |
| `metal.datumapis.com/v1alpha1` | MachineClass, MachineProfile, MachineCapacity, Machine, MachineAllocation, Installation | Providers and consumers, with resource-specific access restrictions |
| `coordination.fleet.datumapis.com/v1alpha1` | Candidate MachineAllocationHandoff for admitted location work | Fleet management, federation, and selected location executors only |

One inventory group does not imply one controller, permission set, or process.
Facility and Device are independent resources because they have independent
lifetimes, but no separate facilities group is needed for this scope.

Fleet is an operational service, not a new consumer entitlement in this proposal.
The existing catalog service remains `metal`, displayed as Bare Metal, with
`metal.datumapis.com` service identity. Inventory access uses explicit operator
authorization. Neither Fleet nor coordination receives an additional Service or
ServiceConfiguration merely because it has an API group. Any future independent
Fleet offering and catalog enrollment requires a separate decision.

### Proposed inventory resources

| Kind | Desired or registered facts | Controller-owned results |
| --- | --- | --- |
| Facility | Premises identity, display metadata, Location reference, provider aliases | Validation and attributable operational summaries |
| Rack | Facility reference, mounting geometry and constraints | Reserved/occupied capacity and validated constraints |
| DeviceClaim | Expected identity, planned placement, ports, required Cable references and verification policy | Acceptance, physical reservation, committed Device binding, verification conditions |
| Device | Canonical physical asset identity and registered components, written by the registrar | Observations and verified placement tied to an accepted installation |
| Cable | Typed endpoints and termination mappings; the authoritative connection plan | Reservation and physical path verification |
| PowerFeed | Supply termination, rated/usable limits, known upstream dependencies | Verified dependency evidence and capacity accounting |

These are semantic contracts, not finalized field schemas. The initial design
keeps operator intent apart from registrar-owned identity and verifier-owned
observations using admission, status subresources, and narrowly scoped writers.
Exact field paths and resource scopes require schema review before generation.

The following illustrates local authoring references only; it is not deployable
configuration and omits the evidence and policy needed for acceptance:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Facility
metadata:
  name: example-facility
  namespace: infra-ops
spec:
  displayName: Example Facility
  locationRef:
    name: example-location
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: Rack
metadata:
  name: example-rack
  namespace: infra-ops
spec:
  facilityRef:
    name: example-facility
  heightUnits: 42
```

`infra-ops` is an illustrative namespace within the owning project API surface,
not a project identity or an authorization boundary by itself.

### References, identity, and scope

Inventory objects are proposed as project-scoped operator resources, canonically
stored in an infrastructure-owning Service Provider Project. Local references use
names. Cross-project references must also identify the owning project and resource
kind and be authorized at both ends; their exact public encoding remains open.
The service resolves and privately retains exact project and object incarnations
and approved revisions before reservations or effects. Kubernetes UIDs are
API-server-local evidence, not portable authored cross-control-plane references.

The first delivery keeps a physical installation graph in one provider project.
Shared facilities across provider projects require an explicit canonical owner,
authorized reference resolution, and coordinated reservation authority before
enablement. Mirrored views must be read-only, carry provenance, and never become
alternative reservation authorities. An external duplicate asset record must not
create additional supply; enforce enrolled asset uniqueness across provider
projects before enabling multi-provider allocation.

Facility, Device, MachineCapacity, MachineAllocation, Installation, and managed
NodeClaim identities remain distinct. A relationship does not imply cascading
garbage collection or ownership transfer. Recreating a same-named object cannot
inherit the predecessor's reservations, evidence, or execution authority.

### Conditions and visibility

Use `True`, `False`, and `Unknown` conditions with observed generation, reason,
message, and transition time. The inventory contract separates `Accepted`,
`Registered`, identity/placement/path verification, and `Ready`. A summary must
identify its evaluated scope: verified Facility presence is not verified rack
position. Missing or stale evidence is unknown, not successful or not applicable.

Fleet privately retains observation time, evidence provenance, dependency
revisions, and freshness bounds, and reevaluates affected gates when evidence
expires. Conditions alone do not authorize effects. Facility-level summaries
must not replace per-asset and per-operation checks.

Consumers receive permitted location, capabilities, allocation progress, and
their own operation results. Serial numbers, BMC endpoints, physical topology,
provider credentials, evidence internals, and other tenants' usage remain private
unless a separately authorized contract explicitly exposes a safe subset.

## Architecture

### Project and control-plane responsibilities

| Boundary | Canonical state and responsibilities | Must not do |
| --- | --- | --- |
| Service Provider Project | Infrastructure inventory, provider offerings and capacity, protected allocation/operation custody under service-only permissions | Transfer Device ownership to consumers or let providers edit allocator-owned bindings |
| Consumer Project | Local published offering references, permitted profiles, Machine and Installation requests, consumer-owned network intent | Edit source inventory, self-authorize source use, or inspect other allocations |
| Fleet control plane | Independently recoverable controllers, admission coordination, registry/verifier, publication integration, lifecycle orchestration, private evidence and operation services | Treat its deployment cluster as the implicit canonical store for project objects or federation intent |
| Federation Control Plane | Canonical admitted handoff objects and explicit targeting policy for delivery to selected location/cell control planes | Allocate capacity, grant consumer authority, broadcast privileged work, or change admitted inputs |
| Location/cell control plane | Delivered work and local execution/evidence adapters; integration with networking and qualified provider executors | Select another asset, expand permitted effects, or release global reservations independently |

A project is a tenancy and authorization boundary, not necessarily a dedicated
physical cluster. A single organization can operate provider and consumer projects;
Datum's managed platforms use Consumer Projects like other customers. Service
Provider Projects are not automatically federation members, and Consumer Projects
are not execution cells.

The Fleet control plane is the service's management runtime and trust boundary.
It accesses project APIs through authenticated, scoped clients. Provider-project
allocation custody remains canonical under the existing lifecycle contract;
private evidence and journals need a documented durable placement and retention
policy. Runtime caches and indexes are rebuildable views, never new allocation
authorities. This proposal does not move all provider inventory into one global
Fleet API server.

The Federation Control Plane is the intended Karmada hub described in the API
architecture. Coordination CRDs must exist on the hub and participating cells.
Fleet writes admitted intent to the hub, federation delivers only to the selected
cell, and that cell reports execution status directly to the hub. Federation must
not overwrite cell-owned status. No wholesale replication of provider or consumer
projects is required, and privileged credentials do not travel in handoff objects.

### Components and authoritative writers

- **Inventory admission and registrar:** validate plans and reserve physical
  resources; the trusted registrar establishes actual asset identity and binding.
- **Collectors and verifier:** accept attributable observations and evaluate the
  exact approved dependency graph without modifying operator intent.
- **Publication integration:** deliver provider-owned offering references and safe
  summaries through Milo service enablement; publication does not grant source use.
- **Shared lifecycle coordinator:** preserve caller identity, check IAM,
  entitlement, quota and current prerequisites, and retain exclusive allocation
  and exact authorized operation inputs before execution.
- **Handoff controller:** publish retained operation intent to the federation hub
  and validate returned evidence against the selected asset, cell, and operation.
- **Location adapters and provider executors:** execute only admitted effects,
  journal attempts before effects, and return evidence without granting new work.
- **Status projectors:** expose audience-appropriate progress without becoming
  writers of intent, reservations, or source-use grants.

These are logical responsibilities, not a requirement for a separate deployment
per component. Admission, privileged execution, and consumer access nevertheless
require enforced trust boundaries even when components share infrastructure.

### End-to-end lifecycle

1. **Plan and verify:** an operator in a Service Provider Project registers a
   Facility and Rack, submits installation claims and connections, and obtains
   physical reservations. Trusted registration and evidence establish actual
   equipment and verified prerequisites without powering or provisioning it.
2. **Prepare and publish:** separately authorized provider work prepares eligible
   equipment. Provider capacity and class definitions remain in that project;
   service publication places references in eligible Consumer Projects.
3. **Request and admit:** a consumer creates a Machine and, when needed, an
   Installation. Fleet validates the authenticated caller's source-use authority,
   project state, entitlement, quota, location eligibility, and current provider
   evidence. It retains an exclusive allocation and exact operation plan before
   submitting any executable handoff.
4. **Dispatch and execute:** Fleet publishes the admitted location work to the
   Federation Control Plane. Targeting chooses exactly the authorized cell, whose
   adapters request networking work from its owner and invoke only the permitted
   provider effects. The retained recipe defines ordering and prerequisites;
   delivery is not permission to run every operation concurrently.
5. **Verify and report:** the cell reports attributable results to the hub. Fleet
   checks target identity, operation revision, freshness, and required evidence
   before projecting readiness into the Consumer Project. Successful replication
   or a running controller is not successful execution.
6. **Release and recover:** release blocks new consumer effects, accounts for
   in-flight work, revokes sessions and capabilities, and verifies location network
   cleanup or fencing before required provider cleanup and quota settlement.
   Capacity returns to supply only after all release obligations are proven.

The durable admission-to-handoff protocol must recover a crash between reservation
and publication without reserving twice or losing cleanup ownership. An unpublished
admission remains reserved and recoverable; replay of a published handoff retains
the original operation identity. Storage and commit mechanisms remain an
implementation decision, not an assumed cross-API-server transaction.

### Integrations and boundaries

- **Milo:** project identity, IAM, protected resources, service enablement, quota,
  and project lifecycle. Broad controller credentials do not stand in for caller
  authorization; a project name or delivered class reference is not a grant.
- **Location:** geographic identity and permitted location availability. A
  Facility references geography; a future Site expresses placement policy over
  eligible Facilities rather than being another premises identity.
- **Networking/IPAM:** logical attachments, address allocation, switch
  configuration, and their cleanup evidence. Fleet supplies authorized physical
  prerequisites and consumes results, not a second network configuration writer.
- **Galactic and other services:** fabric/routing intent, DNS, certificates, and
  service addresses remain with their owners.
- **Provider backends:** hardware operations and observations through qualified
  adapters. Exact management endpoints and credentials remain private to execution.

### Failure handling and independent management

Unknown execution, verification, or cleanup retains reservations or quarantines
capacity. Recovery queries the retained operation and evidence before retrying.
Executor takeover requires fencing at the actual effect boundary; a controller
lease or federation retarget alone cannot exclude an old remote worker.

If a project API, Fleet runtime, or federation hub becomes unavailable, new work
requiring that authority stops. Previously admitted work may proceed only within
its retained scope and fencing contract; disconnected cells cannot admit new
allocations or infer cleanup success. Buffered evidence must be attributable and
reconciled when connectivity returns. Running workloads must not be disrupted
merely because management is unavailable.

Consumer project deletion does not remove provider-side cleanup obligations.
Provider project retirement must block while inventory reservations, allocations,
or cleanup custody remain, unless an explicit authorized custody transfer is
completed. Deleting a Facility cannot cascade into destroying devices. Restoring
old state must not resurrect released authority or replay work on a new consumer.

Fleet management, provisioning, artifact delivery, credential custody, and recovery
storage must be independently recoverable from the workloads they manage. The
deployment design must identify their bootstrap dependencies and failure domains.
Federation is a delivery mechanism, not a substitute for that recovery design.

## Delivery scope and acceptance

### 1. Inventory-only foundation

Finalize scopes, IAM and field writers for the six inventory kinds. Introduce
schemas and validation, durable physical reservations, trusted registration, and
verification for one qualified installation graph in one provider project.
Import occupied equipment observationally, preserving existing ownership and
unavailable capacity until explicit adoption is complete.

Acceptance requires conflicting rack, port, power, and identity claims to be
rejected under concurrency; wrong-host and stale evidence must not become ready.
Imports and ordinary record creation must cause no power, boot, network, or
allocation effects. Facility-only placement must not claim rack verification.

### 2. Provider and consumer integration

Connect verified inventory prerequisites to Metal supply and its existing shared
allocator. Exercise one Service Provider Project and two Consumer Projects with
real project identity, source-use checks, entitlement, quota, and durable custody.

Acceptance requires exactly one winner for competing allocations, no physical
data leakage, no grant through forged publication, and cleanup that survives
consumer project deletion. Namespace stand-ins alone do not establish this gate.

### 3. Federation and qualified execution

Define the coordination schema, exact-target propagation policy, status writer,
credential boundaries, admission-to-handoff recovery, and executor fencing.
Qualify one location and disposable hardware/backend combination before expansion.

Acceptance requires lost responses, duplicate delivery, stale status, hub/cell
outages, wrong-cell targeting, credential revocation, and stale executor takeover
to fail safely. No uncertain cleanup may expose reusable supply. Complete the
standalone lifecycle with a supported non-Talos OS and no managed-cluster or
ongoing guest-agent prerequisite.

### 4. Operational expansion

Add qualified moves, maintenance, retirement, and authorized shared-facility
references, followed by additional providers and managed consumers. Evaluate
whether field work warrants a dedicated operation resource from actual use.

Acceptance requires movement to preserve asset history without releasing old
physical reservations prematurely, replacement to create a new asset identity,
and maintenance to respect active allocation authority. Test backup restoration
and project retirement before expanding the ownership graph.

### Rollout and observability

Enable observation before reservation, and reservation before any provider
effects. Stop new admissions on rollback while keeping recovery and cleanup
available for retained operations; disabling controllers does not cancel remote
work or erase custody. Each output changes authoritative writer only through an
explicit transition after observational comparison.

Track reservation conflicts, verification age, blocked prerequisites, handoff
delivery/status lag, unknown operations, cleanup backlog, and quarantined supply.
Audit actor, project, admitted operation, exact target, and result without logging
credentials or rendered secrets. Service objectives and retention periods remain
to be selected from pilot evidence.

## Alternatives and open decisions

Keeping Facility data embedded in Device duplicates shared facts and loses its
independent lifecycle. A separate facilities API group is possible if facilities
become an independently owned product, but adds no useful boundary for this scope.
Putting all inventory on the federation hub would confuse delivery authority with
project ownership; putting all state in the Fleet deployment cluster would also
hide the canonical project contracts. Neither is proposed.

Before implementation, decide:

- Exact project/API namespace scopes, cross-project reference encoding, and IAM
  permissions for operator intent, registration, evidence, and destructive work.
- Canonical Facility ownership and deduplication, and the shared reservation
  protocol when multiple providers operate within one premises.
- Durable reservation, journal, and evidence storage; atomicity, restoration,
  retention, and explicit project-retirement custody transfer.
- Qualified geometry, power/redundancy accounting, evidence sources and freshness,
  and which technician attestations are accepted for the first facility.
- Coordination schema, federation targeting/status enforcement, transport identity,
  and executor fencing; criteria for keeping any provider work management-local.
- Deployment topology and bootstrap dependencies for independent Fleet recovery.
- Whether maintenance and field work need public kinds after the initial pilot.

These decisions must be recorded with implementation evidence. The enhancement
does not select their answers implicitly through illustrative names or examples.
