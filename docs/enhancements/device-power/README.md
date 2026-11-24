---
status: provisional
stage: alpha
---

# Staff power operations on physical Devices

[Documentation overview](../../README.md) · [Architecture guide](../../architecture/README.md)

This is a proposal. None of it is implemented.

- [Summary](#summary)
- [Who it is for](#who-it-is-for)
- [Goals and non-goals](#goals-and-non-goals)
- [How it works](#how-it-works)
- [API](#api)
- [Lifecycle and outcomes](#lifecycle-and-outcomes)
- [Who can do what](#who-can-do-what)
- [Design decisions](#design-decisions)
- [Relationship to Machine power](#relationship-to-machine-power)
- [Staff UI](#staff-ui)
- [Delivery phases](#delivery-phases)
- [Open design questions](#open-design-questions)

## Summary

Staff who build and maintain hardware need to power servers on and off, and to
reset them. Today they do it by hand in each server's management controller
(BMC). Fleet keeps no record of who did it, the action uses credentials Fleet
does not manage, and nothing stops a reset of a server a customer is using.

This proposal adds a `DevicePowerOperation` resource. A staff member creates one
to ask for a single power action on one unallocated Device. Fleet checks the
request, sends one command to the exact server named, and reports whether the
action succeeded, failed, or has an unknown outcome.

## Who it is for

Infrastructure staff who rack, qualify and move servers before those servers
offer capacity to customers.

- A technician racks a new server and powers it on from the Device page, so
  Fleet can read its hardware inventory.
- Staff reset a server that hangs during qualification. The Device page shows
  who reset it, when, and the power state afterwards.
- Staff power off a server before moving it to another rack.
- A server's BMC is replaced and now reports a different system. A power
  request made against the old record is refused before anything reaches the
  server.
- Staff try to reset a server that backs customer capacity. Fleet refuses, and
  nothing reaches the server.

## Goals and non-goals

Goals:

- Staff can request power on, power off, restart and power cycle on an
  unallocated Device, and see the result.
- Each request sends at most one command, to the exact server staff named.
- An outcome Fleet cannot prove is reported as unknown, never as success, and
  is never retried automatically.
- Each request records who asked, why, and the power state before and after.

Non-goals:

- Power control of allocated servers. That stays with the customer's Machine.
- A desired power state for Devices. A Device has none.
- Graceful shutdown and graceful restart, which need the operating system to
  cooperate.
- Boot settings, virtual media, BMC settings, firmware, or power distribution
  units.

## How it works

```text
staff creates a DevicePowerOperation
        │
        ▼
dispatcher, in the site that can reach the BMC
  checks the request, then starts one worker
        │
        ▼
worker, holding only this Device's BMC credential
  re-checks the server's identity, sends one command,
  watches the power state, reports the result
```

Three components take part:

- **The dispatcher** runs in the site, next to the existing BMC discovery
  process. It accepts or refuses each request and starts a worker. It is the
  only writer of the operation's status. It holds no BMC credential.
- **The worker** is a short-lived process started for one operation. It can
  read only the target Device's BMC credential. It has no access to the Fleet
  control plane.
- **BMC discovery** already reads each BMC and records what it reports on the
  Device. It stays read-only. This proposal adds one thing to what it records:
  which power actions each server offers.

Every connection to a BMC checks the BMC's approved TLS key before it sends a
credential, the same check discovery uses today.

## API

`DevicePowerOperation` belongs to `fleet.datumapis.com`. It lives in the
provider project, in the same namespace as its Device. The values below are
synthetic.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: DevicePowerOperation
metadata:
  name: device-a-restart-1
  namespace: default
spec:
  deviceRef: {name: device-a}
  action: ForceRestart
  expectedSystemUUID: 00000000-0000-4000-8000-0000000000a1
  requester: staff@example.com
  reason: Hung in firmware during qualification
status:
  conditions:
    - {type: Accepted, status: "True", reason: Accepted}
    - {type: Executing, status: "False", reason: Finished}
    - {type: Finished, status: "True", reason: ResetObserved}
  result: Succeeded
  requestSentAt: "2026-01-02T03:04:06Z"
  power:
    before: {state: "On", observedAt: "2026-01-02T03:04:05Z"}
    after: {state: "On", observedAt: "2026-01-02T03:04:41Z"}
  finishedAt: "2026-01-02T03:04:42Z"
```

| Field | Meaning |
| --- | --- |
| `deviceRef` | The Device to act on, in the same namespace. |
| `action` | `On`, `ForceOff`, `ForceRestart` or `PowerCycle`. |
| `expectedSystemUUID` | The server staff expect behind the BMC. The operation fails if the Device record or the BMC reports another. |
| `requester` | Must equal the user who creates the operation. Admission enforces this. |
| `reason` | Required free text for the audit record. |
| `status.result` | `Succeeded`, `Failed` or `Unknown`. Set once and never changed. |
| `status.power` | The power state seen before and after the command. |

The whole spec is fixed at creation. A changed request is a new operation.
Status never carries a credential or any BMC response content.

## Lifecycle and outcomes

An operation moves through three conditions: `Accepted`, `Executing`, then
`Finished` with a result.

**Acceptance.** The dispatcher refuses an operation with `result: Failed`, and
sends nothing, when any of these holds:

| Reason | When |
| --- | --- |
| `CapacityBacked` | Customer capacity is defined on the Device. |
| `OperationInProgress` | Another operation on the Device is still running. |
| `SystemUUIDNotRecorded` | The Device has no system UUID on record. |
| `SystemUUIDMismatch` | The expected UUID differs from the Device record. |
| `DiscoveryNotCurrent` | Discovery has not recently read the BMC and matched its identity. |
| `ResetTypeNotOffered` | The server does not offer the requested action. |
| `DeviceRetired` | The Device is retired. |
| `Expired` | The operation waited more than five minutes to be accepted. |

**Execution.** The worker re-checks the server's identity and offered actions,
reads the power state, and sends one command. If the server is already in the
requested state, the worker sends nothing and reports success. A restart or
power cycle of a server that is off is refused.

**Result.** HTTP success from the BMC is not proof. The result depends on what
the worker observes afterwards:

| Action | `Succeeded` when | Otherwise |
| --- | --- | --- |
| `On`, `ForceOff` | The requested power state appears within three minutes. | `Unknown` |
| `ForceRestart`, `PowerCycle` | The BMC shows that a reset happened after the request, and the server is on again. | `Unknown` |

A BMC that refuses the command gives `Failed`. A command whose response is lost
is never resent, and its result follows the table above.

**What staff see when something breaks.** If Fleet knows nothing was sent, the
result is `Failed`. If a command may have been sent and Fleet cannot prove the
outcome, the result is `Unknown`. This covers these cases:

- a worker that crashes;
- a site process that restarts;
- a BMC that stops answering;
- a second worker started by mistake, which stops before it contacts the BMC
  and leaves the first worker's result in place.

`Unknown` ends the operation. Staff check the next discovery result and create
a new operation if they still need one. Deleting an operation does not cancel a
command already sent.

## Who can do what

| Who | Can |
| --- | --- |
| Staff with the power operator role | Create and read operations, and read Devices. They cannot edit or delete operations or write their status. |
| The dispatcher | Read operations, Devices and capacity, write operation status, and start workers in its site. |
| A worker | Read one Device's BMC credential and record its own progress in the site. |
| BMC discovery | Unchanged: read BMCs and write Device status. |

The dispatcher could, in principle, start a worker with any credential in its
site. Admission policies in the site limit every worker to the approved image,
one credential, and no extra privileges. A worker pointed at the wrong BMC fails
the TLS key check before it sends the credential. A worker given the wrong
credential fails the identity check before it sends a command.

## Design decisions

**A separate resource, not a Device field.** A reset is an event, not a state.
A field on the Device would need a controller to tell an old value from a new
request, and re-applying a manifest would reset the server again. A separate
resource gives each operation its own audit record and keeps one writer for each
part of the Device.

**No override for capacity-backed Devices.** A server that offers customer
capacity may be allocated, installing or being cleaned. Every power action on it
must be ordered with that work, and a staff override would skip that ordering.
Staff first remove the capacity through the normal release process.

**Refuse instead of queueing.** A second request while one is running is
refused. A queued request could run minutes later, after its reason is gone.

**One command, no retries.** Repeating a restart is a second restart. A staff
member who wants another attempt creates a new operation, which leaves a new
audit record and passes the checks again.

**One credential per worker.** Neither discovery nor the dispatcher sends power
commands. A long-lived process that could send commands to every BMC would be
the most valuable target in the site. Each worker can reach only one BMC.

**Name the exact server.** The request carries the server's UUID, and the worker
re-checks it against the BMC before acting. A replaced or renumbered server
behind the same BMC address is refused.

## Relationship to Machine power

`Machine.spec.powerState` in the
[machine lifecycle enhancement](../machine-lifecycle/README.md) is the
customer's ongoing on or off intent for an allocated machine. A
`DevicePowerOperation` is a one-time staff action on an unallocated Device. The
two never act on the same server at once. A Device with capacity refuses power
operations, and a later phase makes the allocator refuse a Device with a running
operation.

The Machine path will reuse the worker and its outcome rules, not the resource.
Its authority comes from the allocation, as the
[bare-metal hardware proposal](../../proposals/fleet-management/bare-metal-hardware.md)
requires for resets: "a bounded, idempotent operation with MachineAllocation
preconditions."

## Staff UI

The Device page gets a power button next to the observed power state. Its menu
lists only the actions the server offers, and an unavailable action shows its
refusal reason in a tooltip. Confirming asks for a reason and creates the
operation, and a badge then shows progress and the result. An **Operations**
tab lists past operations as a table of time, action, requester, result, and
power before and after. Users without the power operator role do not see the
button.

## Delivery phases

1. **Discovery records offered actions.** No new effect.
2. **API and roles.** Operations can be created but stay unaccepted, so they
   have no effect.
3. **Dispatcher and worker**, tested against an emulated BMC for lost
   responses, a replaced server, an unsupported action and a duplicate worker.
4. **Staff UI and one real server** that backs no capacity. Restart and power
   cycle stay disabled on real hardware until this test shows a restart can be
   proven.
5. **Allocator gate.** The allocator refuses capacity whose Device has a running
   operation. This closes a window of about two minutes between acceptance and
   the command, in which new capacity could be defined.

## Open design questions

- **Override for capacity-backed Devices.** This proposal has none. Is a future
  audited maintenance step on the capacity enough for incidents?
- **Lost response, then the right state.** The proposal reports `Succeeded`
  when the requested power state appears after a lost response. The stricter
  option reports `Unknown`.
- **When restarts ship.** Should restart and power cycle wait for the
  real-hardware test, or ship earlier for the lab only?
- **Graceful actions.** They are excluded because an unallocated server often
  has no trusted operating system. Does anyone need them?
- **Retention.** How long are finished operations kept, and who may delete
  them?
- **Unserved operations.** An operation for a Device that no site serves is
  never accepted. Should something expire it?
- **Credential naming.** Workers find their credential by a name derived from
  the Device, which changes how discovery credentials are named. Is that
  acceptable, or should a credential broker handle it?
- **Installation server list.** The installation system keeps its own list of
  servers. Should it derive that list from Devices, so the capacity check
  covers it?
- **Requester source.** Should the requester come from Milo's audit record
  instead of a spec field checked at admission?
