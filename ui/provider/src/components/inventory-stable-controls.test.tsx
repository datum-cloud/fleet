import { StrictMode, useContext, type ReactNode } from 'react';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { DevicePlacementEdit } from './device-placement-edit';
import { InventorySessionBoundary } from './inventory-session-boundary';
import { inventoryPayload, INVENTORY_FIELD_MANAGER, type ManagedKind } from '../lib/inventory-management';
import { fixturePhysicalDevices } from '../lib/fixtures';
import { InventoryOperationContext, type InventoryOperationScope } from '../lib/inventory-operation';

const values = { name: 'synthetic', namespace: 'default', displayName: 'Original', facility: '', height: '' };
const record = { ...inventoryPayload('Facility', values), metadata: { name: 'synthetic', namespace: 'default', uid: 'synthetic', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const device = { ...fixturePhysicalDevices[0], metadata: { ...record.metadata }, kind: 'Device' as const };
const response = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ data }) });
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

function host(content: ReactNode, strict = false) {
  let hold = false;
  let allow = true;
  let principal = 'staff-a';
  let releaseHost: (() => void) | undefined;
  let releasePermission: (() => void) | undefined;
  let scope: InventoryOperationScope | undefined;
  function Content() {
    scope = useContext(InventoryOperationContext);
    return content;
  }
  const writes = vi.fn();
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) {
      if (hold) await new Promise<void>(resolve => { releasePermission = resolve; });
      return response({ status: { allowed: allow } });
    }
    if (init.method !== 'GET') writes();
    return response(record);
  });
  vi.stubGlobal('fetch', fetch);
  const loader = vi.fn(async () => {
    if (hold) await new Promise<void>(resolve => { releaseHost = resolve; });
    return { user: { metadata: { uid: principal } } };
  });
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader, element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><Content /></InventorySessionBoundary> }] }]);
  render(strict ? <StrictMode><RouterProvider router={router} /></StrictMode> : <RouterProvider router={router} />);
  return {
    router, writes, fetch, loader, operation: () => scope!.begin(),
    hold: () => { hold = true; }, deny: () => { allow = false; },
    principal: (value: string) => { principal = value; },
    releaseHost: async () => { await waitFor(() => expect(releaseHost).toBeDefined()); await act(async () => releaseHost!()); },
    releasePermission: async () => { await waitFor(() => expect(releasePermission).toBeDefined()); hold = false; await act(async () => releasePermission!()); },
  };
}

const kinds: ManagedKind[] = ['Facility', 'Rack', 'Device', 'DeviceClaim', 'Cable'];
const cases = [...kinds.map(kind => ({ kind, mode: 'create', button: `Create ${kind === 'DeviceClaim' ? 'installation plan' : kind.toLowerCase()}` })),
  { kind: 'Facility' as const, mode: 'label', button: 'Edit display name' },
  { kind: 'Device' as const, mode: 'location', button: 'Add reported location' }];

it.each(cases.flatMap(test => [false, true].flatMap(strict => [false, true].map(expanded => ({ ...test, strict, expanded })))))('visible blur/focus leaves $mode $kind unchanged expanded=$expanded strict=$strict', async ({ kind, mode, button, strict, expanded }) => {
  const view = host(mode === 'location' ? <DevicePlacementEdit device={{ ...device, spec: { ...device.spec, reportedPlacement: undefined } }} locations={[]} /> : <InventoryManagement compact kind={kind} record={mode === 'label' ? record : undefined} facilities={[]} />, strict);
  await waitFor(() => expect(screen.getByRole('button', { name: button })).toBeEnabled());
  const access = screen.queryByText(mode === 'label' ? 'Editing access' : 'Namespace & access')?.closest('details');
  if (access && expanded) fireEvent.click(access.querySelector('summary')!);
  const opener = screen.getByRole('button', { name: button });
  const operation = view.operation();
  const requests = view.fetch.mock.calls.length;
  const loads = view.loader.mock.calls.length;
  const closedMarkup = document.body.innerHTML;
  fireEvent(window, new Event('blur'));
  expect(screen.getByRole('button', { name: button })).toBe(opener);
  expect(opener).toBeEnabled();
  fireEvent(window, new Event('focus'));
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  expect(document.body.innerHTML).toBe(closedMarkup);
  expect(() => operation.assertCurrent()).not.toThrow();
  fireEvent.click(opener);
  const input = screen.getByLabelText(mode === 'location' ? 'Correction reason' : 'Display name');
  fireEvent.change(input, { target: { value: 'Safe draft' } });
  const openMarkup = document.body.innerHTML;
  for (let cycle = 0; cycle < 3; cycle++) {
    fireEvent(window, new Event('blur'));
    expect(input).toBeEnabled();
    fireEvent(window, new Event('focus'));
  }
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  expect(screen.getByLabelText(mode === 'location' ? 'Correction reason' : 'Display name')).toBe(input);
  expect(document.body.innerHTML).toBe(openMarkup);
  expect(input).toHaveValue('Safe draft');
  expect(input).toBeEnabled();
  expect(() => operation.assertCurrent()).not.toThrow();
  expect(view.fetch).toHaveBeenCalledTimes(requests);
  expect(view.loader).toHaveBeenCalledTimes(loads);
  expect(view.writes).not.toHaveBeenCalled();
  if (access) {
    expect(screen.getByText(mode === 'label' ? 'Editing access' : 'Namespace & access').closest('details')).toBe(access);
    expect(access.open).toBe(expanded);
  }
  view.router.dispose();
});

it.each(['denied', 'principal', 'hidden', 'pagehide'])('removes retained private drafts on confirmed %s', async transition => {
  const view = host(<InventoryManagement compact kind="Facility" facilities={[]} />);
  fireEvent.click(await screen.findByRole('button', { name: 'Create facility' }));
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Private draft' } });
  view.hold();
  fireEvent(window, new Event('blur'));
  if (transition === 'hidden') {
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
    fireEvent(document, new Event('visibilitychange'));
  } else if (transition === 'pagehide') fireEvent(window, new Event('pagehide'));
  else {
    if (transition === 'denied') view.deny();
    else view.principal('staff-b');
    act(() => { void view.router.revalidate(); });
    await view.releaseHost();
    await view.releasePermission();
  }
  await waitFor(() => expect(screen.queryByDisplayValue('Private draft')).not.toBeInTheDocument());
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});
