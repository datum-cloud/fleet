---
status: provisional
stage: alpha
---

# Production allocation admission

[Documentation overview](../../README.md) · [Functionality guide](../../functionality/README.md)

- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
- [Design details](#design-details)
  - [What Milo provides today](#what-milo-provides-today)
  - [Caller identity](#caller-identity)
  - [Request authorization](#request-authorization)
  - [Admission fence](#admission-fence)
  - [Quota custody](#quota-custody)
  - [Project lifecycle](#project-lifecycle)
  - [Requests to other teams](#requests-to-other-teams)
  - [Implementation decisions](#implementation-decisions)
  - [Acceptance tests](#acceptance-tests)
- [Production readiness review questionnaire](#production-readiness-review-questionnaire)
- [Implementation history](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure needed](#infrastructure-needed)

## Summary

Fleet allocates machines today only in a synthetic mode. In that mode, Fleet
writes an admission decision itself instead of checking the customer's access,
entitlement, and quota. The [machine
lifecycle](../machine-lifecycle/README.md#implementation-decisions) needs three
things from Milo before production allocation can run:

- the identity of the caller who created a Machine, recorded so that a
  controller can trust it;
- an admission fence that orders allocation against revoked access and
  suspended or deleted projects and entitlements; and
- a quota hold that Fleet keeps until it releases the hardware.

This enhancement records what Milo supports today and designs the rest. Milo
already lets Fleet record the caller, check the caller's access when the
Machine is created, and hold quota with a finalizer. The fence, fresh access
reviews, protected finalizers, an immutable quota claim, and a purge guard
need changes owned by other teams. They are listed as [requests to other
teams](#requests-to-other-teams).

This enhancement is a proposal. Nothing in it is implemented.

## Motivation

Synthetic admission accepts every Machine that matches a fixed shape. It cannot
tell whether the caller may use a class, whether the project is entitled or
suspended, or whether the project has quota. A production allocation must
answer all three, and must keep answering them correctly when access is
revoked while a request is in progress.

### Goals

- Fleet acts only for the caller who created the Machine, never with its own
  authority.
- A Machine binds only if access, entitlement, and project state still hold
  when the allocation commits.
- Quota stays held until the hardware is released, even if the Machine, its
  namespace, or its project is deleted first.
- Each requirement uses one-object conditional updates and deterministic names,
  with no database.

### Non-goals

- Changing the custody, operation, or release contracts of [Device
  custody](../device-operations/README.md).
- Pricing, quota limits, or entitlement policy.
- Authorization for installations and power changes. They follow the same
  pattern and are designed with their operations.

## Proposal

A Machine create passes through Fleet's admission webhooks in Milo's root API
server. They record the caller in the Machine and check the caller's
permissions. The allocator later commits the allocation only if the fences of
the consumer and provider projects have not moved since it checked admission.
Fleet holds the Machine's quota claim with its own finalizer until release
closes the quota gate.

![Machine admission from create through a fenced commit](diagrams/admission-flow.svg)

## Design details

### What Milo provides today

The table records Milo's behavior at its current main branch. "Partial" means
Fleet can use the capability, but a gap remains that only Milo can close.

| Capability | Milo today | Gap |
| --- | --- | --- |
| Webhooks see the real caller | Exists. Project requests run the root server's authentication, authorization, and admission chain, and webhooks registered at the root see them. | None. |
| Trusted project context | Exists. Milo overwrites the caller's project context on every project request. | Root requests outside a project keep whatever context the caller supplied. |
| Creator recording | Partial. Some Milo kinds stamp a creator in their own webhooks. Nothing generic exists. | Fleet must record the creator itself. |
| Access review for a retained caller | Partial. Access reviews accept a named user and go through the normal authorizer. | Milo sets no limit on how long the authorizer may cache an allowed decision. No revision ties a decision to a point in time. |
| Project suspension | Partial. Suspension refuses creates and updates in the project's API. | It takes effect asynchronously, with no point at which it is known to be in force. |
| Project deletion | Partial. Purge honors finalizers on objects and namespaces. | A deleting project still serves its API and admits new objects. Purge deletes every object at once, including credentials and quota claims, and a finalizer on the Project does not delay it. |
| Invalidation acknowledgment | Missing. | No fence, epoch, or acknowledgment exists for access, entitlement, or project changes. |
| Quota claim lifecycle | Partial. A granted claim is never revoked. A deleting claim stays counted while a finalizer holds it. | Deleting a claim frees quota at once. Removing a request or changing the consumer after the grant also frees it. |
| Protected finalizers | Missing. | Anyone who can update an object can remove a service's finalizer. |

ServiceEntitlement is owned by the service catalog, not Milo. It has no fence
or acknowledgment either.

### Caller identity

The Machine gains a server-set `spec.creator` with the caller's username and
UID. Fleet's mutating webhook sets it on create from the authenticated
request, replacing any value the client sent. The same webhook adds Fleet's
Machine protection finalizer, so the caller and the finalizer are recorded in
the same write that creates the Machine.

Fleet's validating webhook then refuses a create whose creator differs from
the caller or that lacks the finalizer, and any change to the creator. A
schema rule also refuses adding, changing, or removing the creator after
create. Both webhooks fail closed and refuse a request without a Milo project
context.

Device operations use a validating webhook only, because Fleet's own services
create them and know their identity. Customers create Machines, and a client
cannot be expected to send its own UID, so the Machine webhook sets it.

The controller trusts the creator because only the webhooks could have set it.
A Machine without a creator was created before the webhooks were registered,
or while they were bypassed. Fleet does not allocate it and reports
`CreatorUnknown`.

Fleet retains the username and UID, never a token or group list.

Fleet names a tenant only with a typed consumer reference, such as
`{apiGroup: resourcemanager.miloapis.com, kind: Project, name: consumer-a}`,
and never with a bare project field. The tenant is a Milo Project, or a
Namespace in standalone mode, and admission refuses any other kind. Fleet's
single tenancy lookup resolves the reference to the tenant's identity, quota
consumer, and API client. The webhook and every later step use it rather than
resolving a project themselves.

### Request authorization

The validating webhook checks the caller's permissions with access reviews
while the caller is still on the request:

- `create` on Machines in the consumer namespace;
- `use` on the MachineClass in the provider project; and
- `use` on the MachineProfile, and on its provider source when the profile is
  published.

The review carries the caller's full identity, groups included. A
cross-project check keeps the caller and changes only the project named in the
review. A denied create fails immediately, so the customer sees the error. A
review Fleet cannot complete also refuses the create.

The webhook's decision only gates the create, so a cached answer there is
acceptable. The allocator repeats the same reviews for the retained creator
when it [commits the allocation](#admission-fence). Those reviews must not be
cached, and must resolve the creator's current groups from the username and
UID. Milo does not offer that yet ([Milo 5](#milo-5-fresh-access-reviews-for-a-retained-caller)),
so production commits wait for it.

A caller who loses access before the commit gets no machine. A caller who
loses access after the commit keeps the machine, as the [active-allocation
contract](../machine-lifecycle/README.md#request-authority) requires.

### Admission fence

Each project has one fence with a generation. An authority that can remove
access raises the generation first, then waits for every participating
service to acknowledge it. Revoked access, a suspended entitlement, and a
suspended or deleted project raise it. Changes that only grant access do not.
The fence of the provider project also covers provider retirement.

Each MachineAllocation is named from its Machine, as Device custody already
requires. It names its tenant with the consumer reference and records the
generation of the consumer fence and of the provider fence. Standalone mode
runs without Milo, so it has neither fence, and the fence reads and checks in
steps 1, 2, and 4 do not apply there. The allocator commits a Machine in this
order:

1. Read both fences from the API server, not a cache.
2. Check admission for the retained creator with fresh access reviews, an
   Active entitlement that is not suspended, and consumer and provider
   projects that are neither suspended nor deleting.
3. Create the MachineAllocation as `Pending` with both generations.
4. Read both fences again. If either moved, delete the allocation and start
   again.
5. Change the allocation from `Pending` to `Admitted` with a conditional
   update. This is the commit.
6. Claim Device custody once, with a conditional update on the custody
   version it read. If custody refuses the claim, delete the allocation and
   start again from step 1. No effect ran under it.

Every delete in these steps carries the allocation's UID and resource version
as preconditions, so a delayed worker cannot delete a newer allocation with the
same name. A recreated allocation reuses the name, because the deleted one
never held a Device.

When a fence moves, Fleet's fence controller lists, from the API server, every
`Pending` allocation that records that fence: by consumer reference for a
consumer fence, across every provider namespace that holds allocations, and by
provider Project for a provider fence. It deletes each one recorded at an
older generation, with the same preconditions. It acknowledges the new
generation only after a complete list contains no such allocation.

The allocation record is the single object that both sides change, so one
conditional write decides each race:

- **Invalidation wins:** the fence controller deleted the `Pending`
  allocation. The Machine does not bind. The allocator checks admission again
  at the new generation, and refuses the Machine if access is gone.
- **Allocation wins:** the allocation became `Admitted` first. It committed
  before the acknowledgment, and the active-allocation contract applies.

If the claim's result is uncertain, Fleet reads custody from the API server
and never sends the claim again:

- Custody names the allocation: the claim succeeded.
- Custody is at a later version and does not name it: the claim can no longer
  apply, because its version precondition failed. Fleet deletes the
  allocation and starts again from step 1.
- Custody is still at the claimed version: the result stays unknown. The
  Machine reports `Bound=Unknown`, and Fleet reads again.

An allocation created after the fence moved finds the new generation in step 4.
An acknowledgment therefore means that no admission checked at an older
generation can still commit. Step 6 claims once, so an `Admitted` allocation
cannot wait for a Device and claim it long after an acknowledgment. Only a
claim already sent can still land, and it was committed before the
acknowledgment.

The machine lifecycle's [admission
ordering](../machine-lifecycle/README.md#admission-ordering) lists more inputs
than the fence covers:

- **Quota** needs no fence. Milo never revokes a granted claim, and Fleet holds
  the claim until release.
- **Class and profile policy and capacity eligibility** are Fleet's own state,
  and step 2 checks them. A withdrawal has no acknowledgment, so the
  lifecycle's guarantee that invalidation wins does not hold for them: a
  commit that races a withdrawal may still win.

### Quota custody

Fleet creates one ResourceClaim per Machine, named from the Machine UID within
its project, after the access and project checks pass. The claim's consumer is
the allocation's consumer reference. The claim carries Fleet's quota custody
finalizer from the moment it is created. Milo keeps counting a claim while a
finalizer holds it, so deleting the claim, or purging its namespace, does not
free quota early.

Fleet releases the claim when release closes the quota gate after cleaning, or
when a Machine is refused or deleted before it holds an allocation:

1. Delete the claim, if it is not already deleting.
2. Remove Fleet's finalizer with a conditional update.
3. Close the gate, or remove the Machine's finalizer, once the claim is gone.

The finalizer is the custody anchor, and its removal is the release
acknowledgment: quota is free only after Fleet removes it. A claim that is
missing while its allocation is active means someone removed Fleet's
finalizer. Fleet reports `QuotaCustodyLost` and keeps the allocation. It never
reclaims a customer's machine because quota accounting failed.

Until Milo closes two gaps, the hold is weaker. Anyone who can update the
claim can remove Fleet's finalizer, and removing a request or changing the
consumer frees quota without a delete. Fleet compares every claim it reads
with the claim it would create, and reports a mismatch.

### Project lifecycle

**Consumer project deletion.** Purge deletes every Machine and quota claim at
once. Their finalizers hold them, and purge waits for the namespaces to empty.
Today Fleet stops watching a project once its service consumer record is
deleting or inactive, so nothing would remove those finalizers and purge would
wait forever. Fleet must stay engaged with every consumer project in which it
holds a Machine or claim finalizer, whatever the state of that record, and
release each allocation through the provider project.

**Provider retirement.** Purge deletes custody, operations, credentials, and
access grants at the same time. Cleanup can lose the access it needs before
obligations settle. Fleet adds a finalizer to the provider Project and removes
it only after the [retirement
guard](../machine-lifecycle/README.md#provider-project-retirement) settles.
Purge must not start while that finalizer remains.

**Suspension and deletion.** Both raise the fence. Fleet's fence controller
stops pending allocations before it acknowledges.

### Requests to other teams

Each request is written so that its owner can file it as an issue. Changing
another team's repository is a decision for that team.

#### Milo 1: Protected service finalizers

**Problem.** Anyone who can update an object can remove a finalizer that a
service added. A service that holds an object until its work settles cannot
rely on the hold.

**Request.** A service registers the finalizer names it owns. Milo refuses to
add or remove a registered finalizer, in project and root requests, unless the
caller is that service's principal. Deleting the object stays allowed.

**Acceptance criteria.**

- A project owner cannot remove a registered finalizer from a Machine, a
  ResourceClaim, a namespace, or the Project.
- The owning service's principal can add and remove its own finalizers.
- Purge does not remove a registered finalizer.
- Unregistered finalizers behave as they do today.

#### Milo 2: ResourceClaim spec immutability

**Problem.** Removing a request from a claim, or changing its consumer, after
the grant frees quota without a delete. Fleet cannot tell whether its hold
still exists.

**Request.** Make a ResourceClaim's spec immutable after create. Keep counting
a claim while it is deleting and a finalizer holds it, as Milo does today.

**Acceptance criteria.**

- An update that changes a claim's consumer, resource reference, or requests is
  refused.
- A deleting claim held by a finalizer stays counted in its allowance bucket
  until the finalizer is removed.
- Both behaviors have tests.

#### Milo 3: Project purge guard

**Problem.** Purge starts as soon as a Project is deleted and deletes every
object at once. A provider can lose its credentials and access grants before
it finishes cleaning hardware that customers used.

**Request.** Do not start purge while the Project carries a registered service
finalizer. Report each finalizer that blocks purge in the Project's status.

**Acceptance criteria.**

- A deleted Project with a registered service finalizer keeps every object,
  credential, and access grant until the finalizer is removed.
- Removing the last service finalizer starts purge as it runs today.
- The Project's status names each blocking service.
- The deleting project refuses new objects while purge waits.

#### Milo 4: Per-project admission fence

**Problem.** Nothing orders an allocation against a revocation. A service that
checks access and then commits can commit after the access was removed, and
cannot report when old decisions stop committing.

**Request.** Each project has one fence with a generation that only Milo
raises. Milo raises it whenever access to the project can be removed:

- a PolicyBinding in the project is changed or deleted;
- a Role bound in the project changes;
- a member is removed from a group bound in the project;
- the Project is suspended or deleted; and
- a ServiceEntitlement for the project is suspended or deactivated, as
  reported under [Service catalog 1](#service-catalog-1-entitlement-changes-raise-the-fence).

Milo raises the generation only after the change takes effect in the
authorizer's source of truth. Each participating service writes its own
acknowledgment with the highest generation it has settled. Milo reports a
suspension or deletion as in force only after every registered participant has
acknowledged its generation.

The fence and the acknowledgments must stay writable while the project is
suspended or deleting, either by living outside the project or by an
exemption.

A participant that never acknowledges blocks the report, not the suspension:
the project is suspended as it is today. Milo names the participant in the
status. A platform administrator can remove a participant's registration,
which Milo records with the generation it was removed at.

**Acceptance criteria.**

- Each listed change raises the generation at least once, and nothing
  lowers it.
- An uncached access review ([Milo 5](#milo-5-fresh-access-reviews-for-a-retained-caller))
  evaluated after the generation is raised reflects the change.
- Only Milo can write the fence. Each participant can write only its own
  acknowledgment, including while the project is suspended or deleting.
- A suspension reports in force only after every participant acknowledges, and
  the status names a participant that has not.
- Removing a participant's registration is recorded and unblocks the report.
- A test covers a revocation that races a participant's commit.

#### Milo 5: Fresh access reviews for a retained caller

**Problem.** The authorizer may answer an access review from a cache, so an
allowed answer can outlive a revocation. Fleet checks access for a retained
creator with only a username and UID, so it cannot supply current groups.

**Request.** Offer an access review that bypasses any decision cache and
resolves the user's current group membership from the username and UID. This
may need a change in the authorization provider as well as Milo.

**Acceptance criteria.**

- After a PolicyBinding is deleted and the change takes effect, an uncached
  review for its subject is denied.
- A review naming only a user's name and UID reflects that user's current
  groups.
- Only service principals granted the uncached path can use it.

#### Service catalog 1: Entitlement changes raise the fence

**Problem.** A ServiceEntitlement can be suspended or deactivated while a
service commits work under it, and nothing orders the two.

**Request.** When a ServiceEntitlement for a project is suspended or
deactivated, raise that project's fence from Milo 4 before reporting the
change as effective.

**Acceptance criteria.**

- Suspending or deactivating an entitlement raises the fence of its project.
- The entitlement reports the change as in force only after the fence's
  participants acknowledge.

### Implementation decisions

Record these decisions before production allocation is enabled:

- **Interim fence.** Until Milo provides the fence, Fleet can raise a fence of
  its own when it observes invalidations. Its order is then only as good as
  Fleet's observation. Decide whether production allocation may run on it,
  and with which limit on the delay. Commits still need uncached access
  reviews from Milo 5 on any fence.
- **Unprotected finalizers.** Decide whether production allocation may run
  while customers can remove Fleet's quota and Machine finalizers.
- **Fence location.** Decide where the fence lives and which component owns
  the fence controller.

### Acceptance tests

These tests are required and not implemented:

- A client sends a creator, and the webhook replaces it with the caller.
- A Machine whose tenant is neither a Milo Project nor, in standalone mode, a
  Namespace is refused.
- A Machine's allocation and quota claim both name the Machine's tenant as
  their consumer.
- A client changes, adds, or removes the creator, and admission refuses it.
- A Machine without a creator never binds.
- A caller without class `use` cannot create a Machine.
- Access is revoked between the check and the commit, and the Machine does not
  bind.
- A revocation and a commit race on one allocation, and exactly one wins.
- A consumer fence moves, and Fleet deletes that consumer's pending
  allocations in every provider namespace before it acknowledges.
- A provider fence moves, and Fleet deletes every consumer's pending
  allocations in that provider before it acknowledges.
- A delayed worker deletes an allocation by name after it was recreated, and
  the newer allocation survives.
- A custody claim's response is lost, and the allocator reads custody instead
  of deleting the allocation. It deletes the allocation only once custody has
  moved past the claimed version without naming it.
- An admitted allocation fails to claim its Device, and the allocator checks
  admission again.
- A Machine is refused, or deleted before allocation, and its claim is
  released.
- A Machine's namespace is purged during cleaning, and quota stays held until
  the quota gate closes.
- A consumer's service consumer record is deleted while Fleet holds claims,
  and Fleet still releases them.
- A quota claim's finalizer is removed while its allocation is active, and
  Fleet reports `QuotaCustodyLost` without reclaiming the machine.
- A provider project is deleted while an allocation is active, and purge waits
  for release.

## Production readiness review questionnaire

These controls are proposed, not implemented.

### How do operators enable or disable the feature?

Register the webhooks, then enable production admission in the manager.
Disabling it stops new commits. Committed allocations, releases, and quota
custody continue.

### Does enabling the feature change any default behavior?

Machine creates gain a creator and a finalizer, and creates by callers without
the required permissions fail. Synthetic admission remains a separate mode.

### What must operators monitor?

Fence generations that Fleet has not acknowledged, `CreatorUnknown` Machines,
`QuotaCustodyLost` reports, and failed access reviews.

### What does the feature depend on?

Milo's root API server for webhooks, access reviews, quota, and project
lifecycle, and the service catalog for entitlements.

### Does the feature add API types or calls?

It adds the Machine creator, fence generations on MachineAllocation, two
webhooks, and one access review per permission for each create and each
commit.

## Implementation history

- 2026-11-25: Proposed.

## Drawbacks

- Every Machine create depends on Fleet's webhooks being available.
- Each commit repeats the access reviews, which adds latency to allocation.
- A conservative fence raises the generation for changes that did not affect
  the caller, and those Machines are checked again.

## Alternatives

- **Capture the caller in a controller:** a watch event does not carry the
  caller, so a controller cannot recover who created the object.
- **Trust an annotation the client sets:** any caller could name someone else.
- **A Fleet database of decisions:** it adds a database. The allocation record
  and the fence give the same order with one conditional write.
- **Milo-created quota claims through a claim creation policy:** Milo creates
  the claim before Fleet admits the Machine, and garbage-collects it with the
  Machine, before cleaning ends.

## Infrastructure needed

Testing needs Milo with the root webhooks registered, one provider project,
two consumer projects, quota grants, and entitlements. Race tests need a way
to pause the allocator between its fence reads and its commit.
