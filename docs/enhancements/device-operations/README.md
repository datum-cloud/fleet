---
status: provisional
stage: alpha
---

# Device custody and operations

[Documentation overview](../../README.md) · [Functionality guide](../../functionality/README.md)

- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
- [Design details](#design-details)
  - [API design](#api-design)
  - [Admission](#admission)
  - [Site execution](#site-execution)
  - [Unknown outcomes](#unknown-outcomes)
  - [Consistency without a database](#consistency-without-a-database)
  - [Fleet as a Metal provider](#fleet-as-a-metal-provider)
  - [Capacity status](#capacity-status)
  - [Authorization](#authorization)
  - [Implementation decisions](#implementation-decisions)
  - [Acceptance tests](#acceptance-tests)
- [Production readiness review questionnaire](#production-readiness-review-questionnaire)
- [Implementation history](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure needed](#infrastructure-needed)

## Summary

Fleet records custody for each server in one `DeviceCustody` object. Custody
names who holds the server and the one operation running on it. Fleet starts
every physical action, such as an install, a power change, or a wipe, by
updating that object.

Customers keep using the Metal API. Fleet fulfills Metal requests with typed
Device operations, and staff use the same operations for servers that no
customer holds.

This enhancement is a proposal. Nothing in it is implemented.

## Motivation

Five kinds of work act on the same server:

- customer installation and reinstallation;
- customer power changes;
- cleaning between customers;
- preparation before a server is offered, such as burn-in and firmware; and
- staff power changes on unallocated servers.

Today each kind has its own lock, or none. Two actions can reach one BMC at the
same time. A lock per allocation cannot cover staff work, because staff work has
no allocation.

### Goals

- One record per server says who holds it and what runs on it.
- Every physical action on a server goes through that record.
- Customer and staff work use the same operations and the same executor.
- Staff cannot act on a server that a customer holds.
- A server with an unproven operation outcome stays out of use.
- Staff cannot read customer secrets.
- No new database.

### Non-goals

- Image catalog and bootstrap APIs. Metal owns them.
- Network attachment and address management.
- Cleaning policy content and assurance levels.
- Customer console access.
- How other Metal providers run their hardware.

## Proposal

Customers create Machines and Installations in their project. Fleet's Metal
provider turns those requests into Device operations in the provider project.
Staff create operations there too. The Fleet coordinator admits each operation
by updating the server's custody, then sends it to the site that can reach the
server.

![Every effect on a server passes through its custody record](diagrams/layers.svg)

### User stories

- A customer installs an operating system, then reinstalls it.
- A staff member commissions a new server so that it can be offered.
- A staff member power-cycles an unallocated server that stopped responding.
- A customer deletes a Machine, and Fleet wipes the server before reuse.
- An operator settles an operation whose outcome is unknown.

### Relationship to other proposals

This enhancement:

- replaces per-allocation serialization in the [machine
  lifecycle](../machine-lifecycle/README.md#provider-execution-and-recovery)
  with per-server custody;
- absorbs the staff power proposal in [#173][power-pr] as one operation kind;
  and
- replaces the relational ledger in [#60][admission-pr] and [#62][execution-pr]
  with the custody object.

## Design details

### API design

All kinds are in `fleet.datumapis.com` and live in the provider project, in the
namespace of their Device. Names are provisional. Examples use synthetic data.

#### DeviceCustody

Each Device has one `DeviceCustody` with the same name. Only the coordinator
writes it.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: DeviceCustody
metadata:
  name: server-example-001
  namespace: default
spec:
  deviceRef: {name: server-example-001}
  holder:
    type: Allocation          # None | Allocation | Release | Service | Retired
    allocationRef: {name: alloc-7f3c2}
  # Increases on every change. Executors reject work with any other value.
  fence: 42
  activeOperation:
    kind: DeviceInstallation
    name: alloc-7f3c2-install-01
  # Set after an Unknown outcome.
  recoveryHold: null
```

The fields are in `spec` because custody is a coordination record, like a
Kubernetes `Lease`, not desired state.

| Holder | Meaning |
| --- | --- |
| `None` | Nobody holds the server. |
| `Allocation` | A customer Machine holds the server. |
| `Release` | The allocation ended, and cleanup is not yet verified. |
| `Service` | A staff operation holds the server while it runs. |
| `Retired` | The server is out of service permanently. |

![Device custody holders and transitions](diagrams/custody-states.svg)

The allocator claims a server with the same conditional update that admits
operations. If two allocators race, one wins and the other picks another
server. A server leaves `Release` only after every [machine lifecycle release
gate](../machine-lifecycle/README.md#release-and-project-lifecycle) closes.

#### Operations

Every operation has these fields:

```yaml
spec:
  deviceRef: {name: server-example-001}
  authority:                         # exactly one
    allocationRef: {name: alloc-7f3c2}
    # serviceRequest: {reason: "Host hung during inspection"}
status:
  admission: {fence: 43}             # set by the coordinator
  phase: Executing
```

The spec is immutable. Only the coordinator writes status.

| Kind | Required holder | Inputs |
| --- | --- | --- |
| `DeviceInstallation` | `Allocation` | Image digest, bootstrap reference and digest, boot interface, target disks |
| `DevicePowerOperation` | `Allocation` or `None` | `On`, `ForceOff`, `ForceRestart`, or `PowerCycle` |
| `DeviceWipe` | `Release` | Cleaning policy and digest pinned at allocation, target disks |
| `DevicePreparation` | `None` | Preparation plan and digest |

For example, an installation:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: DeviceInstallation
metadata:
  name: alloc-7f3c2-install-01
  namespace: default
spec:
  deviceRef: {name: server-example-001}
  authority:
    allocationRef: {name: alloc-7f3c2}
  image: {name: debian-12, digest: "sha256:9f4ad8d8b8a6..."}
  bootstrap:
    secretRef: {project: consumer-a, namespace: default, name: debian-12-bootstrap}
    digest: "sha256:0c2b77e1d9a4..."
  bootInterface: primary
  targetDisks: [disk-0]
```

Operations carry references and digests for secrets, never their content. This
covers bootstrap data, user data, SSH keys, and customer boot endpoint
credentials. Fleet treats a bootstrap payload as secret as a whole. Only the
worker running the operation reads it.

| Phase | Meaning |
| --- | --- |
| `Pending` | Not yet admitted. |
| `Admitted` | Custody names the operation. Nothing has been sent. |
| `Executing` | The site started the operation. |
| `Succeeded` | The kind's success test passed. |
| `Failed` | Nothing was sent, or the server refused and did not change. |
| `Unknown` | A command may have reached the server, and Fleet cannot prove the result. |

Conditions follow the [machine lifecycle status
contract](../machine-lifecycle/README.md#status-contract).

### Admission

The coordinator admits an operation with one conditional update to custody:

![One Device operation from request to outcome](diagrams/operation-sequence.svg)

The coordinator admits an operation only if all of these hold:

1. An `allocationRef` names the current holder, or a `serviceRequest` finds
   holder `None`.
2. The creator may create that kind. See [Authorization](#authorization).
3. No operation is active.
4. The Device is not retired, and its identity matches current discovery.

The update sets the active operation and raises the fence. A staff operation
also sets the holder to `Service`. If another writer changed custody first, the
update fails, and the coordinator evaluates again.

Operations for the current holder wait in `Pending`. Staff operations are
refused instead, because a queued staff request can run after its reason is
gone. Pending operations expire after a limit set per kind.

### Site execution

The site runs one short-lived worker per operation. The worker:

1. reads the BMC endpoint, system UUID, boot MAC, and BMC key digest captured
   at admission;
2. reads that Device's BMC credential from the site;
3. checks the BMC's TLS key and the system identity, and fails without sending
   anything on a mismatch;
4. records each step before sending it, and after a crash queries the server
   instead of sending again; and
5. returns a result with the admission fence.

Management rejects a result with a stale fence. Sites have no credentials for
project APIs.

### Unknown outcomes

An `Unknown` outcome sets a recovery hold on custody. The holder stays in place.
While the hold exists, the coordinator admits only an operation that can settle
it, and the server cannot return to `None`. A held allocation can still move to
`Release`.

| Kind | What settles the hold |
| --- | --- |
| Power | Fresh discovery evidence of the power state and a staff recovery record |
| Installation | A later installation or wipe that succeeds |
| Wipe | A later wipe that succeeds |
| Preparation | A later preparation that succeeds, or retirement |

Each recovery record names the held operation, its evidence, and the decision.
Fleet never retries an operation automatically.

### Consistency without a database

A conditional update on one Kubernetes object is atomic. Fleet keeps every value
that must change together on the custody object:

- **Commit point:** The custody update admits an operation. After a crash, the
  coordinator resumes delivery from custody.
- **Duplicates:** Operation names are deterministic, and custody names the one
  admitted.
- **Quota:** Milo holds quota. Metal claims quota before allocation and releases
  the claim if no server is free.
- **History:** Fleet keeps finished operations for a set period. Platform audit
  and activity records keep history after that.

### Fleet as a Metal provider

| Metal request | Fleet action |
| --- | --- |
| Machine admitted | The allocator sets the holder to `Allocation`. |
| Installation created | The provider creates a `DeviceInstallation`. |
| `powerState` changed | The provider creates a `DevicePowerOperation`. |
| Machine deleted | Custody moves to `Release`, and the release controller creates `DeviceWipe` operations. |

The Metal projector reports results without operation names or inventory
details. Other Metal providers do not use these resources.

### Capacity status

The capacity projector derives MachineCapacity conditions:

- `Prepared` needs a succeeded preparation under the current plan.
- `Healthy` needs current evidence from preparation and discovery.
- `Available` also needs holder `None`, no recovery hold, and an enabled
  allocation policy.

### Authorization

| Identity | May create |
| --- | --- |
| Fleet provider for Metal | `DeviceInstallation`, `DevicePowerOperation` |
| Release controller | `DeviceWipe` |
| Staff power operator | `DevicePowerOperation` |
| Staff preparation operator | `DevicePreparation` |
| Fleet coordinator | `DeviceCustody` and all operation status |

Staff roles can read custody and operations. No staff role can read bootstrap
secrets.

### Implementation decisions

Record these decisions before implementation:

- Whether the provider project API supports the needed conditional updates with
  allocator-level availability.
- What makes a preparation plan qualified, and who approves it.
- What evidence each recovery needs, and whether a second person confirms it.
- How long Fleet keeps finished operations.
- Which staff roles can see the customer that holds a server.

### Acceptance tests

These tests are required and not implemented:

- Two allocators race for one server, and one wins.
- A staff operation races an allocation, and one is admitted.
- A staff operation targets an allocated, releasing, or held server, and Fleet
  refuses it without sending anything.
- A lost BMC response ends each kind as `Unknown` and sets a hold.
- A worker with a stale fence is rejected.
- A coordinator restart between admission and delivery delivers once.
- A rotated BMC key or credential is used, and a key mismatch sends nothing.
- A Machine deleted during installation waits for the installation outcome
  before wiping.

## Production readiness review questionnaire

These controls are proposed, not implemented.

### How do operators enable or disable the feature?

Register the kinds and IAM resources, then deploy the coordinator and site
executors. To disable it, pause admission. Running operations and recoveries
continue.

### Does enabling the feature change any default behavior?

No. Custody starts at `None`, and creating it has no effect on a server.

### What must operators monitor?

Recovery holds, `Unknown` operations, long `Release` periods, rejected fences,
and sites that stop reporting.

### What does the feature depend on?

Milo for project APIs, IAM, and quota. Federation to reach sites. Inventory and
BMC discovery for Device facts.

### Does the feature add API types or calls?

It adds `DeviceCustody` and four operation kinds. Each custody change is one
write.

## Implementation history

- 2026-11-24: Proposed.

## Drawbacks

- Every action depends on the coordinator and the custody object.
- Four operation kinds add resources to register and grant.
- A recovery hold can keep a server out of use while evidence is missing.
- The allocator claims one server at a time, so it cannot claim several servers
  atomically.

## Alternatives

- **One operation kind with a `type` field:** IAM grants permissions per kind,
  not per field. A staff role that can request power could also request an
  installation.
- **Desired state per Device:** Wipes and resets are one-time actions.
  Reapplying desired state can repeat them, and recovery needs the exact
  admitted operation.
- **Private records for customer work:** Customer and staff work would keep two
  admission paths and two locks.
- **A relational ledger:** It adds a database. One object per server gives the
  same guarantee for conflicts on that server.
- **A lock per allocation:** Staff work has no allocation.

Other systems draw similar lines. OpenStack separates tenant instances from
Ironic hardware nodes and runs manual cleaning only in a state no tenant holds.
Metal3 records the holder on each `BareMetalHost`. Kubernetes binds a
`PersistentVolumeClaim` to a `PersistentVolume` with one conditional update.

## Infrastructure needed

Testing needs one provider project, two consumer projects, one site, and BMC
emulators that can drop responses and rotate TLS keys. Hardware qualification
needs reachable BMCs and a boot network.

[power-pr]: https://github.com/datum-cloud/fleet/pull/173
[execution-pr]: https://github.com/datum-cloud/fleet/pull/62
[admission-pr]: https://github.com/datum-cloud/fleet/pull/60
