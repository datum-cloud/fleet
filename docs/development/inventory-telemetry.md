# Inventory power telemetry (proposed)

Status: **proposed**. Nothing in this document is implemented except the
provider UI's telemetry interface and its synthetic preview source (see
[What exists today](#what-exists-today)). Metric names, labels and intervals
are provisional.

[Staff physical inventory UI](staff-inventory-ui.md) · [Hardware catalog](hardware-catalog.md)

## Why

The staff inventory UI draws power paths from records: PowerFeed ratings,
Cable records and PDU internal mappings. Operators also want to see what is
actually drawn: feed load against its rating, per-outlet current, PSU input
power and PSU status. That is observed telemetry. It must not be confused
with records, and it must not be stored where records live.

## Principles

- **Time series live in the platform metrics store**, the existing source the
  platform already scrapes and queries. There is no Fleet-specific store, and
  no series in CRD status.
- **At most, slow-changing summaries may later go to status.** Examples: a
  PSU reports Fault, or an outlet breaker tripped. Each would be a dated,
  sourced condition written by one collector, and is a separate proposal.
- **Telemetry never changes records.** It never creates, edits or deletes a
  Cable or PowerFeed, never fills in a missing Cable, and never sets a
  verification condition.
- **Telemetry never verifies redundancy.** Two PSUs drawing from two feeds
  does not prove the feeds are independent upstream. Views keep "Separate
  feeds in records do not prove independent supply".
- **Disagreement is shown, not resolved.** Load observed on an outlet that no
  Cable records is shown as "observed and recorded disagree", with links to
  the records. Nothing is changed automatically.
- **One writer per metric family.** Each family below has exactly one
  collector kind that writes it.
- **No secrets.** Labels and values carry inventory identities and numbers
  only: no endpoints, addresses, community strings, credentials or tokens.

## Identity labels

Every series is keyed by inventory identity, never by an address or hostname:

| Label | Value | Notes |
| --- | --- | --- |
| `namespace` | Inventory namespace (`default`) | |
| `kind` | `PowerFeed` or `Device` | The record that owns the series |
| `name` | Record name | For example `example-pdu-a01` |
| `uid` | Record UID | Separates a recreated record from an old one |
| `component` | Component or termination id | Absent for whole-record series; for example `outlet-6`, `psu-1` |
| `collector` | `pdu` or `bmc` | The collector kind that wrote it |
| `collector_instance` | Opaque collector name | Never an endpoint |

How a collector maps hardware to these identities (a PDU's outlet index to
`outlet-6`, a Redfish `PowerSupplies` member to `psu-1`) is part of its own
configuration, keyed by the Device record name and its DeviceType template
ids. A collector that cannot map a reading leaves it unlabelled and does not
publish it under a guessed component.

## Metric families

| Metric | Type | Unit | Writer | Labels beyond identity |
| --- | --- | --- | --- | --- |
| `fleet_pdu_outlet_current_amperes` | gauge | A | PDU collector | |
| `fleet_pdu_outlet_power_watts` | gauge | W | PDU collector | |
| `fleet_pdu_outlet_apparent_power_voltamperes` | gauge | VA | PDU collector | |
| `fleet_pdu_outlet_output_on` | gauge | 1 or 0 | PDU collector | |
| `fleet_pdu_outlet_breaker_tripped` | gauge | 1 or 0 | PDU collector | |
| `fleet_pdu_inlet_power_watts`, `..._apparent_power_voltamperes`, `..._current_amperes` | gauge | W, VA, A | PDU collector | `component` is the inlet id |
| `fleet_power_feed_termination_current_amperes`, `..._power_watts`, `..._apparent_power_voltamperes` | gauge | A, W, VA | PDU collector | `kind="PowerFeed"`; only for a termination whose meter (a metered PDU inlet or metered whip) the collector is configured to read |
| `fleet_power_feed_apparent_power_voltamperes` | recording rule | VA | platform metrics store | Sum of the feed's termination series; `partial="true"` when any termination has no series |
| `fleet_psu_input_power_watts` | gauge | W | BMC collector | |
| `fleet_psu_input_voltage_volts` | gauge | V | BMC collector | |
| `fleet_psu_status` | gauge (state set) | 1 for the current state | BMC collector | `state` is `OK`, `Fault` or `Absent`; optional `reason` from a fixed list |
| `fleet_device_input_power_watts` | gauge | W | BMC collector | Whole-device input from Redfish `PowerSubsystem`, when the BMC reports it |
| `fleet_telemetry_stale_after_seconds` | gauge | s | each collector | The collector's own freshness window, per `collector_instance` |

## Collectors

**PDU collector.** Reads metered and switched PDUs over SNMP (vendor PDU
MIBs) or Redfish (`PowerDistribution`, `Outlets`, `Mains`). It writes every
`fleet_pdu_*` family and the feed-termination families for meters it is
configured to attribute to a PowerFeed termination. Expected scrape interval:
**60 s**.

**BMC collector.** Reads server BMCs over Redfish `PowerSubsystem`
(`PowerSupplies` members: `InputPowerWatts`, `InputVoltage`, `Status`). It
writes `fleet_psu_*` and `fleet_device_input_power_watts`. Expected scrape
interval: **120 s**, because BMCs answer slowly and must not be polled hard.
BMC credentials stay with the collector and never appear in labels or in
Fleet records.

Neither collector reads or writes inventory records. Collector deployment,
discovery and credentials are out of scope here and belong with the platform
metrics owners.

## Staleness

- Each collector publishes `fleet_telemetry_stale_after_seconds`, set to
  **five scrape intervals** (300 s for PDUs, 600 s for BMCs). The UI never
  picks a window; a value without one is invalid and shows as "Not reported".
- A value is **stale** when `now − sample time > stale_after_seconds`. Stale
  values are shown greyed and labelled "Stale · 3 h ago", never green, and
  never count as a disagreement or as load.
- A **missing** series is "Not reported", never 0. A reported 0 is shown as
  0 with its age.
- Capacity comparisons use the feed's recorded rating: single-phase
  `S = V × I`, three-phase `S = √3 × V × I` (line-to-line voltage), DC
  `P = V × I`; the continuous limit is 80% of that. Load above 80% of the
  continuous limit is "high", above the limit "over"; both get a glyph as
  well as colour. A rating is not available power.

## What exists today

- `ui/provider/src/lib/telemetry.ts` defines the `TelemetrySource`
  interface: `read({ kind, namespace, name, componentID? })` returns
  `PowerTelemetry` (`powerW`, `apparentPowerVA`, `currentA`, `outputState`,
  `health`), each an `Observed<T>` with `origin` (collector kind and
  instance), `observedAt`, `staleAfterSeconds` and an optional 24-hour
  `series`.
- The default source is `none`: every view reads "Not reported" and "No
  telemetry source configured". This is what staging and production show.
- `ui/provider/src/lib/telemetry-mock.ts` is a synthetic source for the
  fixture preview only, with `normal`, `high-load`, `stale`, `missing` and
  `mismatch` scenarios (`?telemetry=<scenario>`, or `none`). It is loaded
  behind the same build-time flag as the fixture inventory, and
  `src/test/production-bundle.test.ts` builds the production bundle and fails
  if any mock-only string is in it.
- Views show telemetry as a load tint and status marks on outlet and PSU
  tiles, one "Updated N s ago" line per outlet group, and the values in the
  component card's Telemetry section. The collector, instance, observed time
  and freshness window are behind the card's More. See
  [Power path and telemetry](staff-inventory-ui.md#power-path-and-telemetry-implemented-telemetry-mocked).
- There is no metrics client. A real source would implement
  `TelemetrySource` by querying the platform metrics store through the
  portal's existing proxy, using the labels above, and would return nothing
  for identities the reader cannot read.

## Open questions

- Which platform metrics endpoint the staff portal proxy may query, and with
  what authorization, so that a reader without inventory access learns
  nothing from series labels.
- Whether feed-level load should come only from the recording rule, or also
  from facility branch-circuit monitoring when a facility provides it.
- Whether PSU Fault and breaker-tripped summaries should become Device status
  conditions, and which collector would own that writer.
