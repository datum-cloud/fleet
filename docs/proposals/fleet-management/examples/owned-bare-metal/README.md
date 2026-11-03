# One owned server, from rack to customer

Part of the [Fleet infrastructure management enhancement](../../README.md). Inventory, allocation, and bare-metal provisioning below are Fleet subsystems; networking retains its own configuration authority.

This walkthrough describes an operator and customer journey through Fleet, using proposed service contracts to show intent and progress. The groups, fields, conditions, custom operations, and namespace scoping are proposed, not implemented CRDs. Provider resources live in project `datum-metal`, namespace `infra-ops`; customer requests live in project `acme`, namespace `acme`. Both provider and consumer MachineClasses are project-scoped within their respective APIs and have no namespace. These are distinct authenticated project API contexts, not just namespace separation. Project UIDs, rather than names supplied by clients, establish allocation and access scope. The files are design examples, not an installable resource bundle.

The operator plans physical installation `DeviceClaim/iad1-r12-u20-server` in rack `iad1-r12` at Facility `iad1`, within geography `us-east-1`. Trusted registration binds actual `Device/server-example-001` to that claim. Its compute representation remains `Machine/iad1-r12-srv01`; resource names do not establish identity. Acme requests a standalone machine named `db-01`, attaches it to its existing private network, installs a qualified Linux image, and receives graphical OOB console access. Hardware, MACs, addresses, and UIDs/digests are illustrative. This example makes no hardware-support certification claim.

This example uses image deployment, direct BMC connection configuration, and a powered-off handoff as one qualified execution path. Other backends can use different mechanisms while satisfying the same identity, isolation, and outcome contracts. Fleet does not require a particular agent or provisioning product.

Existing prerequisites:

- **Project eligibility:** Acme has Fleet service enablement, current bare-metal entitlement, and source-use permission. Service provisioning registers its local class reference; privileged delivery does not establish that permission.

- **Location and Site `us-east-1`:** Geographic offering and operational placement policy
  already exist; Site policy admits the Facility.
- **Facility `iad1`:** Provider-neutral physical facility associated with this location.
- **Device `iad1-r12-leaf-a`:** Existing, managed data switch with physical component
  `ethernet20`.
- **Device `iad1-oob01`:** Existing, managed BMC switch with component `port12`.
- **Devices `iad1-r12-pdu-a` and `iad1-r12-pdu-b`:** Each has `outlet20`; upstream feed
  mappings are verified. Different names alone do not establish redundant power.
- **Operator Secrets:** `srv01-bmc`, `bmc-ca`, `leaf-a-management`, and
  `network-management-ca` have been securely populated in operator scope. Endpoint
  resolution/management addressing already exists.
- **Customer Network `acme-private`:** Existing authorized network reachable in this
  facility, with IPAM and customer access configured; example subnet is `10.42.0.0/24`.
- **BareMetalMachineClass `qualified-server-v1`:** Private hardware compatibility,
  preparation, boot transport, and deterministic disk selection. No class configuration is
  expanded in this example.
- **Operator cleaning/restoration policy `tenant-reuse-v1`:** Required device
  sanitization, applicable credential revocation, and hardware/firmware baseline assurance
  before reuse. It is selected by the asset owner; consumers cannot weaken it. Its
  resolved contents are pinned in each preparation/reclamation journal.
- **MachineImage `linux-lts-v1`:** Published immutable, qualified Linux whole-disk
  artifact and supported bootstrap contract; the selected OS adapter understands its
  cloud-init input.
- **Management/provisioning services:** An independently operable provisioning execution
  layer behind the Fleet adapter, protected artifact delivery, operator network
  controller, console gateway, and the connectivity/support services required by the
  selected method.

1. **The operator plans placement and cabling before registration.** The Rack, DeviceClaim, and four Cable records in [operator.yaml](operator.yaml) declare the physical installation. Accepting DeviceClaim reserves its rack footprint and required terminations, preventing conflicting installation plans. DeviceClaim owns expected identity, placement, declared port roles, and references to required cables. Cable owns expected topology; a planned endpoint references DeviceClaim plus port ID before a Device exists. A technician installs the machine and scans the asset, rack/U location, and cable labels. A YAML Cable is not proof of an installed wire. The direct paths here avoid patch panels only for brevity.

   Trusted registration must match an existing accepted DeviceClaim and authenticate the enrollment channel before creating a managed Device. It commits an exclusive claim-to-Device UID binding; multiple matching devices cannot all consume the same plan. Unknown or unauthorized hardware remains isolated discovery evidence for operators and cannot become capacity. Registration may precede full placement/cabling verification so discrepancies can be diagnosed. [registration.yaml](registration.yaml) shows the resulting Device and verification outputs. Cable endpoint resolution depends on the binding, not Device Ready, avoiding a circular readiness dependency. Claim ports map to actual components using trusted chassis labels or qualified slot/port layout, independently of observed neighbors; a controller cannot remap ports merely to make incorrect wiring pass. Labels in this example are illustrative.

2. **The operator enrolls management access without making the server allocatable.** After Device registration, the Machine references that actual Device and its BareMetalMachine extension. Allocation begins Disabled; management begins ObserveOnly. Device owns actual physical identity and observations; DeviceClaim/Cable own approved installation expectations. NetworkDevice owns switch-management configuration. BareMetalMachine owns BMC/console configuration. The operator network controller supplies restricted commissioning/BMC connectivity and leaves customer connectivity unavailable. Initial registration/inspection uses a claim-scoped trusted enrollment path; any boot-based inspection has its own bounded authorization. Serial/MAC matching alone is not authentication.

3. **Datum defines capacity and publishes a reference into Acme.** [provider-class.yaml](provider-class.yaml) defines the provider MachineClass: public capabilities, the `general-metal` supply, and the private hardware class. Separately, [publication.yaml](publication.yaml) shows service provisioning registering [catalog.yaml](catalog.yaml) in Acme. Its spec contains only a source reference; status reports authorized public capabilities. The reference cannot override the definition. The resolver retains both project/class UID pairs and the evaluated source revision; copying the class creates neither permission nor capacity. Publication can precede available stock, allowing a customer to submit a pending claim. Other eligible projects receive their own references to the same source definition and shared supply.

4. **Acme submits its desired resources.** [customer.yaml](customer.yaml) declares a MachineClaim, NetworkAttachment, immutable Installation request, and bootstrap Secret. Customer references never contain a BMC, switch port, VLAN, rack, or physical Machine name. The claim may exist before the server is installed; it stays pending until capacity and all named requirements can be fulfilled. The bootstrap public-key placeholder must be replaced in any future real implementation.

5. **The operator verifies and explicitly prepares the asset.** DeviceClaim declares required identity, rack-placement, network-path, and power-path checks constrained by operator policy. Its verification compares the approved plan with trusted hardware, placement, cable/power-path evidence and current dependency revisions. Switch observations can corroborate peer ports; exact rack U, passive cable identity, and power-outlet mappings need scans or suitable instrumentation. Missing evidence is Unknown, and mismatches preserve both expected and observed data while blocking new allocation. Change provider mode to Managed, preview the exact preparation plan, and authorize `/prepare` in [requests.yaml](requests.yaml). Its retained plan includes concrete disks, resolved Machine.cleaningPolicyRef, and permitted effects. Preconditions require allocation disabled, no consumer MachineClaim/Installation, and no competing operation. A bound physical DeviceClaim is required and is not a consumer reservation. Managed or DeviceClaim Ready alone never authorizes wiping. After successful preparation evidence, enable Machine allocation. Availability still requires current physical verification. The pre-eligibility status is in [observations.yaml](observations.yaml).

6. **The shared binder commits the allocation.** It resolves the local class and its canonical source through protected identity records, checks current source-use permission, consumer entitlement and quota, and resolves the named initial Installation and attachments. It checks image/network permissions and current placement feasibility, then selects a compatible eligible Machine in the `datum-metal` provider project. It retains both Project UIDs, the claim, local/source class and Machine UIDs, the source revision and admitted public contract, input digests, capabilities, and the quota hold, then commits exactly one reservation, here `alloc-7ab1`, epoch 1. Claims from two projects cannot both win, and one claim cannot bind twice. This private reservation survives deletion of Acme's project API objects. The customer sees an allocation handle, not operator credentials or the private physical binding. Claim name reuse cannot transfer prior authority.

7. **Networking prepares the target while the provider installs through isolation.** Networking reserves the tenant address and concrete NIC/port binding, reporting Prepared for this exact allocation. It configures the target data port on a protected provisioning network for the authorized operation. Prepared is distinct from customer Attached/Ready. The provider submits the retained plan to the configured execution layer. For this example, the backend uses an authenticated temporary environment to deploy the exact OS-provider image and supported bootstrap payload. The backend may implement that work through its native workflow or operation interfaces. Network configuration is materialized from the authorized attachment, not invented by the hardware provider. Exact disk targets, inputs, and operation authority are retained before effects.

8. **The controllers perform an explicit handoff.** After disk-write verification, remove temporary boot media and confirm the maintenance agent has exited; in this example, the server is powered off. The network controller changes the physical port to the authorized tenant network and confirms Ready for the same claim UID/binding epoch. The provider then powers on for local disk boot, honoring the claim's On intent. Customer traffic must not reach the privileged deployment agent. Installation, network, and power operations are serialized; an independent power reconciler cannot race this handoff.

9. **Acme receives the usable allocation and OOB access.** [observations.yaml](observations.yaml) shows the public claim, installation, and network results. Platform Ready means the agreed hardware/network/image handoff completed, not that a customer application or SSH service is healthy. This self-managed example has no guest readiness agent, so GuestReady is Unknown. Acme uses its private-network access to reach `10.42.0.23` and its public-key credentials; graphical console works over the platform gateway even if the guest networking fails. The console operation envelope is in [requests.yaml](requests.yaml). A short-lived response is issued only to the authorized caller, never stored as a bearer URL in generally readable claim status.

The initial network set is explicit so object-creation order cannot race placement. `initialInstallationRef` has immutable, one-time activation semantics. The initial gate is durably completed; deleting an obsolete Installation later does not turn it back on. Reinstall uses a new Installation with an expected predecessor, while physical replacement uses a new claim. Guest OS administration belongs to Acme after this self-managed handoff. Internal Fleet controllers use the same allocation APIs and add their own managed-node/cluster lifecycle above them.

DeviceClaim is an operator physical-installation claim; MachineClaim is a consumer compute reservation. They have independent lifetimes. A DeviceClaim binds one Device UID for one approved installation incarnation. A move uses a successor DeviceClaim for the same Device with coordinated maintenance; a whole-server replacement creates a new Device and Machine as well. Deleting a DeviceClaim never deletes hardware or releases a consumer MachineClaim. Retain fulfilled claims while Cable references depend on them, or transfer references explicitly under the placement-change protocol. Ongoing drift blocks new allocation or relevant operations and alerts operators; it is not permission to wipe or evict an already allocated customer server.

On eventual release, access is revoked, console streams are disconnected, customer networking is detached/fenced, and required cleaning/restoration completes before this Machine is available to another customer. Inventory and owned hardware remain in the provider project. Removing the catalog entry does not release the allocation. If the customer project terminates, retained service-owned records drive cleanup before reuse; loss of project API connectivity alone does not initiate release.

Files:

- [provider-class.yaml](provider-class.yaml): capacity definition and private supply selection in the provider project.
- [publication.yaml](publication.yaml): separate service provisioning excerpt that registers the consumer reference.
- [catalog.yaml](catalog.yaml): automatically registered source reference and resolved public summary in the consumer project.

- [operator.yaml](operator.yaml): DeviceClaim/Cable installation intent and subsequent Machine/provider enrollment.
- [registration.yaml](registration.yaml): controller-created Device and physical-verification snapshots.
- [customer.yaml](customer.yaml): customer allocation, network, installation, and bootstrap intent.
- [requests.yaml](requests.yaml): explicitly labeled API operation request envelopes.
- [observations.yaml](observations.yaml): controller-written status snapshots at different stages.

The [provisioning execution contract](../../bare-metal-provisioning.md#execution-layer-contract) defines the common capability, operation, evidence, and recovery requirements. The backend's workflow and asset identifiers remain private to its adapter.
