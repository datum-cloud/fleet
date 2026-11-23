import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { MemoryRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { PhysicalInventory } from './physical-inventory';
import { fixtureInventory } from '../lib/inventory-fixtures';
import { mockTelemetrySource } from '../lib/telemetry-mock';
import { TelemetryContext, type TelemetrySource } from '../lib/telemetry';
import type { Inventory } from '../inventory';

const NOW = Date.parse('2026-11-22T12:00:00Z');
const BASE = '/plugins/metal-datumapis-com/inventory';
const FEED_A = `${BASE}?kind=PowerFeed&record=example-feed-a&namespace=default`;
const SERVER = `${BASE}?kind=Device&record=example-srv-a01-01&namespace=default`;
const RACK = `${BASE}?kind=Rack&record=example-rack-a01&namespace=default&tab=power`;

/** Renders a page with the mock scenario, or with no provider at all (the production default). */
function open(path: string, scenario?: string, inventory: Inventory = fixtureInventory()) {
  const page = <QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={[path]}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>;
  if (!scenario) return render(page);
  const source: TelemetrySource = mockTelemetrySource(scenario, NOW);
  return render(<TelemetryContext.Provider value={{ source, now: NOW }}>{page}</TelemetryContext.Provider>);
}

const path = () => screen.getByRole('list', { name: /Power path/ });
const pduStrip = () => screen.getByRole('region', { name: 'PDU a01 (rear left) outlets' });
const outlet = (region: HTMLElement, name: RegExp) => within(region).getByRole('button', { name });

describe('PowerFeed power path', () => {
  it('reads upstream → feed → PDU strip → device PSUs, with the rating formula and the continuous limit', () => {
    open(FEED_A, 'normal');
    const stages = within(path()).getAllByRole('listitem').filter(item => item.parentElement === path());
    expect(stages.map(stage => stage.querySelector('h4')?.textContent)).toEqual(['Upstream', 'Feed', 'PDUs 1', 'Devices 4']);
    expect(screen.getByText(/208 V × 30 A = 6,240 VA/)).toBeInTheDocument();
    expect(screen.getByText(/4\.99 kVA continuous \(80%\)/)).toBeInTheDocument();
    expect(within(pduStrip()).getAllByRole('button').filter(item => item.hasAttribute('data-tile'))).toHaveLength(9);
    // Outlet tiles reuse the shared port tile: shape and record state.
    const o1 = outlet(pduStrip(), /^Outlet 1: Cable recorded/);
    expect(o1).toHaveAttribute('data-shape', 'iec');
    expect(o1).toHaveAttribute('data-state', 'cabled');
    expect(outlet(pduStrip(), /^Outlet 6: No cable recorded/)).toHaveAttribute('data-state', 'none');
    const devices = path().querySelector('[data-stage="devices"]') as HTMLElement;
    expect(within(devices).getAllByRole('link', { name: 'Compute a01-01' })).toHaveLength(1);
    expect(within(devices).getByRole('link', { name: 'Compute a01-02' }).closest('li')).toHaveTextContent('Feed unknown for some PSUs');
    expect(screen.queryByText(/separate feeds in records do not prove independent supply/i)).toBeNull();
    expect(path()).not.toHaveTextContent(/redundant|verified/i);
  });

  it('fills the capacity bar and tints outlets from telemetry, with value, unit and age in accessible names', () => {
    open(FEED_A, 'normal');
    expect(screen.getByText(/^706 VA of 6\.24 kVA rated · 14% of continuous limit/)).toBeInTheDocument();
    // No row of text: the peer and Cable ride in the tile's name, after the state.
    const o1 = outlet(pduStrip(), /^Outlet 1: Cable recorded, to Compute a01-01 · PSU 1, via Power a01-01 PSU 1; telemetry 0\.92 A · 182 W · 40 s ago$/);
    expect(o1.querySelector('.inventory-port-load')).toHaveAttribute('data-tint', 'low');
    // Numbers are in the card only; the group says when it was updated, once.
    expect(pduStrip()).not.toHaveTextContent(/0\.92 A|182 W/);
    expect(within(pduStrip()).getAllByText('Updated 40 s ago')).toHaveLength(1);
    fireEvent.click(o1);
    expect(within(screen.getByRole('dialog', { name: 'Outlet 1 details' })).getByText('Current').nextElementSibling).toHaveTextContent('0.92 A40 s ago');
  });

  it('opens an outlet card with record state, Cable, peer, telemetry, a sparkline and no health', () => {
    open(FEED_A, 'normal');
    fireEvent.click(outlet(pduStrip(), /^Outlet 1:/));
    const card = screen.getByRole('dialog', { name: 'Outlet 1 details' });
    expect(card).toHaveTextContent('Cable recorded');
    expect(within(card).getByRole('link', { name: 'Compute a01-01 · PSU 1' })).toBeInTheDocument();
    const telemetry = card.querySelector('.inventory-port-card-telemetry') as HTMLElement;
    expect(within(telemetry).getByText('Current').nextElementSibling).toHaveTextContent('0.92 A40 s ago');
    expect(within(telemetry).getByText('Power').nextElementSibling).toHaveTextContent('182 W40 s ago');
    expect(within(telemetry).getByText('Output').nextElementSibling).toHaveTextContent('On40 s ago');
    expect(within(telemetry).getByRole('img', { name: /Power, last 24 hours: low \d+ W, high \d+ W/ })).toBeInTheDocument();
    expect(telemetry).toHaveTextContent('Observed only; verifies nothing and changes no record.');
    expect(within(card).getByText('Health').nextElementSibling).toHaveTextContent('No health reported');
    // The collector and its window are behind More, not in the Telemetry section.
    expect(card).not.toHaveTextContent(/PDU collector/);
    fireEvent.click(within(card).getByRole('button', { name: 'More' }));
    expect(within(card).getByText('Telemetry source').nextElementSibling).toHaveTextContent(/^PDU collector \(synthetic-pdu-collector, synthetic\) · observed .* UTC · fresh for 5 min$/);
    expect(telemetry).not.toHaveTextContent(/PDU collector/);
  });

  it('greys and labels stale values, never green', () => {
    const view = open(FEED_A, 'stale');
    const bar = view.container.querySelector('.inventory-capacity')!;
    expect(bar).toHaveAttribute('data-freshness', 'stale');
    expect(bar).toHaveTextContent(/Stale · 3 h ago/);
    expect(outlet(pduStrip(), /^Outlet 1:/).querySelector('.inventory-port-load')).toHaveAttribute('data-tint', 'stale');
    expect(outlet(pduStrip(), /^Outlet 1:/)).toHaveAccessibleName(/telemetry stale: 0\.92 A · 182 W · 3 h ago/);
    expect(within(pduStrip()).getAllByText(/^Stale · /)).toHaveLength(1);
    expect(within(pduStrip()).getByText(/^Stale · /)).toHaveTextContent('Stale · updated 3 h ago');
    fireEvent.click(outlet(pduStrip(), /^Outlet 1:/));
    const value = within(screen.getByRole('dialog')).getByText('Power').nextElementSibling!.firstElementChild!;
    expect(value).toHaveAttribute('data-freshness', 'stale');
    expect(value).toHaveTextContent('Stale · 3 h ago');
    expect(view.container.querySelector('[data-health="ok"]')).toBeNull();
  });

  it('marks high load with a glyph as well as colour', () => {
    const view = open(FEED_A, 'high-load');
    const bar = view.container.querySelector('.inventory-capacity')!;
    expect(bar).toHaveAttribute('data-level', 'high');
    expect(bar.querySelector('.inventory-capacity-glyph')).toHaveTextContent('↑');
    expect(bar).toHaveTextContent(/above 80% of the continuous limit/);
    const inlet = outlet(screen.getByRole('region', { name: 'Power outlets' }), /^Outlet 1:/);
    expect(inlet.querySelector('.inventory-port-mark')).toHaveTextContent('↑');
    expect(inlet).toHaveAccessibleName(/above 80% of the continuous limit/);
  });

  it('banners observed load on an unrecorded outlet, links to the records, and changes no record state', () => {
    open(FEED_A, 'mismatch');
    const issues = screen.getByRole('list', { name: 'Record issues' });
    expect(issues).toHaveTextContent(/Observed and recorded disagree: load is observed on Outlet 3 of Feed A, Outlet 6 of PDU a01 \(rear left\), but no Cable records them/);
    expect(within(issues).getByRole('link', { name: 'PDU a01 (rear left)' })).toHaveAttribute('href', expect.stringContaining('record=example-pdu-a01'));
    expect(within(issues).getByRole('link', { name: 'Cable records' })).toHaveAttribute('href', expect.stringContaining('kind=Cable'));
    const o6 = outlet(pduStrip(), /^Outlet 6: No cable recorded/);
    expect(o6).toHaveAttribute('data-state', 'none');
    expect(o6).toHaveAccessibleName(/observed and recorded disagree/);
    expect(o6.querySelector('.inventory-port-mark')).toHaveTextContent('≠');
    fireEvent.click(o6);
    expect(within(screen.getByRole('dialog')).getByRole('note')).toHaveTextContent(/Observed and recorded disagree/);
  });
});

describe('without a telemetry source (normal builds)', () => {
  it('reads "Not reported" and "No load reported", never 0, and shows no numbers', () => {
    const view = open(FEED_A);
    const bar = view.container.querySelector('.inventory-capacity')!;
    expect(bar).toHaveTextContent('No load reported · No telemetry source configured');
    expect(bar.querySelector('.inventory-capacity-fill')).toHaveStyle({ width: '0%' });
    // One line for the group, never one per outlet.
    expect(within(pduStrip()).getAllByText('Not reported')).toHaveLength(1);
    expect(within(screen.getByRole('region', { name: 'Power outlets' })).getAllByText('Not reported')).toHaveLength(1);
    // No ages and no measured values: only the recorded rating has numbers.
    expect(path()).not.toHaveTextContent(/ ago|\b0(\.00)? (A|W)\b|\d (W|VA) of /);
    expect(outlet(pduStrip(), /^Outlet 1:/)).toHaveAccessibleName('Outlet 1: Cable recorded, to Compute a01-01 · PSU 1, via Power a01-01 PSU 1');
    expect(outlet(pduStrip(), /^Outlet 1:/).querySelector('.inventory-port-load')).toBeNull();
    fireEvent.click(outlet(pduStrip(), /^Outlet 1:/));
    const card = screen.getByRole('dialog');
    expect(card).toHaveTextContent('TelemetryNot reported · No telemetry source configured');
    expect(within(card).getByText('Health').nextElementSibling).toHaveTextContent('No health reported');
    // No page legend repeats the telemetry source.
    expect(screen.queryByText(/Telemetry: No telemetry source configured/)).toBeNull();
  });

  it('hides the draw fact on the device page when nothing reports one, and says Not reported on the PSU card', () => {
    open(SERVER);
    // An empty fact is hidden in the header strip; nothing reads as zero or verified.
    expect(screen.queryByText(/Power draw|Draws/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /^PSU 1: Cable recorded/ }));
    expect(screen.getByRole('dialog', { name: 'PSU 1 details' })).toHaveTextContent('TelemetryNot reported · No telemetry source configured');
  });
});

describe('denied records leak nothing through telemetry', () => {
  it('names no PDU, device or outlet reading when Devices are denied', () => {
    open(FEED_A, 'normal', fixtureInventory('devices-denied'));
    expect(screen.getByText('Device details unavailable. PDUs on this feed are unknown.')).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(/PDU a01|Compute a01|Leaf a01|example-pdu|example-srv/);
    // Feed-level readings belong to the readable feed and remain.
    expect(screen.getByText(/^706 VA of 6\.24 kVA rated/)).toBeInTheDocument();
  });

  it('compares nothing when Cables are denied: states unknown, no disagreement', () => {
    open(FEED_A, 'mismatch', fixtureInventory('cables-denied'));
    expect(screen.queryByText(/Observed and recorded disagree/)).not.toBeInTheDocument();
    const outlets = screen.getByRole('region', { name: 'Power outlets' });
    expect(within(outlets).getAllByRole('button', { name: /^Outlet \d: Cable details unavailable, connections unknown/ })).toHaveLength(4);
    for (const tile of within(outlets).getAllByRole('button')) expect(tile).toHaveAttribute('data-state', 'unavailable');
    for (const tile of within(outlets).getAllByRole('button')) expect(tile).not.toHaveAccessibleName(/disagree/);
  });

  it('names no feed in PSU cards or the rack view when power feeds are denied', () => {
    const view = open(SERVER, 'normal', fixtureInventory('power-feeds-denied'));
    fireEvent.click(screen.getByRole('button', { name: /^PSU 2:/ }));
    expect(screen.getByRole('dialog')).not.toHaveTextContent(/Feed A|Feed B|example-feed|208 V/);
    view.unmount();
    open(RACK, 'normal', fixtureInventory('power-feeds-denied'));
    expect(document.body).not.toHaveTextContent(/Feed A|Feed B|example-feed|706 VA|340 VA/);
  });
});

describe('keyboard', () => {
  it('gives each strip one tab stop, moves with arrows, Home and End, and returns focus on Escape', () => {
    open(FEED_A, 'normal');
    const tiles = within(pduStrip()).getAllByRole('button').filter(item => item.hasAttribute('data-tile'));
    expect(tiles.filter(tile => tile.tabIndex === 0)).toHaveLength(1);
    tiles[0].focus();
    fireEvent.keyDown(tiles[0], { key: 'ArrowDown' });
    expect(document.activeElement).toBe(tiles[1]);
    fireEvent.keyDown(tiles[1], { key: 'End' });
    expect(document.activeElement).toBe(tiles.at(-1));
    fireEvent.keyDown(tiles.at(-1)!, { key: 'Home' });
    expect(document.activeElement).toBe(tiles[0]);
    fireEvent.click(tiles[1]);
    expect(tiles[1]).toHaveAttribute('aria-expanded', 'true');
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(document.activeElement).toBe(tiles[1]);
  });
});

describe('rack Power tab redundancy view', () => {
  it('shows feeds side by side and each device once, flagging single-feed and unknown paths without claiming redundancy', () => {
    open(RACK, 'normal');
    const grid = screen.getByRole('table', { name: 'Device PSUs by recorded feed' });
    expect(within(grid).getAllByRole('columnheader').map(cell => cell.textContent?.split(/AC|Path/)[0].replace('? ', ''))).toEqual(['Device', 'Feed A', 'Feed B', 'Feed unknown']);
    const rowFor = (name: string) => within(grid).getByRole('rowheader', { name: new RegExp(`^${name}`) }).closest('[role="row"]') as HTMLElement;
    expect(within(grid).getAllByRole('rowheader', { name: /^Compute a01-01/ })).toHaveLength(1);
    expect(rowFor('PDU a01')).toHaveTextContent('Single feed (recorded)');
    expect(rowFor('Compute a01-01')).toHaveTextContent('Separate feeds (recorded)');
    expect(rowFor('Storage a01-04')).toHaveTextContent('Feed unknown');
    const cells = within(rowFor('Compute a01-01')).getAllByRole('cell');
    expect(cells[0]).toHaveTextContent('PSU 1 via PDU a01 (rear left) · Outlet 1');
    expect(cells[1]).toHaveTextContent('PSU 2 direct');
    expect(screen.queryByText(/Separate feeds in records do not prove independent supply/)).toBeNull();
    expect(screen.getByRole('region', { name: 'Feeds and PSUs' })).not.toHaveTextContent(/redundant|verified redundan/i);
    // Each feed has a compact capacity bar.
    expect(grid.querySelectorAll('.inventory-capacity[data-compact]')).toHaveLength(2);
  });
});

describe('device PSU telemetry', () => {
  it('shows per-PSU input power and observed status, and a draw summary', () => {
    open(SERVER, 'normal');
    // The draw is one short fact in the header's identity strip; the full draw text and age are its tooltip and accessible text.
    const draw = screen.getByTitle('Draws ~358 W · 40 s ago');
    expect(draw.querySelector('.inventory-visually-hidden')).toHaveTextContent(/^Draws ~358 W · 40 s ago$/);
    expect(draw).not.toHaveTextContent(/verified/);
    expect(draw).toHaveTextContent(/^358 W/);
    expect(draw.closest('dl')).toHaveClass('inventory-fact-strip');
    const psu = screen.getByRole('button', { name: /^PSU 1: Cable recorded/ });
    expect(psu).toHaveAccessibleName(/telemetry 182 W · 40 s ago, status OK, 40 s ago/);
    fireEvent.click(psu);
    const card = screen.getByRole('dialog', { name: 'PSU 1 details' });
    expect(within(card).getByText('Input power').nextElementSibling).toHaveTextContent('182 W40 s ago');
    expect(card.querySelector('[data-health="ok"]')).toHaveTextContent('Observed OK · 40 s ago');
  });

  it('marks a faulted PSU with a glyph and words', () => {
    open(`${BASE}?kind=Device&record=example-srv-a01-02&namespace=default`, 'high-load');
    const psu = screen.getByRole('button', { name: /^PSU 1:/ });
    expect(psu).toHaveAccessibleName(/status Fault \(Input over temperature\)/);
    expect(psu.querySelector('.inventory-port-mark')).toHaveTextContent('✕');
  });
});
