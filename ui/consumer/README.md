# Bare Metal consumer portal plugin

A [cloud-portal](https://github.com/datum-cloud/cloud-portal) plugin for a
project's machines. It is a Module Federation remote: the portal fetches
`plugin-manifest.json` and `remoteEntry.js` server-side and mounts the pages at
`/project/:projectId/services/<slug>/`. The **Machines** nav item opens the
list; the layout follows network-services-operator's NLB pages.

- **Machines** (the mount root) lists the project's Machines with their status,
  profile, allocated hardware and power. Under the table, **Available to
  deploy** shows a card for each profile the project can deploy, from the
  MachineClasses it can use; a class that isn't usable yet says why. A class
  published from another project (with `spec.source`) is read from
  `status.resolvedClass` and stays unusable until Fleet resolves it; the
  project's own class is read from its spec. A project with no Machines gets a
  hero instead, with the profiles as starter cards.
- Each name opens the Machine's page (`:machineName`): what was requested, the
  hardware Fleet allocated and the network, then the four lifecycle conditions
  (Accepted, Bound, NetworkReady, Ready) as steps. A condition Fleet hasn't
  written yet shows as a step still to come.

Deploying creates a Machine in the `default` namespace for the chosen
class and profile, in `us-central-1` (the only location offered for now),
powered off. It allocates hardware only; installing an OS is a separate
Installation this plugin does not offer yet. Creating a Machine reserves
nothing: it lists as Pending until Fleet binds capacity to it.

**Deploy machine** (or a profile card, which picks the profile) opens a dialog
that asks for a profile and a name.

**Release**, at the top of a Machine's page or as a row action in the list,
deletes the Machine after the user types its name.
Fleet's finalizer keeps the Machine, listed as Releasing, until cleanup finishes;
then it disappears and the page shows it as not found. Release is hidden on a
Machine that is already releasing. In fixture mode a released Machine stays
Releasing until the page reloads.

This started as the MVP from [#16](https://github.com/datum-cloud/fleet/issues/16).
It is deployed by `config/components/consumer-ui-plugin` and registered through
the `metal` ServiceConfiguration's `userInterface`, with
`visibility.entitlement: Required`: a project sees **Machines** only with an
Active entitlement to the service.

Consumers see their own Machines, never the provider's MachineCapacity or the
private MachineAllocation between them: allocated hardware comes from
`Machine.status`, following the
[machine lifecycle proposal](https://github.com/datum-cloud/fleet/blob/main/docs/enhancements/machine-lifecycle/README.md).
The wire types are in `../shared/fleet-api.ts`, transcribed by hand from
Fleet's Go API types and CRDs; `src/types.ts` maps them to the list's view.

## Commands

| Command                  | What it does                                            |
| ------------------------ | ------------------------------------------------------- |
| `pnpm install`           | Install dependencies (Node 26, see `.nvmrc`).           |
| `pnpm test`              | Run the vitest suite.                                   |
| `pnpm typecheck`         | Type-check without emitting.                            |
| `pnpm build`             | Build into `dist/`, reading the live Fleet API.         |
| `pnpm build:fixtures`    | Build with synthetic Machines instead of the API.       |
| `pnpm preview`           | Serve `dist/` on http://localhost:7780.                 |
| `pnpm dev`               | Vite dev server for the standalone `index.html` only.   |

## Fixture mode

The Fleet CRDs are on staging, but reading them needs a Bare Metal role. Setting
`VITE_FLEET_FIXTURES=true` at build time makes `useMachines` return the
synthetic Machines in `src/lib/fixtures.ts` instead of calling the project
control plane. Machines deployed in fixture mode are kept in memory until the
page reloads. They follow Fleet's standalone fixtures and the statuses its
standalone runner writes. The flag is baked into the build, so rebuild without
it to use the real API.

## Run inside cloud-portal

Serve the built plugin, not the Vite dev server: dev-mode remote entries use
host-absolute URLs that break behind the portal's `/api/plugins/<slug>/` proxy.

```bash
# fleet/ui/consumer
pnpm build:fixtures && pnpm preview

# cloud-portal, in another terminal
PORTAL_PLUGINS='metal-datumapis-com=http://localhost:7780' AUTH_DEV_TOKEN_EXCHANGE=1 bun run dev
```

If cloud-portal's `.env` already sets `PORTAL_PLUGINS_JSON` for other plugins,
add `{"slug":"metal-datumapis-com","assets":{"baseURL":"http://localhost:7780"}}`
to that array instead. The slug follows the services-operator convention
(service name with dots as dashes) and assumes the service is registered as
`metal.datumapis.com` (Bare Metal); the plugin code does not depend on it.

Open any project; **Machines** appears in the Build section of the project
navigation. Plugins loaded through `PORTAL_PLUGINS` skip the entitlement gate
and show a dev badge.

## Conventions

- The plugin has no CSS build. Use datum-ui components and Tailwind classes the
  host already ships; the few extra styles live inline in
  `src/components/fleet-styles.tsx`, built on the host theme's variables.
- Import only datum-ui subpaths the host shares (cloud-portal's
  `app/modules/plugins/client/federation-host.ts`) and list each one in
  `vite.config.ts` `shared`.
- The federation `name`, the manifest `name`, and the `exposes` /
  `exposedModules` keys must stay in sync.
