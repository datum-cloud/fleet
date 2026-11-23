import { fireEvent, render, screen, within } from '@testing-library/react';
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
const C = (id: string, type: PhysicalComponent['type'], name = id, positions?: number): PhysicalComponent => ({ id, name, type, ...(positions ? { positions } : {}) });
const device = (name: string, components: PhysicalComponent[]): Device => ({
  metadata: { name, namespace: NS },
  spec: { claimRef: { name: `${name}-claim` }, identity: { model: 'example-model', serial: `SYNTH-${name}` }, displayName: `Example ${name}`, topologyAuthority: 'Cable', components },
});
const D = (name: string, componentID: string, position = 1): CableEndpoint => ({ deviceRef: { name }, componentID, position });
const P = (name: string, componentID: string): CableEndpoint => ({ powerFeedRef: { name }, componentID, position: 1 });
const cable = (name: string, medium: string, a: CableEndpoint, b: CableEndpoint): PhysicalRecord => ({ kind: 'Cable', metadata: { name, namespace: NS }, spec: { displayName: name, medium, lanes: [{ id: 'l1', a, b }] } });
const feed: PhysicalRecord = { kind: 'PowerFeed', metadata: { name: 'feed-hidden', namespace: NS }, spec: { displayName: 'Hidden feed', supply: { type: 'AC', voltageV: 208, currentA: 30 }, terminations: [C('outlet-7', 'PowerOutlet', 'Outlet 7')] } };

const server = device('server-01', [C('psu-1', 'PowerInlet', 'PSU 1'), C('psu-2', 'PowerInlet', 'PSU 2'), C('eth0', 'NetworkPort', 'Ethernet 0'), C('eth1', 'NetworkPort', 'Ethernet 1')]);
const leaf = device('leaf-01', [C('swp1', 'NetworkPort'), C('swp2', 'NetworkPort')]);
const serverRecords = [feed, cable('pwr-01', 'Power', P('feed-hidden', 'outlet-7'), D('server-01', 'psu-1')), cable('dac-01', 'Copper', D('server-01', 'eth0'), D('leaf-01', 'swp1'))];
const data = (issues?: Inventory['issues'], devices = [server, leaf], physical = serverRecords): Inventory => ({ ...toInventory([], devices, physical), issues });

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

describe('DeviceConnections', () => {
  it('labels the view as records, not live state, and toggles between diagram and list', () => {
    show(server, data());
    expect(screen.getByText('Cable records, not live link state')).toBeInTheDocument();
    expect(screen.getByText(/Nothing here is verified/)).toBeInTheDocument();
    expect(screen.queryByText(/^Verified$/)).not.toBeInTheDocument();
    const toggle = screen.getByRole('group', { name: 'Connection view' });
    expect(within(toggle).getByRole('button', { name: 'Diagram' })).toHaveAttribute('aria-pressed', 'true');
    expect(within(diagram()).getByRole('list', { name: 'Power links' })).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();

    fireEvent.click(within(toggle).getByRole('button', { name: 'List' }));
    expect(within(toggle).getByRole('button', { name: 'List' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByRole('group', { name: /Connection diagram/ })).not.toBeInTheDocument();
    const network = screen.getByRole('table', { name: /Network · 2 links/ });
    const rows = within(network).getAllByRole('row').slice(1);
    expect(rows.map(row => within(row).getAllByRole('cell')[1].textContent)).toEqual(['● Cable recorded', '○ No cable recorded']);
    expect(within(rows[0]).getByRole('link', { name: 'dac-01' })).toHaveAttribute('href', '/inventory?kind=Cable&record=dac-01&namespace=default');
    expect(within(rows[0]).getByRole('link', { name: 'Example leaf-01 · swp1' })).toHaveAttribute('href', '/inventory?kind=Device&record=leaf-01&namespace=default');
    expect(within(rows[1]).getByText('Unknown · not free')).toBeInTheDocument();
  });

  it('shows every state with a symbol and words in the legend', () => {
    show(server, data());
    const legend = screen.getByRole('list', { name: 'Record states' });
    expect(within(legend).getAllByRole('listitem').map(item => item.textContent)).toEqual(['● Cable recorded', '○ No cable recorded', '! Records disagree', '? Peer unavailable', '◇ Catalog only']);
  });

  it('moves through cables and peers with arrow keys using one tab stop, and opens the focused link', () => {
    show(server, data());
    const items = [...diagram().querySelectorAll<HTMLElement>('[data-connection-roving]')];
    expect(items.map(item => item.getAttribute('tabindex'))).toEqual(['0', '-1', '-1', '-1']);
    items[0].focus();
    fireEvent.keyDown(items[0], { key: 'ArrowDown' });
    expect(document.activeElement).toBe(items[1]);
    expect(items[1]).toHaveAttribute('tabindex', '0');
    expect(items[0]).toHaveAttribute('tabindex', '-1');
    fireEvent.keyDown(items[1], { key: 'End' });
    expect(document.activeElement).toBe(items[3]);
    fireEvent.keyDown(items[3], { key: 'Home' });
    expect(document.activeElement).toBe(items[0]);
    fireEvent.keyDown(items[0], { key: 'ArrowUp' });
    expect(document.activeElement).toBe(items[0]);
    // The roving items are anchors, so Enter follows them natively.
    const focused = document.activeElement as HTMLAnchorElement;
    expect(focused.tagName).toBe('A');
    expect(focused).toHaveAccessibleName('PSU 1 to Hidden feed · Outlet 7 via cable pwr-01, Power: Cable recorded');
    fireEvent.click(focused);
    expect(screen.getByTestId('location')).toHaveTextContent('?kind=Cable&record=pwr-01&namespace=default');
    fireEvent.click(within(diagram()).getByRole('link', { name: 'Peer Example leaf-01 · swp1' }));
    expect(screen.getByTestId('location')).toHaveTextContent('?kind=Device&record=leaf-01&namespace=default');
  });

  it('shows an unreadable peer as peer unavailable without leaking its identity', () => {
    const view = show(server, data({ PowerFeed: { status: 403 } }));
    const power = within(diagram()).getByRole('list', { name: 'Power links' });
    const [psu1] = within(power).getAllByRole('listitem');
    expect(psu1).toHaveAttribute('data-state', 'unavailable');
    expect(within(psu1).getByText('Power feed unavailable')).toBeInTheDocument();
    expect(within(psu1).getByText('You cannot read this peer')).toBeInTheDocument();
    expect(within(psu1).getAllByRole('link')).toHaveLength(1);
    expect(within(psu1).getByRole('link', { name: /PSU 1 to Power feed unavailable via cable pwr-01/ })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'List' }));
    expect(view.container.textContent).not.toMatch(/feed-hidden|Hidden feed|outlet-7|Outlet 7|208 V/);
  });

  it('treats unreadable Cables as unknown connections, never as uncabled', () => {
    show(server, data({ Cable: { status: 403 } }));
    expect(screen.getByText(/Cable records are unavailable to you/)).toBeInTheDocument();
    expect(screen.queryByText('No cable recorded')).not.toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'Connection view' })).not.toBeInTheDocument();
  });

  it('defaults a large device to the list and collapses the diagram by group with counts', () => {
    const ports = DIAGRAM_LINK_LIMIT + 16;
    const switchDevice = device('switch-01', Array.from({ length: ports }, (_, index) => C(`swp${index + 1}`, 'NetworkPort')));
    const hosts = Array.from({ length: 10 }, (_, index) => device(`host-${index + 1}`, [C('eth0', 'NetworkPort')]));
    const cables = hosts.map((host, index) => cable(`dac-${index + 1}`, 'Copper', D('switch-01', `swp${index + 1}`), D(host.metadata.name, 'eth0')));
    show(switchDevice, data(undefined, [switchDevice, ...hosts], cables));
    expect(screen.getByRole('button', { name: 'List' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByText(new RegExp(`${ports} links is too many to draw clearly`))).toBeInTheDocument();
    expect(screen.getByRole('table', { name: new RegExp(`Network · ${ports} links`) })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Diagram' }));
    const group = diagram().querySelector('details')!;
    expect(group).not.toHaveAttribute('open');
    expect(group.querySelector('summary')).toHaveTextContent(`Network · ${ports} links 10 cable recorded, ${ports - 10} no cable recorded`);
    // Collapsed links are not tab stops.
    expect(diagram().querySelectorAll('[data-connection-roving][tabindex="0"]')).toHaveLength(0);
    group.open = true;
    fireEvent(group, new Event('toggle'));
    expect(diagram().querySelectorAll('[data-connection-roving][tabindex="0"]')).toHaveLength(1);
  });

  it('keeps a small device in the diagram by default', () => {
    show(leaf, data());
    expect(screen.getByRole('button', { name: 'Diagram' })).toHaveAttribute('aria-pressed', 'true');
    expect(diagram().querySelector('details')).toBeNull();
    expect(diagram()).not.toHaveAttribute('data-left');
  });

  it('shows disagreeing records as two lines into one port, with the reason', () => {
    const records = [...serverRecords, cable('dac-02', 'Copper', D('server-01', 'eth0'), D('leaf-01', 'swp2'))];
    show(server, data(undefined, [server, leaf], records));
    const network = within(diagram()).getByRole('list', { name: 'Network links' });
    const conflicts = within(network).getAllByRole('listitem').filter(item => item.getAttribute('data-state') === 'conflict');
    expect(conflicts).toHaveLength(2);
    expect(conflicts[0]).toHaveTextContent('2 Cable lanes name this position. Neither is treated as current.');
    expect(conflicts[0]).toHaveTextContent('! dac-01');
  });
});

describe('Device detail connection view', () => {
  it('appears on the Device page and links the recorded power cable', () => {
    const inventory = toInventory(fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords);
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={['/inventory?kind=Device&record=rack-01-u12-synthetic-0001&namespace=default']}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
    const section = screen.getByRole('region', { name: 'Connections' });
    expect(within(section).getByText('Cable records, not live link state')).toBeInTheDocument();
    fireEvent.click(within(section).getByRole('link', { name: /Power supply 1 to Feed A · Outlet 1 via cable Server power cable/ }));
    expect(screen.getByRole('heading', { name: 'Server power cable' })).toBeInTheDocument();
  });
});
