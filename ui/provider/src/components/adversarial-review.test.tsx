import { findEnabledButton } from '../test/enabled-button';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { InventorySessionBoundary } from './inventory-session-boundary';
import DeviceInventoryList from '../pages/device-inventory-list';
import { inventoryPayload, INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';
import { focusManager } from '@tanstack/react-query';
import { useContext } from 'react';
import { InventoryOperationContext, type InventoryOperationScope } from '../lib/inventory-operation';

const values = { name: 'review-facility', namespace: 'default', displayName: 'Original label', facility: '', height: '' };
const original = { ...inventoryPayload('Facility', values), metadata: { name: values.name, namespace: 'default', uid: 'synthetic-only', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const response = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ data }) });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); focusManager.setFocused(undefined); });

it.each([false, true])('visible focus never refreshes real inventory; explicit refresh observes denial=%s', async denied => {
  let rechecking = false;
  const requests = vi.fn(async (url: string) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: false } });
    if (rechecking && denied) return { ok: false, status: 403, json: async () => ({ error: 'Private diagnostic' }) };
    return response({ items: url.includes('/facilities?') ? [original] : [] });
  });
  vi.stubGlobal('fetch', requests);
  const loader = vi.fn(() => ({ user: { metadata: { uid: 'staff-a' } } }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader, element: <Outlet />, children: [{ index: true, element: <DeviceInventoryList /> }] }], { initialEntries: ['/?kind=Facility'] });
  render(<RouterProvider router={router} />);
  await screen.findByRole('link', { name: /Original label/ });
  const table = screen.getByRole('table');
  const loadingStates: boolean[] = [];
  const observer = new MutationObserver(() => loadingStates.push(!!screen.queryByTestId('fleet-plugin-loading')));
  observer.observe(document.body, { childList: true, subtree: true });
  rechecking = true;
  const before = requests.mock.calls.length;
  fireEvent(window, new Event('blur'));
  expect(screen.getByRole('table')).toBe(table);
  fireEvent(window, new Event('focus'));
  await act(async () => { focusManager.setFocused(false); focusManager.setFocused(true); await new Promise(resolve => setTimeout(resolve, 0)); });
  expect(screen.getByRole('table')).toBe(table);
  expect(requests).toHaveBeenCalledTimes(before);
  expect(loader).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole('button', { name: 'Refresh inventory' }));
  if (denied) {
    await screen.findByRole('heading', { name: 'Inventory access needed' });
    expect(screen.queryByText(/Original label|Private diagnostic/)).not.toBeInTheDocument();
  } else {
    await waitFor(() => expect(requests.mock.calls.filter(([url]) => url.includes('/facilities?'))).toHaveLength(2));
    expect(screen.getByRole('table')).toBe(table);
  }
  observer.disconnect();
  expect(loadingStates).not.toContain(true);
});

it('renders calm denied access with exactly nine list attempts and no permission reviews or writes', async () => {
  const requests = vi.fn(async () => ({ ok: false, status: 403, json: async () => ({ error: 'DO NOT DISPLAY internal authorization diagnostic' }) }));
  vi.stubGlobal('fetch', requests);
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'denied-staff' } } }), element: <Outlet />, children: [{ index: true, element: <DeviceInventoryList /> }] }]);
  render(<RouterProvider router={router} />);
  await screen.findByRole('heading', { name: 'Inventory access needed' });
  expect(requests).toHaveBeenCalledTimes(9);
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
  await findEnabledButton({ name: 'Create facility' });
  fireEvent.click(screen.getByRole('button', { name: 'Create facility' }));
  fireEvent.change(screen.getByLabelText('Record name'), { target: { value: values.name } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: values.displayName } });
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
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
  await findEnabledButton({ name: 'Create facility' });
  fireEvent.click(screen.getByRole('button', { name: 'Create facility' }));
  fireEvent.change(screen.getByLabelText('Record name'), { target: { value: values.name } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: values.displayName } });
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
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
  await findEnabledButton({ name: 'Edit display name' });
  fireEvent.click(screen.getByRole('button', { name: 'Edit display name' }));
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'My unsaved label' } });
  current = { ...original, metadata: { ...original.metadata, resourceVersion: '2' }, spec: { ...original.spec, displayName: 'Other operator saved this' } };
  mounted.rerender(content());
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  await waitFor(() => expect(screen.getByRole('status')).toBeInTheDocument());
  expect(patches).toEqual([]);
});

// Restored from "must keep inventory concealed if foreground verification
// finishes after another pagehide". The page is no longer concealed during a
// check; the risk it guarded is that a check answered for an earlier return
// lets work through after the page has gone away again. It must not.
it('must keep work held if a return check finishes after another pagehide', async () => {
  let release: (() => void) | undefined;
  let count = 0;
  let scope: InventoryOperationScope | undefined;
  function Private() { scope = useContext(InventoryOperationContext); return <p>Private inventory label</p>; }
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: async () => { if (++count === 2) await new Promise<void>(resolve => { release = resolve; }); return { user: { metadata: { uid: 'staff-a' } } }; }, element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><Private /></InventorySessionBoundary> }] }]);
  render(<RouterProvider router={router} />);
  await screen.findByText('Private inventory label');
  fireEvent(window, new Event('pagehide'));
  fireEvent(window, new Event('pageshow'));
  await waitFor(() => expect(release).toBeDefined());
  fireEvent(window, new Event('pagehide'));
  const operation = scope!.begin();
  let settled = false;
  void operation.settle().then(() => { settled = true; });
  await act(async () => { release!(); });
  await waitFor(() => expect(router.state.revalidation).toBe('idle'));
  expect(scope!.held).toBe(true);
  expect(settled).toBe(false);
  // The next return checks again, and only then does the work go on.
  fireEvent(window, new Event('pageshow'));
  await waitFor(() => expect(settled).toBe(true));
  expect(count).toBe(3);
  expect(screen.getByText('Private inventory label')).toBeInTheDocument();
  router.dispose();
});
