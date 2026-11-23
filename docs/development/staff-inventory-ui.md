# Staff physical inventory UI

## Grouped inventory navigation (implemented)

This follow-up starts from PR133's exact head
`15f99a3234019faac8dca53248216d7719b60f97`. PR133 merged into
`feat/manual-inventory-records` as `ce65b369ccf4a0d113cd23db7ab5052b089a5b87`;
this navigation change retains that required source head rather than incorporating
unrelated target-branch changes. It supersedes the flat nine-category navigation
described in the historical notes below, not inventory contracts or IAM.

| Primary route tab | Secondary navigation | Meaning |
| --- | --- | --- |
| Equipment | Devices, Cables, Power feeds | Physical assets and recorded connections/supplies |
| Locations | Operational sites, Facilities, Racks | Site operational membership is distinct from physical Facility premises and rack containment |
| Installation plans | None | The directory remains **Claims & registrations**: DeviceClaim intent, legacy slots, and registration history, not a claim that every legacy record is an installation plan or allocates capacity |
| Hardware catalog | Manufacturers, Device types | Reference descriptions/templates, not installed assets or available capacity |

- Uses the installed Datum `Tabs`, `TabsList variant="line"`, and
  `TabsLinkTrigger` with React Router links, matching the pinned Compute and
  cloud-portal sources. No custom underline imitation, sidebar, or category menu.
  Primary tabs remain mounted; secondary tabs depend only on the selected group,
  never access results. Each row scrolls horizontally on narrow screens.
- Equipment / Devices is the default. Group links select the first kind unless
  already in that group. Existing `?kind=...`, record/namespace bookmarks, child
  create routes, and legacy claim URLs remain valid. Detail and task breadcrumbs
  include group and category, for example Equipment / Devices / record name.
  Protected names appear only after successful access checks, not from URL text.
- Search, filters, refresh, and Create remain page controls, not navigation.
  Same-category links retain `q`, `location`, and `model`; switching categories
  starts an unfiltered directory, and browser Back/Forward restores prior URLs.
  Clicking the already-current tab is a no-op, not an implicit refresh.
- Same-principal presentation navigation reuses authorized default-namespace
  collection data, including changes to the three known client-side filters
  and the detail pages' `tab`, `selectedKind` and `selected` parameters.
  The narrow presentation allowlist does not include unknown query parameters,
  route changes, form submissions, or explicit revalidation. Host loaders still
  verify navigations; changed principal, logout, real denial, hidden/pagehide,
  and unknown-scope boundaries still fence/clear private data. Operation
  generations restart independently of retained read caches.
- Route tabs use manual activation: arrows/Home/End move focus, Enter or Space
  activates the focused link. Lists are named and selected tabs expose their
  selected/current state, with visible keyboard focus. No dangling panel IDs are
  emitted for route-linked tabs. Dirty dialog/task navigation, immutable edit
  baselines, unknown write outcomes, and visible blur/focus no-op behavior remain.

Synthetic validation covers direct links for all nine kinds, group defaults,
detail/task breadcrumbs, dirty task navigation, keyboard focus/activation,
Back/Forward and filters, stable access-state navigation, and zero additional
collection reads for same-principal presentation changes. Provider and consumer
full tests, typechecks, and production builds pass. Local Chromium fixture checks
at 1280px and 390px verify actual underline components, mobile scroll containment,
group and secondary routes, rack breadcrumbs, and Arrow/End/Enter/Space navigation
without page errors. The 390px page stays 390px wide while the primary tab row
scrolls internally. These are not authenticated staff-portal or staging acceptance.
No live writes, deployment, schema changes, or new capacity-allocation semantics
are introduced; Go validation is not rerun because this follow-up changes no Go.

## Page design rules

Most people open this dashboard to find one record and confirm it is the right
one. Detail and list pages follow these rules so that they are not slowed down
by unnecessary or duplicated information. Follow them when adding a kind,
field or view.

1. **Compact header.** The header shows the display name and one summary line:
   lifecycle (only when it is not the normal state), location and issue
   badges. Generated resource names, the namespace, the UID and the creation
   time go in a collapsed **About this record** disclosure at the end of the
   page. The breadcrumb is the way back; there is no separate back link.
2. **One primary view per kind**, above the fold:

   | Kind | Primary view |
   | --- | --- |
   | Device | Identity strip (model, serial, asset tag), component counts, then the connection view |
   | Cable | End A and End B side by side, with one row per lane |
   | PowerFeed | Supply rating, then outlets and what each one is cabled to |
   | Facility | Its racks |
   | Rack | Its elevation |
   | Site | Its linked facilities |
   | DeviceType | Component templates grouped by chassis face, then type |
   | Manufacturer | Its device types |
   | Claim | Planned placement, expected hardware and registered devices |

3. **Secondary detail goes in tabs held in the URL** (`?tab=`), for example
   Components, Cable records, Location, Registration and History &
   verification. A tab label carries its count, or `?` when the collection is
   unreadable. Every panel stays mounted and inactive panels are `hidden`, so
   open dialogs and permission checks keep their state. `tab` is in the session
   boundary's presentation allowlist, so switching tabs never refetches
   inventory. Links to other records never carry `tab`.
4. **Say each fact once.** Do not repeat a facility or rack name in rows when
   the header or breadcrumb states it. Put type words and shared values (for
   example "Power outlets · IEC C19") in the group header, not on every row.
   Do not show a field in both the summary and the body. Replace a related
   records list with a count that links to the tab holding the list.
   `ui/provider/src/lib/display-names.ts` holds the helpers for this, next to
   the rack page's `condensedName` for port tiles.
5. **Issues first, quiet when normal.** Disagreements, unknown placement,
   conflicts and unreadable or missing records become short banners at the top
   of the page that link to the problem (`IssueBanners`). Empty and "not
   recorded" fields are hidden by default (`Facts`). Keep an absent value
   visible only when the absence itself matters: verification is unknown,
   power budget is unknown, and "No cable recorded" stays on outlets because
   it means unknown, not free.
6. **Honesty notes stay but shrink.** "Not verified", "Cable records, not live
   link state" and "unknown, not free" appear once per view as a legend line
   (`ViewLegend`), not on every row. Never remove the meaning: unknown must
   never look free, and nothing may look verified. When a collection is
   unreadable, counts and states read as unknown, never zero or empty.
7. **List pages are search-first.** Search and filters sit at the top and keep
   their URL parameters (`q`, `location`, `model`). Rows show only the columns
   needed to pick the right record (Devices: name, model, location, serial;
   Cables: name, medium, End A, End B; Racks: name, facility, height) and no
   generated names, except to tell apart catalog records whose display names
   collide. Clicking a row opens the record; the name stays the keyboard link.
8. **Mobile.** At 390 px the header, primary view and tabs must work without
   horizontal page scroll. `pnpm screenshots` fails a page that overflows.

Write controls stay available but quiet: display-name editing and Refresh sit
in the header actions, and reported-location correction is on the Device's
Location tab. Their dialogs, task pages, dirty-dismiss confirmation and
unknown-outcome handling are unchanged.

The building blocks are in `ui/provider/src/components/inventory-record-layout.tsx`
(`RecordSummary`, `IssueBanners`, `RecordTabs`, `Facts`, `ViewLegend`,
`AboutRecord`), and the per-kind views are in `inventory-record-detail.tsx`.
`inventory-record-layout.test.tsx` covers the disclosure, tab URL state, issue
banners, hidden empty fields and list columns.

## Default-only directory and stable controls

Implemented on top of `baa210318fe17927f447a8e9e9e8053a23f6b173` on
`feat/manual-inventory-records`. This update supersedes the namespace-choice and
access-dependent navigation descriptions in the historical phase notes below.

- All nine inventory collections, including Sites, Manufacturers, and DeviceTypes,
  are read from `/namespaces/default/`, including every continuation page.
  Collection responses and rendered inventory are filtered to explicit `default`
  metadata; foreign and missing namespaces are not silently relabeled.
- The directory, query-string details, and legacy claim detail route cannot select
  a foreign namespace. A missing query namespace means `default`; an explicit
  foreign namespace shows safe unsupported-scope guidance rather than resolving
  even a same-named default record. Old explicit default links still work.
- Creation, display-name editing, reported-placement editing, management reviews,
  and reference rereads reject nondefault scope before any request. Reference
  responses must also belong to `default`. Reference choices and relationship
  projections only use readable default records. Inventory query cache keys
  include the fixed namespace; real denial still redacts cached data and drafts.
- The namespace input and Namespace & access disclosure are removed. Namespace
  columns and repeated default labels are unnecessary in this UI. Public group
  and selected-group category labels never disappear or change their active default because of
  access results. No private record names or counts appear in the public shell.
- Pending, allowed, and denied directory states share heading, action, count,
  status, and search/filter layout slots. Actions remain mounted and disabled
  until usable; successful authorization adds no “access checked” clutter.
  Explanations for ownership restrictions, denied access, unavailable references,
  expired sessions, and uncertain saves remain visible. Long failure explanations
  may wrap rather than being clipped. The composition follow-up below replaces
  expanding forms with focused dialogs and child task pages.
- Existing immutable edit baselines, submit-time authorization, read/write
  generation guards, principal/logout and hidden/pagehide fences are retained.
  Visible window blur/focus remains a complete no-op, including draft, DOM,
  request, loader, and operation-authority behavior.

This is a frontend scope restriction, not a new security boundary. Backend
namespace support, schemas, IAM policies, other Bare Metal surfaces, audit logs,
and live inventory are unchanged. Backend default-only enforcement or namespace
migration remains deferred and would require separate design and authorization.

Validation includes provider and consumer Vitest suites, typechecks and builds,
inventory Go tests, documentation links and whitespace. New synthetic tests cover
all nine kinds, static layout structure through access transitions, foreign URL
and legacy-route selection, paginated default requests, and foreign reference and
mutation guards. DOM/CSS structure checks do not prove pixel geometry in the
authenticated staff portal. No browser login manipulation, live writes, deployment,
or authenticated visual acceptance was performed for this update.

### Platform composition follow-up (implemented)

Implemented from PR129's exact head
`0644a207f1f7200c6df90e54ad00c92b2455a15c`; PR129 subsequently merged into
`feat/manual-inventory-records` as `387397be709be2518bb74e264d575c6b8475f82a`.
This follow-up changes presentation and navigation, not domain APIs or authority.

The platform audit confirms that the mismatch is page/form composition, not a
missing Datum primitive library. Source exemplars reviewed from the supplied audit
archives (these are source observations, not authenticated visual acceptance):

| Source and supplied pin | Concrete pattern | Fleet follow-up |
| --- | --- | --- |
| [Compute page chrome](https://github.com/datum-cloud/compute/blob/9521a49e10c55e13b42d652533caaa5c7fe09494/ui/consumer/src/components/workload-page-chrome.tsx) and [plugin tabs](https://github.com/datum-cloud/compute/blob/9521a49e10c55e13b42d652533caaa5c7fe09494/ui/consumer/src/components/plugin-tabs.tsx) | Breadcrumb, PageTitle actions, and line-style route tabs; disabled action while authority is pending | Standardize directory/detail breadcrumb and title/action/table chrome without copying provisioning behavior or introducing a global sidebar |
| [Cloud portal domain form](https://github.com/datum-cloud/cloud-portal/blob/7456e469a8589d03afe13623b15992e712563d0f/app/features/edge/domain/domain-form-dialog.tsx) | Focused controlled `Form.Dialog`, closing after an acknowledged successful mutation | Separate focused display-name and reported-location dialogs; compact Facility/Rack create dialogs |
| [Infra configurator part form](https://github.com/datum-cloud/infra-configurator/blob/25e5dc7d26ba892643f7dd7dadbc8711e4b1abe8/src/components/catalog/part-form-dialog.tsx) | Controlled Datum Dialog with an explicit viewport height cap and overflow scrolling | Bound dialog scrolling and keep explicit Cancel / Create record / Save changes actions accessible |

- Directory, details, and task pages use a local composition of Datum Breadcrumb
  and PageTitle, with explicit accessible headings and stable action/status slots.
  Existing line-style Bare Metal tabs remain; the grouped-navigation follow-up
  organizes the nine inventory categories without changing their contracts. No
  new global navigation or unsupported Activity/audit surface is added.
- Facility/Rack creation and portal-owned Facility/Rack/Device/Cable display-name
  edits use controlled Datum Dialog. Reported-location correction is a separate
  dialog: correcting an observation never means a physical move or reservation.
  These surfaces no longer expand the directory or detail page.
- Device, Cable, and installation-plan creation use
  `inventory/create/device`, `inventory/create/cable`, and
  `inventory/create/installation-plan`. The manifest and federation exposure
  register the child routes alongside the unchanged legacy claim route. Unsupported
  task kinds and foreign namespace URLs fail closed before reads or writes.
- `q`, `location`, and `model` directory filters are URL-backed. Task/back links
  retain only those filters and the relevant kind, not arbitrary redirect targets
  or foreign scope. Same-category record/back links preserve filters. Back/forward
  and direct task URLs work without storing drafts in browser storage or URLs.
  Search applies on Enter or **Apply search**; keystrokes remain local so host
  loader revalidation cannot interrupt typing or weaken session fences.
- Dialogs provide focus trapping/restoration through Datum's Radix-backed primitive,
  viewport-bounded scrolling, and explicit Cancel / Create record / Save changes.
  Native field errors have an accessible summary, field descriptions, and focus
  links; failed saves focus a summary while sanitized status stays visible.
- Dirty dismissal (Cancel, Escape, outside click, and router navigation) requests
  browser confirmation. Reload/close uses `beforeunload`. Pending dialog dismissal
  is disabled. Explicit navigation during a pending write requires a separate
  warning that leaving does not cancel the write. Unknown results keep the form
  open and disabled, with **Leave form**, not Cancel; leaving requires an explicit
  unknown-outcome warning. No retry or success is inferred from dismissal.
- Existing exact scopes, captured UID/resourceVersion baselines, after-await
  checks, denied-reference redaction, real hidden/logout/principal fences, and
  inert visible blur/focus remain. Session boundaries still discard private drafts;
  the router guard does not intercept explicit logout routes. Verification Unknown remains visible.

The inspected [staff-portal federation host](https://github.com/datum-cloud/staff-portal/blob/a0c4cd3e4bd9e0343ffe3310caaa25b160073b41/app/modules/plugins/client/federation-host.ts)
shares only Datum badge, button, card, icons, and skeleton, alongside React,
React DOM, router, and query singletons. Dialog, Breadcrumb, PageTitle, and the
existing field primitives are bundled from Fleet's installed Datum UI; no new
dependency or shared-module declaration is added. Controlled Dialog wraps the
existing guarded form handlers instead of adopting a new submission owner.
Portal `Table.Client` and `useTableUrlAdapter` remain application-local and are
not imported. The host's nested plugin Routes accept the additive task exposure.

Validation: provider and consumer suites/typechecks/builds, inventory Go race tests
with local disposable Kubernetes 1.37.0 envtest assets,
documentation links, and whitespace. Regression coverage includes all five create
kinds, all four supported label edits, separate location correction, native field
validation/focus, dirty dismissal, pending/unknown results, child routes/history,
default-only crafted URLs, StrictMode task activation, permission changes, and session transitions. Existing
real-handler tests continue to cover exact write bodies and operation fencing.
Local Chromium checks use only a temporary synthetic fetch harness: desktop and
390px mobile layout, dialog focus/dirty Escape/restoration, task routing, no page
overflow, and a scrollable location dialog capped at 90dvh. The harness is not
shipped. These are synthetic UI/accessibility checks, **not authenticated staff
portal acceptance**. No login manipulation, live mutation, deployment, or IAM
change occurred. Full authenticated/federated acceptance, catalog editing,
backend namespace restriction, and operational provisioning remain outside scope.

## Historical implementation phases

The additive [Site–Facility directory](site-facility-relationship.md) extends this
baseline with operational Site reads and provider-qualified premises identifiers.
Its identity/membership fields are managed by direct API, not new UI forms.

Local, uncommitted implementation on `0b6c8977227157575d388a42aac127769258f998`
(PR95 merged into `feat/manual-inventory-records`). This phase does not publish,
deploy, change IAM membership, or change inventory ownership.
The [provider plugin](../../ui/provider/README.md) remains the existing staff-portal
Module Federation remote. The implemented six-kind API, not the broader
[inventory proposal](../proposals/fleet-management/inventory.md), bounds the UI.

## Native Pencil comparison

Read and rendered `/Users/scotwells/Downloads/asset-inventory.pen` with Pencil's
native Get, TakeScreenshot and Export tools. No design mutations were made.
Source SHA-256:
`1c20996c68b29206597c69ad2bd6b7833854b906427b2ec30d63b6afd0c40590`.

Inspected Physical Devices (`IqwfB`), Locations (`IReVL`), Site Detail (`fbmDU`),
Rack Detail (`Kick4`), Rack Elevation (`i8lb9P`), Cable Plant (`qLV4T`), device
create/edit (`owtU7`, `txtZw`), site registration (`lj6gc`), add-device panel
(`vSVae`), non-racked equipment (`GcX1u`), and cable creation (`zFUzi`). Native
exports of Devices, Rack Detail and Cable Plant are retained locally under
`/tmp/fleet-pencil-reference/`; they are not runtime assets.

| Design pattern | Before this phase | Local implementation |
| --- | --- | --- |
| Compact heading, search/filter, dense bordered rows | Duplicate Bare Metal and inventory headings; six large kind tiles | Inherited portal tabs, compact category tabs and one section heading; real model/facility/medium filters |
| Location drill-down | Generic record/relationship directory | Facility racks and facility-only equipment; descriptive facility → rack → equipment links |
| Front/rear elevation beside equipment | Placement text only | Recorded plan geometry beside equipment; separate unpositioned and non-unit mounts |
| Device identity and components | Read-only generic details | Hardware registration with typed components; immutable identity preserved in details |
| Cable plant and endpoints | Generic records | Endpoint columns, medium filter, lane details and typed endpoint creation |
| Restrained physical forms | Facility/Rack only | Facility, Rack, installation plan, Device and Cable creation with permission checks |

The result follows the actual design's information layout, not its invented
operational facts. The host sidebar is not duplicated. Phone layouts stack
sections and keep wide tables internally scrollable. This is not a claim of
pixel identity or completed authenticated staging acceptance.

## Supported workflows

- Browse Devices, Facilities, Racks, Installation plans (the API's DeviceClaim),
  Cables and Power feeds. Counts are loaded records, not verified hardware.
- Search recorded values; filter by actual model, facility and cable medium.
  Namespaced detail links, typed references and historical registrations remain.
- Rack elevation renders only complete, in-range RackUnits plans with a known
  front/rear face and known rack height. Blank units are unverified, never free
  capacity. Unknown placement stays in the explicit unpositioned section;
  NonRackUnitMount remains separate. Multiple registrations do not select an
  active device. Overlapping plans are labeled for review, not resolved silently.
  Multi-unit plans render as one continuous block spanning their recorded unit
  range, with one detail link; overlapping plans occupy separate visual columns.
  Model height alone never supplies missing placement geometry.
  Occupancy is face-aware
  ([semantics](../operations/physical-inventory.md#rack-faces-depth-and-component-faces)):
  full-depth equipment, and equipment of unknown depth, is drawn on both
  elevations, muted and hatched on the far face and labelled "rear of" the
  equipment; half-depth equipment is drawn only on its mounted face, so the
  other face keeps its unverified dashes. Each block carries its depth source
  (reported, recorded on Device, catalog, or "depth unknown"). Overlaps on the
  same face are marked `!` and labelled "records disagree"; they are never
  blocked. The logic is in `ui/provider/src/lib/rack-faces.ts` and
  `device-depth.ts`.
  The Rack page layout is described under [Rack page](#rack-page-implemented).
- Create Facility/Rack facts; rack height is optional. Create an Installation
  plan at a known Facility or Rack with unknown unit position, then separately
  register a Device with a real serial or system UUID and typed components.
  The form does not offer the `identity.unidentified` marker; record such a
  Device through the API. Directory and detail views show the marker and its
  reason as "No hardware identity".
  This is not an atomic multi-record workflow or an adoption operation.
- Create Cable records with a medium and named lanes using existing readable
  Device/PowerFeed components with recorded positions. No guessed ports, cable
  lengths or live connectivity. Reference owner UID/resourceVersion is retained
  and checked before creation; the API still cannot atomically reserve endpoints.
- Edit display name only for records with established
  portal ownership. No device identity, claim binding, components, topology,
  placement, lifecycle, status or operational changes are exposed through edit.
- Correct Device-reported location in a separate form, including the first report
  on an existing Device. Evidence reference, reason and explicit
  unknown geometry are retained without changing identity or claim binding.

## Authorization, ownership and concurrency

Device-reported locations and labelled claim fallback now share a placement
projection. See [Device-reported placement](device-reported-placement.md) for the
additive contract and ordinary authorized corrections, including first reports
on existing Devices. Immutable identity and claim binding remain intact.

The existing `fleet.datumapis.com-inventory-manager` role inherits the viewer and
adds create/update/patch for six kinds, project-wide. The UI uses exact-resource
SelfSubjectAccessReviews and the signed-in principal's API request, never group
names or staff membership. Role/binding deployment and membership are separate
work; nothing here asserts their live state. There are no delete/status grants.

Inventory is direct API/operator-managed, not GitOps-only. Legacy Flux-managed
records keep their writer until an explicit safe transition. This UI's
deliberately narrow display-name edit policy still requires exclusively
`fleet-staff-inventory-ui` managed fields, with no deletion/other-owner
markers. The separate reported-location editor permits operator-managed
Devices under normal manager authorization, excluding legacy Flux markers from
its UI. These markers are not server authorization or owner isolation. Location
record correction is not a handover or installation move.
New authorized API records do not require editing Git.

Before writes, the UI renews exact create/patch permission. Hardware references
are re-read and checked against selected UID/resourceVersion and lifecycle/type.
Each awaited prerequisite rechecks the operation authority. Edits freeze their
original UID/resourceVersion and use atomic JSON Patch test operations. They do
not silently adopt a refreshed baseline. No automatic write retries occur.

Existing session protections are retained: principal-isolated QueryClients,
authority invalidation at session/revalidation/navigation/background/unmount
boundaries, fresh authority generations even for React-batched revalidation,
and generation-guarded background-return checks. Already-dispatched uncertain
outcomes stay unknown rather than becoming cancelled-success.

### Browser focus is not a session boundary

Implemented against PR103's merged staging base
`f190d28397fc6110e02c9c83e6819a3a584258d0`. Ordinary visible window blur/focus
is deliberately ignored. Opening the URL bar does not change the session:
it does not suspend reads or writes, reset edit baselines, close or disable forms,
change controls, or start host/permission/inventory requests.
Both the session boundary and individual management/location forms have no
blur/focus listeners. QueryClient defaults and inventory queries disable window
focus refetch. Drafts and frozen baselines remain usable without reopening.

Actual document hidden/pagehide events still synchronously invalidate operation
and read scopes, cancel queries and mask private children. Visibility restoration
or pageshow revalidates the host only after an observed hidden/pagehide boundary.
Overlapping background events advance a generation so an older verification
cannot restore authority. Failed verification remains masked. Explicit host
revalidation/navigation and observed principal changes also fence operations and
replace the private query cache, including fast batched A→B→A transitions.
Cached permission is never substituted for a confirmed denial; denied references
are redacted. Genuine session boundaries do not transfer drafts, immutable edit
baselines or pending intents into a new authority generation.

The original PR104 disable-on-blur/reopen design was superseded by this simpler
trigger model before review/merge. Stable wrappers and native disabled controls
remain useful for actual permission refreshes, but browser chrome focus alone
never activates those states. This is not a new inventory or IAM contract.

Backend per-request authentication/authorization, submit-time exact permission
checks, after-await operation checks, and atomic UID/resourceVersion JSON Patch
tests remain required. A cookie/account change not yet observed by the host
cannot be detected atomically by this UI. Focus is not authentication, and this
change does not claim protection against an unobserved cookie transition between
requests. Observed host boundaries revoke pending work; backend checks protect
each dispatched request. Unknown dispatched outcomes are not reported as success.

### Regression evidence and acceptance limits

PR104's integration with feature-target revision
`5b60656759033b61674ee202a9ad94f4d49f4a64` retains PR106's removal of provenance,
Device source identity, component source references, reported-placement method
and snapshot digest, and the built-in NetBox importer/planner. The portal does not
restore those fields as compatibility inputs or infer writer ownership from them.
Display-name edits retain managed-field ownership checks; both label and location
patches retain exact UID/resourceVersion guards. Creation metadata is display
history only, never evidence of a current Device or verified placement.
The visible-focus no-op and actual host/background authority fences are unchanged.

The actual-component stable-controls matrix covers all five create kinds,
display-name and location editing, collapsed/expanded access, and StrictMode.
Repeated visible blur/focus preserves exact markup, DOM nodes, enabled controls,
drafts, disclosures and captured operation authority with no added requests.
Hosted pending-save tests cover delayed permission, baseline reads and dispatch;
saves continue across visible focus with the original UID/resourceVersion tests.
The same hosted cases revoke pending work across true hidden/visible and principal
changes; already-dispatched results never publish success into the new view.
Inventory-page tests also drive the query focus manager and assert no added
requests. Previous blur-revocation cases now use real hidden/pagehide or explicit
host boundaries, retaining delayed-operation, A→B→A, denied-read and unknown-write
coverage. These tests are local regression evidence, not independent signoff.

Earlier synthetic browser geometry/keyboard checks at PR104 head `4a30bb7`
exercised the now-superseded disabled-view behavior; they are not acceptance
evidence for this revision. The real staging URL redirected to sign-in and no
login was manipulated. Authenticated Safari URL-bar acceptance remains unverified.
Independent review, CI and authenticated staging/Safari acceptance remain release
gates; this work does not merge, deploy or change live inventory/IAM/NetBox data.

Revised local validation (2026-11-21): provider 495 tests in 32 files and consumer
44 tests in 3 files pass, along with both TypeScript checks/production builds,
`go test ./internal/api/inventory/...`, documentation links and whitespace.
Temporarily restoring the management form's blur invalidation fails 29 of 56
focused stable-controls/pending-save tests; the mutation was removed before the
full passing run. This is sensitivity evidence, not independent review.

Revised synthetic browser checks at 390px retain identical markup, form/control
nodes, bounding rectangles and request counters across visible blur/focus for
all five create kinds with collapsed/expanded access, plus display-name and
location editors. Native Tab advances to an enabled form input; subsequent
synthetic blur/focus retains that focus and markup without requests. Pagehide
removes forms, held pageshow verification keeps them absent, and release restores
fresh data without old drafts. No writes occurred. These are synthetic Chromium
DOM/geometry/event checks, not real browser-chrome or Safari acceptance. The owned
tab and fixture server were closed after checking.

For an optional synthetic browser check, run from `ui/provider`:

```sh
pnpm exec vite --config vitest.config.ts --host 127.0.0.1 --port 7793
```

Open `/session-fixture.html`. This mounts the actual inventory page and boundary
under a synthetic host loader, not the preview bypass. API calls are handled in
memory; writes return a synthetic failure and never reach an API. The console's
`inventorySessionFixture` exposes `hold(boolean)`, `release()`,
`principal(string|null)`, `denied(boolean)`, and `stats()`. Blur/focus should do
nothing; pagehide followed by pageshow exercises held restoration. This harness
is not a production build entry and is not evidence of real Safari acceptance.

Read-only users browse without enabled writes. No-read and direct-link denials
show calm access guidance, not empty/zero inventory. Partial collection failures
preserve readable sections and do not offer inaccessible references as choices.
401, 403, 404 and transient errors remain distinct. Submit denial retains safe
input, blocks retries and explains the change. Restricted cached records are
removed on an observed denial/session boundary. Reads are manually refreshed;
this is not instantaneous notification of a server-side grant change during an
idle session. Detail pages now offer the same explicit Refresh inventory action.

A failed submit-time reference read is distinct from a denied write. It carries
the reference kind and namespace, removes that kind's cached records and form
choices conservatively across the current view, and clears selected references
without discarding unrelated user-entered fields. In-flight inventory reads are
cancelled before cached records are removed. Session expiry hides all kinds.
Reopening inventory starts a fresh access evaluation; refreshing an existing view
does not clear its recorded read failures. Search indexes explicit visible fields
and readable endpoint identities, not raw records or redacted references.
The retained direct claim route uses the same issue-aware placement and endpoint
labels. Legacy expected peers hide both the referenced device and port whenever
Device reads are unavailable, including temporary collection failures.

### Retained host source evidence

The prior implementation inspected staff-portal source at
`b1489125fe5b77ea6fc379ad378c8e1951174de5`: `app/server/routes/internal.ts`
accepts methods under `/api/internal/*`; `app/server/lib/proxy.ts` forwards body,
Content-Type and the caller's token, supporting SSAR POST, inventory POST and
JSON Patch. Authentication and same-origin middleware still apply. Successful
responses use an HTTP 200 `data` envelope; failures retain actual HTTP status
and an `error` string. No new proxy method is assumed by the added forms.

The protected `layouts/milo.layout` loader supplies the IAM User metadata
UID/name. The host QueryClient is a singleton and must not be reused for private
inventory across principals. The existing plugin boundary provides the isolated
cache and synchronous router invalidation. Source support does not prove the
deployed revision, upstream SSAR permission, or authenticated runtime behavior.

## Device connection view (implemented)

Device detail shows a **Connections** section in its primary view, under the
identity strip; the component list is on the Components tab. It is labelled "Cable records, not live link state". It joins the Device's typed
components to the Cable lanes that name them, using the Cables, Devices,
PowerFeeds and DeviceTypes the Inventory tab already lists. It adds no
requests, API fields or permissions.

`deviceConnections()` in `ui/provider/src/lib/device-connections.ts` is a
pure join. It returns one link per component position and per claiming lane,
grouped as power, network, console, pass-through, modules and items, or not
on this Device. Each link has one of five record states:

| State | Glyph | Meaning |
| --- | --- | --- |
| Cable recorded | ● | Exactly one Cable lane names the position, and the readable peer records the far component and position. |
| No cable recorded | ○ | No readable Cable names the position. This means unknown, not free. |
| Records disagree | ! | Two or more lanes name the position or the far port, or a Cable names a component or position that this Device or the peer does not record. Every claiming Cable is shown, and none is treated as current. |
| Peer unavailable | ? | The peer collection is denied, or the named peer record is not in the namespace. A denied peer shows no name, component, position or rating. A missing peer shows only what the Cable records, marked "record not found". |
| Catalog only | ◇ | A DeviceType template whose id (matched by convention) is not recorded on the Device. |

A breakout lane maps to its own component position. PowerFeed peers show
their recorded supply rating, and the power group always states that power
redundancy is unknown. When a peer Device records an internal mapping
(FrontPort to RearPort, or PowerOutlet to PowerInlet), the link traces onward
for up to four hops, stopping at the first hop that is unrecorded, disputed
or unreadable. A Device's own internal mapping is shown on its local port.
When Cables cannot be read, the view says that connections are unknown and
classifies nothing.

### Names

Views never show a generated resource name (`netbox-ca213d185ea9153b853f295e`,
or any name with a run of twelve or more hex digits, or a UID) by default.
`humanName()` in `ui/provider/src/lib/display-names.ts`, the single home for
name condensing, uses the display name, or the resource name when it reads as a
human name (`pdu-a.rk105.yul3`). Without one, a Cable is named by its medium
("Copper cable") and a peer by its kind ("Unnamed device"). When two different
records would read the same, `viewNames()` adds a short suffix from the
resource name ("Copper cable #295e"). A denied peer is never named or
disambiguated. Full resource names appear only in a port's pinned card. Type
words ("Device", "Power", "NetworkPort") appear only in group headers.

### Port tiles (default view)

The view opens on **Ports** at every size. `portLayout()` lays the tiles out
as a faceplate: by chassis face (Front, Rear, or Face unknown), then one
cluster per type, ordered by the first component of that type, which follows
the DeviceType's template order. Each face's title states its derived rack
face once ("Rear · rack rear"); when no port has a recorded face, one caption
reads "Component faces not recorded". `portLayout()` is the only place that
decides placement, so DeviceType layout data (rows, columns, banks) can
replace template order there later. A cluster that is entirely catalog only
is hidden behind a "Show … catalog only" button.

The faceplate is the shared `PortFaceplate` component in
`ui/provider/src/components/device-faceplate.tsx`, which the rack side panel
uses too. Its pieces are exported for layout rendering: `PortTileButton`
(state, accessible name, optional short label, optional connector shape and
lane states), `PortTooltip` and `PortCard`.

Each tile is one component. In a cluster of 12 or fewer
(`TEXT_TILE_LIMIT`), a tile shows a short label (`PSU 1`, `eth0`, `BMC`) that
`shortLabels()` keeps unique within its cluster, starting from the shared
`condensedName()` abbreviations. A larger cluster draws port-shaped squares
with no text, like a switch faceplate, and prints faint port numbers at the
first port, every eighth port (`NUMBER_EVERY`), each bank edge where the
connector changes, and the last port, so port 37 is easy to find. A
multi-position component
draws one lane bar per position, and the tile takes its most urgent lane
state. The outline follows the recorded connector only when the free text
clearly names a family (RJ45, SFP, QSFP, IEC C13/C14/C19/C20); anything else is
a generic tile. State never relies on colour:

| State | Tile |
| --- | --- |
| Cable recorded | Solid foreground fill |
| No cable recorded | Empty outline |
| Records disagree | `!` and a 2 px destructive outline |
| Peer unavailable | `?` and a dashed outline |
| Catalog only | Faint dashed outline |

The label stays in the foreground colour on disagreeing tiles, because the
destructive token measures 3.4:1 (dark) to 3.9:1 (light) against the card,
which is under the 4.5:1 that 11 px text needs. Measured in both themes, tile
labels are at least 6.2:1 and outlines at least 3.4:1.

The tile grid is one tab stop. Arrow keys move between tiles by their drawn
rows (left and right, up and down, Home and End). Each tile's accessible name
gives the port, state and every peer and Cable, so nothing hidden visually is
lost. Hover or keyboard focus shows a one-line tooltip ("swp37 · no cable
recorded"), hidden from assistive technology because it repeats the
accessible name. A click, tap, Enter or Space opens the port's card, a
non-modal dialog named "swp37 details" with the connector, any identity, links
to the Cable and peer, the reasons records disagree, any opposite-face check,
any trace, the full resource names, and a **Health** section that reads "No
health reported" because no kind reports observed status. On screens 600 px
wide or less, the card is a bottom sheet. Touch shows no tooltip. Escape or
Close closes the card and returns focus to the tile.

The section header is one line: the port counts ("60 ports · 7 recorded · 51
unknown · 2 disagree"), "Unknown is not free; nothing here is verified", and a
**Legend** toggle. It does not repeat the Device's name, which the page heading
already shows.

### Diagram and list

**Diagram** is offered only up to 16 links (`DIAGRAM_LINK_LIMIT`), because
the tiles show larger devices in a fraction of the height. It puts the Device
in the centre, power peers on the left, and network, console and other peers
on the right. Each line shows the local and remote port and the Cable. Its
style (solid, dotted, double, dashed, or faint dashed with a hatched peer)
repeats the state glyph. State words appear only for lines that are not
recorded. The diagram has one tab stop; the arrow keys, Home and End move
between Cable and peer links, and Enter or a click opens the record. **List**
is a table per group with the same links, states, reasons and traces, using
the same condensed names. Styling uses only the host's tokens
(`--foreground`, `--background`, `--muted`, `--muted-foreground`, `--border`,
`--accent`, `--primary`, `--destructive`, `--card`, `--popover`, `--ring` and
`--radius`), and the diagram stacks into one column below 600 px.

`pnpm screenshots` captures the tiles (`ports-server`, `ports-leaf`), open
cards (`ports-leaf-card`, `ports-server-conflict-card`,
`ports-denied-peer-card`), and the same faceplate in the rack panel
(`rack-leaf-faceplate`, `rack-leaf-card`) at desktop light, desktop dark and
the opt-in `phone-dark` viewport.

In the diagram and list, within each type group, ports are grouped by chassis face (Front, Rear, Face
unknown) when the Device's DeviceType records a face for at least one of them;
otherwise the group stays flat. A port's chassis face comes from the DeviceType
template with the same id and is unknown without a readable catalog. When the
Device's mounted face is known, each port also shows its derived rack face
(for example "Chassis front · rack rear" on a reversed switch). A Cable whose
two ends land on opposite rack faces gets a soft "Check" note in the row and
in the port's card. The note does not change the record state.

Not shown, because the model does not record them: link speed, Cable label,
colour or length, x/y port positions and port layout, live or observed link
state, health, and any verified state. Shared-failure or redundancy analysis
is not attempted.

## Rack page (implemented)

The Rack page centres the elevation and moves everything else into tabs, so
the page answers "what is in this rack" without scrolling past related
records. `ui/provider/src/components/rack-detail.tsx` wraps the existing
elevation (`RackOverview` in `inventory-location.tsx`) rather than redrawing
it.

- **Header.** The title, then one line: the facility, the rack height and
  issue badges ("2 placements disagree", "1 without a position"). A count
  strip follows (Equipment, Cables, Power feeds); each count links to its tab
  and a denied collection reads "unavailable", never 0. The free-space note
  stays visible as one small line. The record name, namespace, UID and
  display-name editing move into a collapsed **About this record** section.
- **Tabs.** Elevation (default), Equipment, Cables, Power and History. The tab
  is the `tab` query key beside `kind`, `record` and `namespace`; Back and
  shared links restore it. An unknown value falls back to the elevation.
- **Selection.** Selecting equipment in the elevation sets `selectedKind`
  (`Device` or `DeviceClaim`) and `selected` (the record name) and opens a
  side panel; below 600 px it is a bottom sheet (`role="dialog"`, non-modal,
  focus on Close). The panel leads with the device's faceplate, then model,
  units, rack face, depth, lifecycle, a count of recorded cable states, and
  links to the Device page and installation plan. Arrow keys, Home and End
  move between elevation entries; Enter selects; Escape closes the panel and
  returns focus to the entry. A selection that is not among the readable
  records for this rack says so without repeating the name.
- **Faceplate.** `DeviceFaceplate` in `device-faceplate.tsx` draws the same
  port faceplate as the Device connection view (see "Port tiles" above), so
  the two cannot drift apart. The rack panel also draws a recorded face that
  has no components ("No component records this face."), and keeps
  catalog-only clusters visible. When Cables are denied it still draws the
  recorded components, each named "Cable details unavailable, connections
  unknown", never "no cable recorded".
- **Equipment tab.** One condensed table per section (positioned,
  unpositioned, non-racked mounts) with Device, U / face / depth (depth with
  its source), Lifecycle and Claim / registration columns. Facility and rack are not repeated per
  row. Claim-derived fallback rows stay labelled.
- **Cables tab.** Cables grouped by the device in this rack they terminate on,
  with counts, port, peer, record state and the Cable's display name. Record
  names are only in each group's collapsed "Record names" list.
- **Power tab.** Power feeds that name the rack, with each termination's
  recorded cable or "no cable recorded", and PDUs (Devices with power
  outlets) with the outlets that have a cable recorded.
- **History tab.** Record history and verification, unchanged in meaning.
- **Issue banners.** Above the elevation, one short banner per cluster of
  same-face overlapping placements, from the elevation's own face-aware
  occupancy (flagged, not blocked), and one for equipment that
  names the rack without a usable unit position. Each names the affected
  equipment as links that select it.

The elevation keeps device bars this round. `RackSlotDevice`
(`rack-slot-device.tsx`) is the single component that renders equipment in a
unit span, so mini faceplates can replace the bars once DeviceTypes record a
layout. The elevation toolbar (`RackOverview`'s `toolbar` slot, beside the
face switch) and the stage's `data-view="2d"` leave room for a later 2D/3D
toggle; no 3D view exists. `condensedName()` in `lib/display-names.ts`
shortens component names for tiles (Ethernet 0 → eth0, Outlet 3 → O3); the
full name stays in the tile's accessible name and card.

## Deliberate gaps and decisions

| Interaction | Remaining boundary |
| --- | --- |
| Other-writer edits | Reported-location correction is available to managers for operator-managed Devices; legacy Flux remains outside the UI. Other edit forms retain their existing restrictions |
| Rack position entry, move/assign equipment | Reported-location correction supports unknown or explicit geometry; physical moves, installation/binding changes and handovers still require separate workflows |
| PowerFeed creation; component or endpoint edits | API kinds exist, but these forms are not implemented. Existing records remain browsable; no dead action buttons |
| Online/active/connected badges, occupancy/free units, power utilization | No verified evidence projection; never infer health/readiness from inventory |
| Device role/primary IP, cages/maps/address, cable length/color | No matching authoritative fields in the present contract |
| Hosted infrastructure, circuit/service CRUD, provisioning | Out of scope; networking, IPAM, routing and billing remain with their owners |
| Cross-object consistency and endpoint exclusivity | Prerequisite re-reads are not backend transactions/reservations; operators must review duplicates and changed references |
| Authenticated host acceptance | Still unverified. Supplied host pin `b1489125fe5b77ea6fc379ad378c8e1951174de5` uses Router `^8.3.1`; local harness uses Router 7.18.2. No live login was manipulated |

## Validation and review

Run from the repository:

```sh
pnpm --dir ui/provider test
pnpm --dir ui/provider typecheck
pnpm --dir ui/provider build
go test ./internal/api/inventory/...
git diff --check
```

Prior UI phase results: 278 tests in 20 files, TypeScript check, production build,
and the Go inventory package tests pass. Build warnings concern the existing
transitive undici share and Zod annotation handling; they do not fail the build.
Documentation relative links and whitespace were checked. At that phase, the 68
independent tests remained byte-for-byte identical to the supplied review suite.

The independent suite from the previously reviewed `239dd312` head was
originally copied unchanged into `exact-head-independent.test.tsx` (68 cases), alongside
the original adversarial regressions. New tests cover geometry, unknown height,
non-unit mounts, real filters, hardware payloads, typed references, permission
denial, input retention, unavailable prerequisites, and unmount during delayed
endpoint validation. These are regression evidence, not a new independent review.

Local browser checks use the existing standalone harness with synthetic fixtures
or intercepted `/api/internal` envelopes only. Desktop and 390×844 phone checks
cover compact lists, rack faces/equipment, location and cable links, device and
cable forms, reader/manager controls, denial after submit, partial/no-read states,
session expiry, backend failure, and removed stale identities after observed
denial. Synthetic successful submissions are not live inventory mutations.
Screenshots are local browser artifacts; the original Pencil exports are separate.

### Local visual evidence

Screenshots below are in `/Users/scotwells/.t3/userdata/browser-artifacts/`
(synthetic data only, not committed application assets):

| Check | Screenshot file |
| --- | --- |
| Before: generic directory | `browser-screenshot-localhost-mwpst023-01d6f601.png` |
| After: compact desktop devices | `browser-screenshot-localhost-mwptcps2-5bac903c.png` |
| After: desktop rack and equipment | `browser-screenshot-127-0-0-1-mwpvdwjl-dfeb7d93.png` |
| After: mobile devices | `browser-screenshot-127-0-0-1-mwpv9qeo-18b6794c.png` |
| After: mobile device form | `browser-screenshot-127-0-0-1-mwpvb96u-707e61d0.png` |
| After: mobile rack faces | `browser-screenshot-127-0-0-1-mwpvcnqw-6a677229.png` |
| Detail read revocation, desktop | `browser-screenshot-127-0-0-1-mwpvf3ib-0af0ac53.png` |
| Detail read revocation, mobile | `browser-screenshot-127-0-0-1-mwpvfer9-fdda9c70.png` |

Keyboard search and Tab navigation, Enter on the rear-face control, facility →
rack and cable → device links were checked. Explicit detail refresh after a
synthetic 403 removed the restricted device name while retaining other categories.
Mobile lists, forms and rack views had no page-wide horizontal overflow. These
checks do not replace screen-reader testing or authenticated host acceptance.

The stable-controls follow-up requires a **new independent adversarial review**
before staging merge or deployment. Its implementation authorization includes a
signed commit and a PR against `feat/manual-inventory-records`, not a merge,
deployment or live mutation. Authenticated staging integration, including host
principal changes and proxy enforcement, remains a release acceptance check.

## Operational Site overview (implemented)

Site detail now leads with linked Facilities, equipment recorded at those premises,
and installation plans. Facility links use `Site.spec.facilityRefs` in the same
namespace, never display names or provider codes. Shared/duplicate Facility links
and device/rack records are deduplicated; equipment is **at linked facilities**,
not assigned to the Site. Counts grant no eligibility, capacity or readiness.

Devices follow the existing reported-placement projection, with explicitly labeled
claim-derived fallback only when no report exists. Rack-only references resolve
through readable, non-deleting Rack records. Every matching device registration
is counted; none is selected as the active device. Plans use their own typed
placement and `lifecycle: Installation`; legacy claims remain available through
Claims & registrations. The implemented API has no execution-status fields, so
plans say recorded and execution status not recorded, never ready or complete.
Facility premises identifiers and an existing geographic Location reference are
shown when recorded. Operator, address and region are absent from the implemented
Facility schema and are not invented from provider identifiers or display names.

Missing/deleting Facility references remain unresolved. Denied collections redact
names and links and provide access guidance; unavailable collections offer refresh.
Readable subsets remain useful with incomplete counts where dependencies are
unavailable or unresolved. Zero matching records describes the loaded snapshot,
not absence of physical equipment. Unmapped Sites ask for an operator to record
Facility references. Record name, UID, internal code and name-reuse semantics move
to collapsed Technical details; the redundant default namespace label and empty
Related records section are omitted on Site detail.

This is presentation over the existing authorized default-only inventory snapshot.
No new queries, forms, mutations, cache policy or session/operation guards are
introduced. Same-principal cache reuse, observed denial redaction, actual session
fencing, inert visible blur/focus and exact mutation preconditions are unchanged.

Validation uses provider component/regression tests, typecheck and production build,
plus documentation link and whitespace checks. Tests cover Site → Facility → Rack
→ claim/device paths, direct reports, legacy claims, duplicates/shared premises,
missing/deleting/foreign references, denied collections and partial reads. No
browser or authenticated host acceptance, live inventory reads/writes, deployment,
or operational readiness is claimed. Responsive behavior uses the existing scoped
layout and scrollable Datum tables; visual layout remains unverified in a browser.

Local validation on 2026-11-22: all 41 provider test files / 678 tests passed;
provider `pnpm typecheck`, `pnpm build`, `bash hack/validate-doc-links.sh` and
`git diff --check` passed. Consumer/shared API code is unchanged; consumer checks
were not required for this provider-only presentation change.
