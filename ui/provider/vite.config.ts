import { federation } from '@module-federation/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// The host (staff-portal) loads this remote and provides react / react-dom /
// react-router / @tanstack/react-query and a few datum-ui subpaths as shared
// singletons. Unlike cloud-portal, staff-portal runs react-router 7 and
// shares only five datum-ui subpaths; the rest (data-table, page-title, …)
// are bundled into the plugin.
//
// Assets are fetched server-side by staff-portal's asset proxy and served
// under /api/plugins/<slug>/…, so the browser never contacts this origin.
export default defineConfig({
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
    react(),
    federation({
      // MUST equal the manifest `name`; the host keys the remote by this id.
      name: 'fleet.staff-portal.datumapis.com',
      filename: 'remoteEntry.js',
      manifest: true,
      // Keys map 1:1 to the manifest's `exposedModules` keys / $codeRefs.
      exposes: {
        './MachineList': './src/pages/machine-list.tsx',
        './MachineProfileList': './src/pages/machine-profile-list.tsx',
        './MachineClassList': './src/pages/machine-class-list.tsx',
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
});
