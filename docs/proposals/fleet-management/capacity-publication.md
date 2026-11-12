---
status: provisional
stage: alpha
latest-milestone: "v0.1"
---

<!-- omit from toc -->
# Publishing machine classes across projects

Part of the [Fleet infrastructure management service](README.md) and its
[machine allocation subsystem](bare-metal-hardware.md). Datum defines machine
classes over provider-owned supply and registers references in eligible consumer
projects. Customers use those local classes to discover capabilities and request
shared capacity.

- [Customer and operator experience](#customer-and-operator-experience)
- [Class and publication responsibilities](#class-and-publication-responsibilities)
- [Publication example](#publication-example)
- [Cross-project allocation](#cross-project-allocation)
- [Changes, withdrawal, and project deletion](#changes-withdrawal-and-project-deletion)
- [Service integration](#service-integration)
- [Acceptance gates](#acceptance-gates)

## Customer and operator experience

An operator defines a MachineClass in the provider project: the hardware and
access capabilities offered, eligible MachineProfiles, and the eligible supply.
Private backend configuration governs acquisition or preparation. Service publication determines which
consumer projects receive a local MachineClass referencing that definition.
Project selection and entitlement are separate from the capacity definition.

Customers discover available classes through their own project. Each local class
has a source reference and a read-only summary of the resolved public capabilities.
Permitted location and availability summaries help customers choose capacity;
they expose neither private inventory nor other customers' allocations.
Availability is an observation with an evaluation time, not a reservation or a
promise that a subsequent request will succeed. An unknown or stale observation
must not appear as zero stock or guaranteed capacity.

A customer creates a `Machine` referencing a local class and MachineProfile. Fleet checks current
source-use permission, entitlement, and quota, then attempts to reserve compatible
inventory in the provider project. Multiple projects can reference the same
source class and compete for the same finite supply. A valid request remains
pending with `InsufficientCapacity` when no eligible machine can satisfy it.

The two references are required and immutable: `machineClassRef` selects the
published offering and its eligible supply, while `machineProfileRef` selects the
requested hardware shape. Neither a copied profile nor knowledge of its name
grants use of the offering.

The selected `MachineCapacity`, its `Device`, and private management configuration
remain in the provider project throughout allocation. The customer operates its
Machine, network attachments, Installation, and delegated access in its own
project. Release returns retained capacity to the shared pool only after required
cleanup.

The first supplier is Datum's own infrastructure operation. Third-party provider
onboarding and marketplace behavior are separate enhancements.

## Class and publication responsibilities

- **Provider MachineClass definition:** The canonical hardware and access
  contract, permitted profile policy, and private supply selection. It does
  not select consumer projects or contain publication policy.
- **Consumer MachineClass reference:** A project-local name whose entire spec is
  `source: {project, name}`. Consumers cannot override the source definition.
  Fleet reports sanitized resolved capabilities and permitted availability in
  status; these observations are not allocation authority.
- **Service publication and IAM:** Register references in eligible projects and
  govern who may use each source class. Class delivery and permission are separate
  responsibilities. Neither a copied reference nor a controller's broad provider
  credentials grant the consumer access.
- **MachineProfile:** Immutable hardware requirements published into the consumer
  project alongside an offering, or customer-authored when the class explicitly
  permits custom profiles. A profile describes desired shape; it neither selects
  provider inventory nor grants allocation authority.
- **Provider adapter configuration:** Reusable acquisition or hardware
  preparation requirements remain private to the selected provider adapter and
  do not decide which projects receive the capacity definition.
- **MachineCapacity:** Actual capacity in the provider project, with current eligibility
  and a single global reservation identity. Several classes may select overlapping
  supply without duplicating it. Enforce the canonical Device/MachineCapacity association
  across provider projects; another record for the same enrolled physical asset
  cannot create additional allocatable supply.
- **Machine:** The consumer-owned lifecycle resource. A private, service-owned
  MachineAllocation can reserve provider MachineCapacity across project boundaries
  without transferring ownership of the asset.

MachineClass has mutually exclusive **definition** and **reference** modes. A
reference may contain no definition fields or overrides. `spec.source` and the
mode are immutable; retargeting requires replacement. Resolution follows exactly
one hop to a definition: reject self-references, cycles, and reference-to-reference
chains. A definition can also serve authorized Machines in its own project.

Both modes are project-scoped, meaning cluster-scoped within that project's API,
with no namespace. Machines in authorized namespaces resolve the local class by
name. A definition must explicitly scope selection of namespaced Machines and
references to namespaced provider resources; it cannot infer provider scope from
a consumer Machine. This model needs no separate Fleet publication or public allocation
kind for the initial version.

## Publication example

All Fleet shapes below are proposed. The provider project is `datum-metal`; the
consumer project is `acme`. Project identity comes from authenticated project API
context, never from a namespace, label, or project name supplied in a Machine.
API-server-local identity used to reject object recreation is retained privately;
projects can have separate API servers.

The operator defines the source class in the provider project:

```yaml
# Project context: datum-metal (provider). Project-scoped definition.
apiVersion: metal.datumapis.com/v1alpha1
kind: MachineClass
metadata:
  name: datum-metal-standard
spec:
  arch: amd64
  capabilities: [baremetal]
  profilePolicy:
    allowedProfiles:
      - name: standard-metal
  interfaces:
    - {name: primary, type: Ethernet}
  access:
    consoleModes: [Graphical]
    powerControl: true
  capacity:
    machineCapacities:
      namespace: infra-ops
      selector:
        matchLabels:
          fleet.datumapis.com/capacity-pool: general-metal
```

The selector is restricted to MachineCapacity resources in the source class's provider project
and explicit namespace. It cannot override preparation, physical verification,
or compatibility checks. Private backend configuration resolves within the
provider location. Matching labels never authorize purchasing
or creating more owned servers.

Separately, Fleet's service registration supplies local references through
`ServiceConfiguration.spec.provisioning`. The
[publication configuration](examples/owned-bare-metal/publication.yaml) shows this
platform integration as an excerpt. Service enablement determines the entitled
project cohort; IAM establishes source-use permission. Neither belongs in the
MachineClass definition.

The service writes this class into the consumer project, and Fleet resolves its
public summary:

```yaml
# Project context: acme (consumer). PLATFORM OUTPUT, not customer-authored intent.
apiVersion: metal.datumapis.com/v1alpha1
kind: MachineClass
metadata:
  name: metal-standard
spec:
  source:
    project: datum-metal
    name: datum-metal-standard
status:
  resolvedClass:
    arch: amd64
    capabilities: [baremetal]
    profilePolicy:
      allowedProfiles:
        - name: standard-metal
    interfaces:
      - {name: primary, type: Ethernet}
    access:
      consoleModes: [Graphical]
      powerControl: true
  conditions:
    - {type: SourceResolved, status: "True", reason: AuthorizedSource}
```

The source name identifies what to resolve; it is not proof of authorization.
Check consumer-scoped source-use permission before exposing resolved capabilities,
availability, or source details; a privileged resolver must not disclose foreign
inventory through status or distinguishable unauthorized lookup errors.
The resolver retains a protected mapping from the authenticated consumer project
and exact local MachineClass incarnation to the provider project, canonical
MachineClass incarnation, and evaluated contract revision/digest. Report the
evaluated revision, freshness,
and unresolved dependencies without exposing private supply selectors or provider
configuration. Copied status cannot establish that mapping. Only the publication
service manages these published reference entries; report a conflicting name
without overwriting or adopting an unrelated class.

The customer requests capacity using only local names:

```yaml
# Project context: acme (consumer). A blank allocation for this short example.
apiVersion: metal.datumapis.com/v1alpha1
kind: Machine
metadata:
  name: db-01
  namespace: acme
spec:
  machineClassRef: {name: metal-standard}
  machineProfileRef: {name: standard-metal}
  locationRef: {name: us-east-1}
  powerState: "Off"
```

The [complete customer example](examples/owned-bare-metal/customer.yaml) adds
networking and an initial OS installation. Customers never name another project's
MachineCapacity, Device, private provider configuration, or management Secret. Class publication
can precede available stock so customers can submit requests that wait for supply.

## Cross-project allocation

Publication distributes discovery; the shared machine coordinator owns
allocation. Its authorization and commit protocol must span the project APIs:

1. Derive the consumer project identity from authenticated context and authorize
   the caller's Machine operation. Resolve the local class and its single-hop
   source and referenced profile through protected identity records. Retain the exact local object
   incarnations, provider project identity, admitted public requirements, and
   source revision as private evidence.
2. Verify current consumer-scoped source-use permission, service entitlement,
   permitted locations, and allocation quota. Evaluate authorization through
   supported IAM principals and project scope, not the publisher's or allocator's
   privileged identity. Local class presence or cached `SourceResolved` status
   cannot substitute for these checks.
3. Resolve initial image, bootstrap, and network inputs under their respective
   access rules. Customer inputs stay in consumer scope; scoped artifact
   delivery gives execution only the approved inputs it needs. Provider
   credentials remain in provider scope.
4. Select MachineCapacity compatible with the resolved profile from the canonical class's authorized provider
   project and explicit supply namespace. Revalidate physical eligibility,
   preparation, network feasibility, and the admitted public requirements.
5. Create one immutable, service-owned MachineAllocation and commit its quota hold
   with current source-use, entitlement, and source-policy preconditions. Its
   generated metadata.name is the allocation identity. It references the customer
   Machine and provider MachineCapacity by authoritative project context and name;
   protected evidence retains their exact local incarnations, class revision and
   digest, authorization policy revision, public contract, granted capabilities,
   and quota hold. The conflict protocol must prevent two Machines acquiring one
   MachineCapacity, one Machine allocating twice, quota oversubscription, or
   allocation after permission withdrawal.
6. Deliver authorized work to provider-side execution and customer-scoped
   networking and OS services. Public Machine status exposes permitted results;
   the private MachineAllocation and physical relationship remain private.

Machine admission pins the resolved identities and requirements. Source edits can
refresh discovery and govern future admissions, but cannot silently weaken a
pending request or change an existing allocation. A pending Machine can allocate only if
current policy still permits its retained requirements. New policy checks cannot
be bypassed by retaining an older source revision. Deleting or recreating a
project or class under the same name cannot inherit the old authorization;
replacement requires fresh publication/resolution, and prior Machines cannot
silently adopt the new identities.

The transaction must commit or recover the same reservation, policy decision,
and quota hold before effects. If one storage transaction cannot span them,
retain explicit preparation, commit, and abort records with equivalent conflict
guarantees. Quota accounting follows these retained records, not counts of
surviving consumer Machines. Close the quota hold at the durable release boundary;
releasing quota alone never makes unclean hardware available.

Controller privileges do not grant the initiating customer provider access.
Runtime operations validate the exact consumer Machine, immutable
MachineAllocation, and delegated capabilities. Work arriving from an old project
or Machine incarnation cannot affect a later tenant. The networking service owns the
authorized relationship between customer network intent and provider-side
physical endpoints; it does not expose switch credentials to the customer.

## Changes, withdrawal, and project deletion

- **Source definition changes:** Refresh the consumer's resolved status and
  publish evaluated revision/progress. Do not copy policy into the reference spec.
  Bindings and admitted operations retain their exact inputs. Source resolution,
  authorization, and available inventory are separate observations.
- **Publication changes:** Change which references service provisioning delivers.
  Removing a publication template is discovery cleanup, not immediate permission
  revocation. To stop new bindings, revoke source-use permission or entitlement;
  do not rely on eventual deletion of local classes. Zero stock also does not
  remove or recreate a class: it remains discoverable with observed availability.
- **Source-use or entitlement withdrawal:** Stop new bindings using authoritative
  policy, even if local class removal is delayed. Stop exposing protected source
  details and withdraw or mark the local entry unavailable. Existing allocations
  follow their retained contract and current runtime authorization; catalog withdrawal alone does
  not evict customers, reinstall machines, or weaken cleanup obligations.
  Suspension or administrative reclaim requires a separate audited decision;
  publication edits cannot expand existing runtime grants.
- **Local class deletion or name collision:** Preserve active allocations. Repair
  eligible managed entries through the publication service, establishing fresh
  private incarnation evidence for a recreated object. Never adopt a conflicting customer object
  merely because its name, source, or labels match.
- **Source class retirement or deletion:** Stop new bindings while preserving
  existing allocations, retained contracts, and cleanup obligations. Report the
  unresolved source to consumers. Removing a source definition does not authorize
  eviction; an active allocation must remain operable and releasable from its
  retained contract and current runtime permission.
- **Consumer project termination:** Deny new work, revoke active access, and run
  release from retained provider-side allocation records. Keep cleanup authority,
  exact network/artifact revocation obligations, and reservation state outside
  the disappearing project, together with private evidence of the terminated
  authenticated project identity. Project
  finalization must await cleanup or durably transfer it to the service's cleanup
  process. Lost API access is an unknown state, not proof that the project or
  allocation was deleted.
- **Provider project retirement:** Stop new allocations, coordinate separately
  authorized drain or release of active allocations, and retain obligations
  until cleanup is verified. Provider project deletion is blocked while it
  would discard required asset or operation authority.

Bindings and publication records use an explicit lifecycle protocol across
projects. They are not garbage-collection owner references; Kubernetes also
disallows cross-namespace owner references. See
[Kubernetes garbage collection](https://kubernetes.io/docs/concepts/architecture/garbage-collection/#owners-and-dependents).

## Service integration

Reuse the definition/reference model already used by
[provider IPClass definitions](https://github.com/datum-cloud/infra/blob/a712d2d3aaddcdc4d81c09d7f4cdd139bef06631/apps/network-services-operator/platform-project/classes.yaml)
and [networking service provisioning](https://github.com/datum-cloud/infra/blob/a712d2d3aaddcdc4d81c09d7f4cdd139bef06631/apps/network-services-operator/components/control-plane-networking/service-catalog-provisioning-patch.yaml).
Fleet's catalog publication service integrates with service enablement, registers
source references, and resolves safe consumer summaries. Its protected provenance
supports the shared coordinator; it cannot reserve MachineCapacity. The
[service architecture](architecture.md#coordinating-fleet-subsystems) shows these
separate responsibilities.

Existing service provisioning delivers the same configured objects to projects
with an active service entitlement. It has no per-resource project selector.
Use that entitled cohort for the initial publication model; class-specific
publication audiences belong in generic service/entitlement policy or a future
extension to that platform capability, outside MachineClass.

Publication does not currently establish consumer source-use grants. Fleet must
integrate authorization explicitly and enforce it again at allocation commit;
privileged delivery of a reference cannot stand in for that work. The platform's
force-apply delivery also needs admission/ownership checks that reject unrelated
class-name collisions. These are required integration changes, not guarantees of
today's provisioning controller. See the
[service provisioning contract](https://github.com/milo-os/service-catalog/blob/8b9e1c4989a79b3c806443c76b1ae2399354a0bf/api/v1alpha1/serviceconfiguration_types.go#L569)
and [authorization integration](https://github.com/milo-os/service-catalog/blob/8b9e1c4989a79b3c806443c76b1ae2399354a0bf/internal/controller/provisioning_controller.go#L303).

Fleet adopts IPAM's reference shape, not every allocation-lifecycle behavior.
[IPAM reference authorization](https://github.com/milo-os/ipam/blob/46163689d36374b9ebcef5aab41931dfb7a31942/internal/registry/ipam/ipclass/storage.go#L31)
permits existing references to survive source-use permission revocation. Fleet
requires current authorization at binding and immutable project/class identity
pinning. The existing
[Milo project authorization note](https://github.com/datum-cloud/infra/blob/a712d2d3aaddcdc4d81c09d7f4cdd139bef06631/apps/vpc-system/control-plane/milo-ipam-rbac/rbac.yaml)
also explains why plain Kubernetes RBAC does not interpret project identity from
authenticated user extras. Use supported project-scoped IAM principals and retain
the authenticated project identity privately; do not treat a source project name as an authorization
boundary.

## Acceptance gates

- Automatically register references and update safe summaries for eligible
  projects without copying private configuration; include later-created projects
  and service disablement.
- Reject mixed definition/reference specs, source retargeting, reference chains,
  cycles, self-reference, forged provenance, and unrelated class-name collisions.
- Reject unauthorized source use and project/class name reuse. Permission
  revocation must block new bindings even when a local class remains present.
- Race Machines from at least two consumer projects against one provider
  MachineCapacity, including overlapping source classes. The authenticated
  provider project must differ from both consumer projects; exactly one allocation
  can commit.
- Enforce allocation quota and entitlement at commit despite concurrent policy
  changes, retries, and delayed publication reconciliation.
- Keep exhausted capacity pending with actionable status. Stale availability
  must not reserve stock or expose another customer's allocation or inventory.
- Preserve pending requirements and existing allocations through source edits,
  local class replacement, and withdrawal; reject stale runtime operations after
  release or project recreation.
- Complete release or quarantine after consumer project deletion, controller
  failure, or source class removal before any cross-project reuse.
