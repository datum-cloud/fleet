---
status: provisional
stage: alpha
---

# Cleaning servers between customers

[Documentation overview](../../README.md) · [Functionality guide](../../functionality/README.md)

- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
- [Design details](#design-details)
  - [CleaningPolicy](#cleaningpolicy)
  - [Methods and assurance](#methods-and-assurance)
  - [Drive inventory](#drive-inventory)
  - [Pinning at allocation](#pinning-at-allocation)
  - [Planning a release](#planning-a-release)
  - [Running a wipe](#running-a-wipe)
  - [Crashes and deadlines](#crashes-and-deadlines)
  - [Evidence and the cleaning gate](#evidence-and-the-cleaning-gate)
  - [Capacity status](#capacity-status)
  - [What the lab proves](#what-the-lab-proves)
  - [Implementation decisions](#implementation-decisions)
  - [Acceptance tests](#acceptance-tests)
- [Production readiness review questionnaire](#production-readiness-review-questionnaire)
- [Implementation history](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure needed](#infrastructure-needed)

## Summary

A `CleaningPolicy` says how Fleet cleans each drive of a server before another
customer can use it, what each step must prove, and what assurance the policy
claims. Allocation pins the policy and the server's drives. Release runs a
`DeviceWipe` planned from that pin, and the server leaves release only when the
wipe's evidence meets the policy.

This enhancement is a proposal. Nothing in it is implemented.

## Motivation

MachineCapacity names a cleaning policy that no API defines. Release runs one
lab procedure that zeroes a virtual disk and reads it back. Disk names come from
site enrollment, not from the drives the server has. Nothing ties what Fleet
erased to what the customer could write.

### Goals

- Providers state, per drive type, which erase methods Fleet may use and in
  which order.
- Every drive the customer could write is cleaned, named by its identity, not
  by a guessed device path.
- Each step produces evidence that Fleet checks against the policy.
- A policy claims only the assurance its methods can give.
- A server whose cleaning is unproven stays out of use.
- Changing a policy never weakens cleaning for servers already allocated.

### Non-goals

- Firmware, BMC settings, and credentials. Other release gates own them.
- Physical destruction. Staff retire the server or record a drive's disposal.
- Storage that is not a local drive the host addresses directly: RAID volumes,
  SAN, NVMe over fabrics, and removable media. Capacity with such storage is
  not offered.
- Standards certification. Fleet uses the assurance names of NIST SP 800-88
  and IEEE 2883 and makes no certification claim.

## Proposal

![A pinned policy and drive manifest drive each release wipe](diagrams/cleaning-flow.svg)

### User stories

- A provider publishes a policy that purges NVMe drives with a cryptographic
  erase and falls back to a block erase.
- A customer deletes a Machine, and Fleet erases every drive with the methods
  pinned at allocation.
- A drive refuses its first method, and Fleet uses the next one.
- A wipe's verification fails, and the server stays in release until staff
  retry the wipe or retire the server.
- A drive fails during an allocation and is pulled. Staff record its
  disposal, and release cleans the remaining drives.

### Relationship to other proposals

This enhancement fills a non-goal of [Device
operations](../device-operations/README.md): cleaning policy content and
assurance levels. It changes these parts of that design:

- **[Operations](../device-operations/README.md#operations):** `DeviceWipe`
  carries a [cleaning plan](#planning-a-release) instead of a policy digest
  and target disks, and its deadline comes from the plan.
- **[Operations](../device-operations/README.md#operations):**
  `DeviceInstallation` target disks resolve through the [pinned
  manifest](#pinning-at-allocation).
- **[Operation phases](../device-operations/README.md#operations):** a wipe
  with a `Missing` or `Uncovered` step moves from `Admitted` to
  `OutcomeUnknown` with nothing sent. The data on that drive is unaccounted
  for, which is an outcome Fleet cannot prove.
- **[Unknown outcomes](../device-operations/README.md#unknown-outcomes):**
  such a hold is settled by a new `DeviceRecovery` decision, `DisposeDrive`,
  not by `Retry`. A `Release` holder in custody records disposed drives. See
  [Planning a release](#planning-a-release).
- **[Capacity status](../device-operations/README.md#capacity-status):**
  `Available` adds the [cleaning check](#capacity-status).

Commissioning must capture an in-band drive inventory. See [Drive
inventory](#drive-inventory).

The [machine lifecycle](../machine-lifecycle/README.md#release-and-project-lifecycle)
release gates are unchanged. This enhancement defines when the `Cleaning` gate
closes.

It takes the media manifest, the coverage rules, and the limits on
cryptographic erase from [#65][release-reuse-pr], without its relational
ledger.

## Design details

### CleaningPolicy

`CleaningPolicy` is in `fleet.datumapis.com` and lives in the provider
project, in the namespace of the MachineCapacity that references it. Provider
operators own it. Customers cannot read it. Examples use synthetic data.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: CleaningPolicy
metadata:
  name: tenant-reuse-v1
  namespace: infra-ops
spec:
  # Clear or Purge. Every drive must reach it.
  assurance: Purge
  # Rules are matched in order. The first rule that matches a drive applies.
  rules:
    - name: nvme-ssd
      match: {protocol: NVMe, mediaType: SSD}      # NVMe | SATA | SAS, SSD | HDD
      # Tried in order. Fleet plans only the methods qualified for the drive.
      methods: [CryptographicErase, BlockErase]
      verify: {mode: Canary, samples: 64}
      # How long each method may take per TiB of capacity.
      timePerTiB: 10m
    - name: sata-hdd
      match: {protocol: SATA, mediaType: HDD}
      methods: [SanitizeOverwrite]
      verify: {mode: Canary, samples: 64}
      timePerTiB: 3h
  # The drive models and firmware that passed hardware qualification, and the
  # methods each passed. A Purge method runs only on a qualified drive.
  qualified:
    - {model: EXAMPLE-NVME-3840, firmware: [EX1.2, EX1.3],
       methods: [CryptographicErase, BlockErase], alwaysEncrypts: true}
    - {model: EXAMPLE-HDD-8000, firmware: [EXH4], methods: [SanitizeOverwrite]}
```

The CRD's validation rules keep the spec immutable. To change a policy, a
provider creates a new one and points MachineCapacity at it. The Fleet
validating webhook that checks operation creators also rejects a policy when:

- a rule lists a method that cannot reach the policy's assurance, so a
  fallback never lowers it;
- a method does not apply to the rule's protocol; or
- a method's check is not one it allows.

### Methods and assurance

Fleet takes each method's assurance from IEEE 2883-2022, which NIST SP 800-88
Rev. 2 refers to. Where guidance differs, Fleet uses the lower level.

| Method | NVMe | SATA | SAS | Assurance |
| --- | --- | --- | --- | --- |
| `CryptographicErase` | Sanitize, crypto erase | Sanitize, crypto scramble | Sanitize, cryptographic erase | Purge |
| `BlockErase` | Sanitize, block erase | Sanitize, block erase | Sanitize, block erase | Purge |
| `SanitizeOverwrite` | Sanitize, overwrite | Sanitize, overwrite | Sanitize, overwrite | Purge |
| `Format` | Format, user data erase | None | Format unit | Clear |
| `SecureErase` | None | Security erase unit | None | Clear |
| `Overwrite` | Host writes zeros to every block | Same | Same | Clear |

Some guidance rates an enhanced security erase as Purge. Fleet rates it Clear,
because its coverage of spare sectors depends on firmware. `Overwrite` covers
only the blocks the host can address.

A Purge method runs on a drive only when the policy lists the drive's model,
firmware, and that method as qualified. `CryptographicErase` also needs a
qualification that says the model always encrypts user data. Enabling
encryption during release does not count.

Each method uses one of two checks:

| Verify mode | Check | Allowed for |
| --- | --- | --- |
| `Full` | Read every block back and compare it with the written pattern. | `Overwrite`, required. Any method that leaves a fixed pattern. |
| `Canary` | Before the erase, write random markers to sampled ranges, including the first and last. After it, read them back. No marker may remain. | Every method except `Overwrite` |

### Drive inventory

Two sources describe a server's drives, and they must agree:

- **BMC discovery** reads each drive over Redfish into Device status: serial
  number, model, firmware, protocol, media type, capacity, and controller mode.
  Discovery stays the only writer of Device status.
- **Commissioning** boots the server and records every block device the host
  sees, including drives Redfish omits, NVMe namespaces, and removable or
  virtual media.

A server is covered only when the two lists name the same drives, every drive
is addressed directly by the host, and no removable media is attached. A drive
behind a RAID volume, a drive only one source reports, or a USB or SD device
leaves the server uncovered.

For NVMe, Fleet treats the controller as the drive. It compares the
controller's total capacity, not namespace sizes, because a customer can
create and delete namespaces.

### Pinning at allocation

When it creates a MachineAllocation, the allocator computes the policy's
digest, the SHA-256 of its canonical spec, and copies into the allocation:

- the policy's name, UID, digest, and spec; and
- the media manifest: each drive's name, identity, capacity, and matching rule.

The allocator names drives `disk-0`, `disk-1`, and so on, at pin time. Names
stay fixed for the allocation, and installations resolve target disks through
the manifest. Allocation refuses a server that is not
[covered](#capacity-status), with reason `CleaningUncovered`. Later changes to
the policy or discovery do not change the pin.

### Planning a release

One shared planner turns the pin and current discovery into a plan. The release
controller uses it to create a wipe, and admission uses it to check one.

- Every manifest drive gets a step. Its methods are the rule's methods that are
  qualified for the drive, or all of them for a Clear rule.
- A drive that discovery reports now and the manifest lacks also gets a step,
  planned with the pinned policy and named with the next free `disk-N`. The
  customer could have written to it.
- A manifest drive that discovery no longer reports gets a step marked
  `Missing`.
- A drive with no planned method gets a step marked `Uncovered`.

Fleet cannot account for the data on a `Missing` or `Uncovered` drive. The
release controller waits 10 minutes for the plan to cover every drive again,
so a brief gap in discovery does not hold the server. The wait is fixed, not a
policy setting, and it starts again after each hold is settled. If a drive is
still `Missing` or `Uncovered` when it ends, the release controller creates the
wipe. The
coordinator admits it, sends nothing, and ends it `OutcomeUnknown` with reason
`DriveMissing` or `DriveUncovered`. The server gets a recovery hold, and the
`Cleaning` gate stays open.

A recovery operator settles that hold with a `DeviceRecovery` decision
`DisposeDrive`. One operator may record it; no second person confirms it. It
names the drive and how it was destroyed or removed, with a reference. Admission
accepts it only under that hold, and only for a drive the held wipe could not
clean and current discovery no longer reports, so an uncovered drive must be
pulled first. Admission refuses `Retry` for this hold, because a retried wipe
would plan the same step. `DisposeDrive` clears the hold. The coordinator
records the disposal in custody with the `Release` holder, the planner omits
the drive, and the release controller plans the next wipe with the next step
number. The record ends when the holder leaves `Release`. Replacing a drive
does not settle the missing one.

The `DeviceWipe` carries the plan:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: DeviceWipe
metadata:
  name: alloc-3b9d61c2-wipe-1
  namespace: default
spec:
  creator: system:serviceaccount:fleet-system:release-controller
  deviceRef: {name: server-example-001, uid: 6f1c0b52-1d7e-4a35-9e0b-2c41f7a0d9e3}
  authority:
    allocationRef: {name: alloc-3b9d61c2}
  step: 1
  policy: {name: tenant-reuse-v1, uid: 0d6e3b9a-52c1-4f7e-a8d0-3c9b1e5f7a24,
           digest: "sha256:5b1e0c7d9a3f..."}
  assurance: Purge
  drives:
    - name: disk-0
      serial: EXAMPLE-SN-0001
      model: EXAMPLE-NVME-3840
      firmware: EX1.3
      protocol: NVMe
      capacityBytes: 3840755982336
      rule: nvme-ssd
      methods: [CryptographicErase, BlockErase]
      verify: {mode: Canary, samples: 64}
    - name: disk-1
      serial: EXAMPLE-SN-0002
      model: EXAMPLE-NVME-3840
      firmware: EX1.3
      protocol: NVMe
      capacityBytes: 3840755982336
      # Set only for a drive the plan cannot clean: Missing or Uncovered.
      state: Missing
```

Admission checks that the plan equals the planner's output, as a check of the
kind's own inputs. It refuses a mismatch with reason `PlanMismatch`, and the
wipe ends `Failed` with nothing sent. The release controller then plans again
as the next step.

### Running a wipe

Before powering on, the site worker checks identity over Redfish, as it does
today, and ejects every BMC virtual media. It then boots the pinned wipe
environment over the network once. The boot server refuses a second boot under
the same admission, so the environment never runs twice for one wipe.

Before it erases anything, the wipe environment:

1. lists every block device, NVMe controller, and namespace;
2. matches each drive to a planned drive by serial number, model, and
   firmware;
3. restores a host-protected area that shrank a drive, the only change it
   makes before erasing; and
4. stops with reason `MediaMismatch` if a planned drive is missing, an
   unplanned device exists, a device configuration overlay hides capacity, or
   a drive's size still differs from the plan.

For NVMe, a Sanitize covers the whole controller. Before a `Format` or
`Overwrite`, the environment deletes every namespace and creates one that spans
the controller, so no capacity stays unaddressed.

Then, for each drive, it tries the planned methods in order. It moves to the
next method only when the drive refuses a command without starting it: an
invalid opcode or field status on NVMe or SAS, or an aborted command on SATA.
It records each refusal. If every method is refused, the wipe ends `Failed`
with reason `NoSupportedMethod`. Any other error leaves that drive's outcome
unknown.

### Crashes and deadlines

The wipe environment sends each step to the site's boot server before it
starts the step, with the operation's boot capability. The site journals the
step at the operation's fence, then acknowledges it. The environment starts the
step only after the acknowledgement, and never sends an erase twice.

If the server stops mid-wipe, the worker does not boot it again. Whether a
drive's state can be read afterward depends on the method:

| Method | After a crash or deadline |
| --- | --- |
| `CryptographicErase`, `BlockErase`, `SanitizeOverwrite` | The drive keeps its sanitize status across power loss and finishes the sanitize, or reports that it failed. |
| `Format`, `SecureErase`, `Overwrite` | No status survives. The drive's state is unknown. |

Either way, the wipe ends `OutcomeUnknown`, and the server gets a [recovery
hold](../device-operations/README.md#unknown-outcomes). A wipe that staff retry
reads any sanitize status first, and waits while a sanitize still runs.

The wipe's deadline comes from the plan. It is a fixed boot allowance plus,
for each drive, the sum over its methods of the rule's `timePerTiB` times the
drive's capacity. A wipe that passes its deadline while a sanitize runs leaves
that drive busy until the sanitize ends.

### Evidence and the cleaning gate

The wipe returns one entry per planned drive:

```yaml
evidence:
  policyDigest: "sha256:5b1e0c7d9a3f..."
  drives:
    - name: disk-0
      serial: EXAMPLE-SN-0001
      firmware: EX1.3
      refused: [{method: CryptographicErase, status: "invalid field"}]
      method: BlockErase
      completion: {status: Succeeded, deviceStatus: "sanitize completed"}
      verify: {mode: Canary, samples: 64, markersFound: 0}
      startedAt: "2026-11-24T18:31:02Z"
      completedAt: "2026-11-24T18:52:40Z"
```

Evidence never carries data read from a drive.

The coordinator, not the site, decides whether a wipe succeeded. It accepts a
`Succeeded` result only when every planned drive has an entry with the planned
identity, a method from its planned list, a completed erase, and a passed check.
It derives each method's assurance itself. The kind's evidence check returns
one of these reasons, the coordinator records it, and the result ends
`OutcomeUnknown` with a recovery hold:

| Reason | Meaning |
| --- | --- |
| `EvidenceIncomplete` | A drive's entry is missing, or its erase or check did not complete. |
| `EvidenceMismatch` | An entry names another drive or firmware, or a method not in the plan. |
| `DataRemains` | A canary survived, or a block differed from the written pattern. |

`DataRemains` proves the erase failed. The hold is the same, because staff
must still choose between a retry and retirement.

The `Cleaning` gate closes when a `DeviceWipe` for the allocation succeeds.

### Capacity status

A server is covered when its MachineCapacity's policy resolves, commissioning
and discovery agree on its drives, and every drive has a planned method.
MachineCapacity is `Available` only when the server is covered. An uncovered
server reports reason `CleaningUncovered`.

### What the lab proves

The lab runs virtual machines behind the DMTF Redfish Interface Emulator.
Today it proves only `Overwrite` with `Full` verification on one virtual disk.

With this work, the lab adapter reports drives with serial numbers, models, and
firmware, and the lab machines get emulated NVMe drives. The lab can then also
prove:

- drive inventory, the manifest, planning, and missing or uncovered drives;
- matching drives by serial number, and refusing a missing or extra drive;
- the evidence check and every shortfall reason;
- falling back when a drive refuses a method; and
- step journaling, and that a stopped wipe is never booted again.

Whether the emulated NVMe drive implements `Format` and Sanitize must be
checked before the lab claims either.

The lab cannot prove that any method removes data from real media. Emulated
drives have no spare flash, no controller cache, and no encryption keys. Each
drive model and firmware must pass hardware qualification for each method a
policy lists for it.

### Implementation decisions

Decided:

- A dedicated provider role creates CleaningPolicies, separate from staff
  viewer roles.
- A single recovery operator may record `DisposeDrive`.
- A drive waits a fixed 10 minutes before it holds the server.

Record this decision before implementation:

- Who approves a hardware qualification, and where its results live.

### Acceptance tests

These tests are required and not implemented:

- A policy with a fallback below its assurance is rejected.
- A policy cannot change after creation, and an allocation keeps its pin after
  the policy is deleted.
- Allocation refuses a server with a drive no rule covers, or a drive only one
  inventory source reports.
- A drive is removed during an allocation. Release waits 10 minutes, then
  holds the server with `DriveMissing` until a recovery operator records
  `DisposeDrive` under that hold.
- A drive is missing from discovery for less than 10 minutes, and release
  wipes it without holding the server.
- The wipe environment finds an unplanned device and stops before erasing.
- A drive refuses its first method, and the next method's evidence closes the
  gate.
- A canary survives an erase, and the server gets a recovery hold with
  `DataRemains`.
- The server stops during an erase. The worker does not boot it again, and the
  wipe ends `OutcomeUnknown`.

## Production readiness review questionnaire

These controls are proposed, not implemented.

### How do operators enable or disable the feature?

Register the kind, then create a policy and point MachineCapacity at it.
Capacity without a policy is not `Available`. Releases always use their pins.

### What must operators monitor?

Recovery holds from wipes, wipe duration per drive model, and
uncovered capacity.

### Does the feature add API types or calls?

It adds `CleaningPolicy`, changes `DeviceWipe`, and adds a `DeviceRecovery`
decision. Discovery adds reads of each server's drives.

## Implementation history

- 2026-11-25: Proposed.

## Drawbacks

- Each drive model needs hardware qualification for each Purge method.
- A pinned policy cannot be tightened. If a qualified method proves flawed,
  servers already allocated still release with it, unless staff retire them.
- Copying the policy into every allocation duplicates it.
- Large drives make long wipes, and a crash late in one restarts it.

## Alternatives

- **Erase over Redfish:** Redfish offers a drive erase action, but its effect
  depends on the vendor, and Fleet cannot verify it without booting the server.
  Fleet can add it as a method once a vendor's action passes qualification.
- **A policy digest without a copy:** Release would fail if the policy were
  deleted. A finalizer would need a writer that tracks every pin.
- **A policy chosen by each customer:** Cleaning protects the next customer,
  so the provider owns it.
- **A relational ledger, as in [#65][release-reuse-pr]:** It adds a database.
  The allocation and the operation hold everything release needs.

## Infrastructure needed

Testing needs virtual machines with emulated NVMe drives and a Redfish emulator
that reports drives. Hardware qualification needs each drive model and firmware
on real servers, and a way to inspect the media after an erase.

[release-reuse-pr]: https://github.com/datum-cloud/fleet/pull/65
