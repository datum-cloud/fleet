import MachineClassList from './pages/machine-class-list';
import CapacityList from './pages/capacity-list';
import MachineProfileList from './pages/machine-profile-list';
import DeviceInventoryList from './pages/device-inventory-list';
import DeviceClaimDetail from './pages/device-claim-detail';
import './preview.css';
import { InventoryPreviewContext } from './components/inventory-session-boundary';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router';

const queryClient = new QueryClient();

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <div className="fleet-preview">
        <p className="fleet-preview-notice">Local plugin preview · {import.meta.env.VITE_FLEET_FIXTURES === 'true' ? 'Synthetic example data — not live inventory' : 'Requires the staff-portal API proxy'}</p>
        <MemoryRouter initialEntries={['/plugins/metal-datumapis-com']}>
          <InventoryPreviewContext.Provider value={true}>
          <Routes>
            <Route path="/plugins/:slug" element={<CapacityList />} />
            <Route path="/plugins/:slug/profiles" element={<MachineProfileList />} />
            <Route path="/plugins/:slug/classes" element={<MachineClassList />} />
            <Route path="/plugins/:slug/inventory" element={<DeviceInventoryList />} />
            <Route path="/plugins/:slug/inventory/:namespace/:claim" element={<DeviceClaimDetail />} />
          </Routes>
          </InventoryPreviewContext.Provider>
        </MemoryRouter>
      </div>
    </QueryClientProvider>
  </StrictMode>
);
