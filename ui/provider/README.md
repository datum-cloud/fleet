# Bare Metal staff portal plugin

A [staff-portal](https://github.com/datum-cloud/staff-portal) plugin for the
provider project's Bare Metal resources (`metal.datumapis.com`). It is a Module Federation remote: the
portal fetches `plugin-manifest.json` and `remoteEntry.js` server-side and
mounts its pages under `/plugins/<slug>/`, with a **Bare Metal** item under
Operations in the navigation. The page has four tabs:

- **Capacity** (the mount root, where the nav item points) lists the
  provider's MachineCapacities in every namespace. A verified host session can
  register an existing `default` Device as `device-<Device UID>` capacity.
  Selection retains the exact Device UID/resourceVersion; the named reference
  is immutable. Registration is Disabled/Retain with cleaning explicitly
  unconfigured, without class assignment, allocation, preparation or positive
  readiness. The capacity name is derived from the Device UID, so a Device
  backs at most one capacity. Devices sharing a hardware alias are marked
  Conflicting. The server enforces permissions and checks live Devices for
  duplicates. Unknown completion blocks retries in the form and never deletes
  capacity to roll back. See the [registration contract](../../docs/development/inventory-capacity-registration.md).
- **Profiles** (`profiles`) lists MachineProfiles and creates new ones. A
  profile can't be changed after it is created, so there is no edit.
- **Classes** (`classes`) lists MachineClasses and creates new ones: the
  profiles a class allows, the namespace and capacity-pool label of the
  MachineCapacities that serve it, and whether consumers get power control. Custom
  profiles and console access stay off.
- **Inventory** (`inventory`) is a hardware directory, starting with
  Devices rather than installation claims. Nine static searchable categories cover
  Devices, Operational sites, Facilities, Racks, Claims, Cables, Power feeds,
  Manufacturers, and Device types. Inventory reads and writes use only `default`.
  Query-string detail links retain the kind and record name (and accept the existing
  explicit `namespace=default`) without adding host routes. Foreign namespace
  links never resolve records or change request scope.
  Facilities lead to racks and installation plans; claims lead to all registered
  devices; cable endpoints lead to devices or feeds. Identity, typed components,
  internal paths, external service references, legacy peers, and creation times
  remain visible. Device detail adds a connection view of Cable records
  (diagram and list), not live link state; see the
  [connection view](../../docs/development/staff-inventory-ui.md#device-connection-view-implemented).
  Missing claims do not hide hardware, and historical
  registrations do not establish an active device. The original `inventory/:namespace/:claim`
  route remains available for existing `default` links. Authorized manual management uses
  focused Datum dialogs for Facility/Rack creation, display-name editing, and
  separate reported-location correction. Device/Cable/installation-plan creation
  uses `inventory/create/device`, `inventory/create/cable`, and
  `inventory/create/installation-plan` task pages, with explicit back/cancel and
  URL-preserved list filters. Dirty dismissal requires confirmation; pending and
  unknown writes never imply cancellation or success. All existing guarded write
  handlers and default-only scope remain. Site membership and premises identifiers remain
  direct API operations. Records are not managed through Git. See the
  [Site–Facility contract](../../docs/development/site-facility-relationship.md).

The portal proxy forwards the staff member's own token, so creating capacity,
profiles and classes needs a Bare Metal role with `machinecapacities.create`,
`machineprofiles.create` and `machineclasses.create` in the provider project,
such as `metal.datumapis.com-provider`. The Inventory tab needs list access to
the six physical kinds and observational Sites in `fleet.datumapis.com`, which
`fleet.datumapis.com-inventory-viewer` grants; metal roles grant no inventory
access. Capacity registration additionally requires current named Device read
permission. Fixture builds cannot register capacity. Errors show inside the create dialog,
because staff-portal does not share its toast module with plugins.

This started as the MVP from [#17](https://github.com/datum-cloud/fleet/issues/17).
It is deployed by `config/components/provider-ui-plugin` and registered through
the `metal` ServiceConfiguration's `userInterface`.

The provider project is `datum-cloud`, the only one for now, set in
`src/lib/api.ts`. The wire types are in `../shared/fleet-api.ts`, transcribed
by hand from Fleet's Go API types and CRDs; `src/types.ts` maps them to the
list's view.

## Commands

| Command                  | What it does                                                        |
| ------------------------ | ------------------------------------------------------------------- |
| `pnpm install`           | Install dependencies (Node 26, see `.nvmrc`).                       |
| `pnpm test`              | Run the vitest suite.                                               |
| `pnpm typecheck`         | Type-check without emitting.                                        |
| `pnpm build`             | Build into `dist/`, reading the live Fleet API.                     |
| `pnpm build:fixtures`    | Build with synthetic data instead of the API (`--mode fixtures`).   |
| `pnpm preview`           | Serve `dist/` on http://localhost:7781.                             |
| `pnpm preview:fixtures`  | `build:fixtures`, then serve it on http://localhost:7781.           |
| `pnpm dev`               | Vite dev server for the standalone `index.html` only.               |
| `pnpm dev:fixtures`      | The same dev server with synthetic data.                            |
| `pnpm screenshots`       | Build the fixture preview and capture review screenshots.           |

## Fixture mode

The Fleet CRDs are on staging, but reading them needs a Bare Metal role. A
fixture build replaces every API read with synthetic data:

- `useMachineCapacities` returns the MachineCapacities in `src/lib/fixtures.ts`:
  one free, one allocated and one just enrolled, following Fleet's standalone
  fixtures and the statuses its standalone runner writes. Profiles and classes
  start with the standalone fixture's; ones you create stay in memory until
  the page reloads.
- `useInventory` returns the preview dataset in `src/lib/inventory-fixtures.ts`:
  one facility with three 42U racks, about fifteen Devices (1U and 2U servers,
  a 48+8-port leaf mounted reversed, a half-depth rear patch panel, a PDU on a
  non-unit mount, a Device with a rack but no position, one with no placement,
  two overlapping placements, a Decommissioning and a Retired Device), the
  catalog of Manufacturers and DeviceTypes with network, power, console and
  BMC templates, two power feeds, and Cables covering every connection-view
  state (recorded, two Cables on one port, a missing peer, a 4-lane breakout,
  PSU to PDU outlet to feed, and a patch-panel pass-through). It is typed
  against `../shared/fleet-api.ts`, so wire-type drift fails `pnpm typecheck`,
  and `inventory-fixtures.test.ts` checks it stays coherent.
- Inventory writes are disabled: permission checks answer "not allowed", so
  management and capacity-registration controls stay hidden or read-only.
  Nothing is sent to the API.

Add `?fixture=<scenario>` to a preview URL to deny whole collections, as a
403 would: `power-feeds-denied`, `devices-denied` or `cables-denied`
(`FIXTURE_SCENARIOS`). The scenario is read once per page load.

Fixture data can never reach a production build. `VITE_FLEET_FIXTURES` is
baked into the bundle at build time and is only honoured in Vite's `fixtures`
mode, which the `*:fixtures` and `screenshots` scripts pass. `vite.config.ts`
fails any other build or dev server that sets it, including through a local
`.env` file (`scripts/fixture-mode.ts`). The image build runs plain
`pnpm build`, which compiles the flag to `false`; the inventory dataset is
loaded lazily behind it, so it is not in that bundle at all.

## Preview locally

```bash
# fleet/ui/provider
pnpm preview:fixtures   # or pnpm dev:fixtures for hot reload
```

Open http://localhost:7781/. The standalone harness (`index.html`, `src/main.tsx`)
uses browser URLs under the same `/plugins/metal-datumapis-com/` paths
staff-portal mounts, with no login and no session boundary. Stable URLs:

| View                         | Path                                                                                             |
| ---------------------------- | ------------------------------------------------------------------------------------------------ |
| Inventory list               | `/plugins/metal-datumapis-com/inventory`                                                         |
| Rack elevation               | `/plugins/metal-datumapis-com/inventory?kind=Rack&record=example-rack-a01&namespace=default`     |
| Server detail and connections | `/plugins/metal-datumapis-com/inventory?kind=Device&record=example-srv-a01-01&namespace=default` |
| Large switch (list fallback) | `/plugins/metal-datumapis-com/inventory?kind=Device&record=example-leaf-a01&namespace=default`   |
| Denied peer                  | the server URL plus `&fixture=power-feeds-denied`                                                |

The banner's button switches light and dark; `?theme=light` or `?theme=dark`
on any URL sets it directly. The choice is remembered in local storage. The
theme switch is part of the harness only; federated pages use the host's theme.
`session-fixture.html` is a separate dev-only page for session-boundary tests
and is not built.

## Screenshots

```bash
# fleet/ui/provider
pnpm screenshots
```

This builds with fixtures, serves the build on port 7782, and captures each
page in `scripts/screenshot-pages.ts` at 1280px light, 1280px dark and 390px
phone width into `screenshots/` (gitignored). It prints every file it wrote,
and fails if a page logs a console error, throws, calls the API, renders
without the fixture banner, or scrolls horizontally at 390px. Playwright is a
pinned devDependency; if its Chromium is missing, run
`pnpm exec playwright install chromium`.

| Variable                | Effect                                                     |
| ----------------------- | ---------------------------------------------------------- |
| `SCREENSHOT_DIR`        | Output directory (default `ui/provider/screenshots`).      |
| `SCREENSHOT_PORT`       | Preview port (default 7782).                               |
| `SCREENSHOT_ONLY`       | Comma-separated page names to capture.                     |
| `SCREENSHOT_SKIP_BUILD` | `1` reuses `dist/` from an earlier `pnpm build:fixtures`.  |

To capture a new view, append an entry to `PAGES` in
`scripts/screenshot-pages.ts`: a `name` (the file stem), a preview `path`
(without `theme`), a `ready` selector to wait for, and optionally an `element`
selector to capture instead of the full page or a `viewports` subset. Name
records from `inventory-fixtures.ts`, adding any the view needs there; a unit
test fails if a page names a record or scenario the dataset lacks.

## Run inside staff-portal

```bash
# fleet/ui/provider
pnpm preview:fixtures

# staff-portal, in another terminal (you must be in the staff-users group)
PORTAL_PLUGINS='metal-datumapis-com=http://localhost:7781' npm run dev
```

If staff-portal's `.env` already sets `PORTAL_PLUGINS_JSON`, add
`{"slug":"metal-datumapis-com","assets":{"baseURL":"http://localhost:7781"}}`
to that array instead.

## Differences from the consumer plugin

Inventory uses four route-linked Datum underline groups: Equipment (Devices,
Cables, Power feeds), Locations (Operational sites, Facilities, Racks), Installation
plans, and Hardware catalog (Manufacturers, Device types). Only the selected
group's secondary navigation is shown. Installation plans opens the existing
Claims & registrations directory: legacy slots and registrations are not all
installation plans, and claims do not allocate capacity. Site membership and
Facility premises remain distinct. The default is Equipment / Devices.
Search/filter/refresh/Create stay in the page toolbar. Existing query bookmarks,
detail and task routes work with group/category breadcrumbs; Back/Forward restores
filters. Static navigation survives access checks and denial without exposing
protected names or counts. Rows scroll horizontally on small screens, with
keyboard-focus navigation and Enter/Space link activation. See the
[implemented navigation and safety boundaries](../../docs/development/staff-inventory-ui.md#grouped-inventory-navigation-implemented).

The Inventory tab loads all six physical inventory collections with API pagination.
Counts describe loaded records, not capacity or verified equipment. Unknown
occupancy, power budgets, and ambiguous bindings stay unknown. Contextual help
explains these boundaries; there is no report picker, sync, or hardware action.
Reads require the provider-scoped inventory viewer role, not a consumer Metal
role. Management separately checks backend create/patch permission in the
provider project: Facility/Rack/Installation-plan/Device/Cable creation
and portal-owned display-name editing. The existing
`fleet.datumapis.com-inventory-manager` role and separate
`physical-inventory-managers` group binding are not deployed by this UI phase.
Group names/staff membership never enable controls. Flux-managed and unknown-writer
records remain read-only pending ownership review. New inventory is direct
API/operator-managed. There is no namespace selector or namespace/access disclosure.
Default-only UI scope is not a backend authorization boundary; namespaced APIs and
existing IAM remain unchanged. Denied
collections and detail links show access guidance, not empty inventory; readable sections remain available.
See [hardware directory scope and design gaps](../../docs/development/staff-inventory-ui.md).

The standalone harness imports `src/preview.css`, compiling the installed Datum UI
theme with Tailwind. That stylesheet is not imported by federated pages; the host
still supplies its design system. Inventory-specific CSS is scoped under
`.fleet-inventory` and shipped with the existing remote. No second app or host
navigation is introduced. Fixture mode includes all six inventory kinds with
explicitly synthetic identities and no verified state (see [Fixture mode](#fixture-mode)).

- The local harness pins react-router 7.18.2 and shares it with
  `requiredVersion: false`. The supplied current host pin uses Router ^8.3.1;
  authenticated host integration remains an acceptance check, not proven by
  local harness tests.
- staff-portal shares only the `badge`, `button`, `card`, `icons` and
  `skeleton` datum-ui subpaths. Everything else, including the data table, is
  bundled, so its peer packages are runtime dependencies here.
- Responses from staff-portal's `/api/internal` proxy are wrapped in an
  envelope; `proxyFetch` returns its `data`.
- `ui/shared/` holds code both plugins use. It must not import packages,
  because each plugin resolves dependencies from its own `node_modules`.
