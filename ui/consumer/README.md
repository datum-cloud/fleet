# Fleet consumer portal plugin

A [cloud-portal](https://github.com/datum-cloud/cloud-portal) plugin for a
project's machines. It is a Module Federation remote: the portal fetches
`plugin-manifest.json` and `remoteEntry.js` server-side and mounts the pages at
`/project/:projectId/services/<slug>/`. The **Machines** nav item opens two
tabs:

- **Machines** (the mount root) lists the project's MachineClaims. Each name
  opens the claim's page (`:claimName`): the request, the allocated machine's
  hardware, network interfaces when requested, and every condition with its
  reason. A missing condition shows as Unknown.
- **Catalog** (`catalog`) lists the MachineClasses this project can claim from
  and the profiles each allows. A class published from another project (with
  `spec.source`) is read from `status.resolvedClass` and stays pending until
  Fleet resolves it; the project's own class is read from its spec. Each
  published profile has a **Deploy** button.

Deploying creates a MachineClaim in the `default` namespace for the chosen
class and profile, in `us-central-1` (the only location offered for now),
powered off. It allocates a machine only; installing an OS is a separate
Installation this plugin does not offer yet. Creating a claim reserves
nothing: it lists as Pending until Fleet binds a machine.

**Release**, on a claim's page or as a row action in the list, deletes the claim
after the user types its name.
Fleet's finalizer keeps the claim, listed as Releasing, until cleanup finishes;
then it disappears and the page shows it as not found. Release is hidden on a
claim that is already releasing. In fixture mode a released claim stays
Releasing until the page reloads.

This started as the MVP from [#16](https://github.com/datum-cloud/fleet/issues/16).
It has no deployment manifests yet.

Consumers see claims, never Machines: allocated hardware comes from
`MachineClaim.status`, following the
[machine lifecycle proposal](https://github.com/datum-cloud/fleet/blob/main/docs/enhancements/machine-lifecycle/README.md).
The wire types are in `../shared/fleet-api.ts`, transcribed by hand from
Fleet's Go API types and CRDs; `src/types.ts` maps them to the list's view.

## Commands

| Command                  | What it does                                            |
| ------------------------ | ------------------------------------------------------- |
| `npm install`            | Install dependencies (Node 26, see `.nvmrc`).           |
| `npm test`               | Run the vitest suite.                                   |
| `npm run typecheck`      | Type-check without emitting.                            |
| `npm run build`          | Build into `dist/`, reading the live Fleet API.         |
| `npm run build:fixtures` | Build with synthetic claims instead of the API.         |
| `npm run preview`        | Serve `dist/` on http://localhost:7780.                 |
| `npm run dev`            | Vite dev server for the standalone `index.html` only.   |

## Fixture mode

The Fleet CRDs are on staging, but reading them needs a Fleet role. Setting
`VITE_FLEET_FIXTURES=true` at build time makes `useMachineClaims` return the
synthetic claims in `src/lib/fixtures.ts` instead of calling the project
control plane. Claims deployed in fixture mode are kept in memory until the
page reloads. They follow Fleet's standalone fixtures and the statuses its
standalone runner writes. The flag is baked into the build, so rebuild without
it to use the real API.

## Run inside cloud-portal

Serve the built plugin, not the Vite dev server: dev-mode remote entries use
host-absolute URLs that break behind the portal's `/api/plugins/<slug>/` proxy.

```bash
# fleet/ui/consumer
npm run build:fixtures && npm run preview

# cloud-portal, in another terminal
PORTAL_PLUGINS='fleet-datumapis-com=http://localhost:7780' AUTH_DEV_TOKEN_EXCHANGE=1 bun run dev
```

If cloud-portal's `.env` already sets `PORTAL_PLUGINS_JSON` for other plugins,
add `{"slug":"fleet-datumapis-com","assets":{"baseURL":"http://localhost:7780"}}`
to that array instead. The slug follows the services-operator convention
(service name with dots as dashes) and assumes the service is registered as
`fleet.datumapis.com`; the plugin code does not depend on it.

Open any project; **Machines** appears in the Build section of the project
navigation. Plugins loaded through `PORTAL_PLUGINS` skip the entitlement gate
and show a dev badge.

## Conventions

- The plugin has no CSS build. Use datum-ui components and Tailwind classes the
  host already ships.
- Import only datum-ui subpaths the host shares (cloud-portal's
  `app/modules/plugins/client/federation-host.ts`) and list each one in
  `vite.config.ts` `shared`.
- The federation `name`, the manifest `name`, and the `exposes` /
  `exposedModules` keys must stay in sync.
