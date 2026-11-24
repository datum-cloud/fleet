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
| Installation plans | None | The directory is **Installation plans**, named once in the breadcrumb: installation plans and the devices registered against them. Plans do not allocate capacity |
| Hardware catalog | Manufacturers, Device types | Reference descriptions/templates, not installed assets or available capacity |

- Uses the installed Datum `Tabs`, `TabsList variant="line"`, and
  `TabsLinkTrigger` with React Router links, matching the pinned Compute and
  cloud-portal sources. No custom underline imitation, sidebar, or category menu.
  Primary tabs remain mounted; secondary tabs depend only on the selected group,
  never access results. A group with one kind (Installation plans) has no
  second tab row. At phone width the group tabs show short labels (Plans,
  Catalog, and Sites for Operational sites) so all four fit on one row; the
  accessible name stays the full title.
- Kind tabs carry the directory's count ("Devices 18"). A filtered directory
  shows "shown/total" on its own tab, and a denied collection reads "?". The
  count is the tab's accessible description; its name stays the title.
  `PhysicalInventory` reports counts through `InventoryCountsProvider` only
  after the session boundary resolves, so the public shell, pending and
  unavailable states render no number.
- Equipment / Devices is the default. Group links select the first kind unless
  already in that group. `?kind=...` and record/namespace bookmarks and the
  child create routes are the only inventory URLs. Directories have no
  breadcrumb, visible title or description: the tabs say where the reader is,
  and an `sr-only` `h1` names the page. Detail and task breadcrumbs
  include group and category, for example Equipment / Devices / record name.
  Protected names appear only after successful access checks, not from URL text.
- Search, filters, Refresh and Create are page controls, not navigation. They
  share one toolbar row above the table: search, the facility and model
  filters, **Clear filters** (only while a filter is set), an icon-only
  Refresh named "Refresh inventory", then Create.
  Same-category links retain `q`, `location`, and `model`; switching categories
  starts an unfiltered directory, and browser Back/Forward restores prior URLs.
  Clicking the already-current tab is a no-op, not an implicit refresh.
- Same-principal presentation navigation reuses authorized default-namespace
  collection data, including changes to the three known client-side filters
  and the detail pages' `tab`, `view`, `selectedKind` and `selected` parameters.
  The narrow presentation allowlist does not include unknown query parameters,
  route changes, form submissions, or explicit revalidation. Host loaders still
  verify navigations. During a presentation navigation the page already on
  screen stays (the location has not changed yet, so nothing new is shown) and
  writes are withheld until the host check finishes; other navigations show the
  pending shell. Changed principal, logout, real denial, hidden/pagehide, and
  unknown-scope boundaries still fence/clear private data. Operation
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
   badges. A Device adds one strip of identity facts under it. A control
   that is unavailable explains why in its tooltip and accessible
   description, not in a sentence beside it. Page actions, including
   **Edit display name**, sit in the header beside **Refresh inventory**.
   The generated resource name and creation date close every record page
   as one quiet **Record** line ("Record example-rack-a01 · created
   2026-10-01"), plus any fact the page shows nowhere else. The namespace
   (always `default`), the UID and a per-record verification row are not
   shown. No page has a History tab; it returns when an activity timeline
   exists. The breadcrumb is the way back; there is no separate back link.
2. **One primary view per kind**, above the fold:

   | Kind | Primary view |
   | --- | --- |
   | Device | The device picture, then the dense component table (see [Device page](#device-page-picture-and-component-table)) |
   | Cable | A one-lane Cable is one line, "Compute a01-01 · eth0 ⟷ Leaf a01 · swp1". More lanes are a table, one line per lane, with an end device (and port) every lane shares named once in its column head. An end another Cable also names is red, naming and linking that Cable, with the reason as tooltip and accessible text |
   | PowerFeed | The power path: upstream, feed rating and capacity, outlet tiles for the feed and each PDU, then device PSUs (see [Power path](#power-path-and-telemetry-implemented-telemetry-mocked)) |
   | Facility | None: Racks, Without a rack, Power feeds and Cables tabs (see [Locations and catalog pages](#locations-and-catalog-pages-implemented)) |
   | Rack | Its elevation |
   | Site | None: Facilities (unless the header names its one facility), Racks, Devices and Installation plans tabs |
   | DeviceType | The faceplate drawn from the model's layout, then a dense component table |
   | Manufacturer | None: Device types and Devices tabs |
   | Claim | Placement, expected hardware (linked to its DeviceType when exactly one readable model matches the manufacturer and model key) and registered devices. A registered device that reports another model is red, with the reason as tooltip and accessible text. No lifecycle badge or registration count; the Planned connections tab appears only when the plan names Cables |

3. **Secondary detail goes in tabs held in the URL** (`?tab=`), for example
   a Device's Location and Hardware & management. Tabs hold
   only what the primary view does not already show. A tab label carries its count, or `?` when the collection is
   unreadable. A count that needs checking (`17+` when incomplete, `?` with a
   reason) is amber, with the reason as its tooltip and in the tab's
   accessible name, instead of a sentence on the page. Every panel stays mounted and inactive panels are `hidden`, so
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
   of the page that link to the problem (`IssueBanners`), unless the primary
   view already shows them: the Device page leaves Cable disagreements to its
   picture, its table rows and the count line, where "n disagree" is red. Empty and "not
   recorded" fields are hidden by default (`Facts`). Keep an absent value
   visible only when the absence itself matters: verification is unknown,
   power budget is unknown, and "No cable recorded" stays on outlets because
   it means unknown, not free.
6. **State, not caveats.** Pages carry no legend lines or caveat prose
   ("not verified", "not free", "does not prove…"). States are shown
   visually and named in words where they apply: "No cable recorded", red
   and amber edges, "?" counts, with the reason as the tooltip and
   accessible name. Nothing may look free, available or verified, and when
   a collection is unreadable, counts and states read as unknown, never
   zero or empty.
7. **List pages are search-first and dense.** Search and filters sit at the
   top and keep their URL parameters (`q`, `location`, `model`); search
   matches the text the row shows. Directory tables use `inventory-dense-table`:
   one line per row, long cells cut with an ellipsis and the full text in the
   tooltip. Each kind has its own columns (`inventory-directory-rows.ts`):

   | Kind | Columns |
   | --- | --- |
   | Devices | Name, Rack, U, Face, Model, Serial, Asset tag, Power (feeds the PSUs reach) |
   | Cables | Name, Medium, A end, B end, Lanes |
   | Power feeds | Name, Location, Type, Voltage, Breaker, Capacity, Load |
   | Operational sites | Name, Code, Facilities, Racks, Devices |
   | Facilities | Name, Code, Site, Racks, Devices |
   | Racks | Name, Height, Devices, Used ("12/42U"), Power, Feeds |
   | Installation plans | Name, Rack, U, Face, Model, Device, State |
   | Manufacturers | Name, Aliases, Device types, Devices |
   | Device types | Name, Manufacturer, Model key, Height, Depth, Ports, Devices |

   Facility appears before Rack only when more than one facility exists.
   Serial, Asset tag, Power, Lanes and Aliases appear only when some row has
   a value. Devices and plans sort by location (facility, rack, top of rack
   first), with unplaced devices last; other kinds sort by name. A cable end
   is "Leaf a01 · swp1", and a breakout end that reaches several devices is
   "4 devices · eth0" with each lane in the tooltip. Related records show
   their display names, never record names, except to tell apart catalog
   records whose display names collide. A non-Active device has its
   lifecycle as a muted word after the name.

   State is colour, not words in cells. A row whose records disagree
   (multiple registrations, two cables on one port, overlapping rack
   placements) has a red edge; a missing or unreadable peer, an unknown
   position, a planned-only location or a placement that differs from its plan
   has an amber edge. The reason is the row tooltip and part of the name
   cell's accessible text. Unknown values read "?", with the reason in the
   tooltip and accessible text; Used units never show free space and read "?"
   when any placement lacks a position. Clicking a row opens the record; the
   name stays the keyboard link.
8. **Mobile.** At 390 px the header, primary view and tabs must work without
   horizontal page scroll. `pnpm screenshots` fails a page that overflows.

Write controls stay available but quiet: display-name editing and Refresh sit
in the header actions, and reported-location correction is on the Device's
Location tab. Their dialogs, task pages, dirty-dismiss confirmation and
unknown-outcome handling are unchanged.

The building blocks are in `ui/provider/src/components/inventory-record-layout.tsx`
(`RecordSummary`, `IssueBanners`, `RecordTabs`, `Facts`, `FactSection`,
`RecordFooter`), and the per-kind views are in `inventory-record-detail.tsx`.
`inventory-record-layout.test.tsx` covers the disclosure, tab URL state, issue
banners, hidden empty fields, list columns, and the Device page's header
strip, picture, table, one count line and tab contents.

## Form controls (implemented)

Every control in the staff and consumer plugins comes from datum-ui:
Select, Input, Button, Table and the `picker` DateTimePicker. Neither plugin
renders a native `<select>`, `<input>`, `<textarea>` or `<button>`, apart from
the exemptions below.

- **Selects.** `FieldSelect` (`ui/provider/src/components/field-select.tsx`)
  wraps the datum-ui Select and takes a plain string value, where `''` means
  none. A field that can be cleared (the directory filters, Catalog model,
  a lane's device, and Facility, Rack, Face and Depth in the location
  correction) lists its clearing choice first: "All facilities", "All
  models", "All media", "No catalog assignment", "Device or feed",
  "Unknown / not recorded" or "Unknown". A required field (Location,
  Installation plan, Cable medium, a lane's port, a Rack's Facility and the
  capacity registration's Device) shows its prompt until a choice is made,
  and the prompt is not a choice. Groups (facilities and racks, racks and
  power feeds) keep their headings. A marked option keeps its amber text and
  its reason as its tooltip ("· has device", "· cabled"). An unavailable
  option, such as an ineligible Device or "Selected catalog unavailable", is
  listed but cannot be chosen. The open list may be wider than a narrow
  trigger, up to the screen width less 32 px.
- **Keyboard.** Enter, Space or an arrow key opens a Select. Arrow keys move
  through the options, typing jumps to a match, Enter chooses and Escape
  closes without choosing. A disabled Select does not open.
- **Directory filters.** The facility and model (or medium) filters still
  write `location` and `model` to the URL. Choosing the "All" option removes
  the parameter.
- **Validation.** Inside a form, each Select keeps a hidden form select, so
  required Selects and lane ports (with their `data-problem` rule) take part
  in validation. `InventoryForm` reports the problem against the visible
  trigger: it is marked invalid, described by the summary line, and focused
  when that line is clicked. Choosing a value clears the mark.
- **Observed at.** The location correction picks the observation time with
  the datum-ui date-time picker: a calendar, a typed `HH:mm` time, and the
  browser time zone shown. It opens set to the time the form was opened,
  cannot be cleared, and cannot pick a future date. The save still rejects
  a time later than now.
- **Tables.** The component and lane editors are datum-ui Tables.
- **Exemptions.** Port tiles (`PortTileButton` in `device-faceplate.tsx`)
  stay native buttons. They are drawn faceplate tiles, and their look is the
  drawing. Rack slot links are links, not form controls.
- **Guard.** `src/test/native-controls.test.ts` in each plugin fails when a
  component adds one of those native elements outside its allowlist.
- **Tests.** `ui/provider/src/test/field-select.ts` opens a Select from the
  keyboard and picks an option by value (`chooseValue`), lists the options a
  reader would see (`selectOptions`) and reads the chosen value
  (`selectedValue`).

## Default-only directory and stable controls

Implemented on top of `baa210318fe17927f447a8e9e9e8053a23f6b173` on
`feat/manual-inventory-records`. This update supersedes the namespace-choice and
access-dependent navigation descriptions in the historical phase notes below.

- All nine inventory collections, including Sites, Manufacturers, and DeviceTypes,
  are read from `/namespaces/default/`, including every continuation page.
  Collection responses and rendered inventory are filtered to explicit `default`
  metadata; foreign and missing namespaces are not silently relabeled.
- The directory and query-string details cannot select
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
- Pending, allowed, and denied directory states share the heading and the
  toolbar's search, filter, action and status slots. Actions remain mounted
  and disabled until usable; successful authorization adds no “access checked”
  clutter. Missing management access and ownership restrictions are the
  disabled button's tooltip and accessible description, not a sentence.
  Explanations for unavailable references, expired sessions, and uncertain
  saves remain visible. Long failure explanations
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
selection, paginated default requests, and foreign reference and
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

- Details and task pages use a local composition of Datum Breadcrumb and
  PageTitle, with explicit accessible headings and stable action/status slots.
  Directories use the route tabs and the toolbar instead.
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
  register the child routes. There is no per-claim route. Unsupported
  task kinds and foreign namespace URLs fail closed before reads or writes.
- `q`, `location`, and `model` directory filters are URL-backed. Task/back links
  retain only those filters and the relevant kind, not arbitrary redirect targets
  or foreign scope. Same-category record/back links preserve filters. Back/forward
  and direct task URLs work without storing drafts in browser storage or URLs.
  Search applies as the reader types, 200 ms after the last keystroke, or at
  once on Enter. The field keeps its own draft, so URL updates and host loader
  revalidation never interrupt typing or weaken session fences. The no-match
  view offers **Clear filters**, which clears `q`, `location` and `model`.
- Dialogs provide focus trapping/restoration through Datum's Radix-backed primitive,
  viewport-bounded scrolling, and explicit Cancel / Create record / Save changes.
  Native field errors have an accessible summary, field descriptions, and focus
  links; failed saves focus a summary while sanitized status stays visible.
- Forms carry no recordkeeping disclaimers. Why a create or edit trigger is
  unavailable ("Needs inventory management access", "Checking access…",
  "References unreadable", "Session expired — sign in again", "Managed
  elsewhere — not editable here", "Refresh to retry") is the trigger's
  tooltip and accessible description. The status line holds save results
  only and takes no space when empty; a create page, which has no trigger,
  shows the reason as its one line.
- Display name comes first; the record name follows it as a slug until the
  operator edits it. Hints are one short line under the field they protect:
  "Can't be changed later" (installation plan, catalog model, plan
  location), "Real serial or system UUID required. Not an asset tag or
  provider ID", "Physical ports and parts only — no interfaces or IPs" and
  "No credentials or secrets" (evidence). An unreadable reference collection
  is an amber "list incomplete" line, never an empty list.
- Rules the browser cannot check are checked before any request and reported
  in the error summary on their field: a serial or a system UUID, a
  lowercase system UUID, unique component and lane IDs, and distinct cable
  ends. A rule the form checks while saving (such as a changed installation
  plan) shows its own message; only an API rejection reads as generic. Field
  patterns compile under the browser's unicode-sets mode.
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
  same face are coloured red and labelled "records disagree" in the tooltip
  and accessible name; they are never blocked. The logic is in `ui/provider/src/lib/rack-faces.ts` and
  `device-depth.ts`.
  The Rack page layout is described under [Rack page](#rack-page-implemented).
- Create Facility/Rack facts; rack height is optional. Create an Installation
  plan at a known Facility or Rack (grouped Facilities / Racks) with unknown
  unit position, then separately register a Device with a real serial or
  system UUID and typed components. The Device form starts with the
  installation plan; a plan that already has a device reads "· has device" in
  amber. Selecting a plan or a catalog model never fills observed identity or
  components. Components are one dense table row each (ID, Name, Type,
  Connector, Positions).
  The form does not offer the `identity.unidentified` marker; record such a
  Device through the API. Directory and detail views show the marker and its
  reason as "No hardware identity".
  This is not an atomic multi-record workflow or an adoption operation.
- Create Cable records with a medium and named lanes using existing readable
  Device/PowerFeed components with recorded positions. Each lane is one row
  (ID, End A, End B); an end is picked device or feed first (grouped by
  readable rack, then power feeds), then port ("swp49/1"). A port that a
  recorded Cable already names is amber with "· cabled" and the Cable's name
  as its tooltip; the same port twice is red. New lanes default to `lane-N`.
  No guessed ports, cable lengths or live connectivity. Reference owner UID/resourceVersion is retained
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
adds create/update/patch for six kinds, project-wide. The UI uses
SelfSubjectAccessReviews and the signed-in principal's API request, never group
names or staff membership. The answer that enables a Create or Edit control is
checked once per kind and verb, shared by every record of that kind, and reused
for ten minutes: management access rarely changes. A new principal gets a new
query cache, and **Refresh inventory** checks again at once. Every write is
still checked at submit time and authorized by the server. Role/binding deployment and membership are separate
work; nothing here asserts their live state. There are no delete/status grants.

Inventory is direct API/operator-managed, not GitOps-only. Flux- or Argo-managed
records keep their writer until an explicit safe transition. This UI's
deliberately narrow display-name edit policy still requires exclusively
`fleet-staff-inventory-ui` managed fields, with no deletion/other-owner
markers. The separate reported-location editor permits operator-managed
Devices under normal manager authorization, excluding records with GitOps
markers from its UI. These markers are not server authorization or owner isolation. Location
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

Device detail's primary view is the device picture and the component table
under it (see [Device page](#device-page-picture-and-component-table)). It
is the one place the page lists the Device's components and what each is
cabled to. It joins the Device's typed
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
| Records disagree | ! | Two or more lanes name the position or the far port, or a Cable names a component or position that this Device or the peer does not record. Every claiming Cable is shown; the reason ("2 Cable lanes name this position.") is said once. |
| Peer unavailable | ? | The peer collection is denied, or the named peer record is not in the namespace. A denied peer shows no name, component, position or rating. A missing peer shows only what the Cable records, marked "record not found". |
| Catalog only | ◇ | A DeviceType template whose id (matched by convention) is not recorded on the Device. |

A breakout lane maps to its own component position. PowerFeed peers show
their recorded supply rating. When a peer Device records an internal mapping
(FrontPort to RearPort, or PowerOutlet to PowerInlet), the link traces onward
for up to four hops, stopping at the first hop that is unrecorded, disputed
or unreadable. A Device's own internal mapping is shown on its local port.
When Cables cannot be read, the view says that connections are unknown and
classifies nothing: the picture draws every recorded port as unknown, and
the table still lists the components, without the State and Connected to
columns.

### Device page: picture and component table

The Device page shows two things, and nothing else competes with them: a
picture of the device and a dense table of its components. Top to bottom:

1. **Header.** The display name, the location line ("Facility · Rack · U2
   front", prefixed "Planned ·" only when it comes from the installation
   plan), then one facts strip: the model link (its label names the
   manufacturer, so it is not repeated) · Serial · Asset tag · Draw. Empty
   facts are hidden, including the draw when nothing reports one (each
   PSU's card still says "Not reported"). The draw is short ("358 W", "≥"
   when only some PSUs report, ◷ when stale); its age and "observed, not
   verified" are the tooltip and accessible text. A Device whose model is
   not from a readable catalog shows the recorded model; the reason is its
   tooltip and the catalog banner. **Edit display name** is
   disabled for records the portal does not own; the reason ("Managed
   elsewhere — not editable here") is the control's tooltip and
   accessible description, not a sentence on the page. Issue banners follow,
   one compact line each.
2. **Picture.** Always shown, never toggled. The drawn faceplate when the
   DeviceType records a layout (see [Faceplate drawing](#faceplate-drawing)),
   otherwise the grouped port tiles captioned "Layout not recorded". Faces
   carry short labels, "Front" and "Rear", with the rack face added only
   when the Device is mounted reversed ("Front · rack rear"); the full title
   stays each face's accessible name. Drawn faces are scaled whole to their
   column, between 1.15 px/mm and the 1.9 px/mm drawing scale (0.7 px/mm
   below 600 px wide), so front and rear sit side by side when both fit and
   stack otherwise; a face too wide for its frame scrolls inside it. The
   picture has no outer frame (each chassis is its own) and no "Not on the
   faceplate" tray: the table lists those components. Clicking, tapping
   or Enter on a port opens the shared card.
3. **Component table,** directly under the picture. One row per component,
   about 30 px high, 13 px type, no wrapping (a breakout listing several
   peers is the exception) and tabular figures. Columns:
   - **Component**: the name. The component id is its tooltip. A lane end
     that matches no recorded position adds "· position 0" or "· position
     not recorded".
   - **Type**: short words ("Network", "Power inlet", "Module"), with the
     connector and any "4 positions" muted inline.
   - **Face**: "Front", "Rear" or "Face unknown", by the same rule as the
     picture's labels.
   - **State**: the glyph and words of the record states below, never
     colour alone. Modules and items that no Cable names read "■ Component
     recorded". The reason for a disagreement is the cell's tooltip and
     accessible text; a soft opposite-face check shows a short "· Check"
     with its sentence the same way.
   - **Connected to**: the peer link, then "via" and the Cable as a small
     secondary link, with the Cable's medium and lane as its tooltip. A
     peer's rating or "You cannot read this peer" is the tooltip and
     accessible text. A recorded path beyond the peer reads "+1 hop", with
     the full trace in words ("via internal path to Rear 1, then Cat6 patch
     rear 1 to Management a01 · ge1") as its tooltip and accessible text; the
     port card lists the same path. No cable recorded reads "Unknown",
     never an empty cell. A breakout lists each cabled position and "No
     cable recorded at position 1, 3, 4".
   - **Serial** and **Asset tag** appear only when some component records
     one.

   Rows follow the picture's groups (power, network, console, pass-through,
   modules and items), then components that only a Cable names, then Cable
   lane ends `deviceConnections()` cannot place (see below). With Cables
   denied the table has Component, Type, Face and any Serial and Asset tag
   columns.

   The table's header row holds the one count line ("9 ports · 4 recorded ·
   1 disagree · 4 catalog only"), which also describes the table for
   assistive technology. With Cables denied it reads "5 components · Cable
   records are unavailable to you." There is no legend: each state is its
   glyph and words in place, and "· catalog unavailable" joins the count
   line when the catalog cannot be read.
4. **Tabs:** Location and Hardware & management, below, then the
   **Record** section.

**MAC addresses.** A recorded network interface whose name matches a
component's name or id puts its MAC address in a **MAC address** column,
shown only when some row has one (`interfaceAddresses()` in
`lib/device-connections.ts`). Interfaces that match no component are listed
in Hardware & management. Once BMC discovery has read the host NICs, a
recorded MAC that no host NIC reports is amber, with "No host NIC reports
this MAC address" as its tooltip and accessible text.

**Highlight.** Hovering or focusing a row outlines that component in the
picture, and hovering or focusing a picture port (or hovering a drawn
module) outlines its row. Both use a `--ring` outline, not colour alone,
from one piece of state in `DeviceConnections` (`ComponentHighlight` in
`device-faceplate.tsx`). Touch does not highlight; a tap opens the card.

The page has one view: the picture and the table. A `view` value in a link,
such as `diagram`, `table` or `picture`, is ignored and shows the default
page.

`deviceConnections()` drops a Cable lane end that names a recorded component
at a position below 1, or with no position (an omitted position decodes as
0). `unmatchedLinks()` returns those ends with their peer resolved under the
same access rules, and the table lists them last as "Records disagree" rows
with the reason. So every Cable that names the Device appears in the table.

The tabs carry no component counts or badges.

The Device's tabs are **Location** and **Hardware & management**.
Location states each fact once: **Placement** holds only
the basis, units, height and face ("Reported · U2 · 1U · front"), because
the header names the facility and rack and depth has its own fact; the
**Placement report** gives the revision, observation time and "not
physically verified", with the reason and evidence on a small second line;
**Installation plan** links the claim the hardware was placed against;
**Depth for rack conflicts** is unchanged, followed by the catalog size and
any recorded size. **Correct location** (or **Add reported location**) sits
on the Placement row and stays behind its permission check, with the reason
as its tooltip. Its dialog is two columns: Facility | Rack, Position |
Height, Face | Depth, then Mount, then Observed at | Evidence and the
reason. Observed at is a date-time picker (see [Form controls](#form-controls-implemented)). Choosing a rack sets its facility; changing the facility clears a
rack outside it. Hardware & management groups its
facts into titled sections of label/value rows: **Hardware** (the reported
model when a catalog model is assigned, system UUID, MAC addresses without
an interface, and a small table of interfaces that match no component) and
**Management** (management endpoint and provider host), plus small tables
for **Internal paths** and **Service references** when recorded. Empty
facts and sections are hidden. The model is stated once, in the header
strip.

### Device page: BMC discovery status

BMC discovery is the only writer of `Device.status` (see
[BMC discovery](../operations/bmc-discovery.md)). The UI reads it from the
same Device list as spec; it has no other request. `lib/device-discovery.ts`
derives what the page shows and mirrors the comparisons in
`internal/bmcdiscovery/status.go`; `components/device-discovery.tsx` draws
it. Red is a disagreement or failure, amber needs checking, and OK stays
plain: there is no green. Every reason is a tooltip and accessible text.

- **Header chip.** One chip ends the identity strip once status exists:
  "BMC ✓" when all five conditions are True; else the first False condition
  in discovery order, red ("BMC unreachable", "BMC key not approved", "BMC
  key changed", "BMC auth failed", "BMC not read", "Identity mismatch",
  "BMC key shared"); else "BMC ?" in amber.
- **Hardware: expected next to observed.** When discovery has read a
  System, a Field | Expected | Observed table replaces the reported model
  and system UUID facts: Serial, Model, Manufacturer, System UUID, SKU and
  Power (Redfish `powerState` as a short word). Expected is `spec.identity`
  and is blank where spec does not record the field. The compared System is
  the one reporting the spec system UUID, else the spec serial (as serial
  number or SKU), else the only System; with several candidates the table is
  hidden and the IdentityMatched chip carries the reason. An observed value
  is red when IdentityMatched is False with SerialMismatch, ModelMismatch or
  SystemUUIDMismatch for that field (the condition message is the tooltip),
  or when it differs by discovery's rule (a vendor prefix on the model is
  allowed). Manufacturer is shown but, as in discovery, not compared. The
  page never calls identity verified.
- **Observation from another endpoint.** When the retained inventory was
  read from an endpoint other than `spec.bmc.endpoint` (or spec records no
  endpoint), discovery reports
  IdentityMatched Unknown with StaleObservation, so the page compares
  nothing: observed values are amber with "Read from … ; spec.bmc.endpoint
  is …" as the tooltip, no spec MAC is marked unobserved, and a TLS key
  read from another endpoint is amber before anyone approves it.
- **Host NICs.** `systems[].networkInterfaces` of the compared System, one
  row each: NIC, MAC address, Link, and Speed when reported. A link that is
  not up (LinkDown, NoLink or not reported) is amber with the raw
  `linkStatus` as its tooltip. An observed MAC that `spec.interfaces`
  records is followed by that interface's name; matching is by MAC address
  only, never by name. When spec interfaces match no component, their table
  sits beside the host NICs, with a spec MAC no NIC reports in amber.
  Interfaces that match a component keep their MAC in the component table,
  so no MAC is listed twice.
- **Management.** The five conditions as one line of chips (Reachable, Auth,
  Inventory, Identity, Key unique), each with type, status, reason and
  message as its tooltip: red ✕ for False, amber ? for Unknown or absent,
  plain for True. Then **Last attempt** (relative, absolute time as the
  tooltip; the attempt endpoint is added in amber only when it differs from
  `spec.bmc.endpoint`), **TLS key** (the SHA-256 shortened, full value as
  the tooltip, with Copy), **Key expiry** (amber within 30 days, red once
  expired), and **Key approval**: Approved when Authenticated is True, "Not
  approved" in amber for PinNotApproved, red for PinMismatch, and hidden for
  other reasons. Then a BMC table (Manager, Model, Firmware, and the service
  root's Redfish version, Vendor and Product; empty columns hidden) and the
  manager NICs in the same NIC table form.
- **Before discovery.** A Device with a BMC endpoint and no status adds one
  muted "Not discovered" fact under Management and nothing else.
- **Unreadable Devices.** Status arrives only inside a readable Device. A
  denied or redacted Device collection shows the access state, never a
  Device or its status.

The preview fixtures carry discovery status on three servers: a01-01
(matched, powered on, NICs up, key approved), a01-02 (serial mismatch, a
spec MAC not observed, an interface outside the component list) and a01-05
(key not approved, certificate expiring in 12 days, one host NIC down).
`device-discovery.test.ts` and `device-discovery.test.tsx` cover the
comparison and mismatch marking, condition chips and tooltips,
PinNotApproved, expiry tones, link states, MAC matching without name
mapping, the no-status case and denied Devices.

`device-connections.test.tsx` covers the picture with no toggle, the table
columns and rows (unmatched lanes included), conditional serial and asset
columns, the single count line, the absence of a legend and a diagram
(every `view` value is the default page), the highlight in both directions, denied peers and denied
Cables. `inventory-record-layout.test.tsx` covers the drawn and tiled
pictures on the fixture pages, the header strip and the Edit button's
description.

### Names

Views never show a generated resource name (`netbox-ca213d185ea9153b853f295e`,
or any name with a run of twelve or more hex digits, or a UID) by default.
`humanName()` in `ui/provider/src/lib/display-names.ts`, the single home for
name condensing, uses the display name, or the resource name when it reads as a
human name (`pdu-a.rk105.yul3`). Without one, a Cable is named by its medium
("Copper cable") and a peer by its kind ("Unnamed device"). When two different
records would read the same, `viewNames()` adds a short suffix from the
resource name ("Copper cable #295e"). A denied peer is never named or
disambiguated. Full resource names appear only in each record's own footer. Type
words ("Device", "Power", "NetworkPort") appear only in group headers.

### Port tiles

The tiles are the picture when the Device's model records no layout.
They carry a "Layout not recorded" caption: they are
grouped, not placed. `portLayout()` lays the tiles out
as a faceplate: by chassis face (Front, Rear, or Face unknown), then one
cluster per type, ordered by the first component of that type, which follows
the DeviceType's template order. Each face's title states its derived rack
face once ("Rear · rack rear"); when no port has a recorded face, one caption
reads "Component faces not recorded". `portLayout()` decides grouping for the
tiles; recorded positions are decided only by `faceplate-layout.ts`. A
cluster that is entirely catalog only
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
clearly names a family (RJ45, SFP, QSFP, LC, IEC C13/C14/C19/C20/C22); anything else is
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
non-modal dialog named "swp37 details". The card leads with the answer: each
position's record state, then "→ peer · port" with "via Cable" on its own
muted line, using display names and the Cable's medium as its tooltip (a
breakout names its shared Cable once, then one line per lane). A reason
every link shares, such as "2 Cable lanes name this position.", is said
once under the links; other reasons and any opposite-face check follow
their link. Power tiles (PSUs and outlets) then get a **Telemetry** section
with the values, their ages and a sparkline, and a load tint and words in
their accessible name when a source reports values; the collector, observed
time and freshness window are the section's tooltip. See
[Power path and telemetry](#power-path-and-telemetry-implemented-telemetry-mocked).
A **Health** section appears only when a telemetry source observes health;
with none there is no section, so unknown never reads as OK. The card ends
with the facts that are recorded, inline: **Connector** (and positions),
**Identity** (serial and asset tag), **Internal path** and the onward
**Path**. There is no More: record names, match basis and "recorded on"
wording are not shown. A denied peer's name and record name appear
nowhere. On screens 600 px
wide or less, the card is a bottom sheet. Touch shows no tooltip. Escape or
Close closes the card and returns focus to the tile.

On the Device page, the table's header row holds the port counts ("60 ports ·
7 recorded · 51 unknown · 2 disagree"). It does not repeat the Device's
name, which the page heading already shows.

### Faceplate drawing

**Implemented.** When the Device's DeviceType records a
[faceplate layout](hardware-catalog.md#faceplate-layout-implemented), Device
detail's picture is the drawing, and the rack side panel shows the same one.
`faceplate-drawing.tsx` draws each recorded face from one pure geometry
function, `faceGeometry()` in `lib/faceplate-layout.ts`, in millimetres at
1.9 px/mm. The drawing never invents a position:

- **Banks.** Each bank is a grid of cells. Banks with offsets sit at
  offset × face width; banks without offsets flow left to right with even
  gaps. A layout wider or taller than the face is scaled down and says so.
  CEL cannot check physical fit.
- **Ports.** Ports are the shared `PortTileButton` in their cells, with the
  shared tooltip and `PortCard` through `usePortGrid()`. They are not a
  second tile or card. A cell's outline comes from the connector kind (see
  below).
- **Module cells.** Drive bays, fans and accessory ports (`Module`,
  `InventoryItem`, `DeviceBay`) are drawn with no Cable state. They read
  "Component recorded" (■) or "Catalog only" (◇, hatched), and they are not
  focusable.
- **The dense rule.** `TEXT_TILE_LIMIT` (12) is one constant, in
  `lib/faceplate-layout.ts`. It is shared by the tile clusters and the drawn
  banks. Above it, ports carry no text: faint printed-style numbers sit at
  the bank's first and last columns, above the top row and below the bottom
  row. Hover or focus shows the one-line tooltip, and click, tap or Enter
  opens the card. Smaller banks print short labels above the top row or
  below the bottom row. Module cells carry their label inside. Accessible
  names always give the port, its state and every peer.
- **Not on the faceplate.** Templates without a layout, layout that
  disagrees (an overlap, a bank on the other face, outside the grid), and
  Device components with no matching template are listed in this tray with
  the reason. They are never placed. Disagreeing layout also shows a
  "Records disagree" warning on the face.
- **Template join.** `joinTemplates()` in `lib/faceplate-layout.ts` decides
  which component a template cell draws. The id join comes first. A
  component with no template of the same id falls back to an exact match on
  name and type, with names trimmed and runs of whitespace collapsed (case
  is kept). The fallback is used only when exactly one template matches,
  that template is not joined by id to another component, and no other
  component falls back to the same template. Otherwise the component stays
  in the tray. Imported records need this: their component ids are importer
  instance ids (`interfaces-3362`) and their template ids are template ids
  (`interface-templates-34`), so no ids match. A
  name match only positions the component on the drawing. Record state,
  Cable joins, chassis faces (and so opposite-face checks), the grouped
  tiles and the table still join by id, so a name-matched template still
  appears there as catalog only. The long-term fix is for the importer to
  record each component's template id (gap P7). Changing component ids is
  not the fix, because Cables reference component ids.
- **Honest labels.** Unknown width is drawn full width and says "Width not
  recorded". A zero-U model is drawn as a one-unit strip and says its
  length is not recorded. A model with no `heightUnits` gets tiles.
- **Keyboard.** The drawing is one tab stop. Ports are in row-major order
  per bank, so Left and Right move along a drawn row, and Up and Down move
  to the nearest port in the next drawn row. Escape closes the card and
  returns focus to the port.
- **Phones.** The chassis keeps its drawn width and scrolls sideways inside
  its own frame. The page itself never overflows at 390 px.

**Connector kinds** are derived, never stored. `connectorKind()` in
`lib/connector-kind.ts` holds the one fixed table. It matches whole tokens,
case-insensitively, and the first row wins:

| Kind | Matches (examples) | Nominal cell (mm) |
| --- | --- | --- |
| QSFP-DD | `QSFP-DD`, `qsfpdd`, `400gbase-x-qsfpdd` | 18.4 × 10.8 |
| OSFP | `OSFP`, `800gbase-x-osfp` | 22.6 × 13 |
| QSFP56 | `QSFP56`, `200gbase-x-qsfp56` | 18.4 × 9.4 |
| QSFP28 | `QSFP28`, `100gbase-x-qsfp28` | 18.4 × 9.4 |
| SFP28 | `SFP28`, `25gbase-x-sfp28` | 13.7 × 9.2 |
| SFP | `SFP`, `SFP+`, `sfpp`, `10gbase-x-sfpp`, `1000base-x-sfp` | 13.7 × 9.2 |
| RJ45 | `RJ45`, `rj-45 keystone`, `8P8C`, and any `<rate>base-t` or `<rate>base-tx`: `1000base-t`, `100base-tx`, `10gbase-t`, `2.5gbase-t`, `5gbase-t` | 13.2 × 11.6 |
| LC (duplex fiber) | `LC`, `lc`, `LC duplex`, `lc-upc`, `LC/APC` | 13 × 10 |
| C13, C14, C19, C20, C22 | `IEC C13`, `iec-60320-c14`, `iec-60320-c22` | 24 × 16.4; 22 × 17 |
| USB | `USB`, `USB-A`, `usb-c` | 13.2 × 6 |
| DB9 (serial) | `DB9`, `DE-9` | 31 × 12.5 |
| VGA | `VGA`, `DE-15` | 31 × 12.5 |
| Other | anything else, including `QSFP`, `QSFP+`, `40gbase-x-qsfpp`, `SFP56`, `MPO`, `100base-t1`, and blank or absent text | 14 × 12 |

NetBox interface types read `<rate>base-<media>-<form factor>`, so the cage
rows match the last token (`800gbase-x-osfp` is OSFP, `10gbase-x-sfpp` is
SFP). Copper types name no form factor and come after every cage row, so a
copper module in an SFP cage stays SFP. Single-pair types (`*base-t1`) are
not 8P8C jacks and stay Other.

QSFP56 is its own kind, not a QSFP28 alias: it has the same cage and nominal
size, so it is drawn with the QSFP outline, but the card does not
call a 200G port QSFP28. Plain `QSFP` and `QSFP+` stay Other. `QSFP` names no
generation, and adding `QSFP+` would mean a new kind for a family the
imported data does not use; the table keeps only families operators name.

The tile outline (`connectorShape()`) derives from the same table. LC is
drawn as a duplex connector: one body with a latch notch for each fiber.

**Elevation slots show names only.** `RackSlotDevice` draws the device's
name ("rear of" on the far face of a full-depth device) and, for equipment
taller than one unit, its range and height; no faceplate drawing sits inside
the slot. The selected device's faceplate is in the side panel. Each unit is
18 px, and the only view control is the face: **Front + rear** (the
default), **Front** or **Rear**. The slot is one link. A slot that needs
checking is colour coded rather than marked: red when records disagree
(overlapping placements, or ports more than one Cable names) and amber when
a connection reaches a missing or unreadable record, as a tinted fill and a
3 px left edge. The reason is the tooltip and the link's accessible name
("2 ports disagree"). The elevation has no legend or caption text: the
face switch is its only control.

**Every unit is drawn.** The elevation has no collapsed rows: each unit is
one grid row (row = height − unit + 1), so no placement's row ever moves and
Front + rear columns always line up. A unit with no recorded placement
shows "—" (occupancy unverified); blank is never free. The rack page's
arrow keys, Home and End move between equipment links in drawn order, top
to bottom.

**Not implemented:**

- health marks; a card has a Health section only when health is observed;
- decorative marks;
- photos or hotspots;
- cards for module cells;

`pnpm screenshots` captures these pages:

- `faceplate-server`, `faceplate-storage` and `faceplate-switch`;
- `faceplate-switch-card`;
- `faceplate-patch-pdu` and `faceplate-pdu`;
- `faceplate-fallback`;
- `faceplate-imported` and `faceplate-imported-phone`: a synthetic imported switch whose component
  ids match no template id, drawn by name and type, with its two same-name
  PSUs in the tray;
- `rack-panel-faceplate`, which includes a 390 px phone.

### Screenshots and chassis faces

`pnpm screenshots` captures the tiles (`ports-server`, `ports-leaf`), open
cards (`ports-leaf-card`, `ports-server-conflict-card`,
`ports-denied-peer-card`), and the same faceplate in the rack panel
(`rack-leaf-faceplate`, `rack-leaf-card`) at desktop light, desktop dark and
the opt-in `phone-dark` viewport. `device-table` captures the default page
through the retired `?view=table`, and `server-detail` and
`device-detail-no-layout` capture a whole Device page with and without a
recorded layout.

A port's chassis face comes from the DeviceType template with the same id
and is unknown without a readable catalog. When the Device's mounted face is
known, the face label adds the derived rack face only when it differs
("Front · rack rear" on a reversed switch). A Cable whose two ends land on
opposite rack faces gets a soft "Check" note in the table row and in the
port's card. The note does not change the record state.

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
  issue badges ("2 placements disagree", "1 without a position"). There is
  no separate count strip or free-space line. **Edit display name** sits in the header
  beside **Refresh inventory**; the resource name closes the page in the
  one-line **Record** footer.
- **Tabs.** Elevation (default), Equipment, Cables and Power, each related
  tab with its count ("Equipment 10"): "?" when the collection is
  unreadable, never 0, and "+" when it is incomplete. The tab
  is the `tab` query key beside `kind`, `record` and `namespace`; Back and
  shared links restore it. An unknown value falls back to the elevation.
- **Selection.** Selecting equipment in the elevation sets `selectedKind`
  (`Device` or `DeviceClaim`) and `selected` (the record name) and opens a
  side panel. With nothing selected there is no placeholder panel: the
  elevation takes the full width. Below 600 px it is a bottom sheet (`role="dialog"`, non-modal,
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
- **Equipment tab.** One dense table (`Rack equipment`), top of rack first,
  then equipment with no usable position ("No position"), then non-unit
  mounts (named by their mount). Columns: **U**, **Device** (lifecycle only
  when not Active; a plan with no registered device shows its name, muted),
  **Model**, **Face**, **Depth** (Full, Half or Unknown), **Ports** (cabled
  of total, the reason in the tooltip), **Power** (the feeds its PSUs reach,
  "?" when unknown, amber for a single feed), and **Serial** and **Asset
  tag** when any row records them. Rows that need checking carry the
  elevation's red or amber edge. Facility, rack and installation plan are
  not repeated per row; the plan is one step away in the side panel.
- **Cables tab.** One dense table (`Rack cables`), one row per Cable lane
  that reaches a device in this rack, listed once even when both ends are
  here. Columns: **Cable** (display name; a breakout lane adds "· 1/4"),
  **End** (this rack's device · port), **Far end** (device or feed · port)
  and **Medium** when any lane records one. There is no status column and no
  record names. A lane whose records disagree has a red edge and the end
  more than one lane names in red; a missing or unreadable peer has an amber
  edge and the far end in amber. The reasons are the row's tooltip and the
  far end's accessible text. Below 600 px the ends wrap and Medium is
  dropped, so nothing scrolls sideways. With Cables or Devices unreadable
  the tab reads "Cable details unavailable." or "Device details
  unavailable." and its count is "?".
- **Power tab.** One redundancy view: the rack's feeds side by side, and
  each device once with its PSUs under the feed their recorded path reaches
  (see
  [Power path and telemetry](#power-path-and-telemetry-implemented-telemetry-mocked)).
  Each feed column carries a compact capacity bar ("706 VA · 14%", the
  rating, continuous limit and age in the tooltip) and the feed's
  terminations as outlet tiles (peer and Cable in each tile's name, tooltip
  and card); the feed name links to its page and power path. Rows carry no
  feed-class badges: the columns show which feeds each PSU reaches, PSUs in
  the Feed unknown column are named only, and a single-feed row has an
  amber edge with the words as its tooltip. A PDU's row links to the PDU,
  whose page draws every outlet. The separate "Feed outlets" and "PDUs" lists below the grid were
  removed because they repeated this. When power feeds are unreadable the
  grid still lists devices and PDUs, with every PSU's feed unknown and no
  feed named.
- **Issue banners.** Above the elevation, one short banner per cluster of
  same-face overlapping placements, from the elevation's own face-aware
  occupancy (flagged, not blocked), and one for equipment that
  names the rack without a usable unit position. Each names the affected
  equipment as links that select it.

`RackSlotDevice` (`rack-slot-device.tsx`) is the single component that
renders equipment in a unit span. For a readable Device it draws a mini
faceplate (see [Faceplate drawing](#faceplate-drawing)). The elevation toolbar (`RackOverview`'s `toolbar` slot, beside the
face switch) and the stage's `data-view="2d"` leave room for a later 2D/3D
toggle; no 3D view exists. `condensedName()` in `lib/display-names.ts`
shortens component names for tiles (Ethernet 0 → eth0, Outlet 3 → O3); the
full name stays in the tile's accessible name and card.

## Power path and telemetry (implemented, telemetry mocked)

**PowerFeed page.** The primary view is the recorded power path, read left
to right and stacked at phone width: **Upstream** (up to three source ids,
one line each and truncated with the full id as tooltip; more become a count
linking to an Upstream sources tab, the only time that tab exists) →
**Feed** (rating, phase, capacity bar, and the feed's terminations as outlet
tiles) → **PDUs** (each PDU cabled to a termination, drawn as its faceplate
when its model records a layout, otherwise as outlet tiles: inlet first,
then outlets) → **Devices** (one dense table, one row per PSU: **Device**,
once per device; **PSU**; **Via**, the PDU outlet, or just "Outlet 1" when
the path has one PDU, with the PDU in the tooltip and accessible text, or
the other feed it is on, or "Feed unknown" with the reason hidden for
assistive technology; and **Draw**, "182 W" with the age as tooltip, "?"
when not reported). A device on a single feed or with a PSU whose feed is
unknown has an amber edge on its cell, with the feed class as tooltip and
accessible text; separate feeds carry no mark. PSUs on another feed are
muted rows. `feedPath()` and `psuFeeds()` in
`ui/provider/src/lib/power-paths.ts` derive this from Cable records and PDU
internal mappings only; `feedConnections()` in `device-connections.ts` joins
a feed's terminations with the same record states as Device components.

Outlets are tiles, not rows of text. A PDU whose DeviceType records a
layout is drawn by the shared faceplate renderer (`FaceplateDrawing` with
`fit`), cropped to its recorded banks and scaled to the column, down to
1.2 px/mm, and never wider than the column, so a 390 px phone shows every
outlet. Its face label is the short one ("Rear"). Anything else, including a
feed's terminations, is `OutletTiles` in `power-feed-path.tsx`: the shared
`PortTileButton` shaped by connector kind. Both use `usePortGrid`, so the
tooltip, keyboard model and `PortCard` match the Device faceplate; there is
no second card. The dense rule is the faceplate's: above `TEXT_TILE_LIMIT`
(12) outlets the tiles carry no text and faint edge numbers mark the first,
every eighth and the last. Each tile has the load tint and the status marks
(`↑`, `⚠`, `◷`, `≠`, `✕`); what an outlet is cabled to is in its accessible
name, tooltip and card, and the numbers (A, W) are in the card only. A
group (the feed's outlets, each PDU) shows a freshness line only when some
reading is stale or missing: "Stale · updated 3 h ago", "Updated 50 s ago ·
6 not reported" or "Not reported" (`groupFreshness()`). When every reading
is fresh the ages are in each tile's accessible name and card. A group is
one tab stop: arrow keys, Home and End move; Enter or a click opens the card;
Escape closes it and returns focus.

**Capacity.** `feedCapacity()` in `power-capacity.ts` computes the rated
apparent power from the recorded supply: single-phase `S = V × I`,
three-phase `S = √3 × V × I` (recorded line-to-line voltage), DC
`P = V × I`. The continuous limit is 80% of that, drawn as a line on the bar.
A missing voltage, current or phase leaves the rating unknown. Measured
apparent power (or real power, when apparent is not reported) fills the bar.
The text is the same on the PowerFeed page and the rack Power tab, "706 VA ·
14%"; the full reading, rating, continuous limit, formula and age are its
tooltip and accessible text. Without telemetry it reads "No load reported",
never empty or 0. Load above 80% of the continuous limit is "high" (`↑`), above it "over"
(`⚠`).

**Rack Power tab.** `RackPowerRedundancy` in `rack-power.tsx` shows the
rack's feeds as columns (plus any feed reached but recorded on another rack,
and "Feed unknown"), and each device with a power inlet as one row, its PSUs
in the column of the feed they reach. `classifyFeeds()` classifies a device
as separate feeds, single feed (amber row edge), feed unknown for some PSUs
or feed unknown; the words are the row's tooltip and accessible text, and
nothing claims redundancy.
Below 700 px each row stacks and names the feed in each cell.

**Telemetry.** Views read observed values through `TelemetrySource`
(`ui/provider/src/lib/telemetry.ts`), keyed by inventory identity (feed,
Device, or a component of either). Each value carries its origin, observed
time, the collector's staleness window and an optional 24-hour series. The
display rules, in `power-telemetry.tsx`:

- every value shows its age, and accessible names carry value, unit and age;
- stale values are greyed, dotted and labelled "Stale", never green;
- a missing value is "Not reported", never 0;
- over-threshold values get a glyph as well as colour;
- a tile reading with no limit to scale against (a Device's PSU) is a
  neutral full bar (`reported`), never red: red is only for over the limit
  and for observed-and-recorded disagreement;
- observed health appears only when reported; "OK" is plain text with its
  age, Fault is red with `✕` and Absent amber with `▼`;
- observed load on a component with no Cable recorded is "observed and
  recorded disagree": a banner on the PowerFeed page, a `≠` mark on the
  tile, and a note in the card, each linking to the records, which are not
  changed. With Cables unreadable nothing is compared;
- sparklines appear only in cards;
- the Device page's header strip shows "Draw X W", with the age and
  "observed, not verified" as its tooltip (hidden when nothing reports a draw), and PSU cards show input
  power and observed status ("Not reported" when absent).

Every build uses the `none` source: everything reads "Not reported" and "No
telemetry source configured". Fixture builds load a synthetic mock
(`telemetry-mock.ts`) behind the same build-time flag as the fixture
inventory; `?telemetry=normal|high-load|stale|missing|mismatch|none` picks a
scenario. `src/test/production-bundle.test.ts` builds the production bundle
and fails if any mock-only string appears in it. The real source is a
[proposal](inventory-telemetry.md).

`pnpm screenshots` captures `power-feed-normal`, `power-feed-high-load`,
`power-feed-stale`, `power-feed-mismatch`, `power-feed-no-telemetry`,
`power-feed-outlet-card`,
`rack-power-redundancy`, `device-psu-card` and `device-single-feed` (a server
whose PSUs both reach Feed A through one PDU: "Single feed (recorded)").
`rack-elevation` shows every unit.

## Bare Metal tabs: Capacity, Profiles and Classes (implemented)

All four Bare Metal tabs share one header in `fleet-page.tsx`: the tab row
comes first, with the page action (Register capacity, Create profile, Create
class) at its right end, so switching tabs never moves the row. There is no
page title or project subtitle; Capacity, Profiles and Classes carry a
visually hidden "Bare Metal" heading, and Inventory pages title themselves
below the tabs.

- **Tab counts.** Capacity, Profiles and Classes each read all three lists
  (shared with the page through React Query) and show their lengths on the
  tabs. A failed or denied read shows "?"; a list still loading shows no count.
  Inventory pages only read what is already cached and send no Bare Metal
  requests of their own.
- **Search** appears only when a list has 10 or more rows (`SEARCH_MIN_ROWS`).
- **Tables** keep one line per row. At phone width the table scrolls inside
  its frame and the page does not; the leading columns are the ones operators
  scan for.

**Capacity** columns are Name | State | Device | Pool | Allocation | CPU |
Memory. State is one word from `capacityState()` in `types.ts`:

| State | When | Colour |
| --- | --- | --- |
| Unhealthy | Healthy=False, even on Disabled capacity | Red |
| Disabled | `allocationPolicy: Disabled` | None |
| Allocated | Available=False with reason `Allocated` | None |
| Unavailable | Available=False for any other reason | Amber |
| Available | Available, Prepared and Healthy are all True | None |
| ? | Anything else, including absent conditions | Amber |

The tooltip and accessible name list each condition with its reason. Device is
the capacity's `spec.deviceRef`, linked to the inventory record when the
capacity is in the inventory namespace (`default`); elsewhere it is named
without a link. `?namespace=…&pool=…` filters the list to one pool, with a
"Show all" link.

Register capacity is disabled when the session or permission is missing, and
the reason is the button's tooltip and accessible description, not page text.
A confirmed registration closes the dialog and leaves no message: the new row
is the confirmation. See
[inventory-backed capacity registration](inventory-capacity-registration.md#ui).

**Classes** show the pool the class selects as `central-metal · 1/3`
(available / in pool), linked to Capacity filtered to that pool. The count is
amber when nothing in the pool is available, and "?" when capacity cannot be
read. The create form picks the pool from the pools that MachineCapacities
carry, each with its count, and derives the namespace from that choice; with
no readable pool, a class cannot be created.

## Deliberate gaps and decisions

| Interaction | Remaining boundary |
| --- | --- |
| Other-writer edits | Reported-location correction is available to managers for operator-managed Devices; GitOps-managed records remain outside the UI. Other edit forms retain their existing restrictions |
| Rack position entry, move/assign equipment | Reported-location correction supports unknown or explicit geometry; physical moves, installation/binding changes and handovers still require separate workflows |
| PowerFeed creation; component or endpoint edits | API kinds exist, but these forms are not implemented. Existing records remain browsable; no dead action buttons |
| Online/active/connected badges, occupancy/free units | No verified evidence projection; never infer health/readiness from inventory |
| Power utilization | Views accept observed telemetry through `TelemetrySource`; only `none` (every build) and a fixture-only mock exist. The real source is [proposed](inventory-telemetry.md) |
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

## Locations and catalog pages (implemented)

Site, Facility, Manufacturer and DeviceType pages carry no caveat sentences or
issue banners: counts on tabs show unknown (`?`) and incomplete (amber `N+`)
collections, with the reason in the tooltip.

- **Site.** The header is the site code and, when the Site links exactly one
  readable facility, a link to it. Otherwise the first tab is **Facilities**
  (Facility | Premises code | Racks), with an unresolved membership as one
  amber line. **Racks** uses the shared racks table. **Devices** is a dense
  table: Device | Rack · U (Location, naming the facility, when the devices
  span several) | Model | Serial | Asset tag | Lifecycle; a planned-only or
  unknown location has an amber edge, the reason in its tooltip.
  **Installation plans** is Plan | Planned location | Expected model |
  Registered device, sorted by rack, then unit.
- **Facility.** The header is the premises code (provider and identifier
  type in the tooltip), Site links and the geographic location reference.
  Tabs: **Racks** (Rack | Height | Equipment | Power, the power as the Rack
  page's compact load per feed; red edge when placements overlap, amber
  when some have no usable position), **Without a rack** (the Rack page's
  equipment table without the U column, plus **Plan**: the plan's link,
  "Unavailable" when plans are unreadable, or amber "Plan not found" with
  the missing plan's name in the tooltip), **Power feeds** (Feed | Rack |
  Capacity) and **Cables** (Cable | Medium | Lanes | End A | End B; red edge
  when another Cable names the same position).
- **Manufacturer.** Tabs **Device types** (Device type | Model key | Size |
  Devices) and **Devices** (Device | Model | Rack · U | Serial | Asset tag,
  reported placement only; the facility only when devices span several or a
  device has no rack). An alias equal to the record name is not repeated in
  the header.
- **DeviceType.** The header is one facts strip: manufacturer link, model key
  (tooltip "Model key"), height and depth; a zero-U model reads "Zero-U"
  with no depth. The picture is `FaceplateDrawing dense` over
  `modelDrawingModel(model)`: the model's layout with every template catalog
  only, no Cables and no rack face. Without banks it reads "Layout not
  recorded". The component table is Component | Type | Face | Connector |
  Positions, Positions only when some template has several; hovering or
  clicking a row outlines its tile. **Devices** uses the Site's device table
  without the Model column.

The Rack page's equipment table lives in `equipment-table.tsx` and is shared
with the Facility's **Without a rack** tab.

### Operational Site membership

Site detail shows linked Facilities, equipment recorded at those premises,
and installation plans. Facility links use `Site.spec.facilityRefs` in the same
namespace, never display names or provider codes. Shared/duplicate Facility links
and device/rack records are deduplicated; equipment is **at linked facilities**,
not assigned to the Site. Counts grant no eligibility, capacity or readiness.

Devices follow the existing reported-placement projection, with explicitly labeled
claim-derived fallback only when no report exists. Rack-only references resolve
through readable, non-deleting Rack records. Every matching device registration
is counted; none is selected as the active device. Plans use their own typed
placement. The implemented API has no execution-status fields, so no plan
shows ready or complete. Operator, address and region are absent from the
implemented Facility schema and are not invented from provider identifiers or
display names.

Missing/deleting Facility references remain unresolved. Denied collections redact
names and links and read "Access denied"; unavailable collections read "Records
unavailable". Readable subsets remain useful with incomplete counts where
dependencies are unavailable or unresolved.

This is presentation over the existing authorized default-only inventory snapshot.
No new queries, forms, mutations, cache policy or session/operation guards are
introduced. Same-principal cache reuse, observed denial redaction, actual session
fencing, inert visible blur/focus and exact mutation preconditions are unchanged.

Validation uses provider component/regression tests, typecheck and production build,
plus documentation link and whitespace checks. Tests cover Site → Facility → Rack
→ claim/device paths, direct reports, duplicates/shared premises,
missing/deleting/foreign references, denied collections and partial reads. No
browser or authenticated host acceptance, live inventory reads/writes, deployment,
or operational readiness is claimed. Responsive behavior uses the existing scoped
layout and scrollable Datum tables; visual layout remains unverified in a browser.

Local validation on 2026-11-22: all 41 provider test files / 678 tests passed;
provider `pnpm typecheck`, `pnpm build`, `bash hack/validate-doc-links.sh` and
`git diff --check` passed. Consumer/shared API code is unchanged; consumer checks
were not required for this provider-only presentation change.
