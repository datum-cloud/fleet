# Fleet design context

Reviewed on 2026-10-27. This is a synthesis for implementation planning, not a
replacement for the source proposals or a claim that their APIs exist.

## Source proposals

| Source | Reviewed revision | Contribution |
| --- | --- | --- |
| [infra #5782: Fleet infrastructure management service](https://github.com/datum-cloud/infra/pull/5782) | `51781e2456367be82281e1443ca56d0686311133` | Service architecture, inventory, shared allocation, capacity publication, provisioning, OS configuration, and cluster lifecycle. Open draft at review time. |
| [infra #7705: Node services and consumer pool gates](https://github.com/datum-cloud/infra/pull/7705) | `a712d2d3aaddcdc4d81c09d7f4cdd139bef06631` | Node services, consumer-owned pool sizing and gates, procurement guardrails, and regional independence. Open; targets the #5782 proposal branch. |

Read the [core proposal at its reviewed revision][proposal] and the
[extension at its reviewed revision][extension]. The latter includes the core
proposal plus the extension. The source documents, examples, and diagrams remain
in `infra`; this repository records their implications without duplicating them.

## Problem and intended outcome

Operators currently repeat infrastructure facts across inventory, provisioning,
Omni templates, IPAM records, routing configuration, and cluster overlays. Fleet
establishes authoritative records and explicit transitions from installation
planning through preparation, allocation, operation, release, and reuse.

Customers and internal platforms use the same infrastructure lifecycle. A
standalone customer can claim and install a machine without creating a Cluster
or NodeClaim. Managed cluster orchestration adds membership, logical identity,
and coordinated changes above that shared service.

## Subsystem boundaries

All names below describe proposed resources, not implemented schemas.

| Subsystem | Responsibilities and principal resources |
| --- | --- |
| Inventory | `Facility`, `Rack`, `DeviceClaim`, `Device`, `Cable`, and `PowerFeed`: approved installation plans, actual assets, physical reservations, and verification evidence. |
| Capacity publication | Provider `MachineClass` definitions and consumer-local references; discovery and safe summaries through service enablement and IAM. Publication does not reserve capacity or grant source-use permission. |
| Machine allocation | `Machine` and `MachineClaim`: exclusive reservation, admission of consumer operations, activation, and release across provider and consumer projects. |
| Bare-metal provisioning | Typed provider resources and adapters: execute admitted preparation, installation, power, console, and cleanup operations against exact assets. Backend workflows remain private. |
| OS configuration | `MachineImage`, `InterfaceClass`, `Installation`, and `OSConfigPolicy`: approved artifacts, immutable installation requests, configuration composition, and one selected executor per incarnation. |
| Cluster lifecycle | `Site`, `Cluster`, `MachinePool`, `NodeClaim`, and `Rollout`: placement policy, capacity, membership, logical node continuity, and admission of bounded configuration changes. |
| Node services | `NodeService`: per-region service intent, stable slots on managed NodeClaims, placement, rolling updates, and readiness reporting. |

The proposed groups are `fleet.datumapis.com`,
`inventory.fleet.datumapis.com`, and `baremetal.fleet.datumapis.com`, with typed
vendor and OS adapter groups. Exact versions, scopes, and packaging remain design
decisions. A separate public resource is not required for every internal record.

Location owns geography; Facility represents physical premises; Site expresses
operational placement policy over eligible Facilities. Networking/IPAM owns
logical attachments, address allocation, and switch configuration. Galactic owns
fabric and routing intent. DNS, certificates, and service addresses stay with
their owning services. Fleet consumes these contracts and their evidence.

## Lifecycle and authority contracts

1. **Preserve distinct identities.** DeviceClaim describes an approved physical
   installation; Device identifies an actual asset; Machine represents capacity;
   MachineClaim reserves one immutable Machine UID; Installation identifies an OS
   incarnation; NodeClaim preserves a managed logical node across replacements.
   Hardware replacement creates a new MachineClaim and Installation.
2. **Registration does not authorize hardware effects.** Physical verification,
   provider management mode, preparation, and allocation eligibility are separate
   gates. Discovery or a Ready condition cannot authorize wiping, rebooting, or
   installing a machine.
3. **Use one allocator.** Standalone consumers and managed clusters compete through
   the same durable reservation protocol. Retain provider/consumer Project UIDs,
   claim and machine UIDs, class revisions, current authorization, quota holds,
   and binding epochs. Overlapping classes must not duplicate supply.
4. **Separate discovery from authorization.** Consumer MachineClasses reference a
   provider definition in one hop. Check current source-use permission,
   entitlement, quota, and placement feasibility at allocation commit. A copied
   reference, cached status, namespace, or privileged controller identity is not
   proof of consumer access.
5. **Persist exact inputs before effects.** Retain admitted plans, artifacts,
   digests, target identities, predecessor preconditions, execution authority,
   and operation correlation. Timeouts mean unknown outcomes; recover the existing
   operation before retrying. A controller lease alone cannot fence a remote worker.
6. **Admit configuration changes explicitly.** Mutable catalog or policy changes
   produce candidates. Per-node approval pins retained content, including during
   partial rollout and repair. Conflicting configuration contributions fail
   visibly. Talos and Omni must not become competing configuration writers.
7. **Release is a durable protocol.** Revoke access and active sessions, account for
   in-flight work, detach or fence networking, and verify required cleaning before
   reuse. Unknown results retain the reservation or quarantine the asset. Owned
   assets and cleanup obligations survive consumer project deletion.
8. **Adopt without disruption.** Import observations and current reservations,
   compare generated outputs, then transfer one output's ownership at a time.
   Adoption cannot briefly expose an active machine as free or reprovision it.

## Node services and consumer pools

The extension makes Compute a consumer of Fleet-managed pools. A named consumer
owns pool size through the scale subresource and publishes its readiness and
disruption conditions. Missing or stale gates block the transition they guard.

NodeServices select managed NodeClaims by authorized Fleet roles. A service owns
its template and probe semantics; Fleet owns placement, slot identity, update
order, and readiness reporting; networking owns attachments and routing. A runtime
class is installed through the OS configuration path, not by NodeService.

The latest proposal distinguishes initial readiness from degraded operation:

- Joining, rebooting, reinstalling, or replacing a node requires **every slot of
  every gated service** to be ready before opening `node-services-ready`.
- Once open, each gated service needs at least one ready slot to keep the gate
  open. A failed slot reports `SlotDegraded`; no ready slot closes the gate.
  Unknown or stale probe evidence is not success.
- Services remain available while tenants drain. Shared slot addresses activate
  on replacement hardware only after predecessor fencing is proven.

Standalone customer machines are excluded. Their Private DNS is served by the
platform node hosting the VPC attachment. A DPU-based resolver and a location-wide
fallback remain alternatives under discussion.

Regional independence includes continued record publication as well as serving:
each region needs its own NodeService writer and broker storage. The rollout must
establish these before another region relies on the resolver NodeService.

Rented capacity may be acquired only under operator-owned procurement policy.
The extension proposes pilot guardrails of 30 minutes of sustained demand,
at most two orders per location per day, and an alert at 20% excess over committed
demand. These are proposed policy values, not defaults implemented here. Owned
inventory remains finite and reports `InsufficientCapacity` when exhausted.

## Review observations and decisions still needed

- **Provider facts versus Datum policy:** An [open comment on #5782][network-comment]
  asks for a normalized provider-assigned uplink/gateway/peer fact contract,
  distinct from Datum-owned IPAM allocations and routing policy. The reviewed
  proposal acknowledges external facts but does not fully define that shape.
  Resolve this before porting existing host variables into API fields.
- **Durable state and concurrency:** Select storage and commit/recovery protocols
  for physical reservations, allocation plus quota, immutable recipes, operation
  admission, and executor fencing. API examples do not resolve these choices.
- **Schema and authorization:** Define project/operator scope, field writers,
  typed references, immutability, and cross-project IAM. Service catalog delivery
  alone does not provide source-use grants or safe handling of name collisions.
- **Provider qualification:** Choose the initial OS/hardware/backend combinations,
  enrollment trust, evidence freshness, cleanup assurance, and independent
  management deployment. The proposals select no provisioning engine.
- **Fencing and capacity recovery:** Specify how replacement installation and
  joining interact with the prohibition on activating shared identity before
  fencing. Also reconcile a stuck NodeClaim's occupied logical slot with the
  extension's expectation that consumer demand can obtain capacity elsewhere.
- **Node service policy:** Resolve permitted roles, runtime/template privileges,
  region scoping, and the remaining regional DNS writer work.
- **Repository ownership:** Source text uses `milo-os/fleet` in implementation
  examples and leaves subsystem ownership open. This local `fleet` repository is
  the requested starting point; remote organization, module path, license, and
  any provider repository split still need to be chosen.

The [review of #7705][node-review] requested clarity on readiness, regional rollout,
fencing, and procurement policy. Its latest text addresses those points with the
rules summarized above. Review comments also report unrelated Flux test failures;
those reports do not establish implementation or deployment readiness for Fleet.

[proposal]: https://github.com/datum-cloud/infra/tree/51781e2456367be82281e1443ca56d0686311133/docs/enhancements/fleet-management
[extension]: https://github.com/datum-cloud/infra/tree/a712d2d3aaddcdc4d81c09d7f4cdd139bef06631/docs/enhancements/fleet-management
[network-comment]: https://github.com/datum-cloud/infra/pull/5782#issuecomment-5838307998
[node-review]: https://github.com/datum-cloud/infra/pull/7705#pullrequestreview-5646192072
