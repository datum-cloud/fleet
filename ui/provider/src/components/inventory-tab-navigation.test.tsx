import { findEnabledButton } from '../test/enabled-button';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
import DeviceInventoryList from '../pages/device-inventory-list';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';
import { inventoryGroups, inventoryGroup } from './inventory-groups';
import { sections } from './inventory-navigation';

beforeEach(() => vi.stubGlobal('confirm', vi.fn(() => true)));

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
    const kind = url.includes('/manufacturers') ? 'Manufacturer' : url.includes('/devicetypes') ? 'DeviceType' : url.includes('/facilities') ? 'Facility' : url.includes('/racks') ? 'Rack' : url.includes('/cables') ? 'Cable' : url.includes('/powerfeeds') ? 'PowerFeed' : 'Site';
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
  const view = host('?kind=Site');
  await screen.findByRole('searchbox', { name: 'Search operational sites' });
  expect(view.collections).toHaveBeenCalledTimes(9);
  const navigation = screen.getByRole('navigation', { name: 'Inventory categories' });
  const facilities = within(navigation).getByRole('tab', { name: 'Facilities' });
  view.hold();
  fireEvent.click(facilities);
  await screen.findByText('Checking inventory access…');
  expect(screen.getByRole('navigation', { name: 'Inventory categories' })).toBe(navigation);
  expect(within(navigation).getByRole('tab', { name: 'Facilities' })).toBe(facilities);
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
  await screen.findByRole('searchbox', { name: 'Search operational sites' });
  await act(async () => { await view.router.navigate(1); });
  await screen.findByRole('searchbox', { name: 'Search facilities' });
  expect(screen.getByRole('navigation', { name: 'Inventory categories' })).toBe(navigation);
  expect(view.collections).toHaveBeenCalledTimes(9);
  expect(view.loader).toHaveBeenCalledTimes(6);
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it.each([['Manufacturers', 'Search manufacturers'], ['Device types', 'Search device types']])('keeps the %s catalog tab mounted and reuses authorized collections', async (title, search) => {
  const view = host(title === 'Manufacturers' ? '?kind=DeviceType' : '?kind=Manufacturer');
  await screen.findByRole('searchbox', { name: title === 'Manufacturers' ? 'Search device types' : 'Search manufacturers' });
  const navigation = screen.getByRole('navigation', { name: 'Inventory categories' });
  const tab = within(navigation).getByRole('tab', { name: title });
  view.hold();
  fireEvent.click(tab);
  await screen.findByText('Checking inventory access…');
  expect(within(navigation).getByRole('tab', { name: title })).toBe(tab);
  expect(view.collections).toHaveBeenCalledTimes(9);
  await view.release();
  await screen.findByRole('searchbox', { name: search });
  expect(screen.getByRole('navigation', { name: 'Inventory categories' })).toBe(navigation);
  expect(view.collections).toHaveBeenCalledTimes(9);
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it('fences an in-flight create across a cached tab transition without carrying its draft', async () => {
  const view = host('?kind=Facility', true);
  fireEvent.click(await findEnabledButton({ name: 'Create facility' }));
  expect(screen.queryByLabelText('Recorded by')).not.toBeInTheDocument();
  expect(screen.queryByLabelText('Change reference')).not.toBeInTheDocument();
  for (const [label, value] of [['Record name', 'synthetic'], ['Display name', 'Private pending draft']]) fireEvent.change(screen.getByLabelText(label), { target: { value } });
  view.holdReview();
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  await screen.findByRole('button', { name: 'Saving…' });
  await act(async () => { await view.router.navigate('/plugins/metal/inventory?kind=Device'); });
  await screen.findByRole('searchbox', { name: 'Search devices' });
  await view.releaseReview();
  expect(view.collections).toHaveBeenCalledTimes(9);
  expect(view.writes).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole('tab', { name: 'Locations' }));
  fireEvent.click(await screen.findByRole('tab', { name: 'Facilities' }));
  fireEvent.click(await findEnabledButton({ name: 'Create facility' }));
  expect(screen.getByLabelText('Display name')).toHaveValue('');
  expect(screen.queryByText('Facility record created.')).not.toBeInTheDocument();
  view.router.dispose();
});

it('loads a direct namespaced link without requiring a previous directory visit', async () => {
  const view = host('?kind=Facility&record=example-facility&namespace=default');
  await screen.findByRole('heading', { name: 'Example facility' });
  expect(view.collections).toHaveBeenCalledTimes(9);
  expect(within(screen.getByRole('navigation', { name: 'Inventory categories' })).getByRole('tab', { name: 'Facilities' })).toHaveAttribute('aria-current', 'page');
  view.router.dispose();
});

it('keeps typing local and focused, then persists the applied search in the URL', async () => {
  const view = host();
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  const search = screen.getByRole('searchbox');
  search.focus();
  const loads = view.loader.mock.calls.length;
  for (const value of ['E', 'EX', 'EXAMPLE']) fireEvent.change(search, { target: { value } });
  expect(search).toHaveFocus();
  expect(view.loader).toHaveBeenCalledTimes(loads);
  expect(view.collections).toHaveBeenCalledTimes(9);
  fireEvent.click(screen.getByRole('button', { name: 'Apply search' }));
  await waitFor(() => expect(new URLSearchParams(view.router.state.location.search).get('q')).toBe('EXAMPLE'));
  await waitFor(() => expect(screen.getByRole('searchbox')).toHaveValue('EXAMPLE'));
  expect(view.collections).toHaveBeenCalledTimes(9);
  view.router.dispose();
});

it.each(sections)('selects the correct group and only its subtabs for a direct $kind bookmark', async section => {
  const view = host(`?kind=${section.kind}`);
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  const group = inventoryGroup(section.kind);
  const primary = screen.getByRole('tablist', { name: 'Inventory groups' });
  expect(within(primary).getAllByRole('tab').map(tab => tab.textContent)).toEqual(inventoryGroups.map(item => item.title));
  expect(within(primary).getByRole('tab', { name: group.title })).toHaveAttribute('aria-selected', 'true');
  const navigation = screen.getByRole('navigation', { name: 'Inventory categories' });
  expect(within(navigation).getAllByRole('tablist')).toHaveLength(group.kinds.length > 1 ? 2 : 1);
  if (group.kinds.length > 1) {
    const secondary = screen.getByRole('tablist', { name: `${group.title} categories` });
    expect(within(secondary).getAllByRole('tab').map(tab => tab.textContent)).toEqual(sections.filter(item => group.kinds.some(kind => kind === item.kind)).map(item => item.title));
    expect(within(secondary).getByRole('tab', { name: section.title })).toHaveAttribute('aria-selected', 'true');
  } else expect(navigation).toHaveTextContent('Plans do not allocate capacity');
  expect(view.collections).toHaveBeenCalledTimes(9);
  view.router.dispose();
});

it('defaults to Equipment / Devices and navigates group defaults without extra collection reads', async () => {
  const view = host();
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  expect(screen.getByRole('tab', { name: 'Equipment' })).toHaveAttribute('aria-selected', 'true');
  expect(screen.getByRole('tab', { name: 'Devices' })).toHaveAttribute('aria-selected', 'true');
  const groups = screen.getByRole('tablist', { name: 'Inventory groups' });
  const tabs = within(groups).getAllByRole('tab');
  for (const group of [...inventoryGroups.slice(1), inventoryGroups[0]]) {
    fireEvent.click(within(groups).getByRole('tab', { name: group.title }));
    await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
    expect(view.router.state.location.search).toBe(`?kind=${group.kinds[0]}`);
    expect(within(groups).getAllByRole('tab')).toEqual(tabs);
    expect(view.collections).toHaveBeenCalledTimes(9);
  }
  const loads = view.loader.mock.calls.length;
  fireEvent.click(screen.getByRole('tab', { name: 'Equipment' }));
  fireEvent.click(screen.getByRole('tab', { name: 'Devices' }));
  expect(view.loader).toHaveBeenCalledTimes(loads);
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it('supports roving keyboard focus with manual Space activation on real route tabs', async () => {
  const view = host();
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  const equipment = screen.getByRole('tab', { name: 'Equipment' });
  const locations = screen.getByRole('tab', { name: 'Locations' });
  equipment.focus();
  fireEvent.keyDown(equipment, { key: 'ArrowRight' });
  await waitFor(() => expect(locations).toHaveFocus());
  expect(view.router.state.location.search).toBe('');
  expect(equipment).toHaveAttribute('aria-selected', 'true');
  fireEvent.keyDown(locations, { key: ' ' });
  await waitFor(() => expect(screen.getByRole('searchbox', { name: 'Search operational sites' })).toBeEnabled());
  expect(locations).toHaveFocus();
  expect(locations).toHaveAttribute('aria-selected', 'true');
  const sites = screen.getByRole('tab', { name: 'Operational sites' });
  sites.focus();
  fireEvent.keyDown(sites, { key: 'End' });
  const racks = screen.getByRole('tab', { name: 'Racks' });
  await waitFor(() => expect(racks).toHaveFocus());
  fireEvent.keyDown(racks, { key: ' ' });
  await waitFor(() => expect(screen.getByRole('searchbox', { name: 'Search racks' })).toBeEnabled());
  expect(view.collections).toHaveBeenCalledTimes(9);
  view.router.dispose();
});

it('restores search and filters with detail, group, Back and Forward presentation navigation', async () => {
  const query = '?kind=Device&q=Example&location=default%2Fexample-facility&model=Example';
  const view = host(query);
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  await act(async () => { await view.router.navigate(`${query}&record=${fixturePhysicalDevices[0].metadata.name}&namespace=default`); });
  await screen.findByRole('heading', { name: 'Example compute server' });
  const breadcrumb = screen.getByRole('navigation', { name: 'breadcrumb' });
  expect(breadcrumb).toHaveTextContent('EquipmentDevicesExample compute server');
  expect(within(breadcrumb).getByRole('link', { name: 'Devices' })).toHaveAttribute('href', `/plugins/metal/inventory${query}`);
  fireEvent.click(screen.getByRole('tab', { name: 'Devices' }));
  await waitFor(() => expect(screen.getByRole('searchbox')).toHaveValue('Example'));
  expect(screen.getByRole('combobox', { name: 'Filter by facility' })).toHaveValue('default/example-facility');
  fireEvent.click(screen.getByRole('tab', { name: 'Locations' }));
  await waitFor(() => expect(screen.getByRole('searchbox', { name: 'Search operational sites' })).toBeEnabled());
  await act(async () => { await view.router.navigate(-1); });
  expect(screen.getByRole('searchbox')).toHaveValue('Example');
  expect(new URLSearchParams(view.router.state.location.search).get('model')).toBe('Example');
  await act(async () => { await view.router.navigate(1); });
  expect(screen.getByRole('tab', { name: 'Locations' })).toHaveAttribute('aria-selected', 'true');
  expect(view.collections).toHaveBeenCalledTimes(9);
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it('restarts management authority after immediate same-principal host navigation', async () => {
  const view = host('?kind=Facility', true);
  await findEnabledButton({ name: 'Create facility' });
  for (const [category, action] of [['Devices', 'Create device'], ['Facilities', 'Create facility'], ['Devices', 'Create device']]) {
    fireEvent.click(screen.getByRole('tab', { name: category === 'Devices' ? 'Equipment' : 'Locations' }));
    await screen.findByRole('tab', { name: category });
    await act(async () => {
      fireEvent.click(within(screen.getByRole('navigation', { name: 'Inventory categories' })).getByRole('tab', { name: category }));
    });
    await waitFor(() => expect(screen.getByRole('button', { name: action })).toBeEnabled());
  }
  expect(view.collections).toHaveBeenCalledTimes(9);
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
    fireEvent.click(within(navigation).getByRole('tab', { name: 'Locations' }));
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
    expect(view.collections).toHaveBeenCalledTimes(9);
  } else if (transition === 'denial') {
    await screen.findByRole('heading', { name: 'Inventory access needed' });
    expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
    expect(view.collections).toHaveBeenCalledTimes(18);
  } else {
    await screen.findByRole('searchbox');
    expect(view.collections).toHaveBeenCalledTimes(18);
  }
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it('switches record detail tabs as presentation navigation without extra collection reads', async () => {
  const view = host(`?kind=Device&record=${fixturePhysicalDevices[0].metadata.name}&namespace=default`);
  await screen.findByRole('heading', { name: 'Example compute server' });
  expect(view.collections).toHaveBeenCalledTimes(9);
  const tabs = () => within(screen.getByRole('tablist', { name: 'Example compute server details' }));
  fireEvent.click(tabs().getByRole('tab', { name: 'Hardware & management' }));
  await waitFor(() => expect(tabs().getByRole('tab', { name: 'Hardware & management' })).toHaveAttribute('aria-selected', 'true'));
  expect(new URLSearchParams(view.router.state.location.search).get('tab')).toBe('hardware');
  await act(async () => { await view.router.navigate(-1); });
  await waitFor(() => expect(tabs().getByRole('tab', { name: 'Location' })).toHaveAttribute('aria-selected', 'true'));
  expect(view.collections).toHaveBeenCalledTimes(9);
  // The link diagram is presentation state too: switching to it reads nothing.
  expect(screen.getByRole('table', { name: 'Components and Cables' })).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Link diagram' }));
  await waitFor(() => expect(new URLSearchParams(view.router.state.location.search).get('view')).toBe('diagram'));
  expect(await screen.findByRole('group', { name: /Connection diagram/ })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Link diagram' })).toHaveAttribute('aria-pressed', 'true');
  expect(view.collections).toHaveBeenCalledTimes(9);
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});
