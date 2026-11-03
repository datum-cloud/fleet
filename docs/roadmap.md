# Implementation roadmap

This proposed sequence follows the [reviewed Fleet designs](design.md). Milestones
are unimplemented. Owned-metal qualification and rented/managed adoption can
progress independently while sharing allocation and installation contracts.

## 1. Define the service contracts

The [Machine lifecycle enhancement](enhancements/machine-lifecycle/README.md) defines the five
public resources, private reservation/operation records, Milo tenancy boundaries,
and recovery acceptance criteria. Its functional target uses a persistent
simulated provider; real hardware qualification remains in stage 3 below.

Record decisions for API scope and field ownership, project identity and IAM,
durable reservation/operation storage, artifact retention, provider fact schemas,
and the initial implementation toolchain and module path. Introduce code and
generation tooling after those choices are explicit.

Acceptance: demonstrate a recoverable exclusive reservation and quota protocol,
including two projects racing for one machine, policy withdrawal at commit,
retries, and project/object recreation. No hardware effects are needed.

## 2. Introduce inventory and observation

Implement physical plans, reservations, trusted registration, and evidence
evaluation. Import existing inventory, provider state, and active allocations in
observation mode. Expose contradictions and unknown facts.

Acceptance: conflicting rack/port claims and wrong-host registration are rejected;
import does not alter power, boot, networking, or an active reservation.

## 3. Qualify provisioning and the standalone lifecycle

Integrate one execution adapter on disposable lab capacity and independent
management infrastructure. Connect class publication, shared allocation, networking,
OS installation, console/power, and release.

Acceptance: a supported non-Talos customer OS completes claim, installation,
network handoff, operation, reinstall, and release without Cluster/NodeClaim or a
mandatory ongoing guest agent. Two consumer projects share provider inventory
without double allocation. Lost responses, stale executors, access revocation,
and incomplete cleaning cannot cause duplicate effects or premature reuse.

## 4. Adopt rented capacity and managed clusters

Normalize servers.com/NetActuate facts, retain existing identities and
reservations, and compare candidate configuration with current outputs. Introduce
managed recipes, membership, consumer pool gates, and observed capabilities.
Transfer each generated output to one Fleet-controlled writer only after parity.

Acceptance: adoption causes no reinstall or silent rename; replacement uses new
claims with proven predecessor fencing; paused-rollout repair preserves each
node's admitted revision; procurement follows operator policy.

## 5. Pilot node services and regional independence

Observe existing Private DNS deployments. Introduce a resolver NodeService as a
third producer behind a new frontend in one staging cell, then enable readiness
gating and migrate the existing frontends. Coordinate runtime class installation
through OS configuration before retiring the old per-host wiring.

Acceptance: new nodes wait for every gated slot, a single failed slot degrades
without evicting tenants, rolls preserve a serving slot, drains preserve service
until tenants leave, and replacement never activates unfenced shared addresses.
Before expanding to another region, prove independent broker storage and writers
allow it to publish and serve during another region's outage.

## 6. Expand through recovery evidence

Qualify bounded rollouts, provider failure and takeover, backup restoration,
physical maintenance, and another execution integration. Add deployment/release
automation and operational guidance for the qualified implementation.

Acceptance: recovery cannot replay stale authority, discard cleanup obligations,
or allocate quarantined capacity. Wider rollout follows demonstrated lifecycle
and failure handling rather than controller process health alone.
