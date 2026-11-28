---
status: provisional
stage: alpha
---

# BMC hardware telemetry

[Documentation overview](../../README.md) · [Architecture guide](../../architecture/README.md)

Everything in this document is proposed. Nothing described here is implemented
or verified. Metric names, labels, and intervals are provisional.

- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
  - [User stories](#user-stories)
  - [What tenants get](#what-tenants-get)
  - [Relationship to other proposals](#relationship-to-other-proposals)
  - [Risks and mitigations](#risks-and-mitigations)
- [Design details](#design-details)
  - [Principles](#principles)
  - [Architecture and data flow](#architecture-and-data-flow)
  - [Target and credential lifecycle](#target-and-credential-lifecycle)
  - [Telemetry account operation](#telemetry-account-operation)
  - [Tenant attribution](#tenant-attribution)
  - [Power data](#power-data)
  - [Failure behavior](#failure-behavior)
  - [Security](#security)
  - [Upstream work](#upstream-work)
  - [Implementation phases](#implementation-phases)
  - [Open questions](#open-questions)
- [Production readiness review questionnaire](#production-readiness-review-questionnaire)
- [Implementation history](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)

## Summary

Tenants will see the health and power draw of the servers allocated to them
in their project's metrics: temperatures, fan speeds, power supply status, and
power use. They can chart it, alert on it, and send it to their own monitoring
tools, the same way they use other project metrics.

Operators will see the same data for every server at every site, and get an
alert when collection stops.

Each tenant sees only their own servers. When a server is released, its data
stops reaching that tenant, and the next tenant never sees it.

## Motivation

Tenants on dedicated servers cannot see their hardware's health. They cannot
size a workload against real power draw, report on power use for
sustainability, or catch a failing fan or power supply before it causes an
outage. The management network that reports this data is private by design, so
they cannot reach it themselves.

Operators have the same gap. Without a fleet-wide view, they log in to
individual servers to find a hardware fault, and they learn about a failing
part from the tenant's outage.

### Goals

- A tenant can see temperatures, fans, power supplies, and power use for each
  server in their project.
- A tenant can alert on a hardware fault and export the data to their own
  monitoring stack.
- A tenant sees only their own servers, and only while they hold them.
- An operator can see hardware health across all sites in one place.
- An operator learns when collection stops.
- A missing reading is shown as missing, never as a healthy or zero value.
- Reading the data never changes a server. Setup adds a read-only
  credential while the server is idle and unallocated.

### Non-goals

- A new metrics store, query tool, or tenant access model. Tenants use the
  platform's existing ones.
- Building a custom collector. Fleet adopts an existing open-source one.
- Data from inside the tenant's operating system.
- Prebuilt dashboards and alert rules beyond collection failures.
- Precision beyond what the hardware reports.

## Proposal

Fleet will read hardware health and power data from each server's management
controller (BMC) and add it to the platform's metrics. Operators see every
server. Tenants see the servers allocated to their project.

### User stories

- **Tenant sees hardware health.** A tenant with `server-a` in `<project>` can
  query its temperatures, fan speeds, power supply status, and power use in
  their project's metrics.
- **Tenant alerts on a failing part.** A tenant sets an alert on a power supply
  fault on `server-a` and is notified before the server fails.
- **Tenant exports metrics.** A tenant sends their hardware metrics to their own
  monitoring stack using the platform's existing export policy.
- **Tenant releases a server.** When a tenant releases `server-a`, they stop
  seeing its data. The next tenant of `server-a` never sees the previous
  tenant's data, and the reverse.
- **Operator sees health across sites.** An operator opens one view of hardware
  health for every server at `site-1` and every other site, whether allocated
  or not.
- **Operator learns collection stopped.** When Fleet cannot read a server or a
  whole site, the operator gets an alert, and the tenant sees a gap.

### What tenants get

For each server in their project:

- **Metrics:** temperatures, fan speeds, power supply status, power use, memory
  and storage health, overall system health, and firmware versions. The
  final list is an open question.
- **Only theirs.** The data is scoped to their project. Only the server's
  stable name is shown; no address, serial number, or MAC.
- **Starts a few minutes after handover.** The figure is provisional and
  depends on the collection interval. It stops within a few minutes of release
  beginning.
- **Gaps are gaps.** When a reading is unavailable, it is missing, never a fake
  zero. Tenants get this data only once the collector reports missing readings
  as missing.
- **Fresh.** Readings are a few minutes old.
- **Retention.** A short period, still an open question.
- **Read-only.** Reading the data never changes a server. Setup adds a
  read-only credential while the server is idle and unallocated.

### Relationship to other proposals

- **[Device operations](../device-operations/README.md).** Changes to a
  server's management controller are coordinated there, so telemetry setup
  never runs on a server a tenant holds, or while another operation is in
  progress.
- **[Machine allocation and installation](../machine-lifecycle/README.md).**
  Hardware metrics start when the machine is handed to the tenant and stop
  within a few minutes of release beginning.
- **Server discovery and per-site management access.** Telemetry builds on
  them. They are proposed separately and are not yet in this repository.

### Risks and mitigations

- **Data is only as precise as the hardware reports.** Some sensors are coarse,
  and some firmware omits or delays readings. Operators check each hardware
  family before enabling it, and Fleet cross-checks power later.
- **Data could reach the wrong tenant.** Data starts only after a short delay
  following handover and stops when release begins. Unknown ownership produces
  no data.
- **Collection could disturb a server.** Reading the data never changes a
  server. Setup adds a read-only credential while the server is idle and
  unallocated, separate from the credential Fleet uses to operate the server.

## Design details

### Principles

- Time series live only in the platform metrics store. There is no
  Fleet-specific store and no series in resource status.
- Telemetry never changes records. It does not create, edit, or delete
  inventory, allocation, or custody, and it does not make a device available.
- Each metric family has one writer. The exporter is the single writer for
  BMC-sourced families. Fleet writes the allocation info series. The recording
  rule writes tenant series.
- Labels carry identity and numbers only: no addresses, serial numbers, MAC
  addresses, or credentials.
- Disagreement between sources is shown, not resolved.
- Unknown is not zero and is never read as ready.

### Architecture and data flow

Fleet adopts the open-source `idrac_exporter` and contributes upstream
instead of writing, forking, or embedding a collector.

The first diagram shows the systems involved, grouped by owner and trust
boundary. The second shows how data flows through them.

![Systems involved in BMC hardware telemetry, grouped by owner and trust boundary, marked as existing, new, or later](diagrams/components.svg)

![BMC hardware telemetry from server BMCs to the operator view and the tenant view](diagrams/architecture.svg)

1. **Source.** Each BMC exposes health and power data over Redfish (HTTPS). BMCs
   are reachable only on the site's out-of-band network.
2. **Access.** The per-site BMC gateway holds a VPN tunnel into that network,
   where Fleet's Redfish discovery already runs. The exporter runs in the
   gateway and reuses the tunnel. No new network path is opened. Only the
   scraper can reach the exporter, and its management endpoints are not
   reachable outside the gateway. No default or fallback BMC is configured. A
   network policy admits only the scraper to the exporter and to the renderer's
   target list, which carries BMC addresses.
3. **Collector.** The exporter queries one BMC per scrape, live, on request from
   the scraper. It covers thermal, fans, PSUs, power, memory, storage, system
   health, and firmware. A scrape takes several seconds per BMC, so intervals
   are minutes.
4. **Targets.** The renderer builds one snapshot from Fleet inventory. From it,
   it writes the exporter's configuration and serves the scraper's target list
   with device labels. See [target and credential
   lifecycle](#target-and-credential-lifecycle).
5. **Operator view.** The scraper, in the site's cell cluster, attaches
   physical identity (device and site) and remote-writes to the platform
   metrics store.
6. **Tenant view.** The recording rule joins hardware series to the allocation
   info series and adds the tenant's project label. An allowlist federation
   copies only those series, with an allowed label set, into the platform's
   tenant-facing metrics store, which has short retention. Tenants reach it
   through the query API, which enforces project scoping.

#### What Fleet adds

| Piece | Owner |
| --- | --- |
| Renderer and scraper target list | Fleet |
| Telemetry account provisioning, as a custody operation | Fleet |
| Network policy for the exporter and target list | Fleet, with the site deployment |
| Allocation info publisher | Fleet |
| Recording rule, federation allowlist, tenant label allowlist | Platform configuration |
| Tenant store, query API, export policy | Existing platform, unchanged |

The tenant label allowlist drops the exporter's instance label, addresses,
serial numbers, and MAC addresses. Tenants see the project, a stable device
name, and metric labels such as sensor name.

### Target and credential lifecycle

Fleet inventory is the source of truth. A BMC becomes a target when its device
has a BMC address and an active telemetry account.

**Snapshot.** The renderer validates the snapshot before using it. It writes
the exporter's configuration to memory-backed storage in the gateway, readable
only by the exporter, replacing the file atomically (write, then rename). The
configuration is never on durable storage. The exporter must be set to reload
on file change, which is off by default. The renderer also serves the scraper's
target list from the same snapshot, with device labels. Targets, credentials,
and identity come from one snapshot, so a series is labeled by device from its
first scrape and no address join is needed. If upstream adds per-host labels to
the exporter's own discovery output, the renderer's list can be dropped.

| Event | Behavior |
| --- | --- |
| Add | The device gains an address and an active telemetry account. The next snapshot includes it, and it is scraped within about a minute. |
| Rotate | Order-safe. Fleet creates the new account and verifies that it can log in. Only then does the renderer publish it. Fleet deletes the old account after a scrape succeeds with the new one. This avoids locking out a live scrape. It runs only when the device is idle and unallocated. |
| Remove | The device leaves the scraper's list at once. The exporter keeps the entry, and its credential in memory, until it restarts. The renderer restarts the exporter when removed devices' credentials are still held, batched to at most once a day. Scrapes interrupted by a restart read as unknown. |

**Session limits.** BMCs allow few concurrent sessions, and discovery uses them
too. The exporter's concurrency is set explicitly to fit each BMC's session
budget after discovery's share. Rotation stays disabled until the exporter
backs off after failed authentication. Today it falls back to basic
authentication and retries on every scrape, which can lock out an account.

### Telemetry account operation

Reading telemetry is not a custody operation; only account changes are.

**Telemetry account.** Each BMC gets a dedicated read-only account, separate
from the operations credential. Fleet creates it through the BMC's own account
service using the operations credential, and stores it in its own Secret.
Telemetry never uses the operations credential to read data.

Creating, rotating, and deleting the account are writes to the BMC, so they
are a new custody operation kind, `DeviceTelemetryAccount` (name provisional).
It follows the [device operations](../device-operations/README.md#operations)
model:

| Kind | Creator | Authority | Required holder | Inputs |
| --- | --- | --- | --- | --- |
| `DeviceTelemetryAccount` | Fleet telemetry controller | `serviceRequest` | `None`, which becomes `Service` while it runs | Action (create, rotate, or delete), account name, sealed password reference and digest, expected system UUID |

The new password is generated by Fleet and stored in the renderer's Secret,
which is the one durable copy for the account's lifetime. The telemetry
controller writes it through the sealed-store path that device operations
defines for bootstrap payloads, as an opaque reference plus a digest. This
proposal extends that path to the telemetry controller. The site deletes its
copy when the operation ends. The password never appears in the operation
object.

The operation runs only from holder `None`, so it never runs on an allocated
device or during release, and it needs no new authority over a tenant's
machine. Creation, rotation, and deletion happen when the device is idle and
unallocated, including after release cleaning returns it to `None`. Scheduled
rotation of an allocated device waits until the device is idle again.

While the `Service` hold lasts, the allocator is refused with `HolderMismatch`
and retries. The operation is short, and that brief unavailability is accepted.
The hold ends on any terminal result except `OutcomeUnknown`, which places a
recovery hold. A later successful telemetry account operation, or a recovery
operation that retries it, settles the hold. The controller runs at most one
telemetry account operation per device per day, so a device cannot be starved of
allocation.

If custody refuses the request, the controller gets the shared reason
(`CustodyUnverified`, `RecoveryHold`, `OperationInProgress`, or
`HolderMismatch` for an allocated device), and retries later with back-off.

Emergency response to a compromised telemetry credential needs no BMC write.
The renderer drops the device from the target list and the configuration
immediately. Fleet deletes or rotates the account when the device is next idle,
or through the existing operator recovery path.

This adds one kind to the device operations list, so that proposal's count of
operation kinds changes from five to six when this lands. It also extends the
`Service` hold, which that proposal calls a staff hold, to a Fleet controller
under the same admission rules, and adds a row for this kind to its recovery
table. Both change that proposal too.

### Tenant attribution

Fleet resolves a tenant from this chain of identities: Device (the physical
asset), MachineCapacity (provider supply), MachineAllocation (the
allocation), and the customer Machine in the tenant's project. The allocation
info series maps a device to the tenant that the Machine's project reference
names. That reference is typed. Only Project tenants get series in this
proposal.

**Activation time.** This proposal adds a write-once activation time to the
allocation record. Machine-lifecycle gives allocation records one writer, the
coordinator, and gives public Machine status one writer, the projector. The
coordinator writes the activation time, and no other component can, when it
hands the machine to the customer: for allocate and install, when the first
initial installation succeeds and passes the activation gate; for allocate
only, when it has verified network and power handoff for the prepared
machine. It uses its own
evidence and does not read projected Ready status. The record is durable, so a
later reinstall or Ready drop never changes it. If no activation time is
recorded, no tenant series are produced.

**When the info series exists.** The allocation publisher, the only writer of
the series, publishes it while the allocation has a recorded activation time,
the device's custody holder is that allocation, and the Machine's `Releasing`
condition is not true. Before activation the Machine is bound but not handed
off, so a device is not attributed. The series ends on release or a
custody change, never on other Machine conditions.

**Guard window.** The info series carries the activation time as its value.
The recording rule applies the guard window against it: tenant series start one
full scrape interval after that time, and samples inside the window are
dropped.

**Fail closed.** If the info series is missing or stale, or two info series
exist for one device, the join produces no tenant series. Unknown or
conflicting ownership is never a default tenant.

**Latency.** Series appear for a new tenant roughly two scrape intervals plus a
minute after activation, about five minutes at a two-minute interval. When
release begins, the info series is withdrawn within about a minute, and in the
worst case after the metrics store's staleness window, so tenant data stops
within a few minutes. Readings are a few minutes old, set by the collection
interval. Samples taken early in release can still be attributed to the
outgoing tenant during that time. That is acceptable: it is the outgoing
tenant's own data, and the next tenant needs cleaning and a new activation
first. The operator view is not delayed.

Unallocated hardware, and hardware in any other state, is visible to operators
only. The allocation info series is a view of allocation records, not a second
source of truth. Telemetry never changes allocation.

### Power data

These are assumptions the [coverage check](#coverage-check) confirms for each
vendor and firmware family:

- A chassis power reading can be absent or stale on some firmware.
- PSU input readings are coarse, because the sensors have low resolution.
- In-band readings are more accurate but are not available remotely.

So Fleet does not rely on a single chassis reading. The primary remote signal
is the sum of PSU input power, averaged over time. PDU readings, added later
through a standard SNMP exporter with the PDU's REST API as a fallback, give an
independent cross-check. When the BMC and PDU disagree, both are shown. Remote
IPMI is not assumed to be available.

The tenant-facing power metric is not chosen. See [open
questions](#open-questions).

#### Coverage check

Before Fleet enables a BMC vendor or firmware family, operators confirm what
the exporter covers on it and record the metric families supported. A family
that is not recorded is not enabled.

### Failure behavior

Target behavior, which the exporter does not yet meet:

| Condition | Result |
| --- | --- |
| BMC unreachable | The scrape fails and the device's series go stale. |
| Field missing from a BMC response | The series is absent, not zero. |
| Exporter or tunnel down | Targets at the site stop, series go stale, and an operator alert fires. |
| Allocation info missing or stale | No tenant series are produced. |

Tenants see gaps, not false values.

**Today's exporter.** Missing fields read as 0. Power output can fall back to a
cached last value. A failure in one section of a BMC response still returns a
successful scrape and only increments an error counter. A stale or zero value
is a false value for a tenant, so exposing anything to tenants (phase 2)
requires the upstream fix to report absent fields and the removal of the stale
fallback. Operators alert on scrape failures and on the error counter.

The exporter should also report whether each configuration reload succeeded,
so a rejected reload alerts instead of silently keeping the old configuration.

### Security

- **Read-only, dedicated account.** The telemetry account cannot change power
  state or settings, and it is not the operations credential.
- **Secrets.** Credentials exist only in Secrets and in the exporter's
  configuration, on memory-backed storage readable only by the exporter and
  never on durable storage. They never appear in labels, records, metrics,
  logs, or Git.
- **Tenant labels.** The allowlist above removes addresses and identifiers.
  Only joined, allowlisted series reach the tenant store, and tenants cannot
  read the operator store.
- **Exporter surface.** A network policy admits only the scraper to the
  exporter and to the target list. Management endpoints are not admitted.
- **TLS verification is skipped in phases 1 and 2.** This is an accepted risk.
  Until [fleet#234][pin-issue] lands, a host that answers at a BMC's address
  would receive the telemetry credential. The credential is read-only and
  separate from the operations credential, which limits the damage to read
  access to that BMC's telemetry. The recorded key pin does not gate scraping
  until the pin is enforced.

### Upstream work

Fleet will contribute small changes to the exporter rather than fork it:

1. Drop hosts removed from the configuration on reload. Until then, Fleet
   restarts the exporter occasionally to purge them.
2. Report missing fields as absent rather than 0, and remove the stale-value
   fallback for power.
3. Report configuration reload success or failure as a metric.
4. Back off after failed authentication instead of retrying on every scrape.
   This is required before rotation is enabled.
5. Add a per-host public-key pin. A pin change must reset the BMC session.
6. Allow optional per-host labels in the discovery output. If accepted, the
   renderer's target list can be dropped.

### Implementation phases

1. **Operator-only scraping.** Renderer, target list, telemetry accounts
   (a custody operation, without rotation until the upstream back-off lands),
   and exporter at one site. Series appear in the operator view only. Alerts on
   scrape failures and the error counter. The coverage check gates each
   vendor. TLS verification is skipped, as an accepted risk.
2. **Tenant view.** Allocation info publisher, recording rule, label
   allowlist, and federation. Requires the upstream absent-not-zero fix and
   removal of the stale fallback. TLS verification is still skipped.
3. **Cross-check and pin enforcement.** PDU collection as an independent power
   signal, and enforcement of the key pin once available upstream.

Each phase is accepted only with verification. None is implemented.

### Open questions

- Do metrics from a site's cell cluster reach the platform store through an
  existing path, or do off-cluster sources need authenticated ingest? The
  platform lacks the latter today.
- Which metrics belong in the tenant allowlist, and which is the tenant-facing
  power signal?
- Can sites reach their PDUs for collection?
- What retention should the tenant store use?
- Should Namespace tenants get series, and how are they attributed?
- How should metric family names and identity labels align with Fleet's
  telemetry naming once it is defined? The exporter's metric prefix is
  configurable.

## Production readiness review questionnaire

These controls are proposed, not implemented.

### How do operators enable or disable the feature?

Enable collection per site, and per hardware family after its coverage check.
Tenant visibility is enabled separately, in phase 2. To disable tenant
visibility, stop publishing allocation info. Tenant series then stop, because
the join fails closed. To disable collection for a server, remove it from the
target list. Scraping stops at once, but the server's credential stays in the
collector's memory until the next daily restart.

### Does enabling the feature change any default behavior?

No allocation or inventory behavior changes. A server is briefly unavailable to
the allocator while its telemetry account is created, rotated, or deleted.

### What must operators monitor?

Scrape failures and the exporter's error counter, collection loss per site,
failed configuration reloads, authentication failures per server, servers with
conflicting or missing allocation info, and telemetry account operations in
recovery.

### What does the feature depend on?

BMC discovery and the per-site BMC gateway, Fleet inventory and allocation
records, the platform's metrics store, query API, and export policy, the device
operations custody model and its sealed input store, and the upstream
exporter.

### Does the feature add API types or calls?

No new public API types. It adds one custody operation kind, a write-once
activation time on the allocation record, and platform configuration for the
join and the tenant allowlists. Each server is read once per scrape interval.

## Implementation history

- 2026-11-27: Proposed.

## Drawbacks

- It depends on an upstream exporter that Fleet does not control. Fixes need
  upstream changes or a wait.
- Each BMC needs its own read-only account, which Fleet must create, rotate,
  and delete.
- Coarse hardware sensors limit precision, especially for power.
- A server is briefly unavailable for allocation while its telemetry
  credential is set up or rotated.
- It requires changes to device operations: one more operation kind, the
  `Service` hold for a Fleet controller, and a recovery table row.
- Tenant data starts a few minutes after handover.
- TLS verification of BMCs is skipped at first, an accepted risk.

## Alternatives

| Alternative | Why not |
| --- | --- |
| Write a Fleet exporter on a Redfish library | Duplicates a maintained project. |
| Telegraf Redfish input | Covers only power and thermal, with a heavy dependency tree. |
| `ipmi_exporter` | Needs remote IPMI, which is not assumed reachable. |
| `redfish_exporter` | Unmaintained. |
| Embed an exporter as a library | The chosen exporter is not importable, and it is not needed. |
| Use the exporter's own discovery output | It has no device labels today, so series would be joined by address. Revisit if upstream adds labels. |

[pin-issue]: https://github.com/datum-cloud/fleet/issues/234
