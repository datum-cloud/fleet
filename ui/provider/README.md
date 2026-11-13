# Bare Metal staff portal plugin

A [staff-portal](https://github.com/datum-cloud/staff-portal) plugin for the
provider project's Bare Metal resources (`metal.datumapis.com`). It is a Module Federation remote: the
portal fetches `plugin-manifest.json` and `remoteEntry.js` server-side and
mounts its pages under `/plugins/<slug>/`, with a **Bare Metal** item under
Operations in the navigation. The page has three tabs:

- **Capacity** (the mount root, where the nav item points) lists the
  provider's MachineCapacities in every namespace and creates new ones. The
  form asks only for a name, a class, and whether to offer the capacity for
  allocation (off by default). It goes in the class's namespace and capacity
  pool, with a device reference named after the record (Fleet has no Device
  inventory yet) and the standalone fixtures' cleaning policy. The device
  reference can't be changed later. Hardware and conditions stay empty until
  Fleet's discovery reports them.
- **Profiles** (`profiles`) lists MachineProfiles and creates new ones. A
  profile can't be changed after it is created, so there is no edit.
- **Classes** (`classes`) lists MachineClasses and creates new ones: the
  profiles a class allows, the namespace and capacity-pool label of the
  MachineCapacities that serve it, and whether consumers get power control. Custom
  profiles and console access stay off.

The portal proxy forwards the staff member's own token, so creating capacity,
profiles and classes needs a Bare Metal role with `machinecapacities.create`,
`machineprofiles.create` and `machineclasses.create` in the provider project,
such as `metal.datumapis.com-provider`. Errors show inside the create dialog,
because staff-portal does not share its toast module with plugins.

This started as the MVP from [#17](https://github.com/datum-cloud/fleet/issues/17).
It is deployed by `config/components/provider-ui-plugin` and registered through
the `metal` ServiceConfiguration's `userInterface`.

The provider project is `datum-cloud`, the only one for now, set in
`src/lib/api.ts`. The wire types are in `../shared/fleet-api.ts`, transcribed
by hand from Fleet's Go API types and CRDs; `src/types.ts` maps them to the
list's view.

## Commands

| Command                  | What it does                                            |
| ------------------------ | ------------------------------------------------------- |
| `pnpm install`           | Install dependencies (Node 26, see `.nvmrc`).           |
| `pnpm test`              | Run the vitest suite.                                   |
| `pnpm typecheck`         | Type-check without emitting.                            |
| `pnpm build`             | Build into `dist/`, reading the live Fleet API.         |
| `pnpm build:fixtures`    | Build with synthetic capacity instead of the API.       |
| `pnpm preview`           | Serve `dist/` on http://localhost:7781.                 |
| `pnpm dev`               | Vite dev server for the standalone `index.html` only.   |

## Fixture mode

The Fleet CRDs are on staging, but reading them needs a Bare Metal role. Setting
`VITE_FLEET_FIXTURES=true` at build time makes `useMachineCapacities` return
the synthetic MachineCapacities in `src/lib/fixtures.ts`: one free, one allocated and one
just enrolled, following Fleet's standalone fixtures and the statuses its
standalone runner writes. Profiles start with the standalone fixture's
profile; profiles you create are kept in memory until the page reloads. The
flag is baked into the build.

## Run inside staff-portal

```bash
# fleet/ui/provider
pnpm build:fixtures && pnpm preview

# staff-portal, in another terminal (you must be in the staff-users group)
PORTAL_PLUGINS='metal-datumapis-com=http://localhost:7781' npm run dev
```

If staff-portal's `.env` already sets `PORTAL_PLUGINS_JSON`, add
`{"slug":"metal-datumapis-com","assets":{"baseURL":"http://localhost:7781"}}`
to that array instead.

## Differences from the consumer plugin

- staff-portal runs react-router 7, so this plugin pins it for types and
  shares it with `requiredVersion: false`.
- staff-portal shares only the `badge`, `button`, `card`, `icons` and
  `skeleton` datum-ui subpaths. Everything else, including the data table, is
  bundled, so its peer packages are runtime dependencies here.
- Responses from staff-portal's `/api/internal` proxy are wrapped in an
  envelope; `proxyFetch` returns its `data`.
- `ui/shared/` holds code both plugins use. It must not import packages,
  because each plugin resolves dependencies from its own `node_modules`.
