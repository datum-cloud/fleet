import { createEvent, fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { MemoryRouter, useLocation } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { CableEndpoint, Device, PhysicalComponent, PhysicalRecord } from '../../../shared/fleet-api';
import { toInventory, type Inventory } from '../inventory';
import { DeviceConnections } from './device-connections';
import { inventoryHref } from './inventory-location';
import { PhysicalInventory } from './physical-inventory';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';
import { TEXT_TILE_LIMIT } from './device-faceplate';

// Synthetic records only.
const NS = 'default';
const C = (id: string, type: PhysicalComponent['type'], name = id, positions?: number, connector?: string): PhysicalComponent => ({ id, name, type, ...(positions ? { positions } : {}), ...(connector ? { connector } : {}) });
const device = (name: string, components: PhysicalComponent[], extra: Partial<Device['spec']> = {}): Device => ({
  metadata: { name, namespace: NS },
  spec: { claimRef: { name: `${name}-claim` }, identity: { model: 'example-model', serial: `SYNTH-${name}` }, displayName: `Example ${name}`, topologyAuthority: 'Cable', components, ...extra },
});
const D = (name: string, componentID: string, position = 1): CableEndpoint => ({ deviceRef: { name }, componentID, position });
const P = (name: string, componentID: string): CableEndpoint => ({ powerFeedRef: { name }, componentID, position: 1 });
const cable = (name: string, medium: string, a: CableEndpoint, b: CableEndpoint, displayName = name): PhysicalRecord => ({ kind: 'Cable', metadata: { name, namespace: NS }, spec: { displayName, medium, lanes: [{ id: 'l1', a, b }] } });
const feed: PhysicalRecord = { kind: 'PowerFeed', metadata: { name: 'feed-hidden', namespace: NS }, spec: { displayName: 'Hidden feed', supply: { type: 'AC', voltageV: 208, currentA: 30 }, terminations: [C('outlet-7', 'PowerOutlet', 'Outlet 7')] } };

const server = device('server-01', [C('psu-1', 'PowerInlet', 'PSU 1'), C('psu-2', 'PowerInlet', 'PSU 2'), C('eth0', 'NetworkPort', 'Ethernet 0'), C('eth1', 'NetworkPort', 'Ethernet 1')]);
const leaf = device('leaf-01', [C('swp1', 'NetworkPort'), C('swp2', 'NetworkPort')]);
const serverRecords = [feed, cable('pwr-01', 'Power', P('feed-hidden', 'outlet-7'), D('server-01', 'psu-1')), cable('dac-01', 'Copper', D('server-01', 'eth0'), D('leaf-01', 'swp1'))];
const data = (issues?: Inventory['issues'], devices = [server, leaf], physical = serverRecords): Inventory => ({ ...toInventory([], devices, physical), issues });

// Everything that leaks a hidden PowerFeed: its name, display name, outlet and rating.
const HIDDEN_FEED = /feed-hidden|Hidden feed|outlet-7|Outlet 7|208 V/;

function Location() {
  const { search } = useLocation();
  return <output data-testid="location">{search}</output>;
}

function show(target: Device, inventory: Inventory, search = '') {
  return render(
    <MemoryRouter initialEntries={[`/inventory${search}`]}>
      <DeviceConnections device={target} inventory={inventory} href={inventoryHref} />
      <Location />
    </MemoryRouter>,
  );
}

const grid = () => screen.getByRole('group', { name: /Port tiles/ });
const tiles = () => within(grid()).getAllByRole('button').filter(item => item.hasAttribute('data-tile'));
const tile = (name: RegExp) => within(grid()).getByRole('button', { name });
const card = () => screen.getByRole('dialog');
const table = () => screen.getByRole('table', { name: /^Components/ });
const tableRows = () => within(table()).getAllByRole('row').slice(1);
const cells = (row: HTMLElement) => within(row).getAllByRole('cell');
const accessibleNames = (root: HTMLElement) => [...root.querySelectorAll('[aria-label]')].map(item => item.getAttribute('aria-label')).join('\n');
// Tooltips are visible on hover, so they follow the same rules as text.
const tooltips = (root: HTMLElement) => [...root.querySelectorAll('[title]')].map(item => item.getAttribute('title')).join('\n');
// jsdom has no PointerEvent, so set the pointer type on a plain event. React
// derives enter and leave from over and out, so those are what is fired.
function pointer(target: HTMLElement, type: 'pointerEnter' | 'pointerLeave', pointerType = 'mouse') {
  const event = type === 'pointerEnter' ? createEvent.pointerOver(target, { relatedTarget: document.body }) : createEvent.pointerOut(target, { relatedTarget: document.body });
  Object.defineProperty(event, 'pointerType', { value: pointerType });
  fireEvent(target, event);
}

describe('DeviceConnections', () => {
  it('always shows the picture with the component table under it, with no Picture or Table choice', () => {
    show(server, data());
    expect(within(grid()).getByRole('list', { name: 'Power ports' })).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'Connection view' })).toBeNull();
    for (const name of ['Picture', 'Table', 'Diagram']) expect(screen.queryByRole('button', { name })).toBeNull();
    expect(screen.queryByText(/^Verified$/)).not.toBeInTheDocument();
    const rows = tableRows();
    // The name only; the component id is the name's tooltip.
    expect(rows.map(row => cells(row)[0].textContent)).toEqual(['PSU 1', 'PSU 2', 'Ethernet 0', 'Ethernet 1']);
    expect(within(cells(rows[0])[0]).getByText('PSU 1')).toHaveAttribute('title', 'psu-1');
    expect(rows.map(row => cells(row)[1].textContent)).toEqual(['Power inlet', 'Power inlet', 'Network', 'Network']);
    expect(rows.map(row => cells(row)[3].textContent)).toEqual(['● Cable recorded', '○ No cable recorded', '● Cable recorded', '○ No cable recorded']);
    // The peer is the link; the Cable is a small secondary link beside it, not a column.
    expect(within(rows[2]).getByRole('link', { name: 'dac-01' })).toHaveAttribute('href', '/inventory?kind=Cable&record=dac-01&namespace=default');
    expect(within(rows[2]).getByRole('link', { name: 'Example leaf-01 · swp1' })).toHaveAttribute('href', '/inventory?kind=Device&record=leaf-01&namespace=default');
    expect(cells(rows[2])[4]).toHaveTextContent(/^Example leaf-01 · swp1 via dac-01$/);
    // No cable recorded reads Unknown, never an empty (free-looking) cell.
    expect(cells(rows[3])[4]).toHaveTextContent(/^Unknown$/);
  });

  it('has no link diagram: every view value, diagram included, is the default page', () => {
    for (const search of ['?view=diagram', '?view=table', '?view=picture', '?view=list']) {
      const page = show(server, data(), search);
      expect(grid()).toBeInTheDocument();
      expect(screen.getByRole('table', { name: 'Components and Cables' })).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Link diagram' })).toBeNull();
      expect(screen.queryByRole('group', { name: /Connection diagram/ })).toBeNull();
      page.unmount();
    }
  });

  it('states one count line in the table header, without the Device name, and no legend', () => {
    const view = show(server, data());
    expect(view.container.querySelector('.inventory-connection-summary')).toHaveTextContent(/^4 ports · 2 recorded · 2 unknown$/);
    expect(document.body.textContent?.match(/\d+ ports · /g)).toHaveLength(1);
    // The table is described by the count line.
    expect(screen.getByRole('region', { name: 'Components table' })).toHaveAccessibleDescription('4 ports · 2 recorded · 2 unknown');
    // The page heading already names the Device; the view does not repeat it.
    expect(view.container.textContent).not.toMatch(/server-01/);
    // No explanatory caveat and no legend: each state is a glyph and words in place.
    expect(screen.queryByText(/Unknown is not free/)).toBeNull();
    expect(screen.queryByRole('button', { name: 'Legend' })).toBeNull();
    expect(screen.queryByRole('list', { name: 'Record states' })).not.toBeInTheDocument();
  });

  it('outlines a component in the picture and the table at once, from either side', () => {
    show(server, data());
    const row = (name: string) => tableRows().find(item => cells(item)[0].textContent === name)!;
    const highlighted = () => tiles().filter(item => item.hasAttribute('data-highlight')).map(item => item.textContent);
    const marked = () => tableRows().filter(item => item.hasAttribute('data-highlight')).map(item => cells(item)[0].textContent);
    expect(highlighted()).toEqual([]);
    // Row to picture: hover, and focus on a link inside the row.
    pointer(row('Ethernet 0'), 'pointerEnter');
    expect(highlighted()).toEqual(['eth0']);
    expect(marked()).toEqual(['Ethernet 0']);
    pointer(row('Ethernet 0'), 'pointerLeave');
    expect(highlighted()).toEqual([]);
    fireEvent.focus(within(row('PSU 1')).getByRole('link', { name: 'pwr-01' }));
    expect(highlighted()).toEqual(['PSU 1']);
    fireEvent.blur(within(row('PSU 1')).getByRole('link', { name: 'pwr-01' }));
    expect(highlighted()).toEqual([]);
    // Picture to row: hover and keyboard focus on a tile.
    pointer(tile(/^PSU 2:/), 'pointerEnter');
    expect(marked()).toEqual(['PSU 2']);
    pointer(tile(/^PSU 2:/), 'pointerLeave');
    expect(marked()).toEqual([]);
    fireEvent.focus(tile(/^Ethernet 1:/));
    expect(marked()).toEqual(['Ethernet 1']);
    // Leaving an earlier tile does not clear a newer highlight.
    fireEvent.blur(tile(/^PSU 2:/));
    expect(marked()).toEqual(['Ethernet 1']);
    // Touch does not highlight on its way to the card.
    fireEvent.blur(tile(/^Ethernet 1:/));
    pointer(row('PSU 1'), 'pointerEnter', 'touch');
    expect(marked()).toEqual([]);
  });

  it('draws each port as a tile with its state in the accessible name, a glyph where outline alone is ambiguous, and no type words', () => {
    const records = [...serverRecords, cable('dac-02', 'Copper', D('server-01', 'eth1'), D('leaf-09', 'swp1'))];
    show(server, data(undefined, [server, leaf], records));
    expect(tiles().map(item => item.getAttribute('aria-label'))).toEqual([
      'PSU 1: Cable recorded, to Hidden feed · Outlet 7, via pwr-01',
      'PSU 2: No cable recorded',
      'Ethernet 0: Cable recorded, to Example leaf-01 · swp1, via dac-01',
      'Ethernet 1: Peer unavailable, to leaf-09 · swp1 (record not found), via dac-02',
    ]);
    expect(tiles().map(item => [item.getAttribute('data-state'), item.textContent])).toEqual([
      ['cabled', 'PSU 1'], ['none', 'PSU 2'], ['cabled', 'eth0'], ['unavailable', 'eth1?'],
    ]);
    expect(within(grid()).getByText(/^Power · 2/)).toHaveTextContent('Power · 2 · 1 recorded · 1 unknown');
    // Type words appear only in group headers, never per port.
    expect(grid().textContent).not.toMatch(/Device|PowerInlet|NetworkPort|cable recorded/);
  });

  it('marks disagreeing records with a ! tile and lists every claiming Cable, with the reason once, in the card', () => {
    const records = [...serverRecords, cable('dac-02', 'Copper', D('server-01', 'eth0'), D('leaf-01', 'swp2'))];
    show(server, data(undefined, [server, leaf], records));
    const eth0 = tile(/^Ethernet 0: Records disagree; 2 Cables: to Example leaf-01 · swp1, via dac-01; to Example leaf-01 · swp2, via dac-02$/);
    expect(eth0).toHaveAttribute('data-state', 'conflict');
    expect(eth0).toHaveTextContent('eth0!');
    fireEvent.click(eth0);
    const links = within(card()).getAllByRole('link', { name: /^dac-0[12]$/ });
    expect(links.map(link => link.textContent)).toEqual(['dac-01', 'dac-02']);
    // Both links share the reason, so the card says it once.
    expect(card().textContent?.match(/2 Cable lanes name this position\./g)).toHaveLength(1);
    expect(card()).not.toHaveTextContent(/Neither is treated as current/);
  });

  it('moves between tiles with arrow keys using one tab stop', () => {
    show(server, data());
    const items = tiles();
    expect(items.map(item => item.getAttribute('tabindex'))).toEqual(['0', '-1', '-1', '-1']);
    items[0].focus();
    fireEvent.keyDown(items[0], { key: 'ArrowRight' });
    expect(document.activeElement).toBe(items[1]);
    expect(items.map(item => item.getAttribute('tabindex'))).toEqual(['-1', '0', '-1', '-1']);
    // Without layout to read, Down moves to the next tile; across groups too.
    fireEvent.keyDown(items[1], { key: 'ArrowDown' });
    expect(document.activeElement).toBe(items[2]);
    fireEvent.keyDown(items[2], { key: 'End' });
    expect(document.activeElement).toBe(items[3]);
    fireEvent.keyDown(items[3], { key: 'ArrowRight' });
    expect(document.activeElement).toBe(items[3]);
    fireEvent.keyDown(items[3], { key: 'Home' });
    expect(document.activeElement).toBe(items[0]);
    fireEvent.keyDown(items[0], { key: 'ArrowLeft' });
    expect(document.activeElement).toBe(items[0]);
  });

  it('shows a one-line tooltip on keyboard focus, opens the card on click or Enter, follows the arrow keys, and closes on Escape', () => {
    show(server, data());
    const [psu1, psu2] = tiles();
    psu1.focus(); fireEvent.focus(psu1);
    // The tooltip repeats the tile's accessible name, so it is hidden from assistive technology.
    const tip = screen.getByRole('tooltip', { hidden: true });
    expect(tip).toHaveAttribute('aria-hidden', 'true');
    expect(tip).toHaveTextContent(/^PSU 1 · cable recorded → Hidden feed · Outlet 7$/);
    fireEvent.keyDown(psu1, { key: 'ArrowRight' });
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent(/^PSU 2 · no cable recorded$/);
    fireEvent.keyDown(psu2, { key: 'ArrowLeft' });

    // A native button turns Enter and Space into a click.
    psu1.focus(); fireEvent.focus(psu1);
    fireEvent.click(psu1);
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull();
    expect(psu1).toHaveAttribute('aria-expanded', 'true');
    expect(psu1).toHaveAttribute('aria-controls', card().id);
    expect(card()).toHaveAccessibleName('PSU 1 details');
    expect(card()).toHaveAttribute('aria-modal', 'false');
    // No health is observed, so there is no Health section: unknown is never shown as OK.
    expect(within(card()).queryByText('Health')).toBeNull();
    expect(card()).not.toHaveTextContent(/No health reported|OK/);
    expect(within(card()).getByRole('link', { name: 'pwr-01' })).toHaveAttribute('href', '/inventory?kind=Cable&record=pwr-01&namespace=default');
    expect(within(card()).getByRole('link', { name: 'Hidden feed · Outlet 7' })).toHaveAttribute('href', '/inventory?kind=PowerFeed&record=feed-hidden&namespace=default');
    // Record names are on the records' own pages, never in the card; the card has no More.
    expect(card()).not.toHaveTextContent('feed-hidden');
    expect(within(card()).queryByRole('button', { name: 'More' })).toBeNull();

    fireEvent.keyDown(psu1, { key: 'ArrowRight' });
    expect(document.activeElement).toBe(psu2);
    expect(card()).toHaveAccessibleName('PSU 2 details');
    expect(card()).toHaveTextContent('No cable recorded');
    expect(card()).not.toHaveTextContent(/free|available|verified/i);

    fireEvent.keyDown(psu2, { key: 'Escape' });
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(psu2).toHaveAttribute('aria-expanded', 'false');
    expect(document.activeElement).toBe(psu2);

    // Escape inside the card closes it and returns focus to its tile.
    fireEvent.click(psu1);
    const link = within(card()).getByRole('link', { name: 'pwr-01' });
    link.focus();
    fireEvent.keyDown(link, { key: 'Escape' });
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(document.activeElement).toBe(psu1);

    // The Close button, and a second click on the tile, close it too.
    fireEvent.click(psu1);
    fireEvent.click(within(card()).getByRole('button', { name: 'Close PSU 1' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    fireEvent.click(psu1);
    fireEvent.click(psu1);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('opens the card on tap without a hover preview, and the card links navigate', () => {
    show(server, data());
    const [, , eth0] = tiles();
    // jsdom has no PointerEvent, so set the pointer type on a plain event.
    const tap = createEvent.pointerDown(eth0);
    Object.defineProperty(tap, 'pointerType', { value: 'touch' });
    fireEvent(eth0, tap);
    fireEvent.focus(eth0);
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull();
    fireEvent.click(eth0);
    fireEvent.click(within(card()).getByRole('link', { name: 'Example leaf-01 · swp1' }));
    expect(screen.getByTestId('location')).toHaveTextContent('?kind=Device&record=leaf-01&namespace=default');
  });

  it('shows a denied peer as unavailable without leaking it in tiles, previews, cards or accessible names', () => {
    const view = show(server, data({ PowerFeed: { status: 403 } }));
    const psu1 = tile(/^PSU 1:/);
    expect(psu1).toHaveAttribute('data-state', 'unavailable');
    expect(psu1).toHaveAccessibleName('PSU 1: Peer unavailable, to Power feed unavailable, via pwr-01');
    expect(psu1).toHaveTextContent('PSU 1?');
    psu1.focus(); fireEvent.focus(psu1);
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('Power feed unavailable');
    expect(view.container.textContent).not.toMatch(HIDDEN_FEED);
    fireEvent.click(psu1);
    expect(card()).toHaveTextContent('You cannot read this peer');
    // Only the Cable is a link; the peer has no name, link or record name.
    expect(within(card()).getAllByRole('link').map(link => link.textContent)).toEqual(['pwr-01']);
    expect(within(card()).queryByText('Peer record')).toBeNull();
    expect(card().textContent).not.toMatch(HIDDEN_FEED);
    for (const target of tiles()) { fireEvent.focus(target); fireEvent.click(target); }
    expect(view.container.textContent).not.toMatch(HIDDEN_FEED);
    expect(accessibleNames(view.container)).not.toMatch(HIDDEN_FEED);
    expect(tooltips(view.container)).not.toMatch(HIDDEN_FEED);
  });

  it('never shows generated resource names by default, and tells same-named records apart', () => {
    const hostA = 'netbox-30ba6ae0ee289bda385a7a96';
    const hostB = 'netbox-41cb7bf1ff39acebb496b8a7';
    // Imported records: one with its generated name copied into displayName, the rest with none.
    const imported = (name: string) => device(name, [C('eth0', 'NetworkPort')], { displayName: name, identity: { manufacturer: 'Example Networks', model: 'EX-1' } });
    const pdu = device('pdu-a.rack-01.ex1', [C('out-1', 'PowerOutlet', 'Outlet 1')], { displayName: undefined });
    const SUBJECT = 'netbox-0f1e2d3c4b5a69788796a5b4';
    const subject = device(SUBJECT, [C('eth0', 'NetworkPort'), C('eth1', 'NetworkPort'), C('psu-1', 'PowerInlet', 'PSU1-A')], { displayName: undefined, identity: { model: 'EX-9' } });
    const records = [
      cable('netbox-ca213d185ea9153b853f295e', 'Copper', D(SUBJECT, 'eth0'), D(hostA, 'eth0'), 'netbox-ca213d185ea9153b853f295e'),
      cable('netbox-77aa13d185ea9153b853a1c0', 'Copper', D(SUBJECT, 'eth1'), D(hostB, 'eth0'), ''),
      cable('netbox-99bb13d185ea9153b853b2d1', 'Power', D(SUBJECT, 'psu-1'), D('pdu-a.rack-01.ex1', 'out-1'), ''),
    ];
    const view = show(subject, data(undefined, [subject, imported(hostA), imported(hostB), pdu], records));
    expect(view.container.textContent).not.toMatch(/netbox-/);
    expect(accessibleNames(view.container)).not.toMatch(/netbox-/);
    expect(tiles().map(item => item.getAttribute('aria-label'))).toEqual([
      'eth0: Cable recorded, to Unnamed device #7a96 · eth0, via Copper cable #295e',
      'eth1: Cable recorded, to Unnamed device #b8a7 · eth0, via Copper cable #a1c0',
      'PSU1-A: Cable recorded, to pdu-a.rack-01.ex1 · Outlet 1, via Power cable',
    ]);
    // The card leads with the answer, the Cable on its own line; resource names appear nowhere.
    fireEvent.click(tiles()[0]);
    expect(card()).toHaveTextContent(/^.*● Cable recorded→ Unnamed device #7a96 · eth0via Copper cable #295e/);
    expect(card().textContent).not.toMatch(/netbox-|Connector|Cable record(?!ed)|Peer record|not live link state/);
    expect(within(card()).queryByRole('button', { name: 'More' })).toBeNull();
    fireEvent.click(within(card()).getByRole('button', { name: /^Close/ }));
    expect(view.container.textContent).not.toMatch(/netbox-/);
    expect(accessibleNames(view.container)).not.toMatch(/netbox-/);
    expect(tooltips(view.container)).not.toMatch(/netbox-/);
  });

  it('keeps groups that are entirely catalog only behind a count until expanded', () => {
    const typed = device('server-03', [C('eth0', 'NetworkPort', 'Ethernet 0')], { deviceTypeRef: { name: 'example-type' } });
    const physical: PhysicalRecord[] = [
      { kind: 'Manufacturer', metadata: { name: 'example-maker', namespace: NS }, spec: { displayName: 'Example maker', canonicalName: 'Example maker' } },
      { kind: 'DeviceType', metadata: { name: 'example-type', namespace: NS }, spec: { displayName: 'Example type', manufacturerRef: { name: 'example-maker' }, model: 'example-model', componentTemplates: [C('eth0', 'NetworkPort', 'Ethernet 0'), C('console', 'ConsolePort', 'Serial console', undefined, 'RJ45')] } },
    ];
    show(typed, data(undefined, [typed], physical));
    expect(document.querySelector('.inventory-connection-summary')).toHaveTextContent(/^2 ports · 1 unknown · 1 catalog only$/);
    expect(within(grid()).queryByRole('list', { name: 'Console ports' })).toBeNull();
    fireEvent.click(within(grid()).getByRole('button', { name: 'Show Console · 1 port, catalog only' }));
    const consoleTile = tile(/^Serial console:/);
    expect(consoleTile).toHaveAccessibleName('Serial console: Catalog only');
    expect(consoleTile).toHaveAttribute('data-shape', 'rj45');
  });

  it('treats unreadable Cables as unknown connections, never as uncabled, and still draws the picture', () => {
    show(server, data({ Cable: { status: 403 } }));
    expect(screen.getByText('Cable records are unavailable to you.')).toBeInTheDocument();
    expect(screen.queryByText(/No cable recorded|\d+ recorded/)).not.toBeInTheDocument();
    // The picture shows every recorded port as unknown, never as free.
    expect(tiles().map(item => item.getAttribute('data-state'))).toEqual(['unavailable', 'unavailable', 'unavailable', 'unavailable']);
    expect(tiles().map(item => item.getAttribute('aria-label'))).toEqual(['PSU 1', 'PSU 2', 'Ethernet 0', 'Ethernet 1'].map(name => `${name}: Cable details unavailable, connections unknown`));
    expect(screen.getByRole('table', { name: 'Components' })).toBeInTheDocument();
  });

  it('shows a large device as tiles with lane segments', () => {
    const ports = 32;
    const switchDevice = device('switch-01', [...Array.from({ length: ports }, (_, index) => C(`swp${index + 1}`, 'NetworkPort', `swp${index + 1}`, undefined, 'SFP28')), C('swp99', 'NetworkPort', 'swp99', 4, 'QSFP28')]);
    const hosts = Array.from({ length: 10 }, (_, index) => device(`host-${index + 1}`, [C('eth0', 'NetworkPort')]));
    const cables = hosts.map((host, index) => cable(`dac-${index + 1}`, 'Copper', D('switch-01', `swp${index + 1}`), D(host.metadata.name, 'eth0')));
    show(switchDevice, data(undefined, [switchDevice, ...hosts], cables));
    expect(within(grid()).getByText(/^Network ·/)).toHaveTextContent(`Network · ${ports + 1} · 10 recorded · ${ports - 9} unknown`);
    expect(tiles()).toHaveLength(ports + 1);
    expect(tiles().filter(item => item.tabIndex === 0)).toHaveLength(1);
    // Above TEXT_TILE_LIMIT the tiles carry no text: faint numbers mark every 8th port and bank edges.
    expect(ports + 1).toBeGreaterThan(TEXT_TILE_LIMIT);
    expect(tiles().map(item => item.textContent).filter(Boolean)).toEqual([]);
    const numbers = [...grid().querySelectorAll('.inventory-port-number')].map(item => item.textContent).filter(Boolean);
    expect(numbers).toEqual(['1', '9', '17', '25', '99']);
    expect(grid().querySelector('.inventory-port-number')).toHaveAttribute('aria-hidden', 'true');
    // The tooltip still names the port.
    fireEvent.focus(tile(/^swp20:/));
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent(/^swp20 · no cable recorded$/);
    const qsfp = tile(/^swp99:/);
    expect(qsfp).toHaveAttribute('data-shape', 'qsfp');
    expect(qsfp.querySelectorAll('.inventory-port-lanes > span')).toHaveLength(4);
    expect(qsfp).toHaveAccessibleName('swp99: No cable recorded; position 1 no cable recorded; position 2 no cable recorded; position 3 no cable recorded; position 4 no cable recorded');
    // One row per component: the breakout's four positions share one row.
    expect(tableRows()).toHaveLength(ports + 1);
    const breakout = tableRows().find(row => cells(row)[0].textContent === 'swp99')!;
    expect(cells(breakout)[1]).toHaveTextContent(/^Network QSFP28 · 4 positions$/);
  });
});

describe('component table', () => {
  const omitted = { deviceRef: { name: 'box-01' }, componentID: 'eth0' } as CableEndpoint;
  const box = device('box-01', [
    { ...C('psu-1', 'PowerInlet', 'PSU 1', undefined, 'IEC C14'), serial: 'SYNTH-PSU-1' },
    C('eth0', 'NetworkPort', 'Ethernet 0', undefined, 'SFP28'),
    C('qsfp1', 'NetworkPort', 'Breakout 1', 4, 'QSFP28'),
    C('bay-1', 'Module', 'Bay 1'),
    C('usb-1', 'InventoryItem', 'USB 1', undefined, 'USB-A'),
  ], { deviceTypeRef: { name: 'box-type' } });
  const peers = device('leaf-02', [C('swp1', 'NetworkPort'), C('swp2', 'NetworkPort'), C('swp3', 'NetworkPort')]);
  const catalog: PhysicalRecord[] = [
    { kind: 'Manufacturer', metadata: { name: 'example-maker', namespace: NS }, spec: { displayName: 'Example maker', canonicalName: 'Example maker' } },
    { kind: 'DeviceType', metadata: { name: 'box-type', namespace: NS }, spec: { displayName: 'Example box', manufacturerRef: { name: 'example-maker' }, model: 'example-box', componentTemplates: [...box.spec.components!, C('console', 'ConsolePort', 'Serial console', undefined, 'RJ45')] } },
  ];
  const twoLanes: PhysicalRecord = { kind: 'Cable', metadata: { name: 'zero-01', namespace: NS }, spec: { displayName: 'zero-01', medium: 'Copper', lanes: [{ id: 'l1', a: D('box-01', 'eth0'), b: D('leaf-02', 'swp1') }, { id: 'l2', a: D('box-01', 'eth0', 0), b: D('leaf-02', 'swp3') }] } };
  const records = [
    feed, ...catalog, twoLanes,
    cable('pwr-02', 'Power', P('feed-hidden', 'outlet-7'), D('box-01', 'psu-1')),
    cable('brk-01', 'Fiber', D('box-01', 'qsfp1', 2), D('leaf-02', 'swp2')),
    cable('ghost-01', 'Copper', D('box-01', 'ghost'), D('leaf-02', 'swp3')),
    cable('omitted-01', 'Power', omitted, P('feed-hidden', 'outlet-7')),
  ];
  const naming = (inventory: Inventory) => inventory.physical.filter(record => record.kind === 'Cable' && record.spec.lanes?.some(lane => lane.a.deviceRef?.name === 'box-01' || lane.b.deviceRef?.name === 'box-01')).map(record => record.metadata.name);
  const headers = () => within(table()).getAllByRole('columnheader').map(cell => cell.textContent);

  const stateWords = (row: HTMLElement) => cells(row)[3].textContent?.match(/^(?:. )?(Cable recorded|No cable recorded|Records disagree|Peer unavailable|Catalog only|Component recorded)/)?.[1];

  it('lists every component once and every Cable that names the Device, unmatched lane ends last', () => {
    const inventory = data(undefined, [box, peers], records);
    show(box, inventory);
    expect(headers()).toEqual(['Component', 'Type', 'Face', 'State', 'Connected to', 'Serial']);
    const rows = tableRows();
    expect(rows.map(row => cells(row)[0].textContent)).toEqual([
      'PSU 1', 'Ethernet 0', 'Breakout 1', 'Serial console', 'Bay 1', 'USB 1', 'ghost',
      'Ethernet 0 · position 0', 'Ethernet 0 · position not recorded',
    ]);
    // PSU 1 disagrees: the unmatched omitted-01 names the same feed outlet as pwr-02.
    expect(rows.map(stateWords)).toEqual([
      'Records disagree', 'Cable recorded', 'Cable recorded', 'Catalog only', 'Component recorded', 'Component recorded', 'Records disagree', 'Records disagree', 'Records disagree',
    ]);
    // Short type words with the connector inline; a breakout is one row, its positions in the peer cell.
    expect(rows.map(row => cells(row)[1].textContent)).toEqual([
      'Power inlet IEC C14', 'Network SFP28', 'Network QSFP28 · 4 positions', 'Console RJ45', 'Module', 'Item USB-A', 'Not on this Device', 'Network SFP28', 'Network SFP28',
    ]);
    expect(cells(rows[2])[4]).toHaveTextContent(/^Position 2 · Example leaf-02 · swp2 via brk-01/);
    expect(cells(rows[2])[4]).toHaveTextContent('No cable recorded at position 1, 3, 4');
    expect(within(cells(rows[2])[4]).getByRole('link', { name: 'brk-01' })).toHaveAttribute('title', 'Fiber');
    expect(cells(rows[0])[5]).toHaveTextContent('SYNTH-PSU-1');
    expect(rows.at(-2)).toHaveAttribute('data-unmatched');
    // The reason is the state's tooltip, and stays in the accessible text.
    const reason = 'The Cable names position 0; this Device records one position for "eth0".';
    expect(cells(rows.at(-2)!)[3]).toHaveTextContent(reason);
    expect(cells(rows.at(-2)!)[3].querySelector('[title]')!.getAttribute('title')).toContain(reason);
    // Every Cable that names the Device is a link in the table, including the unmatched lane ends.
    const linked = new Set(within(table()).getAllByRole('link').map(item => item.textContent));
    expect(naming(inventory).filter(name => !linked.has(name))).toEqual([]);
    expect(within(rows.at(-1)!).getByRole('link', { name: 'omitted-01' })).toBeInTheDocument();
    expect(within(rows.at(-2)!).getByRole('link', { name: 'zero-01' })).toBeInTheDocument();
  });

  it('adds serial and asset tag columns only when some component records one', () => {
    const first = show(server, data());
    expect(headers()).toEqual(['Component', 'Type', 'Face', 'State', 'Connected to']);
    first.unmount();
    const tagged = device('server-01', [server.spec.components![0], { ...server.spec.components![1], assetTag: 'EX-TAG-2' }, ...server.spec.components!.slice(2)]);
    show(tagged, data(undefined, [tagged, leaf]));
    expect(headers()).toEqual(['Component', 'Type', 'Face', 'State', 'Connected to', 'Asset tag']);
    expect(cells(tableRows()[1]).at(-1)).toHaveTextContent('EX-TAG-2');
  });

  it('keeps a denied peer nameless in the table, unmatched rows included', () => {
    const view = show(box, data({ PowerFeed: { status: 403 } }, [box, peers], records));
    expect(cells(tableRows().at(-1)!)[4]).toHaveTextContent(/^Power feed unavailable You cannot read this peer via omitted-01$/);
    expect(view.container.textContent).not.toMatch(HIDDEN_FEED);
    expect(accessibleNames(view.container)).not.toMatch(HIDDEN_FEED);
    expect(tooltips(view.container)).not.toMatch(HIDDEN_FEED);
  });

  it('lists components without states or peers when Cables are denied, and never calls a port uncabled', () => {
    show(box, data({ Cable: { status: 403 } }, [box, peers], records));
    expect(screen.getByText(/Cable records are unavailable to you/)).toBeInTheDocument();
    expect(screen.getByRole('table', { name: 'Components' })).toBeInTheDocument();
    expect(headers()).toEqual(['Component', 'Type', 'Face', 'Serial']);
    expect(tableRows()).toHaveLength(5);
    expect(screen.queryByText(/No cable recorded|Cable recorded|Unknown/)).toBeNull();
    // The picture still shows the recorded ports, every one unknown.
    expect(tiles().map(item => item.getAttribute('data-state'))).toEqual(['unavailable', 'unavailable', 'unavailable']);
  });
});

describe('Device detail connection view', () => {
  it('appears on the Device page and links the recorded power cable from the port card', () => {
    const inventory = toInventory(fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords);
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={['/inventory?kind=Device&record=rack-01-u12-synthetic-0001&namespace=default']}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
    const section = screen.getByRole('region', { name: 'Components and connections' });
    expect(within(section).queryByRole('button', { name: 'Legend' })).toBeNull();
    fireEvent.click(within(section).getByRole('button', { name: 'Power supply 1: Cable recorded, to Feed A · Outlet 1, via Server power cable' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('link', { name: 'Server power cable' }));
    expect(screen.getByRole('heading', { name: 'Server power cable' })).toBeInTheDocument();
  });
});
