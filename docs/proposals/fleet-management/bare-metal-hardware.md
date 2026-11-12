---
status: provisional
stage: alpha
latest-milestone: "v0.1"
---

<!-- omit from toc -->
# Machine allocation and hardware providers

Part of the [Fleet infrastructure management service](README.md). This subsystem
owns compute capacity and its exclusive allocation. Both standalone projects and Fleet's
cluster controllers use it. Physical installation planning belongs to
[inventory](inventory.md); owned-server execution belongs to
[bare-metal provisioning](bare-metal-provisioning.md); software incarnation and
managed configuration belong to [OS configuration](os-configuration.md).

The filename is retained for existing links. Allocation applies to owned bare
metal, rented servers, and provider-created virtual machines. All schemas below
are proposals. Provider and consumer projects are separate authorization and API
contexts; namespaces organize resources within them. Project identity is derived
from authenticated context, not namespace or project names supplied in a request.
Any API-server-local UID retained for recreation detection is private evidence,
not an authored or cross-control-plane reference.

- [Summary](#summary)
- [Proposal](#proposal)
  - [Capacity publication](#capacity-publication)
  - [Hardware class resource](#hardware-class-resource)
  - [Provider capacity resource](#provider-capacity-resource)
  - [Customer Machine resource](#customer-machine-resource)
  - [Allocation and activation](#allocation-and-activation)
  - [Runtime access and networking](#runtime-access-and-networking)
  - [Release and replacement](#release-and-replacement)
  - [Infrastructure providers](#infrastructure-providers)
  - [Status](#status)
  - [Replaced artifacts](#replaced-artifacts)
- [Implementation](#implementation)
- [Rollout plan](#rollout-plan)
- [Acceptance gates](#acceptance-gates)

## Summary

Operators define MachineClasses over provider-owned `MachineCapacity`. Service
publication registers local MachineClasses that reference those definitions in
eligible projects. Customers reference those local classes to create a `Machine`
in their own project. A private, service-owned `MachineAllocation` reserves one
provider `MachineCapacity` for that customer `Machine`. One shared allocator
serves all projects and classes; provider backends cannot allocate independently.

`DeviceClaim` and customer `Machine` have separate meanings. The first is an
operator's expected physical installation, fulfilled by a registered `Device`.
The second is the customer's durable handle for allocated capacity. Provider
`MachineCapacity` expresses eligibility and lifecycle policy for capacity backed
by that asset. These resources have independent lifetimes.

## Proposal

### Capacity publication

The provider's MachineClass defines a public capability contract, private supply
selection, and provider class. Service publication registers source references
in consumer projects; service entitlement and IAM determine who can use them.
Project selection and publication policy remain outside MachineClass.

Publication makes the offering discoverable; it does not reserve inventory.
The same finite supply can serve requests from multiple consumer projects. The
shared allocator validates protected publication provenance and current policy
before allocating. Customers see neither private provider configuration nor
other projects' Machines. See [publishing machine classes across projects](capacity-publication.md)
for the publication YAML, lifecycle, and authorization contract.

### Hardware class resource

A MachineClass is either a provider definition or a consumer reference. Both are
project-scoped: cluster-scoped within their project's API, with no namespace.
The source definition holds hardware and access capabilities, private supply
selection, and permitted profile policy. Private backend qualification is not a
public Fleet reference. A reference has only
`spec.source`; its controller-written status presents safe resolved capabilities
and permitted availability. For owned metal, the private location adapter retains
qualified preparation, boot, and disk-selection configuration without adding
another Fleet machine identity. OS images, cluster membership, BGP policy, and
tenant network topology remain separate from the hardware offering.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineClass
metadata:
  name: metal-standard
spec:
  source:
    project: datum-metal
    name: datum-metal-standard
```

Definition and reference fields are mutually exclusive. The mode and source are
immutable; resolution follows one hop to a definition and rejects chains or
self-reference. Source-use permission is required before revealing source details.
Project-wide definitions explicitly scope selection of namespaced provider supply.

Distinguish hardware-supported, policy-permitted, and currently available
capabilities. Admission rejects an unsupported mandatory capability rather than
silently dropping it. Admission pins the exact local and canonical source class
incarnations, public requirements, authenticated project identities, and source
revision in protected evidence. Later edits cannot weaken
a pending request or alter a committed allocation. The class contains no
consumer-editable route to provider inventory. A copied or forged class cannot
authorize a Machine.

### Provider capacity resource

For owned hardware, `MachineCapacity` references the actual registered `Device`,
not the `DeviceClaim`'s expectation or an independently authored copy of physical
facts. Operators first register and verify the asset, then explicitly prepare it
before enabling allocation. The `deviceRef` relationship is immutable; replacing
hardware creates a new `Device` and a new `MachineCapacity` rather than retargeting
existing capacity.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineCapacity
metadata:
  name: iad1-r12-srv01
  namespace: infra-ops
  labels:
    fleet.datumapis.com/capacity-pool: general-metal
spec:
  deviceRef:
    name: server-example-001
  allocationPolicy: Disabled
  cleaningPolicyRef:
    name: tenant-reuse-v1
  reclaimPolicy: Retain
```

`allocationPolicy: Eligible` permits selection only when current identity,
physical readiness, preparation, health, and network feasibility also pass. It is
not an instruction to wipe, power on, or evict an existing allocation. Disabling
allocation prevents future assignments; maintenance of active consumers needs its
own coordinated transition.

Owned `MachineCapacity` resolves physical placement through `Device` and its
active `DeviceClaim`. Site selectors filter those verified Facility
relationships. `MachineCapacity` does not carry a second editable rack, facility,
serial number, or cable map. Provider-specific management enrollment and
connection material remain private backend state. Reclaim policy describes the
operator's retention/release policy; a consumer cannot weaken cleaning
requirements or discard a retained asset.

The owned `Device`/`MachineCapacity` association is one-to-one and immutable.
Admission may retain API-server-local object identity privately to detect deletion
and recreation, but Kubernetes UIDs are not authored references or cross-control-
plane identity. Operator credentials, low-level endpoints, adapter records, and
billing state remain private.

<a id="claim-resource"></a>

### Customer Machine resource

A standalone project can request capacity directly. A Fleet NodeClaim controller
creates the same resource for a managed node. The customer-facing `Machine` never
needs a Cluster or NodeClaim reference to execute its contract.

The `Machine` and referenced MachineClass and MachineProfile are local to the consumer project. Its
eventual `MachineCapacity` can be in a different provider project; only the
shared allocator resolves that private relationship. The caller cannot select
foreign capacity or grant itself provider access through a reference.

Both `machineClassRef` and `machineProfileRef` are required and immutable. The
class selects an offering, access policy, and eligible supply; the profile selects
the requested hardware shape. A profile is not a capacity identifier and does not
grant permission to use the class. Admission resolves and retains both contracts
before allocation.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Machine
metadata:
  name: db-01
  namespace: acme
spec:
  machineClassRef:
    name: metal-standard
  machineProfileRef:
    name: standard-metal
  locationRef:
    name: us-east-1
  powerState: "On"
  networkAttachmentRefs:
    - name: db-01-primary
  initialInstallationRef:
    name: db-01-os-01
```

An operator-controlled managed Machine may additionally select a Site policy with
`siteRef`. Location and Site constraints must agree. Exact Facility constraints
are authorized references to inventory Facilities, not provider codes. Public
consumers receive only placement controls and topology details their offering
permits. Required placement and class fields are immutable after binding.

`powerState` is persistent runtime intent on the allocation. It does not live on
an Installation and is not a reboot command. A consumer can request a blank,
powered-off Machine by omitting `initialInstallationRef` and selecting `"Off"`.
Power operations remain serialized with preparation, installation, and release.

The initial Installation is an optional immutable one-time activation gate. It is
not a perpetual desired OS: deleting an old completed request never reopens that
gate. Reinstallation uses a new immutable request with an expected predecessor,
as defined in [OS configuration](os-configuration.md#installation-resource).

<a id="binding-and-activation"></a>

### Allocation and activation

Before committing an allocation, the allocator:

1. Derives the consumer project identity from authenticated context, authorizes the
   caller, and resolves the local class and profile through protected publication provenance.
   It validates current source-use permission, consumer entitlement and
   allocation quota, location/Site, networks, and image/configuration inputs.
2. Resolves every required initial reference, retains its exact local incarnation
   and relevant input digest, and checks current network placement feasibility. Missing references
   leave the Machine pending; creation order cannot accidentally bypass them.
3. Selects an eligible, prepared `MachineCapacity` from the canonical class's
   provider project and explicit supply namespace, matching physical placement,
   capacity, profile, capability, and network constraints.
4. Creates one immutable, service-owned `MachineAllocation` and commits its quota
   hold with current policy preconditions. The allocation references the consumer
   `Machine` and provider `MachineCapacity` by their authoritative project context
   and name. Protected admission evidence retains the exact local object
   incarnations, class revisions, authorization decision, admitted public
   contract, and quota hold. Competing projects and overlapping classes cannot
   allocate one `MachineCapacity` twice or allocate one customer `Machine` twice.
5. Starts the admitted activation work for that exact `MachineAllocation`, if
   requested.

`MachineAllocation.metadata.name` is the allocation identity; its spec is
immutable, release is terminal, and a released allocation is never reactivated.
Reallocation creates a newly named allocation, so the contract needs neither a
duplicated binding identifier nor an epoch. API-server-local UIDs may be retained
as private admission evidence to reject delete-and-recreate races, but they are
not authored references and do not cross project or location control planes. If
no eligible owned capacity exists, report `InsufficientCapacity`. Procurement
through a dynamic provider requires separate authorized policy, quotas, and
idempotency; it does not bypass the allocator.

Network feasibility before allocation is distinct from NetworkAttachment Ready,
which requires actual hardware. After allocation, networking reserves resources and
establishes isolated provisioning connectivity. The provider deploys through that
isolation, verifies the maintenance agent has exited, and coordinates the switch
to customer connectivity before normal customer disk boot. See the
[worked handoff](examples/owned-bare-metal/README.md).

### Runtime access and networking

The public handle is the customer `Machine`. The service authorizes each
operation against its current `MachineAllocation`, capabilities, and principal.
Private `MachineCapacity`/`Device`/BMC mapping is not a customer-editable
reference.

- **Console:** an authenticated operation creates a short-lived serial or
  graphical session scoped to the principal, project, customer Machine, and
  immutable MachineAllocation.
  Keep bearer capabilities out of generally readable status. Release actively
  disconnects existing streams as well as denying new ones.
- **Power:** reconcile persistent On/Off intent. Reboot/reset is a bounded,
  idempotent operation with MachineAllocation preconditions. A stale reset cannot affect
  a later consumer.
- **Rescue or media:** explicit supported operations with admitted artifacts,
  bounded lifetime, and conflict checks against installs and cleanup.
- **Network:** a network-domain NetworkAttachment targets the customer Machine and its
  published interface slot. For owned or physically visible infrastructure, the
  network controller resolves actual inventory ports and owns switch configuration
  and IPAM integration. Opaque rented/virtual offerings use authorized provider
  attachment APIs and normalized endpoint observations without inventing physical
  inventory. Both paths retain one network configuration authority. Private DNS
  for the machine is served from the platform node where its attachment lands;
  see [standalone machines](node-services.md#standalone-machines).

Raw BMC administration is a separate privileged offering if supported. Brokered
console/power does not imply access to operator management networks or shared
credentials. Cluster-managed Machines have a coordinator as their runtime authority;
operator overrides and authority transfers are explicit, audited maintenance.

After standalone installation handoff, the consumer owns its OS. Ongoing managed
OS reconciliation requires a separate admitted management contract; a Fleet
controller cannot infer that permission from physical ownership.

### Release and replacement

Deleting a completed Installation does not wipe or release the customer Machine.
Deleting a Machine begins the release protocol:

1. Reject new consumer effects and stop or account for in-flight operations.
2. Revoke sessions, media/bootstrap capabilities, and credentials; actively close
   console streams.
3. Detach or fence consumer networking while preserving protected cleanup access.
4. Complete the operator-selected cleaning/restoration policy with per-device
   evidence. Unknown outcomes retain the reservation or quarantine the asset.
5. Finish provider release if policy requires it, or return retained
   `MachineCapacity` to eligibility once all gates pass.

Cross-consumer assurance includes relevant firmware/BMC state and device access,
not only filesystem or disk erasure. Owned assets are retained independently of
consumer garbage collection. Finalizer force-removal is not a supported shortcut
to declare an uncertain machine available.

Consumer project termination initiates this same release from retained service
records. Cleanup authority, project identity tombstones, reservations, and exact
network/artifact revocation obligations survive the consumer API's removal.
Unreachable project APIs are not evidence of deletion. Quota accounting uses
durable MachineAllocations rather than counts of surviving Machine objects.
Offering withdrawal prevents new allocations; it does not erase active authority or release
capacity. See the [cross-project lifecycle](capacity-publication.md#changes-withdrawal-and-project-deletion).

A failed server is never substituted inside an existing customer Machine.
Replacement creates a new Machine and Installation. For managed clusters,
NodeClaim coordinates
drain, predecessor fencing, logical identity transfer, and joining the successor.
Standalone consumers use an explicit replacement workflow; names and IPs do not
silently transfer. Moving or replacing physical inventory has its separate
[DeviceClaim lifecycle](inventory.md).

### Infrastructure providers

The provider contract separates capacity acquisition from effect execution:

- **Adopt/acquire:** Establish concrete provider identity and capabilities; return
  `MachineCapacity` to the shared allocator.
- **Inspect/prepare:** Perform bounded, separately authorized work against enrolled
  identity; preserve unknown evidence.
- **Install:** Execute a retained Installation plan with exact asset, MachineAllocation, artifact,
  configuration, and disk preconditions.
- **Runtime access:** Perform authorized power/console/media operations for the
  current MachineAllocation.
- **Clean/release:** Return verifiable results; do not independently free the shared
  reservation.

The owned provider integrates a pluggable
[provisioning execution layer](bare-metal-provisioning.md#execution-layer-contract).
A static servers.com adapter adopts rented hosts and performs qualified operations; a
NetActuate adapter may acquire instances under procurement policy. Vendor region
and facility codes map to provider-neutral Facility references inside adapters.
Routing policy and logical connectivity remain with networking rather than
MachineClass or vendor install configuration.

No provider is allowed to make a generic "reconcile" retry destructive. Persist
operation authority before an external effect. On timeout, discover the existing
operation's result and reconcile it rather than assuming failure and issuing a
second request. Provider credentials cannot expand the originating authorization.

### Status

Report independently actionable conditions such as Allocated, Prepared,
PhysicalReady, Available, and Ready, tied to evaluated generations and dependency
revisions. `MachineCapacity` status may report operator-only provider billing,
physical health, and allocation observations. Public `Machine` status reports
permitted placement, interface information, power, console capabilities, and
handoff progress.

Readiness means the published platform handoff contract is satisfied. Without a
guest-agent contract, GuestReady remains Unknown; successful image writing and
power-on do not prove SSH or application health. Drift does not silently change
the committed MachineAllocation or authorize eviction.

### Replaced artifacts

- **Vendor host IDs and billing artifacts:** Typed provider records and observed provider
  state.
- **Serial/UUID, rack and connector maps:** Fleet inventory DeviceClaim, Device, Cable and
  component identities.
- **Facility-code maps:** Adapter mapping to provider-neutral inventory Facilities.
- **Per-box disk WWIDs:** Observed device components and exact targets pinned in admitted
  preparation/installation plans.
- **Bonds, uplink addressing and BGP policy:** Network attachment/configuration and
  IPAM/routing authorities.
- **Pool-to-hardware selection:** Shared allocator and immutable MachineAllocation.

## Implementation

Fleet's allocation controller owns the durable MachineAllocation and runtime admission
service. Its storage and conflict protocol must support controller retries,
restarts, and stale-worker exclusion. Kubernetes status reports outcomes but is
not the only record of irreversible-operation authorization.

The catalog publisher owns consumer MachineClass references, resolved summaries,
and protected provenance. The allocator separately validates current policy and
commits both allocation uniqueness keys and quota holds through a recoverable transaction.
Project-scoped IAM limits the provider and consumer scopes each controller may
serve. Broad controller credentials never become consumer permission.

Provider controllers consume admitted operations and report execution evidence.
Inventory, networking, OS rendering, and cluster controllers keep separate
permissions and responsibilities. Controller dependencies follow these contracts,
not a blanket rule forbidding necessary authorization or readiness reads across
subsystems.

## Rollout plan

Introduce schema and observation first, then register references to one provider
MachineClass in two distinct consumer projects and exercise one shared allocator
against provider-owned MachineCapacity.
Use standalone and managed lab consumers. Qualify owned-metal preparation/reuse before
customer exposure; adopt rented active machines without reinstalling them. The
[umbrella rollout](README.md#rollout-plan) coordinates these tracks with inventory,
OS configuration, and cluster orchestration.

## Acceptance gates

Prove exclusive allocation under concurrent requests, rejected unauthorized
references, unavailable capacity remaining pending, initial-reference creation
order independence, and network feasibility before reservation. Exercise crashes
and timeouts at each hardware effect, concurrent reinstalls, stale power/network
operations, console revocation, failed cleanup, customer Machine name reuse, and hardware
replacement. Confirm that policy edits and deletion of historical installation
requests cannot reinstall or free an active customer's machine.

Also prove automatic catalog registration, trusted publication provenance,
cross-project quota and entitlement enforcement, one winner for shared MachineCapacity,
source-use revocation during commit, and release after consumer project deletion.
