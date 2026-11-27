import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { PhysicalInventory } from './physical-inventory';
import { fixtureInventory } from '../lib/inventory-fixtures';
import type { Inventory } from '../inventory';
import { categoryMenu, categoryTrigger } from '../test/inventory-category';

afterEach(() => vi.unstubAllGlobals());

function show(query: string, inventory: Inventory = fixtureInventory()) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={[`/${query}`]}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
}

const table = (name: string) => screen.getByRole('table', { name });
const heads = (name: string) => within(table(name)).getAllByRole('columnheader').map(cell => cell.textContent);
const row = (name: string) => screen.getByRole('link', { name }).closest('tr')!;
const cell = (rowName: string, column: string, tableName: string) => within(row(rowName)).getAllByRole('cell')[heads(tableName).indexOf(column)];

describe('directory header', () => {
  it('has no breadcrumb, visible title, description or count strip; the category switcher carries the place and the count', () => {
    const { container } = show('?kind=Device');
    expect(screen.queryByRole('navigation', { name: /breadcrumb/i })).not.toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 1, name: 'Devices' })).toHaveClass('sr-only');
    expect(container.querySelector('.inventory-page-title, .inventory-group-description, .inventory-record-count')).toBeNull();
    expect(screen.queryByText(/ of \d+ records/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Physical hardware, identity/)).not.toBeInTheDocument();
    const trigger = categoryTrigger();
    expect(trigger).toHaveAccessibleName('Inventory category: Devices');
    expect(trigger).toHaveAccessibleDescription(`${within(table('Devices')).getAllByRole('row').length - 1} records`);
  });

  it('reads "?" on the category of a denied collection, never 0', () => {
    show('?kind=Device', { ...fixtureInventory(), issues: { Cable: { status: 403 } } });
    const cables = within(categoryMenu()).getByRole('link', { name: 'Cables' });
    expect(cables.querySelector('.inventory-tab-count')).toHaveTextContent('?');
    expect(cables).toHaveAccessibleDescription('Count unknown: this collection is unavailable');
  });

  it('names the plans section "Installation plans" everywhere, with no tab rows', () => {
    const { container } = show('?kind=DeviceClaim');
    expect(categoryTrigger()).toHaveAccessibleName('Inventory category: Installation plans');
    expect(screen.getByRole('heading', { level: 1, name: 'Installation plans' })).toBeInTheDocument();
    expect(table('Installation plans')).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/Claims & registrations/);
    expect(screen.queryAllByRole('tablist')).toHaveLength(0);
  });

  it('keeps Manufacturers and Device types out of the inventory switcher; the Catalog page toggles them', () => {
    show('?kind=Device');
    const menu = categoryMenu();
    expect(within(menu).queryByRole('link', { name: 'Manufacturers' })).toBeNull();
    expect(within(menu).queryByRole('link', { name: 'Device types' })).toBeNull();
  });
});

describe('directory toolbar', () => {
  it('puts search, Refresh and Create on one row, with no Apply search and no Clear filters until one is set', () => {
    show('?kind=Device');
    const toolbar = screen.getByRole('search').parentElement!;
    expect(within(toolbar).getByRole('button', { name: 'Refresh inventory' })).toBeInTheDocument();
    expect(within(toolbar).getByRole('button', { name: 'Create device' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Apply search' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Clear filters' })).not.toBeInTheDocument();
  });

  it('gives the reason for a disabled Create as its tooltip, not as page text', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ data: { status: { allowed: false } } }) })));
    show('?kind=Device');
    const create = screen.getByRole('button', { name: 'Create device' });
    await vi.waitFor(() => expect(create).toHaveAccessibleDescription(/^Needs inventory management access$/));
    expect(create.parentElement).toHaveAttribute('title', 'Needs inventory management access');
    expect(screen.queryByText(/Viewing and management access are separate/)).not.toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('');
  });
});

describe('directory columns', () => {
  it('shows each kind its own columns instead of Details and Location / reference', () => {
    const expected: Record<string, [string, string[]]> = {
      Device: ['Devices', ['Name', 'Rack', 'U', 'Face', 'Model', 'Serial', 'Asset tag', 'Power']],
      Cable: ['Cables', ['Name', 'Medium', 'A end', 'B end', 'Lanes']],
      PowerFeed: ['Power feeds', ['Name', 'Location', 'Type', 'Voltage', 'Breaker', 'Capacity', 'Load']],
      Site: ['Operational sites', ['Name', 'Code', 'Facilities', 'Racks', 'Devices']],
      Facility: ['Facilities', ['Name', 'Code', 'Site', 'Racks', 'Devices']],
      Rack: ['Racks', ['Name', 'Height', 'Devices', 'Used', 'Power', 'Feeds']],
      DeviceClaim: ['Installation plans', ['Name', 'Rack', 'U', 'Face', 'Model', 'Device', 'State']],
      Manufacturer: ['Manufacturers', ['Name', 'Aliases', 'Device types', 'Devices']],
      DeviceType: ['Device types', ['Name', 'Manufacturer', 'Model key', 'Height', 'Depth', 'Ports', 'Devices']],
    };
    for (const [kind, [title, columns]] of Object.entries(expected)) {
      const view = show(`?kind=${kind}`);
      expect(heads(title)).toEqual(columns);
      expect(table(title).closest('[data-testid="fleet-plugin-inventory-table"]')).not.toBeNull();
      expect(table(title).querySelector('small')).toBeNull();
      view.unmount();
    }
  });

  it('names a power feed rack by its display name', () => {
    show('?kind=PowerFeed');
    expect(within(cell('Feed A', 'Location', 'Power feeds')).getByRole('link', { name: 'Rack A01' })).toBeInTheDocument();
    expect(screen.queryByText('example-rack-a01')).not.toBeInTheDocument();
  });

  it('resolves an installation plan model through the catalog', () => {
    show('?kind=DeviceClaim');
    expect(cell('Installation · A01 U2', 'Model', 'Installation plans')).toHaveTextContent(/^Example 1U compute$/);
    expect(screen.queryByText(/example-compute EX-C1/)).not.toBeInTheDocument();
  });

  it('shows the facility code without its identifier type', () => {
    show('?kind=Facility');
    expect(cell('Example Facility One', 'Code', 'Facilities')).toHaveTextContent(/^EXDC1$/);
    expect(screen.queryByText(/PremisesCode/)).not.toBeInTheDocument();
  });

  it('names catalog records by display name and never shows their record names', () => {
    show('?kind=DeviceType');
    expect(within(cell('Example 1U compute', 'Manufacturer', 'Device types')).getByRole('link', { name: 'Example Compute Co.' })).toBeInTheDocument();
    expect(within(table('Device types')).queryByText(/^example-/)).not.toBeInTheDocument();
    expect(screen.queryByText(/reference template|Not recorded/)).not.toBeInTheDocument();
  });

  it('counts racks and used units without showing free space, and colours disagreeing racks red', () => {
    show('?kind=Rack');
    expect(cell('Rack A01', 'Used', 'Racks')).toHaveTextContent(/^\d+\/42U$/);
    expect(cell('Rack A01', 'Feeds', 'Racks')).toHaveTextContent('2');
    expect(row('Rack A02')).toHaveAttribute('data-attention', 'conflict');
    expect(row('Rack A02')).toHaveAttribute('title', expect.stringMatching(/placements disagree/));
    // An unpositioned placement leaves used units unknown, never a smaller number.
    expect(cell('Rack A03', 'Used', 'Racks')).toHaveTextContent('?');
    expect(row('Rack A03')).toHaveAttribute('data-attention', 'unavailable');
    expect(screen.queryByText(/free/i)).not.toBeInTheDocument();
  });

  it('reads "?" for counts that depend on a denied collection and names nothing from it', () => {
    const inventory = fixtureInventory('devices-denied');
    show('?kind=Rack', inventory);
    expect(cell('Rack A01', 'Devices', 'Racks')).toHaveTextContent('?');
    expect(cell('Rack A01', 'Used', 'Racks')).toHaveTextContent('?');
    const devices = heads('Racks').indexOf('Devices');
    for (const line of within(table('Racks')).getAllByRole('row').slice(1)) expect(within(line).getAllByRole('cell')[devices]).toHaveTextContent('?');
  });

  it('shows "?" with the reason for a cable end whose device collection is denied', () => {
    show('?kind=Cable', fixtureInventory('devices-denied'));
    const end = cell('DAC a01-01 eth0', 'A end', 'Cables');
    expect(end).toHaveTextContent(/^\? · eth0 · Device records unavailable$/);
    expect(end.querySelector('[title]')).toHaveAttribute('title', 'Device records unavailable');
    expect(row('DAC a01-01 eth0')).toHaveAttribute('data-attention', 'unavailable');
    expect(screen.queryByText(/Related endpoint unavailable|Compute a01-01/)).not.toBeInTheDocument();
  });

  it('marks multiple registrations against one plan red', () => {
    const inventory = fixtureInventory();
    const slot = inventory.slots.find(item => item.devices.length === 1)!;
    const twin = { ...slot.devices[0], metadata: { ...slot.devices[0].metadata, name: 'synthetic-twin', uid: 'synthetic-twin' }, spec: { ...slot.devices[0].spec, displayName: 'Synthetic twin' } };
    slot.devices = [...slot.devices, twin];
    show('?kind=DeviceClaim', inventory);
    const name = slot.claim.spec.displayName!;
    expect(cell(name, 'Device', 'Installation plans')).toHaveTextContent('2 devices');
    expect(row(name)).toHaveAttribute('data-attention', 'conflict');
  });
});
