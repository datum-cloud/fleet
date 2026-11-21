# Operational Sites and physical Facilities

[Physical inventory](../operations/physical-inventory.md) · [Architecture](../architecture/design.md) · [Staff inventory](staff-inventory-ui.md)

## Implemented scope, not deployment acceptance

This additive implementation starts from staging feature revision
`0bc37cf23ca949df97b6ede8bc78f2a71c670ac4`. It adds an operator-written `Site`
record, Facility premises identifiers, observational readers, IAM declarations,
staff directory views and an offline import option. It does not install these
changes, mutate live records or assert authenticated staging acceptance.

The clarification **Datum Site DFW3 relates to DataBank premises DFW1** describes
two concepts, not two Facilities. It is conceptual evidence only, not permission
to migrate existing records. No DFW mapping is seeded by this change. Tests and
preview records use synthetic `EX3` / `example-provider / EX1` identities.

## Ownership and cardinality

The [pinned Site proposal](../proposals/fleet-management/cluster-lifecycle.md#site-and-cluster-resources)
describes an operational placement and capacity-sharing boundary with explicit
allocation selection. The [inventory appendix](../proposals/fleet-management/inventory-reference.md#facility-and-rack-contracts)
assigns premises to Facility and physical containment to racks and installations.
This implementation reserves the same `fleet.datumapis.com/v1alpha1` Site concept
but implements **identity and observed membership only**, not the proposed
`allocation.facilitySelector`, cluster placement, or capacity-sharing authority.
`facilityRefs` must never be interpreted as the proposed eligibility selector.
Future policy needs a separately authorized contract and admission checks.

Actual geography and service readiness already belong to the Milo
`locations.miloapis.com` Location projection and service-catalog ServiceAvailability
used by `internal/location.Resolver`. Neither its authority revision nor its
eligibility inputs consume inventory Site records. Facility's existing optional
`locationRef` remains unchanged. Site does not introduce another geographic
resource, provider metro code, account grouping, or Location readiness claim.

- A Facility owns physical premises. Racks continue to reference exactly one
  Facility through their existing `facilityRef`; nothing rewrites these references.
- A Site may record zero to many Facility references (bounded at 64 per record).
  A Facility may participate in zero to many Sites. This permits multiple
  operational boundaries at shared premises and a boundary spanning premises
  without inventing duplicated buildings. Zero means unmapped, never all premises.
- Operational membership is not physical containment. A device's reported or
  claim-derived placement is unchanged. A Facility association does **not** assign
  every device, installation, allocation, or NodeClaim there to that Site.
  Per-consumer operational membership remains with its future owning contract.
- Site and Facility are namespaced provider-project inventory records with the
  same explicit direct-API writer and provenance discipline. This adds no mapping
  service, controller, synchronization owner, or GitOps ownership for records.

## Stable identity and references

`Site.spec.code` is the mutable internal human code; `displayName` is a mutable
label. Neither is a reference key or globally unique identifier. Resource names
are stable API addresses; Kubernetes UIDs identify exact resource incarnations.
Site identity references must pin name and UID, never a code. Changing a human
code or label does not require rename, deletion, recreation or UID changes.

`Site.spec.facilityRefs` contains `{name, uid}` entries scoped to the Site's own
namespace and project. Duplicate names or UIDs and missing reference fields fail
schema admission. `ReadSiteFacilities` reads through the caller-provided authorized
reader, returns no partial result on failure, checks namespace, deletion and exact
UID, and redacts read error details. It is not a policy resolver. Its caller must
already be authorized to read the Site; it neither grants credentials nor bypasses
the supplied reader's authorization. A privileged reader is not a user IAM check.

CRD CEL cannot perform cross-object existence/authorization checks. A structurally
valid but missing, stale, or cross-namespace target remains **unresolved**, not
accepted placement or authority. UI readers likewise require exact namespace,
name and UID before showing a related Facility. Name reuse cannot silently bind
the replacement. Reference repair requires explicit operator review and new
provenance. This is observational validation, not atomic graph admission or a
referential-integrity controller.

`Facility.spec.identifiers` is an optional bounded list of
`{provider, type, value}`. `provider` is a canonical provider namespace (for example
`databank`), not its mutable display label. Types are `PremisesCode`, `PremisesAlias`
and `PremisesID`. Multiple identifiers, aliases, and provider namespaces are
supported. Identical code values under different providers are distinct; exact
duplicate entries are rejected. These are sourced descriptive facts, not resource
references or global uniqueness claims. A repeated qualified identifier on
different Facilities requires human adjudication; it never merges records.
Provider metro, account, service and host identifiers are not premises identifiers.
Identifier edits, Site code changes and membership edits must advance provenance.

## Staff access and compatibility

The existing inventory viewer/editor/manager role definitions gain only equivalent
Site read/write verbs. No memberships, consumer roles, status writer permissions,
allocation permissions or operational permissions are changed. The staff directory
adds an Operational sites category, internal code details, provider-qualified
premises identifiers, and reverse Site membership on Facilities. Site writes and
identifier/membership edits remain direct API operations; there is no new UI form.
Existing inventory forms and their session, operation and frozen revision guards
are unchanged. Human label edits preserve the new fields.

Site 403/404/transport failure does not hide legacy physical records or imply
unmapped membership: the UI says operational Site details are unavailable. A
successful Site list with no matching membership says explicitly unmapped. Denied
Facility reads suppress related identifiers and links from Site details. Search
uses only accessible identities. Existing global session-denial/cache fencing
also covers the additional collection. No credentials or secret-bearing fields
are added to search, details or forms.

Old Facilities without identifiers remain valid and browsable, and all existing
rack/device/claim references, resource UIDs, immutable identity/source bindings,
reportedPlacement revisions and unknown geometry retain their contracts.
No allocation, installation, provisioning, capacity, reservation or verification
effects result from any new record. Empty rack units and missing Site mappings
remain observations, not available capacity or readiness.

## Explicit offline importer mapping

The existing NetBox planner still makes no writes. Facility mapping may optionally
include `identifiers` and `identifiersVerified: true`; identifiers are not inferred
from source names. Invalid types such as Metro or Account are quarantined.
An optional `operationalSites` list accepts this synthetic shape:

```json
{
  "name": "site-stable",
  "displayName": "Example operational boundary",
  "code": "EX3",
  "source": "dcim/sites/1",
  "verified": true,
  "facilityRefs": [{"name": "premises-stable", "uid": "existing-premises-uid"}]
}
```

The source must be a present source site/location, but that alone establishes no
mapping. The explicit verified entry and a complete current Fleet snapshot,
including the Site collection, are required. Each Facility must already exist at
the exact namespace/name/UID, not be deleting, and survive graph reconciliation.
New Facility candidates have no API UID, so a mapping to them must wait for a
separately authorized creation and a new snapshot. No UID is invented. Existing
six-collection snapshots remain usable when no operational Site mapping is
requested. Existing target differences still block automatic updates or takeover.
All candidate changes, including identifiers on an existing Facility, require
separate operator review. Name similarity never creates a mapping.

## Rollout and remaining gates

1. Review and deploy CRD/IAM definitions through the existing packaging path,
   then the provider UI. Do not apply any example records. The UI tolerates Site
   API absence before rollout while keeping physical inventory browsable.
2. Read existing records privately and preserve stable names, UIDs, provenance,
   ownership and revision baselines. Agree canonical provider namespaces and
   verify individual premises identity before adding descriptive identifiers.
3. Separately authorize any Site creation or membership edit. Pin actual Facility
   UIDs from authorized reads and use current resourceVersion/UID preconditions
   for corrections. No delete/recreate migration is required or allowed here.
4. Independently review IAM and authenticated staging behavior before rollout.
   This task changes no live counts or memberships and does not deploy. The nine
   DFW3 reported-placement corrections and separate audit companion are out of scope.

Future allocation policy, canonical provider namespace governance, cross-record
identifier uniqueness adjudication, and atomic relationship admission remain
explicit design/qualification gates. Synthetic tests cannot establish real-world
premises truth or production readiness.

## Local validation

```sh
go test -race ./internal/api/inventory/v1alpha1 ./internal/location
python3 -m unittest discover -s hack -p 'test_*inventory.py'
python3 -m unittest discover -s hack -p 'test_device_placement.py'
(cd ui/provider && pnpm test && pnpm typecheck && pnpm build)
(cd ui/consumer && pnpm test && pnpm typecheck && pnpm build)
bash hack/validate-doc-links.sh
git diff --check
```

Tests cover additive schema, provider qualification/collisions, legacy records,
provenance, stable identity despite label changes, stale/invalid relationships,
read denial/redaction, observational imports and existing independent safety
scenarios. Existing six-collection request-count assertions now count seven;
their denial, session lifetime, operation and no-write assertions remain intact.

Local validation on this implementation passed the full `go test -race ./...`
suite (the two manager envtest tests skip without `KUBEBUILDER_ASSETS`), provider
and consumer tests/typechecks/builds, 33 importer tests, six reported-placement
planner tests, inventory manifest rendering, local documentation links and
whitespace checks. This is not authenticated staging acceptance. Mobile inspection
was attempted at 390×844 with synthetic fixtures, but both browser snapshots timed
out; the alternate browser host reported unavailable. Visual mobile acceptance
remains a review gate, not a claimed pass.
