import { fireEvent, render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';
import { PhysicalInventory } from './physical-inventory';
import { FIXTURE_FACILITY, fixtureInventory } from '../lib/inventory-fixtures';
import type { Inventory } from '../inventory';

function show(inventory: Inventory = fixtureInventory(), extra = '') {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={[`/inventory?kind=Facility&record=${FIXTURE_FACILITY}&namespace=default${extra}`]}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
}
const tabs = () => within(screen.getByRole('tablist', { name: /details$/ }));
const openTab = (name: string) => fireEvent.click(tabs().getByRole('tab', { name: new RegExp(`^${name}`) }));
const unracked = () => screen.getByRole('table', { name: 'Equipment without a rack' });
const rowOf = (table: HTMLElement, name: string) => within(table).getByRole('link', { name }).closest('tr')!;

describe('Facility page', () => {
  it('puts racks, unracked equipment, feeds and cables in one strip of counted tabs, with no banners', () => {
    show();
    expect(tabs().getAllByRole('tab').map(tab => tab.textContent)).toEqual(['Racks 3', 'Without a rack 2', 'Power feeds 2', 'Cables 21']);
    expect(screen.queryByText(/Power & cables|Equipment records|That does not establish/)).toBeNull();
    expect(screen.queryByRole('list', { name: 'Record issues' })).toBeNull();
    const racks = screen.getByRole('table', { name: 'Facility racks' });
    expect(within(racks).getAllByRole('columnheader').map(cell => cell.textContent)).toEqual(['Rack', 'Height', 'Equipment', 'Power']);
  });

  it('states the header facts once: the premises code, a Site link and the location, with no footer extras', () => {
    const { container } = show();
    const header = container.querySelector('.inventory-record-summary')!;
    expect(within(header as HTMLElement).getByText('EXDC1')).toHaveAttribute('title', 'example-colo / EXDC1 (PremisesCode)');
    expect(within(header as HTMLElement).getByRole('link', { name: 'Example operational site' })).toHaveAttribute('href', '/inventory?kind=Site&record=example-site-one&namespace=default');
    expect(within(header as HTMLElement).getByTitle('Geographic location reference')).toHaveTextContent('example-region');
    expect(screen.getByRole('region', { name: 'Record' })).toHaveTextContent(/^Record example-dc1 · created \S+$/);
  });

  it('lists equipment without a rack in the rack equipment columns, and shows a missing plan as not found, never as unavailable', () => {
    show(fixtureInventory(), '&tab=unracked');
    const table = unracked();
    expect(within(table).getAllByRole('columnheader').map(cell => cell.textContent)).toEqual(['Device', 'Model', 'Face', 'Depth', 'Ports', 'Power', 'Serial', 'Asset tag', 'Plan']);
    // The fixture's facility-only devices name installation plans that are not recorded.
    for (const name of ['Imported switch 01', 'Compute retired-01']) {
      const plan = within(rowOf(table, name)).getByText('Plan not found');
      expect(plan).toHaveAttribute('data-tone', 'warning');
      expect(plan.getAttribute('title')).toMatch(/^Installation plan \S+ not found$/);
    }
    expect(screen.queryByText(/Claim details unavailable|Reported placement ·/)).toBeNull();
  });

  it('keeps an unreadable plan collection unavailable, and links a plan that exists', () => {
    const inventory = fixtureInventory();
    const view = show({ ...inventory, issues: { DeviceClaim: { status: 403 } } }, '&tab=unracked');
    expect(tabs().getByRole('tab', { name: /^Without a rack \?/ })).toBeInTheDocument();
    expect(within(rowOf(unracked(), 'Imported switch 01')).getByText('Unavailable')).toBeInTheDocument();
    expect(screen.queryByText('Plan not found')).toBeNull();
    view.unmount();
    // Give the imported switch a plan: its row links it.
    const imported = inventory.unboundDevices.find(device => device.spec.displayName === 'Imported switch 01')!;
    const claim = { ...inventory.slots[0].claim, metadata: { ...inventory.slots[0].claim.metadata, name: imported.spec.claimRef.name }, spec: { ...inventory.slots[0].claim.spec, displayName: 'Imported plan' } };
    show({ ...inventory, slots: [...inventory.slots, { key: `default/${claim.metadata.name}`, claim, devices: [imported] }], unboundDevices: inventory.unboundDevices.filter(device => device !== imported) }, '&tab=unracked');
    expect(within(rowOf(unracked(), 'Imported switch 01')).getByRole('link', { name: 'Imported plan' })).toBeInTheDocument();
  });

  it('lists feeds and cables as tables and marks a Cable that duplicates another in red', () => {
    show(fixtureInventory(), '&tab=feeds');
    const feeds = screen.getByRole('table', { name: 'Facility power feeds' });
    expect(within(feeds).getAllByRole('columnheader').map(cell => cell.textContent)).toEqual(['Feed', 'Rack', 'Capacity']);
    openTab('Cables');
    const cables = screen.getByRole('table', { name: 'Facility cables' });
    expect(within(cables).getAllByRole('columnheader').map(cell => cell.textContent)).toEqual(['Cable', 'Medium', 'Lanes', 'End A', 'End B']);
    const duplicate = rowOf(cables, 'DAC a01-01 eth1 (duplicate record)');
    expect(duplicate).toHaveAttribute('data-attention', 'conflict');
    expect(within(duplicate).getByTitle(/Records disagree/)).toBeInTheDocument();
    expect(rowOf(cables, 'DAC a01-01 eth0')).not.toHaveAttribute('data-attention');
  });

  it.each([['Rack', 'Racks'], ['PowerFeed', 'Power feeds'], ['Cable', 'Cables']] as const)('reads a denied %s collection as unknown, naming nothing from it', (kind, label) => {
    const { container } = show({ ...fixtureInventory(), issues: { [kind]: { status: 403 } } });
    expect(tabs().getByRole('tab', { name: new RegExp(`^${label} \\?$`) })).toBeInTheDocument();
    const hidden = { Rack: /Rack A01|Rack A02/, PowerFeed: /Feed A|Feed B/, Cable: /DAC a01/ }[kind];
    expect(container.querySelector(`[data-tab="${label === 'Racks' ? 'racks' : label === 'Power feeds' ? 'feeds' : 'cables'}"]`)!.textContent).not.toMatch(hidden);
  });
});
