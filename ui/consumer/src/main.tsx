import ClaimList from './pages/claim-list';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router';

const queryClient = new QueryClient();

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <div style={{ maxWidth: 960, margin: '2rem auto', padding: '0 1rem', fontFamily: 'system-ui' }}>
        <p style={{ opacity: 0.6 }}>
          Standalone preview. The portal loads this plugin through
          <code> /plugin-manifest.json</code> and <code>/remoteEntry.js</code>, not this page.
          Without the portal proxy the list shows its error state unless
          <code> VITE_FLEET_FIXTURES=true</code>.
        </p>
        <MemoryRouter initialEntries={['/project/demo-project/services/fleet']}>
          <Routes>
            <Route path="/project/:projectId/services/:serviceSlug" element={<ClaimList />} />
          </Routes>
        </MemoryRouter>
      </div>
    </QueryClientProvider>
  </StrictMode>
);
