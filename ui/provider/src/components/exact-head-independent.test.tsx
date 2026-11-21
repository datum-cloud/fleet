import { StrictMode } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { InventorySessionBoundary } from './inventory-session-boundary';
import { inventoryPayload, INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';
import DeviceInventoryList from '../pages/device-inventory-list';

const values = { name: 'exact-review', namespace: 'default', displayName: 'Original', recordedBy: 'synthetic', reference: '', facility: '', height: '' };
const original = { ...inventoryPayload('Facility', values, '2026-01-01T00:00:00Z'), metadata: { name: values.name, namespace: 'default', uid: 'synthetic', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const response = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => ({ data }) });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

async function open(editing: boolean, label: string) {
  fireEvent.click(await screen.findByRole('button', { name: editing ? 'Edit display name' : 'Create facility' }));
  if (!editing) fireEvent.change(screen.getByLabelText('Record name'), { target: { value: values.name } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: label } });
  fireEvent.change(screen.getByLabelText('Recorded by'), { target: { value: 'synthetic' } });
}

it.each([false, true].flatMap(strict => [false, true].flatMap(editing => ['review', 'dispatch-ok', 'dispatch-error'].flatMap(stage => ['fast-repeat', 'slow', 'overlap', 'cancel-navigation', 'focus-blur', 'focus-pagehide'].map(transition => ({ strict, editing, stage, transition }))))))('independent $transition $stage edit=$editing strict=$strict', async ({ strict, editing, stage, transition }) => {
  let submitting = false;
  let intercepted = false;
  let releaseOperation: ((value: unknown) => void) | undefined;
  let holdLoader = false;
  const loaders: (() => void)[] = [];
  const writes: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    const review = url.includes('selfsubjectaccessreviews');
    const write = !review && init.method !== 'GET';
    if (write) writes.push(init.method!);
    if (submitting && !intercepted && ((stage === 'review' && review) || (stage !== 'review' && write))) {
      intercepted = true;
      return new Promise(resolve => { releaseOperation = resolve; });
    }
    return response(review ? { status: { allowed: true } } : original);
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: async () => {
    if (holdLoader) await new Promise<void>(resolve => loaders.push(resolve));
    return { user: { metadata: { uid: 'same-user' } } };
  }, element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><InventoryManagement kind="Facility" record={editing ? original : undefined} facilities={[]} /></InventorySessionBoundary> }] }]);
  render(strict ? <StrictMode><RouterProvider router={router} /></StrictMode> : <RouterProvider router={router} />);
  await open(editing, 'Old input');
  const submit = editing ? 'Save display name' : 'Create facility record';
  submitting = true;
  fireEvent.click(screen.getByRole('button', { name: submit }));
  await waitFor(() => expect(releaseOperation).toBeDefined());
  if (transition === 'fast-repeat') {
    await act(async () => { await router.revalidate(); await router.revalidate(); await router.revalidate(); });
  } else {
    holdLoader = true;
    act(() => { if (transition.startsWith('focus-')) window.dispatchEvent(new Event('focus')); else if (transition === 'cancel-navigation') void router.navigate('/?kind=Rack'); else void router.revalidate(); });
    await waitFor(() => expect(loaders).toHaveLength(1));
    expect(screen.queryByLabelText('Display name')).not.toBeInTheDocument();
    if (transition.startsWith('focus-')) {
      fireEvent(window, new Event(transition === 'focus-blur' ? 'blur' : 'pagehide'));
      await act(async () => loaders[0]());
      expect(screen.queryByLabelText('Display name')).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Create facility' })).not.toBeInTheDocument();
      holdLoader = false;
      await act(async () => window.dispatchEvent(new Event('focus')));
    } else if (transition === 'overlap' || transition === 'cancel-navigation') {
      act(() => { if (transition === 'cancel-navigation') void router.navigate('/?kind=Facility'); else void router.revalidate(); });
      await waitFor(() => expect(loaders).toHaveLength(2));
      await act(async () => loaders[1]());
      await act(async () => loaders[0]());
      holdLoader = false;
    } else {
      await act(async () => loaders[0]());
      holdLoader = false;
    }
  }
  await open(editing, 'Fresh input');
  await act(async () => releaseOperation!(response(stage === 'review' ? { status: { allowed: true } } : original, stage === 'dispatch-error' ? 503 : 200)));
  expect(writes).toHaveLength(stage === 'review' ? 0 : 1);
  expect(screen.queryByText(/Display name saved\.|Facility record created\./)).not.toBeInTheDocument();
  expect(screen.getByLabelText('Display name')).toHaveValue('Fresh input');
  fireEvent.click(screen.getByRole('button', { name: submit }));
  await screen.findByText(editing ? 'Display name saved.' : 'Facility record created.');
  expect(writes).toHaveLength(stage === 'review' ? 1 : 2);
  router.dispose();
});

it.each([false, true].flatMap(strict => [401, 403, 404, 503].flatMap(status => ['revalidate', 'focus'].map(transition => ({ strict, status, transition })))))('late previous cache never overrides new denial $status strict=$strict via=$transition', async ({ strict, status, transition }) => {
  let phase = 'initial';
  const releases: (() => void)[] = [];
  const freshRequests: string[] = [];
  const privateRecord = { ...original, spec: { ...original.spec, displayName: 'Private facility label' } };
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (phase === 'denied') {
      freshRequests.push(init.method!);
      return response(undefined, status);
    }
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    const result = response({ items: url.includes('/facilities?') ? [privateRecord] : [] });
    if (phase === 'pending') return new Promise(resolve => releases.push(() => resolve(result)));
    return result;
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'same-user' } } }), element: <Outlet />, children: [{ index: true, element: <DeviceInventoryList /> }] }], { initialEntries: ['/?kind=Facility'] });
  render(strict ? <StrictMode><RouterProvider router={router} /></StrictMode> : <RouterProvider router={router} />);
  await screen.findByRole('link', { name: /Private facility label/ });
  await screen.findByRole('button', { name: 'Create facility' });
  phase = 'pending';
  fireEvent.click(screen.getByRole('button', { name: 'Refresh inventory' }));
  await waitFor(() => expect(releases).toHaveLength(7));
  phase = 'denied';
  await act(async () => {
    if (transition === 'focus') { fireEvent(window, new Event('blur')); fireEvent(window, new Event('focus')); }
    else { await router.revalidate(); await router.revalidate(); }
  });
  const title = status === 401 ? 'Your session has expired' : status === 403 ? 'Inventory access needed' : status === 404 ? 'Inventory section unavailable' : 'Inventory is temporarily unavailable';
  await screen.findByRole('heading', { name: title });
  await act(async () => releases.forEach(release => release()));
  expect(screen.getByRole('heading', { name: title })).toBeInTheDocument();
  expect(screen.queryByText('Private facility label')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Create facility' })).not.toBeInTheDocument();
  expect(freshRequests.length).toBeGreaterThanOrEqual(7);
  expect(freshRequests.every(method => method === 'GET')).toBe(true);
  router.dispose();
});
