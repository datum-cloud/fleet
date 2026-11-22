# Shared hardware catalog: implemented additive slice

[Implementation status](README.md) · [Physical inventory](../operations/physical-inventory.md)

This implementation builds on staging branch `feat/manual-inventory-records` at
`f190d28397fc6110e02c9c83e6819a3a584258d0`, including Site/Facility membership and
Device reported placement. It does not replace those APIs or their writers.
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
- Shared `modelShape` reuses the existing height-in-rack-units/full-depth shape;
  absent values mean unknown. It is **nominal**, never a measured installation or
  observed occupancy. `componentTemplates` reuse the component type, connector,
  position, name and stable local-ID vocabulary, but have no serial, asset tag or
  source-instance identity. They do not create installed ports, topology or assets.
- Both kinds retain normal provenance. Every changed spec must advance
  `provenance.recordedAt`. Imported `sourceIdentity` is immutable, including its
  presence, and must match NetBoxImport provenance. A catalog edit changes no
  Device, claim, installed component, source observation, or reported placement.
- Device gains an optional `deviceTypeRef: {name}` outside immutable `identity`.
  A first explicit attachment must advance existing provenance; once attached,
  the reference cannot be removed or rebound. Existing inline manufacturer/model,
  serial, sourceIdentity and claimRef remain unchanged and immutable. Imported
  Devices retain their original NetBoxImport source evidence when an authorized
  writer records an attachment; catalog edits never overwrite source observations.

## Scope, guarantees, and reader safety

References always resolve in the referring object's namespace and provider
Project. No spec accepts a UID or an explicit cross-namespace reference. Ordinary
Kubernetes unknown-field pruning is **not** rejection: clients must use
`fieldValidation=Strict`; the offline importer rejects unknown mapping keys and
UIDs. Disposable API tests exercise strict decoding as well as conflict handling.

These are name references, not incarnation pins. Missing or deleting targets are
unavailable; recreating a name resolves to the replacement object. This never
proves physical continuity or triggers instance changes. Do not reuse names during
routine catalog maintenance. Preventing reuse requires a separately reviewed
retention/tombstone policy, not a UID added to the spec.

CRD admission validates **one object's** syntax, alias uniqueness, immutable
fields and provenance. It cannot atomically enforce uniqueness of a normalized
model across objects within a manufacturer, canonical manufacturer names across
objects, source IDs across objects, or the existence/authorization of references.
Distinct resource names can still describe the same model. No webhook or global
uniqueness reservation is claimed. Offline cohort checks detect known collisions,
but a target snapshot is not a transaction and races remain possible.

The Go reader requires a caller-authorized `client.Reader`; do not substitute an
unrestricted controller credential for a staff caller. It reads both targets and
returns no partial catalog/error detail when either is unavailable. UI collection
reads use existing provider proxy and session-generation fencing. Denial or
unavailability of Manufacturers suppresses dependent DeviceTypes, including deep
links, search and filters. Device labels use the catalog only when both references
are readable; otherwise the Device's independently authorized legacy/source
observation remains available. Catalog fallback does not query aliases or leak
denied reference names. Existing session, focus and late-response safety tests
remain intact; only their collection cardinality increases from seven to nine.

Inventory viewer gains catalog get/list/watch. Importer inherits those reads and
gains catalog create only, without update, patch, or delete permissions. The
exact-role regression test checks these grants alongside every inventory kind.
Manager gains create/update/patch;
editor also has delete, consistent with the existing editor role. No status,
wildcard, consumer, membership or operational permission is added. Deploying
these definitions does not bind anyone to these roles.

## Optional offline catalog handoff

`hack/netbox_catalog.py` consumes the existing version-1 NetBox snapshot format,
an explicit mapping, and a complete namespace-local Manufacturer/DeviceType
snapshot. It never calls NetBox or Fleet. Run, retaining output privately:

```sh
python3 hack/netbox_catalog.py --snapshot /private/netbox.json \
  --mapping /private/catalog-mapping.json --existing /private/fleet-catalog.json \
  --recorded-by operator-identity --recorded-at 2026-11-21T01:00:00Z
```

The synthetic [mapping fixture](../../hack/testdata/catalog/mapping.json) shows
the format. `target` requires `project`, `namespace`, and `writer: DirectAPI`.
The Fleet snapshot must attest `collections: [Manufacturer, DeviceType]`, contain
that same target, and include both complete collections, including empty ones.
Manufacturer `sourceID` and DeviceType `sourceID`/`manufacturerSourceID` are
explicit positive NetBox IDs, never aliases, host IDs or chassis serials. Model
normalization lowercases ASCII and collapses non-alphanumeric runs to a hyphen;
the explicitly supplied key must match. Normalization collisions are quarantined,
not silently deduplicated. Non-ASCII source models require adjudication.

Deduplication uses `(source system, source collection, source ID)` and the exact
stable target name. Repeated identical imports are no-ops; interrupted creates
are handled by refreshing the snapshot. Conflicting existing records, alias or
canonical-name collisions, duplicate normalized model pairs, missing references,
malformed inputs, unknown mapping fields and duplicate JSON keys quarantine the
**entire cohort** with no candidates. Source/catalog evidence stays private.
Snapshot version must be an integer and collection attestations must be lists.
Existing-record provenance and source identity must validate before deduplication
ignores changing receipt fields. Observation and recording timestamps use strict
RFC3339 date-times; broader Python ISO formats are rejected rather than emitted.

Output is a review envelope, not an automatically applied Kubernetes List.
The approved owner must re-read authorization, existing identities and conflicts
immediately before individual create-only API requests. Reject existing names;
never apply/overwrite, adopt a source, change its writer or claim, or treat an
uncertain creation as success. Retain receipts and refresh after interruption.
No GitOps inventory/catalog records, recurring sync, or live migration is added
or authorized by this command. The older physical importer is not invoked.

## Explicitly deferred user-visible work

Staff can browse Manufacturers/DeviceTypes and see catalog-backed Device labels,
source observations and nominal template details. New manual Device registrations
offer an optional same-namespace catalog selection, with fresh authorized reads
and UID/resourceVersion checks of both targets immediately before create. Strict
field validation prevents an older Device schema silently dropping the reference.
Selection never copies labels, components or shape into source observations;
inline observed model data remains required. These reads are not an atomic
cross-object lock: a later recreation follows the documented name semantics.
Catalog create/edit forms and attachment/correction forms for **existing** Devices
are not implemented. Importing catalog records does not attach
existing or new Devices; shape/template import, legacy-source migration,
assignment correction/rebinding and accepted-plan revision pinning need follow-up.

An explicit direct-API attachment must use the Device's current owner, require
fresh authorized reads of both same-namespace targets, retain exact inputs, and
test metadata UID and resourceVersion before adding the reference and advancing
provenance. UIDs are request preconditions, never spec references. Use Strict
field validation. A failed or uncertain request requires readback, not a blind
retry. No live attachment is performed here. Correcting an already attached
reference requires a reviewed compatibility contract; do not weaken identity
immutability, delete/recreate the Device, or transfer its claim to work around it.

## Validation and rollout gates

```sh
go test ./...
go test -race ./...
go vet ./...
python3 -m unittest discover -s hack -p 'test_netbox*.py'
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

The parent task owns independent adversarial review, CI and staging deployment.
Roll out additive CRDs and least-privilege IAM definitions, then denial-aware
readers, **before any writer**. Confirm existing Site/Facility/Device placement
behavior and unreadable/missing catalog fallback on staging. Never roll back by
pruning CRDs or catalog/occupied records, downgrading schemas to prune retained
fields, deleting Devices, or rebinding identity. Roll back readers first while
retaining additive schemas, records and existing writer authority. Live migration
and any ownership transition remain separately approved operations.
