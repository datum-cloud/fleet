---
status: provisional
stage: alpha
---

# Staff power operations on physical Devices

[Documentation overview](../../README.md) · [Architecture guide](../../architecture/README.md)

- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-goals](#non-goals)
- [What exists today](#what-exists-today)
- [Proposal](#proposal)
  - [User stories](#user-stories)
  - [Why a separate resource](#why-a-separate-resource)
- [Design details](#design-details)
  - [API](#api)
  - [Admission](#admission)
  - [Acceptance checks](#acceptance-checks)
  - [Dispatch](#dispatch)
  - [Worker](#worker)
  - [Outcomes](#outcomes)
  - [Failure handling](#failure-handling)
  - [Identities and permissions](#identities-and-permissions)
  - [Redfish client and discovery changes](#redfish-client-and-discovery-changes)
  - [Relationship to Machine power](#relationship-to-machine-power)
  - [Staff UI](#staff-ui)
  - [Security boundaries](#security-boundaries)
- [Delivery phases](#delivery-phases)
- [Acceptance tests](#acceptance-tests)
- [Production readiness review questionnaire](#production-readiness-review-questionnaire)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Open design questions](#open-design-questions)
- [Infrastructure needed](#infrastructure-needed)

## Summary

Staff need to turn an unallocated physical server on or off, or reset it,
without logging in to its BMC by hand. This proposal adds a one-shot
`DevicePowerOperation` resource in `fleet.datumapis.com`. A staff member
creates one to request a single Redfish `ComputerSystem.Reset` on one Device. A
dispatcher in the cell that can reach the BMC checks the request and starts one
worker Job for it. The worker holds only that Device's BMC credential. It
re-checks the system identity, sends the reset once, watches the power state,
and reports a result: `Succeeded`, `Failed` or `Unknown`.

Operations are refused while the Device backs capacity. Power on an allocated
machine stays with `Machine.spec.powerState` from the
[machine lifecycle enhancement](../machine-lifecycle/README.md). That path can
later reuse the same worker.

Everything in this document is proposed. Nothing described under
[Design details](#design-details) is implemented.

## Motivation

Bringing up and maintaining hardware needs basic power control: power on a
newly racked server so discovery can read its inventory, power off a server
before moving it, or reset one that hangs during qualification. Today staff do
this in the BMC's own web interface or with ad hoc tools. That has four
problems:

- Fleet has no record of who changed power on which asset, or when.
- Staff use BMC credentials directly, outside the per-Device credential
  Secrets and TLS pins that discovery already relies on.
- Nothing stops a reset of a server that a customer is using.
- Nothing ties the action to the exact hardware staff looked at. A swapped or
  renumbered system behind the same BMC address gets the reset instead.

### Goals

- Let authorized staff request `On`, `ForceOff`, `ForceRestart` and
  `PowerCycle` on an unallocated Device, and see the result.
- Send at most one reset per request, to the exact Redfish system staff named.
- Report a lost or unprovable outcome as `Unknown`, never as success, and never
  retry it automatically.
- Keep BMC credentials in the cell, readable only by discovery and by the one
  worker of each operation.
- Record who asked, why, and the power state observed before and after.
- Build an executor that the Machine power path can reuse.

### Non-goals

- Power on allocated or capacity-backed Devices. That belongs to the Machine
  and its allocation authority.
- Persistent power intent for Devices. A Device has no desired power state.
- Graceful shutdown or restart, `Nmi` and `PushPowerButton`. See
  [open questions](#open-design-questions).
- Boot source overrides, virtual media, BMC settings or firmware.
- Power distribution units or `PowerFeed` control.
- A general BMC access broker for staff.

## What exists today

These pieces exist on the integration branch `feat/manual-inventory-records`,
which is ahead of `main`. They are not yet on `main`. Links below point to
that branch at commit
[`7b97c6b`](https://github.com/datum-cloud/fleet/tree/7b97c6b2f5c61858a301de0ebb4e0cc165ab37cb).

| Piece | Where | What it does |
| --- | --- | --- |
| Redfish client | `internal/redfish/client.go` | `Discover` walks the Systems collection with `GET` only and requires `On`, `ForceOff` and the `Pxe` and `Hdd` boot targets. It takes an installation enrollment `Target`. `Reset` sends exactly one `ComputerSystem.Reset` `POST`, never retries, and does not treat HTTP success as proof of the effect. `Observe` re-reads the System and re-checks its Id and UUID. |
| TLS pinning | `internal/redfish/pin.go` | `NewPinnedClient` accepts a BMC only when its leaf public key hashes to the approved pin, on every connection, before any credential is sent. |
| Installation provider | `internal/redfish/provider.go` | Runs `ForceOff`, a one-time boot override, then `On`, with a journal. It observes after a lost `ForceOff`, because power off before a boot is idempotent. It repeats `On` only when the target is still off and still armed after a settle window, within a budget of three. It reads one shared credential and takes no TLS pin. |
| BMC discovery | `internal/bmcdiscovery`, [BMC discovery guide](https://github.com/datum-cloud/fleet/blob/7b97c6b2f5c61858a301de0ebb4e0cc165ab37cb/docs/operations/bmc-discovery.md) | A single cell-side replica. It reads one credential Secret per Device (username, password, TLS pin), reads the inventory with `GET` only, and writes `Device.status`. It never changes power, boot or BMC settings. |
| Device API | `internal/api/inventory/v1alpha1/types.go` | `Device.status.discovery.inventory.systems[]` records each `ObservedSystem`, including `powerState`. It does not record which `ResetType` values the BMC offers. Device status has one writer: discovery. |
| Site execution | [Site execution guide](https://github.com/datum-cloud/fleet/blob/7b97c6b2f5c61858a301de0ebb4e0cc165ab37cb/docs/operations/site-execution.md) | Per-operation worker Jobs in the cell, each with its own ServiceAccount and a Role scoped to one object. Workers never write status; a separate observer does. |
| Redfish lab | `hack/redfish-lab`, [Redfish lab guide](https://github.com/datum-cloud/fleet/blob/7b97c6b2f5c61858a301de0ebb4e0cc165ab37cb/docs/operations/redfish-lab.md) | The DMTF emulator with a lab adapter. It implements `On`, `ForceOff` and `ForceRestart` and can inject `reject-auth`, `hide-reset` and `drop-response` faults. It serves plain HTTP. |

Two facts in the current code shape this design:

- **`DeviceClaim` is not an allocation.** It records a planned installation,
  and every Device binds one through the required `spec.claimRef`. Capacity
  that consumers can allocate is a `MachineCapacity` whose `spec.deviceRef`
  names the Device. This proposal therefore refuses operations on Devices that
  back a `MachineCapacity`, not on Devices that have a claim.
- **Device has no `Ready` condition.** Discovery reports `Reachable`,
  `Authenticated`, `Discovered`, `IdentityMatched` and `PublicKeyUnique`. The
  [acceptance checks](#acceptance-checks) name the exact conditions required.

The [bare-metal hardware proposal](../../proposals/fleet-management/bare-metal-hardware.md)
states the rule for allocated machines: "reconcile persistent On/Off intent.
Reboot/reset is a bounded, idempotent operation with MachineAllocation
preconditions." This proposal applies the bounded-operation half to
unallocated Devices, with Device preconditions in place of allocation ones.

## Proposal

### User stories

- A technician racks a new server. Discovery reports it `Off`. The technician
  powers it on from the Device page, and the page shows `On` a minute later.
- Staff qualifying hardware reset a server that hangs in firmware. The page
  shows who reset it, when, and that the BMC reported `On` again afterwards.
- Staff power off a server before moving it to another rack. The Device keeps
  its identity; only its power changes.
- A server's BMC is replaced and now answers with another system UUID. A power
  request made against the old discovery is refused before any reset.
- A staff member tries to restart a server that backs capacity. The request is
  refused with `CapacityBacked`, and nothing reaches the BMC.

### Why a separate resource

A reset is an event, not a state. `ForceRestart` has no end state to converge
on, and repeating it is a second restart. A field such as
`Device.spec.powerAction` would need a controller to tell an old value from a
new request, and a re-applied manifest would restart the server again.

A separate resource also keeps one writer per surface:

| Surface | Writer |
| --- | --- |
| `Device.spec` | The inventory source: an operator or an importer |
| `Device.status` | BMC discovery |
| `DevicePowerOperation.spec` | The staff member who creates it; immutable after creation |
| `DevicePowerOperation.status` | The cell dispatcher |

Each operation is its own audit record. It keeps the requester, the reason, the
exact target and the observed result after the Device changes.

## Design details

### API

`DevicePowerOperation` is a namespaced kind in `fleet.datumapis.com/v1alpha1`.
It lives in the provider project, in the same namespace as its Device.
The example uses synthetic values.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: DevicePowerOperation
metadata:
  name: device-a-restart-1
  namespace: default
spec:
  # The Device in this namespace. The operation never follows a rename.
  deviceRef: {name: device-a}
  # One of On, ForceOff, ForceRestart, PowerCycle.
  action: ForceRestart
  # The SMBIOS system UUID staff expect behind the BMC, lowercase canonical.
  # The operation fails if the Device or the BMC reports another.
  expectedSystemUUID: 00000000-0000-4000-8000-0000000000a1
  # Admission requires this to equal the creating user's name.
  requester: staff@example.com
  reason: Hung in firmware during qualification
status:
  conditions:
    - {type: Accepted, status: "True", reason: Accepted}
    - {type: Executing, status: "False", reason: Finished}
    - {type: Finished, status: "True", reason: ResetObserved}
  # Succeeded, Failed or Unknown. Absent until Finished is True.
  result: Succeeded
  # Pinned at acceptance from the Device and its discovery.
  target:
    deviceUID: 3f0c9a52-0000-4000-8000-000000000001
    endpoint: https://192.0.2.10:443
    systemID: "1"
  acceptedAt: "2026-01-02T03:04:00Z"
  dispatchedAt: "2026-01-02T03:04:01Z"
  # When the worker sent the one reset request. Absent if none was sent.
  requestSentAt: "2026-01-02T03:04:06Z"
  # Accepted, Refused or Lost: the BMC's answer to the request, not the effect.
  response: Accepted
  power:
    before: {state: "On", observedAt: "2026-01-02T03:04:05Z"}
    after: {state: "On", observedAt: "2026-01-02T03:04:41Z"}
    # The evidence for a restart: the BMC's LastResetTime, or an observed
    # state other than On between the request and the deadline.
    resetEvidence: LastResetTime
  finishedAt: "2026-01-02T03:04:42Z"
```

The API has these rules:

- **The spec is immutable.** A CRD validation rule `self == oldSelf` covers the
  whole spec. A changed intent is a new operation.
- **Every field is required.** `reason` is 1 to 256 characters. There is no
  default action.
- **`result` is set once.** After `Finished` is `True`, the status never
  changes again.
- **Status never carries a credential, a pin, a URL path, a header or a
  response body.** `endpoint` is the Device's `spec.bmc.endpoint`, which is
  already in the Device record.

### Admission

A `ValidatingAdmissionPolicy` in the Milo project control plane checks what a
single request can show:

- On create, `spec.requester` equals `request.userInfo.username`. Kubernetes
  objects do not record their creator; this check makes the spec field
  trustworthy.
- `action` is one of the four values, and `expectedSystemUUID` is lowercase
  canonical.
- Only the dispatcher's identity can write status. IAM already enforces this;
  the policy repeats it for defense in depth.

An admission policy cannot read other objects. The checks that need the Device,
its capacity or other operations happen at [acceptance](#acceptance-checks), in
the dispatcher. Until the dispatcher accepts an operation, it has no effect.

### Acceptance checks

The dispatcher accepts or refuses each new operation for a Device it serves. It
processes one Device's operations in order of creation time. It sets
`Accepted=True` only when every check below passes. Otherwise it sets
`Accepted=False`, `Finished=True` and `result: Failed` with the reason shown.
A refused operation never reaches the BMC.

| Check | Reason on failure |
| --- | --- |
| The Device exists in the operation's namespace. | `DeviceNotFound` |
| `spec.lifecycle` is `Active` or `Decommissioning`. | `DeviceRetired` |
| `Device.spec.identity.systemUUID` is set. A Device recorded by serial alone cannot be operated, because the worker selects the Redfish system by UUID. | `SystemUUIDNotRecorded` |
| `expectedSystemUUID` equals `Device.spec.identity.systemUUID`. | `SystemUUIDMismatch` |
| Discovery is current: `Authenticated`, `Discovered` and `IdentityMatched` are `True`; `status.observedGeneration` equals the Device's generation; the inventory endpoint equals `spec.bmc.endpoint`; the inventory is younger than twice the discovery `interval` (two hours at the default of one hour), so one missed pass does not block staff. | `DiscoveryNotCurrent` |
| `PublicKeyUnique` is not `False`. A shared TLS key cannot tell BMCs apart. | `SharedPublicKey` |
| The inventory has exactly one System with the expected UUID. | `SystemNotObserved` |
| That System's recorded `resetTypes` include the action. | `ResetTypeNotOffered` |
| No `MachineCapacity` in the namespace has `spec.deviceRef.name` equal to the Device. | `CapacityBacked` |
| No other operation on the Device is accepted and unfinished. | `OperationInProgress` |
| The operation was created less than five minutes ago. | `Expired` |

Two decisions are built into this table:

- **There is no override for capacity-backed Devices.** A Device that backs a
  `MachineCapacity` may be allocated, mid-installation or mid-cleanup. The
  [PXE installation enhancement](https://github.com/datum-cloud/fleet/blob/7b97c6b2f5c61858a301de0ebb4e0cc165ab37cb/docs/enhancements/pxe-installation/README.md#shared-bmc-and-installation-execution)
  requires all power effects on an allocation to share one serialization and
  fencing domain, with "no lock-free emergency reset path". A staff override
  would be that path. Staff who must power-control such a Device first remove
  its `MachineCapacity` through the release lifecycle. A future audited
  maintenance transition can add a safer path; see
  [open questions](#open-design-questions).
- **Operations are refused, not queued.** A second request while one runs is
  refused with `OperationInProgress`. A queued request could run minutes later,
  after the reason for it has changed.

The capacity check has a gap. A `MachineCapacity` could be created after the
dispatcher checks and before the worker sends its reset. The dispatcher repeats
the capacity check immediately before it creates the Job. The worker refuses to
start more than two minutes after dispatch. Together these bound the window to
about two minutes. A new `MachineCapacity` defaults to
`allocationPolicy: Disabled`, so it cannot be allocated within that window
without a second operator action. Closing the window fully needs the allocator
to refuse a Device with an unfinished power operation. That change is listed in
[delivery phase 5](#delivery-phases).

### Dispatch

The dispatcher is a single-replica Deployment in the cell, next to discovery,
with no leader election and the `Recreate` strategy. It creates worker Jobs in
discovery's credential namespace, because a Pod can mount only Secrets in its
own namespace. Like discovery, it serves
the Device namespaces listed in its configuration. Two dispatchers must never
serve the same namespace.

```text
staff ──create──▶ DevicePowerOperation (Milo project)
                        │ watch
                        ▼
                 dispatcher (cell) ── Accepted, then Executing ──▶ operation status
                        │ create (only after Executing is stored)
                        ▼
                 Job dpo-<hash of operation UID>
                   ├─ mounts one credential Secret
                   └─ worker ── claim Lease dpo-<hash> (stage, result)
                                  │
                                  ▼
                            BMC (pinned TLS)
```

For each accepted operation, the dispatcher:

1. Re-checks capacity, as described above.
2. Writes `Executing=True` and `dispatchedAt` to the operation status with an
   optimistic lock. It never creates a Job for an operation whose status it did
   not move to `Executing` itself.
3. Creates a per-operation ServiceAccount, Role and RoleBinding, and a Job named
   `dpo-` plus the first 16 hex digits of the SHA-256 of the operation UID. The
   deterministic name makes a repeated create fail with `AlreadyExists`.
4. Watches the Job and the worker's claim Lease, and publishes the result to the
   operation status.

The Job runs one Pod with `restartPolicy: Never`, `backoffLimit: 0`,
`podReplacementPolicy: Failed` and `activeDeadlineSeconds: 600`. A worker is
never retried. The dispatcher passes the operation UID, action, expected UUID,
system Id, endpoint and a start deadline two minutes after dispatch as
arguments.

The Job mounts exactly one Secret: the credential Secret for the target Device.
Today discovery finds that Secret by its annotations, which the dispatcher could
read only with Secret read access. This proposal adds a name rule to the
credential Secret contract instead: the Secret's name is `bmc-` plus the first
20 hex digits of the SHA-256 of `<project>/<namespace>/<device>`. The dispatcher
computes the name and never reads Secrets. Discovery keeps selecting by
annotation, and it logs a warning when a
selected Secret does not have the derived name.

The dispatcher keeps the Job and the claim Lease until the result is stored in
Milo. It then deletes the per-operation RBAC at once and the Job and Lease after
seven days.

### Worker

The worker is a mode of the Fleet image. It holds no Milo credential. Its
ServiceAccount can create Leases in its namespace and get or update only its
own Lease, by `resourceNames`. It runs these steps in order and stops at the
first failure:

1. **Check the start deadline.** After the deadline it exits with reason
   `Stale` and sends nothing.
2. **Claim the operation.** It creates the Lease `dpo-<hash>`. If the Lease
   already exists, another worker ran or is running for this operation: it
   exits with reason `Duplicate` and sends nothing. Kubernetes can start a
   Job's Pod twice in rare cases, so the Job alone is not an at-most-once
   guarantee.
3. **Load the credential.** It reads `username`, `password` and
   `tlsPublicKeySHA256` from the mounted Secret. A missing pin is
   `PinNotApproved`. The worker has no unpinned or plain HTTP path.
4. **Connect and re-check identity.** It connects with `NewPinnedClient` and
   runs the power-only `DiscoverSystem` with the expected UUID. Exactly one
   System must carry the UUID, its Id must equal the pinned system Id, and its
   reset target must sit under that System. The worker issues `GET` requests
   only in this step.
5. **Check the action.** The BMC must offer the action as a `ResetType` now,
   not only at discovery time.
6. **Observe before.** It reads `PowerState`. Then:
   - `On` when already `On`, or `ForceOff` when already `Off`, finishes as
     `Succeeded` with reason `AlreadyInState`. Nothing is sent.
   - `ForceRestart` or `PowerCycle` when not `On` fails with `NotPoweredOn`.
     Vendors differ on what a restart of an off system does.
   - Any other state, such as `PoweringOn` or an empty value, fails with
     `PowerStateNotSettled`.
7. **Record the request.** It writes stage `Requesting` and the time to its
   Lease. If that write fails, it exits without sending. From this point on,
   a missing result means `Unknown`.
8. **Send once.** It calls `Reset` exactly once.
9. **Observe after.** It polls `Observe` every five seconds until the expected
   evidence appears or three minutes pass. Each read re-checks the System Id
   and UUID.
10. **Record the result.** It writes the result, reason and observations to its
    Lease and exits 0.

The installation provider repeats `On` when it can prove the first one had no
effect. The power worker does not repeat anything. A staff member who wants
another attempt creates a new operation, which leaves a second audit record and
passes the acceptance checks again.

### Outcomes

What counts as evidence depends on the action. HTTP success is never evidence.

| Action | `Succeeded` requires | Otherwise |
| --- | --- | --- |
| `On` | `PowerState` reads `On` before the deadline | `Unknown`, reason `TargetStateNotObserved` |
| `ForceOff` | `PowerState` reads `Off` before the deadline | `Unknown`, reason `TargetStateNotObserved` |
| `ForceRestart`, `PowerCycle` | `PowerState` reads `On` at the end, and either the BMC's `LastResetTime` is later than `requestSentAt` or a state other than `On` was observed after the request | `Unknown`, reason `ResetNotObserved` |

The BMC's answer to the request also matters:

| BMC response | Result |
| --- | --- |
| 2xx | Judged by the evidence above. |
| 400, 401, 403, 404, 405 or 409 | `Failed`, reason `RefusedByBMC`. The worker still observes power once and records it as `after`. If that observation shows a change, the result is `Unknown` instead. |
| 412, any other status, a timeout or a transport error | `response: Lost`. The worker keeps observing. `On` and `ForceOff` can still reach `Succeeded` with reason `ObservedAfterLostResponse`. A restart is `Unknown` unless `LastResetTime` proves it. |

For `On` and `ForceOff`, this refines the rule that a lost response ends as
`Unknown`. A lost response followed by the requested state, observed through
the pinned client on the exact system, is the same evidence the installation
provider already accepts for `ForceOff`. The result records the lost response
in `response` so staff can see it. Whether to accept it is listed as an
[open question](#open-design-questions).

A short restart can finish between two polls. A BMC that reports neither
`LastResetTime` nor an intermediate state leaves every restart `Unknown`.
Phase 4 measures this on real hardware before `ForceRestart` and `PowerCycle`
are enabled there.

### Failure handling

The dispatcher derives the result from the Job and the Lease when the worker
cannot report:

| Case | What happens | Result |
| --- | --- | --- |
| Pod or Job lost before the Lease exists | Nothing was sent. | `Failed`, reason `WorkerLost` |
| Pod or Job lost with the Lease at a stage before `Requesting` | Nothing was sent. | `Failed`, reason `WorkerLost` |
| Pod or Job lost with the Lease at `Requesting` and no result | The reset may have been sent. | `Unknown`, reason `WorkerLost` |
| Job missing while the operation is `Executing` | The dispatcher cannot tell whether a worker ran. It never creates a second Job. | `Unknown`, reason `DispatchLost` |
| Dispatcher restarts | It rebuilds its state from operation status, Jobs and Leases. It adopts existing Jobs by name and never re-dispatches an `Executing` operation. | Unchanged |
| Duplicate dispatch: a second Job or Pod for one operation | The second worker finds the Lease and exits with `Duplicate` before contacting the BMC. The second Job's exit does not change the operation. | Unchanged |
| Operation older than five minutes when first seen | Refused at acceptance. | `Failed`, reason `Expired` |
| Worker starts after its deadline, for example a Pod pending on an image pull | The worker exits before claiming. | `Failed`, reason `Stale` |
| Discovery not current | Refused at acceptance. | `Failed`, reason `DiscoveryNotCurrent` |
| TLS pin mismatch, rejected credential, identity mismatch, action no longer offered | The worker stops before sending. | `Failed` with `PinMismatch`, `CredentialRejected`, `IdentityMismatch` or `ResetTypeNotOffered` |
| BMC unreachable after the request | The worker observes until the deadline. | `Unknown`, reason `TargetStateNotObserved` |
| Operation deleted while executing | Deletion does not cancel the reset. The worker finishes. The result stays in the Lease and the worker log. | Not published |

`Unknown` is a final result for the operation, not a state of the Device. The
next discovery pass reports the Device's power. Staff decide whether to create
another operation.

### Identities and permissions

Each component gets its own identity. None inherits another's role.

| Identity | Milo project permissions | Cell permissions | BMC access |
| --- | --- | --- | --- |
| Staff, role `fleet.datumapis.com-device-power-operator` | `devicepoweroperations` create, get, list, watch; `devices` get, list, watch | None | None |
| Dispatcher, role `fleet.datumapis.com-device-power-dispatcher` | `devicepoweroperations` get, list, watch and status update; `devices` get, list, watch; `metal.datumapis.com/machinecapacities` get, list, watch | In the credential namespace: create, get, list, watch and delete Jobs, ServiceAccounts, Roles and RoleBindings; create, get, list, watch, update and delete Leases. No Secret access, and no `bind` or `escalate`. | None |
| Worker, one ServiceAccount per operation | None | Create Leases; get and update its own Lease | The one mounted credential |
| Discovery, role `fleet.datumapis.com-inventory-discoverer` | Unchanged | Unchanged | Read-only, unchanged |

The staff role grants no update, patch or delete on operations. A finished
operation is the audit record. Retention is an
[open question](#open-design-questions).

Kubernetes RBAC lets an identity create a Role only with permissions it
already holds. The dispatcher therefore holds the Lease verbs it grants to
workers, and needs neither `bind` nor `escalate`. Holding them lets the
dispatcher write a Lease itself. That adds no authority, because the dispatcher
already writes the operation's result.

The workers run in the credential namespace, so the dispatcher's right to
create Jobs reaches every `bmc-*` Secret there. Its own RBAC grants no Secret
read. Two `ValidatingAdmissionPolicies` in the cell are the barrier: they limit
the dispatcher's writes to the worker shape, as the site execution composition
already does for kro.

- A Job has a name `dpo-<hash>` and the Job's own ServiceAccount.
- It runs one container, from the image the dispatcher runs, with the worker
  command.
- It mounts exactly one Secret volume, named `bmc-` and 20 hex digits.
- It has no privilege, added capability or host namespace.
- A Role grants only Lease create, and get and update on its own Lease by
  `resourceNames`.
- A RoleBinding binds only its own Role to its own ServiceAccount.

A dispatcher that is compromised could still point a worker at the wrong Device's
Secret or at another endpoint. The pin stops the second case: an endpoint
without that BMC's private key fails the TLS handshake before the credential is
sent. In the first case the worker's UUID check fails and nothing is sent,
unless both Devices share a TLS key.

### Redfish client and discovery changes

- **Power-only discovery.** Add `Client.DiscoverSystem(ctx, uuid)`. It reads
  every Systems member with `GET` only, requires exactly one member with the
  UUID, checks that the reset target and `ActionInfo` sit under that System, and
  returns the System with its `ResetTypes`. It requires no boot target, no
  `On` or `ForceOff`, and no installation `Target`. `Discover` becomes
  `DiscoverSystem` plus the enrolled Id check and the installation checks it
  makes today, so the installation provider behaves exactly as before.
- **Reset evidence.** `Observe` also returns `LastResetTime` when the BMC
  reports it.
- **Offered reset types in discovery.** `Inventory` reads each System's
  `#ComputerSystem.Reset` allowable values, and its `ActionInfo` with one more
  `GET` when the values are not inline. `ObservedSystem` gains `resetTypes`.
  An empty list means the BMC did not report any, and every operation on that
  System is refused. Discovery stays read-only: it reads the action's
  description and never invokes it.
- **No fault hooks** in `internal/redfish`. Faults stay in the lab adapter.

### Relationship to Machine power

`Machine.spec.powerState` is persistent On or Off intent on an allocated
Machine, owned by the consumer. `DevicePowerOperation` is a one-shot staff
action on an unallocated Device. They never act on the same hardware at the
same time:

- A Device power operation is refused while any `MachineCapacity` names the
  Device. A Machine exists only on allocated capacity.
- After phase 5, the allocator refuses to allocate capacity whose Device has an
  unfinished power operation.

The Machine path reuses the executor, not the resource. A location-side Machine
power reconciler receives intent through the coordination handoff and starts the
same worker, with these differences:

| | Device power operation | Machine power |
| --- | --- | --- |
| Authority | Staff role and Device acceptance checks | MachineAllocation, its generation and the per-allocation fence |
| Intent | One action | Persistent On or Off, plus a bounded reset operation |
| Serialization | One unfinished operation per Device | Shared with installation and release for the allocation |
| Result | `DevicePowerOperation.status` | Machine `status.power`, through the projector |

The shared parts are `DiscoverSystem`, the pinned client, the claim record, the
single-request rule and the outcome table.

### Staff UI

The Device page in the provider UI gets a power control next to the observed
power state:

- A power button with a menu of the actions the Device's discovery reports as
  offered. Disabled actions carry their refusal reason, such as
  `CapacityBacked`, in a tooltip.
- A confirm dialog that shows the action, the Device name and the last four
  characters of the system UUID, and a required reason field. Confirming
  creates the operation with the UUID from discovery.
- While an operation runs, the control shows a spinner and its condition. When
  it finishes, a colored badge shows the result, with the reason and times in
  its tooltip.
- An **Operations** tab on the Device lists past operations as a dense table:
  time, action, requester, result, before and after power.

The page adds no explanatory prose. A user without the power operator role
does not see the control.

### Security boundaries

- BMC credentials stay in the cell. Only discovery and a per-operation worker
  read them, and each worker reads one.
- Every BMC connection is pinned, and the worker re-checks identity before it
  sends anything.
- No component writes `Device.spec` or `Device.status` for a power operation.
- Logs and status record outcome classes, never credentials, pins, request
  bodies or response bodies.
- The operation's requester comes from admission, not from the client.

## Delivery phases

1. **Redfish and discovery.** `DiscoverSystem`, `LastResetTime`, and
   `resetTypes` in `ObservedSystem`, with unit tests against `httptest`
   Redfish services. No new effect path.
2. **API.** The CRD, admission policy, ProtectedResource and the two Milo
   roles. With no dispatcher deployed, operations are inert: they stay
   unaccepted.
3. **Dispatcher and worker.** Both binaries, the cell admission policies,
   Kustomize packaging, the credential Secret name rule, and the lab acceptance
   tests below.
4. **Staff UI and one real host.** The Device page control, and one staged test
   on a single real server that backs no capacity: `On`, `ForceOff`, then
   `ForceRestart`, recording whether the BMC reports `LastResetTime`.
   `ForceRestart` and `PowerCycle` stay disabled on real hardware until this
   test shows a restart can be proven.
5. **Allocator gate.** The allocator refuses capacity whose Device has an
   unfinished operation, which closes the window described under
   [acceptance checks](#acceptance-checks).

## Acceptance tests

Unit tests cover the acceptance table, the outcome table and the failure table
case by case, with a fake clock. Lab tests run against `hack/redfish-lab`. The
lab needs three additions first, all lab-only:

- a TLS listener with a generated key in front of the adapter, because pinned
  clients refuse plain HTTP;
- `LastResetTime` published on each reset;
- an identity-swap fault that reports another System's UUID at the enrolled Id.

| Test | Expected |
| --- | --- |
| `On` and `ForceOff` on an idle lab target | `Succeeded`; the BMC audit shows one `POST` per operation. |
| `On` on a target that is already on | `Succeeded`, `AlreadyInState`; the audit shows no `POST`. |
| `drop-response` on `ForceOff` | `response: Lost`, `Succeeded`, `ObservedAfterLostResponse`; one `POST`. |
| `drop-response` on `ForceRestart` with `LastResetTime` withheld | `Unknown`, `ResetNotObserved`; one `POST`; no retry. |
| `ForceRestart` with `LastResetTime` | `Succeeded`, `resetEvidence: LastResetTime`. |
| Identity swap after acceptance | `Failed`, `IdentityMismatch`; the audit shows `GET` requests only. |
| `hide-reset ForceRestart` after discovery | `Failed`, `ResetTypeNotOffered`; no `POST`. |
| `PowerCycle`, which the lab never offers | Refused at acceptance with `ResetTypeNotOffered`. |
| Wrong TLS key | `Failed`, `PinMismatch`; the lab receives no request. |
| `reject-auth` | `Failed`, `CredentialRejected`; no `POST`. |
| A second Job created by hand for one operation | The second worker exits `Duplicate`; one `POST`. |
| Worker Pod deleted after the `POST` | `Unknown`, `WorkerLost`; no second Job. |
| Job deleted while `Executing`, then the dispatcher restarted | `Unknown`, `DispatchLost`; no second Job. |
| Two operations created together for one Device | One accepted, one `OperationInProgress`. |
| `MachineCapacity` naming the Device | `CapacityBacked`; no Job created. |
| `requester` set to another user | Denied at admission. |
| Staff role tries to write status, update spec or delete | Denied. |
| Discovery's audit during all tests | `GET` requests only. |

## Production readiness review questionnaire

### How is the feature enabled or disabled?

Per cell, by deploying the dispatcher for a set of Device namespaces. Without a
dispatcher, operations stay unaccepted and have no effect. Removing the
dispatcher stops new work; a running worker finishes, and its result waits in
the Lease until a dispatcher publishes it.

### What must operators monitor?

Operations unaccepted for more than a minute, `Unknown` results, workers that
exit `Duplicate`, dispatcher restarts, and Leases at `Requesting` with no
result.

### How are failures recovered?

An `Unknown` operation needs a person. Read the next discovery pass and the BMC
audit, then create a new operation if one is still needed. Nothing retries
automatically.

### What must upgrades preserve?

The Job and Lease naming, the Lease stage values, and the rule that an
`Executing` operation is never dispatched again. Upgrade the dispatcher with no
operation executing.

## Drawbacks

- One more cell process and a Job per action add seconds of latency over a
  direct BMC call.
- Restarts can end as `Unknown` on BMCs that do not report `LastResetTime`.
- The credential Secret gains a name rule that operators must follow.
- Staff cannot power-control a capacity-backed Device without first removing
  its capacity.

## Alternatives

- **A Device spec field or annotation.** Rejected: a reset is not desired state,
  and a second writer on `Device.spec` breaks single ownership.
- **Machine power intent for unallocated Devices.** Rejected: a Machine is a
  consumer allocation, and creating one for staff would expose capacity.
- **Let discovery send resets.** Rejected: discovery would stop being read-only,
  and a long-lived process would hold every BMC credential with write power.
- **The dispatcher sends resets itself.** Rejected for the same reason. The
  per-operation worker limits each process to one credential.
- **Reuse the installation provider.** It needs an enrollment, a journal and
  boot targets, and it uses one shared unpinned credential.
- **Queue operations per Device.** Rejected: a queued request can run after its
  reason is gone.
- **A staff override for capacity-backed Devices.** Not proposed; see
  [acceptance checks](#acceptance-checks).

## Open design questions

These need a decision from a reviewer:

- **Override for capacity-backed Devices.** This proposal has none. A future
  audited maintenance transition on the capacity is the alternative. Is that
  acceptable for incident response?
- **Lost response, then the requested state observed.** This proposal reports
  `Succeeded` for `On` and `ForceOff`, with `response: Lost`. The stricter
  option is `Unknown` for every lost response.
- **Restart evidence.** Should `ForceRestart` and `PowerCycle` ship in phase 3
  for the lab only, or wait for the phase 4 hardware result?
- **Graceful actions.** `GracefulShutdown` and `GracefulRestart` depend on the
  OS cooperating, and an unallocated Device often has no trusted OS. They are
  excluded. Is there a need for them?
- **Retention.** How long finished operations stay, and who may delete them.
- **Unserved operations.** An operation for a Device that no dispatcher serves
  is never accepted. Should a management-side controller expire it?
- **Credential Secret names.** The derived name changes the discovery Secret
  contract. Alternatives are a dispatcher with Secret read access, or a
  credential broker.
- **Installation enrollment overlap.** The installation provider's enrollment is
  separate configuration. A lab Device can be enrolled there without a
  `MachineCapacity`. Should enrollment derive from Devices so the capacity check
  covers it?
- **Requester.** If Milo records the creating user in an audit stream the
  dispatcher can read, the `requester` field could come from there instead.

## Infrastructure needed

The Redfish lab additions listed under [acceptance tests](#acceptance-tests), a
dispatcher deployed beside discovery, with workers in the credential namespace,
and for phase 4, one real server that backs no capacity, with an approved TLS
pin and a credential Secret.
