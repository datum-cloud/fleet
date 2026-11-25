import { fireEvent, render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, useLocation } from 'react-router';
import { describe, expect, it } from 'vitest';
import { PhysicalInventory } from './physical-inventory';
import { fixtureInventory } from '../lib/inventory-fixtures';
import type { Inventory } from '../inventory';

function Url() {
  const { search } = useLocation();
  return <output data-testid="url">{search}</output>;
}

function show(query: string, inventory: Inventory = fixtureInventory()) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={[`/inventory${query}`]}><Url /><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
}
const record = (kind: string, name: string, extra = '') => `?kind=${kind}&record=${name}&namespace=default${extra}`;
const url = () => new URLSearchParams(screen.getByTestId('url').textContent ?? '');
const tabs = () => within(screen.getByRole('tablist', { name: /details$/ }));

describe('record identity', () => {
  it('closes the page with one Record section and keeps identity out of the header', () => {
    const { container } = show(record('Device', 'example-srv-a01-01'));
    expect(screen.queryByText('About this record')).not.toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: /History/ })).not.toBeInTheDocument();
    const about = screen.getByRole('region', { name: 'Record' });
    expect(about.closest('.inventory-record-tabs')).toBeNull();
    // One line: the resource name and creation date; namespace, UID and verification are not repeated per record.
    expect(about).toHaveTextContent(/^Record example-srv-a01-01 · created 2026-10-01$/);
    expect(about.textContent).not.toMatch(/Namespace|synthetic-example|Verification/);
    const header = container.querySelector('.inventory-record-header')!;
    expect(header).toHaveTextContent('Compute a01-01');
    expect(header.textContent).not.toMatch(/example-srv-a01-01|synthetic-|default/);
    expect(screen.queryByText(/Devices \/ example-srv-a01-01/)).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Back to devices/ })).not.toBeInTheDocument();
  });

  it.each([['Cable', 'example-brk-swp49'], ['PowerFeed', 'example-feed-a'], ['Facility', 'example-dc1'], ['Site', 'example-site-one'], ['DeviceType', 'example-server-1u'], ['Manufacturer', 'example-compute'], ['DeviceClaim', 'example-srv-a01-01-install']])('puts the %s record name only in the Record section', (kind, name) => {
    show(record(kind, name));
    const matches = screen.getAllByText(name, { exact: true });
    for (const match of matches) expect(match.closest('.inventory-record-footer')).not.toBeNull();
  });
});

describe('detail tabs held in the URL', () => {
  it('selects the first tab by default and hides the other panels', () => {
    const { container } = show(record('Device', 'example-srv-a01-01'));
    expect(tabs().getByRole('tab', { name: 'Location' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('region', { name: 'Location' })).toBeVisible();
    expect(container.querySelector('[data-tab="hardware"]')).toHaveAttribute('hidden');
    expect(url().get('tab')).toBeNull();
  });

  it('writes the selected tab to the URL and keeps other parameters', () => {
    show(record('Device', 'example-srv-a01-01', '&q=compute&tab=hardware'));
    fireEvent.click(tabs().getByRole('tab', { name: 'Location' }));
    expect(url().get('tab')).toBe('location');
    expect(url().get('record')).toBe('example-srv-a01-01');
    expect(url().get('q')).toBe('compute');
    const location = screen.getByRole('region', { name: 'Location' });
    expect(location).toBeVisible();
    // The guarded correction entry point lives here, still behind its permission check.
    expect(within(location).getByRole('button', { name: /Correct location|Add reported location/ })).toBeDisabled();
    expect(within(location).queryByText(/not physically verified/)).toBeNull();
    expect(location).not.toHaveTextContent(/\bverified\b/i);
  });

  it('opens a tab named in the URL and falls back for unknown values', () => {
    const view = show(record('Device', 'example-srv-a01-01', '&tab=hardware'));
    expect(tabs().getByRole('tab', { name: 'Hardware & management' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('region', { name: 'Hardware & management' })).toHaveTextContent('example-provider/Host/synthetic-host-0001');
    view.unmount();
    show(record('Device', 'example-srv-a01-01', '&tab=not-a-tab'));
    expect(tabs().getByRole('tab', { name: 'Location' })).toHaveAttribute('aria-selected', 'true');
  });

  it('does not carry the tab into links to other records', () => {
    show(record('Device', 'example-srv-a01-01', '&tab=location'));
    const rack = within(screen.getByRole('region', { name: 'Location' }).ownerDocument.querySelector('.inventory-record-header') as HTMLElement).getByRole('link', { name: 'Rack A01' });
    expect(new URL(rack.getAttribute('href')!, 'https://inventory.test').searchParams.get('tab')).toBeNull();
  });

  it('shows unknown counts when a collection is denied', () => {
    show(record('DeviceType', 'example-server-1u'), fixtureInventory('devices-denied'));
    expect(tabs().getByRole('tab', { name: 'Devices ?' })).toBeInTheDocument();
    expect(tabs().queryByRole('tab', { name: 'Devices 0' })).not.toBeInTheDocument();
  });
});

describe('Device page states each fact once', () => {
  const connections = () => within(screen.getByRole('region', { name: 'Components and connections' }));
  const facts = (container: HTMLElement) => [...container.querySelectorAll('.inventory-fact-strip > div')].map(item => item.textContent);

  it('always shows the drawn faceplate when the model records a layout, with no Picture or Table choice', () => {
    show(record('Device', 'example-srv-a01-01'));
    expect(connections().getByRole('group', { name: /^Faceplate drawing/ })).toBeInTheDocument();
    // Short face labels; the full title stays the group's name.
    expect(connections().getByRole('group', { name: 'Front · rack front face' })).toHaveTextContent(/^Front/);
    // No tray on this page: the table lists what is not on the faceplate.
    expect(connections().queryByRole('group', { name: 'Not on the faceplate' })).toBeNull();
    expect(within(connections().getByRole('table')).getByText('Serial console')).toBeInTheDocument();
    expect(connections().queryByRole('group', { name: /Port tiles/ })).toBeNull();
    expect(connections().queryByText('Layout not recorded')).toBeNull();
    expect(connections().queryByRole('group', { name: 'Connection view' })).toBeNull();
    for (const name of ['Picture', 'Table', 'Diagram', 'Link diagram', 'Legend', 'Faceplate', 'Ports', 'List']) expect(connections().queryByRole('button', { name })).toBeNull();
    expect(connections().getByRole('table', { name: 'Components and Cables' })).toBeInTheDocument();
  });

  it('always shows the grouped tiles when the model records no layout', () => {
    show(record('Device', 'example-app-a01'));
    expect(connections().getByText('Layout not recorded')).toBeInTheDocument();
    expect(connections().getByRole('group', { name: /Port tiles/ })).toBeInTheDocument();
    expect(connections().queryByRole('group', { name: /^Faceplate drawing/ })).toBeNull();
    for (const name of ['Picture', 'Table', 'Diagram']) expect(connections().queryByRole('button', { name })).toBeNull();
    expect(connections().getByRole('table', { name: 'Components and Cables' })).toBeInTheDocument();
  });

  it.each(['', '&view=table', '&view=diagram'])('states the port counts once, in the table header (%s)', view => {
    const { container } = show(record('Device', 'example-srv-a01-01', view));
    expect(document.body.textContent?.match(/\d+ ports · /g)).toHaveLength(1);
    expect(container.querySelector('.inventory-connection-summary')).toHaveTextContent(/^9 ports · 4 recorded · 1 disagree · 4 catalog only$/);
    // Whatever view the URL names, the page is the picture and the table: there is no diagram.
    expect(connections().getByRole('group', { name: /^Faceplate drawing/ })).toBeInTheDocument();
    expect(connections().getByRole('table')).toBeInTheDocument();
    expect(connections().queryByRole('group', { name: /Connection diagram/ })).toBeNull();
    expect(screen.queryByText(/All components/)).toBeNull();
    expect(screen.queryByText(/\d+ network ports? · /)).toBeNull();
    // No caveat sentence runs inline.
    expect(screen.queryByText(/Unknown is not free/)).toBeNull();
    // The tabs hold only what is not about ports, with no count badges.
    expect(tabs().getAllByRole('tab').map(tab => tab.textContent)).toEqual(['Location', 'Hardware & management']);
    expect(container.querySelector('.inventory-record-tabs .inventory-count')).toBeNull();
  });

  it('puts identity in one header strip and the display-name reason in the button, not on the page', () => {
    const { container } = show(record('Device', 'example-srv-a01-01'));
    const header = container.querySelector<HTMLElement>('.inventory-record-header')!;
    expect(header.querySelector('dl.inventory-fact-strip')).toHaveAttribute('aria-label', 'Identity');
    expect(facts(header)).toEqual(['ModelExample 1U compute', 'SerialSYNTH-EX-01', 'Asset tagEX-ASSET-01', expect.stringMatching(/^BMCBMC ✓/)]);
    // The old Identity row and its separate draw line are gone from the body.
    expect(container.querySelector('.inventory-primary .inventory-fields')).toBeNull();
    expect(container.querySelector('.inventory-primary .inventory-device-draw')).toBeNull();
    const edit = within(header).getByRole('button', { name: 'Edit display name' });
    expect(edit).toBeDisabled();
    expect(edit).toHaveAccessibleDescription('Managed elsewhere — not editable here');
    expect(edit.closest('.inventory-management-controls')).toHaveAttribute('title', 'Managed elsewhere — not editable here');
    expect(within(header).getByRole('status').textContent).toBe('');
  });

  it('states the model once and keeps the plan and rack footprint in Location', () => {
    const { container } = show(record('Device', 'example-srv-a01-01'));
    expect(screen.getAllByRole('link', { name: 'Example 1U compute' })).toHaveLength(1);
    expect(container.textContent).not.toMatch(/Shared catalog/);
    const location = screen.getByRole('region', { name: 'Location' });
    expect(within(location).getByText('Catalog size').nextElementSibling).toHaveTextContent(/^1U · full depth$/);
    expect(within(location).getByText('Installation plan').nextElementSibling).toHaveTextContent('Installation · A01 U2');
    expect(container.querySelector('[data-tab="hardware"]')!.textContent).not.toMatch(/Catalog size|Installation plan|Device claim/);
    // Interface MACs sit beside their ports in the component table, not in a tab.
    const table = screen.getByRole('table', { name: /Components/ });
    expect(within(table).getByRole('columnheader', { name: 'MAC address' })).toBeInTheDocument();
    expect(within(table).getByText('00:00:5e:00:53:11')).toBeInTheDocument();
    // The tab lists the recorded MAC only where discovery observed it, on the host NIC that reports it.
    const hardware = container.querySelector<HTMLElement>('[data-tab="hardware"]')!;
    expect(within(hardware).queryByRole('table', { name: 'Interfaces not in the component list' })).toBeNull();
    expect(within(hardware).getAllByText('00:00:5e:00:53:11').map(cell => cell.closest('table')?.getAttribute('aria-label'))).toEqual(['Host NICs']);
    expect(within(location).getByText('Depth for rack conflicts')).toBeInTheDocument();
    // Placement states units, height and face; the header has the facility and rack, and depth has its own fact.
    const placement = within(location).getByText('Placement').nextElementSibling as HTMLElement;
    expect(placement.querySelector('.inventory-placement-row > span')).toHaveTextContent(/^Reported · U2 · 1U · front$/);
    // The correction action sits on the Placement row it changes.
    expect(within(placement).getByRole('button', { name: /Correct location/ })).toBeDisabled();
    expect(location.textContent?.match(/Full depth/g)).toHaveLength(1);
    // The correction editor carries no recordkeeping caveat, and stays permission-gated.
    expect(location).not.toHaveTextContent(/not a physical move/);
    expect(within(location).getByRole('button', { name: /Correct location/ })).toBeDisabled();
    expect(within(within(location).getByRole('region', { name: 'Correct reported location' })).getByRole('status')).toBeInTheDocument();
  });
});

describe('issue banners', () => {
  const issues = () => screen.queryByRole('list', { name: 'Record issues' });

  it('leave cable disagreements to the picture, the rows and the marked count', () => {
    const { container } = show(record('Device', 'example-srv-a01-01'));
    expect(screen.queryByText(/Records disagree on/)).not.toBeInTheDocument();
    expect(container.querySelector('.inventory-connection-summary [data-state="conflict"]')).toHaveTextContent('1 disagree');
  });

  it('flag unknown placement and link to the Location tab', () => {
    const view = show(record('Device', 'example-srv-a03-01'));
    expect(within(issues()!).getByText('Position in the rack is unknown.')).toBeInTheDocument();
    fireEvent.click(within(issues()!).getByRole('link', { name: 'Review' }));
    expect(url().get('tab')).toBe('location');
    view.unmount();
    show(record('Device', 'example-srv-unplaced-01'));
    expect(within(issues()!).getByText(/Location unknown: no reported placement, and the claim record was not found/)).toBeInTheDocument();
  });

  it('say unreadable cables are unavailable once in the count line, never as zero or free', () => {
    const { container } = show(record('Device', 'example-srv-a01-01'), fixtureInventory('cables-denied'));
    const summary = container.querySelector('.inventory-connection-summary');
    expect(summary).toHaveTextContent(/^13 components · Cable records are unavailable to you\.$/);
    expect(summary).not.toHaveTextContent(/\b0 |recorded|free/);
    expect(document.body.textContent?.match(/Cable records are unavailable to you/g)).toHaveLength(1);
  });

  it('stay away when nothing is wrong', () => {
    show(record('Device', 'example-sto-a01-03'));
    expect(issues()).toBeNull();
    expect(screen.getByRole('heading', { name: 'Storage a01-03' })).toBeInTheDocument();
  });

  it('flag a Cable end whose record is missing', () => {
    show(record('Cable', 'example-dac-a01-02-eth1'));
    expect(within(issues()!).getByText(/End B names a record not found: example-leaf-missing/)).toBeInTheDocument();
  });
});

describe('hidden empty fields', () => {
  it('hides empty facts but keeps meaningful absence', () => {
    const inventory = fixtureInventory();
    const device = inventory.slots.flatMap(slot => slot.devices).find(item => item.metadata.name === 'example-sto-a01-03')!;
    delete device.spec.assetTag;
    show(record('Device', 'example-sto-a01-03'), inventory);
    expect(screen.queryByText('Asset tag')).not.toBeInTheDocument();
    expect(screen.queryByText('Management endpoint')).not.toBeInTheDocument();
    expect(screen.queryByText('Recorded nominal dimensions')).not.toBeInTheDocument();
    expect(screen.queryByText('Not recorded')).not.toBeInTheDocument();
    expect(document.querySelector('dl.inventory-fact-strip')).toHaveTextContent('Serial');
  });

  it('names empty power outlets as no cable recorded and never shows a denied outlet as free', () => {
    const view = show(record('PowerFeed', 'example-feed-a'));
    const outlets = screen.getByRole('region', { name: 'Power outlets' });
    // Outlets are tiles: "No cable recorded" is in each empty tile's name, not a row of text.
    expect(within(outlets).getAllByRole('button', { name: /^Outlet \d: No cable recorded$/ })).toHaveLength(3);
    // The peer is one click away, in the outlet's card.
    fireEvent.click(within(outlets).getByRole('button', { name: /^Outlet 1: Cable recorded/ }));
    expect(within(screen.getByRole('dialog', { name: 'Outlet 1 details' })).getByRole('link', { name: 'PDU a01 (rear left) · Inlet' })).toBeInTheDocument();
    expect(screen.queryByText(/No cable recorded means unknown, not free/)).toBeNull();
    expect(screen.queryByText(/do not establish available power/)).toBeNull();
    view.unmount();
    show(record('PowerFeed', 'example-feed-a'), fixtureInventory('cables-denied'));
    expect(screen.queryByText('No cable recorded')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /No cable recorded/ })).not.toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: 'Power outlets' })).getAllByRole('button', { name: /^Outlet \d: Cable details unavailable, connections unknown$/ })).toHaveLength(4);
  });

  it('states type words once per group instead of on every row', () => {
    show(record('PowerFeed', 'example-feed-a'));
    expect(screen.queryByText('PowerOutlet')).not.toBeInTheDocument();
    expect(screen.getAllByText(/IEC C19/)).toHaveLength(1);
  });
});

describe('search-first lists', () => {
  it('shows no generated names and opens a record from its row', () => {
    show('?kind=Device&q=compute');
    const table = screen.getByRole('table', { name: 'Devices' });
    expect(within(table).queryByText('example-srv-a01-01')).not.toBeInTheDocument();
    // Location first, so it stays in view at phone width; optional columns appear because some device records them.
    expect(within(table).getAllByRole('columnheader').map(cell => cell.textContent)).toEqual(['Name', 'Rack', 'U', 'Face', 'Model', 'Serial', 'Asset tag', 'Power']);
    expect(table.closest('[data-testid="fleet-plugin-inventory-table"]')).not.toBeNull();
    fireEvent.click(within(table).getByText('SYNTH-EX-01'));
    expect(url().get('record')).toBe('example-srv-a01-01');
    expect(screen.getByRole('heading', { name: 'Compute a01-01' })).toBeInTheDocument();
  });

  it('marks unknown locations with "?", an amber edge and the reason', () => {
    show('?kind=Device');
    const table = screen.getByRole('table', { name: 'Devices' });
    const unpositioned = within(table).getByRole('link', { name: 'Compute a03-01 (position unknown)' }).closest('tr')!;
    expect(unpositioned).toHaveAttribute('data-attention', 'unavailable');
    expect(unpositioned).toHaveAttribute('title', 'Position not recorded');
    const [, rack, position] = within(unpositioned).getAllByRole('cell');
    expect(rack).toHaveTextContent('Rack A03');
    expect(position).toHaveTextContent('?Position not recorded');
    expect(position.querySelector('[aria-hidden="true"]')).toHaveTextContent('?');
    const unplaced = within(table).getByRole('link', { name: 'Compute unplaced-01' }).closest('tr')!;
    expect(unplaced).toHaveAttribute('data-attention', 'unavailable');
    expect(unplaced).toHaveTextContent('Installation plan not found and no placement reported');
    // Unplaced equipment sorts after everything with a location; racks run top of rack first.
    const names = within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell')[0].querySelector('a')!.textContent);
    expect(names.at(-1)).toBe('Compute unplaced-01');
    expect(names.indexOf('Leaf a01')).toBeLessThan(names.indexOf('Compute a01-01'));
    expect(within(table).queryByText(/· position unknown|claim-derived|Claim record not found/)).toBeNull();
  });

  it('shows a non-Active lifecycle as a muted word after the name', () => {
    show('?kind=Device');
    const row = screen.getByRole('link', { name: 'Compute retired-01' }).closest('tr')!;
    expect(within(row).getByTitle('Lifecycle: Retired')).toHaveTextContent('Retired');
  });

  it('names a Cable end once when every lane shares it', () => {
    show('?kind=Cable');
    const row = screen.getByRole('link', { name: 'Breakout swp49' }).closest('tr')!;
    expect(within(row).getAllByRole('link', { name: 'Leaf a01 · swp49' })).toHaveLength(1);
    // The four-way end is one line; each lane is in the tooltip.
    const fanout = within(row).getByText('4 devices · eth0');
    expect(fanout).toHaveAttribute('title', expect.stringContaining('1: Storage a01-03 · eth0'));
    expect(row.querySelector('small')).toBeNull();
    expect(within(row).getAllByRole('cell').at(-1)).toHaveTextContent('4');
  });

  it('colours cable rows: red for a port two cables record, amber for a missing peer', () => {
    show('?kind=Cable');
    const duplicate = screen.getByRole('link', { name: 'DAC a01-01 eth1 (duplicate record)' }).closest('tr')!;
    expect(duplicate).toHaveAttribute('data-attention', 'conflict');
    expect(duplicate).toHaveAttribute('title', 'Another cable records the same port');
    const missing = screen.getByRole('link', { name: 'DAC a01-02 eth1' }).closest('tr')!;
    expect(missing).toHaveAttribute('data-attention', 'unavailable');
    expect(within(missing).getByText('example-leaf-missing · swp7')).toHaveAttribute('title', 'Device record not found');
    expect(screen.queryByText(/record not found\)/)).toBeNull();
  });
});
