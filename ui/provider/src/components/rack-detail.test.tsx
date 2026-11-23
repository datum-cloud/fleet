import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, useLocation, useNavigate } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { PhysicalInventory } from './physical-inventory';
import { fixtureInventory } from '../lib/inventory-fixtures';
import { condensedName } from '../lib/display-names';
import type { Inventory } from '../inventory';

const BASE = '/plugins/metal-datumapis-com/inventory';
const RACK = (name = 'example-rack-a01', extra = '') => `${BASE}?kind=Rack&record=${name}&namespace=default${extra}`;

function Probe() {
  const location = useLocation();
  const navigate = useNavigate();
  return <><output data-testid="search">{location.search}</output><button type="button" onClick={() => void navigate(-1)}>Test back</button></>;
}

function open(path: string, scenario?: string, inventory: Inventory = fixtureInventory(scenario)) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={[path]}><PhysicalInventory inventory={inventory} /><Probe /></MemoryRouter></QueryClientProvider>);
}
const search = () => new URLSearchParams(screen.getByTestId('search').textContent ?? '');
const elevationLink = (name: RegExp, side = 'front') => within(screen.getByRole('list', { name: `${side} recorded placements` })).getByRole('link', { name });

function phoneWidth(matches: boolean) {
  vi.spyOn(window, 'matchMedia').mockImplementation(query => ({ matches: matches && query.includes('max-width: 600px'), media: query, onchange: null, addListener: () => {}, removeListener: () => {}, addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => false }) as MediaQueryList);
}
afterEach(() => vi.restoreAllMocks());

describe('rack page tabs', () => {
  it('defaults to the elevation and routes each tab through the URL', () => {
    open(RACK());
    const tabs = screen.getByRole('tablist', { name: 'Rack views' });
    expect(within(tabs).getByRole('tab', { name: 'Elevation' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('list', { name: 'front recorded placements' })).toBeInTheDocument();

    fireEvent.click(within(tabs).getByRole('tab', { name: /^Equipment\b/ }));
    expect(search().get('tab')).toBe('equipment');
    expect(screen.queryByRole('list', { name: 'front recorded placements' })).not.toBeInTheDocument();
    // One dense table, top of rack first; non-unit mounts sort last and read as their mount.
    const equipment = screen.getByRole('table', { name: 'Rack equipment' });
    expect(within(equipment).getAllByRole('columnheader').map(cell => cell.textContent)).toEqual(['U', 'Device', 'Model', 'Face', 'Depth', 'Ports', 'Power', 'Serial', 'Asset tag']);
    const rows = within(equipment).getAllByRole('row').slice(1).map(row => [...row.querySelectorAll('td')].map(cell => cell.textContent));
    expect(rows[0].slice(0, 2)).toEqual(['U42', 'Patch panel a01']);
    expect(rows.at(-1)!.slice(0, 2)).toEqual(['Rear left vertical', 'PDU a01 (rear left)']);
    expect(rows.find(row => row[1] === 'Compute a01-05')!.slice(3, 7)).toEqual(['front', 'Full', expect.stringMatching(/^\d+ \/ \d+$/), 'Feed A']);
    // The page states facility and rack once; rows do not repeat them, and nothing reads Active per row.
    expect(within(equipment).queryByText(/Example Facility One|Rack A01|Reported placement ·|^Active$/)).not.toBeInTheDocument();

    fireEvent.click(within(tabs).getByRole('tab', { name: /^Cables\b/ }));
    expect(search().get('tab')).toBe('cables');
    expect(screen.getByRole('heading', { name: /Leaf a01/ })).toBeInTheDocument();
    // Display names only; generated names stay in the collapsed record-name details.
    const leafCables = screen.getByRole('table', { name: 'Leaf a01 cables' });
    expect(within(leafCables).queryByText(/^example-/)).not.toBeInTheDocument();
    expect(within(leafCables).getAllByRole('link', { name: 'Breakout swp49' })).toHaveLength(4);

    fireEvent.click(within(tabs).getByRole('tab', { name: /^Power\b/ }));
    expect(search().get('tab')).toBe('power');
    expect(screen.getByRole('link', { name: 'Feed A' })).toBeInTheDocument();
    // The grid holds the PDU's row and the feed's outlet tiles.
    expect(screen.getByRole('rowheader', { name: /^PDU a01/ })).toBeInTheDocument();
    expect(within(screen.getByRole('list', { name: 'Feed A outlets' })).getByRole('button', { name: /^Outlet 2: No cable recorded/ })).toBeInTheDocument();

    expect(within(tabs).queryByRole('tab', { name: 'History' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Test back' }));
    expect(search().get('tab')).toBe('cables');
  });

  it('opens a tab from a deep link and falls back to the elevation for unknown tabs', () => {
    const view = open(RACK('example-rack-a01', '&tab=cables'));
    expect(within(screen.getByRole('tablist', { name: 'Rack views' })).getByRole('tab', { name: /^Cables\b/ })).toHaveAttribute('aria-current', 'page');
    view.unmount();
    open(RACK('example-rack-a01', '&tab=nonsense'));
    expect(screen.getByRole('list', { name: 'front recorded placements' })).toBeInTheDocument();
  });
});

describe('tab counts', () => {
  const tabs = () => within(screen.getByRole('tablist', { name: 'Rack views' }));
  it('puts each related-record count on its tab, with no separate count strip', () => {
    open(RACK());
    expect(tabs().getAllByRole('tab').map(tab => tab.textContent)).toEqual(['Elevation', 'Equipment 10', 'Cables 21', 'Power 2']);
    expect(screen.queryByRole('navigation', { name: 'Related records' })).not.toBeInTheDocument();
    fireEvent.click(tabs().getByRole('tab', { name: /^Cables/ }));
    expect(search().get('tab')).toBe('cables');
  });

  it('moves the resource name into the one-line Record footer', () => {
    open(RACK());
    const about = screen.getByRole('region', { name: 'Record' });
    expect(within(about).getByText('example-rack-a01')).toBeInTheDocument();
    expect(about.textContent).not.toMatch(/synthetic-example-rack-a01|Facility record|Verification/);
  });

  it.each([
    ['cables-denied', 'Cables ?'],
    ['power-feeds-denied', 'Power ?'],
    ['devices-denied', 'Cables ?'],
  ])('shows denied counts as unknown, never zero (%s)', (scenario, label) => {
    open(RACK(), scenario);
    expect(tabs().getByRole('tab', { name: label })).toBeInTheDocument();
  });
});

describe('issue banners', () => {
  it('flags overlapping placements without blocking them and jumps to the devices', () => {
    open(RACK('example-rack-a02'));
    const issues = screen.getByRole('region', { name: 'Rack issues' });
    expect(within(issues).getByText(/2 placements disagree at U20–U21/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /2 placements disagree/ })).toBeInTheDocument();
    // Both remain drawn and flagged.
    expect(within(screen.getByRole('list', { name: 'front recorded placements' })).getAllByRole('link', { name: /overlapping placements/ })).toHaveLength(2);
    fireEvent.click(within(issues).getByRole('link', { name: 'Compute a02-02' }));
    expect(search().get('selected')).toBe('example-srv-a02-02');
    expect(within(screen.getByTestId('rack-device-panel')).getByRole('heading', { name: 'Compute a02-02' })).toBeInTheDocument();
  });

  it('lists equipment with a rack but no usable position and opens it in the panel', () => {
    open(RACK('example-rack-a03'));
    const issues = screen.getByRole('region', { name: 'Rack issues' });
    expect(within(issues).getByText(/1 in this rack without a usable unit position/)).toBeInTheDocument();
    fireEvent.click(within(issues).getByRole('link', { name: 'Compute a03-01 (position unknown)' }));
    const panel = screen.getByTestId('rack-device-panel');
    expect(panel.querySelector('.inventory-rack-panel-facts')).toHaveTextContent(/Position not recorded/);
  });

  it('shows no banners when nothing disagrees', () => {
    open(RACK());
    expect(screen.queryByRole('region', { name: 'Rack issues' })).not.toBeInTheDocument();
  });
});

describe('device selection', () => {
  it('opens the side panel from the elevation, keeps the selection in the URL, and Back closes it', () => {
    phoneWidth(false);
    open(RACK());
    expect(screen.queryByTestId('rack-device-panel')).not.toBeInTheDocument();
    fireEvent.click(elevationLink(/^Review Compute a01-01/));
    expect(search().get('selectedKind')).toBe('Device');
    expect(search().get('selected')).toBe('example-srv-a01-01');
    const panel = screen.getByTestId('rack-device-panel');
    expect(panel).toHaveAttribute('role', 'region');
    expect(within(panel).getByRole('heading', { name: 'Compute a01-01' })).toBeInTheDocument();
    // One line of facts under the title, then the faceplate; no field grid or state counts.
    const plate = within(panel).getByRole('region', { name: 'Faceplate' });
    const facts = panel.querySelector('.inventory-rack-panel-facts')!;
    expect(facts).toHaveTextContent(/^Example Compute Co. Example 1U compute · U2 · 1U · front · full depth \(catalog\)$/);
    expect(facts.compareDocumentPosition(plate) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(panel.querySelector('.inventory-rack-panel-fields, [aria-label="Ports and cables"]')).toBeNull();
    // The model records a layout, so the panel draws it: ports on the chassis
    // rear, drive bays on the front, and the unplaced console in the tray.
    const rear = within(plate).getByRole('group', { name: 'Rear · rack rear face' });
    expect(within(rear).getByRole('button', { name: /^PSU 1: / })).toHaveTextContent('PSU 1');
    const front = within(plate).getByRole('group', { name: 'Front · rack front face' });
    expect(within(front).getByRole('img', { name: 'Bay 7, module, Catalog only' })).toBeInTheDocument();
    expect(within(plate).getByRole('group', { name: 'Not on the faceplate' })).toHaveTextContent('Serial console');
    // Lifecycle shows only when it is not Active; the drawing carries each port's state.
    expect(within(panel).queryByText('Active')).toBeNull();
    expect(within(panel).getByRole('link', { name: 'Open device page' })).toHaveAttribute('href', expect.stringContaining('kind=Device&record=example-srv-a01-01'));
    expect(elevationLink(/^Review Compute a01-01/)).toHaveAttribute('aria-current', 'true');
    fireEvent.click(screen.getByRole('button', { name: 'Test back' }));
    expect(screen.queryByTestId('rack-device-panel')).not.toBeInTheDocument();
  });

  it('opens a bottom sheet instead at phone width', () => {
    phoneWidth(true);
    open(RACK());
    fireEvent.click(elevationLink(/^Review Leaf a01/, 'rear'));
    const sheet = screen.getByTestId('rack-device-sheet');
    expect(sheet).toHaveAttribute('role', 'dialog');
    expect(sheet).toHaveAttribute('data-variant', 'sheet');
    expect(within(sheet).getByRole('heading', { name: 'Leaf a01' })).toBeInTheDocument();
    expect(within(sheet).getByRole('button', { name: 'Close device panel' })).toHaveFocus();
    fireEvent.click(within(sheet).getByRole('button', { name: 'Close device panel' }));
    expect(screen.queryByTestId('rack-device-sheet')).not.toBeInTheDocument();
    expect(search().get('selected')).toBeNull();
  });

  it('moves through the elevation with arrow keys, selects with Enter and closes with Escape', () => {
    phoneWidth(false);
    open(RACK('example-rack-a01'));
    const first = screen.getAllByRole('link', { name: /^Review / })[0];
    first.focus();
    // Arrow keys follow the drawing top to bottom, equipment to equipment.
    fireEvent.keyDown(first, { key: 'ArrowDown' });
    const next = document.activeElement as HTMLElement;
    expect(next).not.toBe(first);
    expect(next).toHaveAttribute('data-slot-key');
    fireEvent.keyDown(next, { key: 'End' });
    const last = document.activeElement as HTMLElement;
    fireEvent.keyDown(last, { key: 'Home' });
    expect(document.activeElement).toBe(first);
    const leaf = elevationLink(/^Review Leaf a01/, 'rear');
    leaf.focus();
    fireEvent.keyDown(leaf, { key: 'Enter' });
    expect(search().get('selected')).toBe('example-leaf-a01');
    expect(within(screen.getByTestId('rack-device-panel')).getByRole('heading', { name: 'Leaf a01' })).toBeInTheDocument();
    fireEvent.keyDown(elevationLink(/^Review Leaf a01/, 'rear'), { key: 'Escape' });
    expect(screen.queryByTestId('rack-device-panel')).not.toBeInTheDocument();
    expect(search().get('selected')).toBeNull();
    expect(document.activeElement).toHaveAttribute('data-slot-key', 'Device/default/example-leaf-a01');
  });

  it('opens a component card with recorded cables and no invented health', async () => {
    phoneWidth(false);
    open(RACK('example-rack-a01', '&selectedKind=Device&selected=example-leaf-a01'));
    const panel = screen.getByTestId('rack-device-panel');
    const tile = within(panel).getByRole('button', { name: /^swp1: Cable recorded, to Compute a01-01 · Ethernet 0/ });
    await act(async () => { fireEvent.click(tile); });
    const card = await screen.findByRole('dialog', { name: 'swp1 details' });
    expect(within(card).getByText('No health reported')).toBeInTheDocument();
    expect(within(card).getByText(/Compute a01-01 · Ethernet 0/)).toBeInTheDocument();
    expect(within(card).getByRole('link', { name: 'DAC a01-01 eth0' })).toBeInTheDocument();
    // Escape inside the card closes the card, not the device panel.
    fireEvent.keyDown(card, { key: 'Escape' });
    expect(screen.getByTestId('rack-device-panel')).toBeInTheDocument();
  });

  it('draws a patch panel on both recorded faces, its dense jacks without text', () => {
    phoneWidth(false);
    open(RACK('example-rack-a01', '&selectedKind=Device&selected=example-patch-a01'));
    const plate = within(screen.getByTestId('rack-device-panel')).getByRole('region', { name: 'Faceplate' });
    const front = within(plate).getByRole('group', { name: /^Front · rack (front|rear) face$/ });
    const rear = within(plate).getByRole('group', { name: /^Rear · rack (front|rear) face$/ });
    expect(within(front).getAllByRole('button')).toHaveLength(24);
    expect(within(rear).getByRole('button', { name: /^Rear 1: / })).toBeInTheDocument();
    // 24 jacks is above the dense threshold: no labels on ports, numbers at the edges.
    expect(within(front).getAllByRole('button').filter(tile => tile.hasAttribute('data-labelled'))).toEqual([]);
    expect([...front.querySelectorAll('.inventory-chassis-number')].map(item => item.textContent)).toEqual(['1', '24']);
    expect(within(plate).queryByText('Face not recorded')).not.toBeInTheDocument();
  });

  it('says once that faces are not recorded rather than drawing two empty faces', () => {
    phoneWidth(false);
    // Template faces are optional on the wire; without them nothing is guessed
    // from the type. Layout needs a face, so the models record none here.
    const inventory = structuredClone(fixtureInventory());
    for (const record of inventory.physical) {
      delete record.spec.faceplate;
      for (const template of record.spec.componentTemplates ?? []) { delete template.face; delete template.layout; }
    }
    open(RACK('example-rack-a01', '&selectedKind=Device&selected=example-srv-a01-01'), undefined, inventory);
    const plate = within(screen.getByTestId('rack-device-panel')).getByRole('region', { name: 'Faceplate' });
    expect(within(plate).getByText('Layout not recorded')).toBeInTheDocument();
    expect(within(plate).getByText('Component faces not recorded')).toBeInTheDocument();
    expect(within(plate).queryByText(/^(Front|Rear)$/)).not.toBeInTheDocument();
    expect(within(plate).getByRole('group', { name: 'Component faces not recorded · Power' })).toHaveTextContent('PSU 1');
    // A catalog-only template is a tile, styled and named as such.
    expect(within(plate).getByRole('button', { name: /^Serial console: Catalog only/ })).toHaveAttribute('data-state', 'catalog');
  });
});

describe('denied records', () => {
  it('does not echo a selected device when Devices are denied', () => {
    phoneWidth(false);
    open(RACK('example-rack-a01', '&selectedKind=Device&selected=example-srv-a01-01'), 'devices-denied');
    const panel = screen.getByTestId('rack-device-panel');
    expect(within(panel).getByText(/not among the readable records/)).toBeInTheDocument();
    // Only the URL the visitor followed carries the name; the page does not repeat it.
    expect(screen.queryAllByText(/example-srv-a01-01|Compute a01-01/).filter(item => item !== screen.getByTestId('search'))).toHaveLength(0);
    fireEvent.click(within(screen.getByRole('tablist', { name: 'Rack views' })).getByRole('tab', { name: /^Cables\b/ }));
    expect(screen.getByText(/Device details unavailable. Cables cannot be grouped/)).toBeInTheDocument();
  });

  it('keeps faceplate state unknown when Cables are denied', () => {
    phoneWidth(false);
    open(RACK('example-rack-a01', '&selectedKind=Device&selected=example-srv-a01-01'), 'cables-denied');
    const panel = screen.getByTestId('rack-device-panel');
    expect(within(panel).getAllByText(/Cable details unavailable/).length).toBeGreaterThan(0);
    expect(within(panel).queryByText(/cable recorded/i)).not.toBeInTheDocument();
    expect(within(panel).getByRole('button', { name: /^Ethernet 0: Cable details unavailable, connections unknown$/ })).toBeInTheDocument();
    fireEvent.click(within(screen.getByRole('tablist', { name: 'Rack views' })).getByRole('tab', { name: /^Cables\b/ }));
    expect(screen.getByText(/Cable details unavailable. Connections for this rack are unknown, not absent/)).toBeInTheDocument();
  });

  it('names no power feed when power feeds are denied', () => {
    open(RACK('example-rack-a01', '&tab=power'), 'power-feeds-denied');
    expect(screen.getByText('Power feed details unavailable.')).toBeInTheDocument();
    expect(screen.queryByText(/Feed A|Feed B|example-feed/)).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'PDU a01 (rear left)' })).toBeInTheDocument();
  });
});

describe('condensed names', () => {
  it.each([['Ethernet 0', 'eth0'], ['Outlet 3', 'O3'], ['Front 12', 'F12'], ['Management', 'mgmt'], ['Serial console', 'console'], ['swp49', 'swp49'], ['PSU 1', 'PSU 1']])('%s → %s', (name, short) => {
    expect(condensedName(name)).toBe(short);
  });
});
