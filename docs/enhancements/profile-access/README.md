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
cases, while existing Machine APIs continue to handle allocation and operation.

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

| Resource | Responsibility |
| --- | --- |
| MachineProfile | Immutable hardware requirements. |
| MachineClass | Provider offering: supported profiles, capabilities, and eligible supply. |
| MachineOfferingAssignment (proposed) | Audience and optional profile restrictions for one offering. |
| Machine | Consumer request for an allowed class/profile combination. |

The registered service, such as Bare Metal, is the outer entitlement boundary.
A MachineClass is an offering within that service. Service enablement does not
automatically grant every offering.

Assignments belong to the provider project and have no namespace. Class and
profile names resolve there; consumer project names resolve through Milo.
References must identify provider-owned definitions, not chains of references.
Protected records retain exact object and project identities so a replacement
under the same name cannot inherit access.

The API name and group below are provisional. Platform review must decide whether
to expose a generic service assignment or a Fleet API backed by that platform
contract. There must be one writable source of access policy.

### Assignment API

```yaml
# Proposed API in provider project example-metal.
apiVersion: metal.datumapis.com/v1alpha1
kind: MachineOfferingAssignment
metadata:
  name: standard-default
spec:
  serviceRef: {name: bare-metal}
  machineClassRef: {name: standard}
  profiles:
    mode: Selected
    allowedProfiles:
      - {name: general-8c-v1}
      - {name: general-16c-v1}
  audience:
    entitledProjects: {}
  state: Active
---
apiVersion: metal.datumapis.com/v1alpha1
kind: MachineOfferingAssignment
metadata:
  name: consumer-a-standard-extra
spec:
  serviceRef: {name: bare-metal}
  machineClassRef: {name: standard}
  profiles:
    mode: Selected
    allowedProfiles:
      - {name: general-32c-v1}
  audience:
    projectRef: {name: consumer-a}
  state: Active
```

`serviceRef`, `machineClassRef`, and `audience` are immutable. The service must
belong to the provider and contain the referenced offering; the platform types
for those relationships remain undecided. Exactly one audience field is required:

- `entitledProjects` selects active, unsuspended projects for that service.
- `projectRef` selects one project, which still needs active service entitlement.
  An assignment created before service enablement remains ineligible.

`profiles` and `state` are mutable with resource-version checks to prevent lost
updates. State is `Active` or `Suspended`. Profile modes are:

- `AllSupported`: the default when `profiles` is omitted. Follows current class
  policy and forbids an explicit list. Class additions can therefore expand
  access; previews must show affected assignments.
- `Selected`: requires a nonempty, bounded list of unique provider profiles
  allowed by the class. It excludes custom profiles.

Custom profiles require both an AllSupported assignment and class policy that
permits them. Existing validation and local profile-use checks still apply;
custom profiles need no canonical provider-profile grant. Copying a named
profile into a local custom profile cannot bypass a Selected assignment.

### Effective access and changes

For each class, combine all applicable assignments, then intersect the result
with current class policy, service entitlement, and caller permissions. Grants
are additive: removing one leaves access supplied by another. Evaluate class
and profile together; access through one offering cannot authorize another.

Changing restrictions stages a candidate revision. Invalid changes leave the
previous effective revision in place, still subject to current source policy.
Suspension or deletion overrides staged changes. Removing a profile from class
policy blocks it even if an older assignment still lists it.

Hardware changes require a new immutable profile and class approval. Recreating
a class or consumer project requires a fresh assignment. Recreated profiles need
fresh approval and resolution under the existing lifecycle rules. Pending
Machines keep their exact inputs and never switch to replacement identities.

Revocation blocks new allocations. A request that loses authority before
allocation must be resubmitted after access returns, as required by the
[machine lifecycle contract](../machine-lifecycle/README.md#authorization-and-publication).
Existing allocations retain their contracts and current runtime permission
checks. Catalog withdrawal cannot evict or reinstall a machine or prevent cleanup.

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
| Provider operator | Class/profile intent and assignments, subject to separate management permissions. |
| Service/entitlement authority | Service ownership, project eligibility, and effective assignment membership. |
| IAM | Current caller permissions in authenticated project context. |
| Fleet catalog evaluator | Source validation and publishable class/profile combinations. |
| Existing publisher | Consumer reference specs, profile copies, and protected source mappings. |
| Status reconciler/projector | Assignment status and consumer status respectively. |
| Shared coordinator | Admission, quota, exclusive allocation, and retained operation authority. |

These roles may share a process, but each field has one writer. This enhancement
adds no allocator or hardware execution path. Physical asset, allocation,
installation, and managed-node identities remain separate.

#### Processing flow

1. Validate the assignment, provider ownership, source identities, and audience.
2. Commit its effective revision and durable publication work together, using an
   outbox or equivalent recovery protocol.
3. Reconcile each consumer's combined grants. Create one local class reference
   and one profile copy per distinct source identity. Retain protected mappings
   to the actual local objects; never adopt a conflicting customer object.
4. Report delivery independently for each pair. A new pair cannot allocate
   before its local copies and protected mappings are valid.
5. On a Machine request, retain caller identity and exact inputs. Check current
   assignment access, local and source-use IAM, entitlement, quota, location,
   configuration compatibility, and available capacity before commit.

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

### Validation

Required tests, not yet implemented for assignments:

- Default delivery on service enablement and withdrawal on suspension.
- Shared offerings with independent grants, including overlapping defaults.
- Extra profiles within one class without enabling other profiles or classes.
- AllSupported expansion, Selected restrictions, and custom-copy bypass attempts.
- Forged copies, name collisions, and recreated sources or projects.
- Revocation racing allocation, lost responses, reordered events, and restart.
- Read/list/watch revocation without leaking protected catalog details.
- Invalid updates retaining the previous effective revision; class withdrawal
  overriding it; existing allocations remaining operable and releasable.

### Implementation plan and decisions

First agree on platform placement, service reference types, caller/project IAM,
and the admission fence. Then implement validation and previews without effects,
extend synthetic publication to multiple consumers, and qualify production
authorization before enabling allocation for a small cohort.

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

The gate alone grants nothing. Providers must create assignments. Existing
published objects require an explicit migration; matching names do not authorize
adoption.

#### Can the feature be disabled once it has been enabled (i.e. can we roll back the enablement)?

Disable new admission first. Roll back binaries only if the older version can
read retained records and enforce their restrictions. Otherwise keep admission
paused and deploy a fix.

#### What happens if we reenable the feature if it was previously rolled back?

Replay unfinished work and recheck current permissions and object identities
before allowing new allocations.

#### Are there any tests for feature enablement/disablement?

Not yet. Test mixed gate states, disablement during allocation, revocation while
disabled, and reenablement after source or project replacement.

### Rollout, Upgrade and Rollback Planning


#### How can a rollout or rollback fail? Can it impact already running workloads?

Mixed versions may interpret restrictions differently or lose publication
progress. Require compatible authority, admission, and publisher versions before
activation. Failures may delay operations but must not interrupt running machines.

#### What specific metrics should inform a rollback?

Pause new admission for any unauthorized allocation or revocation-ordering
failure. Investigate rising authority errors, publication backlog, and admission
latency. Numerical thresholds need measurements before rollout.

#### Were upgrade and rollback tested? Was the upgrade->downgrade->upgrade path tested?

No. Version-skew and upgrade/downgrade/upgrade tests must include retained records,
lost responses, and restart during revocation.

#### Is the rollout accompanied by any deprecations and/or removals of features, APIs, fields of API types, flags, etc.?

No API removals are proposed. The migration of existing publication records must
be designed before rollout.

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
remain unset until measured. Correct revocation is a requirement, not a latency
allowance for using withdrawn permission.

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

One proposed MachineOfferingAssignment contract, subject to platform placement
review. Limits per provider and profiles per assignment remain undecided.

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
  No implementation or release approval is claimed.

## Drawbacks

Cross-project access adds coordination and recovery costs. Large default audiences
increase publication work. Unrestricted assignments reduce maintenance but make
class changes affect more consumers; explicit selections require more updates.

## Alternatives

- **Audiences on MachineProfile:** mixes hardware requirements with access policy.
- **One MachineClass per consumer:** duplicates offering policy.
- **Local copies as permission:** cannot enforce timely revocation.
- **Required profile sets:** adds a resource before reuse needs justify it. A
  future convenience set should contain profiles only, with no audience policy.

## Infrastructure Needed

No new repository is needed. Production work requires platform-owner support and
a multi-project environment for concurrency, recovery, and scale tests. Storage
and deployment capacity will follow the decisions above.

[enhancement-template]: https://github.com/datum-cloud/enhancements/blob/b5fe372ca85b50c674a9ce41419ce5210cf33f6d/enhancements/template/README.md
