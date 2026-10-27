---
status: provisional
stage: alpha
latest-milestone: "v0.1"
---

<!-- omit from toc -->
# Operating system configuration

Part of the [Fleet infrastructure management service](README.md). The OS subsystem
resolves image and configuration requirements, coordinates explicitly requested installation,
and reports applied state. It is shared by managed clusters and standalone
infrastructure consumers. Labels select configuration contributions; typed,
authorized contracts identify the reservation, admitted inputs, and permitted
effects.

See [shared machine allocation](bare-metal-hardware.md),
[cluster orchestration](cluster-lifecycle.md), [physical inventory](inventory.md),
and [owned-metal provisioning](bare-metal-provisioning.md). The
[owned-server journey](examples/owned-bare-metal/README.md) shows standalone
customer installation; the [us-east-1-alice example](examples/us-east-1-alice.md)
shows a managed cell.
Fields and resource shapes below are proposed contracts, not deployed schemas.

- [Summary](#summary)
- [Proposal](#proposal)
  - [Image resource](#image-resource)
  - [Interface class resource](#interface-class-resource)
  - [Installation resource](#installation-resource)
  - [Policy resource](#policy-resource)
  - [Merge rules](#merge-rules)
  - [Managed configuration admission](#managed-configuration-admission)
  - [Operating system provider](#operating-system-provider)
  - [Status](#status)
  - [Replaced artifacts](#replaced-artifacts)
  - [Example: add a fabric interface from galactic](#example-add-a-fabric-interface-from-galactic)
- [Implementation](#implementation)
- [Rollout plan](#rollout-plan)
- [Open questions](#open-questions)

## Summary

Customers choose a supported OS and bootstrap configuration for their claimed
machine. Fleet resolves the exact approved inputs, coordinates installation,
and reports the agreed handoff outcome. General-purpose customer installation
is part of the first bare-metal release, using OS integrations that qualify
image, bootstrap, hardware, and execution compatibility. Consumers administer
their own OS after a standalone handoff.

`Installation` is an immutable initial-install or reinstall request against a
MachineClaim. MachineClaim holds the reservation and runtime power intent.
An installation does not require a Cluster, NodeClaim, or ongoing guest agent.
Supported OS choices extend beyond Talos; the Talos examples below illustrate
the existing managed environment and its typed integration.

For managed infrastructure, Galactic, telemetry, and container runtimes
contribute OS requirements through `OSConfigPolicy`. A composition pipeline
combines those contributions with approved image, hardware, network, and
consumer inputs, retaining an immutable recipe for the bound machine. Managed
in-place configuration has its own revision/admission contract and selected
executor. These policies do not continuously overwrite a self-managed guest OS.

## Proposal

### Image resource

A `MachineImage` is a logical OS release, not one image catalogue entry per
role or service. The selected OS provider resolves exact artifacts for the
architecture, platform, and supported extension set. This Talos example shows
one provider integration within the OS catalogue:

```yaml
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

Published artifacts are immutable and content-addressed. A request resolves
its catalogue reference to retained content and exact digests before execution;
a floating tag or mutable reference alone is insufficient. MachineImage inputs
and extension changes can produce new candidates without altering running
machines. Image/configuration compatibility, Secure Boot, architecture, and
supported installation mode are validated against the hardware offering.

Each supported OS integration declares its qualified installation and bootstrap
contract. Consumers choose supported image inputs; the hardware provider does
not choose OS contents. Different integrations can use images, installers, or
other qualified methods under the same Installation contract. Artifact ingestion
must not expose unrestricted fetching from private management networks or leak
configuration credentials.

### Interface class resource

An `InterfaceClass` describes an interface shape and address requirements,
not the ownership or lifetime of a live address:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: InterfaceClass
metadata:
  name: fabric-loopback
spec:
  type: loopback                                   # loopback | dummy | vlan | vrf
  addressing:
    ipv6:
      ipClassRef: {name: datum-fabric-ipv6}
      prefixLength: 128
```

The authorized consumer supplies the IPAM context. A managed NodeClaim owns
its logical identity context so movable loopback/SRv6 reservations survive
reinstall and hardware replacement. A hardware uplink belongs to its physical
attachment; a standalone consumer's network attachment has its own declared
reservation lifecycle. A cluster parent allocation does not imply that every
child address has an Installation lifetime.

Networking/IPAM resolve addresses and attachment facts. The OS pipeline
consumes them and configures the appropriate interfaces. It neither allocates
a second set of addresses nor infers network authority from a label. Interface
removal is an admitted configuration change. Release requires acknowledged
removal from every previous incarnation/attachment or a proven fence, and must
account for retained rollback prerequisites. Removing the last policy entry
alone does not release a still-live address.

### Installation resource

Managed NodeClaim controllers and standalone project consumers use the same
Installation contract. It references a MachineClaim, never an independently
chosen Machine. The request can be created while its claim is pending; the
claim's explicit `initialInstallationRef` and `networkAttachmentRefs` prevent
creation order from racing initial placement and power-on.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Installation
metadata:
  name: alice-compute-0-os-01
  labels:
    fleet.datumapis.com/role: compute
    fleet.datumapis.com/cluster: us-east-1-alice
    fleet.datumapis.com/site: us-east-1
    fleet.datumapis.com/environment: production
spec:
  claimRef: {name: alice-compute-0-hardware-01}
  expectedPredecessor: {type: None}
  imageRef: {name: talos-1.13.7}
  bootstrapConfigRef: {name: alice-compute-0-bootstrap-01}
status:
  bindingID: <exclusive-allocation-id>
  bindingEpoch: 1
  resolvedImageDigest: sha256:<exact-image-digest>
  conditions:
    - type: ImageDeployed
      status: "True"
      reason: VerifiedInstallation
    - type: Ready
      status: "True"
      reason: PlatformHandoffComplete
```

The example is a managed node's initial request. Its controller supplies
bootstrap/configuration derived from that NodeClaim's admitted recipe. A
standalone consumer supplies image-specific bootstrap through the same request
shape. Authorization and provider capabilities determine the supported OS
management mode; labels cannot grant managed execution or access to another
project's configuration.

The immutable request resolves to a protected execution record containing:

- Installation UID, consumer/project authority, MachineClaim UID, committed
  Machine/provider identity, binding ID/epoch, and expected predecessor.
- Retained image, bootstrap, recipe, native configuration, and relevant class
  content with digests; resolved disk/interface identities and required network
  preparation/transition evidence.
- One selected executor, operation ID, permitted effects, and the admission
  epoch or explicit consumer installation authorization. Managed disruptions
  also require the applicable safety checks and predecessor-exclusion evidence.

Reference names are resolved to UIDs before commitment. Secret or source
object recreation does not substitute new contents. The controller persists
authority and exact inputs before external effects; status can summarize the
result but is not the only record of irreversible-operation intent. Consumer
writers cannot forge controller execution authorization.

Image, bootstrap, claim, and predecessor are immutable request inputs. Editing
a MachineImage or policy does not mutate an accepted Installation. Reinstall
requires a new Installation naming the current predecessor UID, for example:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: Installation
metadata:
  name: alice-compute-0-os-02
spec:
  claimRef: {name: alice-compute-0-hardware-01}
  expectedPredecessor:
    type: Installation
    name: alice-compute-0-os-01
    uid: <predecessor-installation-uid>
  imageRef: {name: talos-next-qualified-release}
  bootstrapConfigRef: {name: alice-compute-0-bootstrap-02}
```

The coordinator checks the expected predecessor against the claim's durable
current-incarnation record and commits with a concurrency check. Two competing
reinstall requests cannot both win. It does not choose the newest object by
creation time. Reinstall may retain the same hardware reservation. Hardware
replacement requires a new MachineClaim with a new immutable asset binding;
NodeClaim, where present, coordinates logical identity continuity and the
separate fence of the predecessor on the old claim.

A blank claim with no Installation is valid. Its runtime power intent remains
on MachineClaim. For an initial Installation, the coordinator serializes power
with provisioning; an independent power loop cannot activate it early.
Networking first resolves placement feasibility and reserves resources, then
prepares the restricted connectivity required by the selected method. Consumer
activation waits for verified installation and exclusion of privileged
provisioning access, with attachment evidence for the exact binding. A temporary
agent, whole-disk image, and powered-off handoff are one possible implementation;
other methods must meet equivalent isolation and completion requirements. A
booted installer is not proof of an installed OS. See
[bare-metal provisioning](bare-metal-provisioning.md).

Deleting an in-progress request asks for cancellation at a defined safe
boundary; finalizers and the operation journal retain unresolved obligations.
Deleting completed request history, when retention allows, does not uninstall
the OS, release the reservation, or change power intent. The initial-install
gate is completed durably and does not reopen when old request history is
removed. Claim release owns access revocation, fencing, cleanup, and eventual
hardware availability. Owned inventory and provider records outlive all of
these disposable requests.

### Policy resource

Services contribute managed configuration through OSConfigPolicy. Selectors
are scoped to authorized managed targets. Changes resolve candidates rather
than directly modifying every matching Installation:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: OSConfigPolicy
metadata:
  name: galactic-fabric
spec:
  selector:
    matchExpressions:
      - key: fleet.datumapis.com/role
        operator: In
        values: [compute, edge, dns]
  template:
    interfaces:
      - name: fabric0
        classRef: {name: fabric-loopback}
      - name: srv6
        classRef: {name: srv6-locator}
    sysctls:
      net.ipv6.conf.all.seg6_enabled: "1"
    kernelModules: [vrf, mpls_router]
status:
  matched: 19
  candidateReady: 19
  admitted: 0
  applied: 0
  conflicts: 0
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: OSConfigPolicy
metadata:
  name: compute-runtime
spec:
  selector:
    matchLabels:
      fleet.datumapis.com/role: compute
  template:
    extensions:
      - kata-containers
    files:
      - path: /etc/cri/conf.d/20-customization.part
        content: "..."
```

Self-managed consumer operating systems are outside this continuing policy
reconciliation. A policy match cannot take over an OS that was handed to its
consumer. Provider-specific settings such as `TalosMachineConfigPatch` remain
in the provider group, but enter the same retained input and validation path.

### Merge rules

`OSConfigTemplate` is the normalized contribution schema. Source templates,
the retained compiled recipe, and the private materialized native document
are distinct. There is no highest-priority Installation override that bypasses
composition, and full rendered configuration containing secrets is not copied
into public status.

| Field | Keyed by | Merge rule | Disagreement |
|---|---|---|---|
| `interfaces[]` | `name` | Combine compatible fields | Unequal overlapping settings conflict |
| `sysctls` | Key | Union; identical values can share provenance | Different values conflict |
| `kernelModules`, `extensions` | Value | Deduplicate; validate semantic compatibility | Unsupported/incompatible sets fail |
| `files[]` | `path` | Identical content/attributes may share provenance | Different content or attributes conflict |
| Scalars | Field path | One value or identical shared value | Different values conflict |

The renderer enforces these rules:

- A conflict identifies the contributors and field; the target retains its
  admitted configuration. An arbitrary numeric priority cannot silently
  override another owner's setting. If override support is later required,
  its field scope and authority must be explicit.
- Hostname, selected installation disk, protected uplinks, and identity are
  derived from authorized consumer/allocation inputs. Contributions cannot
  redefine them. Validation includes provider-native patches and the final
  native result, not only the generic template.
- Contribution removal has explicit semantics for removing files/interfaces,
  resetting settings, and any required reboot/reinstall. Omitting a previously
  written field is not assumed to undo it on the target OS.
- The exact extension set is an input to image resolution. The provider checks
  compatibility and classifies the actual change as Live, Reboot, Reinstall,
  or Unsupported. It must not downgrade an unsupported change into an
  unapproved destructive fallback.
- Configuration secrets and native artifacts are retained in protected,
  versioned storage. Public status contains bounded references, digests, and
  redacted provenance. Transport credentials can rotate independently of
  configuration-input versions.

### Managed configuration admission

The managed path separates authored inputs, candidate resolution, approval,
materialization, execution, and observation:

1. Resolve the image, class, policies, provider-specific inputs, selected
   labels, and compiler version into an immutable retained recipe. A list of
   mutable references with a hash is insufficient; the content must survive
   later edits/deletion.
2. Select/admit that recipe through the cluster/pool progression contract and
   the target NodeClaim's durable approval epoch. Initial creation and new
   logical slots require an explicit recipe selection.
3. Materialize for the exact reservation, architecture, disk, addresses, and
   bootstrap context. Different machines can legitimately have different
   native document digests for the same logical recipe.
4. Validate current prerequisites and reserve the applicable concurrency or
   disruption budget. Persist a binding-specific operation authorized for the
   exact Installation incarnation and materialized digest.
5. Apply through one executor and observe the actual applied/booted revision
   plus fresh health before progression continues.

Live changes are gated too. Writing configuration that activates at the next
unexpected reboot already changes the machine's future behavior and is not
unrestricted staging. Lower providers consume the explicit operation contract;
they do not read NodeClaim reboot policy or infer authority from Rollout labels.

In-place changes do not edit the original immutable Installation request. They
use the separate admitted revision/execution record bound to its active
incarnation. A change classified Reinstall instead creates a new Installation
with an expected predecessor. The storage form of revisions, selections, and
operations can be protected artifacts or controller records; each has durable
identity and retention, without requiring a new public CRD for every step.

Per-node approval survives partial rollout, cancellation, and repair. A node
still admitted at R1 is repaired at R1 even when another node has advanced to
R2. Pause stops new approvals; committed work reaches its defined safe boundary
under current checks. Retention covers active selections, unfinished operations,
terminating/quarantined incarnations, and supported rollback prerequisites.
See [Rollout](cluster-lifecycle.md#rollout-resource).

### Operating system provider

The OS provider resolves/builds artifacts, translates configuration, checks
compatibility, and classifies supported changes. Talos kinds live in
`talos.fleet.datumapis.com`; provider details such as schematic hashes remain
there. The infrastructure provider consumes approved artifacts and owns
physical boot transport, power, and authorized hardware preparation. It does
not decide image contents or trigger upgrades from mutable catalogue changes.

Exactly one selected adapter applies configuration to an incarnation. With
Omni, the integration must pin its configuration inputs and verify its final
machine configuration: Omni's own composer cannot race an independent Talos
writer. Native acceptance, reset, or cluster-removal behavior must respect the
same operation authority, rather than independently wiping or rebooting a
reserved host. An unsupported integration blocks activation.

A backend job acceptance or boot acknowledgment is not evidence that the OS
installed correctly, reached the required boot state, or joined a cluster.
Each provider reports its own completion evidence. Bootstrap transport must
authenticate the intended host; MAC/serial matching and an unverified TLS
connection do not establish that trust. The provisioning implementation
qualifies artifact verification, one-time boot, media removal, disk targeting,
network handoff, and crash recovery together.

For a self-managed guest, the qualified installer completes image/bootstrap
handoff and stops OS reconciliation. Runtime power and console access remain
claim-scoped. `GuestReady` is Unknown unless a separate supported guest-health
contract supplies evidence; platform handoff does not assert SSH or application
readiness.

### Status

Status answers distinct questions:

- What candidate exists, what recipe was admitted, and what native revision
  was applied/booted for the current incarnation?
- Which source contributed a setting, and which protected artifact contains
  its exact retained version?
- Is an update blocked by composition, missing admission, hardware/network
  prerequisites, unavailable budget, or unsupported provider capability?
- Which IPAM reservations and verified attachments supplied an interface?

Conditions include observed generation plus relevant input and binding
identities. Configuration dependencies can change without changing the
Installation generation. Do not present a public full-config blob, bootstrap
credentials, console bearer URL, or private BMC/network mapping. Exact
installation and operation evidence survives controller restart and late
callbacks from previous incarnations are rejected.

### Replaced artifacts

- Galactic per-cluster variables and machine-config render templates become
  OSConfigPolicy/InterfaceClass contributions plus networking-owned topology
  and address contracts.
- The image-building portion of `create-iso` becomes MachineImage and
  provider-owned image resolution.
- Ad hoc live patches/reboot coordination become retained revision approval and
  one explicit execution contract.

### Example: add a fabric interface from galactic

Declare the interface class and a policy contribution:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: OSConfigPolicy
metadata:
  name: galactic-fabric
spec:
  selector:
    matchExpressions:
      - key: fleet.datumapis.com/role
        operator: In
        values: [edge, compute]
  template:
    interfaces:
      - name: fabric0
        classRef: {name: fabric-loopback}
```

Matching managed targets gain a configuration candidate. Resolve addresses
through their durable consumer contexts, validate conflicts and provider
capabilities, then admit the exact recipe through Rollout. The executor applies
only approved changes and reports the resulting native revision. Galactic
consumes observed `fabric0` information after its network/OS prerequisites are
satisfied. Policy creation alone neither rewrites all machines nor authorizes
reboot, and policy deletion alone never releases active addresses.

## Implementation

The OS subsystem in `datum-cloud/fleet` watches Installation and configuration
inputs. It resolves a claim's immutable binding and explicit execution contract;
it need not watch Cluster/NodeClaim to infer authorization. The cluster
subsystem supplies managed approvals. Standalone requests use consumer
installation authority subject to offering/operator policy. Scope, reference
permissions, and controller-owned fields must be enforced at admission.

The shared operation coordinator serializes installation, managed application,
power, networking handoff, and release against the exact binding/incarnation.
It retains inputs and idempotency records before external effects. Recovery
queries uncertain operations instead of blindly repeating disk writes or
reboots. A Kubernetes lease by itself does not fence a stale remote worker.

An OS adapter implements artifact resolution, translation, and change
classification. Talos/Omni illustrate integrations for the existing managed
environment; the service contract does not require those products. The selected
execution integration and infrastructure provider implement their respective
completion contracts. IPAM, networking, and inventory remain authoritative for their own state; OS
configuration does not create independent physical-placement or allocation
records.

## Rollout plan

Coordinate these milestones with the [umbrella plan](README.md#rollout-plan):

- Install schemas and admission without hardware effects; import existing
  installation/configuration observations and reservation identities.
- Qualify a complete standalone customer installation and reinstall path for
  an OS other than Talos. Verify exact targets, bootstrap delivery, isolated
  network handoff, the offered boot outcome, and release cleanup without
  managed-cluster resources or a mandatory ongoing guest agent. This is a first-release
  gate for general-purpose bare-metal provisioning.
- Resolve candidate outputs and compare with existing managed configuration.
  Adoption stays non-destructive and does not convert a self-managed OS into
  a continuously reconciled one.
- Qualify each additional OS and managed configuration integration against
  its declared installation, bootstrap, and execution contract.
- Exercise immutable requests, conflicting reinstall preconditions, policy
  conflicts, live-change approval, partial-rollout repair, late callbacks, and
  operation recovery before enabling wider managed progression.

## Open questions

- **Revision persistence.** Choose protected artifact/selection storage,
  retention, and recovery semantics before implementing admission.
- **Provider integration.** Qualify each selected OS/cluster integration and
  final configuration verification with one execution authority. Talos/Omni are
  examples from the existing environment.
- **Initial OS catalogue.** Choose the general-purpose OS families, bootstrap
  contracts, and hardware combinations offered at first release. Each requires
  explicit compatibility and handoff evidence; an arbitrary image URL does not
  establish support. Qualify additional choices through the same process.
