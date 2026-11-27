import { findEnabledButton } from '../test/enabled-button';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { DevicePlacementEdit } from './device-placement-edit';
import { InventorySessionBoundary } from './inventory-session-boundary';
import { inventoryPayload, INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';
import { fixturePhysicalDevices } from '../lib/fixtures';

const values = { name: 'synthetic', namespace: 'default', displayName: 'Original', facility: '', height: '' };
const metadata = { name: 'synthetic', namespace: 'default', uid: 'synthetic', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] };
const facility = { ...inventoryPayload('Facility', values), metadata };
const device = { ...fixturePhysicalDevices[0], metadata, spec: { ...fixturePhysicalDevices[0].spec, reportedPlacement: undefined } };
const response = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ data }) });
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it.each(['create', 'label', 'location'].flatMap(mode => ['review', 'read', 'dispatch'].filter(stage => mode !== 'create' || stage !== 'read').flatMap(stage => ['focus', 'principal'].map(transition => ({ mode, stage, transition })))))('$transition handles pending $mode $stage without transferring authority', async ({ mode, stage, transition }) => {
  let release: (() => void) | undefined;
  let reviews = 0;
  let held = false;
  const original = mode === 'location' ? device : facility;
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    const review = url.includes('selfsubjectaccessreviews');
    if (review) reviews++;
    const write = !review && init.method !== 'GET';
    if (!held && ((stage === 'review' && review && reviews === 2) || (stage === 'read' && init.method === 'GET') || (stage === 'dispatch' && write))) {
      held = true;
      await new Promise<void>(resolve => { release = resolve; });
    }
    if (review) return response({ status: { allowed: true } });
    if (mode === 'location' && write) return response({ ...device, metadata: { ...metadata, resourceVersion: '2' }, spec: { ...device.spec, reportedPlacement: JSON.parse(init.body as string)[2].value } });
    return response(original);
  });
  vi.stubGlobal('fetch', fetch);
  let principal = 'staff-a';
  const loader = vi.fn(() => ({ user: { metadata: { uid: principal } } }));
  const content = mode === 'location' ? <DevicePlacementEdit device={device} locations={[]} /> : <InventoryManagement kind="Facility" record={mode === 'label' ? facility : undefined} facilities={[]} />;
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader, element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary>{content}</InventorySessionBoundary> }] }]);
  render(<RouterProvider router={router} />);
  const opener = await findEnabledButton({ name: mode === 'location' ? 'Add reported location' : mode === 'label' ? 'Edit display name' : 'Create facility' });
  await waitFor(() => expect(opener).toBeEnabled());
  fireEvent.click(opener);
  if (mode === 'location') {
    fireEvent.change(screen.getByLabelText('Correction reason'), { target: { value: 'Synthetic correction' } });
    fireEvent.change(screen.getByLabelText('Evidence reference'), { target: { value: 'ticket/synthetic' } });
  } else {
    if (mode === 'create') fireEvent.change(screen.getByLabelText('Record name'), { target: { value: 'synthetic' } });
    fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'New label' } });
  }
  fireEvent.click(screen.getByRole('button', { name: mode === 'location' ? 'Save changes' : mode === 'label' ? 'Save changes' : 'Create record' }));
  await waitFor(() => expect(release).toBeDefined());
  if (transition !== 'focus') {
    if (transition === 'hidden') {
      const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
      fireEvent(document, new Event('visibilitychange'));
      expect(opener).not.toBeInTheDocument();
      visibility.mockReturnValue('visible');
      fireEvent(document, new Event('visibilitychange'));
    } else {
      principal = 'staff-b';
      await act(async () => router.revalidate());
    }
    await waitFor(() => expect(loader).toHaveBeenCalledTimes(2));
    await act(async () => release!());
    expect(opener).not.toBeInTheDocument();
    expect(screen.queryByText(/Location saved|Display name saved\.|Facility created\./)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(mode === 'location' ? 'Correction reason' : 'Display name')).not.toBeInTheDocument();
    expect(fetch.mock.calls.filter(([url, init]) => !url.includes('selfsubjectaccessreviews') && init.method !== 'GET')).toHaveLength(stage === 'dispatch' ? 1 : 0);
    return;
  }
  const markup = document.body.innerHTML;
  const requests = fetch.mock.calls.length;
  fireEvent(window, new Event('blur'));
  fireEvent(window, new Event('focus'));
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  expect(document.body.innerHTML).toBe(markup);
  expect(fetch).toHaveBeenCalledTimes(requests);
  expect(loader).toHaveBeenCalledTimes(1);
  await act(async () => release!());
  await screen.findByText(mode === 'location' ? /Location saved/ : mode === 'label' ? 'Display name saved.' : 'Facility created.');
  const writes = fetch.mock.calls.filter(([url, init]) => !url.includes('selfsubjectaccessreviews') && init.method !== 'GET');
  expect(writes).toHaveLength(1);
  if (mode !== 'create') expect(JSON.parse(writes[0][1].body as string).slice(0, 2)).toEqual([
    { op: 'test', path: '/metadata/uid', value: 'synthetic' },
    { op: 'test', path: '/metadata/resourceVersion', value: '1' },
  ]);
});

// Returning to a hidden tab re-checks the signed-in user (staff-portal does not):
// a save held before dispatch goes on for the same user and is never sent for another.
it.each(['create', 'label', 'location'].flatMap(mode => ['review', 'read'].filter(stage => mode !== 'create' || stage !== 'read').flatMap(stage => ['same', 'other'].map(user => ({ mode, stage, user })))))('tab return holds a pending $mode $stage until the user is confirmed ($user user)', async ({ mode, stage, user }) => {
  let release: (() => void) | undefined;
  let reviews = 0;
  let held = false;
  const original = mode === 'location' ? device : facility;
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    const review = url.includes('selfsubjectaccessreviews');
    if (review) reviews++;
    if (!held && ((stage === 'review' && review && reviews === 2) || (stage === 'read' && init.method === 'GET'))) {
      held = true;
      await new Promise<void>(resolve => { release = resolve; });
    }
    if (review) return response({ status: { allowed: true } });
    if (mode === 'location' && init.method === 'PATCH') return response({ ...device, metadata: { ...metadata, resourceVersion: '2' }, spec: { ...device.spec, reportedPlacement: JSON.parse(init.body as string)[2].value } });
    return response(original);
  });
  vi.stubGlobal('fetch', fetch);
  let principal = 'staff-a';
  let confirm: (() => void) | undefined;
  const loader = vi.fn(async () => {
    if (loader.mock.calls.length > 1) await new Promise<void>(resolve => { confirm = resolve; });
    return { user: { metadata: { uid: principal } } };
  });
  const content = mode === 'location' ? <DevicePlacementEdit device={device} locations={[]} /> : <InventoryManagement kind="Facility" record={mode === 'label' ? facility : undefined} facilities={[]} />;
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader, element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary>{content}</InventorySessionBoundary> }] }]);
  render(<RouterProvider router={router} />);
  fireEvent.click(await findEnabledButton({ name: mode === 'location' ? 'Add reported location' : mode === 'label' ? 'Edit display name' : 'Create facility' }));
  if (mode === 'location') {
    fireEvent.change(screen.getByLabelText('Correction reason'), { target: { value: 'Synthetic correction' } });
    fireEvent.change(screen.getByLabelText('Evidence reference'), { target: { value: 'ticket/synthetic' } });
  } else {
    if (mode === 'create') fireEvent.change(screen.getByLabelText('Record name'), { target: { value: 'synthetic' } });
    fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'New label' } });
  }
  fireEvent.click(screen.getByRole('button', { name: mode === 'create' ? 'Create record' : 'Save changes' }));
  await waitFor(() => expect(release).toBeDefined());
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
  fireEvent(document, new Event('visibilitychange'));
  if (user === 'other') principal = 'staff-b';
  visibility.mockReturnValue('visible');
  fireEvent(document, new Event('visibilitychange'));
  await waitFor(() => expect(confirm).toBeDefined());
  const writes = () => fetch.mock.calls.filter(([url, init]) => !url.includes('selfsubjectaccessreviews') && init.method !== 'GET');
  // The pending step finishes while the check is still running: nothing is sent yet.
  await act(async () => release!());
  expect(writes()).toHaveLength(0);
  await act(async () => confirm!());
  if (user === 'same') {
    await screen.findByText(mode === 'location' ? /Location saved/ : mode === 'label' ? 'Display name saved.' : 'Facility created.');
    expect(writes()).toHaveLength(1);
  } else {
    await waitFor(() => expect(screen.queryByLabelText(mode === 'location' ? 'Correction reason' : 'Display name')).not.toBeInTheDocument());
    expect(screen.queryByText(/Location saved|Display name saved\.|Facility created\./)).not.toBeInTheDocument();
    expect(writes()).toHaveLength(0);
  }
  router.dispose();
});
