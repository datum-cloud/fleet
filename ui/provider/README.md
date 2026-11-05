# Fleet staff portal plugin

A [staff-portal](https://github.com/datum-cloud/staff-portal) plugin for the
provider project's Fleet resources. It is a Module Federation remote: the
portal fetches `plugin-manifest.json` and `remoteEntry.js` server-side and
mounts its pages under `/plugins/<slug>/`, with a **Fleet** item under
Operations in the navigation. The page has two tabs:

- **Machines** (`machines`) lists the provider's Machines. Read-only.
- **Profiles** (`profiles`) lists MachineProfiles and creates new ones. A
  profile can't be changed after it is created, so there is no edit.
- **Classes** (`classes`) lists MachineClasses and creates new ones: the
  profiles a class allows, the namespace and capacity-pool label of the
  Machines that serve it, and whether consumers get power control. Custom
  profiles and console access stay off.

The portal proxy forwards the staff member's own token, so creating profiles
and classes needs a Fleet role with `machineprofiles.create` and
`machineclasses.create` in the provider project, such as
`fleet.datumapis.com-provider`. Errors show inside the create dialog,
because staff-portal does not share its toast module with plugins.

This started as the MVP from [#17](https://github.com/datum-cloud/fleet/issues/17).
It has no deployment manifests yet.

The provider project is `datum-cloud`, the only one for now. Machines are read
from the `infra-ops` namespace, the
[proposal's](https://github.com/datum-cloud/fleet/blob/main/docs/enhancements/machine-lifecycle/README.md)
example, until the real namespace is decided. Both are constants in
`src/lib/api.ts`. The wire types are in `../shared/fleet-api.ts`, transcribed
by hand from Fleet's Go API types and CRDs; `src/types.ts` maps them to the
list's view.

## Commands

| Command                  | What it does                                            |
| ------------------------ | ------------------------------------------------------- |
| `npm install`            | Install dependencies (Node 26, see `.nvmrc`).           |
| `npm test`               | Run the vitest suite.                                   |
| `npm run typecheck`      | Type-check without emitting.                            |
| `npm run build`          | Build into `dist/`, reading the live Fleet API.         |
| `npm run build:fixtures` | Build with synthetic Machines instead of the API.       |
| `npm run preview`        | Serve `dist/` on http://localhost:7781.                 |
| `npm run dev`            | Vite dev server for the standalone `index.html` only.   |

## Fixture mode

The Fleet CRDs are on staging, but reading them needs a Fleet role. Setting
`VITE_FLEET_FIXTURES=true` at build time makes `useMachines` return the
synthetic Machines in `src/lib/fixtures.ts`: one free, one allocated and one
just enrolled, following Fleet's standalone fixtures and the statuses its
standalone runner writes. Profiles start with the standalone fixture's
profile; profiles you create are kept in memory until the page reloads. The
flag is baked into the build.

## Run inside staff-portal

```bash
# fleet/ui/provider
npm run build:fixtures && npm run preview

# staff-portal, in another terminal (you must be in the staff-users group)
PORTAL_PLUGINS='fleet-datumapis-com=http://localhost:7781' npm run dev
```

If staff-portal's `.env` already sets `PORTAL_PLUGINS_JSON`, add
`{"slug":"fleet-datumapis-com","assets":{"baseURL":"http://localhost:7781"}}`
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
