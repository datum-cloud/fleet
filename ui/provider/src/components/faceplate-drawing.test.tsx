import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import { deviceConnections } from '../lib/device-connections';
import { modelLayout } from '../lib/faceplate-layout';
import { FIXTURE_NAMES, fixtureInventory } from '../lib/inventory-fixtures';
import { TEXT_TILE_LIMIT } from './device-faceplate';
import { DeviceFaceplate, deviceDrawingModel, portProblemCounts } from './faceplate-drawing';
import { RackOverview } from './inventory-location';

// Synthetic fixture records only.
const named = (name: string, scenario?: string) => {
  const inventory = fixtureInventory(scenario);
  const device = [...inventory.slots.flatMap(slot => slot.devices), ...inventory.unboundDevices].find(item => item.metadata.name === name) as Device;
  return { inventory, device };
};
const show = (name: string, scenario?: string) => {
  const { inventory, device } = named(name, scenario);
  return render(<MemoryRouter><DeviceFaceplate device={device} inventory={inventory} /></MemoryRouter>);
};
const drawingGroup = () => screen.getByRole('group', { name: /^Faceplate drawing/ });
const ports = () => within(drawingGroup()).getAllByRole('button').filter(item => item.hasAttribute('data-tile'));
/** One size per rack unit, from an elevation's grid template ("repeat(3, 16px) 24px …"). */
const rowSizes = (list: HTMLElement) => [...list.style.gridTemplateRows.matchAll(/repeat\((\d+), ([^)]+?)\)|(var\([^)]*\)|\S+)/g)].flatMap(([, count, size, single]) => count ? Array(Number(count)).fill(size) : [single]);
/** The distinct unit row sizes; every unit is drawn, so there is one. */
const unitRowSizes = (list: HTMLElement) => new Set(rowSizes(list));
const accessibleNames = (root: HTMLElement) => [...root.querySelectorAll('[aria-label]')].map(item => item.getAttribute('aria-label')).join('\n');

describe('FaceplateDrawing', () => {
  it('draws each recorded face of the server from its layout, with module cells and a tray', () => {
    show(FIXTURE_NAMES.server);
    const front = screen.getByRole('group', { name: 'Front · rack front face' });
    const rear = screen.getByRole('group', { name: 'Rear · rack rear face' });
    // Front: eight drive bays, two of which the Device does not record.
    expect(within(front).getAllByRole('img').map(item => item.getAttribute('aria-label'))).toEqual([
      ...[1, 2, 3, 4, 5, 6].map(index => `Bay ${index}, module, Component recorded`),
      'Bay 7, module, Catalog only', 'Bay 8, module, Catalog only',
    ]);
    expect(within(front).queryAllByRole('button')).toEqual([]);
    // Rear: ports at their recorded positions, left to right by offset.
    const names = within(rear).getAllByRole('button').map(item => item.getAttribute('aria-label')!.split(':')[0]);
    expect(names).toEqual(['PSU 1', 'Ethernet 0', 'Ethernet 1', 'BMC', 'PSU 2']);
    const left = (name: string) => parseFloat((within(rear).getByRole('button', { name: new RegExp(`^${name}:`) }).parentElement as HTMLElement).style.left);
    expect(left('PSU 1')).toBeLessThan(left('Ethernet 0'));
    expect(left('BMC')).toBeLessThan(left('PSU 2'));
    // Connector kind comes from the fixed table, never stored.
    expect(within(rear).getByRole('img', { name: 'VGA, accessory, Component recorded' }).closest('[data-kind]')).toHaveAttribute('data-kind', 'VGA');
    expect(within(rear).getByRole('img', { name: 'USB 2, accessory, Catalog only' })).toHaveAttribute('data-state', 'catalog');
    // The console has no layout: it is in the tray, named, never placed.
    const tray = screen.getByRole('group', { name: 'Not on the faceplate' });
    expect(tray).toHaveTextContent('Serial console');
    expect(tray).toHaveTextContent('Position on the face not recorded');
    expect(within(tray).getByRole('button', { name: /^Serial console: Catalog only/ })).toBeInTheDocument();
    expect(screen.queryByText('Layout not recorded')).toBeNull();
  });

  it('carries no text on dense banks, prints edge numbers, and keeps short labels on small ones', () => {
    show(FIXTURE_NAMES.leaf);
    const front = screen.getByRole('group', { name: 'Front · rack rear face' });
    const sfp = within(front).getAllByRole('button').filter(item => /^swp([1-9]|[1-3]\d|4[0-8]):/.test(item.getAttribute('aria-label')!));
    expect(sfp.length).toBe(48);
    expect(sfp.length).toBeGreaterThan(TEXT_TILE_LIMIT);
    expect(sfp.filter(item => item.hasAttribute('data-labelled') || item.textContent!.replace(/[!?]/g, ''))).toEqual([]);
    expect([...front.querySelectorAll('.inventory-chassis-number')].map(item => item.textContent)).toEqual(['1', '2', '23', '24', '25', '26', '47', '48']);
    // The 2×4 QSFP block is at or under the threshold: printed labels, not on the port.
    expect([...front.querySelectorAll('.inventory-chassis-print')].map(item => item.textContent)).toEqual(['swp49', 'swp51', 'swp53', 'swp55', 'swp50', 'swp52', 'swp54', 'swp56', 'mgmt', 'Console']);
    // Module cells carry their label inside.
    const rear = screen.getByRole('group', { name: 'Rear · rack front face' });
    expect(within(rear).getByRole('button', { name: /^PSU 1:/ })).toHaveTextContent('PSU 1');
    expect(within(rear).getByRole('img', { name: 'Fan 1, module, Component recorded' })).toHaveTextContent('Fan 1');
  });

  it('gives every port an accessible name with its name, state and peer', () => {
    show(FIXTURE_NAMES.leaf);
    expect(screen.getByRole('button', { name: /^swp37: No cable recorded$/ })).toBeInTheDocument();
    const breakout = screen.getByRole('button', { name: /^swp49: Cable recorded; position 1 cable recorded to Storage a01-03 · Ethernet 0, via Breakout swp49/ });
    expect(breakout).toHaveAttribute('data-shape', 'qsfp');
    // The tooltip repeats the name and is hidden from assistive technology.
    fireEvent.focus(breakout);
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveAttribute('aria-hidden', 'true');
  });

  it('is one tab stop that roves by drawn row, opens the shared card, and Escape returns focus', () => {
    show(FIXTURE_NAMES.leaf);
    expect(ports().filter(item => item.tabIndex === 0)).toHaveLength(1);
    const swp1 = screen.getByRole('button', { name: /^swp1:/ });
    expect(swp1.tabIndex).toBe(0);
    swp1.focus();
    // Odd-top numbering: swp3 is beside swp1 in the drawn row.
    fireEvent.keyDown(swp1, { key: 'ArrowRight' });
    const swp3 = screen.getByRole('button', { name: /^swp3:/ });
    expect(document.activeElement).toBe(swp3);
    expect(swp3.tabIndex).toBe(0);
    expect(swp1.tabIndex).toBe(-1);
    fireEvent.keyDown(swp3, { key: 'End' });
    expect(document.activeElement?.getAttribute('aria-label')).toMatch(/^(Console|Management|PSU|swp)/);
    fireEvent.keyDown(document.activeElement!, { key: 'Home' });
    expect(document.activeElement).toBe(swp1);
    // Enter on a native button is a click: it pins the shared PortCard.
    fireEvent.click(swp1);
    const card = screen.getByRole('dialog', { name: 'swp1 details' });
    expect(within(card).queryByText('Health')).toBeNull();
    expect(within(card).queryByRole('button', { name: 'More' })).toBeNull();
    expect(swp1).toHaveAttribute('aria-expanded', 'true');
    fireEvent.keyDown(card, { key: 'Escape' });
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(document.activeElement).toBe(swp1);
  });

  it('leaks nothing about a denied peer in the drawing, tooltip, card or names', () => {
    const view = show(FIXTURE_NAMES.server, 'power-feeds-denied');
    const HIDDEN = /Feed A|Feed B|example-feed/;
    const unavailable = ports().filter(item => item.getAttribute('data-state') === 'unavailable');
    expect(unavailable.map(item => item.getAttribute('aria-label'))).toEqual([expect.stringMatching(/^PSU 2: Peer unavailable, to Power feed unavailable/)]);
    for (const port of ports()) {
      fireEvent.focus(port);
      fireEvent.click(port);
    }
    expect(view.container.textContent).not.toMatch(HIDDEN);
    expect(accessibleNames(view.container)).not.toMatch(HIDDEN);
  });

  it('keeps every state unknown when Cables are denied, and still draws the layout', () => {
    const view = show(FIXTURE_NAMES.server, 'cables-denied');
    expect(screen.getByRole('group', { name: 'Rear · rack rear face' })).toBeInTheDocument();
    for (const port of ports()) expect(port.getAttribute('aria-label')).toMatch(/: Cable details unavailable, connections unknown$/);
    expect(view.container.textContent).not.toMatch(/no cable recorded/i);
  });

  it('falls back to grouped tiles labelled "Layout not recorded" for a model without a layout', () => {
    show('example-app-a01');
    expect(screen.getByText('Layout not recorded')).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: /^Faceplate drawing/ })).toBeNull();
    expect(screen.getByRole('group', { name: /^Front · rack front · Network$/ })).toHaveTextContent('net0');
  });

  it('draws imported components by name and type when their ids match no template', () => {
    const view = show(FIXTURE_NAMES.imported);
    const front = screen.getByRole('group', { name: 'Front · rack face unknown face' });
    const names = within(front).getAllByRole('button').map(item => item.getAttribute('aria-label')!.split(':')[0]);
    expect(names).toHaveLength(24);
    expect(names).toEqual(expect.arrayContaining(['Ethernet1', 'Ethernet16', 'Ethernet21', 'Management1', 'Console']));
    // Drawn cells carry the Device's components, not catalog-only copies.
    expect(within(front).getByRole('button', { name: /^Ethernet1:/ })).toHaveAttribute('data-tile', 'network/interfaces-9101');
    expect(within(front).getAllByRole('button').filter(item => item.getAttribute('data-state') === 'catalog')).toEqual([]);
    // NetBox interface type strings map to kinds; a blank connector is Other.
    const kind = (name: string) => within(front).getByRole('button', { name: new RegExp(`^${name}:`) }).closest('[data-kind]')?.getAttribute('data-kind');
    expect(['Ethernet1', 'Ethernet17', 'Ethernet21', 'Management1', 'Console'].map(kind)).toEqual(['RJ45', 'SFP28', 'QSFP56', 'RJ45', 'Other']);
    // Two PSU templates share a name: both PSUs stay in the tray.
    const tray = screen.getByRole('group', { name: 'Not on the faceplate' });
    expect(within(tray).getAllByRole('listitem').map(item => [item.dataset.reason, item.textContent])).toEqual([
      ['no-template', expect.stringContaining('PSU')], ['no-template', expect.stringContaining('PSU')],
    ]);
    expect(view.container.querySelectorAll('.inventory-chassis-cell .inventory-chassis-module[data-state="catalog"]')).toHaveLength(0);
  });

  it('never lets a name match change record state, Cable joins or faces outside the drawing', () => {
    const { device, inventory } = named(FIXTURE_NAMES.imported);
    const links = deviceConnections(device, inventory).links;
    expect(links.find(link => link.componentID === 'interfaces-9101')).toMatchObject({ state: 'none', chassisFace: undefined });
    expect(links.find(link => link.componentID === 'interface-templates-101')?.state).toBe('catalog');
    const drawing = deviceDrawingModel(device, inventory);
    expect(drawing.tiles.get('interfaces-9101')?.state).toBe('none');
    expect(drawing.join.byComponent.get('interfaces-9101')?.basis).toBe('name+type');
    expect(drawing.at('interface-templates-101')).toBe('interfaces-9101');
    expect(drawing.at('power-port-templates-301')).toBe('power-port-templates-301');
  });

  it('draws a zero-U PDU as a one-unit strip and says why', () => {
    show(FIXTURE_NAMES.pdu);
    expect(screen.getByText(/Zero-U: drawn as a one-unit strip; length not recorded/)).toBeInTheDocument();
    expect(screen.getByText(/Width not recorded; drawn full width/)).toBeInTheDocument();
    expect(screen.getAllByRole('button').filter(item => /^Outlet \d:/.test(item.getAttribute('aria-label') ?? ''))).toHaveLength(8);
  });
});

describe('rack elevation slots', () => {
  it('counts port problems across the whole Device', () => {
    const { inventory, device } = named(FIXTURE_NAMES.leaf);
    expect(portProblemCounts(deviceDrawingModel(device, inventory))).toEqual({ conflict: 2, unavailable: 0 });
  });

  it('shows only the name, colour codes problems and names them in words', () => {
    const inventory = fixtureInventory();
    const rack = inventory.physical.find(record => record.kind === 'Rack' && record.metadata.name === FIXTURE_NAMES.rack) as PhysicalRecord;
    render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><RackOverview rack={rack} inventory={inventory} /></MemoryRouter></QueryClientProvider>);
    const rear = screen.getByRole('list', { name: 'rear recorded placements' });
    const leaf = within(rear).getByRole('link', { name: /^Review Leaf a01/ });
    expect(leaf.getAttribute('aria-label')).toMatch(/, 2 ports disagree$/);
    expect(leaf).toHaveAttribute('data-attention', 'conflict');
    expect(leaf.getAttribute('title')).toMatch(/2 ports disagree$/);
    expect(within(leaf).queryByText(/^! ?\d/)).toBeNull();
    // No drawing inside the slot, and no level-of-detail control.
    expect(leaf.querySelector('svg')).toBeNull();
    expect(screen.queryByRole('group', { name: 'Elevation detail' })).toBeNull();
    expect(rowSizes(rear)).toHaveLength(42);
    expect(unitRowSizes(rear)).toEqual(new Set(['18px']));
    expect(leaf.querySelectorAll('button, a, [tabindex]')).toHaveLength(0);
  });
});
