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

describe('About this record', () => {
  it('collapses generated identity and keeps it out of the header', () => {
    const { container } = show(record('Device', 'example-srv-a01-01'));
    const about = screen.getByText('About this record').closest('details')!;
    expect(about).not.toHaveAttribute('open');
    for (const text of ['Record name', 'example-srv-a01-01', 'Namespace', 'default', 'Resource UID', 'synthetic-example-srv-a01-01']) expect(within(about).getByText(text)).toBeInTheDocument();
    const header = container.querySelector('.inventory-record-header')!;
    expect(header).toHaveTextContent('Compute a01-01');
    expect(header.textContent).not.toMatch(/example-srv-a01-01|synthetic-|default/);
    expect(screen.queryByText(/Devices \/ example-srv-a01-01/)).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Back to devices/ })).not.toBeInTheDocument();
  });

  it.each([['Cable', 'example-brk-swp49'], ['PowerFeed', 'example-feed-a'], ['Facility', 'example-dc1'], ['Site', 'example-site-one'], ['DeviceType', 'example-server-1u'], ['Manufacturer', 'example-compute'], ['DeviceClaim', 'example-srv-a01-01-install']])('puts the %s record name only in the disclosure', (kind, name) => {
    show(record(kind, name));
    const matches = screen.getAllByText(name, { exact: true });
    for (const match of matches) expect(match.closest('details.inventory-about')).not.toBeNull();
  });
});

describe('detail tabs held in the URL', () => {
  it('selects the first tab by default and hides the other panels', () => {
    const { container } = show(record('Device', 'example-srv-a01-01'));
    expect(tabs().getByRole('tab', { name: /^Components/ })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('region', { name: 'Components' })).toBeVisible();
    expect(container.querySelector('[data-tab="location"]')).toHaveAttribute('hidden');
    expect(url().get('tab')).toBeNull();
  });

  it('writes the selected tab to the URL and keeps other parameters', () => {
    show(record('Device', 'example-srv-a01-01', '&q=compute'));
    fireEvent.click(tabs().getByRole('tab', { name: 'Location' }));
    expect(url().get('tab')).toBe('location');
    expect(url().get('record')).toBe('example-srv-a01-01');
    expect(url().get('q')).toBe('compute');
    const location = screen.getByRole('region', { name: 'Location' });
    expect(location).toBeVisible();
    // The guarded correction entry point lives here, still behind its permission check.
    expect(within(location).getByRole('button', { name: /Correct location|Add reported location/ })).toBeDisabled();
    expect(within(location).getByText(/not physically verified/)).toBeInTheDocument();
  });

  it('opens a tab named in the URL and falls back for unknown values', () => {
    const view = show(record('Device', 'example-srv-a01-01', '&tab=registration'));
    expect(tabs().getByRole('tab', { name: 'Registration' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('region', { name: 'Registration' })).toHaveTextContent('example-provider/Host/synthetic-host-0001');
    view.unmount();
    show(record('Device', 'example-srv-a01-01', '&tab=not-a-tab'));
    expect(tabs().getByRole('tab', { name: /^Components/ })).toHaveAttribute('aria-selected', 'true');
  });

  it('does not carry the tab into links to other records', () => {
    show(record('Device', 'example-srv-a01-01', '&tab=location'));
    const rack = within(screen.getByRole('region', { name: 'Location' }).ownerDocument.querySelector('.inventory-record-header') as HTMLElement).getByRole('link', { name: 'Rack A01' });
    expect(new URL(rack.getAttribute('href')!, 'https://inventory.test').searchParams.get('tab')).toBeNull();
  });

  it('shows counts on tabs and unknown counts when a collection is denied', () => {
    const inventory = fixtureInventory('cables-denied');
    show(record('Device', 'example-srv-a01-01'), inventory);
    expect(tabs().getByRole('tab', { name: 'Cable records ?' })).toBeInTheDocument();
    expect(tabs().queryByRole('tab', { name: 'Cable records 0' })).not.toBeInTheDocument();
  });
});

describe('issue banners', () => {
  const issues = () => screen.queryByRole('list', { name: 'Record issues' });

  it('lead with cable disagreements and link to the connection view', () => {
    show(record('Device', 'example-srv-a01-01'));
    const banner = within(issues()!).getByText(/Records disagree on 2 connections/).closest('li')!;
    expect(banner).toHaveAttribute('data-tone', 'danger');
    expect(within(banner).getByRole('link', { name: 'Review' })).toHaveAttribute('href', '#device-connections');
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

  it('say unreadable cables are unknown, not free', () => {
    show(record('Device', 'example-srv-a01-01'), fixtureInventory('cables-denied'));
    expect(within(issues()!).getByText(/connections are unknown, not free/)).toBeInTheDocument();
  });

  it('stay away when nothing is wrong', () => {
    show(record('Device', 'example-sto-a01-03'));
    expect(issues()).toBeNull();
    expect(screen.getByRole('heading', { name: 'Storage a01-03' })).toBeInTheDocument();
  });

  it('flag a Cable end whose record is missing', () => {
    show(record('Cable', 'example-dac-a01-02-eth1'));
    expect(within(issues()!).getByText(/End B names a record not found: example-leaf-legacy/)).toBeInTheDocument();
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
    expect(screen.getByText('Serial number')).toBeInTheDocument();
    // Verification stays visible as unknown, never as verified.
    expect(screen.getByText('Unknown — no verification evidence is available.')).toBeInTheDocument();
  });

  it('keeps unknown, not free on power outlets and never shows a denied outlet as free', () => {
    const view = show(record('PowerFeed', 'example-feed-a'));
    const outlets = screen.getByRole('region', { name: 'Power outlets' });
    // Outlets are tiles: "No cable recorded … unknown, not free" is in each empty tile's name, not a row of text.
    expect(within(outlets).getAllByRole('button', { name: /^Outlet \d: No cable recorded, unknown, not free$/ })).toHaveLength(3);
    // The peer is one click away, in the outlet's card.
    fireEvent.click(within(outlets).getByRole('button', { name: /^Outlet 1: Cable recorded/ }));
    expect(within(screen.getByRole('dialog', { name: 'Outlet 1 details' })).getByRole('link', { name: 'PDU a01 (rear left) · Inlet' })).toBeInTheDocument();
    expect(screen.getByText(/No cable recorded means unknown, not free/)).toBeInTheDocument();
    expect(screen.getByText(/supply ratings do not establish available power/)).toBeInTheDocument();
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
    expect(within(table).getAllByRole('columnheader').map(cell => cell.textContent)).toEqual(['Name', 'Model', 'Location', 'Serial number']);
    fireEvent.click(within(table).getByText('SYNTH-EX-01'));
    expect(url().get('record')).toBe('example-srv-a01-01');
    expect(screen.getByRole('heading', { name: 'Compute a01-01' })).toBeInTheDocument();
  });

  it('keeps unknown and planned locations labelled in the list', () => {
    show('?kind=Device');
    const table = screen.getByRole('table', { name: 'Devices' });
    expect(within(table).getByText('Rack A03 · position unknown')).toBeInTheDocument();
    expect(within(table).getByText('Claim record not found')).toBeInTheDocument();
  });

  it('names a Cable end once when every lane shares it', () => {
    show('?kind=Cable');
    const row = screen.getByRole('link', { name: 'Breakout swp49' }).closest('tr')!;
    expect(within(row).getAllByText('Leaf a01')).toHaveLength(1);
    expect(within(row).getByText('swp49 · 4 lanes')).toBeInTheDocument();
  });
});
