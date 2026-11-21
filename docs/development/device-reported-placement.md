# Device-reported placement

[Physical inventory](../operations/physical-inventory.md) · [Staff inventory UI](staff-inventory-ui.md) · [Optional NetBox review](../operations/netbox-physical-import.md)

## Implemented contract

`Device.spec.reportedPlacement` is optional observational data on the existing
physical asset, not another inventory, allocation, or installation identity.
Authorized managers can add the first report or correct an existing report using
ordinary direct API PATCH/PUT. A reported location can change without a new or
superseded installation. This is record correction, **not acceptance of a physical
move, handover, reservation, verified occupancy, availability, or active binding**.
Device identity (including source identity) and claimRef remain immutable. Typed
claim lifecycle and placement constraints remain unchanged. No hardware, status,
capacity, provisioning, allocator, network, IPAM, or routing effects are introduced.

The first report has `revision: 1`; every changed report increments revision by
exactly one. Unchanged reports need no increment. Every changed Device spec still
advances `provenance.recordedAt`. A report cannot be removed; correcting all its
location fields to unknown retains its revision and evidence. The report is the
**latest snapshot, not append-only history**. Resource audit logs, not a second
per-Device history or bespoke correction service, are the history integration.

Reports have `observedAt`, optional `facilityRef`/`rackRef`, and independently
optional `position`, `height`, `face`, `fullDepth`, or a non-unit `mount`.
Omission means unknown. Neither the editor nor importer infers actual geometry
from a model or claim. The importer retains model shape separately. Positive
fractional position/height is valid but only complete integral in-range geometry
is drawn in an elevation. Position is 1–100; height is greater than zero and at
most 100; position + height cannot exceed 101. Geometry requires a rack, and mount
and unit geometry are mutually exclusive. Observation cannot postdate recording;
a correction may cite an older observation. Name-only references are local to the
Device namespace. There is no new cross-object admission or reference transaction:
CEL cannot verify existence, incarnation, actual rack height, or facility/rack
agreement. The editor re-reads selected locations and checks those facts before
dispatch, but a referenced object can subsequently change.

### Report source and original import provenance

- `method: Manual` requires nonblank `reason` and `reference` (evidence), each at
  most 1024 characters, and forbids `snapshotDigest`. A human correction of an
  imported Device is attributed to manual observation, **not NetBox authorship**.
- `method: NetBoxImport` requires the matching imported identity, observation
  time and snapshot digest in Device provenance. Legacy reports without `method`
  retain these same import semantics. Existing revision-1 reports remain valid.
- Imported Devices retain `provenance.method: NetBoxImport` and immutable source
  system/type/id matching `identity.sourceIdentity`. The editor changes only
  `reportedPlacement` and `provenance.recordedAt`; original source digest, source
  observation, mapping revision, recordedBy, identity and claimRef are preserved.
  A separately reviewed fresh-source import can advance source evidence without
  changing its system/type/id. `recordedBy` is client attribution, never an
  authenticated actor or authorization grant.
- Old objects without reports remain valid on creation and unchanged updates.
  No migration, delete/recreate, identity replacement or claim rebind is required.

## Authorization and UI

The existing manager role grants create/update/patch for six inventory kinds
project-wide; this change adds **no privilege**, delete/status or hardware grant.
It does not introduce owner or namespace isolation. Managed-field/Flux markers
are a UI/migration exclusion for legacy GitOps records, not server authorization.
Imported operator-managed records are eligible for ordinary manager corrections.
Absence of a marker does not prove ownership, and direct API RBAC still applies.
The separate display-name editor retains its older, narrower UI policy.

Device detail offers **Add reported location** or **Correct location**, manual
source explanation, evidence/reason, observation time, unknown geometry and
optional readable facility/rack choices. Forms freeze the target UID and
resourceVersion on open. Saving renews exact patch permission and re-reads the
Device and selected references, checking operation authority after every awaited
prerequisite, including failed reads. JSON Patch tests UID and resourceVersion
**before** adding the report and advancing recordedAt. Stale baselines, replacement,
session switches, blur, navigation, revalidation and unmount cannot authorize a
new dispatch. Rejected patch admission/preconditions require reopening the form.

Already-dispatched uncertainty remains unknown, with no blind retry. Reference
denials purge restricted choices, input names and cached records. A successful
response must retain UID, advance resourceVersion and match submitted report
fields before success is shown. Inventory queries are invalidated after success,
refreshing directory/filter/location/rack projections. There is no background sync.

Readers prefer the report; a partial report never borrows claim geometry. Without
a report they retain labelled **Claim-derived fallback**, not verified current
location. Full-depth and continuous multi-U/overlap rendering remain supported.
Multiple registrations do not select an active asset by time or revision.

## Existing resource audit integration: source-reviewed, not live verified

On 2026-11-21 the following clean local source revisions were inspected. No
authenticated audit event, deployed audit policy or storage retention was queried
or verified by this implementation. This is not a claim of live audit completeness.

| Evidence | Source-reviewed coverage and limitation |
| --- | --- |
| [Infra audit policy at e9426d5](https://github.com/datum-cloud/infra/blob/e9426d5676a555ba02a990c89d12c144d750d241/apps/datum-control-plane-system/core-control-plane/base/milo-system/apiserver-configs/audit-policy-config.yaml) | ResponseComplete is retained. Fleet is absent from the mutation RequestResponse groups and therefore falls through to **Metadata**: authenticated user/resource/verb/outcome, not change bodies, correction reasons or before/after documents. Auth checks, watches and unauthenticated requests are excluded. |
| [Infra API-server wiring](https://github.com/datum-cloud/infra/blob/e9426d5676a555ba02a990c89d12c144d750d241/apps/datum-control-plane-system/core-control-plane/base/milo-system/apiserver-kustomization.yaml) and [webhook](https://github.com/datum-cloud/infra/blob/e9426d5676a555ba02a990c89d12c144d750d241/apps/datum-control-plane-system/core-control-plane/base/milo-system/apiserver-configs/audit-webhook-config.yaml) | Existing shared policy and batched webhook to Activity's Vector sidecar. Not a transactional, per-edit audit commit prerequisite. Fleet does not own or replace this configuration. |
| [Milo default policy at 3c2a160](https://github.com/milo-os/milo/blob/3c2a160d6d5614d6dc7bf119b8d022fc641c5d4f/config/components/apiserver-audit-logging/audit-policy-configmap.yaml) | Also defaults unlisted API groups to Metadata; the Infra deployment overrides this default. Editing only the default would not fix that override. |
| [Activity schema at 8958fc5](https://github.com/milo-os/activity/blob/8958fc5ba8e96ec02e246edabe3ba33c1b5ee98f/migrations/001_initial_schema.sql) and [storage model](https://github.com/milo-os/activity/blob/8958fc5ba8e96ec02e246edabe3ba33c1b5ee98f/docs/architecture/data-model.md) | Stores raw event JSON and materialized actor, audit ID, scope, resource and status. Audit TTL moves data to cold storage after 90 days, without a delete TTL in this schema; derived Activities have a 60-day delete TTL. This is configuration evidence, not a guaranteed production retention period or delivery SLA. |

**Exact gap / smallest integration:** the Infra owner should add
`fleet.datumapis.com` to the existing RequestResponse mutation rule (or a
devices-only create/update/patch RequestResponse rule before the Metadata
catch-all). That reuses existing actor/change logging; no Fleet correction
service, new mandatory storage, or duplicate history is needed. RequestResponse
retains the patch/PUT and response, not an independent pre-change snapshot.
Confirm one authorized canary's audit ID, authenticated principal, target UID,
patch and response through Activity after deployment, plus actual retention and
delivery configuration. Do not claim full change-body history until then. This
specific logging integration gap does not disable ordinary authorized edits.

## Prepare-only enrichment

For new candidates, `includeReportedPlacement: true` still explicitly opts in
per Device; default importer output is unchanged. Existing-record enrichment is
prepared with:

```sh
python3 hack/device_placement.py \
  --snapshot /private/tmp/review/source.json \
  --target /private/tmp/review/target.json \
  --allowlist /private/tmp/review/allowlist.json \
  --output /private/tmp/review/prepared
```

Target contains `target: {project, namespace}`, `observedAt` from the actual fresh
read and `items`. Allowlist has exactly `source` (source system), `target`, and
`devices`, each with `name`, `namespace`, `uid`, `resourceVersion`, `sourceID`.
Source and target reads must be no more than 15 minutes old and not future-dated.
Preparation binds source/target/allowlist/patch digests, exact before/proposed-after,
UID/resourceVersion tests, dependency snapshots and the server dry-run query.
Output remains `executable: false`, `applied: false`; no API client exists here.
It rejects duplicate selections/source IDs, stale/replaced targets, incomplete
sources, source/rack contradictions, Flux references, legacy/positioned claims,
null source positions and rack overflow. It only prepares first-report enrichment
for already imported RackMembership Devices; claims themselves never change.

The private cohort review is retained outside Git. Existing counts, capacity,
memberships and legacy Flux records must remain unchanged. Null-position PDUs
are not selected and must not acquire guessed position, height or depth. Retained
source/target snapshots are **not fresh execution inputs**. After independent
review and deployment, refresh reads, validate the exact private source/name/
position/face allowlist, confirm the deployed head/schema, retain operation
authority, and server-dry-run each exact patch. Separately approve one canary;
read back its UID and exact spec and inspect its resource audit event before the
remainder. Recheck reference UID/resourceVersion before each future dispatch.

The readback helper classifies exact expected state as Applied, exact unchanged
baseline as Unchanged (never automatic retry), mismatched incarnation/competing
state as Conflict, and unavailable reads as Unknown. A later apply workflow must
reconcile every uncertain dispatch. Never delete, recreate, rebind or prune the
report schema as rollback. This implementation performs **no live writes or
deployment** and does not renew a past authorization to execute migration.

## Validation scope

```sh
go test ./internal/api/inventory/v1alpha1
go test ./...
go test -race ./internal/api/inventory/v1alpha1 ./internal/controller/...
python3 -m unittest discover -s hack -p 'test_netbox_inventory.py'
python3 -m unittest discover -s hack -p 'test_device_placement.py'
pnpm --dir ui/provider test
pnpm --dir ui/provider typecheck
pnpm --dir ui/provider build
pnpm --dir ui/consumer test
pnpm --dir ui/consumer typecheck
pnpm --dir ui/consumer build
bash hack/validate-doc-links.sh
git diff --check
```

Local CEL/schema, JSON Patch, IAM configuration, mocked API/editor and planner
tests verify the implemented contracts and regressions. They do not prove live
IAM enforcement, deployed audit coverage, browser acceptance, cross-object
transactions or readiness to operate hardware.
