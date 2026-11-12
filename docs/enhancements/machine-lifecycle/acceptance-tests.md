# Acceptance tests

Required validation for the [machine lifecycle contract](README.md).

Implement the following tests through Milo tenancy, IAM, entitlement, and quota.
No implementation tests exist yet.

The end-to-end scenario uses two consumer projects competing for one provider
machine. One Machine binds while the other remains pending. The winner installs,
reinstalls, and releases; the waiting consumer acquires the machine only after
cleanup. The simulator persists state independently of Fleet. Tests restart each
component, lose responses, and delay old workers to verify recovery.

- **Projects reuse object names in `default`; clients forge context or recreate
  projects/namespaces:** Isolate CRUD, list/watch, caches, and status; reject
  forged context and stale UID work.
- **Consumer admin lacks source use; two local principals have different
  grants:** Deny unauthorized effects and source disclosure, including cached
  summaries and watches.
- **One profile serves multiple classes or projects:** Retain each local and
  canonical identity; keep capacity and authorization scoped to the offering.
- **Public references use names:** Accept scoped names without client-supplied
  UIDs; expose names in status references. Retain resolved UIDs privately and
  reject attempts to supply UIDs in Fleet reference fields.
- **Consumer copies a named profile or reuses its name:** Require the custom
  path or protected publication provenance; reject lifetime substitution.
- **Provider recreates an allowed profile under the same name:** Block use until
  the provider removes and re-adds the entry in separate accepted class revisions,
  or selects a new name. An unrelated class edit cannot renew approval.
- **Custom profiles are disabled, unsupported, or temporarily unverifiable:**
  Report the specific admission result without reserving capacity or purchasing.
- **Profiles require physical cores, per-device GPU memory, or multiple disks:**
  Reject aggregate-only matches, double-counted devices, and missing or stale
  observations. Distinguish supported configurations from available supply.
- **Profile deletion, catalog changes, or hardware drift:** Order pending
  admission against invalidation; retain active requirements, assigned hardware,
  exact execution inputs, and cleanup targets.
- **Inactive entitlement or suspension:** Block new work while preserving
  authorized cleanup.
- **Two projects, overlapping classes, or duplicate enrollment target one
  asset:** Commit one allocation; reject duplicate supply.
- **Replicas reconcile one Machine after lost quota/allocation responses:** Recover
  one hold and one machine; repair status from private state.
- **Revocation, deletion, suspension, quota loss, or policy withdrawal races
  allocation:** Enforce the defined admission order through real integrations.
- **Quota denial or interrupted preparation:** Produce no effects; settle the
  same hold before aborting.
- **Existing project networks, namespace reuse, or a forged foreign reference:**
  Check network-use permission in the referenced namespace, pin exact lifetimes,
  and reject substitution or cross-project access.
- **Multiple interfaces or an unsupported address family:** Report results per
  interface; withhold network readiness until every requested requirement passes.
- **Provider IP without Galactic membership, or preparation without programming:**
  Do not report the interface attached merely because an address or backing
  object exists.
- **Lost network responses:** Recover the same assignments without allocating twice.
- **Evidence expires without a spec change:** Reevaluate from internal freshness
  limits. Report affected machine and network conditions as `Unknown` with
  `EvidenceStale`, and power state as `Unknown`; preserve known blocking results
  as `False`. Withhold readiness that depends on the missing evidence. Keep the
  allocation, completed installation results, and dated hardware snapshot intact.
- **The status projector stops while conditions remain successful:** Block any
  allocation, installation, power change, or release whose required internal
  evidence is stale or unavailable. Condition timestamps and generations cannot
  substitute for these checks. Refresh conditions when projection resumes.
- **Assignment drift or stale network evidence during installation:** Block
  affected effects and readiness; never replace retained execution inputs.
- **Reinstall, network deletion, or release with a reserved address:** Preserve
  valid assignments through reinstall; verify old-allocation detachment and address
  obligations before reuse. Machine release must preserve the shared Network.
- **Consumer lists requested and allocated machines:** Return its Machines with
  pending or bound state and sanitized allocation details, without requiring
  access to provider MachineCapacity resources.
- **Machine omits the initial Installation:** Allow prepared, powered-off handoff
  once the other gates pass; omit current installation status. Admit a later
  first Installation against the same Machine with predecessor `None`.
- **Consumer reinstalls an allocated machine:** Create a new Installation with
  the same Machine name and its current Installation as predecessor. Preserve the allocation
  and `initialInstallationRef`; report disruptive work through Machine readiness
  and the new request's status. Reject edits to an existing Installation's spec.
- **Initial Installation or network input arrives after the Machine:** Resolve
  dependencies without deadlock or premature activation. Admit only the retained
  initial request while its gate remains unsatisfied.
- **Two first installs use `None`, or two reinstalls name the same predecessor:** Admit
  one operation. Consume the precondition and retain exact inputs atomically
  before dispatch; reject the loser with `InstallationPredecessorConflict` even
  when public status has not caught up.
- **Reinstall names a stale or foreign predecessor, or a Machine name is recreated:**
  Reject the precondition with no effects. Internal Machine and allocation identity
  prevent a predecessor from moving to another lifetime. Name possession does
  not bypass permissions.
- **Admission succeeds but its response is lost:** Recover the same operation
  for the same retained request identity without consuming another predecessor.
  Recreating its name cannot inherit that operation.
- **Installation succeeds:** Publish the resulting Installation name as the
  Machine's current installation; subsequent work changes only that reference.
- **Cancellation or failure proves the prior state is unchanged:** Admit another
  successor only after fencing work and verifying no effects. Keep the initial
  handoff gate; do not reset an allocated machine to predecessor `None`.
- **Inputs change or dependency names recur after resolution:** Execute only
  retained identities and content; public names cannot retarget admitted work.
- **Executor response disappears or a retired worker returns after reuse:**
  Recover the existing operation; reject stale effects and callbacks.
- **Installation fails after partial effects:** Retain a dirty/unknown
  incarnation and withhold a usable predecessor; resume recovery or release.
- **Consumer Machine, namespace, class, or project disappears during work:**
  Preserve allocations, operations, and required inputs in the provider project;
  complete cleanup or quarantine without reconstructing authority from status.
- **Provider retirement races preparation, quota acquisition, allocation, or operation
  admission:** Order the transition. Either reject new work or retain its
  obligations before retirement can proceed.
- **Provider project or namespace deletion, record deletion, or finalizer removal
  is requested while obligations remain:** Reject bypass attempts by ordinary
  provider administrators. Keep records, artifacts, credentials, quota anchors,
  cleanup permissions, and the project API available; do not wipe active machines
  merely because retirement was requested.
- **Provider retirement encounters unknown operations, quarantine, or unsettled
  quota:** Keep the deletion guard closed. After authorized release and verified
  cleanup, retire enrollment and execution authority, satisfy history retention,
  and allow purge in the defined order. Exercise the same-project consumer case.
- **Provider API outage or forced storage loss:** Never treat absence as release.
  Restore exact identities and retained state, reconcile external executor and
  quota evidence, and reject stale work before resuming. Unresolved recovery
  blocks effects and deletion.
- **Cleanup or quota-release acknowledgment remains unknown:** Keep the machine
  unavailable.
- **Consumer deletes a completed Installation:** Preserve the OS, allocation,
  initial gate, and predecessor history. Reusing its name cannot replace the
  retained predecessor identity or authorize another operation.
- **Fleet, project API, simulator, or backing store restarts/restores:** Recover
  the same identities and predecessor admission; verify fences before resuming
  effects. Restoring an old public object cannot make it usable again.

## Lifecycle reference

The tests cover these allocation transitions, including later installation and
reinstallation on an active allocation, as well as the required
[release gates](README.md#release-and-project-lifecycle).

```mermaid
stateDiagram-v2
    [*] --> Preparing
    Preparing --> Aborted: preparation and quota settled
    Preparing --> Bound: exclusive commit
    Bound --> Activating: installation and handoff
    Activating --> Active: verified handoff
    Bound --> Active: blank powered-off handoff
    Active --> Activating: admitted install or reinstall
    Bound --> Releasing: release request
    Activating --> Releasing: safe boundary
    Active --> Releasing: release request
    Releasing --> Quarantined: unresolved obligations
    Quarantined --> Releasing: verified recovery
    Releasing --> CleanupVerified: external obligations complete
    CleanupVerified --> Released: quota release confirmed
    Released --> [*]
```
