import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { MemoryRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { FIXTURE_NAMES, fixtureInventory } from '../lib/inventory-fixtures';
import { mockTelemetrySource } from '../lib/telemetry-mock';
import { TelemetryContext } from '../lib/telemetry';
import { PhysicalInventory } from './physical-inventory';
import { groupFreshness } from './power-feed-path';

// Synthetic fixture records only.
const NOW = Date.parse('2026-11-22T12:00:00Z');
const BASE = '/plugins/metal-datumapis-com/inventory';
const FEED_A = `${BASE}?kind=PowerFeed&record=example-feed-a&namespace=default`;
const RACK_POWER = `${BASE}?kind=Rack&record=example-rack-a01&namespace=default&tab=power`;

function open(path: string, scenario?: string, inventory: Inventory = fixtureInventory()) {
  const page = <QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={[path]}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>;
  if (!scenario) return render(page);
  return render(<TelemetryContext.Provider value={{ source: mockTelemetrySource(scenario, NOW), now: NOW }}>{page}</TelemetryContext.Provider>);
}

/** The fixture with one record replaced, leaving the shared dataset untouched. */
function replaced(kind: string, name: string, change: (record: PhysicalRecord) => PhysicalRecord): Inventory {
  const inventory = fixtureInventory();
  return { ...inventory, physical: inventory.physical.map(record => record.kind === kind && record.metadata.name === name ? change(structuredClone(record)) : record) };
}

const feedOutlets = () => screen.getByRole('region', { name: 'Power outlets' });
const pduOutlets = () => screen.getByRole('region', { name: 'PDU a01 (rear left) outlets' });
const tiles = (region: HTMLElement) => within(region).getAllByRole('button').filter(item => item.hasAttribute('data-tile'));

describe('outlets as a tile strip', () => {
  it('draws a small strip as labelled tiles shaped by connector kind, with no rows of text', () => {
    open(FEED_A, 'normal');
    const strip = tiles(feedOutlets());
    expect(strip).toHaveLength(4);
    expect(strip.map(tile => tile.textContent)).toEqual(['O1', 'O2', 'O3', 'O4']);
    for (const tile of strip) expect(tile).toHaveAttribute('data-shape', 'iec');
    expect(within(feedOutlets()).getByRole('list', { name: 'Power outlets' })).not.toHaveAttribute('data-compact');
    expect(feedOutlets().querySelectorAll('.inventory-port-number')).toHaveLength(0);
    // Empty outlets are tiles, not rows: no "No cable recorded" text, only the tile's state and name.
    expect(within(feedOutlets()).queryByText('No cable recorded')).not.toBeInTheDocument();
    expect(strip[1]).toHaveAttribute('data-state', 'none');
    expect(strip[1]).toHaveAccessibleName(/^Outlet 2: No cable recorded; telemetry /);
  });

  it('applies the dense rule above twelve outlets: unlabelled tiles with edge numbers', () => {
    const dense = replaced('PowerFeed', 'example-feed-a', feed => ({ ...feed, spec: { ...feed.spec, terminations: Array.from({ length: 16 }, (_, index) => ({ id: `outlet-${index + 1}`, name: `Outlet ${index + 1}`, type: 'PowerOutlet' as const, connector: 'IEC C13', positions: 1 })) } }));
    open(FEED_A, 'normal', dense);
    const strip = tiles(feedOutlets());
    expect(strip).toHaveLength(16);
    for (const tile of strip) expect(tile.querySelector('.inventory-port-label')).toBeNull();
    expect(within(feedOutlets()).getByRole('list', { name: 'Power outlets' })).toHaveAttribute('data-compact');
    // First, every eighth, and last.
    expect([...feedOutlets().querySelectorAll('.inventory-port-number')].map(item => item.textContent).filter(Boolean)).toEqual(['1', '9', '16']);
    // Accessible names still give the outlet, state and peer.
    expect(strip[0]).toHaveAccessibleName(/^Outlet 1: Cable recorded, to PDU a01 \(rear left\) · Inlet/);
  });

  it('draws a PDU with a recorded layout through the faceplate renderer, cropped to its banks', () => {
    open(FEED_A, 'normal');
    const drawing = within(pduOutlets()).getByRole('group', { name: /^PDU a01 \(rear left\) outlets\. Arrow keys/ });
    expect(drawing).toHaveClass('inventory-drawing');
    expect(drawing).toHaveAttribute('data-fit');
    expect(drawing.querySelector('.inventory-chassis')).toHaveAttribute('data-fit');
    // Cropped and fitted, so the "drawn full width" note does not apply.
    expect(pduOutlets()).not.toHaveTextContent('Width not recorded');
    expect(tiles(pduOutlets()).map(tile => tile.getAttribute('aria-label')?.split(':')[0])).toEqual(['Inlet', ...Array.from({ length: 8 }, (_, index) => `Outlet ${index + 1}`)]);
    // The load tint and marks are on the drawn tiles.
    expect(within(pduOutlets()).getByRole('button', { name: /^Outlet 1:/ }).querySelector('.inventory-port-load')).toHaveAttribute('data-tint', 'low');
  });

  it('falls back to a tile strip for a PDU whose model records no layout', () => {
    const plain = replaced('DeviceType', 'example-pdu-8', type => ({ ...type, spec: { ...type.spec, faceplate: undefined, componentTemplates: type.spec.componentTemplates?.map(({ layout: _layout, ...template }) => template) } }));
    open(FEED_A, 'normal', plain);
    expect(pduOutlets().querySelector('.inventory-drawing')).toBeNull();
    const strip = tiles(pduOutlets());
    expect(strip.map(tile => tile.textContent)).toEqual(['Inlet', 'O1', 'O2', 'O3', 'O4', 'O5', 'O6', 'O7', 'O8']);
    expect(strip[1]).toHaveAttribute('data-shape', 'iec');
  });

  it('marks high load on the tile with a glyph, and keeps numbers in the card', () => {
    open(FEED_A, 'high-load');
    const inlet = within(feedOutlets()).getByRole('button', { name: /^Outlet 1:/ });
    expect(inlet.querySelector('.inventory-port-mark')).toHaveTextContent('↑');
    expect(inlet.querySelector('.inventory-port-load')).toHaveAttribute('data-tint', 'high');
    expect(feedOutlets()).not.toHaveTextContent(/\d A\b|\d W\b|kW/);
    fireEvent.click(inlet);
    expect(within(screen.getByRole('dialog', { name: 'Outlet 1 details' })).getByText('Current').nextElementSibling).toHaveTextContent('21.1 A35 s ago');
  });
});

describe('an "updated" line per group only when some reading is stale or missing', () => {
  it.each([
    ['normal', undefined, undefined],
    ['stale', 'Stale · updated 3 h ago', 'Stale · updated 3 h ago'],
    ['missing', 'Not reported', 'Updated 50 s ago · 6 not reported'],
    [undefined, 'Not reported', 'Not reported'],
  ])('%s: the feed and the PDU each say it at most once', (scenario, feed, pdu) => {
    open(FEED_A, scenario);
    for (const [region, text] of [[feedOutlets(), feed], [pduOutlets(), pdu]] as const) {
      const lines = region.querySelectorAll('.inventory-power-updated');
      // All fresh: the ages are in each tile's name and card, so the group says nothing.
      expect(lines).toHaveLength(text ? 1 : 0);
      if (text) expect(lines[0]).toHaveTextContent(text);
      // No per-outlet ages.
      expect(within(region).queryAllByText(/ ago$/).filter(item => !item.classList.contains('inventory-power-updated'))).toHaveLength(0);
    }
  });

  it('summarises fresh, stale and missing readings honestly', () => {
    const at = (seconds: number, window = 300) => ({ value: 1, origin: { collector: 'pdu' as const, name: 'synthetic' }, observedAt: new Date(NOW - seconds * 1000).toISOString(), staleAfterSeconds: window });
    expect(groupFreshness([], NOW)).toEqual({ text: 'Not reported', freshness: 'missing' });
    expect(groupFreshness([{ powerW: at(20) }, { currentA: at(70) }], NOW)).toEqual({ text: 'Updated 1 min ago', freshness: 'fresh', complete: true });
    expect(groupFreshness([{ powerW: at(20) }, { powerW: at(4000) }, undefined], NOW)).toEqual({ text: 'Updated 20 s ago · 1 stale, last 1 h ago · 1 not reported', freshness: 'stale' });
    // A value without a window is invalid, so it is not reported, never fresh.
    expect(groupFreshness([{ powerW: at(20, 0) }], NOW)).toEqual({ text: 'Not reported', freshness: 'missing' });
  });
});

describe('outlet and PSU cards', () => {
  it('shows no record-identity rows and no More on feed or PDU outlet cards', () => {
    open(FEED_A, 'normal');
    for (const region of [feedOutlets(), pduOutlets()]) {
      fireEvent.click(within(region).getByRole('button', { name: /^Outlet 2:/ }));
      const card = screen.getByRole('dialog', { name: 'Outlet 2 details' });
      expect(card).not.toHaveTextContent(/Recorded on this|Record|Peer record|example-/);
      expect(within(card).queryByRole('button', { name: 'More' })).toBeNull();
      fireEvent.click(within(card).getByRole('button', { name: 'Close Outlet 2' }));
    }
  });

  it('keeps Telemetry in the card body, with the collector as its tooltip, for a PSU too', () => {
    open(`${BASE}?kind=Device&record=${FIXTURE_NAMES.server}&namespace=default`, 'normal');
    fireEvent.click(screen.getByRole('button', { name: /^PSU 1: Cable recorded/ }));
    const card = screen.getByRole('dialog', { name: 'PSU 1 details' });
    const telemetry = card.querySelector('.inventory-port-card-telemetry')!;
    expect(telemetry).toHaveTextContent(/^TelemetryInput power182 W40 s ago/);
    expect(card).not.toHaveTextContent(/BMC collector|synthetic-bmc-collector/);
    expect(card.querySelector('[data-health="ok"]')).toHaveTextContent(/^OK · 40 s ago$/);
    expect(telemetry.parentElement).toHaveAttribute('title', expect.stringMatching(/^BMC collector \(synthetic-bmc-collector, synthetic\) · observed .* · fresh for 10 min$/));
  });
});

describe('rack Power tab without duplicate lists', () => {
  it('drops the "Feed outlets" and "PDUs" sections and keeps their facts in the grid', () => {
    open(RACK_POWER, 'normal');
    expect(screen.queryByRole('heading', { name: /^Feed outlets/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: /^PDUs/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Power feeds' })).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'PDUs' })).not.toBeInTheDocument();
    expect(screen.queryByRole('list', { name: /outlets in use$/ })).not.toBeInTheDocument();
    const grid = screen.getByRole('table', { name: 'Device PSUs by recorded feed' });
    // Each feed's terminations, with what each is cabled to, are the feed column's tiles and cards.
    const feedA = within(grid).getByRole('list', { name: 'Feed A outlets' });
    expect(within(feedA).getByRole('button', { name: /^Outlet 1: Cable recorded, to PDU a01 \(rear left\) · Inlet, via Power PDU inlet/ })).toBeInTheDocument();
    expect(within(feedA).getAllByRole('button', { name: /No cable recorded/ })).toHaveLength(3);
    const feedB = within(grid).getByRole('list', { name: 'Feed B outlets' });
    expect(within(feedB).getByRole('button', { name: /^Outlet 1: Cable recorded, to Compute a01-01 · PSU 2/ })).toBeInTheDocument();
    expect(within(feedB).getByRole('button', { name: /^Outlet 2: Cable recorded, to Leaf a01 · PSU 2/ })).toBeInTheDocument();
    fireEvent.click(within(feedA).getByRole('button', { name: /^Outlet 1:/ }));
    expect(within(screen.getByRole('dialog', { name: 'Outlet 1 details' })).getByRole('link', { name: 'PDU a01 (rear left) · Inlet' })).toBeInTheDocument();
    // The tiles carry the outlet states, so there is no count line or separate power-path link under them.
    expect(grid).not.toHaveTextContent(/\d+ outlets · /);
    expect(within(grid).queryByRole('link', { name: 'Power path' })).toBeNull();
    // The PDU has its own row; its outlets and their use are on its own page.
    const pduRow = within(grid).getByRole('rowheader', { name: /^PDU a01/ });
    expect(pduRow).not.toHaveTextContent(/outlets have a cable recorded/);
    expect(within(pduRow).getByRole('link', { name: 'PDU a01 (rear left)' })).toHaveAttribute('href', expect.stringContaining('record=example-pdu-a01'));
  });

  it('still lists the PDU when power feeds are denied, naming no feed', () => {
    open(RACK_POWER, 'normal', fixtureInventory('power-feeds-denied'));
    expect(screen.getByText('Power feed details unavailable.')).toBeInTheDocument();
    expect(screen.getByRole('rowheader', { name: /^PDU a01/ })).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(/Feed A|Feed B|example-feed/);
  });
});

describe('a single-feed device in the fixtures', () => {
  it('flags the server whose PSUs both reach Feed A, with both PSUs in that column', () => {
    open(RACK_POWER, 'normal');
    const grid = screen.getByRole('table', { name: 'Device PSUs by recorded feed' });
    const row = within(grid).getByRole('rowheader', { name: /^Compute a01-05/ }).closest('[role="row"]') as HTMLElement;
    expect(row).toHaveAttribute('data-class', 'single');
    // Colour marks the row; the words are its tooltip and accessible text, not a visible badge.
    expect(row).toHaveAttribute('title', 'Single feed (recorded)');
    expect(row.querySelector('.inventory-feed-class')).toBeNull();
    const [feedA, feedB] = within(row).getAllByRole('cell');
    expect(feedA).toHaveTextContent('PSU 1 via PDU a01 (rear left) · Outlet 4');
    expect(feedA).toHaveTextContent('PSU 2 via PDU a01 (rear left) · Outlet 5');
    expect(feedB).toHaveAttribute('data-empty');
    expect(screen.getByRole('region', { name: 'Feeds and PSUs' }).querySelector('h4')).toHaveTextContent(/2 on a single feed$/);
  });

  it('shows on the feed path with an amber edge, and its device page draws both PSUs to the PDU', () => {
    const view = open(FEED_A, 'normal');
    const devices = screen.getByRole('list', { name: /Power path/ }).querySelector('[data-stage="devices"]') as HTMLElement;
    const cell = within(devices).getByRole('link', { name: 'Compute a01-05' }).closest('td')!;
    expect(cell).toHaveAttribute('data-class', 'single');
    expect(cell).toHaveAttribute('title', 'Single feed (recorded)');
    expect(cell).toHaveAttribute('rowspan', '2');
    const rows = [cell.closest('tr')!, cell.closest('tr')!.nextElementSibling!];
    expect(rows.map(row => row.textContent)).toEqual([expect.stringMatching(/PSU 1Outlet 4 on PDU a01 \(rear left\)/), expect.stringMatching(/^PSU 2Outlet 5 on PDU a01 \(rear left\)/)]);
    view.unmount();
    open(`${BASE}?kind=Device&record=${FIXTURE_NAMES.singleFeedServer}&namespace=default`);
    expect(screen.getByRole('button', { name: /^PSU 1: Cable recorded, to PDU a01 \(rear left\) · Outlet 4/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^PSU 2: Cable recorded, to PDU a01 \(rear left\) · Outlet 5/ })).toBeInTheDocument();
  });
});
