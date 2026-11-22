import MachineClassList from './pages/machine-class-list';
import CapacityList from './pages/capacity-list';
import MachineProfileList from './pages/machine-profile-list';
import DeviceInventoryList from './pages/device-inventory-list';
import DeviceClaimDetail from './pages/device-claim-detail';
import InventoryCreatePage from './pages/inventory-create';
import './preview.css';
import { InventoryPreviewContext } from './components/inventory-session-boundary';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createMemoryRouter, RouterProvider } from 'react-router';

const queryClient = new QueryClient();
const router = createMemoryRouter([
  { path: '/plugins/:slug', element: <CapacityList /> },
  { path: '/plugins/:slug/profiles', element: <MachineProfileList /> },
  { path: '/plugins/:slug/classes', element: <MachineClassList /> },
  { path: '/plugins/:slug/inventory', element: <DeviceInventoryList /> },
  { path: '/plugins/:slug/inventory/create/:kind', element: <InventoryCreatePage /> },
  { path: '/plugins/:slug/inventory/:namespace/:claim', element: <DeviceClaimDetail /> },
], { initialEntries: ['/plugins/metal-datumapis-com'] });

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <div className="fleet-preview">
        <p className="fleet-preview-notice">Local plugin preview · {import.meta.env.VITE_FLEET_FIXTURES === 'true' ? 'Synthetic example data — not live inventory' : 'Requires the staff-portal API proxy'}</p>
        <InventoryPreviewContext.Provider value={true}><RouterProvider router={router} /></InventoryPreviewContext.Provider>
      </div>
    </QueryClientProvider>
  </StrictMode>
);
