---
status: provisional
stage: alpha
---

# Device power operations

[Documentation overview](../../README.md) · [Architecture guide](../../architecture/README.md)

- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-goals](#non-goals)
- [Proposal](#proposal)
  - [User stories](#user-stories)
  - [Requesting staff power](#requesting-staff-power)
  - [Customer power](#customer-power)
  - [Who takes part](#who-takes-part)
- [Design details](#design-details)
  - [API design](#api-design)
  - [Admission](#admission)
  - [Execution and outcomes](#execution-and-outcomes)
  - [Recovery](#recovery)
  - [Delivery phases](#delivery-phases)
  - [Acceptance tests](#acceptance-tests)
- [Production readiness review questionnaire](#production-readiness-review-questionnaire)
- [Implementation history](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Open design questions](#open-design-questions)
- [Infrastructure needed](#infrastructure-needed)

## Summary

`DevicePowerOperation` powers a server on or off, or resets it, with one
command. Staff use it on servers that no customer holds. Fleet's Metal provider
uses it when a customer changes `Machine.spec.powerState`.

This document specifies the power kind only. The [device custody and
operations proposal][ops] defines custody, the operation envelope, admission,
site execution, recovery holds and authorization. Both documents are proposals,
and the device operations proposal is not yet merged. Nothing in either is
implemented.

## Motivation

Staff need basic power control to rack, qualify and move servers. Today they
use each server's baseboard management controller (BMC) by hand. Fleet keeps no
record of the action, and nothing stops a reset of a server a customer is using.
Customer power intent on a Machine has no executor yet.

### Goals

- Staff and customer power changes use one kind, one executor and one set of
  outcome rules.
- Each operation sends at most one command, to the exact server named.
- Each staff operation records why it ran and the power state before and after.

### Non-goals

- Graceful shutdown and restart, which need the operating system to cooperate.
- A customer restart. The Machine API has only on and off intent.
- Boot settings, virtual media, BMC settings, firmware and power distribution
  units.

## Proposal

### User stories

- A technician racks a new server and powers it on, so Fleet can read its
  hardware inventory.
- Staff reset a server that hangs during qualification, then see who reset it,
  when, and the power state afterwards.
- A customer sets a Machine's power to off, and the server powers off.
- A server's BMC is replaced and reports a different system. A request made
  against the old record is refused before anything reaches the server.

### Requesting staff power

Staff use a power button on the Device page, next to the observed power state.
Its menu lists only the actions the server offers. An unavailable action shows
why in a tooltip, for example that a customer holds the server. Confirming asks
for a reason and creates the operation. A badge shows progress and the result.
An **Operations** tab lists past operations with their time, action, requester,
result, and power before and after. Users without the power operator role do
not see the button.

### Customer power

The [Fleet provider for Metal][provider] creates a `DevicePowerOperation` when
a Machine's `powerState` differs from the server's observed power:

| `powerState` | Action |
| --- | --- |
| `On` | `On` |
| `Off` | `ForceOff` |

The provider keeps at most one power operation per Machine. When one finishes,
it compares the observed power with the latest `powerState` and creates another
only if they differ. A superseded intent therefore never reaches the server, as
the [machine lifecycle](../machine-lifecycle/README.md) requires.

### Who takes part

![Staff and the Metal provider create power operations; the coordinator admits them and writes status; one site worker acts on the BMC](diagrams/participants.svg)

BMC discovery is read-only. It reports which power actions each server offers
and the server's current power state. The coordinator uses both to check a
request, and recovery uses the power state as evidence.

## Design details

### API design

`DevicePowerOperation` uses the [operation envelope][operations]. This document
adds the power fields. Identifiers and timestamps are synthetic.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: DevicePowerOperation
metadata:
  name: device-a-restart-1
  namespace: default
spec:
  deviceRef: {name: device-a}
  # Staff use a service request, and the reason is required. The Metal
  # provider uses allocationRef instead.
  authority:
    serviceRequest: {reason: "Hung in firmware during qualification"}
  # On, ForceOff, ForceRestart or PowerCycle.
  action: ForceRestart
  # The server the requester expects behind the BMC. The Metal provider copies
  # it from the Device record.
  expectedSystemUUID: 00000000-0000-4000-8000-0000000000a1
status:
  admission: {fence: 43}
  phase: Succeeded
  # When the one command was sent. Absent if none was sent.
  requestSentAt: "2026-01-02T03:04:06Z"
  power:
    before: {state: "On", observedAt: "2026-01-02T03:04:05Z"}
    after: {state: "On", observedAt: "2026-01-02T03:04:41Z"}
```

Status never carries a credential or BMC response content.

### Admission

The coordinator admits a power operation through [custody admission][admission]
when the holder matches its authority:

| Authority | Required holder |
| --- | --- |
| `serviceRequest` | `None`. Disabled capacity on the Device does not block it. Custody moves to `Service` while it runs. |
| `allocationRef` | `Allocation`, naming the same allocation. |

Power adds these refusals. Each ends the operation as `Failed` with nothing
sent:

| Reason | When |
| --- | --- |
| `AuthorityNotPermitted` | A creator other than the Fleet provider for Metal uses `allocationRef`. |
| `SystemUUIDNotRecorded` | The Device has no system UUID on record. |
| `SystemUUIDMismatch` | `expectedSystemUUID` differs from the Device record or from discovery. |
| `DiscoveryNotCurrent` | Discovery has not read the BMC within twice its interval. |
| `SharedPublicKey` | The BMC shares its TLS key with another BMC, so the key cannot tell them apart. |
| `ResetTypeNotOffered` | Discovery does not report the action as offered. |
| `Expired` | A staff operation was not admitted within five minutes. |

A customer operation does not expire. It waits while another operation, such as
an installation, holds custody.

### Execution and outcomes

![One power operation from request to result](diagrams/operation-sequence.svg)

The [site worker][site] adds these rules for power:

- It refuses with `Stale`, and sends nothing, if it starts more than two minutes
  after admission.
- It re-checks that the action is offered, then reads the power state.
- If the server is already in the requested state, it sends nothing and reports
  success.
- It refuses a restart or power cycle of a server that is off, and any action
  while the power state is still changing.

HTTP success from the BMC is not proof. The result depends on what the worker
observes after the command:

| Action | `Succeeded` when | Otherwise |
| --- | --- | --- |
| `On`, `ForceOff` | The requested power state appears within three minutes. | `Unknown` |
| `ForceRestart`, `PowerCycle` | The BMC shows a reset after the request, and the server is on again. | `Unknown` |

A BMC that refuses the command gives `Failed`, unless the power state changed
anyway, which gives `Unknown`. A lost response is never resent. Its result
follows the table above.

### Recovery

![A power operation ends Succeeded or Failed and releases custody, or ends Unknown and places a recovery hold](diagrams/operation-lifecycle.svg)

An `Unknown` power operation places a [recovery hold][unknown] on custody. The
hold settles only with both:

- discovery evidence of the power state, observed after the operation ended;
  and
- a staff recovery record that names the operation, that evidence and the
  decision.

A held allocated server keeps its customer. The Machine reports power as
`Unknown` until the hold settles.

### Delivery phases

1. **Discovery records offered actions.** No new effect.
2. **Staff power.** The kind and the staff role, on the custody and site
   execution from the device operations proposal. Tested against an emulated
   BMC.
3. **Staff UI and one real server** with holder `None`. Restart and power cycle
   stay disabled on real hardware until this test shows a restart can be
   proven.
4. **Customer power.** The Metal provider creates operations from `powerState`.

### Acceptance tests

These tests are required and not written:

- A lost response after on, off and restart gives the result in the outcome
  table, with one command at the BMC.
- A replaced server behind the same BMC address is refused before any command.
- An action the server does not offer, and a BMC with a shared TLS key, are
  refused.
- A staff operation is admitted on a Device with disabled capacity and holder
  `None`.
- A staff creator using `allocationRef` is refused.
- A worker that starts late refuses as `Stale`.
- Quick `powerState` changes send only the latest intent.
- An `Unknown` power operation settles only with fresh discovery evidence and a
  recovery record.

## Production readiness review questionnaire

These controls are proposed, not implemented.

### How do operators enable or disable the feature?

Grant or revoke the staff power operator role. Customer power follows the Fleet
provider for Metal. Pausing admission is covered in the [device operations
proposal][ops].

### What must operators monitor?

Power recovery holds, most urgently on allocated servers, `Expired` and `Stale`
refusals, and restarts that end `Unknown` on each BMC model.

### How are failures recovered?

Wait for discovery to read the server's power state after the operation ended.
Then record the decision in a staff recovery record, which settles the hold.

## Implementation history

- 2026-11-24: Proposed as a standalone staff feature.
- 2026-11-24: Revised as the power kind of the device custody and operations
  proposal, covering customer power.

## Drawbacks

- Restarts can end `Unknown` on BMCs that do not report when a reset happened.
- An `Unknown` customer power change keeps the customer's server held until
  staff record recovery.
- A customer power change waits behind a running installation.

## Alternatives

- **Separate kinds for staff and customer power.** Rejected. Both need the same
  executor and outcome rules. `AuthorityNotPermitted` keeps allocation authority
  with the Metal provider.
- **Machine power intent for unallocated Devices.** Rejected. A Machine is a
  customer allocation, and creating one for staff would expose capacity.

## Open design questions

- **Lost response, then the right state.** This proposal reports `Succeeded`
  when the requested power state appears after a lost response. The stricter
  option reports `Unknown`.
- **Restart evidence.** A restart can be proven only when the BMC reports that a
  reset happened. Should restart and power cycle wait for the real-hardware
  test, or ship earlier on test hardware only?
- **Customer power off.** `Off` maps to `ForceOff`, which cuts power without
  warning the operating system. Should Metal offer a graceful power off?
- **Requester.** The operation envelope has no requester field. Where does the
  staff UI's requester come from?
- **Pending limits.** The device operations proposal sets one pending limit per
  kind. Power needs five minutes for staff and none for customers. Should the
  envelope allow a limit per authority?
- **Shared TLS keys.** The refusal protects every kind that contacts a BMC.
  Should the device operations proposal apply it to all kinds?

## Infrastructure needed

An emulated BMC that supports TLS and reports when a reset happened, and one
real server with holder `None`, an approved TLS key and a credential.

[ops]: ../device-operations/README.md
[operations]: ../device-operations/README.md#operations
[admission]: ../device-operations/README.md#admission
[site]: ../device-operations/README.md#site-execution
[unknown]: ../device-operations/README.md#unknown-outcomes
[provider]: ../device-operations/README.md#fleet-as-a-metal-provider
