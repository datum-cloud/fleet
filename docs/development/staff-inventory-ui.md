# Staff hardware directory

Local implementation on the PR94 baseline (`bebb7eba0af040bd1234a736c8a9c8aeee47b5d8`),
not a deployment or a change to inventory ownership. The
[provider plugin](../../ui/provider/README.md) remains the existing staff-portal
Module Federation remote. Contracts come from the current six-kind API, not the
broader [inventory proposal](../proposals/fleet-management/inventory.md).

## Design direction

Inspected `/Users/scotwells/Downloads/asset-inventory.pen` as read-only JSON, not
`iam.pen`. Relevant native frames are Physical Devices (`IqwfB`), Locations
(`IReVL`), Site Detail (`fbmDU`), Rack Detail (`Kick4`), Cable Plant (`qLV4T`), and
Rack Elevation (`i8lb9P`). The 1440×900 screens use a compact sidebar, a heading
and description, search/filter row, bordered tables, location drill-downs, and
separate rack/connection detail. Reuse those information patterns within the
existing portal shell rather than introducing the design's second sidebar.

Pencil `get_app_state` and `read_skill` both failed with
`transport not connected to app: desktop`; no Pencil app was available locally.
Consequently native visual rendering of the source design is **not verified**.
The structural review is not a claim of pixel-faithful implementation. The source
was never written. Its observed SHA-256 is
`1c20996c68b29206597c69ad2bd6b7833854b906427b2ec30d63b6afd0c40590`.

## Implemented locally

- Hardware-first Devices list, plus Facilities, Racks, Claims, Cables and Power
  feeds with actual record counts, search, detail links, and category empty states.
- Kind/namespace/name query links support host navigation without adding exposed
  modules. Same-namespace typed references resolve to descriptive labels; missing
  references remain visible and never resolve to another namespace's namesake.
- Facilities and racks expose installation plans. A plan's placement remains
  planned, not proof of a device's present location. Legacy location strings are
  displayed but do not fabricate typed relationships.
- Devices retain serials, asset tags, UUIDs, provider/source identifiers, BMC
  endpoint text, typed components, internal mappings, service references, legacy
  interfaces/peers, unmapped MACs, and provenance. All registrations remain visible.
- Cable lanes preserve both typed endpoint references and component positions.
  Power feeds show supply ratings, terminations, and external upstream references.
- Verification and implementation caveats move into contextual disclosures;
  occupancy and power uncertainty remain beside the relevant facts. The optional
  report file picker is removed. Inventory editing is not framed as Git-only.
- Existing six-kind API reads, pagination, federation exports, and the old
  claim-detail route are retained. Narrow authorized record management is
  described below. No sync, provisioning, capacity authorization, IPAM, routing,
  or billing behavior is added.
- Local fixture preview now compiles the installed Datum UI theme. The preview
  stylesheet is isolated from federated pages; inventory CSS uses host tokens.

## Management and authorization contract

The integrated companion IAM role is `fleet.datumapis.com-inventory-manager`,
inheriting `fleet.datumapis.com-inventory-viewer` and adding create/update/patch
for all six kinds. Its empty `physical-inventory-managers` group is bound to
the `datum-cloud` Project, not only the `default` namespace. Existing staff
viewers remain readers. The Fleet role and regression tests are included here;
the infra group/binding patch is separate and has not been applied by this work.
No live membership, grant, or deployed authorization is asserted. The UI never checks
group names or assumes staff membership permits a write.

Locally implemented management is deliberately narrower than that role:

- Facility and Rack creation by namespaced POST, without apply or overwrite.
  The namespace defaults to `default` but is editable; it is not an IAM boundary.
  Rack creation requires an existing same-namespace Facility. Unknown height
  remains absent; known height must be a whole number from 1 through 100.
- Display-name edits only for Manual records whose managed fields all belong to
  `fleet-staff-inventory-ui`, with no import source, deletion marker, or detected
  Flux/Argo/ownership marker. Flux-owned, imported, and unknown-writer records
  remain read-only with an ownership-review explanation. IAM grants do not
  transfer ownership. A reviewed transition for migrated records remains open.
- Before enabling controls and again before submission, POST a
  `SelfSubjectAccessReview` to the provider project's control-plane authorization
  API for the exact namespace/resource/name and create or patch verb. Missing,
  denied, errored, or unresolved permission reviews fail closed. The actual
  inventory API must still authorize the request using the signed-in user's token.
- Edits re-read the record and revalidate ownership, UID and resourceVersion.
  JSON Patch tests UID and resourceVersion atomically, changing only displayName
  and provenance with a newer recordedAt. Identity, claim binding, placement,
  topology and lifecycle fields remain untouched. No delete/status controls exist.
- Submit denial, expiry, and conflicts retain safe input without reporting
  success. Ambiguous network/server failures stop repeat submission and ask the
  operator to inspect inventory before another attempt.
- Every imperative create/edit requires an operation lease tied to both the form
  lifetime and host session generation. The router subscription invalidates it
  synchronously when revalidation/navigation begins or identity changes, before
  React needs to commit an unmount. Blur, page hide, hidden-document and unmount
  boundaries also revoke it. Each awaited permission/record prerequisite is
  followed by an authority check before any write dispatch. Revoked work never
  dispatches a later POST/PATCH under the next cookie session.
- An already-dispatched write cannot be recalled by invalidating its lease.
  Its late response (including success) remains an unknown outcome to the stale
  operation. No automatic retry, cancellation-success or late save confirmation
  is produced. Refresh and inspect authoritative inventory before another attempt.
- Opening an edit captures a deep copy of its original UID/resourceVersion and
  display name. Refreshing props never advances that open form's baseline. A
  concurrent change produces a conflict while retaining input; explicitly closing
  and reopening starts a new reviewed baseline.

Host integration was inspected at staff-portal HEAD
`b1489125fe5b77ea6fc379ad378c8e1951174de5` on November 20, 2026:

- `app/server/routes/internal.ts` accepts all methods under `/api/internal/*`.
  `app/server/lib/proxy.ts` forwards method, body, Content-Type and the caller's
  bearer token, including SSAR POST, inventory POST and JSON Patch.
- `app/server/middleware/auth.ts` requires an authenticated staff session.
  `same-origin.ts` additionally rejects mutation requests without matching
  Origin or same-origin fetch metadata. The browser sends ordinary same-origin
  requests; the plugin neither manufactures auth headers nor bypasses this check.
- `app/server/response.ts` returns successful data in the `data` envelope with
  HTTP 200 and no-store headers, and errors with their actual HTTP status and
  `error` string. The plugin does not depend on upstream creation returning 201.
- `app/layouts/milo.layout.tsx` loads `requireStaffUser`, which returns the
  authenticated IAM User; the route ID is `layouts/milo.layout` in `app/routes.ts`.
  The plugin reads only its metadata UID/name, never tokens or group claims.
- `app/routes/auth/logout.tsx` redirects out of the protected layout; OAuth
  login/callback remount it. The host's QueryClient is a module singleton and
  is not cleared by logout, so inventory must not reuse its caches across users.

The existing access-review contract was inspected at cloud-portal commit
`116ffb527a0c4acc1e28257c7b2b686967d46e5c` (access-review resource service/schema
and generated authorization SDK). This verifies source support, not the revision
running in staging or deployed upstream SSAR grants. Live acceptance must verify
the deployed host revision, same-origin configuration and upstream authorization.
No new proxy endpoint or IAM permission is invented by this implementation.

## Viewing and denial behavior

Collection results are handled independently. Forbidden categories are hidden;
explicit links to them show calm access guidance, not empty or zero inventory.
Other readable categories remain accessible. Unavailable categories show no
count. Both query-linked details and the existing claim detail route handle
denial. Session expiry (401), access needed (403), unavailable API (404), missing
record, and transient failure have separate plain-language states. No raw proxy
errors, global error toast, invented request-access flow, or automatic retry loop
is introduced.

Inventory runs under a private QueryClient keyed to the host loader's authenticated
principal, not the host's persistent singleton. Missing host identity fails closed;
the standalone preview explicitly opts into a separate preview context. Principal
changes synchronously replace the query scope. Blur, page hide and hidden-document
events remove inventory and clear its private cache. Focus, visibility restoration
and back-forward-cache restoration revalidate the actual protected host loader
before mounting a fresh inventory view. Pending/failed revalidation cannot render
old records. Focus checks carry a generation and foreground/visibility condition;
obsolete completions cannot undo a later hide or a newer check. This intentionally
resets open forms on session/background boundaries;
submit denial while the view remains active still retains safe unsaved input.
There is no automatic save or retry when returning to a page.

Completed route navigation starts a new operation scope and query cache, even
when the principal is unchanged. Pending navigation unmounts the inventory view;
old operations stay revoked rather than being reactivated for new forms.
The host router subscription also tracks completed revalidation generations.
Even if React batches loading and idle without rendering a loading screen,
completion replaces the scope and cache; it never renews old operation objects.
Inventory queries are also isolated per mounted view and refresh manually.
Known-denied collections are removed
from the rendered data, including stale supplied results and related names;
global session failure suppresses the inventory. Instantaneous server-side role
revocation while an idle page stays foregrounded is not observable without a
server event; the next read or submit enforces the new result. Live revocation
and total effective grants require integration testing; synthetic checks do not
prove deployed authorization.

## Deliberate gaps

| Design capability | Current boundary |
| --- | --- |
| Device/Claim/Cable/PowerFeed create/edit, assign device, placement actions | Not implemented. The proposed manager role permits record writes, but safe field/lifecycle forms and ownership transitions remain to be designed. Facility/Rack creation and portal-owned display-name editing are the only write affordances. Deletion is excluded entirely. |
| Online/active/connected badges, verified occupancy, free rack units, power utilization or redundancy | The API has no verification status/evidence projection. Do not infer them from records, dimensions, port count, or ratings. |
| Front/rear rack elevation | Deferred. Planned positions are displayed as text, including partial and non-rack-unit placement; no empty space is depicted as available. |
| Device type filters and primary IP | No authoritative device-role/primary-IP fields in this inventory contract. Models, identifiers and existing management endpoint text are shown instead. |
| Cages, maps, site address, circuit/service CRUD, hosted infrastructure | Not represented by the six-kind API. External service identities remain references, not new Fleet authorities. |
| Audit timeline and active replacement | Provenance and all registrations are visible, but neither constitutes a complete revision history or verified active binding. |
| Native Pencil comparison and real host integration | Pencil desktop connection was unavailable. Browser checks use the existing local harness with synthetic data; production staff-portal authentication/proxy and deployment are not exercised. |

## Validation

From `ui/provider`, run `pnpm test`, `pnpm typecheck`, and `pnpm build`.
For local browser checks: `VITE_FLEET_FIXTURES=true pnpm dev --port 7796`, then
open the Inventory tab. Synthetic data is explicitly labeled and includes all
six kinds. No live inventory calls are made in fixture mode.

Local checks after review fixes: 121 tests in 14 files pass; typecheck and production build pass.
The inventory Go package tests validate the exact manager/viewer/editor grants.
Both ordinary and race-enabled package tests pass.
The build reports transitive undici sharing and Zod annotation warnings; these
do not fail the build. Federation exposes retain scoped inventory CSS without
the standalone preview's global theme stylesheet.

Tests cover category navigation, filtering, empty/missing records, typed links,
same-namespace relationship resolution (not namespace authorization isolation),
historical registrations, components, uncertainty, no-read/read-only/manager
states, direct links, partial denial, stale denied data, session expiry, backend
failure, exact access reviews, submit revocation, ownership and revision changes.

Synthetic browser checks exercise facility → rack → claim → device and cable →
feed, search and details at 1280/1440 desktop widths and 390×844 phone size.
Additional intercepted requests verify reader controls are unavailable, manager
creation succeeds only with an acknowledged response, revoked submit retains
input without a write, and subsequent read denial removes visible records.
Desktop/mobile access-needed states, detail-route partial denial, session expiry,
404 and transient failure are checked without raw error leakage or page-wide
horizontal overflow. No live write was made. The standalone harness uses an
in-memory router; direct entry paths are also covered in route tests.

After adding session isolation, browser checks mounted the production boundary
under a synthetic host-shaped data router (not the preview bypass). Switching
authenticated loader identity from a reader to a denied principal removed all
previous device names; blur/focus with an expired identity produced the calm
sign-in state without stale data. A synthetic authorized JSON Patch updated the
facility name; changing its writer to Flux then removed editing controls. These
checks used the real plugin and intercepted proxy envelopes, at desktop and
390×844 phone size, without a live host session or inventory mutation.

## Review and rollout gates

This branch targets `feat/manual-inventory-records` at
`bebb7eba0af040bd1234a736c8a9c8aeee47b5d8`. Publication is authorized; merge and
deployment remain with the parent after independent adversarial review and CI.
The manager role must be deployed before the separate infrastructure group
binding. This work does not import records, add memberships, or modify infra.
Validate the deployed staff-portal revision and authenticated loader ID/shape,
SSAR availability, same-origin proxy configuration, reader/manager/denied users,
session switching and actual upstream denials before accepting staging rollout.
Neither the offline IAM tests nor the intercepted browser harness proves live
authorization. Native Pencil fidelity remains a design gap, not an IAM gate.

These are local checks, not evidence of deployed availability, authorization,
hardware verification, or a completed native Pencil visual comparison.

### Adversarial review regressions

The independent suite against `0673b82f073ab065327192a14c74d2dc6720b003`
was copied into `ui/provider/src/components/adversarial-review.test.tsx` with
its original assertions intact. Before fixes it reproduced three failures and
one passing no-read check. All now pass; additional tests cover same-tick router
invalidation before React commits, create/edit permission and GET prerequisites,
blur/unmount, repeated session generations, Strict Mode lifecycle restart,
already-dispatched success/failure remaining unknown, frozen edit conflicts,
and out-of-order focus completions.

Synthetic browser replay independently delayed the create SSAR, switched the
host principal and released the review: zero writes dispatched. An edit opened
at revision 1 retained input while props/API advanced to revision 2: zero patches
and a calm conflict state. A delayed focus revalidation completed after a second
blur without restoring inventory; a new focus restored it only after a fresh
check. Desktop/mobile checks used intercepted requests only. These fixes await
a new independent review; passing local regressions are not self-approval.
