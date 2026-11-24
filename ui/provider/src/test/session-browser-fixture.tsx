import { createRoot } from 'react-dom/client';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import DeviceInventoryList from '../pages/device-inventory-list';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';
import { INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';
import '../preview.css';

let principal: string | null = 'synthetic-staff-a';
let hold = false;
const releases: (() => void)[] = [];
let reads = 0;
let writes = 0;
let denied = false;

window.fetch = async (input, init) => {
  const url = String(input);
  if (!url.startsWith('/api/internal/')) throw new Error('Fixture refuses non-synthetic requests');
  if (url.includes('selfsubjectaccessreviews')) return Response.json({ data: { status: { allowed: true } } });
  if (init?.method && init.method !== 'GET') {
    writes++;
    return Response.json({ error: 'Synthetic fixture never persists writes' }, { status: 503 });
  }
  reads++;
  if (denied) return Response.json({ error: 'Synthetic read denial' }, { status: 403 });
  const kind = url.includes('/facilities') ? 'Facility' : url.includes('/racks') ? 'Rack' : url.includes('/cables') ? 'Cable' : url.includes('/powerfeeds') ? 'PowerFeed' : 'Site';
  const items = url.includes('/devices') ? fixturePhysicalDevices : url.includes('/deviceclaims') ? fixturePhysicalClaims : fixturePhysicalRecords.filter(record => record.kind === kind);
  return Response.json({ data: { items: items.map(record => ({ ...record,
    metadata: { ...record.metadata, uid: `synthetic-${record.metadata.name}`, resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] },
  })) } });
};

const router = createMemoryRouter([{
  id: 'layouts/milo.layout', path: '/',
  loader: async () => {
    if (hold) await new Promise<void>(resolve => releases.push(resolve));
    return { user: principal ? { metadata: { uid: principal } } : null };
  },
  element: <Outlet />,
  children: [{ path: 'plugins/:slug/inventory', element: <DeviceInventoryList /> }],
}], { initialEntries: ['/plugins/metal-datumapis-com/inventory'] });

Object.assign(window, { inventorySessionFixture: {
  hold: (value: boolean) => { hold = value; },
  release: () => releases.shift()?.(),
  principal: (value: string | null) => { principal = value; },
  denied: (value: boolean) => { denied = value; },
  stats: () => ({ reads, writes, pending: releases.length }),
} });

createRoot(document.getElementById('root')!).render(<>
  <p>Synthetic session fixture — real inventory components; no host login or live API.</p>
  <RouterProvider router={router} />
</>);
