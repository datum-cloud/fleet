import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import DeviceInventoryList from '../pages/device-inventory-list';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

function host(query = '', management = false) {
  let principal: string | null = 'staff-a';
  let denied = false;
  let held = false;
  let release: (() => void) | undefined;
  let holdReview = false;
  let releaseReview: (() => void) | undefined;
  const collections = vi.fn();
  const writes = vi.fn();
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) {
      if (holdReview) {
        holdReview = false;
        await new Promise<void>(resolve => { releaseReview = resolve; });
      }
      return Response.json({ data: { status: { allowed: management } } });
    }
    if (init.method !== 'GET') writes();
    collections();
    if (denied) return Response.json({ error: 'Denied' }, { status: 403 });
    const kind = url.includes('/facilities') ? 'Facility' : url.includes('/racks') ? 'Rack' : url.includes('/cables') ? 'Cable' : url.includes('/powerfeeds') ? 'PowerFeed' : 'Site';
    const items = url.includes('/devices') ? fixturePhysicalDevices : url.includes('/deviceclaims') ? fixturePhysicalClaims : fixturePhysicalRecords.filter(record => record.kind === kind);
    return Response.json({ data: { items } });
  }));
  const loader = vi.fn(async () => {
    if (held) await new Promise<void>(resolve => { release = resolve; });
    return { user: principal ? { metadata: { uid: principal } } : null };
  });
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader, element: <Outlet />, children: [{ path: 'plugins/:slug/inventory', element: <DeviceInventoryList /> }] }], { initialEntries: [`/plugins/metal/inventory${query}`] });
  render(<RouterProvider router={router} />);
  return { router, loader, collections, writes,
    hold: () => { held = true; release = undefined; },
    holdReview: () => { holdReview = true; },
    releaseReview: async () => { await waitFor(() => expect(releaseReview).toBeDefined()); await act(async () => releaseReview!()); },
    principal: (next: string | null) => { principal = next; },
    deny: () => { denied = true; },
    release: async () => { await waitFor(() => expect(release).toBeDefined()); held = false; await act(async () => release!()); },
  };
}

it('keeps actual tab DOM through host checks, reuses collections, and preserves detail/back/forward URLs', async () => {
  const view = host();
  await screen.findByRole('searchbox', { name: 'Search devices' });
  expect(view.collections).toHaveBeenCalledTimes(7);
  const navigation = screen.getByRole('navigation', { name: 'Inventory categories' });
  const facilities = within(navigation).getByRole('link', { name: 'Facilities' });
  view.hold();
  fireEvent.click(facilities);
  await screen.findByText('Checking inventory access…');
  expect(screen.getByRole('navigation', { name: 'Inventory categories' })).toBe(navigation);
  expect(within(navigation).getByRole('link', { name: 'Facilities' })).toBe(facilities);
  expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
  expect(screen.queryByTestId('fleet-plugin-loading')).not.toBeInTheDocument();
  expect(navigation.textContent).not.toMatch(/\d/);
  await view.release();
  await screen.findByRole('searchbox', { name: 'Search facilities' });
  expect(view.router.state.location.search).toBe('?kind=Facility');
  fireEvent.click(screen.getByRole('link', { name: 'Example facility' }));
  await screen.findByRole('heading', { name: 'Example facility' });
  expect(view.router.state.location.search).toContain('record=example-facility&namespace=default');
  await act(async () => { await view.router.navigate(-1); });
  await screen.findByRole('searchbox', { name: 'Search facilities' });
  await act(async () => { await view.router.navigate(-1); });
  await screen.findByRole('searchbox', { name: 'Search devices' });
  await act(async () => { await view.router.navigate(1); });
  await screen.findByRole('searchbox', { name: 'Search facilities' });
  expect(screen.getByRole('navigation', { name: 'Inventory categories' })).toBe(navigation);
  expect(view.collections).toHaveBeenCalledTimes(7);
  expect(view.loader).toHaveBeenCalledTimes(6);
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it('fences an in-flight create across a cached tab transition without carrying its draft', async () => {
  const view = host('?kind=Facility', true);
  fireEvent.click(await screen.findByRole('button', { name: 'Create facility' }));
  for (const [label, value] of [['Record name', 'synthetic'], ['Display name', 'Private pending draft'], ['Recorded by', 'synthetic-operator']]) fireEvent.change(screen.getByLabelText(label), { target: { value } });
  view.holdReview();
  fireEvent.click(screen.getByRole('button', { name: 'Create facility record' }));
  await screen.findByRole('button', { name: 'Saving…' });
  fireEvent.click(within(screen.getByRole('navigation', { name: 'Inventory categories' })).getByRole('link', { name: 'Devices' }));
  await screen.findByRole('searchbox', { name: 'Search devices' });
  await view.releaseReview();
  expect(view.collections).toHaveBeenCalledTimes(7);
  expect(view.writes).not.toHaveBeenCalled();
  fireEvent.click(within(screen.getByRole('navigation', { name: 'Inventory categories' })).getByRole('link', { name: 'Facilities' }));
  fireEvent.click(await screen.findByRole('button', { name: 'Create facility' }));
  expect(screen.getByLabelText('Display name')).toHaveValue('');
  expect(screen.queryByText('Facility record created.')).not.toBeInTheDocument();
  view.router.dispose();
});

it('loads a direct namespaced link without requiring a previous directory visit', async () => {
  const view = host('?kind=Facility&record=example-facility&namespace=default');
  await screen.findByRole('heading', { name: 'Example facility' });
  expect(view.collections).toHaveBeenCalledTimes(7);
  expect(within(screen.getByRole('navigation', { name: 'Inventory categories' })).getByRole('link', { name: 'Facilities' })).toHaveAttribute('aria-current', 'page');
  view.router.dispose();
});

it('restarts management authority after immediate same-principal host navigation', async () => {
  const view = host('?kind=Facility', true);
  await screen.findByRole('button', { name: 'Create facility' });
  for (const [category, action] of [['Devices', 'Create device'], ['Facilities', 'Create facility'], ['Devices', 'Create device']]) {
    await act(async () => {
      fireEvent.click(within(screen.getByRole('navigation', { name: 'Inventory categories' })).getByRole('link', { name: category }));
    });
    await waitFor(() => expect(screen.getByRole('button', { name: action })).toBeEnabled());
  }
  expect(view.collections).toHaveBeenCalledTimes(7);
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it.each(['principal', 'logout', 'denial', 'hidden', 'pagehide', 'revalidation', 'unknown-query'])('retains only static chrome across a real %s boundary', async transition => {
  const view = host();
  await screen.findByRole('searchbox', { name: 'Search devices' });
  const navigation = screen.getByRole('navigation', { name: 'Inventory categories' });
  view.hold();
  if (transition === 'principal') view.principal('staff-b');
  if (transition === 'logout') view.principal(null);
  if (transition === 'denial') view.deny();
  if (transition === 'hidden') {
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
    fireEvent(document, new Event('visibilitychange'));
    expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
    visibility.mockReturnValue('visible');
    fireEvent(document, new Event('visibilitychange'));
  } else if (transition === 'pagehide') {
    fireEvent(window, new Event('pagehide'));
    expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
    fireEvent(window, new Event('pageshow'));
  } else if (transition === 'principal' || transition === 'logout') {
    fireEvent.click(within(navigation).getByRole('link', { name: 'Facilities' }));
  } else if (transition === 'unknown-query') {
    act(() => { void view.router.navigate('?kind=Device&scope=other'); });
  } else act(() => { void view.router.revalidate(); });
  await screen.findByText('Checking inventory access…');
  expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
  expect(screen.getByRole('navigation', { name: 'Inventory categories' })).toBe(navigation);
  expect(navigation.textContent).not.toMatch(/\d/);
  await view.release();
  if (transition === 'logout') {
    await screen.findByText('Your session has expired');
    expect(view.collections).toHaveBeenCalledTimes(7);
  } else if (transition === 'denial') {
    await screen.findByRole('heading', { name: 'Inventory access needed' });
    expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
    expect(view.collections).toHaveBeenCalledTimes(14);
  } else {
    await screen.findByRole('searchbox');
    expect(view.collections).toHaveBeenCalledTimes(14);
  }
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});
