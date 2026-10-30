# Complete API examples

These proposed responses accompany the [machine lifecycle enhancement](README.md#api-design).
They show different lifecycle points, not one simultaneous observation. Names,
identifiers, and timestamps are synthetic. They define no implemented APIs or
production timing requirements. The enhancement owns field semantics and lifecycle rules.

- [Provider MachineClass](#provider-machineclass)
- [Consumer MachineClass](#consumer-machineclass)
- [MachineProfile](#machineprofile)
- [Machine](#machine)
- [MachineClaim](#machineclaim)
- [Installation](#installation)
- [Reinstallation predecessor](#reinstallation-predecessor)

## Provider MachineClass

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineClass
# Classes span the provider project so namespaces can share an offering.
metadata:
  generation: 1
  name: standard-metal
# The definition owns offering policy; profiles own hardware requirements.
# Its mode cannot change after creation.
spec:
  # Named profiles select provider-local contracts by name.
  # Fleet retains their resolved identities internally.
  # Custom profiles default to disabled and require provider validation.
  profilePolicy:
    allowedProfiles:
      - name: accelerated-16c-64g
    customProfiles:
      enabled: false

  # Access describes supported operations, not permission to perform them.
  # The first implementation supports power control but no console sessions.
  access:
    consoleModes: []
    powerControl: true

  # Capacity limits selection to existing supply in one provider namespace.
  # Labels identify candidates; they confer no eligibility or purchase authority.
  capacity:
    machines:
      namespace: infra-ops
      selector:
        matchLabels:
          fleet.datumapis.com/capacity-pool: general-metal

    # This provider-local contract adds backend compatibility requirements.
    # Retained identity and revision keep later edits from changing accepted work.
    providerClassRef:
      apiGroup: baremetal.fleet.datumapis.com
      kind: BareMetalMachineClass
      namespace: infra-ops
      name: qualified-server-v1

# The projector reports the definition that the class evaluator accepted.
# Acceptance validates the offering; it does not reserve or promise capacity.
status:
  evaluatedRevision: class-revision-example
  conditions:
    - type: Accepted
      status: "True"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: DefinitionValid
      message: "Profile policy and provider contract resolved."
```

## Consumer MachineClass

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineClass
# Project-wide references give every namespace a local name for an offering.
metadata:
  generation: 1
  name: standard
# The immutable source keeps the provider definition authoritative.
# Fleet resolves the provider project and definition internally; a reference
# cannot become a definition or override its policy.
spec:
  source:
    project: provider
    name: standard-metal

# The projector exposes only the authorized public offering. Private source
# mappings, selectors, provider configuration, and other consumers stay private.
status:
  # The revision identifies the evaluated offering independently of this
  # reference object's generation. It does not grant source-use permission.
  evaluatedRevision: class-revision-example
  resolvedClass:
    # Publication maps allowed provider profiles to protected local copies.
    # These local names do not expose provider inventory or grant use.
    profilePolicy:
      allowedProfiles:
        - name: accelerated-16c-64g
      customProfiles:
        enabled: false
    access:
      consoleModes: []
      powerControl: true
  conditions:
    - type: SourceResolved
      status: "True"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: AuthorizedSource
      message: "The source offering resolved under current authorization."
```

## MachineProfile

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineProfile
metadata:
  name: accelerated-16c-64g
  generation: 1
# Immutable requirements can serve several offerings without coupling hardware
# choices to capacity ownership, price, location, or provisioning credentials.
spec:
  # Cores count physical cores across sockets. Memory describes installed RAM,
  # not memory left available after the OS starts.
  cpu:
    architecture: amd64
    cores:
      min: 16
  memory:
    capacity:
      min: "64Gi"

  # GPU memory applies to each matching device, not their combined memory.
  gpus:
    count:
      min: 1
    vendor: Example
    models:
      - example-gpu
    memoryPerDevice:
      min: "24Gi"

  # Each group requires distinct devices. Capacity is per disk; the group name
  # describes a hardware role without prescribing RAID or filesystem layout.
  disks:
    - name: boot
      count:
        min: 2
      capacity:
        min: "480Gi"
      media: SSD
      interface: NVMe

  # Interfaces describe physical ports. Network attachments and IP addresses
  # remain separate inputs owned by the networking APIs.
  interfaces:
    - name: primary
      type: Ethernet
      speed:
        min: "10G"
status:
  conditions:
    - type: Accepted
      status: "True"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: ProfileValid
      message: "Hardware requirements are well formed."
```

## Machine

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Machine
# Machine identity is independent of consumer allocations. Replacement hardware
# gets a new resource lifetime; labels only group capacity for selection.
metadata:
  generation: 1
  name: lab-server-01
  namespace: infra-ops
  labels:
    fleet.datumapis.com/capacity-pool: general-metal
spec:
  # This required reference keeps provider details in the provider namespace.
  # Enrollment fixes the provider and asset identities to prevent retargeting.
  providerRef:
    apiGroup: baremetal.fleet.datumapis.com
    kind: BareMetalMachine
    name: lab-server-01

  # Qualified owned hardware requires a link to its physical inventory identity.
  # Supply without a physical Device needs no invented inventory record.
  deviceRef:
    name: server-example-001

  # Disabled is the default so registration cannot expose occupied capacity.
  # Eligible still depends on preparation, health, readiness, and placement.
  allocationPolicy: Disabled

  # Each reservation pins this required cleanup policy, preserving its
  # release obligations even if the operator later weakens the policy.
  cleaningPolicyRef:
    name: tenant-reuse-v1
  # Retain separates consumer release from asset disposal; it is the only
  # supported value in the first implementation.
  reclaimPolicy: Retain

# The projector derives these observations from executor evidence and private
# reservations. Operators cannot make capacity available by editing status.
status:
  # Fleet normalizes discovery observations and checks their evidence internally.
  # Logical device names keep serial numbers and management IDs private.
  hardware:
    cpu:
      architecture: amd64
      model: example-cpu
      sockets: 2
      cores: 24
      threads: 48
    memory:
      capacity: "128Gi"
    gpus:
      - name: accelerator-0
        vendor: Example
        model: example-gpu
        memory: "32Gi"
    disks:
      - name: disk-0
        capacity: "960Gi"
        media: SSD
        interface: NVMe
      - name: disk-1
        capacity: "960Gi"
        media: SSD
        interface: NVMe
    interfaces:
      - name: primary
        type: Ethernet
        speed: "25G"
  conditions:
    - type: Prepared
      status: "True"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: PreparationVerified
      message: "The simulator verified preparation for this asset."
    - type: Healthy
      status: "True"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: HealthVerified
      message: "The simulator reported healthy capacity."
    - type: Available
      status: "False"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: AllocationDisabled
      message: "Operator policy excludes this Machine from new allocations."
```

## MachineClaim

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineClaim
# Claim identity belongs to the consumer project, separately from the Machine.
# A new name creates a distinct request rather than retrying an existing one.
metadata:
  generation: 1
  name: example
  namespace: default
# Only powerState can change after creation, preserving the placement contract.
# The local class, profile, and permitted location are required. Fleet retains
# their identities and exact contracts for allocation and recovery.
spec:
  machineClassRef:
    name: standard
  machineProfileRef:
    name: accelerated-16c-64g
  locationRef:
    name: lab

  # This optional gate delays activation until the initial installation succeeds.
  # A reciprocal claim reference in the same project and namespace ties the
  # Installation to this allocation.
  initialInstallationRef:
    name: example-os-1

  # Immutable entries connect profile slots to project Networks. Omission
  # requests no attachment; a Network namespace defaults to the claim namespace.
  networkInterfaces:
    - name: primary
      network:
        namespace: default
        name: production
      # IPv6 is the default; the first family is primary. Every requested family
      # must be supported. The current Network API admits new networks as IPv6-only.
      ipFamilies:
        - IPv6
      # Extra addresses select NSO classes; every requested class must be satisfied.
      # Omission requests none. Delete is the default and requires verified release.
      addresses: []
      reclaimPolicy: Delete
  # Off is the default so creating a claim does not request runtime power-on.
  # On expresses intent; authorization and activation gates still control effects.
  powerState: "On"

# The projector reports allocation progress without exposing the provider
# Machine identity. These fields describe the binding; they cannot authorize it.
status:
  # Admission retains the selected local profile through source deletion.
  acceptedProfileRef:
    name: accelerated-16c-64g
  # Binding captures this hardware snapshot and its observation time.
  # Later observations can change readiness without rewriting the snapshot.
  allocatedHardware:
    observedAt: "2026-10-27T12:00:00Z"
    hardware:
      cpu:
        architecture: amd64
        model: example-cpu
        sockets: 2
        cores: 24
        threads: 48
      memory:
        capacity: "128Gi"
      gpus:
        - name: accelerator-0
          vendor: Example
          model: example-gpu
          memory: "32Gi"
      disks:
        - name: disk-0
          capacity: "960Gi"
          media: SSD
          interface: NVMe
        - name: disk-1
          capacity: "960Gi"
          media: SSD
          interface: NVMe
      interfaces:
        - name: primary
          type: Ethernet
          speed: "25G"
  # This name identifies the successful request for display, not replacement authority.
  currentInstallationRef:
    name: example-os-1
  # The opaque revision guards replacement of the current installation state.
  # It belongs to this allocation and remains independent of request names.
  installationRevision: installation-revision-example-1
  placement:
    locationRef:
      name: lab
  # Networking supplies the assignment view and interface conditions.
  # The revision identifies exact configuration inputs; it does not prove guest setup.
  network:
    revision: network-revision-example
    interfaces:
      - name: primary
        # Status shows the accepted Network by name. Fleet pins its lifetime
        # internally; name reuse cannot redirect this allocation.
        network:
          apiGroup: networking.datumapis.com
          kind: Network
          namespace: default
          name: production
        macAddress: "02:00:00:00:00:10"
        mtu: 1440
        # Addresses retain networking's CIDR and primary-address semantics.
        # For external addresses, [] means verified none; absence means unknown.
        addresses:
          - family: IPv6
            address: "2001:db8::10/64"
            gateway: "2001:db8::1"
            primary: true
        externalAddresses: []
        routes:
          - destination: "::/0"
            gateway: "2001:db8::1"
        dns:
          servers:
            - "2001:db8::53"
          searchDomains: []
        # NSO separates address allocation, preparation, and programming.
        # The claim aggregates these facts without asserting guest health.
        conditions:
          - type: Allocated
            status: "True"
            observedGeneration: 1
            lastTransitionTime: "2026-10-27T12:00:00Z"
            reason: AddressesAllocated
            message: "NSO holds every requested address family and class."
          - type: Prepared
            status: "True"
            observedGeneration: 1
            lastTransitionTime: "2026-10-27T12:00:00Z"
            reason: ConfigurationPrepared
            message: "NSO supplied the configuration needed for installation."
          - type: Programmed
            status: "True"
            observedGeneration: 1
            lastTransitionTime: "2026-10-27T12:00:00Z"
            reason: AttachmentVerified
            message: "Networking verified this interface on the selected network."
  # Observed power is On, Off, or Unknown, independently of spec.powerState intent.
  # Fleet reports Unknown when it cannot verify the state from current evidence.
  power:
    state: "On"
  conditions:
    - type: Accepted
      status: "True"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: InputsAccepted
      message: "Supported class/profile inputs and request identity are retained."
    - type: Bound
      status: "True"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: ReservationCommitted
      message: "The private reservation holds exclusive capacity."
    - type: Ready
      status: "True"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: PlatformHandoffComplete
      message: "Initial installation and required handoff checks passed."
    - type: NetworkReady
      status: "True"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: AttachmentsVerified
      message: "Networking verified the required assignments and attachments."
    - type: Releasing
      status: "False"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: AllocationActive
      message: "The allocation has no release request."
```

## Installation

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Installation
# Installation shares its claim's project and namespace. The claim's
# initialInstallationRef identifies the request that satisfies its initial gate.
metadata:
  generation: 1
  name: example-os-1
  namespace: default
# The entire spec is immutable so recovery uses the same authorized inputs.
# Claim and approved image are required. Fleet checks install and image-use
# permissions and internally pins the claim lifetime, image artifacts, and digests.
spec:
  claimRef:
    name: example
  # This required precondition prevents competing installs from replacing
  # an unexpected OS. None identifies a blank allocation with no admitted install.
  expectedPredecessor:
    type: None
  imageRef:
    name: qualified-linux

  # Optional bootstrap data comes from a Secret in this namespace under
  # explicit caller authority. Protected copies of its identity, digest, and bytes
  # preserve the accepted input through Secret changes or deletion.
  bootstrapConfigRef:
    name: example-bootstrap

# The projector reports the retained installation outcome, not guest health.
status:
  # This is the historical result revision. The claim reports the current one.
  installationRevision: installation-revision-example-1
  completedAt: "2026-10-27T12:00:00Z"
  conditions:
    - type: Accepted
      status: "True"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: OperationAdmitted
      message: "The immutable plan and operation authority are retained."
    - type: Executing
      status: "False"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: OperationComplete
      message: "The executor has no remaining work for this installation."
    - type: Ready
      status: "True"
      observedGeneration: 1
      lastTransitionTime: "2026-10-27T12:00:00Z"
      reason: PlatformHandoffComplete
      message: "The installation and platform handoff completed with verified evidence."
```

## Reinstallation predecessor

This fragment belongs to a new Installation named `example-os-2`, in the same
project and namespace as the claim. Its `spec.claimRef.name` remains `example`;
its image and bootstrap references describe the new installation inputs. The
claim's `initialInstallationRef` stays `example-os-1`. The revision comes from
`MachineClaim.status.installationRevision`. Fleet consumes it when admitting the
replacement; it does not modify the earlier Installation. See the
[revision contract](README.md#installation) for conflicts, retries, and recovery.

```yaml
# The claim's current installation revision guards this replacement.
# It cannot authorize work on another allocation or an older installation state.
expectedPredecessor:
  type: Revision
  revision: installation-revision-example-1
```
