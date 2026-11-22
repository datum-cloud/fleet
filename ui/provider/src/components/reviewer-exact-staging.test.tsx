import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { DevicePlacementEdit } from './device-placement-edit';
import { InventorySessionBoundary } from './inventory-session-boundary';
import { inventoryPayload, INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';
import { fixturePhysicalDevices } from '../lib/fixtures';

const metadata = { name: 'review-only', namespace: 'default', uid: 'synthetic-review', resourceVersion: '7', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] };
const facility = { ...inventoryPayload('Facility', { name: 'review-only', namespace: 'default', displayName: 'Original', recordedBy: 'synthetic', reference: '', facility: '', height: '' }), metadata };
const device = { ...fixturePhysicalDevices[0], metadata, spec: { ...fixturePhysicalDevices[0].spec, provenance: undefined, reportedPlacement: undefined } };
const response = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => ({ data }) });
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it('settled host keeps earlier foreground generations masked until the newest loader finishes', async () => {
  const releases: (() => void)[] = [];
  let reads = 0;
  const loader = vi.fn(async () => {
    if (++reads > 1) await new Promise<void>(resolve => releases.push(resolve));
    return { user: { metadata: { uid: 'synthetic-staff' } } };
  });
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader, element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary>Private generation inventory</InventorySessionBoundary> }] }]);
  await act(async () => { render(<RouterProvider router={router} />); });
  await screen.findByText('Private generation inventory');
  await act(async () => {});
  for (const expectedReads of [1, 2]) {
    await act(async () => {
      fireEvent(window, new Event('pagehide'));
      fireEvent(window, new Event('pageshow'));
    });
    await waitFor(() => expect(releases).toHaveLength(expectedReads));
    expect(screen.queryByText('Private generation inventory')).not.toBeInTheDocument();
  }
  await act(async () => releases[0]());
  expect(screen.queryByText('Private generation inventory')).not.toBeInTheDocument();
  await act(async () => releases[1]());
  await screen.findByText('Private generation inventory');
  expect(router.state.revalidation).toBe('idle');
  expect(loader).toHaveBeenCalledTimes(3);
  router.dispose();
});

it.each(['label', 'location'].flatMap(mode => [401, 403, 404, 409, 422, 503, 0].map(status => ({ mode, status }))))('held $mode PATCH keeps visible focus inert then handles terminal $status', async ({ mode, status }) => {
  let rejectWrite: (() => void) | undefined;
  const original = mode === 'location' ? device : facility;
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    if (init.method === 'GET') return response(original);
    await new Promise<void>(resolve => { rejectWrite = resolve; });
    if (status === 0) throw new TypeError('Synthetic connection lost after dispatch');
    return response({}, status);
  });
  vi.stubGlobal('fetch', fetch);
  const loader = vi.fn(() => ({ user: { metadata: { uid: 'synthetic-staff' } } }));
  const content = mode === 'location' ? <DevicePlacementEdit device={device} locations={[]} /> : <InventoryManagement kind="Facility" record={facility} facilities={[]} />;
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader, element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary>{content}</InventorySessionBoundary> }] }]);
  render(<RouterProvider router={router} />);
  const opener = await screen.findByRole('button', { name: mode === 'location' ? 'Add reported location' : 'Edit display name' });
  await waitFor(() => expect(opener).toBeEnabled());
  fireEvent.click(opener);
  const input = screen.getByLabelText(mode === 'location' ? 'Correction reason' : 'Display name');
  fireEvent.change(input, { target: { value: 'Retain this draft' } });
  if (mode === 'location') {
    fireEvent.change(screen.getByLabelText('Observation time (ISO 8601)'), { target: { value: '2026-01-02T00:00:00Z' } });
    fireEvent.change(screen.getByLabelText('Evidence reference'), { target: { value: 'ticket/synthetic' } });
  } else fireEvent.change(screen.getByLabelText('Recorded by'), { target: { value: 'synthetic' } });
  fireEvent.click(screen.getByRole('button', { name: mode === 'location' ? 'Save reported location' : 'Save display name' }));
  await waitFor(() => expect(rejectWrite).toBeDefined());
  const markup = document.body.innerHTML;
  const requestCount = fetch.mock.calls.length;
  fireEvent(window, new Event('blur'));
  fireEvent(window, new Event('focus'));
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  expect(document.body.innerHTML).toBe(markup);
  expect(fetch).toHaveBeenCalledTimes(requestCount);
  expect(loader).toHaveBeenCalledTimes(1);
  expect(screen.getByLabelText(mode === 'location' ? 'Correction reason' : 'Display name')).toBe(input);
  expect(input).toHaveValue('Retain this draft');
  expect(screen.getByRole('status')).not.toHaveTextContent(/outcome is unknown|saved\./i);
  await act(async () => rejectWrite!());
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/no longer valid|changed|disappeared|unknown|could not accept|session expired|no longer available/));
  expect(screen.getByRole('status')).not.toHaveTextContent(/saved\./i);
  const save = screen.getByRole('button', { name: mode === 'location' ? 'Save reported location' : 'Save display name' });
  if (mode === 'label' && status === 422) expect(save).toBeEnabled();
  else expect(save).toBeDisabled();
  if (status === 503 || status === 0) expect(screen.getByRole('status')).toHaveTextContent('outcome is unknown');
  if (!(mode === 'label' && status === 422)) fireEvent.click(save);
  fireEvent(window, new Event('blur'));
  fireEvent(window, new Event('focus'));
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  expect(fetch).toHaveBeenCalledTimes(requestCount);
  const writes = fetch.mock.calls.filter(([, init]) => init.method === 'PATCH');
  expect(writes).toHaveLength(1);
  const patch = JSON.parse(writes[0][1].body as string);
  expect(patch.slice(0, 2)).toEqual([{ op: 'test', path: '/metadata/uid', value: metadata.uid }, { op: 'test', path: '/metadata/resourceVersion', value: metadata.resourceVersion }]);
  if (mode === 'location') {
    expect(patch).toHaveLength(3);
    expect(patch[2].path).toBe('/spec/reportedPlacement');
    expect(patch.some((entry: { path: string }) => entry.path.includes('provenance'))).toBe(false);
  }
  router.dispose();
});

it.each(['logout', 'failure'].flatMap(outcome => ['pagehide', 'hidden'].flatMap(transition => ['immediate', 'held'].map(timing => ({ outcome, transition, timing })))))('settled host $transition restoration handles $timing $outcome', async ({ outcome, transition, timing }) => {
  let expired = false;
  let release: (() => void) | undefined;
  const loader = vi.fn(async () => {
    if (!expired) return { user: { metadata: { uid: 'synthetic-staff' } } };
    if (timing === 'held') await new Promise<void>(resolve => { release = resolve; });
    if (outcome === 'failure') throw new Response('Synthetic failure', { status: 503 });
    return { user: null };
  });
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader, element: <Outlet />, errorElement: <p>Host failure</p>, children: [{ index: true, element: <InventorySessionBoundary>Private review inventory</InventorySessionBoundary> }] }]);
  await act(async () => { render(<RouterProvider router={router} />); });
  await screen.findByText('Private review inventory');
  await act(async () => {});
  expired = true;
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  await act(async () => {
    if (transition === 'hidden') {
      visibility.mockReturnValue('hidden');
      fireEvent(document, new Event('visibilitychange'));
    } else fireEvent(window, new Event('pagehide'));
  });
  expect(screen.queryByText('Private review inventory')).not.toBeInTheDocument();
  await act(async () => {
    if (transition === 'hidden') {
      visibility.mockReturnValue('visible');
      fireEvent(document, new Event('visibilitychange'));
    } else fireEvent(window, new Event('pageshow'));
  });
  if (timing === 'held') {
    await waitFor(() => expect(release).toBeDefined());
    expect(router.state.revalidation).toBe('loading');
    expect(screen.queryByText('Private review inventory')).not.toBeInTheDocument();
    await act(async () => release!());
  }
  await waitFor(() => expect(router.state.revalidation).toBe('idle'));
  expect(loader).toHaveBeenCalledTimes(2);
  if (outcome === 'logout') expect(router.state.loaderData['layouts/milo.layout']).toEqual({ user: null });
  else expect(router.state.errors).not.toBeNull();
  expect(screen.queryByText('Private review inventory')).not.toBeInTheDocument();
  fireEvent(window, new Event('focus'));
  await act(async () => {});
  expect(screen.queryByText('Private review inventory')).not.toBeInTheDocument();
  expect(loader).toHaveBeenCalledTimes(2);
  router.dispose();
});
