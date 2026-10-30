import { federation } from '@module-federation/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// The host (cloud-portal) loads this remote via @module-federation/runtime and
// provides react / react-dom / react-router / @tanstack/react-query and the
// datum-ui subpaths as shared singletons, so the plugin renders with the
// host's exact React, router, and query client.
//
// Assets are fetched server-side by the portal's asset proxy and served under
// /api/plugins/<slug>/…, so plain http://localhost during dev is fine and the
// browser never contacts this origin directly.
export default defineConfig({
  server: {
    port: 7780,
    strictPort: true,
    cors: true,
  },
  preview: {
    port: 7780,
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
      name: 'fleet.datumapis.com',
      filename: 'remoteEntry.js',
      manifest: true,
      // Keys map 1:1 to the manifest's `exposedModules` keys / $codeRefs.
      exposes: {
        './ClaimList': './src/pages/claim-list.tsx',
      },
      // requiredVersion:false everywhere: the host's copy always wins, so a
      // host major bump cannot reject the plugin.
      shared: {
        react: { singleton: true, requiredVersion: false },
        'react-dom': { singleton: true, requiredVersion: false },
        'react-router': { singleton: true, requiredVersion: false },
        '@tanstack/react-query': { singleton: true, requiredVersion: false },
        // Only subpaths this plugin imports that the host shares
        // (cloud-portal's federation-host.ts DATUM_UI_SHARED).
        '@datum-cloud/datum-ui/badge': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/breadcrumb': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/button': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/card': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/data-table': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/empty-content': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/icons': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/page-title': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/skeleton': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/utils': { singleton: true, requiredVersion: false },
      },
    }),
  ],
});
