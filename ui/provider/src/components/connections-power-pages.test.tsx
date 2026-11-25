import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { PhysicalInventory } from './physical-inventory';
import { tileLoad } from './power-telemetry';
import { fixtureInventory } from '../lib/inventory-fixtures';
import type { Inventory } from '../inventory';

// Synthetic fixture records only.
const NOW = Date.parse('2026-11-22T12:00:00Z');
const styles = readFileSync('src/components/physical-inventory.css', 'utf8');
const record = (kind: string, name: string) => `/inventory?kind=${kind}&record=${name}&namespace=default`;
function show(path: string, inventory: Inventory = fixtureInventory()) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={[path]}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
}
const at = (value: number, seconds = 40) => ({ value, origin: { collector: 'bmc' as const, name: 'synthetic' }, observedAt: new Date(NOW - seconds * 1000).toISOString(), staleAfterSeconds: 600 });
/** The declarations of every rule whose selector names this load tint. */
const tintRules = (tint: string) => [...styles.matchAll(/([^{}]+)\{([^}]*)\}/g)].filter(([, selector]) => selector.includes(`.inventory-port-load[data-tint="${tint}"]`)).map(([, , body]) => body);

describe('a power reading with no limit to scale against', () => {
  it('is a neutral "reported" bar, while over-limit and disagreeing readings stay red', () => {
    expect(tileLoad({ powerW: at(182) }, NOW, undefined, false)).toEqual({ tint: 'reported', fraction: 1 });
    expect(tileLoad({ powerW: at(0) }, NOW, undefined, false)).toEqual({ tint: 'idle', fraction: 0 });
    expect(tileLoad({ powerW: at(182) }, NOW, undefined, true)).toMatchObject({ tint: 'mismatch', markKind: 'mismatch' });
    expect(tileLoad({ currentA: at(40) }, NOW, 24, false)).toMatchObject({ tint: 'over', markKind: 'over' });
  });

  it('is never painted with the destructive colour', () => {
    expect(tintRules('reported').length).toBeGreaterThan(0);
    for (const body of tintRules('reported')) expect(body).not.toMatch(/destructive/);
    for (const tint of ['over', 'mismatch']) expect(tintRules(tint).join(' ')).toMatch(/--destructive/);
  });
});

describe('Cable page', () => {
  it('marks the end another Cable also names in red, naming and linking the other Cable', () => {
    show(record('Cable', 'example-dac-a01-01-eth1-dup'));
    const line = document.querySelector('.inventory-cable-line') as HTMLElement;
    const [server, leaf] = [...line.querySelectorAll<HTMLElement>('.inventory-cable-end')];
    expect(server).toHaveAttribute('data-state', 'conflict');
    expect(server).toHaveAttribute('title', 'Compute a01-01 eth1 is also named by DAC a01-01 eth1');
    expect(server).toHaveTextContent('Records disagree: Compute a01-01 eth1 is also named by DAC a01-01 eth1');
    expect(within(server).getByRole('link', { name: 'DAC a01-01 eth1' })).toHaveAttribute('href', expect.stringContaining('record=example-dac-a01-01-eth1&'));
    // The leaf port is named once: no mark.
    expect(leaf).not.toHaveAttribute('data-state');
    expect(leaf).not.toHaveAttribute('title');
  });

  it('marks the original Cable too, naming the duplicate', () => {
    show(record('Cable', 'example-dac-a01-01-eth1'));
    const server = document.querySelector('.inventory-cable-end[data-state="conflict"]') as HTMLElement;
    expect(server).toHaveAttribute('title', 'Compute a01-01 eth1 is also named by DAC a01-01 eth1 (duplicate record)');
  });

  it('shows a one-lane Cable as one line, with no lane table and no lane count', () => {
    show(record('Cable', 'example-dac-a01-01-eth0'));
    expect(screen.queryByRole('table', { name: 'Cable lanes' })).toBeNull();
    expect(document.querySelector('.inventory-cable-line')).toHaveTextContent(/^Compute a01-01 · eth0 ⟷to Leaf a01 · swp1$/);
    expect(document.querySelector('.inventory-cable-end[data-state]')).toBeNull();
    expect(document.querySelector('.inventory-record-header')).not.toHaveTextContent(/\blanes?\b/);
  });

  it('names a breakout\'s shared device and port once, in the column head, with one line per lane', () => {
    show(record('Cable', 'example-brk-swp49'));
    const lanes = screen.getByRole('table', { name: 'Cable lanes' });
    const [head, ...rows] = within(lanes).getAllByRole('row');
    expect(within(head).getAllByRole('columnheader')[1]).toHaveTextContent(/^End ALeaf a01 · swp49$/);
    expect(rows.map(row => within(row).getAllByRole('cell').map(cell => cell.textContent))).toEqual([
      ['1', 'position 1', 'Storage a01-03 · eth0'],
      ['2', 'position 2', 'Storage a01-04 · eth0'],
      ['3', 'position 3', 'Storage a02-01 · eth0'],
      ['4', 'position 4', 'Compute a02-02 · eth0'],
    ]);
    expect(document.querySelector('.inventory-record-header')).toHaveTextContent(/· 4 lanes/);
  });
});

describe('Installation plan page', () => {
  const PLAN = record('DeviceClaim', 'example-srv-a01-01-install');

  it('states each fact once: no lifecycle badge, no registration count, no empty connections tab', () => {
    show(PLAN);
    const header = document.querySelector('.inventory-record-header')!;
    expect(header).not.toHaveTextContent(/INSTALLATION$|registered/i);
    expect(screen.queryByText(/intent \/ history/)).toBeNull();
    expect(screen.getByText('Placement')).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: /Planned connections/ })).toBeNull();
    expect(screen.queryByText('No cable references recorded.')).toBeNull();
  });

  it('names the directory once, in the back link, as Installation plans', () => {
    show(PLAN);
    const back = document.querySelector('.inventory-back-link')!;
    expect(back).toHaveTextContent(/^Installation plans$/);
    expect(document.querySelector('nav[aria-label="breadcrumb"]')).toBeNull();
    expect(document.body).not.toHaveTextContent(/Claims & registrations/);
  });

  it('links the expected hardware to its model, and marks a registered device reporting another model in red', () => {
    show(PLAN);
    expect(screen.getByRole('link', { name: 'Example 1U compute' })).toHaveAttribute('href', expect.stringContaining('kind=DeviceType'));
    expect(document.querySelector('.inventory-claim-mismatch')).toBeNull();
  });

  it('marks a registered device that reports another model', () => {
    const inventory = fixtureInventory();
    const slot = inventory.slots.find(item => item.claim.metadata.name === 'example-srv-a01-01-install')!;
    slot.devices[0] = { ...slot.devices[0], spec: { ...slot.devices[0].spec, identity: { ...slot.devices[0].spec.identity, model: 'EX-OTHER' } } };
    show(PLAN, inventory);
    const mismatch = document.querySelector('.inventory-claim-mismatch')!;
    expect(mismatch).toHaveAttribute('data-state', 'conflict');
    expect(mismatch).toHaveAttribute('title', expect.stringMatching(/^Reports example-compute EX-OTHER; the plan expects example-compute /));
    expect(mismatch).toHaveTextContent(/Records disagree: Reports example-compute EX-OTHER/);
    expect(within(mismatch as HTMLElement).getByRole('link', { name: 'Compute a01-01' })).toBeInTheDocument();
  });
});
