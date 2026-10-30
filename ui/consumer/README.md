# Fleet consumer portal plugin

A [cloud-portal](https://github.com/datum-cloud/cloud-portal) plugin that lists
the MachineClaims in the current project. It is a Module Federation remote: the
portal fetches `plugin-manifest.json` and `remoteEntry.js` server-side and
mounts the pages at `/project/:projectId/services/<slug>/`.

This is the MVP from [#16](https://github.com/datum-cloud/fleet/issues/16). It
is read-only and has no deployment manifests yet.

Consumers see claims, never Machines: allocated hardware comes from
`MachineClaim.status`, following the
[machine lifecycle proposal](https://github.com/datum-cloud/fleet/blob/docs/first-machine-lifecycle-milestone/docs/enhancements/machine-lifecycle/README.md).
The types in `src/types.ts` are a hand-written subset of that provisional API.

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

The Fleet API is not served anywhere yet. Setting `VITE_FLEET_FIXTURES=true`
at build time makes `useMachineClaims` return the synthetic claims in
`src/lib/fixtures.ts` instead of calling the project control plane. The flag is
baked into the build, so rebuild without it to use the real API.

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
