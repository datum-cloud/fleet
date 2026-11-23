import MachineClassList from './pages/machine-class-list';
import CapacityList from './pages/capacity-list';
import MachineProfileList from './pages/machine-profile-list';
import DeviceInventoryList from './pages/device-inventory-list';
import InventoryCreatePage from './pages/inventory-create';
import './preview.css';
import { InventoryPreviewContext } from './components/inventory-session-boundary';
import { applyPreviewTheme, initialPreviewTheme, PreviewThemeToggle } from './preview-theme';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, Navigate, RouterProvider } from 'react-router';

// Standalone harness (index.html). The portal never loads this file. It uses
// the browser's URL so every view has a stable, bookmarkable address under
// the same /plugins/<slug>/ paths staff-portal mounts.
const SLUG = 'metal-datumapis-com';
const fixtures = import.meta.env.VITE_FLEET_FIXTURES === 'true';

applyPreviewTheme(initialPreviewTheme());

const queryClient = new QueryClient();
const router = createBrowserRouter([
  { path: '/', element: <Navigate to={`/plugins/${SLUG}${fixtures ? '/inventory' : ''}`} replace /> },
  { path: '/plugins/:slug', element: <CapacityList /> },
  { path: '/plugins/:slug/profiles', element: <MachineProfileList /> },
  { path: '/plugins/:slug/classes', element: <MachineClassList /> },
  { path: '/plugins/:slug/inventory', element: <DeviceInventoryList /> },
  { path: '/plugins/:slug/inventory/create/:kind', element: <InventoryCreatePage /> },
]);

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <div className="fleet-preview">
        <div className="fleet-preview-notice">
          <p>Local plugin preview · {fixtures ? 'Synthetic example data — not live inventory' : 'Requires the staff-portal API proxy'}</p>
          <PreviewThemeToggle />
        </div>
        <InventoryPreviewContext.Provider value={true}><RouterProvider router={router} /></InventoryPreviewContext.Provider>
      </div>
    </QueryClientProvider>
  </StrictMode>
);
