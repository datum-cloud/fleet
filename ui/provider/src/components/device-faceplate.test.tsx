import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';
import type { Device } from '../../../shared/fleet-api';
import { faceplate } from '../lib/device-faceplate';
import { FIXTURE_NAMES, fixtureInventory } from '../lib/inventory-fixtures';
import { DeviceFaceplate, PortTileButton, PortTooltip, TEXT_TILE_LIMIT } from './device-faceplate';

// Synthetic fixture records only.
const named = (name: string, scenario?: string) => {
  const inventory = fixtureInventory(scenario);
  const device = [...inventory.slots.flatMap(slot => slot.devices), ...inventory.unboundDevices].find(item => item.metadata.name === name) as Device;
  return { inventory, device };
};

describe('PortTileButton', () => {
  it('draws a labelled tile, or an unlabelled port-shaped square, with the state as glyph and attributes', () => {
    render(<>
      <PortTileButton state="cabled" accessibleName="eth0: Cable recorded, to Leaf · swp1, via DAC 1" label="eth0" shape="sfp" />
      <PortTileButton state="conflict" accessibleName="swp2: Records disagree" />
      <PortTileButton state="unavailable" accessibleName="swp3: Peer unavailable" shape="qsfp" lanes={['cabled', 'none', 'unavailable', 'none']} />
    </>);
    const [eth0, swp2, swp3] = screen.getAllByRole('button');
    expect(eth0).toHaveAccessibleName('eth0: Cable recorded, to Leaf · swp1, via DAC 1');
    expect(eth0).toHaveTextContent('eth0');
    expect(eth0).toHaveAttribute('data-shape', 'sfp');
    expect(eth0).toHaveAttribute('data-labelled', '');
    // Unlabelled tiles show only the glyph that fill and outline cannot carry.
    expect(swp2.textContent).toBe('!');
    expect(swp2).toHaveAttribute('data-shape', 'generic');
    expect(swp2).not.toHaveAttribute('data-labelled');
    expect(swp3.textContent).toBe('?');
    expect([...swp3.querySelectorAll('.inventory-port-lanes > span')].map(item => item.getAttribute('data-state'))).toEqual(['cabled', 'none', 'unavailable', 'none']);
    for (const tile of [eth0, swp2, swp3]) expect(tile.querySelector('.inventory-port-face')).toHaveAttribute('aria-hidden', 'true');
  });

  it('renders a one-line tooltip hidden from assistive technology', () => {
    render(<PortTooltip text="swp37 · no cable recorded" at={{ left: 4, top: 30 }} />);
    const tip = screen.getByRole('tooltip', { hidden: true });
    expect(tip).toHaveTextContent('swp37 · no cable recorded');
    expect(tip).toHaveAttribute('aria-hidden', 'true');
  });
});

describe('DeviceFaceplate', () => {
  it('draws the leaf as unlabelled tiles above the dense threshold, with faint bank numbers', () => {
    const { inventory, device } = named(FIXTURE_NAMES.leaf);
    render(<MemoryRouter><DeviceFaceplate device={device} inventory={inventory} /></MemoryRouter>);
    const network = screen.getByRole('list', { name: 'Network ports' });
    const tiles = within(network).getAllByRole('button');
    expect(tiles.length).toBeGreaterThan(TEXT_TILE_LIMIT);
    expect(tiles.filter(tile => tile.hasAttribute('data-labelled'))).toEqual([]);
    expect(within(network).getByRole('button', { name: /^swp37: No cable recorded, unknown, not free$/ })).toBeInTheDocument();
    const numbers = [...network.querySelectorAll('.inventory-port-number')].map(item => item.textContent).filter(Boolean);
    expect(numbers.slice(0, 6)).toEqual(['1', '9', '17', '25', '33', '41']);
    // Small groups keep their labels.
    const power = screen.getByRole('list', { name: 'Power ports' });
    expect(within(power).getAllByRole('button').map(tile => tile.textContent)).toEqual(['PSU 1', 'PSU 2']);
    fireEvent.click(within(network).getByRole('button', { name: /^swp37:/ }));
    expect(screen.getByRole('dialog', { name: 'swp37 details' })).toHaveTextContent('No health reported');
  });

  it('leads a breakout card with the answer and keeps record names behind More', () => {
    const { inventory, device } = named(FIXTURE_NAMES.leaf);
    render(<MemoryRouter><DeviceFaceplate device={device} inventory={inventory} /></MemoryRouter>);
    fireEvent.click(screen.getByRole('button', { name: /^swp49:/ }));
    const card = screen.getByRole('dialog', { name: 'swp49 details' });
    // One shared Cable by display name, then one state and peer per lane.
    expect(within(card).getByRole('link', { name: 'Breakout swp49' })).toBeInTheDocument();
    expect(within(card).getAllByText(/^→/)).toHaveLength(4);
    expect(card.textContent).not.toMatch(/example-|Peer record|Connector/);
    expect(card.textContent!.indexOf('No health reported')).toBeLessThan(card.textContent!.indexOf('More'));
    fireEvent.click(within(card).getByRole('button', { name: 'More' }));
    expect(within(card).getAllByText('Cable record')).toHaveLength(1);
    expect(within(card).getByText('example-brk-swp49').tagName).toBe('CODE');
    expect(within(card).getAllByText('Peer record')).toHaveLength(4);
    expect(card).toHaveTextContent('QSFP28 · 4 positions');
    // The shared Cable's medium, not one lane's.
    expect(card.textContent).not.toMatch(/lane \d of 4/);
  });

  it('keeps every state unknown, never "no cable", when Cables are denied', () => {
    const { inventory, device } = named(FIXTURE_NAMES.server, 'cables-denied');
    const plate = faceplate(device, inventory);
    expect(plate.cableAccess).toBe('denied');
    expect(plate.links).toEqual([]);
    expect(plate.faces.flatMap(face => face.clusters.flatMap(cluster => cluster.tiles.map(tile => tile.state)))).toSatisfy((states: string[]) => states.length > 0 && states.every(state => state === 'unavailable'));
    const view = render(<MemoryRouter><DeviceFaceplate device={device} inventory={inventory} /></MemoryRouter>);
    expect(view.container.textContent).not.toMatch(/no cable recorded|cable recorded|unknown, not free/i);
    for (const tile of screen.getAllByRole('button').filter(item => item.hasAttribute('data-tile'))) expect(tile.getAttribute('aria-label')).toMatch(/: Cable details unavailable, connections unknown$/);
  });
});
