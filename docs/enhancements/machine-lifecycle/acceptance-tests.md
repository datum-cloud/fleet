# Acceptance tests

Required validation for the [machine lifecycle contract](README.md).

Implement the following tests through Milo tenancy, IAM, entitlement, and quota.
No implementation tests exist yet.

The end-to-end scenario uses two consumer projects competing for one provider
machine. One claim binds while the other remains pending. The winner installs,
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
- **Consumer copies a named profile or reuses its name:** Require the custom
  path or protected publication provenance; reject UID substitution.
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
  asset:** Commit one binding; reject duplicate supply.
- **Replicas reconcile one claim after lost quota/binding responses:** Recover
  one hold and one machine; repair status from private state.
- **Revocation, deletion, suspension, quota loss, or policy withdrawal races
  binding:** Enforce the defined admission order through real integrations.
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
- **Assignment drift or stale network evidence during installation:** Block
  affected effects and readiness; never replace retained execution inputs.
- **Reinstall, network deletion, or release with a reserved address:** Preserve
  valid assignments through reinstall; verify old-binding detachment and address
  obligations before reuse. Machine release must preserve the shared Network.
- **Initial Installation or network input arrives after the claim:** Resolve
  dependencies without deadlock or premature activation.
- **Reinstalls share a predecessor; inputs change or names recur:** Admit one
  operation and execute only retained identities/content.
- **Executor response disappears or a retired worker returns after reuse:**
  Recover the existing operation; reject stale effects and callbacks.
- **Installation fails after partial effects:** Retain a dirty/unknown
  incarnation; resume recovery or release.
- **Claim, namespace, class, or project disappears during work:** Retain
  authority and obligations; complete cleanup or quarantine.
- **Cleanup or quota-release acknowledgment remains unknown:** Keep the machine
  unavailable.
- **Consumer deletes a completed Installation:** Preserve the OS, binding,
  initial gate, and predecessor identity.
- **Fleet, project API, simulator, or backing store restarts/restores:** Recover
  the same identities; verify fences before resuming effects.

## Lifecycle reference

The tests cover these reservation transitions as well as the required
[release gates](README.md#release-and-project-lifecycle).

```mermaid
stateDiagram-v2
    [*] --> Preparing
    Preparing --> Aborted: preparation and quota settled
    Preparing --> Bound: exclusive commit
    Bound --> Activating: installation and handoff
    Activating --> Active: verified completion
    Bound --> Active: blank powered-off handoff
    Bound --> Releasing: release request
    Activating --> Releasing: safe boundary
    Active --> Releasing: release request
    Releasing --> Quarantined: unresolved obligations
    Quarantined --> Releasing: verified recovery
    Releasing --> CleanupVerified: external obligations complete
    CleanupVerified --> Released: quota release confirmed
    Released --> [*]
```
