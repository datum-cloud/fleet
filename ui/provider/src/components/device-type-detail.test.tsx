import { fireEvent, render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';
import { PhysicalInventory } from './physical-inventory';
import { modelDrawingModel } from './faceplate-drawing';
import { fixtureInventory, inventoryFixture } from '../lib/inventory-fixtures';
import type { Inventory } from '../inventory';

function show(name: string, inventory: Inventory = fixtureInventory(), extra = '') {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={[`/inventory?kind=DeviceType&record=${name}&namespace=default${extra}`]}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
}
const model = (name: string) => inventoryFixture.physical.find(record => record.kind === 'DeviceType' && record.metadata.name === name)!;
const components = () => screen.getByRole('region', { name: 'Components table' });
const heads = (table: HTMLElement) => within(table).getAllByRole('columnheader').map(cell => cell.textContent);

describe('DeviceType page', () => {
  it('draws the faceplate from the model layout with every port catalog only and nothing cabled', () => {
    show('example-server-1u');
    const drawing = screen.getByRole('group', { name: /^Model faceplate\./ });
    expect(within(drawing).getByRole('group', { name: 'Front face' })).toBeInTheDocument();
    expect(within(drawing).getByRole('group', { name: 'Rear face' })).toBeInTheDocument();
    const ports = within(drawing).getAllByRole('button');
    expect(ports.length).toBeGreaterThan(0);
    expect(ports.every(port => port.getAttribute('data-state') === 'catalog')).toBe(true);
    expect(screen.queryByText(/rack face unknown|rack front|rack rear/)).toBeNull();
  });

  it('builds the model-only drawing without a Device: no recorded components, no Cables', () => {
    const drawing = modelDrawingModel(model('example-leaf-48x25-8x100'));
    expect(drawing.layout.recorded).toBe(true);
    expect(drawing.recorded.size).toBe(0);
    expect(drawing.cablesDenied).toBe(false);
    expect([...drawing.tiles.values()].every(tile => tile.state === 'catalog' && tile.links.every(link => !link.cable))).toBe(true);
    expect(drawing.tiles.size).toBe((model('example-leaf-48x25-8x100').spec.componentTemplates ?? []).length);
  });

  it('lists components in one dense table, with Positions only when some component has several', () => {
    show('example-leaf-48x25-8x100');
    const table = within(components()).getByRole('table');
    expect(heads(table)).toEqual([expect.stringMatching(/^Component \d+$/), 'Type', 'Face', 'Connector', 'Positions']);
    const row = within(table).getByRole('cell', { name: 'swp49' }).closest('tr')!;
    expect(within(row).getAllByRole('cell').map(cell => cell.textContent)).toEqual(['swp49', 'Network port', 'Front', 'QSFP28', '4']);
    expect(screen.queryByText(/swp1 SFP28, swp2/)).toBeNull();
    expect(screen.queryByRole('heading', { name: /Chassis front|Network ports/ })).toBeNull();
  });

  it('leaves out the Positions column when every component has one position', () => {
    show('example-server-1u');
    expect(heads(within(components()).getByRole('table'))).not.toContain('Positions');
  });

  it('outlines the tile of the row under the pointer or clicked', () => {
    show('example-server-1u');
    const row = within(components()).getByRole('cell', { name: 'PSU 1' }).closest('tr')!;
    fireEvent.click(row);
    expect(row).toHaveAttribute('data-highlight');
    expect(screen.getByRole('button', { name: /^PSU 1/ })).toHaveAttribute('data-highlight');
  });

  it('shows the facts strip once: manufacturer, model key, size; no footer model identity', () => {
    const { container } = show('example-server-1u');
    const summary = container.querySelector('.inventory-record-summary') as HTMLElement;
    expect(summary).toHaveTextContent(/^Example Compute Co. · EX-C1 · 1U · Full depth$/);
    expect(within(summary).getByText('EX-C1')).toHaveAttribute('title', 'Model key');
    expect(screen.getByRole('region', { name: 'Record' }).textContent).not.toMatch(/Normalized model identity|EX-C1/);
  });

  it('says Zero-U for a zero-U strip and drops the depth', () => {
    const { container } = show('example-pdu-8');
    const summary = container.querySelector('.inventory-record-summary') as HTMLElement;
    expect(summary).toHaveTextContent(/· Zero-U$/);
    expect(summary.textContent).not.toMatch(/0U|depth/i);
  });

  it('labels a model without a recorded layout and still lists its components', () => {
    const inventory = fixtureInventory();
    const server = inventory.physical.find(record => record.metadata.name === 'example-server-1u')!;
    const { faceplate: _, ...spec } = server.spec;
    inventory.physical = inventory.physical.map(record => record === server ? { ...server, spec } : record);
    show('example-server-1u', inventory);
    expect(screen.getByText('Layout not recorded')).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: /^Model faceplate/ })).toBeNull();
    expect(within(components()).getByRole('cell', { name: 'PSU 1' })).toBeInTheDocument();
  });

  it('lists devices of the type in a table, and a denied collection as unknown', () => {
    const view = show('example-server-1u', fixtureInventory(), '&tab=devices');
    const table = screen.getByRole('table', { name: 'Devices of this type' });
    expect(heads(table).slice(0, 2)).toEqual(['Device', 'Rack · U']);
    expect(heads(table)).toContain('Lifecycle');
    expect(within(table).getByRole('link', { name: 'Compute a01-01' })).toBeInTheDocument();
    view.unmount();
    const denied = show('example-server-1u', { ...fixtureInventory('devices-denied') }, '&tab=devices');
    expect(screen.getByRole('tab', { name: /^Devices \?$/ })).toBeInTheDocument();
    expect(denied.container.textContent).not.toMatch(/Compute a01-01/);
  });
});
