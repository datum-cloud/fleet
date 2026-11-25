---
status: provisional
stage: alpha
---

# Device commissioning and sellable capacity

[Documentation overview](../../README.md) · [Device operations](../device-operations/README.md)

- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
- [Design details](#design-details)
  - [Commissioning plans](#commissioning-plans)
  - [DeviceCommissioning](#devicecommissioning)
  - [Capacity status](#capacity-status)
  - [Enabling registered capacity](#enabling-registered-capacity)
  - [Duplicate identity](#duplicate-identity)
  - [Removing orphaned capacity](#removing-orphaned-capacity)
  - [Authenticating the registration webhook's caller](#authenticating-the-registration-webhooks-caller)
  - [Write order and crash recovery](#write-order-and-crash-recovery)
  - [Acceptance tests](#acceptance-tests)
- [Decisions for a person](#decisions-for-a-person)
- [Production readiness review questionnaire](#production-readiness-review-questionnaire)
- [Implementation history](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure needed](#infrastructure-needed)

## Summary

A registered server stays `Disabled` today, and nothing can make it
allocatable. This enhancement adds the path from a registered server to
capacity a customer can be given:

1. Staff commission the server with a `DeviceCommissioning` operation. It runs
   an approved, pinned plan: inspection, a firmware baseline check, and
   burn-in.
2. The capacity projector derives `Prepared`, `Healthy` and `Available` from
   commissioning, discovery and custody evidence. It is the only writer of
   those conditions.
3. Staff enable the capacity, naming a cleaning policy and a capacity pool.

This enhancement is a proposal. Nothing in it is implemented.

## Motivation

The [device operations](../device-operations/README.md) design requires a
succeeded commissioning under the current plan before capacity is `Prepared`.
No commissioning kind exists, so no real server can become `Prepared`.
Registration keeps registered capacity `Disabled` until three gaps close: a
duplicate identity race, capacity orphaned by a deleted Device, and an
unauthenticated webhook caller. Test fixtures write capacity conditions
directly, so those conditions prove nothing.

### Goals

- Staff commission a server through custody, like every other physical action.
- A plan is qualified only after a second person approves its exact content.
- Capacity conditions come only from evidence. Editing status cannot make a
  server allocatable.
- Registered capacity can move from `Disabled` to `Enabled` under checks that
  hold for real hardware.
- The same rules apply in every mode, including test modes.

### Non-goals

- Updating firmware. Commissioning checks firmware and refuses a server that
  does not meet the baseline. Updating firmware is a separate operation.
- Cleaning policy content and how a cleaning policy is resolved. Another
  workstream owns the cleaning policy kind.
- Burn-in test content. The plan pins a burn-in image; what it tests is
  operator policy.
- Rack, cabling and network checks.

## Proposal

![A registered server becomes allocatable](diagrams/sellable-path.svg)

1. Staff register a Device. Its capacity is created `Disabled`, as today.
2. A plan author writes a commissioning plan. A different person approves it.
3. A commissioning operator creates a `DeviceCommissioning` for the Device
   with a copy of the approved plan. Custody admits it from holder `None`
   and sets holder `Service`.
4. The site worker inspects the server, checks firmware, runs burn-in, and
   returns evidence. Custody returns to `None`.
5. The capacity projector sets `Prepared` and `Healthy`, and records the
   inspected hardware on the capacity.
6. A capacity operator enables the capacity with a cleaning policy and a pool
   label. The projector sets `Available`, and the pool's MachineClass can
   allocate it.

### User stories

- A staff member commissions a new server, enables it, and a customer gets it.
- A hardware team qualifies a new plan for a server model. Servers of that
  model stop being `Prepared` until they are commissioned under it.
- Burn-in finds a faulty memory module. The server stays unprepared.
- A Device is deleted while its capacity exists. A staff member removes the
  capacity.

### Relationship to other proposals

- **[Device operations](../device-operations/README.md):** adds the
  `DeviceCommissioning` row of the operations table and settles its open
  decision on what makes a commissioning plan qualified. It refines
  [capacity status](../device-operations/README.md#capacity-status) without
  relaxing it.
- **Inventory-backed capacity registration:** this is registration stage 2.
  It resolves the three stage-2 blockers and decides who sets the pool label.
- **Cleaning policy:** this enhancement consumes a cleaning policy by name and
  digest only. See [Enabling registered
  capacity](#enabling-registered-capacity).

## Design details

All new kinds are in `fleet.datumapis.com` and live in the provider project.
Names are provisional. Examples use synthetic data.

### Commissioning plans

A `DeviceCommissioningPlan` says what commissioning must prove for a set of
Devices. Its spec is immutable. To change a plan, write a new one.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: DeviceCommissioningPlan
metadata:
  name: example-2u-v3
  namespace: default
spec:
  creator: {username: plan-author@example.com, uid: 3c1e0b6a-0000-4000-8000-000000000001}
  deviceSelector:
    matchLabels: {fleet.datumapis.com/hardware-model: example-2u}
  supersedes: [example-2u-v2]
  inspection:
    cpu: {architecture: amd64, cores: 32}
    memory: {capacity: 256Gi}
    disks: {count: 2, minCapacity: 960Gi}
    interfaces: {count: 2}
  firmware:
    - {component: BMC, version: "5.10.00"}
    - {component: BIOS, version: "2.19.1"}
  burnIn:
    image: {name: burn-in, digest: "sha256:4be1c1a5d0e7..."}
    duration: 4h
  evidenceLifetime: 2160h
```

| Field | Meaning |
| --- | --- |
| `deviceSelector` | The Devices the plan applies to, by Device labels. |
| `supersedes` | Plans that stop being current once this plan is qualified. |
| `inspection` | The CPU and memory the server must report exactly, and the minimum disks and interfaces. Every reported component must be healthy. |
| `firmware` | The exact version each named firmware component must report. |
| `burnIn` | A burn-in image pinned by digest and how long it runs, from 1 minute to 24 hours. |
| `evidenceLifetime` | How long a commissioning stays current, from 1 hour to 365 days. |

The creator must be the authenticated caller, as for operations.

#### Qualification

A plan is **qualified** when a `DeviceCommissioningPlanApproval` with the
plan's name exists and all of these hold:

- The approval names the plan's UID and the digest of its spec.
- The approval's creator is not the plan's creator.
- The approval's creator held the commissioning plan approver role when the
  approval was created. IAM checks this on create.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: DeviceCommissioningPlanApproval
metadata:
  name: example-2u-v3          # the plan's name, so a plan has at most one approval
  namespace: default
spec:
  creator: {username: approver@example.com, uid: 3c1e0b6a-0000-4000-8000-000000000002}
  planRef: {name: example-2u-v3, uid: 9a7d..., digest: "sha256:77ce..."}
```

An approval is immutable. Deleting it revokes qualification. A plan deleted
and created again has a new UID, so an old approval cannot qualify it.

#### The current plan

A Device's **current plan** is the one qualified plan whose selector matches
the Device's labels and that no qualified plan supersedes. If none matches,
the Device has no current plan. If more than one matches, the Device has no
current plan, and its capacity reports `PlanAmbiguous`.

Qualifying a plan that supersedes another changes the current plan of every
Device it matches at once. Those Devices stop being `Prepared` until they are
commissioned under the new plan.

### DeviceCommissioning

| Creator | Authority | Required holder | Inputs |
| --- | --- | --- | --- |
| Staff commissioning operator | `serviceRequest` | `None`, or `Service` under a commissioning hold | The plan's name, UID and digest, and a copy of its spec |

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: DeviceCommissioning
metadata:
  generateName: server-example-001-commission-
  namespace: default
spec:
  creator: {username: operator@example.com, uid: 3c1e0b6a-0000-4000-8000-000000000003}
  deviceRef: {name: server-example-001, uid: 6f1c0b52-1d7e-4a35-9e0b-2c41f7a0d9e3}
  authority:
    serviceRequest: {reason: New server intake}
  plan:
    name: example-2u-v3
    uid: 9a7d...
    digest: "sha256:77ce..."
    spec: {}                   # an exact copy of the plan's spec
```

The operation carries a copy of the plan, so the site runs exactly the plan
that admission checked, and the record keeps it after the plan is deleted.

#### Admission

The shared admission rules apply. The kind adds these checks:

| Reason | Refused when |
| --- | --- |
| `PlanNotQualified` | The named plan does not exist, has another UID, or is not qualified. |
| `PlanNotCurrent` | The plan is not the Device's current plan. |
| `PlanMismatch` | The copied spec's digest is not the named digest, or not the plan's digest. |
| `DeviceNotActive` | The Device's inventory lifecycle is not `Active`. |

Like every staff operation, a commissioning never queues. Its deadline is the
plan's burn-in duration plus one hour for inspection and boots. The
coordinator sets it at admission.

#### Site execution

The worker uses the Redfish path and the site framework from device
operations: the fence, journal, per-Device credential, pinned TLS key and
identity checks. It runs three phases in order:

1. **Inspect.** Read the system's identity, processors, memory, storage and
   network interfaces. The serial number and system UUID must equal the
   Device's, and the hardware must meet the plan. Nothing is sent to the
   server.
2. **Check firmware.** Read the firmware inventory. Each named component must
   report the plan's version. Nothing is sent to the server.
3. **Burn in.** Power the server off, arm a one-time network boot, and power
   it on once. The boot server serves the plan's burn-in image only while
   the operation's boot admission is open and at the Device's stored fence.
   The image runs for the plan's duration and reports one console receipt
   with its result. The worker then waits for the server to power off and
   reads its health again.

A mismatch in phase 1 or 2 ends the operation `Failed` with
`InspectionMismatch` or `FirmwareBaselineNotMet`, and nothing was sent. A
burn-in that reports failure ends `Failed` with `BurnInFailed`. That outcome
is proven: the server ran the image, reported, and powered off. So it sets no
recovery hold, and the server is left off. Any outcome the worker cannot
prove, such as a missing receipt at the deadline, ends `OutcomeUnknown`.

#### Evidence

A succeeded commissioning records, within 16 KiB:

- the serial number and system UUID the BMC reported, and the BMC key digest;
- the inspected CPU, memory, disks and interfaces, in the shape of
  MachineCapacity hardware;
- each firmware component and its version;
- the burn-in receipt, start and end times, and result; and
- the plan's name, UID and digest.

Evidence carries no credential and no raw BMC response.

#### Holds and retention

A commissioning that ends `OutcomeUnknown` sets a commissioning hold. A later
`DeviceCommissioning` that succeeds settles it, as the device operations
settlement table says. No `Retry` is needed, because staff create the
settling operation directly.

Fleet keeps each Device's latest succeeded commissioning past the 30-day
retention of finished operations, until a later commissioning succeeds. It is
the evidence `Prepared` and `Healthy` read.

### Capacity status

The capacity projector writes `Prepared`, `Healthy`, `Available` and the
hardware of every MachineCapacity. It reads the capacity, its Device, custody,
the Device's latest succeeded commissioning, current plans, the cleaning
policy reference, and every Device in the provider scope.

| Condition | True when |
| --- | --- |
| `Prepared` | The Device's latest succeeded commissioning used its current plan. |
| `Healthy` | `Prepared`, the commissioning ended within the plan's evidence lifetime, discovery is current, and discovery reports the serial number, system UUID and BMC key digest the commissioning recorded. |
| `Available` | `Healthy`, custody is `Verified` with holder `None`, no active operation and no recovery hold, the allocation policy is `Enabled`, the Device is `Active`, the cleaning policy resolves, and no other Device shares an identity alias. |

Discovery is current when it matched the Device's identity for its current
generation, read its inventory within twice the discovery interval, and
recorded the BMC key at its current endpoint. Each false condition names the
first check that failed, such as `NotCommissioned`, `PlanChanged`,
`NoCurrentPlan`, `PlanAmbiguous`, `EvidenceExpired`, `DiscoveryNotCurrent`,
`IdentityChanged`, `Held`, `AllocationDisabled`, `CleaningPolicyUnresolved`
or `IdentityConflict`.

The capacity's hardware is the hardware the latest succeeded commissioning
inspected. Nobody else writes it.

Editing status cannot make a server allocatable, for two reasons:

- The allocator evaluates the same projection from live inputs when it
  selects capacity. It does not read the conditions. Custody still decides
  with a conditional update.
- The registration webhook admits a status write to registered capacity only
  from the projector's identity.

Every mode uses the projector. Test modes commission through their simulated
sites instead of writing conditions.

### Enabling registered capacity

The registration webhook keeps registered capacity immutable except for three
fields: the allocation policy, the cleaning policy reference, and the
`fleet.datumapis.com/capacity-pool` label.

- **While the capacity is `Disabled`**, a capacity operator may change all
  three.
- **While it is `Enabled`**, the only permitted change is to `Disabled`.
- **Enabling** requires, at the time of the update:
  - the caller may update the capacity;
  - the annotated Device exists with the annotated UID and is `Active`;
  - no other Device in the provider scope shares an identity alias;
  - the cleaning policy reference is set and resolves to a digest; and
  - the pool label is set.

Enabling does not require commissioning. A capacity enabled before
commissioning is not `Available` until it is `Prepared` and `Healthy`.

**The pool label.** The capacity operator sets the pool label, at registration
or while the capacity is `Disabled`. A MachineClass selects capacity by that
label. Freezing it while the capacity is `Enabled` keeps a class from losing
or gaining capacity without the capacity being disabled first.

**The cleaning policy.** Fleet consumes a cleaning policy through two values
that the [device operations](../device-operations/README.md) design already
names: the policy's name, on the capacity, and its digest, pinned on each
MachineAllocation. The cleaning policy owner provides one contract: given a
name in the provider project, return the policy's current digest, or report
that it does not resolve. Fleet asks for that digest when a capacity is
enabled, when the projector evaluates `Available`, and when the allocator
pins it. Until the owner provides the contract, no name resolves, and no
registered capacity can be enabled.

### Duplicate identity

Registration rejects a Device that shares an identity alias with another
Device: the serial number, the system UUID, or the provider host tuple. The
stage-1 design leaves open that Device B can be created with Device A's serial
after A was checked. The question is whether A can then be offered while its
identity is ambiguous. Three rules answer it:

1. **One capacity per alias.** Registration lists Devices with quorum reads.
   A's registration happens after A exists. B's registration happens after B
   exists. At least one of the two lists sees both Devices, so at least one
   registration is denied. Two capacities never share an alias.
2. **The BMC proves the identity.** Commissioning reads the serial number and
   system UUID from the BMC and fails if they differ from A's. Admission
   already refuses a BMC key that another Device records. A capacity is
   `Prepared` only if the server behind A's BMC is A.
3. **Every offer checks again.** `Available` and the allocator both require
   that no other Device shares an alias, at each evaluation. Once B appears,
   A is not offered until a person resolves the conflict.

Rule 3 does not end an allocation that already holds A. That server's
identity is still proven by its BMC, and B can never get capacity. No object
can make a Device create and another Device's registration atomic, so Fleet
checks at the point where it acts.

### Removing orphaned capacity

The registration webhook admits the delete of registered capacity when all of
these hold:

- the capacity is `Disabled`;
- its annotated Device is missing, has a different UID, or is `Retired`;
- no MachineAllocation that has not been released names the capacity; and
- the caller may delete the capacity.

The webhook reads the Device and allocations with quorum reads. Removing the
capacity removes nothing else. If the Device was deleted, its identity has
already left the duplicate check.

### Authenticating the registration webhook's caller

Today any holder of a certificate from the webhook's client CA can send the
webhook a review and read the verdict. The webhook will also require that the
client certificate's subject be one of the configured API server webhook
client identities. It rejects any other certificate during the TLS handshake,
before reading a request. The environment configures those identities and
issues them only to the API servers that call the webhook.

### Write order and crash recovery

| Change | Order | After a crash between the writes |
| --- | --- | --- |
| Qualify a plan | Plan, then approval | The plan stays unqualified until the approval exists. |
| Revoke a plan | Delete the approval | One write. Devices stop being `Prepared` when the projector next runs. |
| Commission a server | Custody, then operation status | As for every operation. |
| Project capacity | Read every input, then one status update | The projector derives the status again from the same inputs. |
| Enable or disable capacity | One capacity update | One write. |
| Remove orphaned capacity | One delete with UID and resourceVersion preconditions | One write. |

### Acceptance tests

- A registered server is commissioned over Redfish in the lab, becomes
  `Prepared`, `Healthy` and `Available` after it is enabled, and is allocated.
- A plan approved by its own author does not qualify, and admission refuses a
  commissioning under it.
- A commissioning whose copied plan differs from the approved plan is refused,
  and nothing is sent.
- An inspection or firmware mismatch ends `Failed`, and the lab BMC audit
  shows no effectful request.
- A burn-in failure ends `Failed`, sets no hold, and leaves the server off.
- A commissioning passes its deadline and ends `OutcomeUnknown`, and a later
  commissioning settles the hold.
- A superseding plan is qualified, and every matching capacity stops being
  `Prepared`.
- A status edit sets `Available` on unprepared capacity, and the allocator
  still refuses it.
- A status write to registered capacity by any identity but the projector is
  denied.
- Enabling is denied without a resolving cleaning policy, without a pool
  label, with a duplicate Device, or with a Device that is not `Active`.
- An enabled capacity's pool label cannot change until it is disabled.
- A duplicate Device appears after a capacity is enabled, and the capacity
  stops being `Available`.
- Capacity whose Device was deleted is removed, and capacity with an
  unreleased allocation is not.
- A certificate from the client CA with an unlisted subject is rejected
  during the handshake.

## Decisions for a person

- **Who approves commissioning plans.** This design requires a second person
  with the approver role. A person must decide which team holds that role
  and confirm the two-person rule.
- **Firmware is checked, not updated.** A server below the baseline fails
  commissioning. A person must confirm that firmware updates stay out of
  scope.
- **Plan content per hardware model.** The burn-in image, its duration, and
  the evidence lifetime are operator policy. Fleet sets no defaults.
- **A new plan unprepares a whole model.** Qualifying a superseding plan
  makes every matching free server unallocatable until it is commissioned
  again.
- **The cleaning policy contract.** The cleaning policy owner must provide
  name-to-digest resolution. Until then no registered capacity can be
  enabled.
- **The webhook client identities.** Each environment names the API server
  identities and keeps their issuer dedicated to them.

## Production readiness review questionnaire

These controls are proposed, not implemented.

### How do operators enable or disable the feature?

Register the kinds, roles and webhook rules, then run the projector with the
coordinator. To stop new commissioning, revoke plan approvals. To stop new
allocations, disable capacity.

### Does enabling the feature change any default behavior?

Yes. Capacity conditions come from the projector. Capacity that a fixture
marked `Available` stops being `Available` until it is commissioned.

### What must operators monitor?

Commissioning failures by reason, commissioning holds, capacity reporting
`PlanAmbiguous` or `IdentityConflict`, expiring evidence, and webhook TLS
rejections.

### What does the feature depend on?

Device custody and site execution, BMC discovery, the boot server, and the
cleaning policy owner's resolution contract.

### Does the feature add API types or calls?

It adds `DeviceCommissioningPlan`, `DeviceCommissioningPlanApproval` and
`DeviceCommissioning`. The projector makes one status write per capacity when
an input changes.

## Implementation history

- 2026-11-25: Proposed.

## Drawbacks

- Commissioning reboots the server and holds it for the burn-in duration.
- Every free server of a model needs commissioning again after a plan change.
- Enabling registered capacity waits on the cleaning policy contract.

## Alternatives

- **A plan reference on each capacity:** Capacity is a Metal object, and plans
  are Fleet's. Selecting by Device labels keeps plans with the hardware.
- **Approval as a field on the plan:** The plan author and the approver would
  write one object. A separate approval keeps one writer per object.
- **A plan reference instead of a copy in the operation:** The site would need
  to read the provider project, and the record would lose the plan when it is
  deleted.
- **An identity claim object per alias:** A claim cannot make another
  Device's create fail, so it leaves the same window as rule 3 and adds a
  kind.
- **Updating firmware during commissioning:** A failed update can leave the
  server unbootable, which needs its own recovery design.

## Infrastructure needed

The Redfish lab needs a firmware inventory on the lab BMC and a burn-in mode
in the lab boot image. Hardware qualification needs a burn-in image for each
model.
