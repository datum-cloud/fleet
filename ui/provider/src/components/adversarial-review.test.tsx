import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { InventorySessionBoundary } from './inventory-session-boundary';
import DeviceInventoryList from '../pages/device-inventory-list';
import { inventoryPayload, INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';

const values = { name: 'review-facility', namespace: 'default', displayName: 'Original label', recordedBy: 'synthetic-reviewer', reference: '', facility: '', height: '' };
const original = { ...inventoryPayload('Facility', values, '2026-01-01T00:00:00Z'), metadata: { name: values.name, namespace: 'default', uid: 'synthetic-only', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const response = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ data }) });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('renders calm denied access with exactly seven list attempts and no permission reviews or writes', async () => {
  const requests = vi.fn(async () => ({ ok: false, status: 403, json: async () => ({ error: 'DO NOT DISPLAY internal authorization diagnostic' }) }));
  vi.stubGlobal('fetch', requests);
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'denied-staff' } } }), element: <Outlet />, children: [{ index: true, element: <DeviceInventoryList /> }] }]);
  render(<RouterProvider router={router} />);
  await screen.findByRole('heading', { name: 'Inventory access needed' });
  expect(requests).toHaveBeenCalledTimes(7);
  for (const [, init] of requests.mock.calls as unknown as [string, RequestInit][]) expect(init.method).toBe('GET');
  expect(screen.queryByText(/DO NOT DISPLAY/)).not.toBeInTheDocument();
  expect(screen.queryByText(/Create facility/)).not.toBeInTheDocument();
});
it('invalidates authority synchronously before React commits the session transition', async () => {
  let principal = 'staff-a';
  let releaseReview: ((value: unknown) => void) | undefined;
  let reviews = 0;
  const writes: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) {
      reviews++;
      if (reviews === 2) return new Promise(resolve => { releaseReview = resolve; });
      return response({ status: { allowed: true } });
    }
    if (init.method === 'POST') writes.push(principal);
    return response(original);
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: principal } } }), element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><InventoryManagement kind="Facility" facilities={[]} /></InventorySessionBoundary> }] }]);
  render(<RouterProvider router={router} />);
  await screen.findByRole('button', { name: 'Create facility' });
  fireEvent.click(screen.getByText('Manage facilities'));
  fireEvent.click(screen.getByRole('button', { name: 'Create facility' }));
  fireEvent.change(screen.getByLabelText('Record name'), { target: { value: values.name } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: values.displayName } });
  fireEvent.change(screen.getByLabelText('Recorded by'), { target: { value: values.recordedBy } });
  fireEvent.click(screen.getByRole('button', { name: 'Create facility record' }));
  await waitFor(() => expect(releaseReview).toBeDefined());
  principal = 'staff-b';
  await act(async () => { void router.revalidate(); releaseReview!(response({ status: { allowed: true } })); });
  expect(writes).toEqual([]);
});

it('must not send a create after the initiating authenticated principal unmounts', async () => {
  let principal = 'staff-a';
  let releaseReview: ((value: unknown) => void) | undefined;
  let reviews = 0;
  const writes: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) {
      reviews++;
      if (reviews === 2) return new Promise(resolve => { releaseReview = resolve; });
      return response({ status: { allowed: true } });
    }
    if (init.method === 'POST') writes.push(principal);
    return response(original);
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: principal } } }), element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><InventoryManagement kind="Facility" facilities={[]} /></InventorySessionBoundary> }] }]);
  render(<RouterProvider router={router} />);
  await screen.findByRole('button', { name: 'Create facility' });
  fireEvent.click(screen.getByText('Manage facilities'));
  fireEvent.click(screen.getByRole('button', { name: 'Create facility' }));
  fireEvent.change(screen.getByLabelText('Record name'), { target: { value: values.name } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: values.displayName } });
  fireEvent.change(screen.getByLabelText('Recorded by'), { target: { value: values.recordedBy } });
  fireEvent.click(screen.getByRole('button', { name: 'Create facility record' }));
  await waitFor(() => expect(releaseReview).toBeDefined());
  principal = 'staff-b';
  await act(async () => { await router.revalidate(); });
  await act(async () => { releaseReview!(response({ status: { allowed: true } })); });
  expect(writes).toEqual([]);
});

it('must not silently advance an open edit form to a refreshed resourceVersion', async () => {
  let current = original;
  const patches: unknown[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    if (init.method === 'PATCH') patches.push(JSON.parse(String(init.body)));
    return response(current);
  }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const content = () => <QueryClientProvider client={client}><InventoryManagement kind="Facility" record={current} facilities={[]} /></QueryClientProvider>;
  const mounted = render(content());
  await screen.findByRole('button', { name: 'Edit display name' });
  fireEvent.click(screen.getByText('Manage this record'));
  fireEvent.click(screen.getByRole('button', { name: 'Edit display name' }));
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'My unsaved label' } });
  fireEvent.change(screen.getByLabelText('Recorded by'), { target: { value: values.recordedBy } });
  current = { ...original, metadata: { ...original.metadata, resourceVersion: '2' }, spec: { ...original.spec, displayName: 'Other operator saved this' } };
  mounted.rerender(content());
  fireEvent.click(screen.getByRole('button', { name: 'Save display name' }));
  await waitFor(() => expect(screen.getByRole('status')).toBeInTheDocument());
  expect(patches).toEqual([]);
});

it('must keep inventory concealed if focus verification finishes after another blur', async () => {
  let release: (() => void) | undefined;
  let count = 0;
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: async () => { if (++count === 2) await new Promise<void>(resolve => { release = resolve; }); return { user: { metadata: { uid: 'staff-a' } } }; }, element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary>Private inventory label</InventorySessionBoundary> }] }]);
  render(<RouterProvider router={router} />);
  await screen.findByText('Private inventory label');
  fireEvent(window, new Event('blur'));
  fireEvent(window, new Event('focus'));
  await waitFor(() => expect(release).toBeDefined());
  fireEvent(window, new Event('blur'));
  await act(async () => { release!(); });
  expect(screen.queryByText('Private inventory label')).not.toBeInTheDocument();
});
