# Device-reported placement

[Physical inventory](../operations/physical-inventory.md) · [Staff inventory UI](staff-inventory-ui.md) · [Optional NetBox review](../operations/netbox-physical-import.md)

## Implemented contract

`Device.spec.reportedPlacement` is optional observational data on the existing
physical asset, not another inventory, allocation, or installation identity.
It describes what the retained import source reported. It does not assert a
physical move, verified occupancy, reservation, availability, or active binding.
Inventory has no placement verification or reservation controller today.
MachineCapacity's device reference and allocator authority are unchanged.

The report contains `revision: 1`, `observedAt`, `snapshotDigest`, optional
`facilityRef`/`rackRef`, and independently optional `position`, `height`, `face`,
`fullDepth`, or a non-unit `mount`. Omission means unknown; there are no inferred
1U, face, depth, or mount defaults. Geometry requires a rack; mount and unit
geometry are mutually exclusive. Positive fractional position and height can be
retained, but only complete integral in-range geometry is drawn in an elevation.
Full-depth reports appear on both faces; unknown depth never proves opposite-face
clearance. Overlap columns and continuous multi-U blocks are retained.

Initially reports can be supplied **only when creating a new imported Device**.
They require matching NetBoxImport/source identity, observation time and snapshot
digest in existing provenance. Source identity is not authenticated chassis
identity. `recordedBy` is client attribution, not an authenticated audit principal.
The ordinary spec timestamp rule still applies to other edits.

Old objects without reports remain valid, including unchanged updates. Device
identity/claimRef immutability, typed claim placement immutability, lifecycle
removal rejection and legacy placement behavior are unchanged. References remain
name-only: CEL cannot validate existence, incarnation, ownership, rack bounds or
agreement with a claim. Offline checks are not cross-object admission guarantees.

## Corrections are deliberately disabled

There is no inventory authenticated correction service, durable append-only audit
store, or server-side per-record ownership policy. The manager role grants
create/update/patch project-wide across six kinds, not owner/namespace isolation.
Portal managed-field checks are a UI restriction, not a security boundary.
Labels, field managers and caller-supplied actors cannot authorize corrections.
No privileges are added here.

Device CEL rejects **all changes to reportedPlacement on update**, including
adding the first report, removing it, same-rack enrichment and moves. Direct
PATCH/PUT and bundled edits cannot bypass this gate. The UI explains the disabled
state; it has no correction dispatch path. This does not freeze unrelated existing
inventory edits or promise authenticated history for them. Initial report
creation still uses existing IAM, not special importer-only authorization.

Revision 1 is an immutable snapshot, **not an append-only revision history**.
Enabling corrections requires a separately reviewed implementation retaining
exact inputs before effects, independently established operator ownership,
project/namespace and UID/resourceVersion/reference-incarnation checks, same-rack
enrichment only, protected-field/bundled-change rejection, authenticated durable
actor/reason/before/after evidence, and readback/unknown-outcome reconciliation.
That audit contract must be implemented and tested before lifting CEL. Legacy
Flux ownership, unresolved ownership, contradictions and rack moves remain
blocked. Never delete/recreate/rebind to bypass the gate.

## Readers and history

Device detail, filters, location links, rack/facility equipment and elevations
prefer the Device report. Partial reports never borrow geometry from a claim.
Without a report, the claim is explicitly labelled **Claim-derived fallback**,
not verified current location. Multiple registrations remain historical; neither
newest timestamp nor latest registration selects an active asset. A reported
Device is not duplicated as claim fallback. Claimless readable reports remain
visible. Denied references are masked; denied claims cannot supply fallback;
partial collection counts remain incomplete.

The claim section is registration/intent history. `lifecycle: Installation` is
the sole typed lifecycle, not an existing/prospective discriminator. No automatic
future-installation classification is added. Existing session, navigation/focus,
denial, keyboard and uncertain-dispatch protections are reused.

## Offline review and rollout

For a **new** import candidate, per-device mapping `includeReportedPlacement: true`
opts into source reports. Default importer output is unchanged; existing cohorts
are not migrated. This is direct API candidate data, not GitOps or periodic sync.
Review source semantics: reported positions can themselves be synthetic.

For existing devices, the offline review tool takes only local files:

```sh
python3 hack/device_placement.py --snapshot /private/path/source.json \
  --target /private/path/target.json --allowlist /private/path/allowlist.json \
  --output /private/path/review
```

Target has `target` (exact reviewed project/namespace destination) and `items`
with retained Device, DeviceClaim and Rack snapshots. Allowlist has exactly
`target` and `devices`; each selection has exactly `name`, `namespace`, `uid`,
`resourceVersion`, `sourceID`. The private, **non-executable, blocked** output
retains source/target digests, before/proposed-after records, reference snapshots,
and UID/resourceVersion test preconditions. No patch body, API client, retry or
apply mode exists. Source snapshots remain outside Git.

The planner rejects stale/replaced targets, duplicate selections, source/rack
contradictions, unknown incarnations, positioned/legacy claims, Flux markers,
absent source positions and out-of-bounds geometry. Absence of Flux markers does
not prove ownership: every output is blocked. Proposed provenance preserves
method/source identity, advancing evidence only in the proposed record.

The requested small enrichment cohort is a private review artifact, not a rollout.
Null-position PDUs remain unknown; separately imported exact-position claims
remain labelled fallback. Legacy inventory and capacity are untouched. Later
migration requires fresh source and target snapshots, not these retained inputs.

After independent review/CI, deploy additive schema and readers before explicit
new-record writers. Verify roundtrip before enabling writers. Existing-record
writers remain disabled in this release. After a future audit implementation,
separately approve one same-rack canary, retain exact UID/resourceVersion inputs,
write conditionally, and read back exact UID/spec before proceeding. Timeout is
unknown: GET and reconcile. Replaced UID, changed baseline or competing revision
is conflict, never automatic retry permission. Do not roll back to a schema that
prunes reports or delete objects as rollback. No live writes/deployment are part
of this change.

## Validation

```sh
go test -race ./internal/api/inventory/v1alpha1 ./internal/controller/...
python3 -m unittest discover -s hack -p 'test_netbox_inventory.py'
python3 -m unittest discover -s hack -p 'test_device_placement.py'
cd ui/provider && pnpm test && pnpm typecheck && pnpm build
```

Tests cover old admission, geometry, provenance, direct-update bypass attempts,
exact selection, stale UID/revision, contradictions, projection/fallback, denials,
multiple registrations and existing session/navigation regressions. They do not
establish live IAM enforcement, durable audit, cross-object admission, browser
acceptance or deployment readiness.
