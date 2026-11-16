# Secure BMC lifecycle custody

Status: implemented library and portable tests; production wiring remains
unavailable. This supplements the [access-path prototype](machine-management-prototype.md)
and does not relax the [proposed product contract](../enhancements/secure-bmc-access.md)
or [pinned provisioning contract](../proposals/fleet-management/bare-metal-provisioning.md).

## Authoritative inputs and writers

`controller.LifecycleSessionAuthority` consumes the same `Coordinator` and private
Secret namespace used by standalone allocation, Milo lifecycle integration, and
synthetic staging. It resolves project-scoped claim custody to one bound,
committed asset. Public Machine status, `MachineAllocation` CRDs, and their
`machine-uid`, `member-cluster`, `location-name`, and `provider-project` annotations
are not allocation authority. No parallel allocation writer is introduced.

The injected `ManagementAccessResolver` must authorize the retained principal for
explicit management access, verify current consumer/provider project lifetimes,
verify the live Machine and namespace lifetimes, and resolve trusted current
capacity/device and location/member observations. Its returned identities must
match retained custody. Refresh repeats resolution; failure terminally revokes
access rather than extending cached authority. Local standalone scope explicitly
uses the namespace lifetime; Milo scope uses canonical project authority and UID.
Missing integrations fail closed.

Supply an authoritative private-record client with resource-version CAS, not a
public API cache interpreted as authority. Inject the same lifecycle authority
into `SessionReconciler.Lifecycle` and `ProjectionReconciler.Lifecycle`. The current
`fleet-controller` executable defaults to production-unavailable readiness. An
explicit [synthetic fixture mode](synthetic-bmc-fixture.md) injects private grants
and real Coordinator custody for test-only runnable integration; it is not a
production fallback. The manager and
staging handler remain allocation owners, and their shared Coordinator release
path includes this barrier. The projector checks custody on its existing bounded
refresh cadence, without allocation CRD watches or cluster-wide Secret access.

## Exclusive effects and identities

New private asset records use schema version 2, pinning Device name and location
alongside capacity UID. Customer Machine UID, capacity UID, physical asset key,
allocation BindingID/epoch, and installation operation identity remain separate.
In the protected binding, `AllocationName` is the asset Secret name and
`AllocationUID` is the allocation BindingID, not a CRD reference. Neither is sent
to the cell or public session.

Admission CAS-writes the complete binding inputs into the asset before publishing
a binding or admitting projection. That same record owns the active installation
operation. One management session holds the asset effect slot, including Redfish
power/boot operations, until verified teardown. This is deliberately conservative
session-wide serialization, not an independent BMC lock. No general Machine power
controller is added; future power executors must use this same authority boundary.

Installation requests mark the session revoking and return `ErrManagementActive`
without installation effects. Release CAS-transitions to `AssetReleasing` and
marks revocation before network/provider cleanup, quota settlement or reuse. New
sessions fail admission. CAS selects one winner among management admission,
installation activation and release; no timeout makes uncertain custody available.

The gateway cancels the stream, drains accepted requests, rejects later requests,
and waits for its runner to exit. The durable journal must show settled successful
effects before cleanup acknowledgment. Pending, unknown, legacy failed effects,
missing session custody or a missing operation directory cannot prove cleanup.
Adapter errors without definitive evidence are Unknown. A different mutation
cannot bypass an unsettled device operation, and queued mutations recheck
authority after acquiring the device lock.

## Recovery and teardown

- A crash between asset CAS and binding creation recovers the exact binding,
  deadlines, client key and correlation IDs, including during release. Recovery
  does not create authority or renew expired deadlines.
- Projection intent is retained before federation Create. A revoked hold with no
  admitted projection can settle using that positive private proof. After intent
  is retained, an absent projection is unknown, not teardown proof.
- The gateway persists a session marker before route registration and a terminal
  revocation marker before cleanup acknowledgment. Replayed Active projections
  cannot restart durably revoked sessions. Control markers use the separate
  `sessions` journal and are not exposed as Redfish tasks.
- The projector accepts cleanup only with a digest of the exact revoked projection
  name/spec, not by comparing unrelated hub and cell generation counters, saving
  confirmation on the binding before completing the hold and deleting federation
  artifacts. Completion is replayable. Old confirmation cannot modify a newer
  allocation/session hold. Public deletion retains its finalizer on timeout.
- Version 1 assets remain readable for lifecycle recovery/release but cannot admit
  BMC sessions. No upgrade from mutable annotations is attempted. Older writers
  reject version 2 rather than silently dropping holds. Drain old sessions and
  upgrade all effect writers before enabling an injected source. Downgrade is not
  a supported active-session migration.

## Validation and production dependencies

Portable tests cover identity binding without allocation CRDs, missing/denied
authorization, project/namespace/Machine/capacity recreation, device changes,
installation/session and release/session races, competing sessions, exact-input
recovery, projection intent, schema compatibility, stale/unknown teardown,
next-epoch reuse, obsolete completion replay, request draining, journal loss,
and unresolved mutation exclusion.

```sh
go test ./internal/controller ./internal/gateway ./internal/redfish ./internal/standalone ./internal/manager ./internal/milo ./internal/staging
go test -race ./internal/controller ./internal/gateway ./internal/redfish
bash hack/validate-doc-links.sh
git diff --check
```

Production caller-preserving authorization, trusted enrollment/observation and
location registration, deployment wiring, and provider-enforced multi-executor
fencing remain absent. Private CAS and gateway tests establish a single qualified
executor contract only, not cross-replica fencing or hardware completion from an
arbitrary Redfish response. Stable protected journal storage is required. Unknown
operations still need qualified recovery evidence; this change does not auto-clear
tasks or redesign quota, nonce persistence, or general idempotency.

Pre-bound-CRD E2E fixtures are not an integration source. The
[synthetic fixture executable](synthetic-bmc-fixture.md#exact-harness-recipe)
provides real allocation/release transitions and explicit resolver wiring. The
separate E2E owner integrates those commands into the harness. No Kind/Docker
environment or `hack/e2e` files were changed or run for this integration repair.
