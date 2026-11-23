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
import { DIAGRAM_LINK_LIMIT } from '../lib/device-connections';

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

function show(target: Device, inventory: Inventory) {
  return render(
    <MemoryRouter initialEntries={['/inventory']}>
      <DeviceConnections device={target} inventory={inventory} href={inventoryHref} />
      <Location />
    </MemoryRouter>,
  );
}

const diagram = () => screen.getByRole('group', { name: /Connection diagram/ });
const grid = () => screen.getByRole('group', { name: /Port tiles/ });
const tiles = () => within(grid()).getAllByRole('button').filter(item => item.hasAttribute('data-tile'));
const tile = (name: RegExp) => within(grid()).getByRole('button', { name });
const card = () => screen.getByRole('dialog');
const accessibleNames = (root: HTMLElement) => [...root.querySelectorAll('[aria-label]')].map(item => item.getAttribute('aria-label')).join('\n');

describe('DeviceConnections', () => {
  it('labels the view as records, not live state, opens on port tiles, and toggles to diagram and list', () => {
    show(server, data());
    expect(screen.getByText('Cable records, not live link state')).toBeInTheDocument();
    expect(screen.getByText(/nothing here is verified/)).toBeInTheDocument();
    expect(screen.getByText(/Unknown is not free/)).toBeInTheDocument();
    expect(screen.queryByText(/^Verified$/)).not.toBeInTheDocument();
    const toggle = screen.getByRole('group', { name: 'Connection view' });
    expect(within(toggle).getByRole('button', { name: 'Ports' })).toHaveAttribute('aria-pressed', 'true');
    expect(within(grid()).getByRole('list', { name: 'Power ports' })).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();

    fireEvent.click(within(toggle).getByRole('button', { name: 'Diagram' }));
    expect(within(diagram()).getByRole('list', { name: 'Power links' })).toBeInTheDocument();

    fireEvent.click(within(toggle).getByRole('button', { name: 'List' }));
    expect(within(toggle).getByRole('button', { name: 'List' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByRole('group', { name: /Connection diagram|Port tiles/ })).not.toBeInTheDocument();
    const network = screen.getByRole('table', { name: /Network · 2 links/ });
    const rows = within(network).getAllByRole('row').slice(1);
    expect(rows.map(row => within(row).getAllByRole('cell')[1].textContent)).toEqual(['● Cable recorded', '○ No cable recorded']);
    expect(within(rows[0]).getByRole('link', { name: 'dac-01' })).toHaveAttribute('href', '/inventory?kind=Cable&record=dac-01&namespace=default');
    expect(within(rows[0]).getByRole('link', { name: 'Example leaf-01 · swp1' })).toHaveAttribute('href', '/inventory?kind=Device&record=leaf-01&namespace=default');
    expect(within(rows[1]).getByText('Unknown · not free')).toBeInTheDocument();
  });

  it('summarises once, without the Device name or resource name, and keeps the legend behind a toggle', () => {
    const view = show(server, data());
    expect(screen.getByText(/^4 ports · 2 recorded · 2 unknown\./)).toBeInTheDocument();
    // The page heading already names the Device; the view does not repeat it.
    expect(view.container.textContent).not.toMatch(/server-01/);
    expect(screen.queryByRole('list', { name: 'Record states' })).not.toBeInTheDocument();
    const toggle = screen.getByRole('button', { name: 'Legend' });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(toggle);
    expect(screen.getByRole('button', { name: 'Hide legend' })).toHaveAttribute('aria-expanded', 'true');
    const legend = screen.getByRole('list', { name: 'Record states' });
    const items = within(legend).getAllByRole('listitem');
    expect(items.map(item => item.getAttribute('data-state'))).toEqual(['cabled', 'none', 'conflict', 'unavailable', 'catalog']);
    expect(items.map(item => within(item).getByText(/Cable recorded|No cable recorded|Records disagree|Peer unavailable|Catalog only/).textContent)).toEqual(['● Cable recorded', '○ No cable recorded', '! Records disagree', '? Peer unavailable', '◇ Catalog only']);
  });

  it('draws each port as a tile with its state in the accessible name, a glyph where outline alone is ambiguous, and no type words', () => {
    const records = [...serverRecords, cable('dac-02', 'Copper', D('server-01', 'eth1'), D('leaf-09', 'swp1'))];
    show(server, data(undefined, [server, leaf], records));
    expect(tiles().map(item => item.getAttribute('aria-label'))).toEqual([
      'PSU 1: Cable recorded, to Hidden feed · Outlet 7, via pwr-01',
      'PSU 2: No cable recorded, unknown, not free',
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

  it('marks disagreeing records with a ! tile and lists every claiming Cable, with the reason, in the card', () => {
    const records = [...serverRecords, cable('dac-02', 'Copper', D('server-01', 'eth0'), D('leaf-01', 'swp2'))];
    show(server, data(undefined, [server, leaf], records));
    const eth0 = tile(/^Ethernet 0: Records disagree; 2 Cables: to Example leaf-01 · swp1, via dac-01; to Example leaf-01 · swp2, via dac-02$/);
    expect(eth0).toHaveAttribute('data-state', 'conflict');
    expect(eth0).toHaveTextContent('eth0!');
    fireEvent.click(eth0);
    const links = within(card()).getAllByRole('link', { name: /^dac-0[12]$/ });
    expect(links.map(link => link.textContent)).toEqual(['dac-01', 'dac-02']);
    expect(card()).toHaveTextContent('2 Cable lanes name this position. Neither is treated as current.');
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

  it('previews a card on keyboard focus, pins it on click or Enter, follows the arrow keys, and closes on Escape', () => {
    show(server, data());
    const [psu1, psu2] = tiles();
    fireEvent.focus(psu1);
    // The preview repeats the tile's accessible name, so it is hidden from assistive technology.
    const preview = screen.getByRole('tooltip', { hidden: true });
    expect(preview).toHaveAttribute('aria-hidden', 'true');
    expect(preview).toHaveTextContent('PSU 1');
    expect(preview).toHaveTextContent('Hidden feed · Outlet 7');
    expect(preview).toHaveTextContent('No health reported');
    expect(within(preview).queryByRole('link', { hidden: true })).toBeNull();

    // A native button turns Enter and Space into a click.
    psu1.focus();
    fireEvent.click(psu1);
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull();
    expect(psu1).toHaveAttribute('aria-expanded', 'true');
    expect(psu1).toHaveAttribute('aria-controls', card().id);
    expect(card()).toHaveAccessibleName('PSU 1');
    expect(within(card()).getByText('Health').nextElementSibling).toHaveTextContent('No health reported');
    expect(within(card()).getByRole('link', { name: 'pwr-01' })).toHaveAttribute('href', '/inventory?kind=Cable&record=pwr-01&namespace=default');
    expect(within(card()).getByRole('link', { name: 'Hidden feed · Outlet 7' })).toHaveAttribute('href', '/inventory?kind=PowerFeed&record=feed-hidden&namespace=default');
    // Full resource names live only here.
    expect(within(card()).getByText('feed-hidden').tagName).toBe('CODE');

    fireEvent.keyDown(psu1, { key: 'ArrowRight' });
    expect(document.activeElement).toBe(psu2);
    expect(card()).toHaveAccessibleName('PSU 2');
    expect(card()).toHaveTextContent('Unknown · not free');

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
    fireEvent.focus(psu1);
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('Power feed unavailable');
    expect(view.container.textContent).not.toMatch(HIDDEN_FEED);
    fireEvent.click(psu1);
    expect(card()).toHaveTextContent('You cannot read this peer');
    // Only the Cable is a link; the peer has no name, link or record name.
    expect(within(card()).getAllByRole('link').map(link => link.textContent)).toEqual(['pwr-01']);
    expect(within(card()).queryByText('Peer record')).toBeNull();
    for (const target of tiles()) { fireEvent.focus(target); fireEvent.click(target); }
    expect(view.container.textContent).not.toMatch(HIDDEN_FEED);
    expect(accessibleNames(view.container)).not.toMatch(HIDDEN_FEED);
    for (const name of ['Diagram', 'List']) {
      fireEvent.click(screen.getByRole('button', { name }));
      expect(view.container.textContent).not.toMatch(HIDDEN_FEED);
      expect(accessibleNames(view.container)).not.toMatch(HIDDEN_FEED);
    }
  });

  it('never shows generated resource names by default, and tells same-named records apart', () => {
    const hostA = 'netbox-30ba6ae0ee289bda385a7a96';
    const hostB = 'netbox-41cb7bf1ff39acebb496b8a7';
    // Imported records: one with its generated name copied into displayName, the rest with none.
    const imported = (name: string) => device(name, [C('eth0', 'NetworkPort')], { displayName: name, identity: { manufacturer: 'Example Networks', model: 'EX-1' } });
    const pdu = device('pdu-a.rk105.yul3', [C('out-1', 'PowerOutlet', 'Outlet 1')], { displayName: undefined });
    const SUBJECT = 'netbox-0f1e2d3c4b5a69788796a5b4';
    const subject = device(SUBJECT, [C('eth0', 'NetworkPort'), C('eth1', 'NetworkPort'), C('psu-1', 'PowerInlet', 'PSU1-A')], { displayName: undefined, identity: { model: 'EX-9' } });
    const records = [
      cable('netbox-ca213d185ea9153b853f295e', 'Copper', D(SUBJECT, 'eth0'), D(hostA, 'eth0'), 'netbox-ca213d185ea9153b853f295e'),
      cable('netbox-77aa13d185ea9153b853a1c0', 'Copper', D(SUBJECT, 'eth1'), D(hostB, 'eth0'), ''),
      cable('netbox-99bb13d185ea9153b853b2d1', 'Power', D(SUBJECT, 'psu-1'), D('pdu-a.rk105.yul3', 'out-1'), ''),
    ];
    const view = show(subject, data(undefined, [subject, imported(hostA), imported(hostB), pdu], records));
    expect(view.container.textContent).not.toMatch(/netbox-/);
    expect(accessibleNames(view.container)).not.toMatch(/netbox-/);
    expect(tiles().map(item => item.getAttribute('aria-label'))).toEqual([
      'eth0: Cable recorded, to Unnamed device #7a96 · eth0, via Copper cable #295e',
      'eth1: Cable recorded, to Unnamed device #b8a7 · eth0, via Copper cable #a1c0',
      'PSU1-A: Cable recorded, to pdu-a.rk105.yul3 · Outlet 1, via Power cable',
    ]);
    // The resource names are in the pinned card only.
    fireEvent.click(tiles()[0]);
    expect(within(card()).getByText('netbox-ca213d185ea9153b853f295e').tagName).toBe('CODE');
    expect(within(card()).getByText(hostA).tagName).toBe('CODE');
    fireEvent.click(within(card()).getByRole('button', { name: /^Close/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Diagram' }));
    expect(diagram()).toHaveTextContent('EX-9');
    expect(view.container.textContent).not.toMatch(/netbox-/);
    fireEvent.click(screen.getByRole('button', { name: 'List' }));
    expect(view.container.textContent).not.toMatch(/netbox-/);
  });

  it('keeps groups that are entirely catalog only behind a count until expanded', () => {
    const typed = device('server-03', [C('eth0', 'NetworkPort', 'Ethernet 0')], { deviceTypeRef: { name: 'example-type' } });
    const physical: PhysicalRecord[] = [
      { kind: 'Manufacturer', metadata: { name: 'example-maker', namespace: NS }, spec: { displayName: 'Example maker', canonicalName: 'Example maker' } },
      { kind: 'DeviceType', metadata: { name: 'example-type', namespace: NS }, spec: { displayName: 'Example type', manufacturerRef: { name: 'example-maker' }, model: 'example-model', componentTemplates: [C('eth0', 'NetworkPort', 'Ethernet 0'), C('console', 'ConsolePort', 'Serial console', undefined, 'RJ45')] } },
    ];
    show(typed, data(undefined, [typed], physical));
    expect(screen.getByText(/^2 ports · 1 unknown · 1 catalog only\./)).toBeInTheDocument();
    expect(within(grid()).queryByRole('list', { name: 'Console ports' })).toBeNull();
    fireEvent.click(within(grid()).getByRole('button', { name: 'Show Console · 1 port, catalog only' }));
    const consoleTile = tile(/^Serial console:/);
    expect(consoleTile).toHaveAccessibleName('Serial console: Catalog only');
    expect(consoleTile).toHaveAttribute('data-shape', 'rj45');
  });

  it('treats unreadable Cables as unknown connections, never as uncabled', () => {
    show(server, data({ Cable: { status: 403 } }));
    expect(screen.getByText(/Cable records are unavailable to you/)).toBeInTheDocument();
    expect(screen.queryByText('No cable recorded')).not.toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'Connection view' })).not.toBeInTheDocument();
    expect(screen.queryByRole('group', { name: /Port tiles/ })).not.toBeInTheDocument();
  });

  it('shows a large device as tiles with lane segments, and offers no diagram above the limit', () => {
    const ports = DIAGRAM_LINK_LIMIT + 16;
    const switchDevice = device('switch-01', [...Array.from({ length: ports }, (_, index) => C(`swp${index + 1}`, 'NetworkPort', `swp${index + 1}`, undefined, 'SFP28')), C('swp99', 'NetworkPort', 'swp99', 4, 'QSFP28')]);
    const hosts = Array.from({ length: 10 }, (_, index) => device(`host-${index + 1}`, [C('eth0', 'NetworkPort')]));
    const cables = hosts.map((host, index) => cable(`dac-${index + 1}`, 'Copper', D('switch-01', `swp${index + 1}`), D(host.metadata.name, 'eth0')));
    show(switchDevice, data(undefined, [switchDevice, ...hosts], cables));
    expect(screen.getByRole('button', { name: 'Ports' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByRole('button', { name: 'Diagram' })).toBeNull();
    expect(within(grid()).getByText(/^Network ·/)).toHaveTextContent(`Network · ${ports + 1} · 10 recorded · ${ports - 9} unknown`);
    expect(tiles()).toHaveLength(ports + 1);
    expect(tiles().filter(item => item.tabIndex === 0)).toHaveLength(1);
    const qsfp = tile(/^swp99:/);
    expect(qsfp).toHaveAttribute('data-shape', 'qsfp');
    expect(qsfp.querySelectorAll('.inventory-port-lanes > span')).toHaveLength(4);
    expect(qsfp).toHaveAccessibleName('swp99: No cable recorded; position 1 no cable recorded; position 2 no cable recorded; position 3 no cable recorded; position 4 no cable recorded');
    fireEvent.click(screen.getByRole('button', { name: 'List' }));
    expect(screen.getByRole('table', { name: new RegExp(`Network · ${ports + 4} links`) })).toBeInTheDocument();
  });

  it('keeps the diagram for a small device, with one tab stop over cables and peers', () => {
    show(server, data());
    fireEvent.click(screen.getByRole('button', { name: 'Diagram' }));
    const items = [...diagram().querySelectorAll<HTMLElement>('[data-connection-roving]')];
    expect(items.map(item => item.getAttribute('tabindex'))).toEqual(['0', '-1', '-1', '-1']);
    items[0].focus();
    fireEvent.keyDown(items[0], { key: 'ArrowDown' });
    expect(document.activeElement).toBe(items[1]);
    fireEvent.keyDown(items[1], { key: 'End' });
    expect(document.activeElement).toBe(items[3]);
    fireEvent.keyDown(items[3], { key: 'Home' });
    expect(document.activeElement).toBe(items[0]);
    // The roving items are anchors, so Enter follows them natively.
    const focused = document.activeElement as HTMLAnchorElement;
    expect(focused.tagName).toBe('A');
    expect(focused).toHaveAccessibleName('PSU 1 to Hidden feed · Outlet 7 via cable pwr-01, Power: Cable recorded');
    fireEvent.click(focused);
    expect(screen.getByTestId('location')).toHaveTextContent('?kind=Cable&record=pwr-01&namespace=default');
    fireEvent.click(within(diagram()).getByRole('link', { name: 'Peer Example leaf-01 · swp1' }));
    expect(screen.getByTestId('location')).toHaveTextContent('?kind=Device&record=leaf-01&namespace=default');
    // The drawn Device carries its name and one summary, not its resource name or bare kind words.
    expect(diagram().querySelector('.inventory-connection-device')).toHaveTextContent(/^Example server-01\s*4 ports · 2 recorded · 2 unknown$/);
    expect(within(diagram()).queryByText(/^Device$/)).toBeNull();
    expect(diagram().textContent).not.toMatch(/Power · cable recorded/);
  });
});

describe('Device detail connection view', () => {
  it('appears on the Device page and links the recorded power cable from the port card', () => {
    const inventory = toInventory(fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords);
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={['/inventory?kind=Device&record=rack-01-u12-synthetic-0001&namespace=default']}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
    const section = screen.getByRole('region', { name: 'Connections' });
    expect(within(section).getByText('Cable records, not live link state')).toBeInTheDocument();
    fireEvent.click(within(section).getByRole('button', { name: 'Power supply 1: Cable recorded, to Feed A · Outlet 1, via Server power cable' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('link', { name: 'Server power cable' }));
    expect(screen.getByRole('heading', { name: 'Server power cable' })).toBeInTheDocument();
  });
});
