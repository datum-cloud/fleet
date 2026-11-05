import MachineList from './pages/machine-list';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router';

const queryClient = new QueryClient();

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <div style={{ maxWidth: 1200, margin: '2rem auto', padding: '0 1rem', fontFamily: 'system-ui' }}>
        <p style={{ opacity: 0.6 }}>
          Standalone preview. staff-portal loads this plugin through
          <code> /plugin-manifest.json</code> and <code>/remoteEntry.js</code>, not this page.
          Without the staff-portal proxy the list shows its error state unless
          <code> VITE_FLEET_FIXTURES=true</code>.
        </p>
        <MemoryRouter initialEntries={['/plugins/fleet-datumapis-com/machines']}>
          <Routes>
            <Route path="/plugins/:slug/machines" element={<MachineList />} />
          </Routes>
        </MemoryRouter>
      </div>
    </QueryClientProvider>
  </StrictMode>
);
