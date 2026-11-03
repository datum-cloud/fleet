<!-- omit from toc -->
# Example: us-east-1 and us-east-1-alice

This example applies the [Fleet subsystem model](../README.md) to the rented
infrastructure and managed clusters in the original proposal. For a complete
owned-server installation and customer claim, see the
[rack-to-customer journey](owned-bare-metal/README.md).

For this internal adoption example, provider inventory and managed consumption
share the `datum-cloud` project; namespaced resources use `infra-ops`. Site and
Facility references resolve within that same authorized project. The owned-server
journey separately demonstrates a provider and consumer in distinct projects.
MachineClass remains project-scoped and platform-published in both cases.

The operational values are a snapshot from the original enhancement, not a fresh
assertion about production. Object names, schemas, and evidence handles are
proposed. The [adoption input ledger](us-east-1-adoption-inputs.yaml) preserves all
eight original machine records' UUIDs, disk identifiers, uplink addresses, vendor
peers, credential references, and retirement requests. It is migration input,
not an apply bundle or proof that those facts are correct today.

## Starting point

One geographic location serves two managed clusters and rented capacity from
multiple providers:

| Property | us-east-1-alice | us-east-1-staging-lab |
|---|---|---|
| Location / zone | us-east-1 / us-east-1a | Same |
| Provider facility codes | IAD3, WAS4, retiring WAS1 | IAD3, WAS4 |
| Channel / environment | stable / production | staging / staging |
| Service public identity | 67.14.172.29, 2607:ed40:40::1d | 67.14.174.3, 2607:ed40:60::3 |
| Fabric site code | 1007 | 1010 |
| Edge-service prefixes | 67.14.164.0/24, 2607:ed40:10::/44 | 67.14.174.0/24, 2607:ed40:60::/44 |
| Talos / Kubernetes | v1.13.7 / v1.35.5 | Same |
| ASN | 33438 | Same |

A servers.com box with hardware UUID ending `c3c04f474834` has moved from the
lab's workload to production. That changes its compute allocation, not the
physical asset identity. Service addresses and Galactic's fabric policy remain
with their respective owners.

## Geography, inventory, and Site policy

Location `us-east-1` is an existing geographic identity. Fleet inventory records
actual premises as provider-neutral Facilities. The names below are provisional
operator aliases for the original provider codes; an operator must verify the
canonical premises and merge aliases if multiple providers use the same facility.
A provider code alone does not establish a separate physical failure domain.

Facility labels express eligibility for a Site's operational allocation policy.
The Site selector selects Facility resources; it does not select raw provider
strings or create new physical premises. No existing allocation moves when a
label or selector changes.

```yaml
apiVersion: inventory.fleet.datumapis.com/v1alpha1
kind: Facility
metadata:
  name: iad3
  namespace: infra-ops
  labels:
    fleet.datumapis.com/allocation-group: us-east-1
spec:
  locationRef:
    name: us-east-1
---
apiVersion: inventory.fleet.datumapis.com/v1alpha1
kind: Facility
metadata:
  name: was4
  namespace: infra-ops
  labels:
    fleet.datumapis.com/allocation-group: us-east-1
spec:
  locationRef:
    name: us-east-1
---
apiVersion: inventory.fleet.datumapis.com/v1alpha1
kind: Facility
metadata:
  name: was1
  namespace: infra-ops
  labels:
    fleet.datumapis.com/allocation-group: us-east-1
spec:
  locationRef:
    name: us-east-1
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: Site
metadata:
  name: us-east-1
  namespace: infra-ops
spec:
  locationRef:
    name: us-east-1
  allocation:
    facilitySelector:
      matchLabels:
        fleet.datumapis.com/allocation-group: us-east-1
```
Vendor mappings live with the provider adapter. For example, NetActuate region
`245` maps to Facility `iad3`; legacy ISO identifier `5892` is an adoption input,
not the desired image for every future allocation. New installations resolve an
approved image per request. The legacy `HOSTING-AS7979` and `HOSTING-AS36236`
settings in the input ledger move to the routing/networking authority.

## Shared catalog

Classes stay brief in this example; they describe capabilities or reusable
requirements, not particular installations:

- **MachineClass netactuate-controlplane / netactuate-worker:** Published provider
  capacity for the corresponding managed pools.
- **MachineClass serverscom-compute-2u / serverscom-edge:** Published rented-server
  capabilities.
- **Typed vendor machine classes:** Provider plan and qualified acquisition/preparation
  requirements.
- **MachineImage talos-1.13.7 / TalosImage 1-13-7:** OS release and its provider's
  artifact resolution.
- **InterfaceClass fabric-loopback / srv6-locator:** Managed interface requirements and
  IPAM allocation behavior.
- **BareMetalMachineClass:** Qualified owned-server preparation/boot/disk requirements,
  used in the separate owned-server journey.

Operator MachineClass definitions reference the private vendor classes; Fleet
registers local source references for managed consumers. This example places both
in `datum-cloud`: the consumer alias `serverscom-compute-2u` references the distinct
definition `datum-serverscom-compute-2u`, so it is not a self-reference. That
definition supplies the `amd64`, `compute.unikraft`, and `compute.kata` contract.
The [publication contract](../capacity-publication.md) also applies to these
internal consumers. These entries, the Omni provider connection, admitted
cluster configuration, network attachments, IPAM pools, and operator credentials
are prerequisites. No customer or node claim supplies raw switch or BMC
credentials. Two representative local catalog entries are:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineClass
metadata:
  name: serverscom-compute-2u
spec:
  source:
    project: datum-cloud
    name: datum-serverscom-compute-2u
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineImage
metadata:
  name: talos-1.13.7
spec:
  family: talos
  version: v1.13.7
  providerRef:
    apiGroup: talos.fleet.datumapis.com
    kind: TalosImage
    name: 1-13-7
```
## Cluster and capacity intent

The production and staging clusters share a Site policy. Each has its own
channel, environment, version, provider configuration, and pools. The example
uses namespace `infra-ops` in the shared `datum-cloud` project. MachineClass,
Site, Facility, and inventory references use their declared scopes within that
project. The publisher and binder enforce the same publication and allocation
contracts used for cross-project consumption. The same namespace name in
another project is a different scope.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Cluster
metadata:
  name: us-east-1-alice
  labels:
    fleet.datumapis.com/wave: stable
  namespace: infra-ops
spec:
  siteRef:
    name: us-east-1
  clusterProviderRef:
    apiGroup: omni.fleet.datumapis.com
    kind: OmniInstance
    name: production
  channel: stable
  environment: production
  version: v1.35.5
  zones:
    - us-east-1a
  clusterProviderConfigRef:
    apiGroup: omni.fleet.datumapis.com
    kind: OmniClusterProfile
    name: cell
  updatePolicy:
    admission: Staged
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: Cluster
metadata:
  name: us-east-1-staging-lab
  labels:
    fleet.datumapis.com/wave: lab
  namespace: infra-ops
spec:
  siteRef:
    name: us-east-1
  clusterProviderRef:
    apiGroup: omni.fleet.datumapis.com
    kind: OmniInstance
    name: production
  channel: staging
  environment: staging
  version: v1.35.5
  zones:
    - us-east-1a
  clusterProviderConfigRef:
    apiGroup: omni.fleet.datumapis.com
    kind: OmniClusterProfile
    name: cell
  updatePolicy:
    admission: Staged
```
Runtime/networking features are declared through their owning service APIs targeting
the Cluster. The cluster provider connection and its configuration profile are
separate references; credential rotation cannot initiate a configuration rollout.

The production pools ask for three control-plane machines, two workers, one DNS
machine, two edge machines, and one compute machine. Each is an independent
capacity requirement. A representative pool is:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachinePool
metadata:
  name: us-east-1-alice-compute
  namespace: infra-ops
spec:
  clusterRef:
    name: us-east-1-alice
  role: compute
  machineClassRef:
    name: serverscom-compute-2u
  imageRef:
    name: talos-1.13.7
  replicas: 1
  labels:
    compute.datumapis.com/runtime: unikraft
    node-role.datumapis.com/compute: ''
    galactic.datumapis.com/fabric: router
    galactic.datumapis.com/node: compute
    galactic.datumapis.com/galactic: router
  taints:
    - key: compute.datumapis.com/runtime
      value: unikraft
      effect: NoSchedule
  updatePolicy:
    maxConcurrentUpdates: 1
    maxUnavailable: 1
    maxSurge: 1
  scaleUpPolicy:
    recipe: AdmittedPoolRevision
    whenRolloutPausedOrCancelled: Block
    missingAdmission: Block
```
Direct bare-metal customers create MachineClaims without a MachinePool or
NodeClaim. Managed pool controllers use that same allocation service and add
logical node identity and cluster membership above it.

## Adopt a rented physical server

Adoption starts by recording the expected existing installation. For this rented
server the source material supplies a UUID and provider facility code, but no
trusted rack, cable, or outlet evidence. Do not fabricate that topology. Match the
verified canonical Facility, authorize the registration channel, and obtain the
evidence required for the offered placement and connectivity guarantees.

```yaml
apiVersion: inventory.fleet.datumapis.com/v1alpha1
kind: DeviceClaim
metadata:
  name: was4-c3c04f474834-adoption
  namespace: infra-ops
spec:
  role: Server
  expectedIdentity:
    hardwareUUID: 4c4c4544-0031-4610-8042-c3c04f474834
  placement:
    facilityRef:
      name: was4
  verification:
    requiredChecks:
      - Identity
      - FacilityPlacement
```
This claim has no rack/cabling assertions. Admission must reject it for an offering
that requires those assertions; the absence of a requirement is not evidence that
the property was verified. Rack/path observations remain Unknown until proven.
An owned-rack installation instead declares the placement, ports, and Cable set
shown in the [owned-server plan](owned-bare-metal/operator.yaml).

After trusted registration creates the actual Device, the Machine references it.
The following provider extension shape is illustrative. Its provider ID and BMC
endpoint must be resolved and verified before use; they are not supplied by the
historical record. Observation/adoption must preserve the active workload.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Machine
metadata:
  name: was4-c3c04f474834
  namespace: infra-ops
spec:
  deviceRef:
    name: server-c3c04f474834
  providerRef:
    apiGroup: serverscom.fleet.datumapis.com
    kind: ServersComMachine
    name: was4-c3c04f474834
  allocationPolicy: Disabled
  cleaningPolicyRef:
    name: rented-server-reuse-v1
  reclaimPolicy: Retain
---
apiVersion: serverscom.fleet.datumapis.com/v1alpha1
kind: ServersComMachine
metadata:
  name: was4-c3c04f474834
  namespace: infra-ops
spec:
  providerID: <verified servers.com host id>
  managementMode: ObserveOnly
  bmc:
    endpoint: <verified BMC endpoint>
    credentialsSecretRef:
      name: was4-c3c04f474834-bmc
```
The inventory subsystem owns the registered UUID and observed disk/NIC identities.
The provider resolves exact disk targets for admitted work. Networking owns bonds,
uplink addressing, and routing; the original `enP1s1f1`/`enP1s4f1` names are
migration observations, not durable physical connector identities. The
`170.23.227.193/31` and `2600:9c07:0:43::2/64` inputs must be reconciled with their
network/IPAM authorities before they are admitted.

Reconcile the existing consumer's binding before enabling any future allocation.
A running machine must never briefly appear free during migration. Adoption does
not execute an initial installation or cleaning plan. A provider-created
NetActuate VM follows the same allocation contract, with provider-observed
placement and no invented physical Device or rack record.

## Managed node, claim, and installation

The controller examples below show a future admitted activation using this model,
not instructions to reinstall the running production host. A real adoption
records the current incarnation and preserves it. Managed configuration recipes
are retained immutable inputs; their storage contract is specified by the
[OS subsystem](../os-configuration.md), without requiring a new public recipe kind.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: NodeClaim
metadata:
  name: us-east-1-alice-compute-0
  namespace: infra-ops
spec:
  poolRef:
    name: us-east-1-alice-compute
  role: compute
  desiredState: Running
  identityScopeRef:
    name: alice-compute-0-addresses
  configurationAdmission:
    recipeRef:
      name: alice-compute-0-config-01
      digest: sha256:<retained approved recipe digest>
    epoch: 1
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineClaim
metadata:
  name: mc-4k2p9
  namespace: infra-ops
spec:
  siteRef:
    name: us-east-1
  machineClassRef:
    name: serverscom-compute-2u
  powerState: 'On'
  networkAttachmentRefs:
    - name: alice-compute-0-uplink
  initialInstallationRef:
    name: alice-compute-0-os-01
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: Installation
metadata:
  name: alice-compute-0-os-01
  namespace: infra-ops
  labels:
    fleet.datumapis.com/role: compute
    fleet.datumapis.com/cluster: us-east-1-alice
    fleet.datumapis.com/site: us-east-1
    fleet.datumapis.com/profile: cell
    fleet.datumapis.com/environment: production
spec:
  claimRef:
    name: mc-4k2p9
  expectedPredecessor:
    type: None
  imageRef:
    name: talos-1.13.7
  bootstrapConfigRef:
    name: alice-compute-0-bootstrap-01
```
The controller creates the initial resources before binding. Networking checks
placement feasibility, then resolves the exact physical attachment after exclusive
allocation. Normal customer/managed-node boot waits for the protected provisioning
handoff. The machine binding cannot change inside `mc-4k2p9`.

A reinstall requests a new Installation with the current incarnation as its
expected predecessor. Hardware replacement creates a new MachineClaim and
Installation under the same NodeClaim. The managed coordinator fences the old
incarnation before activating the node name and stable IPAM identity on the new
one. Deleting an Installation alone does not release capacity or erase disks.

After successful activation, an abbreviated controller observation is:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: NodeClaim
metadata:
  name: us-east-1-alice-compute-0
  namespace: infra-ops
status:
  claimRef:
    name: mc-4k2p9
  installationRef:
    name: alice-compute-0-os-01
  node:
    name: us-east-1-alice-compute-0
    ready: true
  interfaces:
    - name: fabric0
      addresses:
        - 2607:ed40:10d::1/128
    - name: srv6
      addresses:
        - 2607:ed40:8004:1::/64
```
The private allocation record pins the exact Machine and Claim UIDs and binding
epoch. Status examples omit timestamps and full UID/digest bookkeeping; production
execution may not omit those preconditions. No customer-visible status contains
BMC credentials or a reusable console bearer URL.

## Managed OS contributions

Galactic and runtimes contribute to one renderer. The following input policies are
from the original example. Rendering a new result does not authorize its rollout:
the coordinator admits an exact recipe revision to each managed node, and one
selected OS executor applies it. Standalone customer operating systems do not
acquire continuous Fleet configuration management by matching these labels.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: OSConfigPolicy
metadata:
  name: routing-base
  labels:
    contributor: fleet
  namespace: infra-ops
spec:
  selector:
    matchLabels:
      fleet.datumapis.com/profile: cell
  template:
    sysctls:
      net.ipv4.conf.all.forwarding: '1'
      net.ipv6.conf.all.forwarding: '1'
    kernelModules:
      - vrf
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: OSConfigPolicy
metadata:
  name: galactic-fabric
  labels:
    contributor: galactic
  namespace: infra-ops
spec:
  selector:
    matchExpressions:
      - key: fleet.datumapis.com/role
        operator: In
        values:
          - compute
          - edge
          - dns
  template:
    interfaces:
      - name: fabric0
        classRef:
          name: fabric-loopback
      - name: srv6
        classRef:
          name: srv6-locator
    sysctls:
      net.ipv6.conf.all.seg6_enabled: '1'
      net.ipv6.conf.default.seg6_enabled: '1'
      net.ipv6.conf.all.seg6_require_hmac: '-1'
    kernelModules:
      - vrf
      - mpls_router
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: OSConfigPolicy
metadata:
  name: compute-runtime
  labels:
    contributor: compute
  namespace: infra-ops
spec:
  selector:
    matchLabels:
      fleet.datumapis.com/role: compute
  template:
    extensions:
      - kata-containers
    files:
      - path: /etc/cri/conf.d/20-customization.part
        content: |-
          [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.katars]
            runtime_type = "io.containerd.katars.v2"
            privileged_without_host_devices = true
            [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.katars.options]
              ConfigPath = "/opt/kata/share/defaults/kata-containers/runtime-rs/configuration.toml"
```
The Talos provider resolves a qualified artifact for the pinned release and
extension set. Provider artifact/configuration digests, references, and execution
authority are retained before effects. Changing a policy cannot cause an
unadmitted reinstall or silently change an in-progress deployment.

## Where the facts go

- **`site_v4`, service VIPs, edge-services anycast, DNS and certificates:** Their services
  and authorized IPAM claims under the cluster context.
- **Provider facility codes and region IDs:** Adapter mappings to canonical Fleet
  inventory Facilities.
- **Facility eligibility for a cluster:** Site allocation selector; resolved physical
  facts stay in inventory.
- **Hardware UUID, serial, components, rack and wiring:** DeviceClaim expectations,
  registered Device observations, Cable/PowerFeed topology and evidence.
- **Provider host ID, management access, billing:** Typed provider extension and
  operator-only observations.
- **Install disk WWID:** Verified component inventory and an exact admitted operation
  target.
- **Bond members, uplink addresses and routed prefixes:** Network configuration/IPAM,
  informed by verified physical endpoints.
- **Vendor BGP peers and `HOSTING-AS7979` policy:** Provider observations supplied to
  networking/routing authority; not hardware-class routing policy.
- **Host assignment to production versus lab:** Exclusive MachineClaim binding; Device
  identity remains stable.
- **Stable node name, fabric loopback and SRv6 identity:** NodeClaim/IPAM lifecycle with
  predecessor fencing on replacement.
- **Sysctls, modules, runtime files and extensions:** Contributed OS policies, retained
  recipes, explicit per-node admission, one executor.
- **Node role, labels and taints:** MachinePool/NodeClaim managed cluster intent.
- **Karmada labels and per-cell configuration:** Cluster intent and verified controller
  outputs.
- **Existing inventory Site/Cluster/Node views:** Optional derived read models; not a
  second physical-inventory writer.
- **Retiring WAS1 machines:** Disable new allocation, coordinate active workload
  replacement, then explicitly authorize provider release.

The WAS1 retirement flags in the ledger are requests to assess and coordinate,
not permission to cancel a rented host during adoption. Removing a Facility from
a selector or deleting a record must not terminate existing workloads.

## Adoption and qualification gates

- Verify canonical premises and provider mappings before using Facility labels
  for placement or independent failure-domain guarantees.
- Authenticate and reconcile existing equipment against accepted DeviceClaims;
  missing rack, cable, or power evidence stays Unknown.
- Recover every active consumer binding before permitting allocation. Demonstrate
  that retries and partial migration cannot double-allocate a machine.
- Select exact trusted disk/NIC identities and separate observed migration inputs
  from future operator policy.
- Prove equivalent rendered managed configuration before transferring its writer;
  retain current image/configuration and bootstrap authority during adoption.
- Coordinate WAS1 retirement and any replacement through the managed node and
  shared release protocols. Neither adoption nor a selector edit authorizes it.
- Qualify the owned provider separately with the
  [owned-server journey](owned-bare-metal/README.md), including customer OOB access,
  isolated installation, session revocation, and cleaning before reuse.
