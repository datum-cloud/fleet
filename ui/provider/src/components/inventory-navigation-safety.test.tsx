import { findEnabledButton } from '../test/enabled-button';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { InventorySessionBoundary } from './inventory-session-boundary';
import DeviceInventoryList from '../pages/device-inventory-list';
import { inventoryPayload, INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';

const values = { name: 'independent-review', namespace: 'default', displayName: 'Original', facility: '', height: '' };
const original = { ...inventoryPayload('Facility', values), metadata: { name: values.name, namespace: 'default', uid: 'synthetic-only', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const response = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => ({ data, error: 'PRIVATE DIAGNOSTIC' }) });
beforeEach(() => vi.stubGlobal('confirm', vi.fn(() => true)));

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('real directory category Link followed by fresh create remains usable', async () => {
  const writes: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    if (init.method !== 'GET') { writes.push(init.method!); return response(original); }
    return response({ items: [] });
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'staff-a' } } }), element: <Outlet />, children: [{ path: 'inventory', element: <DeviceInventoryList /> }] }], { initialEntries: ['/inventory'] });
  render(<RouterProvider router={router} />);
  fireEvent.click(await screen.findByRole('tab', { name: 'Locations' }));
  fireEvent.click(await screen.findByRole('tab', { name: 'Facilities' }));
  await open(false);
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  expect(await screen.findByRole('status')).not.toHaveTextContent('No write was dispatched');
  expect(writes).toEqual(['POST']);
});

it.each([false, true])('same-principal query navigation retains usable management authority: edit=%s', async editing => {
  const writes: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    if (init.method !== 'GET') writes.push(init.method!);
    return response(original);
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'staff-a' } } }), element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><InventoryManagement kind="Facility" record={editing ? original : undefined} facilities={[]} /></InventorySessionBoundary> }] }]);
  render(<RouterProvider router={router} />);
  await findEnabledButton({ name: editing ? 'Edit display name' : 'Create facility' });
  await act(async () => { await router.navigate('/?kind=Facility'); });
  await open(editing);
  fireEvent.click(screen.getByRole('button', { name: editing ? 'Save changes' : 'Create record' }));
  await screen.findByRole('status');
  expect(writes).toEqual([editing ? 'PATCH' : 'POST']);
});

it.each([false, true].flatMap(editing => ['navigation', 'revalidation'].map(transition => ({ editing, transition }))))('$transition revokes pending work while admitting a fresh form: edit=$editing', async ({ editing, transition }) => {
  let reviews = 0;
  let release: ((value: unknown) => void) | undefined;
  const writes: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) {
      if (++reviews === 2) return new Promise(resolve => { release = resolve; });
      return response({ status: { allowed: true } });
    }
    if (init.method !== 'GET') writes.push(init.method!);
    return response(original);
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'staff-a' } } }), element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><InventoryManagement kind="Facility" record={editing ? original : undefined} facilities={[]} /></InventorySessionBoundary> }] }]);
  render(<RouterProvider router={router} />);
  await open(editing);
  fireEvent.click(screen.getByRole('button', { name: editing ? 'Save changes' : 'Create record' }));
  await waitFor(() => expect(release).toBeDefined());
  await act(async () => {
    if (transition === 'revalidation') await router.revalidate();
    else await router.navigate('/?kind=Facility&name=independent-review');
  });
  await open(editing);
  await act(async () => release!(response({ status: { allowed: true } })));
  expect(writes).toEqual([]);
  expect(screen.queryByText(/Display name saved\.|Facility created\./)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: editing ? 'Save changes' : 'Create record' }));
  await screen.findByText(editing ? 'Display name saved.' : 'Facility created.');
  expect(writes).toEqual([editing ? 'PATCH' : 'POST']);
});

async function open(editing: boolean) {
  const opener = await findEnabledButton({ name: editing ? 'Edit display name' : 'Create facility' });
  fireEvent.click(opener);
  if (!editing) fireEvent.change(screen.getByLabelText('Record name'), { target: { value: values.name } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Independent safe input' } });
}

it.each(['create-review', 'edit-review', 'edit-get', 'create-dispatch', 'edit-dispatch'])('A-B-A session transitions fence old %s while new form remains usable', async mode => {
  let principal = 'staff-a';
  let reviews = 0;
  let release: ((value: unknown) => void) | undefined;
  let intercepted = false;
  const writes: string[] = [];
  const editing = mode.startsWith('edit');
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    const review = url.includes('selfsubjectaccessreviews');
    if (review) reviews++;
    const write = !review && init.method !== 'GET';
    if (write) writes.push(principal);
    if (!intercepted && ((mode.endsWith('review') && review && reviews === 2) || (mode.endsWith('get') && init.method === 'GET') || (mode.endsWith('dispatch') && write))) {
      intercepted = true;
      return new Promise(resolve => { release = resolve; });
    }
    return response(review ? { status: { allowed: true } } : original);
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: principal } } }), element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><InventoryManagement kind="Facility" record={editing ? original : undefined} facilities={[]} /></InventorySessionBoundary> }] }]);
  render(<RouterProvider router={router} />);
  await open(editing);
  fireEvent.click(screen.getByRole('button', { name: editing ? 'Save changes' : 'Create record' }));
  await waitFor(() => expect(release).toBeDefined());
  expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled();
  principal = 'staff-b';
  await act(async () => { await router.revalidate(); });
  principal = 'staff-a';
  await act(async () => { await router.revalidate(); });
  await open(editing);
  await act(async () => release!(response(mode.endsWith('review') ? { status: { allowed: true } } : original)));
  expect(writes).toEqual(mode.endsWith('dispatch') ? ['staff-a'] : []);
  expect(screen.queryByText(/Display name saved\.|Facility created\./)).not.toBeInTheDocument();
  expect(screen.getByLabelText('Display name')).toHaveValue('Independent safe input');
  fireEvent.click(screen.getByRole('button', { name: editing ? 'Save changes' : 'Create record' }));
  await screen.findByText(editing ? 'Display name saved.' : 'Facility created.');
  expect(writes).toHaveLength(mode.endsWith('dispatch') ? 2 : 1);
});

it('closing a conflicted edit and reopening explicitly takes the new baseline', async () => {
  let current = original;
  const patches: Array<Array<{ path: string; value: unknown }>> = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    if (init.method === 'PATCH') patches.push(JSON.parse(String(init.body)));
    return response(current);
  }));
  const client = new QueryClient();
  const content = () => <QueryClientProvider client={client}><InventoryManagement kind="Facility" record={current} facilities={[]} /></QueryClientProvider>;
  const mounted = render(content());
  await open(true);
  current = { ...original, metadata: { ...original.metadata, resourceVersion: '2' }, spec: { ...original.spec, displayName: 'Other operator' } };
  mounted.rerender(content());
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  await screen.findByText(/already exists, changed/);
  expect(patches).toEqual([]);
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
  fireEvent.click(screen.getByRole('button', { name: 'Edit display name' }));
  expect(screen.getByLabelText('Display name')).toHaveValue('Other operator');
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Reviewed replacement' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  await screen.findByText('Display name saved.');
  expect(patches[0].find(operation => operation.path === '/metadata/resourceVersion')?.value).toBe('2');
});

it.each([401, 403, 404, 503])('submit-time SSAR HTTP %s fails closed for both create and edit', async status => {
  for (const editing of [false, true]) {
    let reviews = 0;
    const writes: string[] = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
      if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } }, ++reviews === 1 ? 200 : status);
      writes.push(init.method!);
      return response(original);
    }));
    render(<QueryClientProvider client={new QueryClient()}><InventoryManagement kind="Facility" record={editing ? original : undefined} facilities={[]} /></QueryClientProvider>);
    await open(editing);
    fireEvent.click(screen.getByRole('button', { name: editing ? 'Save changes' : 'Create record' }));
    await screen.findByRole('status');
    expect(writes).toEqual([]);
    expect(screen.getByLabelText('Display name')).toHaveValue('Independent safe input');
    expect(screen.getByRole('button', { name: editing ? 'Save changes' : 'Create record' })).toBeDisabled();
    expect(screen.queryByText('PRIVATE DIAGNOSTIC')).not.toBeInTheDocument();
    cleanup();
  }
});
