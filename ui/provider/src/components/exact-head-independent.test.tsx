import { findEnabledButton } from '../test/enabled-button';
import { StrictMode } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { InventorySessionBoundary } from './inventory-session-boundary';
import { inventoryPayload, INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';
import DeviceInventoryList from '../pages/device-inventory-list';

const values = { name: 'exact-review', namespace: 'default', displayName: 'Original', facility: '', height: '' };
const original = { ...inventoryPayload('Facility', values), metadata: { name: values.name, namespace: 'default', uid: 'synthetic', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const response = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => ({ data }) });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

async function open(editing: boolean, label: string) {
  await waitFor(() => expect(screen.getByRole('button', { name: editing ? 'Edit display name' : 'Create facility' })).toBeEnabled());
  fireEvent.click(await findEnabledButton({ name: editing ? 'Edit display name' : 'Create facility' }));
  if (!editing) fireEvent.change(screen.getByLabelText('Record name'), { target: { value: values.name } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: label } });
}

it.each([false, true].flatMap(strict => [false, true].flatMap(editing => ['review', 'dispatch-ok', 'dispatch-error'].flatMap(stage => ['fast-repeat', 'principal-repeat', 'slow', 'overlap', 'cancel-navigation', 'restore-hidden', 'restore-pagehide'].map(transition => ({ strict, editing, stage, transition }))))))('independent $transition $stage edit=$editing strict=$strict', async ({ strict, editing, stage, transition }) => {
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  let principal = 'same-user';
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
    return { user: { metadata: { uid: principal } } };
  }, element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><InventoryManagement kind="Facility" record={editing ? original : undefined} facilities={[]} /></InventorySessionBoundary> }] }]);
  render(strict ? <StrictMode><RouterProvider router={router} /></StrictMode> : <RouterProvider router={router} />);
  await open(editing, 'Old input');
  const submit = editing ? 'Save display name' : 'Create facility record';
  submitting = true;
  fireEvent.click(screen.getByRole('button', { name: submit }));
  await waitFor(() => expect(releaseOperation).toBeDefined());
  if (transition === 'principal-repeat') {
    await act(async () => { principal = 'other-user'; await router.revalidate(); principal = 'same-user'; await router.revalidate(); });
  } else if (transition === 'fast-repeat') {
    await act(async () => { await router.revalidate(); await router.revalidate(); await router.revalidate(); });
  } else {
    holdLoader = true;
    act(() => { if (transition.startsWith('restore-')) { window.dispatchEvent(new Event('pagehide')); window.dispatchEvent(new Event('pageshow')); } else if (transition === 'cancel-navigation') void router.navigate('/?kind=Rack'); else void router.revalidate(); });
    await waitFor(() => expect(loaders).toHaveLength(1));
    expect(screen.queryByLabelText('Display name')).not.toBeInTheDocument();
    if (transition.startsWith('restore-')) {
      if (transition === 'restore-hidden') {
        visibility.mockReturnValue('hidden');
        fireEvent(document, new Event('visibilitychange'));
      } else fireEvent(window, new Event('pagehide'));
      await act(async () => loaders[0]());
      expect(screen.queryByLabelText('Display name')).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Create facility' })).not.toBeInTheDocument();
      holdLoader = false;
      visibility.mockReturnValue('visible');
      await act(async () => window.dispatchEvent(new Event('pageshow')));
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

it.each([false, true].flatMap(strict => [401, 403, 404, 503].flatMap(status => ['revalidate', 'restore'].map(transition => ({ strict, status, transition })))))('late previous cache never overrides new denial $status strict=$strict via=$transition', async ({ strict, status, transition }) => {
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
  await findEnabledButton({ name: 'Create facility' });
  phase = 'pending';
  fireEvent.click(screen.getByRole('button', { name: 'Refresh inventory' }));
  await waitFor(() => expect(releases).toHaveLength(9));
  phase = 'denied';
  await act(async () => {
    if (transition === 'restore') { fireEvent(window, new Event('pagehide')); fireEvent(window, new Event('pageshow')); }
    else { await router.revalidate(); await router.revalidate(); }
  });
  const title = status === 401 ? 'Your session has expired' : status === 403 ? 'Inventory access needed' : status === 404 ? 'Inventory section unavailable' : 'Inventory is temporarily unavailable';
  await screen.findByRole('heading', { name: title });
  await act(async () => releases.forEach(release => release()));
  expect(screen.getByRole('heading', { name: title })).toBeInTheDocument();
  expect(screen.queryByText('Private facility label')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Create facility' })).toBeDisabled();
  expect(freshRequests.length).toBeGreaterThanOrEqual(9);
  expect(freshRequests.every(method => method === 'GET')).toBe(true);
  router.dispose();
});
