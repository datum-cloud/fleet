import { findEnabledButton } from '../test/enabled-button';
import { StrictMode } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { InventorySessionBoundary } from './inventory-session-boundary';
import { inventoryPayload, INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';

const values = { name: 'fresh-test', namespace: 'default', displayName: 'Fresh record', facility: 'fresh-test', height: '' };
const original = { ...inventoryPayload('Facility', values), metadata: { name: values.name, namespace: 'default', uid: 'synthetic', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const response = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ data }) });
beforeEach(() => vi.stubGlobal('confirm', vi.fn(() => true)));

afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

async function open(editing: boolean, rack = false) {
  fireEvent.click(await findEnabledButton({ name: editing ? 'Edit display name' : rack ? 'Create rack' : 'Create facility' }));
  if (!editing) fireEvent.change(screen.getByLabelText('Record name'), { target: { value: values.name } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'New input' } });
  if (rack) fireEvent.change(screen.getByLabelText('Facility'), { target: { value: values.name } });
}

it.each(['same-url', 'replace', 'back-forward', 'revalidate', 'hidden-return', 'pagehide-return'].flatMap(navigation => [false, true].flatMap(editing => [false, true].map(strict => ({ navigation, editing, strict })))) )('fresh authority after $navigation edit=$editing strict=$strict', async ({ navigation, editing, strict }) => {
  const writes: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    if (init.method !== 'GET') writes.push(init.method!);
    return response(original);
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'staff-a' } } }), element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><InventoryManagement kind="Facility" record={editing ? original : undefined} facilities={[]} /></InventorySessionBoundary> }] }]);
  render(strict ? <StrictMode><RouterProvider router={router} /></StrictMode> : <RouterProvider router={router} />);
  await findEnabledButton({ name: editing ? 'Edit display name' : 'Create facility' });
  await act(async () => {
    if (navigation === 'revalidate') await router.revalidate();
    else if (navigation === 'hidden-return') {
      const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
      document.dispatchEvent(new Event('visibilitychange'));
      visibility.mockReturnValue('visible');
      document.dispatchEvent(new Event('visibilitychange'));
    }
    else if (navigation === 'pagehide-return') {
      window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }));
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }));
    }
    else if (navigation === 'back-forward') { await router.navigate('/?kind=Facility'); await router.navigate(-1); await router.navigate(1); }
    else await router.navigate('/', { replace: navigation === 'replace' });
  });
  await open(editing);
  fireEvent.click(screen.getByRole('button', { name: editing ? 'Save changes' : 'Create record' }));
  await waitFor(() => expect(writes).toEqual([editing ? 'PATCH' : 'POST']));
  await screen.findByText(editing ? 'Display name saved.' : 'Facility record created.');
});

it.each(['create-review', 'rack-get', 'edit-review', 'edit-get', 'create-dispatch', 'edit-dispatch'])('same-url navigation retires %s and permits only new work', async mode => {
  const editing = mode.startsWith('edit');
  const rack = mode.startsWith('rack');
  let reviews = 0;
  let release: ((value: unknown) => void) | undefined;
  let intercepted = false;
  const writes: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    const review = url.includes('selfsubjectaccessreviews');
    if (review) reviews++;
    const write = !review && init.method !== 'GET';
    if (write) writes.push(init.method!);
    if (!intercepted && ((mode.endsWith('review') && review && reviews === 2) || (mode.endsWith('get') && init.method === 'GET') || (mode.endsWith('dispatch') && write))) {
      intercepted = true;
      return new Promise(resolve => { release = resolve; });
    }
    return response(review ? { status: { allowed: true } } : original);
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'staff-a' } } }), element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><InventoryManagement kind={rack ? 'Rack' : 'Facility'} record={editing ? original : undefined} facilities={[original]} /></InventorySessionBoundary> }] }]);
  render(<RouterProvider router={router} />);
  await open(editing, rack);
  const submit = editing ? 'Save changes' : rack ? 'Create record' : 'Create record';
  fireEvent.click(screen.getByRole('button', { name: submit }));
  await waitFor(() => expect(release).toBeDefined());
  await act(async () => { await router.navigate('/'); });
  await open(editing, rack);
  await act(async () => release!(response(mode.endsWith('review') ? { status: { allowed: true } } : original)));
  expect(writes).toHaveLength(mode.endsWith('dispatch') ? 1 : 0);
  expect(screen.queryByText(/Display name saved\.|Facility record created\./)).not.toBeInTheDocument();
  expect(screen.getByLabelText('Display name')).toHaveValue('New input');
  fireEvent.click(screen.getByRole('button', { name: submit }));
  await screen.findByText(editing ? 'Display name saved.' : rack ? 'Rack record created.' : 'Facility record created.');
  expect(writes).toHaveLength(mode.endsWith('dispatch') ? 2 : 1);
});
