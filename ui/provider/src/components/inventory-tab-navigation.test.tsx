import { openFilters, selectedValue } from '../test/field-select';
import { findEnabledButton } from '../test/enabled-button';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
import DeviceInventoryList from '../pages/device-inventory-list';
import HardwareCatalog from '../pages/hardware-catalog';
import { categoryLink, categoryMenu, categoryNavigation, categoryTrigger, chooseCategory } from '../test/inventory-category';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';
import { inventoryGroups, isCatalogKind } from './inventory-groups';
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
  const reviews = vi.fn();
  const collections = vi.fn();
  const writes = vi.fn();
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) {
      reviews();
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
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader, element: <Outlet />, children: [{ path: 'plugins/:slug/inventory', element: <DeviceInventoryList /> }, { path: 'plugins/:slug/catalog', element: <HardwareCatalog /> }] }], { initialEntries: [`/plugins/metal/inventory${query}`] });
  render(<RouterProvider router={router} />);
  return { router, loader, collections, writes, reviews,
    hold: () => { held = true; release = undefined; },
    holdReview: () => { holdReview = true; },
    releaseReview: async () => { await waitFor(() => expect(releaseReview).toBeDefined()); await act(async () => releaseReview!()); },
    principal: (next: string | null) => { principal = next; },
    deny: () => { denied = true; },
    release: async () => { await waitFor(() => expect(release).toBeDefined()); held = false; await act(async () => release!()); },
  };
}

it('keeps the switcher DOM through host checks, reuses collections, and preserves detail/back/forward URLs', async () => {
  const view = host('?kind=Site');
  await screen.findByRole('searchbox', { name: 'Search operational sites' });
  expect(view.collections).toHaveBeenCalledTimes(9);
  const navigation = categoryNavigation();
  const trigger = categoryTrigger(navigation);
  // Wait for the loaded page: until then the search box belongs to the loading shell.
  await screen.findByRole('table');
  const search = screen.getByRole('searchbox', { name: 'Search operational sites' });
  view.hold();
  chooseCategory('Facilities', navigation);
  // A same-user presentation navigation keeps the page on screen while the host checks: no placeholder, no rebuild.
  await waitFor(() => expect(view.router.state.navigation.state).toBe('loading'));
  expect(screen.queryByText('Checking inventory access…')).not.toBeInTheDocument();
  expect(screen.getByRole('searchbox', { name: 'Search operational sites' })).toBe(search);
  expect(categoryNavigation()).toBe(navigation);
  expect(categoryTrigger(navigation)).toBe(trigger);
  expect(screen.queryByTestId('fleet-plugin-loading')).not.toBeInTheDocument();
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
  expect(categoryNavigation()).toBe(navigation);
  // Exactly one toolbar however often the list and record pages swap.
  expect(document.querySelectorAll('.inventory-toolbar')).toHaveLength(1);
  expect(view.collections).toHaveBeenCalledTimes(9);
  expect(view.loader).toHaveBeenCalledTimes(6);
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it.each([['Manufacturers', 'Search manufacturers'], ['Device types', 'Search device types']])('keeps the Catalog %s toggle mounted and reuses authorized collections', async (title, search) => {
  const view = host(title === 'Manufacturers' ? '?kind=DeviceType' : '?kind=Manufacturer');
  await screen.findByRole('searchbox', { name: title === 'Manufacturers' ? 'Search device types' : 'Search manufacturers' });
  expect(view.router.state.location.pathname).toBe('/plugins/metal/catalog');
  const navigation = screen.getByRole('navigation', { name: 'Catalog categories' });
  const link = within(navigation).getByRole('link', { name: title });
  view.hold();
  fireEvent.click(link);
  await waitFor(() => expect(view.router.state.navigation.state).toBe('loading'));
  expect(screen.queryByText('Checking inventory access…')).not.toBeInTheDocument();
  expect(within(navigation).getByRole('link', { name: title })).toBe(link);
  expect(view.collections).toHaveBeenCalledTimes(9);
  await view.release();
  await screen.findByRole('searchbox', { name: search });
  expect(screen.getByRole('navigation', { name: 'Catalog categories' })).toBe(navigation);
  expect(within(navigation).getByRole('link', { name: title })).toHaveAttribute('aria-current', 'page');
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
  chooseCategory('Facilities');
  fireEvent.click(await findEnabledButton({ name: 'Create facility' }));
  expect(screen.getByLabelText('Display name')).toHaveValue('');
  expect(screen.queryByText('Facility created.')).not.toBeInTheDocument();
  view.router.dispose();
});

it('loads a direct namespaced link without requiring a previous directory visit', async () => {
  const view = host('?kind=Facility&record=example-facility&namespace=default');
  await screen.findByRole('heading', { name: 'Example facility' });
  expect(view.collections).toHaveBeenCalledTimes(9);
  expect(categoryLink('Facilities')).toHaveAttribute('aria-current', 'page');
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
  // Search applies as the reader types: there is no Apply button, and the URL waits for a pause.
  expect(screen.queryByRole('button', { name: 'Apply search' })).not.toBeInTheDocument();
  expect(new URLSearchParams(view.router.state.location.search).get('q')).toBeNull();
  await waitFor(() => expect(new URLSearchParams(view.router.state.location.search).get('q')).toBe('EXAMPLE'));
  await waitFor(() => expect(screen.getByRole('searchbox')).toHaveValue('EXAMPLE'));
  expect(search).toHaveFocus();
  expect(view.collections).toHaveBeenCalledTimes(9);
  view.router.dispose();
});

it('applies the search at once on Enter', async () => {
  const view = host();
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'leaf' } });
  fireEvent.submit(screen.getByRole('search'));
  // Sooner than the typing pause would apply it.
  await waitFor(() => expect(new URLSearchParams(view.router.state.location.search).get('q')).toBe('leaf'), { timeout: 100 });
  view.router.dispose();
});

it('puts counts in the switcher only after inventory access resolves', async () => {
  const view = host();
  const trigger = await screen.findByRole('button', { name: 'Inventory category: Devices' });
  expect(trigger.querySelector('.inventory-tab-count')).toBeNull();
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  await waitFor(() => expect(trigger).toHaveAccessibleDescription(`${fixturePhysicalDevices.length} records`));
  expect(trigger.querySelector('.inventory-tab-count')).toHaveTextContent(String(fixturePhysicalDevices.length));
  expect(categoryLink('Cables')).toHaveAccessibleDescription(/^\d+ records?$/);
  expect(screen.queryByText(/ of \d+ records$/)).not.toBeInTheDocument();
  view.router.dispose();
});

it('counts the filtered directory on its category', async () => {
  const view = host('?kind=Device&q=no-such-device');
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  await waitFor(() => expect(categoryTrigger()).toHaveAccessibleDescription(`0 of ${fixturePhysicalDevices.length} records`));
  expect(categoryLink('Cables')).not.toHaveAccessibleDescription(/ of /);
  view.router.dispose();
});

it.each(sections)('shows the current $kind category for a direct bookmark, on the page it belongs to', async section => {
  const view = host(`?kind=${section.kind}`);
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  if (isCatalogKind(section.kind)) {
    expect(view.router.state.location.pathname).toBe('/plugins/metal/catalog');
    expect(within(screen.getByRole('navigation', { name: 'Catalog categories' })).getByRole('link', { name: section.title })).toHaveAttribute('aria-current', 'page');
  } else {
    expect(categoryTrigger()).toHaveAccessibleName(`Inventory category: ${section.title}`);
    const menu = categoryMenu();
    expect(within(menu).getAllByRole('list').map(list => document.getElementById(list.getAttribute('aria-labelledby')!)?.textContent)).toEqual(inventoryGroups.map(group => group.title));
    expect(within(menu).getAllByRole('link').filter(link => link.getAttribute('aria-current') === 'page').map(link => link.textContent?.replace(/\d+$/, ''))).toEqual([section.title]);
  }
  expect(screen.queryAllByRole('tablist').filter(list => list.closest('nav'))).toHaveLength(0);
  expect(view.collections).toHaveBeenCalledTimes(9);
  view.router.dispose();
});

it('defaults to Devices and switches to every category without extra collection reads', async () => {
  const view = host();
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  const navigation = categoryNavigation();
  const trigger = categoryTrigger(navigation);
  expect(trigger).toHaveAccessibleName('Inventory category: Devices');
  const inventorySections = sections.filter(section => !isCatalogKind(section.kind));
  for (const section of [...inventorySections.slice(1), inventorySections[0]]) {
    chooseCategory(section.title, navigation);
    await waitFor(() => expect(view.router.state.location.search).toBe(`?kind=${section.kind}`));
    await waitFor(() => expect(view.router.state.navigation.state).toBe('idle'));
    await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
    expect(categoryTrigger(navigation)).toBe(trigger);
    expect(trigger).toHaveAccessibleName(`Inventory category: ${section.title}`);
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    expect(view.collections).toHaveBeenCalledTimes(9);
  }
  // Choosing the current category is not a navigation.
  const loads = view.loader.mock.calls.length;
  chooseCategory('Devices', navigation);
  expect(view.loader).toHaveBeenCalledTimes(loads);
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it('opens the switcher on the current category and moves between categories with the arrow keys', async () => {
  const view = host();
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  const trigger = categoryTrigger();
  trigger.focus();
  fireEvent.keyDown(trigger, { key: 'ArrowDown' });
  const menu = await screen.findByRole('dialog', { name: 'Inventory categories' });
  const devices = within(menu).getByRole('link', { name: 'Devices' });
  await waitFor(() => expect(devices).toHaveFocus());
  fireEvent.keyDown(devices, { key: 'ArrowDown' });
  expect(within(menu).getByRole('link', { name: 'Cables' })).toHaveFocus();
  fireEvent.keyDown(within(menu).getByRole('link', { name: 'Cables' }), { key: 'ArrowUp' });
  expect(devices).toHaveFocus();
  fireEvent.keyDown(devices, { key: 'ArrowUp' });
  expect(within(menu).getByRole('link', { name: 'Installation plans' })).toHaveFocus();
  fireEvent.click(within(menu).getByRole('link', { name: 'Racks' }));
  await waitFor(() => expect(screen.getByRole('searchbox', { name: 'Search racks' })).toBeEnabled());
  expect(screen.queryByRole('dialog', { name: 'Inventory categories' })).not.toBeInTheDocument();
  expect(view.collections).toHaveBeenCalledTimes(9);
  view.router.dispose();
});

it('restores search and filters with detail, group, Back and Forward presentation navigation', async () => {
  const query = '?kind=Device&q=Example&location=default%2Fexample-facility&model=Example';
  const view = host(query);
  await waitFor(() => expect(screen.getByRole('searchbox')).toBeEnabled());
  await act(async () => { await view.router.navigate(`${query}&record=${fixturePhysicalDevices[0].metadata.name}&namespace=default`); });
  await screen.findByRole('heading', { name: 'Example compute server' });
  // The back link returns to the list with its search and filters.
  const back = screen.getByRole('link', { name: 'Devices' });
  expect(back).toHaveClass('inventory-back-link');
  expect(back).toHaveAttribute('href', `/plugins/metal/inventory${query}`);
  fireEvent.click(back);
  await waitFor(() => expect(screen.getByRole('searchbox')).toHaveValue('Example'));
  openFilters();
  expect(selectedValue(screen.getByRole('combobox', { name: 'Filter by facility' }))).toBe('default/example-facility');
  chooseCategory('Operational sites');
  await waitFor(() => expect(screen.getByRole('searchbox', { name: 'Search operational sites' })).toBeEnabled());
  await act(async () => { await view.router.navigate(-1); });
  expect(screen.getByRole('searchbox')).toHaveValue('Example');
  expect(new URLSearchParams(view.router.state.location.search).get('model')).toBe('Example');
  await act(async () => { await view.router.navigate(1); });
  expect(categoryTrigger()).toHaveAccessibleName('Inventory category: Operational sites');
  expect(view.collections).toHaveBeenCalledTimes(9);
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it('restarts management authority after immediate same-principal host navigation', async () => {
  const view = host('?kind=Facility', true);
  await findEnabledButton({ name: 'Create facility' });
  for (const [category, action] of [['Devices', 'Create device'], ['Facilities', 'Create facility'], ['Devices', 'Create device']]) {
    const link = categoryLink(category);
    await act(async () => { fireEvent.click(link); });
    await waitFor(() => expect(screen.getByRole('button', { name: action })).toBeEnabled());
  }
  expect(view.collections).toHaveBeenCalledTimes(9);
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it.each(['principal', 'logout', 'denial', 'unknown-query'])('replaces private data after a real %s boundary', async transition => {
  const view = host();
  await screen.findByRole('searchbox', { name: 'Search devices' });
  const navigation = screen.getByRole('navigation', { name: 'Inventory categories' });
  if (transition === 'denial') {
    // A denial is observed on the next read, here an explicit refresh.
    await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh inventory' })).toBeEnabled());
    view.deny();
    fireEvent.click(screen.getByRole('button', { name: 'Refresh inventory' }));
  } else {
    view.hold();
    if (transition === 'principal') view.principal('staff-b');
    if (transition === 'logout') view.principal(null);
    if (transition === 'unknown-query') act(() => { void view.router.navigate('?kind=Device&scope=other'); });
    else chooseCategory('Operational sites', navigation);
    await waitFor(() => expect(view.router.state.navigation.state).toBe('loading'));
    expect(screen.getByRole('navigation', { name: 'Inventory categories' })).toBe(navigation);
    await view.release();
  }
  if (transition === 'logout') {
    await screen.findByText('Your session has expired');
    expect(view.collections).toHaveBeenCalledTimes(9);
  } else if (transition === 'denial') {
    await screen.findByRole('heading', { name: 'Inventory access needed' });
    expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
    expect(view.collections).toHaveBeenCalledTimes(18);
  } else {
    await screen.findByRole('searchbox');
    await waitFor(() => expect(view.collections).toHaveBeenCalledTimes(18));
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
  expect(screen.getByRole('table', { name: 'Components and Cables' })).toBeInTheDocument();
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});

it('checks management access once per kind, not on every navigation, and again on Refresh', async () => {
  const [first, second] = fixturePhysicalDevices;
  const view = host('?kind=Device', true);
  await screen.findByRole('table');
  await waitFor(() => expect(view.reviews).toHaveBeenCalled());
  await waitFor(() => expect(view.router.state.navigation.state).toBe('idle'));
  const checks = view.reviews.mock.calls.length;
  // Records, a filter and the way back to the directory reuse the answer for Device creation.
  for (const search of [`?kind=Device&record=${first.metadata.name}&namespace=default`, `?kind=Device&record=${second.metadata.name}&namespace=default`, '?kind=Device&q=compute', '?kind=Device']) {
    await act(async () => { await view.router.navigate(search); });
    await waitFor(() => expect(view.router.state.location.search).toBe(search));
    await waitFor(() => expect(view.router.state.navigation.state).toBe('idle'));
  }
  await screen.findByRole('table');
  expect(view.reviews).toHaveBeenCalledTimes(checks);
  expect(view.collections).toHaveBeenCalledTimes(9);
  fireEvent.click(screen.getByRole('button', { name: 'Refresh inventory' }));
  await waitFor(() => expect(view.reviews.mock.calls.length).toBeGreaterThan(checks));
  expect(view.writes).not.toHaveBeenCalled();
  view.router.dispose();
});
