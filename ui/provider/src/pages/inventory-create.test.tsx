import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
import InventoryCreatePage from './inventory-create';
import DeviceInventoryList from './device-inventory-list';
import { useInventory } from '../lib/api';
import { useInventoryPermission, createInventoryRecord } from '../lib/inventory-management';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';
import { toInventory } from '../inventory';
import manifest from '../../public/plugin-manifest.json';

vi.mock('../lib/api', async () => ({ ...await vi.importActual('../lib/api'), useInventory: vi.fn() }));
vi.mock('../lib/inventory-management', async () => ({ ...await vi.importActual('../lib/inventory-management'), useInventoryPermission: vi.fn(), createInventoryRecord: vi.fn() }));
const inventory = toInventory(fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords);
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(useInventory).mockReturnValue({ data: inventory, isLoading: false, error: null, refetch: vi.fn() } as never);
  vi.mocked(useInventoryPermission).mockReturnValue({ data: true, isFetching: false, error: null } as never);
  vi.stubGlobal('confirm', vi.fn(() => false));
  vi.stubGlobal('fetch', vi.fn(() => { throw new Error('Unexpected network call'); }));
});
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

function host(path: string) {
  let principal: string | null = 'staff-a';
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: principal ? { metadata: { uid: principal } } : null }), element: <Outlet />, children: [
    { path: 'plugins/:slug/inventory', element: <DeviceInventoryList /> },
    { path: 'plugins/:slug/inventory/create/:kind', element: <InventoryCreatePage /> },
  ] }], { initialEntries: [`/plugins/metal/inventory${path}`] });
  render(<RouterProvider router={router} />);
  return { router, principal: (next: string | null) => { principal = next; } };
}

it.each([['Device', 'device'], ['Cable', 'cable'], ['DeviceClaim', 'installation-plan']])('routes %s creation without expanding the list and preserves filters through history', async (kind, route) => {
  const view = host(`?kind=${kind}&q=synthetic&model=Example&location=default%2Fexample-facility`);
  fireEvent.click(await screen.findByRole('button', { name: `Create ${route.replace('-', ' ')}` }));
  await screen.findByLabelText('Record name');
  expect(view.router.state.location.pathname).toBe(`/plugins/metal/inventory/create/${route}`);
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  // The directory list is not shown; only the form's own lane editor is a table.
  expect(screen.queryAllByRole('table').filter(table => !table.closest('form'))).toHaveLength(0);
  expect(screen.getByRole('heading', { name: `Create ${route.replace('-', ' ')}` })).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Private draft' } });
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
  expect(screen.getByLabelText('Display name')).toHaveValue('Private draft');
  vi.mocked(window.confirm).mockReturnValue(true);
  fireEvent.click(screen.getByRole('link', { name: 'Back to inventory' }));
  await screen.findByRole('searchbox');
  expect(screen.getByRole('searchbox')).toHaveValue('synthetic');
  expect(new URLSearchParams(view.router.state.location.search).get('model')).toBe('Example');
  expect(new URLSearchParams(view.router.state.location.search).get('location')).toBe('default/example-facility');
  await act(async () => { await view.router.navigate(-1); });
  await screen.findByLabelText('Display name');
  expect(screen.getByLabelText('Display name')).toHaveValue('');
  await act(async () => { await view.router.navigate(1); });
  await screen.findByRole('searchbox');
  expect(createInventoryRecord).not.toHaveBeenCalled();
});

it.each(['device', 'cable', 'installation-plan'])('opens a direct %s task with explicit back/cancel and no namespace input', async kind => {
  host(`/create/${kind}?namespace=default`);
  await screen.findByLabelText('Record name');
  expect(screen.getByRole('link', { name: 'Back to inventory' })).toHaveAttribute('href', expect.stringMatching(/^\/plugins\/metal\/inventory\?kind=/));
  expect(screen.getByRole('button', { name: 'Cancel' })).toBeEnabled();
  expect(screen.queryByLabelText(/namespace/i)).not.toBeInTheDocument();
  expect(screen.getByRole('tab', { name: kind === 'installation-plan' ? 'Installation plans' : 'Equipment' })).toHaveAttribute('aria-selected', 'true');
});

it('uses the task kind rather than an unrelated query kind and guards dirty group navigation', async () => {
  const view = host('/create/cable?kind=Rack&q=retained');
  await screen.findByLabelText('Record name');
  expect(screen.getByRole('tab', { name: 'Equipment' })).toHaveAttribute('aria-selected', 'true');
  expect(screen.getByRole('tab', { name: 'Cables' })).toHaveAttribute('aria-selected', 'true');
  expect(screen.getByRole('link', { name: 'Cables' })).toHaveAttribute('href', '/plugins/metal/inventory?kind=Cable&q=retained');
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Private task draft' } });
  fireEvent.click(screen.getByRole('tab', { name: 'Locations' }));
  expect(window.confirm).toHaveBeenCalled();
  expect(screen.getByLabelText('Display name')).toHaveValue('Private task draft');
  vi.mocked(window.confirm).mockReturnValue(true);
  fireEvent.click(screen.getByRole('tab', { name: 'Locations' }));
  await screen.findByRole('searchbox', { name: 'Search operational sites' });
  expect(view.router.state.location.search).toBe('?kind=Site');
  expect(createInventoryRecord).not.toHaveBeenCalled();
});

it.each(['device', 'cable', 'installation-plan', 'manufacturer', 'facility'])('rejects crafted foreign namespace on %s before loading any inventory', async kind => {
  host(`/create/${kind}?namespace=foreign&returnTo=https://example.invalid`);
  await screen.findByText(/only supports default-namespace/);
  expect(useInventory).not.toHaveBeenCalled();
  expect(useInventoryPermission).not.toHaveBeenCalled();
  expect(screen.queryByRole('button', { name: 'Create record' })).not.toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Back to inventory' }).getAttribute('href')).not.toContain('example.invalid');
});

it.each(['__proto__', 'constructor', 'manufacturer', 'inventory'])('rejects unsupported task %s without a catalog editor or network reads', async kind => {
  host(`/create/${kind}`);
  await screen.findByRole('heading', { name: 'Unknown inventory task' });
  expect(useInventory).not.toHaveBeenCalled();
  expect(screen.getByRole('link', { name: 'Back to inventory' })).toHaveAttribute('href', '/plugins/metal/inventory?kind=Device');
});

it.each(['principal', 'logout', 'hidden', 'pagehide'] as const)('retires private task drafts on %s without transferring late writes', async transition => {
  const view = host('/create/device');
  await screen.findByLabelText('Display name');
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Private draft' } });
  if (transition === 'principal' || transition === 'logout') {
    view.principal(transition === 'logout' ? null : 'staff-b');
    await act(async () => { await view.router.revalidate(); });
  } else if (transition === 'hidden') {
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
    fireEvent(document, new Event('visibilitychange'));
    expect(screen.queryByLabelText('Display name')).not.toBeInTheDocument();
    visibility.mockReturnValue('visible'); fireEvent(document, new Event('visibilitychange'));
  } else { fireEvent(window, new Event('pagehide')); fireEvent(window, new Event('pageshow')); }
  if (transition === 'logout') expect(screen.queryByLabelText('Display name')).not.toBeInTheDocument();
  else await waitFor(() => expect(screen.getByLabelText('Display name')).toHaveValue(''));
  expect(createInventoryRecord).not.toHaveBeenCalled();
});

it('registers the create page and no per-claim route', () => {
  const paths = manifest.extensions.filter(extension => extension.type === 'portal.page/platform').map(extension => extension.properties.path);
  expect(manifest.exposedModules.InventoryCreatePage).toBe('./src/pages/inventory-create.tsx');
  expect(paths).toContain('inventory/create/:kind');
  expect(paths).not.toContain('inventory/:namespace/:claim');
});
