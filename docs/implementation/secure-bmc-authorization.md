# Secure BMC production authorization plan

Status: proposed implementation contract, 2026-11-12. This plan expands the
production authorization work for Secure BMC Access. It does not enable production
admission or claim live Milo acceptance.

Read with the [Secure BMC proposal](../enhancements/secure-bmc-access.md),
[prototype status](machine-management-prototype.md), [API architecture](../architecture.md),
and [pinned provisioning proposal](../proposals/fleet-management/bare-metal-provisioning.md).

## Current implementation and gap

`internal/controller/admission.go` copies the admission request username into a
service-owned annotation and makes the session spec immutable. Kubernetes RBAC
separates session creation from Machine reads. This is useful prototype evidence,
but a username is not a durable Milo authorization receipt, and the asynchronous
controller cannot reconstruct the original caller's authority from it.

`internal/controller/session.go` counts cached bindings to enforce limits. That
check is not an atomic reservation across controller replicas. The existing Milo
contracts in `internal/milo/types.go` describe retained authority, admission fencing,
and protected quota custody for machine allocation; their production adapters are
not implemented. `internal/milo/quota.go` is specific to the machine quota resource
and must not be reused unchanged for sessions.

The production admission path must retain the authenticated caller and exact
request, obtain a target-specific decision, reserve limits durably, and commit
against current allocation authority before any usable route is published.

## Permission and audience contract

Use the existing Bare Metal service, `metal.datumapis.com`, and its `metal`
entitlement. Do not create a second BMC catalog service. The following permission
names are proposed additions, subject to Milo integration validation:

| Permission | Scope and purpose |
| --- | --- |
| `machinemanagementsessions.create` | Create a session request in the authenticated consumer project. |
| `machines.manageBMC` | Authorize the requested Provisioning capability against the exact target Machine in that project. |
| `machinemanagementsessions.get/list/watch` | Read safe session metadata and connection status in the project. |
| `machinemanagementsessions.delete` | Request teardown of a session in the project, including one created by another principal. |
| `machinemanagementsessions.updateStatus` | Service-only status publication. |

All names above are prefixed with `metal.datumapis.com/` in IAM registration.
Creation requires both create and target capability permission. A generic create
grant alone cannot authorize arbitrary Machine references. If Milo cannot evaluate
the target-specific permission with the original caller, production stays disabled;
project-wide create must not silently substitute for that decision.

Add an explicit opt-in BMC operator role containing the customer permissions above.
Do not inherit it into the existing viewer, consumer, or provider roles. Existing
Machine read, update, installation, and provider inventory grants confer no BMC
access. Retain project-wide session visibility for this initial role: the creator
is an audit identity, not an implicit owner-only ACL. A separate reader role may
be added later if needed. Customer writes to status, private admission receipts,
bindings, projections, quota custody, or finalizers remain forbidden. There is no
customer update/patch operation for changing an admitted session.

Provider projects supply capabilities and enrollment authority; customers do not
need provider inventory permissions. Customer errors must not disclose whether a
foreign Machine or physical asset exists. Private allocation and Device details
stay out of public responses and IAM-facing customer objects.

## Caller-preserving admission sequence

1. Authenticate through Milo's trusted request path. Resolve the consumer project
   identity and lifetime from that path, not a namespace name, annotation, or
   request field. Preserve the principal's immutable identity and the authenticated
   delegation context needed by the authorizer. Never persist bearer credentials.
2. Validate the immutable request and bind its digest to the target Machine UID,
   consumer project lifetime, namespace UID, client public key, protocol, access
   profile, and requested TTL. Reject ambiguous or unsupported profiles. Check
   current Bare Metal entitlement and provider-published capability constraints.
3. Evaluate create and target capability permissions as that caller. Retain a
   private receipt with decision ID, exact request digest, principal identity,
   policy evidence/version and validity bound supported by the authorizer. A
   controller service-account decision cannot replace a caller decision.
4. Persist a protected admission intent before quota or projection effects. Bind
   it to the actual persisted session UID before creating any route. The request
   admission boundary must provide a trustworthy handoff to the asynchronous
   controller; an editable annotation is insufficient. Admission dry-run performs
   validation only and creates no durable holds or external effects.
5. Resolve the exact current allocation through Fleet's authoritative lifecycle
   custody. Retain Machine, allocation, capacity, Device and project identities
   separately. Obtain the per-machine slot and project quota holds described below.
6. Commit admission under the shared lifecycle fence. Revalidate allocation
   activity, project lifetime, entitlement, capability and receipt validity at the
   commit boundary. A release or identity change wins over an uncommitted session.
   Persist the admitted binding and its hold references before projection.
7. Publish only the existing narrow location projection. Caller credentials and
   central policy receipts never enter Karmada or the gateway. Location execution
   remains subject to current projected authority and the common effect fence.

The trusted request handoff, target-decision API, and policy validity/revocation
signal are explicit Milo dependencies. Select their concrete adapters before
implementing admission; no invented endpoint or unsigned receipt is acceptable.
A webhook crash or API write failure may leave a prepared intent; recovery must
reconcile it against the persisted session UID and settle any known holds. A
missing response alone is not proof that a request or hold was never committed.

## Durable limits and recovery

Start with the prototype's policy defaults of one live session per Machine and
five per consumer project, configurable by service policy. Count pending admitted
sessions as well as connected ones. Session TTL is an independent maximum, not a
quota reservation mechanism.

Use two durable controls: an allocation-scoped exclusive session slot through the
lifecycle custodian, and a Milo quota hold under a distinct proposed resource
`metal.datumapis.com/machinemanagementsessions`. Include consumer project lifetime,
Machine UID, allocation identity and session UID in the private custody record.
Retries for one session recover the same intent and holds. A second session must
not reuse them, even if its name or request body matches.

The two stores need no assumed distributed transaction: prepare the intent,
reserve the machine slot, acquire project quota, then commit under the lifecycle
fence. Never publish a route before both holds and the commit are durable. A
partial failure remains pending or compensates through recorded settlement; it
cannot bypass one limit. A lost quota response is recovered by exact intent
identity, not by creating another quota claim. Protect hold lifetime from customer
deletions and quota claim edits. Cached list counts may be diagnostic only.

Keep both holds until teardown is verified. Neither public session deletion,
terminal status, nor TTL expiry alone proves that a connector or admitted effect
has stopped. A proven never-projected admission may settle without contacting a
gateway; uncertain projection must take the teardown path. Persist teardown and
settlement receipts so restarts can resume without freeing a live slot. Quota
release uncertainty retains custody and reduces availability rather than allowing
oversubscription. Reconciliation must surface stuck holds for operators.

## Revalidation and revocation

A successful create decision is bounded authority, not permanent access. Refresh
requires current project lifetime, entitlement, allocation activity and a valid
caller-policy decision or renewal. Policy revocation, entitlement removal,
allocation release, explicit session deletion or expiry stops refresh and begins
teardown. A new positive decision cannot reactivate a terminal session.

An unavailable authorizer or allocation reader prevents new admission and authority
extension. It is not evidence of a permanent denial: existing sessions remain
bounded by their last valid freshness deadline and enter recovery/unknown handling
where needed. Do not reset an expired deadline using cached success. Document and
measure the revocation bound as the remaining authority freshness interval plus
allowed clock uncertainty; do not promise instantaneous cancellation across a
partition. Already admitted effects require the lifecycle recovery contract and
must still block reuse until their outcome and teardown are known.

## Implementation slices and evidence

| Slice | Changes | Completion evidence |
| --- | --- | --- |
| IAM registration | Session ProtectedResource, Machine capability permission, explicit BMC role, narrow controller grants, separate quota resource under Bare Metal. | Render/packaging checks and role tests prove existing viewer/consumer roles gain no BMC capability. |
| Trusted admission | Milo caller adapter, protected request/decision custody, project-lifetime and target checks, immutable binding. | Original human and service-account decisions succeed; substituted controller identity, forged annotations and foreign targets fail. |
| Limits and fence | Protected project quota, allocation slot, idempotent recovery and lifecycle commit. | Concurrent admissions across two replicas never exceed either limit; crash injection at every partial commit recovers without a second hold or route. |
| Renewal and teardown | Policy renewal, bounded revocation, durable settlement and operator diagnostics. | IAM removal, project recreation, expiry, controller restart and API partition never extend stale authority or free an unverified slot. |
| Live Milo acceptance | Exercise the real project API and IAM/quota adapters with synthetic assets. | Sanitized evidence covers allow, deny, grant removal, boundary isolation and teardown; mock authorizer tests alone do not satisfy this gate. |

Negative tests must include Machine read without session create; create without
Machine capability; wrong project with an identical Machine name; deleted and
recreated project or Machine; forged principal/receipt; changed key/profile/TTL;
replayed receipt for another session; stale entitlement; unavailable policy API;
concurrent release/admission; and customer deletion during uncertain quota or
projection writes. Dry-run must leave no custody or quota records.

Audit records correlate principal, consumer project lifetime, request digest,
policy decision, session, allocation, quota intent, admission commit and teardown.
Keep physical identifiers in private records. Metrics expose admission denial
categories, authorization latency, held slots, unknown quota outcomes and teardown
age without principal or Machine identifiers as high-cardinality labels.

## Dependencies and rollout

The lifecycle integration work supplies authoritative allocation reads, the shared
commit/effect fence and verified teardown barrier. The Linux E2E work supplies
transport evidence; it does not prove production IAM. Implement registration and
contract tests first, then trusted admission and limits, and finally live Milo
acceptance. Keep production fail-closed until all adapters are available and the
acceptance gates pass. Enable an explicit pilot grant and quota only after that;
existing customers receive no new BMC permissions by default.
