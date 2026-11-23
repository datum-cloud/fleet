import { federation } from '@module-federation/vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig, loadEnv } from 'vite';
import { assertFixtureMode } from './scripts/fixture-mode';

// The host (staff-portal) loads this remote and provides react / react-dom /
// react-router / @tanstack/react-query and a few datum-ui subpaths as shared
// singletons. Unlike cloud-portal, staff-portal runs react-router 7 and
// shares only five datum-ui subpaths; the rest (data-table, page-title, …)
// are bundled into the plugin.
//
// Assets are fetched server-side by staff-portal's asset proxy and served
// under /api/plugins/<slug>/…, so the browser never contacts this origin.
//
// Fixture data is only honoured in `--mode fixtures`; see scripts/fixture-mode.ts.
export default defineConfig(({ command, mode, isPreview }) => {
  const fixtures = assertFixtureMode({ command, mode, isPreview, flag: loadEnv(mode, process.cwd(), 'VITE_').VITE_FLEET_FIXTURES });
  return {
    define: { 'import.meta.env.VITE_FLEET_FIXTURES': JSON.stringify(fixtures ? 'true' : 'false') },
    server: {
      port: 7781,
      strictPort: true,
      cors: true,
    },
    preview: {
      port: 7781,
      strictPort: true,
      cors: true,
    },
    build: {
      target: 'esnext',
      minify: false,
    },
    plugins: [
      tailwindcss(),
      react(),
      federation({
        // MUST equal the manifest `name`; the host keys the remote by this id.
        name: 'metal.staff-portal.datumapis.com',
        filename: 'remoteEntry.js',
        manifest: true,
        // Keys map 1:1 to the manifest's `exposedModules` keys / $codeRefs.
        exposes: {
          './CapacityList': './src/pages/capacity-list.tsx',
          './MachineProfileList': './src/pages/machine-profile-list.tsx',
          './MachineClassList': './src/pages/machine-class-list.tsx',
          './DeviceInventoryList': './src/pages/device-inventory-list.tsx',
          './InventoryCreatePage': './src/pages/inventory-create.tsx',
        },
        // requiredVersion:false everywhere: the host's copy always wins, so a
        // patch or major drift between host and plugin cannot fail the load.
        // react/jsx-runtime and react-dom/client are listed so MF does not
        // auto-share them with a strict version check (see compute ui/provider).
        shared: {
          react: { singleton: true, requiredVersion: false },
          'react/jsx-runtime': { singleton: true, requiredVersion: false },
          'react-dom': { singleton: true, requiredVersion: false },
          'react-dom/client': { singleton: true, requiredVersion: false },
          'react-router': { singleton: true, requiredVersion: false },
          '@tanstack/react-query': { singleton: true, requiredVersion: false },
          // Every datum-ui subpath staff-portal's federation-host.ts shares.
          '@datum-cloud/datum-ui/badge': { singleton: true, requiredVersion: false },
          '@datum-cloud/datum-ui/button': { singleton: true, requiredVersion: false },
          '@datum-cloud/datum-ui/card': { singleton: true, requiredVersion: false },
          '@datum-cloud/datum-ui/icons': { singleton: true, requiredVersion: false },
          '@datum-cloud/datum-ui/skeleton': { singleton: true, requiredVersion: false },
        },
      }),
    ],
  };
});
