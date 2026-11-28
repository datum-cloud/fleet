---
status: provisional
stage: alpha
latest-milestone: null
---

# Provider offerings and consumer access

Uses the [Datum enhancement template][enhancement-template]. All new behavior is
proposed. No release milestone is assigned.

- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
- [Design Details](#design-details)
- [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure Needed](#infrastructure-needed)

## Summary

Let providers give consumers default access to machine offerings and grant extra
offerings or profiles to selected projects. A single assignment API supports both
cases. Metal is the public machine service; Fleet is an optional fulfillment
implementation. MachineOffering is canonical provider product policy, including
installation modes and management capabilities. MachineOfferingAssignment
restricts acquisition; MachineClass publishes a one-hop consumer view;
MachineProfile contains immutable hardware requirements. Machine and Installation
remain the consumer request contracts. All of these are proposed design decisions,
not backwards-compatibility or migration requirements for provisional APIs.

## Motivation

Manual catalog copies make it hard to share an offering, explain who can use it,
and withdraw access consistently. Providers need to maintain hardware policy
once and manage consumer access separately.

### Goals

- Share provider offerings across consumers without duplicating definitions.
- Support defaults and additional grants, with optional profile restrictions.
- Show whether access is effective and whether catalog delivery is complete.

### Non-Goals

Procurement, pricing, reserved capacity, third-party onboarding, consumer
re-sharing, and changes to running machines are outside scope. This proposal adds
no networking policy, negative exceptions to defaults, or required profile sets.

## Proposal

A consumer is a project, including one used by a managed platform. Providers
assign offerings either to all projects entitled to their service or to one
specific project. Sharing an offering uses separate assignments for each project.

### User Stories

A provider offers general-purpose machines with small, medium, and large profiles.
It makes small and medium available by default, grants large to one consumer,
and shares an accelerated offering with two selected consumers. Each additional
grant can be withdrawn independently.

The provider can preview affected projects and profile changes, apply the change,
and inspect delivery failures. Consumers discover available combinations in their
own catalog and submit a Machine using local class and profile names. They see
requirements, permitted locations, and availability with its observation time.
Unknown availability is distinct from exhausted capacity.

### Notes/Constraints/Caveats

Service entitlement, project access, and caller permission are separate checks.
Catalog presence neither reserves capacity nor proves permission. Consumers do
not see other projects' assignments or private provider inventory.

### Risks and Mitigations

The main risks are unintended access expansion, stale grants, and catalog leaks.
The rules below address them through explicit restriction modes, ordered
revocation, and permission-filtered reads. Service-catalog and IAM owners must
review authorization; provider and consumer experience owners must review access
previews and diagnostics. Reviewers have not yet been assigned.

## Design Details

### Resource model

| API group | Resources and responsibility |
| --- | --- |
| `metal.datumapis.com` | MachineProfile defines immutable hardware requirements; MachineClass is a one-hop consumer view; Machine and Installation carry requests; MachineManagementSession is the public allocation-scoped BMC session. |
| `provider.metal.datumapis.com` (proposed) | MachineOffering defines canonical product, installation and management policy; MachineOfferingAssignment restricts acquisition audience and profiles. |
| `fleet.datumapis.com` | Physical inventory for providers using Fleet. |
| `coordination.fleet.datumapis.com` | Fleet's admitted execution handoffs to locations. |

MachineProfile stays in Metal, using one immutable requirements schema for
published and custom profiles. API placement does not determine who can author
an object: providers may maintain source profiles in their project, while the
publisher owns protected consumer copies. Consumers can create custom profiles
where offering policy permits them. No provider-specific profile type is added.

The registered Metal service is the outer entitlement boundary. Service
enablement does not automatically grant every offering. Provider offerings and
assignments are project-scoped, without namespaces. Offering and profile names
resolve in that provider project; consumer project names resolve through Milo.
Protected records retain exact identities so name reuse cannot inherit access.

MachineClass and MachineProfile are also project-wide, without Kubernetes
namespaces. This intentionally differs from namespaced Machine, Installation
and public session requests, Fleet physical inventory (for example `infra-ops`),
and protected hub/cell coordination. Namespace translation retains exact project
and object incarnations; a namespace is not an acquisition grant. See the
[Fleet scope/writer contract](https://github.com/datum-cloud/fleet/pull/51).

### Provider offering

MachineOffering describes supported Metal profiles, locations, installation
options, and operations such as power or console access. It describes the
provider's service promise; inventory selectors, credentials, and provisioning
configuration belong to the provider's implementation.

A consumer MachineClass becomes a publisher-managed, one-hop reference to a
MachineOffering in the provider API. Its status contains the permitted summary.
The consumer still selects a local class and profile when creating a Machine.
The offering and MachineClass source-reference schemas need API review against
this contract. MachineClass cannot contain a second writable product definition,
reference another consumer class, or expose private supply selectors. Published
summary changes affect discovery, not an already accepted allocation contract.

#### Installation and management policy

MachineOffering alone declares product-supported installation modes, permitted
image/artifact constraints, delegated-boot constraints, completion assurance and
management capabilities such as power and console/BMC access. Hardware profiles
may constrain architecture, disks or interfaces but do not grant these operations.
Assignments may restrict which profiles a consumer acquires; they neither add
installation modes nor become runtime management policy. MachineClass exposes a
sanitized, publisher-owned view, not an independently editable policy layer.

| Policy surface | Contract |
| --- | --- |
| Managed golden image | Declare supported qualified image/recipe combinations and completion evidence. Admission resolves and retains exact approved artifact identities, versions/digests and target disks; a mutable image name is insufficient. |
| Delegated customer PXE | Explicitly opt in with bounded delegation, permitted endpoint/origin and network constraints, revocation/fencing requirements and stated assurance. Admit one allocation-scoped endpoint and exact inputs; no shared DHCP control or BMC credentials. Customer-reported completion is not platform verification of an opaque OS. |
| Other supported installation sources | Describe their supported contract in offering policy; use the single Installation source union owned by the lifecycle/PXE design, not a second union in profiles, assignments or provider adapters. |
| Management capabilities | Declare supported power, console/BMC and reinstall actions and limits; actual requests also require current actor and input permissions against the retained allocation. Session creation does not authorize arbitrary BMC methods. |

An Installation selects exactly one supported source mode. The
[PXE/lifecycle companion](https://github.com/datum-cloud/fleet/pull/59) owns the
source union and detailed execution/evidence contract. This proposal owns where
product policy lives, not a duplicate Installation schema. Backend recipes,
inventory selectors, management endpoints and provider execution configuration
remain private. Safe policy constraints can be published without publishing
their privileged fulfillment configuration.

Allocation acceptance retains offering identity/revision, accepted policy content
and digest, exact permitted profile identities, installation modes and management
capabilities. Later installation, reinstall and session admissions use that
retained contract, current action/input authorization and explicit safety
restrictions. They do not reread a changed offering as their runtime contract.
Adding a mode does not silently enable it on existing allocations; removing one
does not silently revoke their accepted capability. A changed runtime contract
requires an explicit authorized transition, not catalog reconciliation.

### Provider independence

A provider can fulfill Metal requests using its own infrastructure systems.
For example, servers.com could publish an offering without creating Fleet
inventory or running Fleet provisioning. Fleet is one possible implementation
of the provider fulfillment contract.

That contract must cover reservation, authorized operations, release, duplicate
request handling, and recovery from unknown outcomes. Metal retains consumer
admission authority; providers report fulfillment results against exact requests.
The transport and any reservation or operation resources remain separate design
work. Individual MachineCapacity records are not required by this catalog API;
external supply and reservation must be defined before external fulfillment ships.

The identity boundaries do not depend on implementation: Device is a physical
asset, MachineCapacity is supply, Machine is consumer intent, MachineAllocation
is the exclusive binding, Installation is an incarnation, and NodeClaim sits
above these as managed logical identity. A non-Fleet provider need not publish
Fleet inventory, but must retain an equivalent exact supply/asset binding,
deduplication, operation fencing and cleanup evidence under Metal's authority.
Offering publication cannot itself reserve external capacity or create a second
allocator. It also cannot select a provisioning engine for the provider.

For Fleet fulfillment, management writes admitted hub intent and selected-cell
targeting. The selected cell writes LOCAL execution status only. Karmada reflects
that status to the hub; management validates identity, revision, fencing,
freshness and evidence before public projection. Locations receive no direct hub
or project API credentials. Public BMC sessions stay in `metal.datumapis.com`;
their protected delivery stays in `coordination.fleet.datumapis.com`, as aligned
with [BMC #41](https://github.com/datum-cloud/fleet/pull/41) and
[Fleet management #51](https://github.com/datum-cloud/fleet/pull/51).

### Platform integration

The proposed provider group is part of the Metal service. It introduces no
separate service entitlement. Generic audience and IAM policy stay with their
platform owners, with one writable source of access policy. The exact platform
assignment integration remains open; Fleet coordination is not the source of
consumer grants.

### Assignment API

```yaml
# Proposed API in provider project example-metal.
apiVersion: provider.metal.datumapis.com/v1alpha1
kind: MachineOfferingAssignment
metadata:
  name: standard-default
spec:
  serviceRef:
    name: metal
  machineOfferingRef:
    name: standard
  profiles:
    mode: Selected
    allowedProfiles:
      - name: general-8c-v1
      - name: general-16c-v1
  audience:
    entitledProjects:
      enabled: true
  state: Active
---
apiVersion: provider.metal.datumapis.com/v1alpha1
kind: MachineOfferingAssignment
metadata:
  name: consumer-a-standard-extra
spec:
  serviceRef:
    name: metal
  machineOfferingRef:
    name: standard
  profiles:
    mode: Selected
    allowedProfiles:
      - name: general-32c-v1
  audience:
    projectRef:
      name: consumer-a
  state: Active
```

`serviceRef`, `machineOfferingRef`, and `audience` are immutable. The service must
belong to the provider and contain the referenced offering; the platform types
for those relationships remain undecided. Exactly one audience field is required:

- `entitledProjects` requires `enabled: true` and selects active, unsuspended
  projects for that service.
- `projectRef` selects one project, which still needs active service entitlement.
  An assignment created before service enablement remains ineligible.

`profiles` and `state` are mutable with resource-version checks to prevent lost
updates. State is `Active` or `Suspended`. Profile modes are:

- `AllSupported`: the default when `profiles` is omitted. Follows current offering
  policy and forbids an explicit list. Offering additions can therefore expand
  access; previews must show affected assignments.
- `Selected`: requires a nonempty, bounded list of unique provider profiles
  allowed by the offering. It excludes custom profiles.

Custom profiles require both an AllSupported assignment and offering policy that
permits them. Existing validation and local profile-use checks still apply;
custom profiles need no canonical provider-profile grant. Copying a named
profile into a local custom profile cannot bypass a Selected assignment.

### Effective access and changes

For new acquisition of each offering, combine all applicable assignments, then
intersect the result with current offering policy, service entitlement, and caller permissions. Grants
are additive: removing one leaves access supplied by another. Evaluate offering
and profile together; access through one offering cannot authorize another.

Changing restrictions stages a candidate revision. Invalid changes leave the
previous effective revision in place, still subject to current source policy.
Assignment suspension or deletion overrides staged acquisition changes. Removing
a profile from offering policy blocks new acquisition with it even if an older
assignment still lists it. `state: Suspended` on an assignment is not suspension
of an active allocation.

Hardware changes require a new immutable profile and offering approval. Recreating
an offering or consumer project requires a fresh assignment. Recreated profiles need
fresh approval and resolution under the same identity safeguards. Pending
Machines keep their exact inputs and never switch to replacement identities.

Revocation blocks new allocations. A request that loses authority before
allocation must be resubmitted after access returns, as required by the
[machine lifecycle contract](../machine-lifecycle/README.md#authorization-and-publication).
Existing allocations retain their accepted offering contract and capabilities;
runtime actions require current action/input permissions, not a fresh assignment
or offering acquisition check. Catalog withdrawal cannot evict or reinstall a
machine or prevent cleanup. Ordinary offering edits cannot silently change
accepted capabilities, including adding or removing installation modes.

Explicit allocation suspension and emergency restrictions are separate authority
records, not inferred from a missing assignment. Retain the actor, reason,
affected allocation/actions, policy revision, effective time and expiry/recovery
conditions. Admission applies them to new affected work; restriction of in-flight
effects requires explicit revocation/fencing under the retained operation
protocol. Restrictions never discard custody or prevent authorized service-owned
revocation, fencing and cleanup. Failed or uncertain cancellation remains unknown.

Deleting an assignment retains a service finalizer until revocation and durable
catalog-cleanup responsibility are recorded. Remove local objects only when no
remaining grant needs them. Consumer deletion and provider retirement use the
existing release protocol, preserving obligations outside a disappearing project.

### Status and inspection

Status reports desired and effective restrictions, observed generation, effective
access revision, evaluation time, and bounded delivery counts. Conditions are:

| Condition | Meaning |
| --- | --- |
| Accepted | The desired configuration validates. |
| AccessEffective | Its access revision is committed; this does not imply caller permission or available capacity. |
| Published | Eligible class/profile pairs have valid local copies and protected source records. Report NoEligibleProjects explicitly for an empty audience. |
| Revoked | This assignment contributes no access; other grants may remain. |

Use Unknown when required evidence is unavailable. A paginated, provider-only
`deliveries` view shows per-project progress and conflicts. A proposed read-only
`preview` operation shows affected projects, additions, removals, and known
conflicts. Its result is advisory; writes revalidate current state.

Consumer discovery continues through MachineClass and MachineProfile. Display
whether access is default or additional, without exposing assignment audiences.
Machines require no assignment reference.

### Architecture

| Owner | Writes or decides |
| --- | --- |
| Provider operator | Offering/profile intent and assignments, subject to separate management permissions. |
| Service/entitlement authority | Service ownership, project eligibility, and effective assignment membership. |
| IAM | Current caller permissions in authenticated project context. |
| Metal catalog evaluator | Source validation and publishable class/profile combinations. |
| Existing publisher | Consumer reference specs, profile copies, and protected source mappings. |
| Status reconciler/projector | Assignment status and consumer status respectively. |
| Metal coordinator | Admission, quota, exclusive allocation, and retained operation authority. |

These roles may share a process, but each field has one writer. This enhancement
adds no allocator or hardware execution path. Physical asset, allocation,
installation, and managed-node identities remain separate.

#### Processing flow

1. Validate the assignment, provider ownership, source identities, and audience.
2. Commit its effective revision and durable publication work together, using an
   outbox or equivalent recovery protocol.
3. Reconcile each consumer's combined grants. Create one local reference to the offering
   and one profile copy per distinct source identity. Retain protected mappings
   to the actual local objects; never adopt a conflicting customer object.
4. Report delivery independently for each pair. A new pair cannot allocate
   before its local copies and protected mappings are valid.
5. On a Machine request, retain caller identity and exact inputs. Check current
   assignment access, local and source-use IAM, entitlement, quota, location,
   configuration compatibility, and available capacity before commit. Retain
   the accepted offering contract and capabilities with exact source identities.
6. On an action against an existing allocation, check its retained capabilities,
   current action/input permissions and explicit suspension/emergency restrictions.
   Do not require acquisition access again. Retain the exact operation before
   dispatch; release continues under service-owned cleanup authority.

For installation and BMC actions, current permissions cover the actual actor,
Machine/allocation, requested action, Installation/session and each selected
image, delegated endpoint, network attachment and secret/bootstrap input as
applicable. Publication and an old permission receipt cannot authorize new input
use. Authorization failure blocks the operation, not cleanup ownership. A
previously admitted effect is recovered or explicitly revoked/fenced; rechecking
IAM cannot establish whether that effect already occurred.

Read, list, and watch paths must filter ungranted profiles and class summaries,
including after revocation. A privileged controller cannot substitute its own
permissions for the caller's. Milo's reviewed IAM subjects are principals, not
Projects; project eligibility must be integrated without inventing Project
subjects or granting a multi-project user access in the wrong project.

Revocation and allocation commit need a common ordering mechanism, called an
admission fence in the lifecycle design. If revocation wins, prepared allocation
cannot commit. If allocation wins, its retained lifecycle contract applies.
Cross-API resource versions and cached permission receipts cannot supply that
ordering. Storage and protocol selection remain production blockers.

Persist unfinished operations before effects. Recover uncertain outcomes from
the same records. Replay durable dependency events after restarts, reject stale
revisions, and index offerings to assignments and consumers to limit work.
Revocation takes effect before asynchronous removal of stale catalog copies.

#### Exact authority records

These are logical durable records, not additional mandatory public resources.
The allocation and operation records align with #51; catalog records extend them
without becoming hardware authority.

| Record / sole authoritative writer | Required retained facts |
| --- | --- |
| Effective acquisition revision / entitlement authority | Provider/service and consumer project incarnations, offering and contributing assignment identities/revisions, exact permitted profile identities/revisions, effective revision and allocation/revocation ordering decision |
| Publication provenance / publisher | Exact offering and source-profile identities, consumer project incarnation, actual local class/profile identities, delivery revision and ownership/conflict evidence; no supply selectors or credentials |
| Allocation acceptance / shared allocator | Authenticated consumer/provider project incarnations; Machine, MachineCapacity, Device and MachineAllocation identities (or equivalent protected external supply binding); MachineClass provenance; offering identity/revision and accepted contract content/digest; contributing assignment revisions and acquisition decision; immutable profile identities/digests; accepted installation modes/management capabilities; quota/reservation evidence |
| Operation admission / lifecycle coordinator | Actor, current action/input authorization decision and policy revision/time; allocation and Installation/session identity; exact source mode, artifacts/digests or delegated endpoint identity, network/input revisions and recipe digest; asset and selected cell incarnation; operation ID/revision, predecessor preconditions, fencing epoch, bounded validity and explicit restriction decision |
| Execution attempt / selected executor | Admitted operation ID/revision/digest, allocation/asset/cell identities, fencing epoch, attempt identity and journal-before-effect marker, observation time and attributable results/evidence; unknown is not success |
| Cleanup custody / lifecycle coordinator | Original binding and in-flight work, sessions/capabilities to revoke, required network fencing/detachment and provider cleaning evidence, custody owner and any explicitly authorized transfer |

Retain identities, not just names or mutable references. Records must survive
offering/assignment/public request removal and project retirement. Lost responses
recover the same operation; stale, wrong-cell, wrong-incarnation or regressive
status cannot replace accepted evidence. A delivered handoff has bounded
authority only; it cannot authorize another asset, allocation or new operation.
Public status and audit summaries never expose secret-bearing retained content.

### Validation

Required tests, not yet implemented for assignments:

- Default delivery on service enablement and withdrawal on suspension.
- Shared offerings with independent grants, including overlapping defaults.
- Extra profiles within one offering without enabling other profiles or offerings.
- AllSupported expansion, Selected restrictions, and custom-copy bypass attempts.
- Forged copies, name collisions, and recreated sources or projects.
- Provider Metal offerings publishing Metal profiles without Fleet inventory.
- Protected published profiles and permitted custom profiles using one schema.
- Offering edits preserving accepted allocation contracts without implicit capability changes.
- Revocation racing allocation, lost responses, reordered events, and restart.
- Read/list/watch revocation without leaking protected catalog details.
- Invalid updates retaining the previous effective revision; offering withdrawal
  overriding it; existing allocations remaining operable and releasable.

#### Shared design acceptance scenarios

The same A1–A7 identifiers are defined in #51. These requirements also exercise the
BMC/PXE contracts in #41/#59; no runtime implementation or test result is claimed.

| Scenario | Required result |
| --- | --- |
| A1: two consumers race for overlapping offerings | Exactly one allocation commits for a MachineCapacity/Device; retain accepted product, profile and acquisition evidence before effects; forged/stale classes grant nothing |
| A2: edit or withdraw offering/assignment after binding | New allocations fail current acquisition checks; existing allocations retain accepted modes/capabilities and can perform currently authorized actions without re-acquisition; edits do not silently upgrade or downgrade them |
| A3: revoke action/input permission or impose explicit emergency restriction | Reject new affected operation admissions even when the capability was retained; fence/revoke affected in-flight work through its explicit protocol, preserve unknown outcomes and permit service-owned cleanup |
| A4: golden-image versus delegated PXE request | Select exactly one supported source mode using retained offering policy, immutable hardware profile and current input permissions; retain exact artifacts/endpoint/network inputs; delegated handoff never proves guest OS readiness |
| A5: duplicate delivery, hostile member, partition or recreated identity | Execute only the exact retained asset/allocation/Installation/cell under fencing; write LOCAL status, reflect through Karmada and validate before projection; stale, absent or wrong-member evidence cannot become readiness |
| A6: project deletion, session teardown or uncertain cleanup | Preserve custody and reservations/quarantine through public object and offering removal; release only after required network fencing, BMC/session revocation and provider cleanup evidence; no cross-allocation replay |
| A7: non-Fleet provider and observational adoption | Fulfill the same Metal contracts without exposing Fleet inventory or backend selectors; imported occupied equipment stays unavailable and unchanged until explicit ownership transition |

### Implementation plan and decisions

First agree on the provider API schemas, service reference types, caller/project
IAM, and the admission fence. Then implement validation and previews without effects,
extend synthetic publication to multiple consumers, and qualify production
authorization before enabling allocation for a small cohort.

Design directly for MachineOffering and one-hop consumer MachineClass views.
No compatibility bridge, legacy reference conversion or migration from provisional
provider MachineClass definitions is required. This does not relax exact identity,
active-allocation custody or observational adoption: importing records cannot
authorize provisioning or expose occupied capacity. Fleet-specific supply policy
stays in its private provider implementation.

Other release decisions are naming rules, resource limits, delivery-view storage,
and watch revocation. Do not introduce CRDs until the platform ownership decision
is settled.

### Evidence

Research used Fleet revision `4c7a10c6e4ee9e007086ef0e62eefb328337e75a`:

- [Capacity publication](../../proposals/fleet-management/capacity-publication.md)
  and the [source migration record](../../proposals/fleet-management/MIGRATION.md)
  establish the definition/reference model and its provenance.
- [Lifecycle authorization](../machine-lifecycle/README.md#authorization-and-publication)
  defines source-use checks; its [source review](../machine-lifecycle/source-review.md)
  records the upstream IAM and catalog revisions reviewed.
- [Alpha types](../../../internal/api/v1alpha1/types.go) separate class policy
  from profile requirements. The [synthetic publisher](../../../internal/staging/publisher.go)
  handles one configured pair per consumer. The [Milo adapter](../../../internal/milo/adapter.go)
  accepts injected source authority; [production integrations remain incomplete](../../development/README.md#explicit-gaps).

This proposal has not re-audited current upstream schemas or live deployments.
The historical sources establish provenance, not a requirement to preserve their
provisional API vocabulary. Current design alignment is with #51 (management),
#41 (BMC) and #59 (PXE/lifecycle); their detailed source and delivery schemas stay
with those companions.

## Production Readiness Review Questionnaire

All answers describe proposed requirements. No production readiness, performance,
or upgrade tests have been completed for this feature.

### Feature Enablement and Rollback

#### How can this feature be enabled / disabled in a live cluster?

Use a proposed operator-controlled gate, disabled by default, for assignment
activation and new allocations. Its name and configuration remain undecided.
Revocation, runtime operations, and cleanup must continue while it is disabled.
No workload downtime or node reprovisioning should be needed.

#### Does enabling the feature change any default behavior?

The gate alone grants nothing. Providers must create assignments. Matching names
do not authorize adoption or reuse another object's identity. No legacy catalog
conversion is a prerequisite for this proposed API.

#### Can the feature be disabled once it has been enabled (i.e. can we roll back the enablement)?

Disable new acquisition first while preserving authorized runtime operations,
custody and cleanup. Recovery must honor retained records and restrictions; no
mixed-version or legacy API compatibility bridge is required by this design.

#### What happens if we reenable the feature if it was previously rolled back?

Replay unfinished work and recheck current permissions and object identities
before allowing new allocations.

#### Are there any tests for feature enablement/disablement?

Not yet. Test mixed gate states, disablement during allocation, revocation while
disabled, and reenablement after source or project replacement.

### Rollout, Upgrade and Rollback Planning

#### How can a rollout or rollback fail? Can it impact already running workloads?

Lost publication progress or incorrect admission ordering can delay delivery or
misapply acquisition restrictions. Stop new acquisition on uncertain authority,
recover retained work, and keep runtime/cleanup authority independent of catalog
availability. Failures must not implicitly interrupt running machines. Designing
a mixed-version compatibility protocol is outside this proposal.

#### What specific metrics should inform a rollback?

Pause new admission for any unauthorized allocation or revocation-ordering
failure. Investigate rising authority errors, publication backlog, and admission
latency. Numerical thresholds need measurements before rollout.

#### Were upgrade and rollback tested? Was the upgrade->downgrade->upgrade path tested?

No. Required recovery scenarios cover retained records, lost responses, and
restart during revocation. A legacy upgrade/downgrade compatibility matrix is not
an acceptance requirement for this design-only change.

#### Is the rollout accompanied by any deprecations and/or removals of features, APIs, fields of API types, flags, etc.?

The target design uses MachineOffering rather than a provider MachineClass
definition mode; see the [implementation plan](#implementation-plan-and-decisions).
No migration or compatibility layer is required, and no implemented API is
removed by this documentation change.

### Monitoring Requirements

#### How can an operator determine if the feature is in use by workloads?

Count effective assignments, published pairs, and allocations admitted through
assignments. Retained allocation records identify the relevant access revisions.

#### How can someone using this feature know that it is working for their instance?

Providers use the conditions and delivery view defined under
[Status and inspection](#status-and-inspection). Consumers use their local catalog
and Machine admission status.

#### What are the reasonable SLOs (Service Level Objectives) for the enhancement?

Publication convergence, authorization availability, and admission latency targets
remain unset until measured. Correct acquisition revocation is a requirement, not
a latency allowance for a new allocation using withdrawn permission. Runtime
actions remain governed by their separate retained/current authorization checks.

#### What are the SLIs (Service Level Indicators) an operator can use to determine the health of the service?

Measure activation and revocation latency, oldest pending delivery, authority
errors, replay backlog, and added admission latency. Use bounded reason labels;
keep project and resource names in authorized detail views.

#### Are there any missing metrics that would be useful to have to improve observability of this feature?

All assignment-specific metrics remain to be implemented. Measure completed
revocation separately from removal of stale catalog copies.

### Dependencies

#### Does this feature depend on any specific services running in the cluster?

Milo supplies project identity and routing; service catalog supplies entitlement;
IAM supplies caller permissions. Durable authority storage and project APIs
support the [processing flow](#processing-flow). Slow or unavailable authority
blocks dependent admission; slow consumer APIs delay publication. No guest agent
is required. Supported dependency versions still need qualification.

### Scalability

#### Will enabling / using this feature result in any new API calls?

Yes: assignment writes and previews, dependency watches, catalog writes, and
status updates. A default change can affect every entitled project. Throughput
has not been measured.

#### Will enabling / using this feature result in introducing new API types?

MachineOffering and MachineOfferingAssignment in the proposed provider group.
MachineProfile remains in Metal. Limits per provider and profiles per assignment
remain undecided.

#### Will enabling / using this feature result in any new calls to the cloud provider?

No. Publication does not call hardware or purchasing APIs.

#### Will enabling / using this feature result in increasing size or count of the existing API objects?

Each consumer receives one reference per distinct class and one copy per distinct
source profile identity, shared across overlapping assignments. Private records
grow with assignments and deliveries; allocation records retain access revisions.
Byte sizes need measurement.

#### Will enabling / using this feature result in increasing time taken by any operations covered by existing SLIs/SLOs?

Authorization adds work to Machine admission and permission-filtered discovery.
Measure that overhead against the service's latency budgets before rollout.

#### Will enabling / using this feature result in non-negligible increase of resource usage in any components?

Yes. Watches, audience indexes, delivery queues, and retained records add memory,
storage, and network use. Costs grow with eligible projects and published pairs.

#### Can enabling / using this feature result in resource exhaustion of some node resources (PIDs, sockets, inodes, etc.)?

Large audiences or repeated changes can exhaust queues, memory, or connections.
Bound workers, clients, request sizes, and history. Test broad default updates,
prolonged outages, and event replay before setting supported limits.

### Troubleshooting

#### How does this feature react if the API server is unavailable?

Provider or platform API loss blocks dependent authorization. Consumer API loss
delays delivery. Keep cleanup obligations outside the unavailable consumer API;
an outage does not prove deletion.

#### What are other known failure modes?

| Failure | Detection | Response |
| --- | --- | --- |
| Local name collision | PublicationConflict | Resolve the name or ownership conflict; never force adoption. |
| Delayed activation after revocation | Rejected revision and replay backlog | Resume the retained operation. |
| Invalid restriction update | Accepted=False with an older effective revision | Correct the request or suspend the assignment. |
| Missing permission or changed identity | Authorization or identity error | Restore the dependency or submit freshly authorized intent. |

Log operation IDs, revisions, and reason codes without credentials. Tests for
these cases are required by [Validation](#validation); none has run for this API.

#### What steps should be taken if SLOs are not being met to determine the problem?

Compare effective revisions, dependency errors, and delivery backlog to locate
the delay. Pause new activation if authorization is uncertain. Preserve retained
records and keep revocation and cleanup running during recovery.

## Implementation History

- 2026-11-12: Initial research and design draft.
- 2026-11-13: Reframed access around offerings and adopted the Datum template.
  Separated provider offerings from consumer profiles and made Fleet optional
  for fulfillment. No implementation or release approval is claimed.
- 2026-11-14: Aligned installation policy, retained runtime authority, federation
  status and shared acceptance with #51/#41/#59; removed migration requirements.

## Drawbacks

Cross-project access adds coordination and recovery costs. Large default audiences
increase publication work. Unrestricted assignments reduce maintenance but make
offering changes affect more consumers; explicit selections require more updates.

## Alternatives

- **Audiences on MachineProfile:** mixes hardware requirements with access policy.
- **Consumer classes containing provider policy:** duplicates the offering
  definition instead of referencing it.
- **Local copies as permission:** cannot enforce timely revocation.
- **Required profile sets:** adds a resource before reuse needs justify it. A
  future convenience set should contain profiles only, with no audience policy.

## Infrastructure Needed

No new repository is needed. Production work requires platform-owner support and
a multi-project environment for concurrency, recovery, and scale tests. Storage
and deployment capacity will follow the decisions above.

[enhancement-template]: https://github.com/datum-cloud/enhancements/blob/b5fe372ca85b50c674a9ce41419ce5210cf33f6d/enhancements/template/README.md
