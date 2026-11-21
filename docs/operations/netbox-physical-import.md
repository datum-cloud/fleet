# Optional NetBox review aids for manual inventory

[Physical inventory](physical-inventory.md) · [Inventory proposal](../proposals/fleet-management/inventory.md) · [Detailed proposed contracts](../proposals/fleet-management/inventory-reference.md)

Start with [manual entry and operator review](physical-inventory.md#manually-record-a-small-installation).
Physical inventory needs neither NetBox access nor a bulk importer. Operators
write and review records individually. New/imported records may use approved
direct API creation; existing Flux-owned records retain that writer until an
explicit reviewed ownership transition.

This guide covers **optional, explicitly invoked review aids**: a GET-only snapshot
command and offline candidate generation. They help compare an existing source
when an operator chooses to use it; they are not an automated migration. There
are no scheduled imports, discovery or sync controllers, background NetBox updates,
auto-apply or auto-delete. Neither command applies anything to Fleet or NetBox.
The retained `--handoff` option writes candidate files for human review only; it
does not hand execution to Flux, change Git, or transfer ownership. No verification,
reservation, adoption, enrollment, or provisioning controller is included.

## What exists and what remains unknown

`Facility`, `Rack`, `DeviceClaim`, `Device`, `Cable`, and `PowerFeed` live in the
existing provider project's Milo control plane. No database, management-cluster
copy, new catalog service, or independently running importer was introduced.
`config/milo` installs their CRDs, ProtectedResources and inventory roles. Existing
consumer Metal roles still grant no inventory access; inventory roles do not
grant status writes. Staff use their own identity through the existing Milo proxy.

The CRDs validate local shapes, provenance, immutable asset/claim identity,
placement scope, component uniqueness, internal component positions, and Cable
lane multiplicity. The offline planner checks local references, compatible
component categories, and conflicting cable terminations against the supplied
Fleet snapshot. **These checks are not concurrent reservations or cross-object
API admission.** A direct API writer can still create dangling references or
cross-object conflicts; do not interpret successful creation as acceptance.
No qualified physical verification, dependency reservation, deletion protection,
move/replacement handover, electrical budget, or eligibility publisher exists.
No status field is populated. Free space, active binding, redundancy and readiness
remain Unknown. Existing editor delete permissions are not a safe retirement API.

### Compatibility and migration decisions

- Existing long-lived slot claims keep their names, text placement, ports, and
  historical Device bindings. Existing `Device.spec.identity` and `claimRef`
  remain immutable. MachineCapacity and its `deviceRef` are not modified.
- New manually entered or optionally generated claims use `lifecycle: Installation` and typed placement. This
  mode cannot be added to a legacy slot; an installation's placement is immutable.
  This is an observational installation record, **not** a reservation or an
  accepted plan. Moving an existing asset remains blocked on the proposal's
  explicit handover protocol; the importer does not create a replacement identity
  to work around immutability.
- A Facility reference means no rack is asserted. `RackMembership` records a known
  rack but forbids position, height and face. The importer deliberately imports
  no exact rack-unit positions, including seemingly plausible positions. This
  prevents servers.com's generated 1U placements from becoming occupied or
  verified footprints. The API also represents exact `RackUnits` and
  `NonRackUnitMount` plans for separately reviewed manual records.
- `height` no longer defaults to 1 in admission: doing so invents a footprint on
  partial placements. Previously stored defaulted values remain unchanged; legacy
  UI rendering still treats omitted legacy height as one. New exact-unit plans
  must supply height. `Device.modelShape` dimensions do not establish occupancy.
- Devices without a genuine serial may have an immutable `identity.sourceIdentity`
  matching import provenance. This is a source-record identity, **not authenticated
  chassis identity**. A provider host ID is `providerHostRef`; an asset tag is
  `assetTag`, never a serial. The importer quarantines potential collisions with
  legacy serials/provider IDs rather than rewriting them.
- Typed Devices opt into `topologyAuthority: Cable` and cannot contain inline
  peers. New installation claims cannot contain expected inline peers. Existing
  inline peers are retained as unmigrated history; they are not automatically
  converted or deleted. Cable endpoints name same-namespace Device components or
  PowerFeed terminations; unbound claim endpoints are deferred. No second expected
  topology graph is written by the importer.
- Snapshot digest, source instance/class/ID, observation time and mapping revision
  are retained in provenance. `recordedBy` identifies the offline tool, not an
  authenticated source author. Milo/Git audit identifies the actual submitting
  user. Imported source identity cannot change in place. Re-import of matching
  physical fields is a no-op; differing existing records are quarantined. Changes
  require a separate reviewed correction, not last-import-wins.

## Source coverage

The exporter discovers **every advertised DCIM and circuits collection**, follows
pagination, and lists errors or missing required collections. The parameterized
`dcim/connected-device` lookup is
explicitly listed under `excludedEndpoints`, rather than mistaken for a collection.
The result is a sequential observation with bounded requests, not a transactional
NetBox database snapshot.
Changing counts, duplicate IDs and pagination cycles fail the affected collection.
Counts can remain equal while content changes; quiesce loaders and rerun for a
stable review. Repeated failed reads never become complete coverage.

The report classifies every fetched record. `imported` means representable in a
candidate (or already identical in the supplied Fleet snapshot), **not applied**.
`unresolved` means mapping, identity, or dependency needs review. `unsupported`
means an adapter cannot faithfully represent that class/shape. `excluded` always
has a reason. Collection completeness and record parity are distinct: a complete
fetch may still contain many unresolved or unsupported records.

| Source classes | Treatment |
| --- | --- |
| `dcim/sites`, `dcim/locations` | Explicit premises mapping only. One metro Site may map to several Facilities. Unmapped records stay unresolved; no Fleet policy Site is created. |
| `dcim/racks` | Explicit Facility mapping, label and known height. No free-capacity inference. |
| `dcim/devices`, `device-types`, `manufacturers` | Explicit physical/virtual and serial interpretation; model metadata, optional dimensions, separate asset tag/provider identity. NetActuate/hosted-VM hints force exclusion. Supporting catalog rows are classified excluded as independent assets; referenced model facts are consumed. |
| `interfaces`, `power-ports`, `power-outlets`, `console-ports`, `console-server-ports`, `front-ports`, `rear-ports` | Typed Device components with source IDs. Logical LAG/bridge/virtual interfaces excluded. Front/rear mappings and outlet/input dependencies retained; missing relationships remain Unknown. |
| `modules`, `inventory-items`, `device-bays` | Typed component records, not public kinds. Nested installation/parent semantics not represented by these fields remain in the private source snapshot; these are not component-incarnation controllers. |
| `cables` | Simple, reviewed-medium cables with resolvable physical endpoints. Multi-termination/breakout imports require explicit reviewed lanes covering every source termination on its original side; unsupported mappings never become all-to-all paths. Multi-position trunks never silently collapse to position one. |
| `power-panels`, `power-feeds` | Feed supply ratings and terminations, external panel dependencies. No fabricated panel Device, derated budget, independent UPS proof or usable power reservation. |
| `circuits/circuits`, `circuit-terminations` | Explicit service-owner identifiers and mapped local component associations; no new Circuit kind, service lifecycle, billing, or routing. Unmapped demarcations remain unresolved. |
| Device/module/port templates, regions, site groups, rack roles, virtual chassis, circuit/provider catalogs | Explicitly excluded as independent installed physical assets; reasons recorded per row. |
| Other advertised classes (including new NetBox-version additions) | Fetched and reported unsupported, never silently discarded. |

IPAM, VLANs, VRFs, routing, virtualization collections, billing and provisioning
are explicitly outside this export. VMs stored in **DCIM** are still inspected and
excluded per record. OOB IP lists and credential/custom-field bodies are not copied;
existing restricted Device BMC facts remain untouched. Sanitization uses a recursive
physical-field allowlist and excludes URLs, comments, custom fields and config
contexts. This intentionally is **not a lossless NetBox backup**. Exports still
contain private asset identities, serials and topology and must stay outside Git.

## Optionally compare the checked-in synthetic example

Python 3 uses only the standard library. No provider SDK or cluster access is
needed. Output directories must not already exist; files are mode 0600 inside
mode 0700 directories. No command overwrites a previous run.
Output paths inside a Git checkout are refused, including worktrees.

```sh
python3 hack/netbox_inventory.py plan \
  --snapshot hack/testdata/netbox/snapshot.json \
  --mapping hack/testdata/netbox/mapping.json \
  --output /private/tmp/fleet-physical-report-example
```

Only `report.json` is emitted by default. The historical `imported` classification
means an offline candidate was representable, **not that any record was applied**.
Inspect every unresolved/unsupported
row; the example intentionally leaves a room mapping unresolved. Staff can open
the report with the Inventory tab's local file picker; its contents are not
uploaded. The portal shows restricted/loading/empty states, paginated collections,
asset components, facility/rack membership, feeds and cables. Multiple historical
Devices on one claim do not select the most recently edited record as active.

To exercise the explicit handoff **with synthetic data only**:

```sh
python3 hack/netbox_inventory.py plan \
  --snapshot hack/testdata/netbox/snapshot.json \
  --mapping hack/testdata/netbox/mapping.json \
  --existing hack/testdata/netbox/existing.json \
  --handoff --allow-partial \
  --output /private/tmp/fleet-physical-handoff-example
```

`--allow-partial` explicitly acknowledges unresolved/unsupported records; it does
not bypass collection errors or target conflicts. The handoff emits candidate
objects and a receipt containing the exact target plus source, mapping, Fleet
snapshot and candidate digests. Nothing invokes kubectl apply or writes Git.

## Optional private source comparison

Skip this entire section for manual entry. No NetBox credentials or parity audit
are required to evaluate the manual physical-inventory features. Only perform a
source comparison when separately requested and authorized by the operator.

1. Use existing read credentials: `NETBOX_TOKEN`, otherwise `NETBOX_TOKEN_FILE`
   (default `~/.config/netbox/token`). Never paste a token into arguments, logs,
   mappings or Git. The existing infra bare-metal reader uses Bearer authentication;
   `--auth-scheme Token` explicitly supports installations using the legacy scheme.
   Redirects, non-HTTPS origins and off-origin/changed-collection pagination are
   refused. All requests are GET; retries are bounded to three for transient errors.
2. Quiesce the source loaders if possible, then capture a private snapshot:

   ```sh
   python3 hack/netbox_inventory.py snapshot --source reviewed-netbox-instance \
     --output /private/tmp/fleet-netbox-private-snapshot
   ```

3. Copy the [synthetic mapping shape](../../hack/testdata/netbox/mapping.json) into
   a private file. Review each facility, physical device classification, serial
   interpretation, placement scope, feed and cable medium. A Source Site name is
   not proof of premises. A missing serial is not permission to invent one.
   For a breakout or multi-position termination, a cable mapping can supply `lanes`
   in the [Cable Go contract](../../internal/api/inventory/v1alpha1/physical.go):
   each lane has an ID, `a` and `b`, each endpoint has exactly one local owner,
   `componentID` and `position`. Use stable candidate names from a prior dry-run
   handoff or the source-key-derived name function. Positions must exist in the
   component model; do not invent transceiver lanes absent from source geometry.
4. Export **all six** Fleet kinds from the explicitly selected **provider project's
   Milo context**, including empty collections, before preparing candidates. The
   [synthetic existing-state envelope](../../hack/testdata/netbox/existing.json)
   shows the exact structure: `target` must exactly match the mapping, `collections`
   must list all six kinds, and `items` contains the complete list in the reviewed
   namespace. Use the project's established authenticated GET/list path; do not
   assume a management-cluster context addresses Milo. The envelope is an operator
   attestation, not cryptographic proof of scope or freshness. Retain capturedAt and
   read evidence privately; re-read before any approved creation or source merge.
5. Run `plan` with private `--snapshot`, `--mapping`, `--existing` and a fresh output
   directory. Add `--previous` with the earlier source snapshot on subsequent runs.
   Review source disappearances: missing rows are **retained**, never deleted,
   retired, freed, or marked ready. Existing differences, duplicate target IDs,
   identity collisions and topology conflicts need adjudication before handoff.
6. Obtain explicit approval for the mapping, exact target and desired cohort.
   Only then generate a handoff. Do not put private exports or imported asset facts
   in a public Fleet PR. Do not apply the candidate List directly to Milo.
7. Use the explicitly approved writer for individual **new** records. A separately
   authorized one-time API creation must be additive, reject existing names rather
   than overwrite them, and retain per-record receipts for recovery. Alternatively,
   if Flux ownership is chosen for those new records, stage them additively in the
   private source. Preserve existing files, identities and bindings; never replace
   the collection with this partial candidate list. Review exact inputs for no
   deletes, modifications of existing bindings, capacities or readiness/status.
   Retain source/mapping/report/receipts in the approved private evidence store.
   This offline importer itself does not execute API writes, commit, push or deploy.

Flux remains the writer for records it already owns; direct creation of new records
does not authorize editing or taking over that cohort. Reviewers must check for
concurrent changes since the Fleet snapshot; the offline handoff does not provide
an optimistic-concurrency apply transaction. After an interrupted creation or
reconciliation, refresh the Fleet snapshot and rerun. Matching records become
no-ops; conflicts stay quarantined. Do not roll back by deleting occupied objects.

The 2026-11-20 configuration observation names `fleet-provider-inventory`, with
path `apps/fleet-system/staging/provider-inventory`, `prune: false` and
`deletionPolicy: Orphan`. The similarly named legacy inventory reconciliations
are not the provider-project destination. Recheck these settings before handoff;
this feature does not modify them or their kubeconfig references.

## Acceptance and observed limits

On 2026-11-20, read-only GitHub checks found both infra staging OCI sources configured
for `v0.0.0-feat-manual-inventory-records`; the corresponding Fleet branch head was
`42086bbe2cf05f5d09362fbc777e514907d64462`. This implementation is stacked there,
including the dependent PXE/runtime code. **That original configured-tag check
was not a deployed-revision observation.** A subsequent read-only staging API
check is recorded in the [staging baseline and acceptance checklist](physical-inventory-staging.md#read-only-baseline-observed-on-november-20-2026).
It observed the manual-inventory base, not this physical extension. No cluster
rollout or live import was performed.

The documented environment/token-file paths did not provide authorized NetBox
access in this implementation checkout. No live inventory audit was performed;
present-day counts, parity, source mappings and schema-version compatibility are
unvalidated. Source interpretation comes from the pinned infra
`d9198940a499be4e744ee0564cf3dc0b3bfcee34` loaders and site-build documents, as
recorded in the [source evidence appendix](../proposals/fleet-management/inventory-reference.md#source-evidence-and-limits).

For manual staging evaluation, use the [acceptance and rollback checklist](physical-inventory-staging.md).
If an operator separately chooses the optional source comparison, establish its
private audit and mappings, compare every classified row, and test repeated and
interrupted candidate generation. No live NetBox access means no claim of source
parity, not a blocker for manually entering and reviewing physical records.

```sh
python3 -m unittest discover -s hack -p test_netbox_inventory.py
go test ./internal/api/inventory/v1alpha1
pnpm --dir ui/provider typecheck
pnpm --dir ui/provider test
pnpm --dir ui/provider build
kustomize build config/milo >/dev/null
bash hack/validate-doc-links.sh
git diff --check
```
