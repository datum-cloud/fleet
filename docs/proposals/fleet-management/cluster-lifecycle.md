---
status: provisional
stage: alpha
latest-milestone: "v0.1"
---

<!-- omit from toc -->
# Cluster lifecycle

Part of the [Fleet infrastructure management service](README.md). Cluster
orchestration declares where a managed cluster runs, its desired
configuration, and its node capacity. It consumes the shared
[machine allocation API](bare-metal-hardware.md) and
[OS installation API](os-configuration.md); standalone infrastructure
consumers use those same APIs without creating a Cluster or NodeClaim.

Physical assets and commissioning belong to [Fleet inventory](inventory.md).
Datum-owned hardware operations belong to
[bare-metal provisioning](bare-metal-provisioning.md). For a managed cell,
see [examples/us-east-1-alice.md](examples/us-east-1-alice.md). Resource shapes
and field names below are proposed contracts, not implemented schemas.

- [Summary](#summary)
- [Proposal](#proposal)
  - [Site and cluster resources](#site-and-cluster-resources)
  - [Capacity resource](#capacity-resource)
  - [Node resource](#node-resource)
  - [Rollout resource](#rollout-resource)
  - [Cluster provider](#cluster-provider)
  - [Network facts and ownership](#network-facts-and-ownership)
  - [Status](#status)
  - [Replaced artifacts](#replaced-artifacts)
  - [Example: add a cluster at an existing site](#example-add-a-cluster-at-an-existing-site)
- [Implementation](#implementation)
- [Rollout plan](#rollout-plan)
- [Open questions](#open-questions)

## Summary

An operator declares a `Site`, a `Cluster` using that Site, and a
`MachinePool` for each role. Controllers maintain one durable `NodeClaim`
per logical node slot. Each NodeClaim obtains hardware through an exclusive
`MachineClaim` and an explicit `Installation` request. The reservation and
installation have shorter lifetimes than the logical node.

A `Rollout` advances an exact, retained configuration through managed clusters
and nodes under declared safety constraints. Editing an input creates a
candidate; it does not immediately change every matching machine. Desired
configuration, approval to activate it, and observed application are separate
contracts. This replaces per-cell literals, provisioning templates, the
Karmada label Job, and the `cluster_lifecycle` flag while preserving their
useful operational outcomes.

## Proposal

### Site and cluster resources

`Site` is an operational placement and capacity-sharing boundary. Geography
belongs to `Location`; physical premises belong to the provider-neutral
`Facility` in `inventory.fleet.datumapis.com`. A Site selects permitted
Facilities rather than maintaining a second list of provider facility codes.
For owned assets, physical placement derives from the inventory Device and
Rack; Site policy does not edit that placement. Provider-reported placement
must retain its provenance when no verified physical Device is available.

One Site can serve several clusters and standalone allocations. Removing a
Facility from its policy affects future allocation and does not migrate,
release, or retire existing reservations. An allocation rechecks policy before
commit. Empty or omitted selection must not silently permit every Facility;
the initial API should require explicit selection. Authority to change
selection labels is part of allocation administration, not ordinary workload
label management.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Site
metadata:
  name: us-east-1
spec:
  locationRef: {name: us-east-1}
  allocation:
    facilitySelector:
      matchLabels:
        fleet.datumapis.com/allocation-group: us-east-1
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: Cluster
metadata:
  name: us-east-1-alice
  labels:
    fleet.datumapis.com/wave: stable
spec:
  siteRef: {name: us-east-1}
  clusterProviderRef:                               # connection and transport credentials
    apiGroup: omni.fleet.datumapis.com
    kind: OmniInstance
    name: production
  clusterProviderConfigRef:                         # independently versioned configuration
    apiGroup: omni.fleet.datumapis.com
    kind: OmniClusterProfile
    name: cell
  channel: stable                                  # authored channel intent
  environment: production
  version: v1.35.5
  zones:
    - us-east-1a
  updatePolicy:
    admission: Staged
    controlPlane:
      maxUnavailable: 1
      requireQuorum: true
status:
  phase: Ready
  machinePools:
    - name: us-east-1-alice-controlplane
      desired: 3
      ready: 3
    - name: us-east-1-alice-compute
      desired: 1
      ready: 1
  capabilities:                                    # observations projected to Karmada labels
    infra.datum.net/gateways: "true"
    compute.datumapis.com/runtime-class.unikernel: "true"
  configDelivered:
    revision: v0.72.0
  conditions:
    - type: Ready
      status: "True"
      reason: AdmittedConfigurationHealthy
```

The provider connection and desired provider configuration have separate
lifecycles. Rotating connection credentials does not create an OS or cluster
configuration change. Provider configuration content, channel revisions, and
other configuration inputs are pinned when a candidate is resolved.

Cluster-specific compute runtime or routing features belong to the
corresponding service APIs targeting this Cluster. Those controllers can
contribute OS requirements through the common configuration pipeline. A
Cluster can supply an IPAM parent context and expose service capability
observations without owning every service address or feature flag.

### Capacity resource

A `MachinePool` requests logical node capacity of one class and role for one
managed cluster. Class and image references are catalogue inputs; their
resolved content is retained with an admitted recipe. Changing `replicas`
alone does not change that recipe.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachinePool
metadata:
  name: us-east-1-alice-compute
spec:
  clusterRef: {name: us-east-1-alice}
  role: compute
  machineClassRef: {name: serverscom-compute-2u}
  imageRef: {name: talos-1.13.7}
  replicas: 1
  labels:
    compute.datumapis.com/runtime: unikraft
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
status:
  replicas:
    desired: 1
    pending: 0
    ready: 1
    draining: 0
```

A drained NodeClaim still occupies its logical slot. Replica management must
not create another slot merely because the existing one is under maintenance.
The single-replica example permits interruption; it does not promise continuous
availability. Surge counts additional physical reservations rather than
additional logical nodes.

A consumer that places tenants on the pool, such as Compute, can own its size
and gate its nodes. The pool names the consumer; that consumer then writes
`replicas` through the scale subresource from its own demand, and nothing else
does. Disruption gates are conditions the consumer sets on a NodeClaim before
Fleet may drain or replace it. Readiness gates are conditions the consumer sets
before the node counts as ready for the pool.

```yaml
spec:
  consumer:
    name: compute.datumapis.com
    managesReplicas: true
    disruptionGates:
      - compute.datumapis.com/evacuated
    readinessGates:
      - compute.datumapis.com/tenant-ready
```

A consumer gate that is missing or stale blocks the transition it guards; it
never counts as passed. Pool sizing that orders new rented capacity follows the
provider's procurement policy (see [Providers](README.md#providers)).

Standalone consumers create a MachineClaim directly, with optional initial
Installation and network attachment requirements. A `membership: standalone`
branch that creates no NodeClaim is unnecessary: cluster pools and direct
allocations have explicit, separate entry points into the same allocator.

### Node resource

The pool controller maintains a `NodeClaim` for each logical node slot. It is
the continuity point for the managed hostname, movable IPAM identity, and the
configuration approved for that node. It is distinct from an inventory
`DeviceClaim`, which records an operator's physical commissioning expectation,
and from a MachineClaim, which reserves a specific asset.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: NodeClaim
metadata:
  name: us-east-1-alice-compute-0
spec:
  poolRef: {name: us-east-1-alice-compute}
  role: compute
  desiredState: Running                            # Running | Cordoned | Drained
  identityScopeRef: {name: alice-compute-0-addresses}
  configurationAdmission:                          # written by the authorized progression controller
    recipeRef:
      name: alice-compute-r1
      digest: sha256:<retained-recipe-digest>
    epoch: 1
status:
  phase: Ready
  claimRef: {name: alice-compute-0-hardware-01}
  installationRef: {name: alice-compute-0-os-01}
  node:
    name: us-east-1-alice-compute-0
    uid: <kubernetes-node-incarnation-uid>
    ready: true
  interfaces:
    - name: fabric0
      addresses:
        - 2607:ed40:10d::1/128
```

NodeClaim status also reports each [node service](node-services.md) instance
on the node, and the `fleet.datumapis.com/node-services-ready` condition. A
consumer readiness gate can depend on that condition.

The recipe reference denotes retained, immutable input content; this proposal
requires that contract without requiring another public CRD for every internal
revision record. Pool template ownership, operator maintenance intent, and
configuration admission have explicitly assigned writers. Labels support
policy selection; they do not grant allocation or disruption authority.

The node controller creates a [MachineClaim](bare-metal-hardware.md#claim-resource)
using the same allocation service as standalone consumers. Its committed
binding to a Machine UID is immutable. It declares the complete required
network attachment set and initial
[Installation](os-configuration.md#installation-resource) reference before
binding can proceed. Names are resolved to UIDs and input digests; the allocator
waits for those inputs and current network placement validation. It must not
wait for a tenant attachment to be fully active before selecting hardware.

The node controller supplies the exact admitted bootstrap/configuration and
identity context to the Installation. Installation does not discover permission
to act by reading NodeClaim labels. Initial power-on is serialized with
installation and networking through MachineClaim runtime intent. A direct
power reconciler cannot turn the host on in the middle of the maintenance-to-
production network handoff.

A reinstall on the same hardware may retain the MachineClaim and creates a
new Installation naming the expected predecessor incarnation. Hardware
replacement creates a new MachineClaim and Installation. It preserves the
NodeClaim's admitted recipe and logical address reservations; it does not
rebind the old MachineClaim or mutate a Machine to represent another asset.
The Kubernetes Node incarnation changes even if its name remains stable.

Before a replacement activates shared credentials, addresses, routes, or
storage identity, the predecessor must be durably excluded. Node deletion,
heartbeat loss, or a power-command acknowledgment is insufficient. Reserve
and prepare replacement capacity under surge limits while the predecessor
remains reserved or quarantined, but block unsafe identity activation.
Quorum/drain coordination, network withdrawal, and storage fencing have their
own completion evidence.

Node deletion or scale-down initiates release through the claim protocol.
Inventory Device, Machine, and provider management records must not be
removed by garbage collection of logical nodes. Child claim/installation
finalizers, if used, retain obligations until their effects are accounted for;
no safety rule assumes a particular sibling deletion order. Logical IPAM
reservations are released only after all prior attachments/incarnations have
been removed or fenced. Address parent scope and reservation lifetime are
separate concepts.

### Rollout resource

A `Rollout` declares a bounded transition to exact revisions, across resolved
cluster identities and waves. A selector may choose initial candidates, but
the durable plan freezes Cluster UIDs, baseline selections, target recipes,
and wave membership before execution. Later label matches do not silently
join an in-progress rollout.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Rollout
metadata:
  name: alice-r2
spec:
  desiredState: Running
  targets:
    - clusterRef:
        name: us-east-1-alice
        uid: <alice-cluster-uid>
      recipeRef:
        name: alice-r2
        digest: sha256:<retained-target-recipe-digest>
      baseline:
        selectionEpoch: 1
  waves:
    - name: canary
      maxConcurrentClusters: 1
      healthyFor: 30m
  onDesiredInputsChanged: Halt
  failurePolicy: Halt
  healthPolicy:
    requireAppliedRevision: true
    requireClusterReady: true
    requirePoolsReady: true
    telemetryMaxAge: 2m
    missingEvidence: Block
status:
  phase: Progressing
  currentWave: canary
  clusters:
    pending: 0
    updating: 1
    done: 0
    failed: 0
```

Rollout advances controller-owned selections and per-node approvals, not
Git-authored Cluster, pool, or policy inputs. Flux/Karmada consume the admitted
outputs. `configDelivered` observes actual delivery; a successful API write or
Flux submission is not proof of application or health.

Cluster and pool selections alone are insufficient. Each NodeClaim retains its
approved recipe and epoch. If node A was approved for R2 and node B remains on
R1 when a rollout pauses, repairing B must use R1. A pool separately retains the
recipe admitted for new slots. New clusters and pools require an explicit
initial admission; there is no fallback to the latest mutable policy.

All effects, including live configuration, pass through approval. Staging a
change that would activate on an unexpected reboot also counts as activation.
Per-pool concurrent-update limits count committed work; unavailability limits
include other maintenance and remediation. Control-plane quorum checks are
separate from drain and pod disruption budgets. Missing or stale health is
Unknown and blocks progression. The healthy hold begins only after the target
revision is applied and its required readiness is observed.

Pause/cancel stops new approvals and selections; previously committed work
reaches its defined safe boundary under fresh prerequisite checks. Cancellation
is not rollback. Rollback is a new supported transition and cannot reuse an
address or artifact prerequisite that no longer exists. A newer input during a
rollout halts new approvals by default and requires a successor objective.
One progression authority per cluster, with epochs/concurrency checks, is the
initial contract. Deleting completed Rollout history does not delete durable
cluster, pool, or node selections, retained recipes, or unfinished obligations.

### Cluster provider

`clusterProviderRef` identifies a connection in the provider-owned group;
`clusterProviderConfigRef` identifies the independent configuration input.
The cluster provider owns bootstrap, membership, labels/taints, drain, and
Kubernetes/control-plane lifecycle coordination. The examples illustrate an Omni
integration for the existing environment, with kinds in
`omni.fleet.datumapis.com`; the service contract does not select a cluster engine.

Provider contracts carry exact target identities, operation IDs, supported
capabilities, and observed completion. The hardware provider executes only
authorized physical work. The OS subsystem and selected executor own OS
configuration. Omni must not race a direct Talos writer, and native acceptance,
reset, or removal flows must not introduce wiping or rebooting outside the
admitted operation. See [OS provider integration](os-configuration.md#operating-system-provider).

### Network facts and ownership

Infrastructure providers expose normalized hardware and attachment facts,
including concrete interface identity and externally assigned addressing or
peer constraints. Their provider-owned schemas are not interpreted directly
by the cluster controller. Networking owns attachment realization, router
membership, topology, BGP policy, and routing configuration. Fleet's cluster
subsystem consumes those outcomes and can summarize their health.

For owned metal, inventory records the physical Device/Cable topology;
networking verifies and programs the appropriate switch attachments. A declared
Cable is not proof of wiring. Provisioning can use an isolated maintenance
network before the tenant attachment becomes active. The explicit transition
and proof of maintenance-agent exclusion belong to the shared allocation and
[bare-metal provisioning](bare-metal-provisioning.md) contracts.

Galactic owns fabric intent such as site code, router membership, locator
requirements, and FRR configuration. Its OS requirements enter the same
configuration pipeline as other contributions. IPAM allocates addresses;
NodeClaim retains managed logical identity reservations. Service VIPs, anycast
blocks, DNS records, and certificates belong to the corresponding services.
The cluster controller does not acquire an independent routing-policy writer.

### Status

Status distinguishes desired input, candidate, selected, per-node approved,
applied, and healthy configuration. Conditions include the observed generation
and relevant input/binding digests; dependent configuration can change without
changing the target object's generation. Counts are numeric, not strings such
as `3/3`. Useful questions include:

- Which clusters support Kata: observed capabilities rolled up from provider
  and NodeClaim runtime readiness, not merely a requested feature flag.
- Which nodes await capacity: `AwaitingMachine` with the claim's reason.
- Whether the intended cell configuration arrived: observed delivery plus
  applied revision and freshness, rather than a submitted reference.
- Whether a replacement can activate: predecessor exclusion and attachment
  evidence for the exact identities, rather than a general `Ready` label.

Public consumer allocation status excludes private BMC/switch mappings.
Operator views may correlate those through the inventory and provider APIs.

### Replaced artifacts

- Per-cell overlay literals become admitted `cluster-config` outputs delivered
  through the existing Flux/Karmada path.
- Provisioning profiles/templates become versioned cluster-provider inputs and
  explicit installation requests.
- Generated upstream-session files become outputs of the networking authority.
- `cluster_labels` and the Karmada patch Job become capability projections.
- `cluster_lifecycle=bringup` becomes observed cluster lifecycle status.
- Hardware-derived managed node names become NodeClaim-derived logical names.
- Physical inventory loading becomes the inventory commissioning/enrollment
  workflow; cluster reconciliation does not create authoritative physical facts.

### Example: add a cluster at an existing site

Declare the Cluster and pools, resolve and review their candidate recipe, and
admit the initial revision. These are authored inputs, not immediate permission
to deploy whatever their dependencies happen to contain later:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Cluster
metadata:
  name: us-east-1-bob
  labels:
    fleet.datumapis.com/wave: canary
spec:
  siteRef: {name: us-east-1}
  channel: beta
  environment: production
  version: v1.35.5
  clusterProviderRef:
    apiGroup: omni.fleet.datumapis.com
    kind: OmniInstance
    name: production
  clusterProviderConfigRef:
    apiGroup: omni.fleet.datumapis.com
    kind: OmniClusterProfile
    name: cell
  updatePolicy:
    admission: Staged
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachinePool
metadata:
  name: us-east-1-bob-controlplane
spec:
  clusterRef: {name: us-east-1-bob}
  role: controlplane
  machineClassRef: {name: netactuate-controlplane}
  imageRef: {name: talos-1.13.7}
  replicas: 3
```

The node controllers request three reservations with the admitted initial
installations and network requirements. A dynamic provider can order capacity;
Datum-owned capacity selects enrolled, prepared stock and reports
`InsufficientCapacity` when it is exhausted. Both use the same exclusive
binding and release protocol. The cluster provider creates membership and
reports readiness. Services targeting the Cluster request their own addresses
and configuration. Reconciliation continues after Ready to report drift and
perform subsequently admitted work.

Changing a node's role creates a candidate pool/template transition. Reducing
one pool and increasing another does not authorize premature reuse of hardware
or logical addresses while the old reservation is still releasing.

## Implementation

The cluster subsystem in `datum-cloud/fleet` watches Site, Cluster, MachinePool,
NodeClaim, and Rollout. It creates shared MachineClaim/Installation requests,
managed identity contexts, and admitted delivery outputs, then consumes their
status. It does not edit physical inventory, provider credentials, or private
hardware bindings. The same shared binder arbitrates direct consumers and
managed clusters; an infrastructure backend must not allocate those machines
independently.

`fleet-provider-omni` implements the cluster contract and, where selected, the
OS execution integration. One executor owns each configuration operation.
Inventory, network, IPAM, and bare-metal provisioning controllers have their
own explicit inputs and completion evidence. Required cross-scope references,
RBAC, immutable fields, and finalizers must be specified before CRD admission
is enabled.

## Rollout plan

Coordinate these milestones with the [umbrella plan](README.md#rollout-plan):

- Install schemas and admission rules; import existing cluster/pool/node
  identity and configuration without triggering allocation or installation.
- Observe existing provider state and compare candidate outputs. Adoption must
  not reset running machines or silently rename managed Kubernetes nodes.
- Transfer output ownership one field/resource at a time, with a single writer
  for `cluster-config`, membership, capabilities, and configuration execution.
- Exercise shared allocation, replacement/fencing, and release with a pilot
  cluster before enabling autonomous progression.
- Enable bounded Rollout progression only after per-node retention, pause,
  partial-rollout repair, controller restart, and fresh-health gates are proven.

## Open questions

- **Adopted node names.** Preserve existing names until an explicitly admitted
  replacement or drain/rejoin; never rename merely because inventory is imported.
- **Revision representation.** Select the retained artifact/binding storage and
  retention mechanism without weakening durable per-node admission or recovery.
- **Provider execution contract.** Qualify the Omni configuration/bootstrap path
  and its lifecycle hooks so no provider bypasses Fleet's authorization.
