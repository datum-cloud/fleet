# Staff physical inventory UI

The additive [Site–Facility directory](site-facility-relationship.md) extends this
baseline with operational Site reads and provider-qualified premises identifiers.
Its identity/membership fields are managed by direct API, not new UI forms.

Local, uncommitted implementation on `0b6c8977227157575d388a42aac127769258f998`
(PR95 merged into `feat/manual-inventory-records`). This phase does not publish,
deploy, import records, change IAM membership, or change inventory ownership.
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
- Create Facility/Rack facts; rack height is optional. Create an Installation
  plan at a known Facility or Rack with unknown unit position, then separately
  register a Device with a real serial or system UUID and typed components.
  This is not an atomic multi-record workflow or an adoption operation.
- Create Cable records with a medium and named lanes using existing readable
  Device/PowerFeed components with recorded positions. No guessed ports, cable
  lengths or live connectivity. Reference owner UID/resourceVersion is retained
  and checked before creation; the API still cannot atomically reserve endpoints.
- Edit display name and recording details only for records with established
  portal ownership. No device identity, claim binding, components, topology,
  placement, lifecycle, status or operational changes are exposed through edit.
- Correct Device-reported location in a separate form, including the first report
  on an existing imported Device. Manual source/evidence/reason and explicit
  unknown geometry are retained without changing identity or claim binding.

## Authorization, ownership and concurrency

Device source reports and labelled claim fallback now share a placement
projection. See [Device-reported placement](device-reported-placement.md) for the
additive contract and ordinary authorized corrections, including first reports
on existing imported Devices. Original import provenance and immutable identity
remain intact; manual corrections do not claim NetBox authorship.

The existing `fleet.datumapis.com-inventory-manager` role inherits the viewer and
adds create/update/patch for six kinds, project-wide. The UI uses exact-resource
SelfSubjectAccessReviews and the signed-in principal's API request, never group
names or staff membership. Role/binding deployment and membership are separate
work; nothing here asserts their live state. There are no delete/status grants.

Inventory is direct API/operator-managed, not GitOps-only. Import provenance is
history, **not evidence of Flux ownership**. Legacy Flux-managed records keep
their writer until an explicit safe transition. This UI's deliberately narrow
display-name edit policy still requires Manual provenance and exclusively
`fleet-staff-inventory-ui` managed fields, with no import/deletion/other-owner
markers. The separate reported-location editor permits imported operator-managed
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
and generation-guarded focus checks. Already-dispatched uncertain outcomes stay
unknown rather than becoming cancelled-success.

### Visible browser blur versus session authority

Visible blur also suspends new inventory/permission query dispatch and publication
and cancels outstanding queries without removing their previously authorized data.
Manual refresh cannot publish responses during unresolved host identity checks.
Read generations reject late successes and errors, even after authority resumes;
only the verified principal's fresh reads can renew management controls.
Rejected focus verification keeps the view masked during subsequent attempts;
only a successful attempt removes that mask. Regression tests directly check
cache replacement for batched principal changes and reject operation lifecycle
renewal when the host principal differs from the mounted principal.

The visible-blur correction separates read visibility from write authority.
Previously, opening Safari's URL bar called the same hide path as backgrounding
the document, unmounting inventory and showing LoadingState. A visible window
blur is not evidence of logout, account switching, or document backgrounding.
It can also mean focus moved to another visible window. Intentionally, already
authorized inventory remains on screen in all these visible-blur cases; this
is not a screen-privacy curtain for unfocused windows.

Writes remain conservatively fenced synchronously on every blur, pagehide,
hidden visibility transition, router revalidation/navigation, and principal
change. Returning focus verifies the real host loader. During that verification,
the existing same-principal read view stays mounted but operation authority
remains invalid. Management views now stay mounted, with disabled controls and
an accessible verification-pending status; cached permission is only evidence
for retaining the view, never current approval to write. Old operations never regain authority, including SSAR/reference
reads completing after an await; dispatched writes retain unknown outcomes.
After verification, an open form remains disabled until explicitly closed and
reopened. Namespace/access disclosure state and safe draft input survive visible
blur; create drafts survive reopening, while edits capture a new baseline and
reset to the refreshed record. Neither operative authority nor an immutable edit
baseline crosses the generation. Closing never submits or revives pending intent.
In-flight operations must settle before closing; a dispatched stale result is
unknown, never success. The inventory table and its view-specific query key stay
unchanged. Native disabled fieldsets cover nested hardware controls as well as
inputs; submit handlers independently enforce readiness and revocation.

Successful same-principal focus checks cancel older query work and refresh reads
in the isolated client before explicitly refreshing management permissions.
Refresh starts only after host authority is verified, not on a host completion
while still blurred or while the verification promise is pending. A failed or wholly unavailable inventory read keeps forms suspended
until the next successful focus verification or reopened inventory. The cache's
settled read result, not a potentially delayed React observer, gates this renewal:
no SSAR is sent for stale forms while the new read result denies all inventory.
An observed read denial still redacts records; revalidation is not removed. Hidden documents and pagehide immediately
unmount and clear the client; foreground restoration waits for verification.
Explicit host revalidation/navigation also retains its masking/cache-reset
policy. Principal transitions reset the client, including observed A→B→A
transitions batched into one React render. Missing identities and host failures
remove private content. A focus check completing after a newer blur cannot
restore write authority; after a newer hidden/pagehide event it cannot restore
read visibility either. The host completion generation is still observed via
the synchronous router subscription, not only React's possibly batched loading
state. The soft-read policy remains latched until the next router cycle so a
promise completion preceding React's idle render cannot momentarily unmount it.

This intentionally replaces tests that treated visible blur as a privacy mask.
Equivalent pagehide/hidden coverage is retained, and the adversarial write matrix
now exercises both visible focus/blur and focus/pagehide (create/edit,
pending SSAR, dispatched success/error, and StrictMode). Actual inventory-table
tests assert DOM identity through visible focus and verify refreshed denials;
operation-generation tests retain stale-completion and A→B→A protection.
Confirmed permission denial/error removes management actions and retained form
contents, rather than treating a cached grant as current. Related-read denials
still redact choices while preserving unrelated safe input. Genuine background,
principal changes and failed host verification still clear private content.

### Stable management controls follow-up

Implemented against PR103's merged staging base
`f190d28397fc6110e02c9c83e6819a3a584258d0`, not the earlier documentation branch.
The generation-keyed ready/null wrappers caused the reported Namespace & access
and create/edit sections to disappear on URL-bar focus. The replacement separates
view lifetime from authority lifetime for all five create kinds, display-name
editing, and reported-location editing. A reserved status area avoids adding a
new row only when verification starts. This is implemented behavior, not a new
inventory, ownership, or IAM contract.

The stable-controls suite checks identical DOM nodes, disabled controls, retained
disclosures/input, held host and permission verification, explicit baseline
renewal, denied permission and hard masking, with StrictMode variants. Earlier
review tests intentionally change only visible-blur removal expectations to
same-node/disabled assertions; hidden/pagehide assertions remain. Pending-write
tests retain zero pre-dispatch writes and unknown post-dispatch outcomes, and
include observed A→B→A. The changed tests are regression evidence, not independent
review signoff.

Follow-up local validation (2026-11-21): provider 475 tests in 31 files and
consumer 44 tests in 3 files pass, as do both TypeScript checks and production
builds, `go test ./internal/api/inventory/...`, relative documentation links, and
`git diff --check`. Temporary mutations restoring ready/null unmounting or
removing readiness from the management gate each fail 24 of the 32 stable-view
tests; both mutations were reverted before final validation.

Synthetic browser DOM/geometry checks retain identical control, form and table
nodes and bounding rectangles through blur and held focus verification. Checks
cover all five create kinds with collapsed/expanded access at 390px, Device
creation at 1280px, and display-name/location editors at 390px. No page-wide
mobile overflow was observed. Retained forms stay disabled after verification
until explicit reopening. Keyboard Tab/Enter and native disabled-focus checks
dispatch zero writes; pagehide removes private content and forms. The harness
supplies synthetic UIDs, resource versions and manual writer metadata for these
edit checks; no real ownership or server data changes. Preview screenshots timed
out, so this follow-up's browser evidence is DOM identity, geometry and event
checks, not screenshot or screen-reader acceptance. The real staging URL again
redirected to sign-in; authenticated Safari URL-bar acceptance remains unverified.

For a synthetic browser check, run from `ui/provider`:

```sh
pnpm exec vite --config vitest.config.ts --host 127.0.0.1 --port 7793
```

Open `/session-fixture.html`. This mounts the actual inventory page and boundary
under a synthetic host loader, not the preview bypass. All API calls are handled
in memory; writes return a synthetic failure and never reach an API. The browser
console's `inventorySessionFixture` exposes `hold(boolean)`, `release()`,
`principal(string|null)`, `denied(boolean)`, and `stats()` for controlled loader
completion, account transitions, and read denial. Dispatch window blur/focus or
pagehide to inspect transitions. This harness is not a production build entry.

Local browser evidence on 2026-11-21 uses synthetic events and data. Visible
blur/focus retained the identical table DOM node with no observed LoadingState;
collection reads renewed from seven to fourteen. Pagehide removed the table and
kept LoadingState through the held return-focus check; release restored a fresh
read view. A subsequent synthetic 403 replaced it with access guidance and no
private table. No fixture writes were dispatched. The real
staging URL redirected to sign-in; no login was manipulated. That evidence and
the supplied Safari screenshot do not prove authenticated Safari URL-bar
acceptance. Independent adversarial signoff, CI, and authenticated staging/Safari
acceptance remain deployment gates; no inventory, IAM, or NetBox data is changed
by this correction.

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

## Deliberate gaps and decisions

| Interaction | Remaining boundary |
| --- | --- |
| Imported/other-writer edits | Reported-location correction is available to managers, including imported Devices; legacy Flux remains outside the UI. Other edit forms retain their existing restrictions |
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
