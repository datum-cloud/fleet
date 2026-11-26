# Shared hardware catalog: implemented additive slice

[Implementation status](README.md) · [Physical inventory](../operations/physical-inventory.md)

This implementation builds on staging branch `feat/manual-inventory-records` at
`5b60656759033b61674ee202a9ad94f4d49f4a64`, including PR106
(`11778ec901812747ad7cb5e915f6c7658836bc04`), Site/Facility membership and
Device reported placement. It preserves the target's provenance-free inventory
contract and does not replace those APIs or their writers.
The [pinned inventory proposal](../proposals/fleet-management/inventory-reference.md#asset-identity-and-typed-components)
already describes a reusable model/port shape, initially a document rather than a
public kind. This authorized implementation promotes that concept to Manufacturer
and DeviceType records. It does **not** implement the proposal's accepted-plan
shape pinning, physical qualification, reservations, or component reconciliation.

## Implemented contracts

- `fleet.datumapis.com/v1alpha1` Manufacturer and DeviceType are namespaced,
  operator-written catalog records served in the provider's Milo Project.
  There is no catalog controller, status writer, capacity publication, allocation,
  installation, node, networking, provisioning, or IAM membership effect.
- Manufacturer has a mutable `canonicalName`, human `displayName`, and optional
  descriptive `aliases`. ASCII labels are bounded and cannot have leading/trailing
  spaces; case-insensitive aliases must be distinct within one object and cannot
  repeat its canonical name. Aliases never merge identities or resolve references.
- DeviceType has an immutable `manufacturerRef: {name}` and normalized `model`
  identity key (`[a-z0-9]+(-[a-z0-9]+)*`). `displayName` is its mutable human model
  label. Stable metadata names are operator-selected, not regenerated from labels.
  Distinct hardware variants need distinct reviewed model keys.
- Shared `modelShape` reuses the existing height-in-rack-units/full-depth shape.
  Absent values mean unknown and are never defaulted; a Device's
  `fullDepth` overrides its DeviceType's. It is **nominal**, never a measured
  installation or observed occupancy, but rack elevations use `fullDepth` to
  decide which faces a Device occupies. `componentTemplates` reuse the component type, connector,
  position, name and stable local-ID vocabulary, but have no serial, asset tag or
  source-instance identity. They do not create installed ports, topology or assets.
  A template may set a chassis-relative `face` (`Front` or `Rear`; absent means
  unknown), which a Device component with the same id inherits. See
  [rack faces, depth and component faces](../operations/physical-inventory.md#rack-faces-depth-and-component-faces).
  `modelShape.width` (`Full` or `Half`) is optional on both kinds and follows
  the same rules; see [faceplate layout](#faceplate-layout-implemented).
- Both kinds use `metadata.creationTimestamp` and resource audit logs, not
  spec provenance, imported source identities, or synthetic timestamp advances.
  A catalog edit changes no Device, claim, installed component, recorded hardware
  observation, or reported placement.
- Device gains an optional `deviceTypeRef: {name}` outside immutable `identity`.
  Once attached, the reference cannot be removed or rebound. Existing inline
  manufacturer/model, serial, system UUID and claimRef remain unchanged and
  immutable. Catalog edits never overwrite recorded observations. Neither catalog
  nor physical records accept the removed provenance/source-identity fields.

## Faceplate layout (implemented)

These fields were proposed in the faceplate design and are now implemented in
the Go types, the CRD and `ui/shared/fleet-api.ts`. Every field is optional,
none is defaulted, and absence means "not recorded".

Layout is nominal catalog data on DeviceType only. A Device inherits it through
`deviceTypeRef` and the template-id convention, and cannot override it. It
carries no state: whether a port is cabled comes from Cable records, and there
is still no observed health.

**Joining components to templates.** The template-id convention means a Device
component whose `id` equals a template's `id` takes that template's face and
cell. Imported records can break the convention: their component ids are
importer instance ids (`interfaces-3362`), and their template ids are template
ids (`interface-templates-34`). For drawing only, the staff UI then falls back
to the one template with the same name and type; see
[template join](staff-inventory-ui.md#faceplate-drawing). That fallback
positions a component on the drawing and changes nothing else. The long-term
fix is for the importer to record each component's template id (gap P7).
Changing component ids is not the fix, because Cables reference component ids.
Connector text maps to a drawn kind through one fixed table; see
[connector kinds](staff-inventory-ui.md#faceplate-drawing).

Positions are cells of a **bank grid**, never millimetres. A bank is one
cluster of identical cells on one face, such as a 2×12 RJ45 block or a PSU
bay. Row and column inside a bank give the usual odd-top, even-bottom switch
numbering.

| Field | Status | Meaning |
| --- | --- | --- |
| `spec.faceplate.banks[]` | Implemented (was proposal) | Up to 32 banks, a list map keyed by `id` (1 to 63 characters). |
| `banks[].face` | Implemented (was proposal) | `Front` or `Rear`, chassis-relative. Required. |
| `banks[].rows`, `banks[].columns` | Implemented (was proposal) | The grid, 1 to 8 rows and 1 to 64 columns. Required. |
| `banks[].offset` | Implemented (was proposal) | The bank's left edge as a fraction (0 to 1) of the usable face width. All or none per face. With none, banks flow left to right in list order. |
| `banks[].cellMM {width, height}` | Implemented (was proposal) | Nominal module cell size in millimetres (width 1 to 450, height 1 to 400), for PSUs, drive bays and fans. With none, readers use the nominal size of the cell's connector. |
| `componentTemplates[].layout {bank, row, column}` | Implemented (was proposal) | The template's cell. 1-based. Required together. |
| `componentTemplates[].layout.span` | Implemented (was proposal) | Columns covered to the right, 1 to 64. Unset means 1. There is no row span. |
| `modelShape.width` | Implemented (was proposal) | `Full` or `Half`, on DeviceType and Device, as for `fullDepth`. Unset is unknown; drawings assume full width and say so. Which half a Device occupies is a placement fact and is not recorded. |

**Decorative marks are not implemented.** The design's optional `marks[]`
(power buttons, LEDs, vents, labels) were dropped to keep the schema to the
fields above. Drawings show only components, and empty space stays empty.

### Admission rules

The CRD's CEL rules reject:

- a template `layout` without a template `face`, without a `faceplate`, or
  naming a bank that is missing or on the other face;
- a row past the bank's rows, or a column plus span past its columns
  (minimums are schema bounds);
- two templates whose cells overlap in one bank row, spans included;
- offsets set on some banks of a face but not all;
- more than 128 templates with a layout.

Banks are per face, so a cell that is unique in its bank is unique on its face.
CEL cannot check physical fit, such as banks that together are wider than the
face or that overlap each other. The staff UI drawing scales such a layout and
warns; it does not reject it.

**CEL cost outcome.** The overlap rule compares every pair of templates. Its
static estimate is about 4.4M against the 10M per-expression limit, so the CRD
installs, and the layout rules total about 5.2M against the 100M CRD limit.
At runtime, though, 256 laid-out templates can exceed the 1M per-call limit.
Measured on the costliest valid shape found (32 one-row banks with 63-character
IDs, every template in row 1), the cost passes 1M between 176 and 192 laid-out
templates. The smallest safe change was a cheap cap: at most 128 templates may
carry a layout. That keeps the worst case at about 627K. `componentTemplates`
keeps its 256-item limit. Templates past the cap simply have no layout and no
position on the drawing; the staff UI lists them in its "Not on the faceplate"
tray.

The cap rule runs before the overlap rule, so an over-cap object reports the
cap rather than a cost error. `faceplate_test.go` pins the estimates and the
worst-case runtime cost with 25% headroom. `faceplate_envtest_test.go` checks
every rule and the worst case against a Kubernetes 1.37.0 API server. If real
hardware needs more than 128 laid-out templates, such as a 2U 96-port patch
panel with front and rear ports, move overlap detection to a reviewed
admission webhook rather than raising the cap.

### Example (synthetic)

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: DeviceType
metadata:
  name: example-1u-server
  namespace: example-inventory
spec:
  displayName: Example 1U server
  manufacturerRef: {name: example-vendor}
  model: example-1u
  modelShape: {heightUnits: 1, fullDepth: true, width: Full}
  faceplate:
    banks:
      - {id: bays, face: Front, rows: 1, columns: 8, cellMM: {width: 40, height: 18}}
      - {id: psu-1, face: Rear, rows: 1, columns: 1, offset: 0, cellMM: {width: 40, height: 38}}
      - {id: ocp, face: Rear, rows: 1, columns: 2, offset: 0.13}
      - {id: mgmt, face: Rear, rows: 1, columns: 1, offset: 0.34}
      - {id: psu-2, face: Rear, rows: 1, columns: 1, offset: 0.89, cellMM: {width: 40, height: 38}}
  componentTemplates:
    - {id: bay-1, name: Bay 1, type: Module, connector: 2.5in bay, face: Front, layout: {bank: bays, row: 1, column: 1}}
    - {id: eth0, name: Ethernet 0, type: NetworkPort, connector: SFP28, face: Rear, layout: {bank: ocp, row: 1, column: 1}}
    - {id: eth1, name: Ethernet 1, type: NetworkPort, connector: SFP28, face: Rear, layout: {bank: ocp, row: 1, column: 2}}
    - {id: bmc, name: BMC, type: NetworkPort, connector: RJ45, face: Rear, layout: {bank: mgmt, row: 1, column: 1}}
    - {id: psu-1, name: PSU 1, type: PowerInlet, connector: IEC C14, face: Rear, layout: {bank: psu-1, row: 1, column: 1}}
    - {id: psu-2, name: PSU 2, type: PowerInlet, connector: IEC C14, face: Rear, layout: {bank: psu-2, row: 1, column: 1}}
    # Recorded, not laid out: its position on the face is not recorded.
    - {id: console, name: Console, type: ConsolePort, connector: RJ45, face: Rear}
```

The front bank flows because none of its face's banks sets `offset`. The rear
banks are placed because all of them do.

## Scope, guarantees, and reader safety

References always resolve in the referring object's namespace and provider
Project. No spec accepts a UID or an explicit cross-namespace reference. Ordinary
Kubernetes unknown-field pruning is **not** rejection: clients must use
`fieldValidation=Strict`. Disposable API tests exercise strict decoding of
references and removed fields as well as conflict handling.

These are name references, not incarnation pins. Missing or deleting targets are
unavailable; recreating a name resolves to the replacement object. This never
proves physical continuity or triggers instance changes. Do not reuse names during
routine catalog maintenance. Preventing reuse requires a separately reviewed
retention/tombstone policy, not a UID added to the spec.

CRD admission validates **one object's** syntax, alias uniqueness, immutable
fields. It cannot atomically enforce uniqueness of a normalized
model across objects within a manufacturer, canonical manufacturer names across
objects, or the existence/authorization of references.
Distinct resource names can still describe the same model. No webhook or global
uniqueness reservation is claimed. Writers must adjudicate collisions; reads of
existing records are not a transaction and races remain possible.

The Go reader requires a caller-authorized `client.Reader`; do not substitute an
unrestricted controller credential for a staff caller. It reads both targets and
returns no partial catalog/error detail when either is unavailable. UI collection
reads use existing provider proxy and session-generation fencing. Denial or
unavailability of Manufacturers suppresses dependent DeviceTypes, including deep
links, search and filters. Device labels use the catalog only when both references
are readable; otherwise the Device's independently authorized recorded hardware
observation remains available. Catalog fallback does not query aliases or leak
denied reference names. Existing session, focus and late-response safety tests
remain intact; only their collection cardinality increases from seven to nine.
If a selected catalog becomes unreadable during registration, the form retains
an unavailable selection without disclosing its label and blocks the write. It
does not silently clear the attachment and create an unassigned Device instead.

Inventory viewer gains catalog get/list/watch. Importer inherits those reads and
gains catalog create only, without update, patch, or delete permissions. The
exact-role regression test checks these grants alongside every inventory kind.
Manager gains create/update/patch;
editor also has delete, consistent with the existing editor role. No status,
wildcard, consumer, membership or operational permission is added. Deploying
these definitions does not bind anyone to these roles.

## Deferred external import

PR106 deliberately removed the physical NetBox importer and placement planner.
Integration with that target also removes this feature's newly introduced offline
NetBox catalog importer, dependent fixtures/tests, and CI steps. No external
catalog import or deduplication command is implemented in this slice. Restoring
source identity, provenance receipts, or the old importer to enable catalog
creation would contradict the current inventory contract.

Authorized direct-API writers may create catalog records with ordinary Kubernetes
metadata and the model fields documented here. External imports, source mappings,
cohort collision handling, migration and recurring synchronization require a
separately reviewed contract. Any external attribution belongs in writer-owned
labels/annotations and audit logs; Fleet does not interpret it or acquire ownership
from it. Creating catalog records remains observational, never a capacity or
provisioning action.

## Manufacturer detail (implemented)

Manufacturer detail now shows recorded overview fields, linked DeviceTypes with
associated Device counts, and linked Devices with their model and reported
location. Joins use same-namespace `manufacturerRef` and `deviceTypeRef` only;
inline manufacturer text and aliases never establish relationships. Rows/counts
are deduplicated by namespace/name. Deleting or missing targets remain unresolved.
Locations use the Device's report only, with links to readable Facility/Rack
records; plans and nominal templates never fill in physical observations.

Display names lead catalog tables, links and registration choices, with resource
names as fallback and contextual identifiers only for colliding labels. The
manufacturer's metadata is collapsed under Technical details. Existing Datum
tables, page chrome, query/history behavior and authorized collection cache are
reused, without new reads, forms, mutations, session policies or backend changes.
Unavailable model/device collections produce unknown counts, not zero; unreadable
dependencies lose their labels and links immediately. Resolved subsets remain
visible with missing-reference guidance. Initial loading stays in the page shell.

Focused tests cover reference scope, unrelated and
dangling records, duplicate rows/counts/labels, fallback labels, catalog picker
choices, denied/partial collections, actual destination URLs, reported-only
location, loading and history/filter retention. Existing session, write-precondition
and visible-focus regressions also pass. Shared/consumer code is unchanged, so
consumer validation is not required. No browser/host acceptance, live reads or
writes, migration, deployment, physical verification or readiness is claimed.

## Explicitly deferred user-visible work

Staff can browse Manufacturers/DeviceTypes and see catalog-backed Device labels,
recorded observations and nominal template details. New manual Device registrations
offer an optional same-namespace catalog selection, with fresh authorized reads
and UID/resourceVersion checks of both targets immediately before create. Strict
field validation prevents an older Device schema silently dropping the reference.
Selection never copies labels, components or shape into recorded observations;
inline observed model data remains required. These reads are not an atomic
cross-object lock: a later recreation follows the documented name semantics.
Catalog create/edit forms and attachment/correction forms for **existing** Devices
are not implemented. Creating catalog records does not attach
existing or new Devices; shape/template import, legacy-source migration,
assignment correction/rebinding and accepted-plan revision pinning need follow-up.

An explicit direct-API attachment must use the Device's current owner, require
fresh authorized reads of both same-namespace targets, retain exact inputs, and
test metadata UID and resourceVersion before adding the reference. Attachment
must not invent provenance or a recording timestamp.
UIDs are request preconditions, never spec references. Use Strict
field validation. A failed or uncertain request requires readback, not a blind
retry. No live attachment is performed here. Correcting an already attached
reference requires a reviewed compatibility contract; do not weaken identity
immutability, delete/recreate the Device, or transfer its claim to work around it.

## Validation and rollout gates

```sh
go test ./...
go test -race ./...
go vet ./...
pnpm --dir ui/provider typecheck && pnpm --dir ui/provider test && pnpm --dir ui/provider build
pnpm --dir ui/consumer typecheck && pnpm --dir ui/consumer test && pnpm --dir ui/consumer build
kustomize build config/milo >/dev/null
kustomize build config/inventory >/dev/null
bash hack/validate-doc-links.sh
git diff --check
```

Set `KUBEBUILDER_ASSETS` to the repository's pinned envtest assets to include the
disposable API checks; without it that test is explicitly skipped. These checks
are not proof of real Milo authorization or live NetBox parity.

Validation of the PR106 integration passed the full Go suite, race suite and vet,
with local Kubernetes 1.37.0 envtest assets including catalog concurrency and
strict rejection of retired fields. The provider and consumer suites, both
typechecks and both production builds pass.
The five CI manifest roots, synthetic-staging render, overlay isolation and its
regression tests, standalone render, Milo acceptance packaging, documentation
links and whitespace checks also pass. These are local synthetic checks, not
remote CI completion, live authorization verification, or deployment acceptance.

The parent task owns CI and the feature-branch merge; further review is waived
for this merge. No staging deployment or live mutation is part of this work.
Roll out additive CRDs and least-privilege IAM definitions, then denial-aware
readers, **before any writer**. Confirm existing Site/Facility/Device placement
behavior and unreadable/missing catalog fallback on staging. Never roll back by
pruning CRDs or catalog/occupied records, downgrading schemas to prune retained
fields, deleting Devices, or rebinding identity. Roll back readers first while
retaining additive schemas, records and existing writer authority. Live migration
and any ownership transition remain separately approved operations.
