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
from authenticated context and immutable Project UIDs, not namespace names.

- [Summary](#summary)
- [Proposal](#proposal)
  - [Capacity publication](#capacity-publication)
  - [Hardware class resource](#hardware-class-resource)
  - [Hardware resource](#hardware-resource)
  - [Claim resource](#claim-resource)
  - [Binding and activation](#binding-and-activation)
  - [Runtime access and networking](#runtime-access-and-networking)
  - [Release and replacement](#release-and-replacement)
  - [Infrastructure providers](#infrastructure-providers)
  - [Status](#status)
  - [Replaced artifacts](#replaced-artifacts)
- [Implementation](#implementation)
- [Rollout plan](#rollout-plan)
- [Acceptance gates](#acceptance-gates)

## Summary

Operators define MachineClasses over provider-owned capacity. Service publication
registers local MachineClasses that reference those definitions in eligible projects.
Customers reference those local classes to request machines held in the provider
project. MachineClaim delegates permitted runtime control while Machine retains
the provider's asset identity and eligibility policy. One shared binder serves
all projects and classes; provider backends cannot allocate independently.

DeviceClaim and MachineClaim have separate meanings. The first is an operator's
expected physical installation, fulfilled by a registered Device. The second is
consumer use of a Machine derived from that asset. They have independent lifetimes.

## Proposal

### Capacity publication

The provider's MachineClass defines a public capability contract, private supply
selection, and provider class. Service publication registers source references
in consumer projects; service entitlement and IAM determine who can use them.
Project selection and publication policy remain outside MachineClass.

Publication makes the offering discoverable; it does not reserve inventory.
The same finite supply can serve claims from multiple consumer projects. The
shared binder validates protected publication provenance and current policy
before allocating. Customers see neither private provider configuration nor
other projects' claims. See [publishing machine classes across projects](capacity-publication.md)
for the publication YAML, lifecycle, and authorization contract.

### Hardware class resource

A MachineClass is either a provider definition or a consumer reference. Both are
project-scoped: cluster-scoped within their project's API, with no namespace.
The source definition holds hardware and access capabilities, private supply
selection, and the typed provider-class reference. A reference has only
`spec.source`; its controller-written status presents safe resolved capabilities
and permitted availability. For owned metal, BareMetalMachineClass
describes qualified hardware preparation, boot, and disk-selection requirements.
OS images, cluster membership, BGP policy, and tenant network topology remain
separate from the hardware offering.

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
silently dropping it. Admission pins the local class UID, public requirements,
and canonical source class/project UIDs and revision. Later edits cannot weaken
a pending request or alter a committed allocation. The class contains no
consumer-editable route to provider inventory. A copied or forged class cannot
authorize a claim.

### Hardware resource

For owned hardware, Machine references the actual registered Device, not the
DeviceClaim's expectation or an independently authored copy of physical facts.
The provider extension contains private management details. Operators first
register and verify the asset, then explicitly prepare it before enabling
allocation.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Machine
metadata:
  name: iad1-r12-srv01
  namespace: infra-ops
  labels:
    fleet.datumapis.com/capacity-pool: general-metal
spec:
  deviceRef:
    name: server-example-001
  providerRef:
    apiGroup: baremetal.fleet.datumapis.com
    kind: BareMetalMachine
    name: iad1-r12-srv01
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

Owned Machines resolve physical placement through Device and its active
DeviceClaim. Site selectors filter those verified Facility relationships. A
Machine does not carry a second editable rack, facility, serial number, or cable
map. A provider-created VM has no invented physical Device; its provider reports
placement and capabilities with provenance. Opaque or unknown physical facts
cannot satisfy stronger placement or failure-domain requirements.

The one-to-one owned Device/Machine/provider association is enforced using UIDs,
not names. Rented-provider IDs and billing live in typed adapter records and
normalized observations. Operator credentials and low-level endpoints remain
private. Reclaim policy describes the operator's retention/release policy; a
consumer cannot weaken cleaning requirements or discard a retained asset.

### Claim resource

A standalone project can request capacity directly. A Fleet NodeClaim controller
creates the same resource for a managed node. MachineClaim never needs a Cluster
or NodeClaim reference to execute its contract.

The claim and referenced MachineClass are local to the consumer project. Its
eventual Machine can be in a different provider project; only the shared binder
resolves that private relationship. The caller cannot select a foreign Machine
or grant itself provider access through a reference.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineClaim
metadata:
  name: db-01
  namespace: acme
spec:
  machineClassRef:
    name: metal-standard
  locationRef:
    name: us-east-1
  powerState: "On"
  networkAttachmentRefs:
    - name: db-01-primary
  initialInstallationRef:
    name: db-01-os-01
```

An operator-controlled managed claim may additionally select a Site policy with
`siteRef`. Location and Site constraints must agree. Exact Facility constraints
are authorized references to inventory Facilities, not provider codes. Public
consumers receive only placement controls and topology details their offering
permits. Required placement and class fields are immutable after binding.

`powerState` is persistent runtime intent on the allocation. It does not live on
an Installation and is not a reboot command. A consumer can request a blank,
powered-off machine by omitting initialInstallationRef and selecting `"Off"`.
Power operations remain serialized with preparation, installation, and release.

The initial Installation is an optional immutable one-time activation gate. It is
not a perpetual desired OS: deleting an old completed request never reopens that
gate. Reinstallation uses a new immutable request with an expected predecessor,
as defined in [OS configuration](os-configuration.md#installation-resource).

### Binding and activation

Before committing an allocation, the binder:

1. Derives the consumer Project UID from authenticated context, authorizes the
   caller, and resolves the local class through protected publication provenance.
   It validates current source-use permission, consumer entitlement and
   allocation quota, location/Site, networks, and image/configuration inputs.
2. Resolves every required initial reference, pins its UID and relevant input
   digest, and checks current network placement feasibility. Missing references
   leave the claim pending; creation order cannot accidentally bypass them.
3. Selects an eligible, prepared Machine from the canonical class's provider
   project and explicit supply namespace, matching physical placement, capacity,
   capability, and network constraints.
4. Commits the quota hold and an exclusive durable reservation with current
   policy preconditions. Enforce uniqueness for both provider Project UID plus
   Machine UID and consumer Project UID plus Claim UID. Retain local/source class
   UIDs, source and authorization policy revisions, the admitted public contract,
   a binding identifier, and epoch outside the consumer project's deletion domain.
   Competing projects and overlapping classes cannot double-allocate a Machine
   or bind one claim to multiple Machines.
5. Starts the admitted activation work for that exact binding, if requested.

A binding is immutable for the MachineClaim's lifetime. If no eligible owned
capacity exists, report `InsufficientCapacity`. Procurement through a dynamic
provider requires separate authorized policy, quotas, and idempotency; it does
not bypass the binder.

Network feasibility before binding is distinct from NetworkAttachment Ready,
which requires actual hardware. After binding, networking reserves resources and
establishes isolated provisioning connectivity. The provider deploys through that
isolation, verifies the maintenance agent has exited, and coordinates the switch
to customer connectivity before normal customer disk boot. See the
[worked handoff](examples/owned-bare-metal/README.md).

### Runtime access and networking

The public handle is MachineClaim. The service authorizes each operation against
its current allocation, capabilities, and principal. Private Machine/Device/BMC
mapping is not a customer-editable reference.

- **Console:** an authenticated operation creates a short-lived serial or
  graphical session scoped to principal/project, Claim UID, and binding epoch.
  Keep bearer capabilities out of generally readable status. Release actively
  disconnects existing streams as well as denying new ones.
- **Power:** reconcile persistent On/Off intent. Reboot/reset is a bounded,
  idempotent operation with binding preconditions. A stale reset cannot affect
  a later consumer.
- **Rescue or media:** explicit supported operations with admitted artifacts,
  bounded lifetime, and conflict checks against installs and cleanup.
- **Network:** a network-domain NetworkAttachment targets the claim and its
  published interface slot. For owned or physically visible infrastructure, the
  network controller resolves actual inventory ports and owns switch configuration
  and IPAM integration. Opaque rented/virtual offerings use authorized provider
  attachment APIs and normalized endpoint observations without inventing physical
  inventory. Both paths retain one network configuration authority. Private DNS
  for the machine is served from the platform node where its attachment lands;
  see [standalone machines](node-services.md#standalone-machines).

Raw BMC administration is a separate privileged offering if supported. Brokered
console/power does not imply access to operator management networks or shared
credentials. Cluster-managed claims have a coordinator as their runtime authority;
operator overrides and authority transfers are explicit, audited maintenance.

After standalone installation handoff, the consumer owns its OS. Ongoing managed
OS reconciliation requires a separate admitted management contract; a Fleet
controller cannot infer that permission from physical ownership.

### Release and replacement

Deleting a completed Installation does not wipe the machine or release its claim.
Deleting a MachineClaim begins the release protocol:

1. Reject new consumer effects and stop or account for in-flight operations.
2. Revoke sessions, media/bootstrap capabilities, and credentials; actively close
   console streams.
3. Detach or fence consumer networking while preserving protected cleanup access.
4. Complete the operator-selected cleaning/restoration policy with per-device
   evidence. Unknown outcomes retain the reservation or quarantine the asset.
5. Finish provider release if policy requires it, or return a retained Machine
   to eligibility once all gates pass.

Cross-consumer assurance includes relevant firmware/BMC state and device access,
not only filesystem or disk erasure. Owned assets are retained independently of
consumer garbage collection. Finalizer force-removal is not a supported shortcut
to declare an uncertain machine available.

Consumer project termination initiates this same release from retained service
records. Cleanup authority, project identity tombstones, reservations, and exact
network/artifact revocation obligations survive the consumer API's removal.
Unreachable project APIs are not evidence of deletion. Quota accounting uses
durable reservations rather than counts of surviving claim objects. Offering
withdrawal prevents new allocations; it does not erase active grants or release
capacity. See the [cross-project lifecycle](capacity-publication.md#changes-withdrawal-and-project-deletion).

A failed server is never substituted inside an existing MachineClaim. Replacement
creates a new claim and installation. For managed clusters, NodeClaim coordinates
drain, predecessor fencing, logical identity transfer, and joining the successor.
Standalone consumers use an explicit replacement workflow; names and IPs do not
silently transfer. Moving or replacing physical inventory has its separate
[DeviceClaim lifecycle](inventory.md).

### Infrastructure providers

The provider contract separates capacity acquisition from effect execution:

- **Adopt/acquire:** Establish concrete provider identity and capabilities; return
  capacity to the shared binder.
- **Inspect/prepare:** Perform bounded, separately authorized work against enrolled
  identity; preserve unknown evidence.
- **Install:** Execute a retained Installation plan with exact asset, binding, artifact,
  configuration, and disk preconditions.
- **Runtime access:** Perform authorized power/console/media operations for the current
  binding.
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

Report independently actionable conditions such as Bound, Prepared, PhysicalReady,
Available, and Ready, tied to evaluated generations and dependency revisions.
Machine status may report operator-only provider billing, physical health, and
binding observations. Public MachineClaim status reports permitted placement,
interface information, power, console capabilities, and handoff progress.

Readiness means the published platform handoff contract is satisfied. Without a
guest-agent contract, GuestReady remains Unknown; successful image writing and
power-on do not prove SSH or application health. Drift does not silently change
the committed binding or authorize eviction.

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
- **Pool-to-hardware selection:** Shared binder and immutable MachineClaim binding.

## Implementation

Fleet's allocation controller owns the durable reservation and runtime admission
service. Its storage and conflict protocol must support controller retries,
restarts, and stale-worker exclusion. Kubernetes status reports outcomes but is
not the only record of irreversible-operation authorization.

The catalog publisher owns consumer MachineClass references, resolved summaries,
and protected provenance. The binder separately validates current policy and
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
against provider-owned supply.
Use standalone and managed lab consumers. Qualify owned-metal preparation/reuse before
customer exposure; adopt rented active machines without reinstalling them. The
[umbrella rollout](README.md#rollout-plan) coordinates these tracks with inventory,
OS configuration, and cluster orchestration.

## Acceptance gates

Prove exclusive binding under concurrent requests, rejected unauthorized
references, unavailable capacity remaining pending, initial-reference creation
order independence, and network feasibility before reservation. Exercise crashes
and timeouts at each hardware effect, concurrent reinstalls, stale power/network
operations, console revocation, failed cleanup, claim name reuse, and hardware
replacement. Confirm that policy edits and deletion of historical installation
requests cannot reinstall or free an active customer's machine.

Also prove automatic catalog registration, trusted publication provenance,
cross-project quota and entitlement enforcement, one winner for a shared Machine,
source-use revocation during commit, and release after consumer project deletion.
