import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import { PhysicalInventory } from './physical-inventory';
import { fixtureInventory, FIXTURE_NAMES } from '../lib/inventory-fixtures';
import { redactInventory } from '../lib/inventory-read-access';
import type { Inventory } from '../inventory';

function show(name: string, inventory: Inventory = fixtureInventory()) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <MemoryRouter initialEntries={[`/inventory?kind=Device&record=${name}&namespace=default&tab=hardware`]}><PhysicalInventory inventory={inventory} /></MemoryRouter>
  </QueryClientProvider>);
}
const MATCHED = FIXTURE_NAMES.server;
const MISMATCHED = 'example-srv-a01-02';
const PENDING = FIXTURE_NAMES.singleFeedServer;
const hardware = () => screen.getByRole('region', { name: 'Hardware & management' });
const table = (label: string) => within(hardware()).getByRole('table', { name: label });
const rowOf = (label: string, first: string) => within(table(label)).getAllByRole('row').find(row => within(row).queryAllByRole('cell')[0]?.textContent === first)!;
const cells = (label: string, first: string) => within(rowOf(label, first)).getAllByRole('cell');
// Each list item holds one datum-ui Badge, which carries the tone, status and tooltip.
const chips = () => within(within(hardware()).getByRole('list', { name: 'Discovery conditions' })).getAllByRole('listitem').map(item => item.firstElementChild as HTMLElement);
const fact = (label: string) => within(hardware()).getByText(label, { selector: 'dt' }).nextElementSibling as HTMLElement;
const header = (container: HTMLElement) => container.querySelector<HTMLElement>('.inventory-fact-strip')!;

describe('Device page: expected next to observed identity', () => {
  it('shows spec and discovery side by side, plain when they agree', () => {
    show(MATCHED);
    expect(within(table('Identity')).getAllByRole('columnheader').map(cell => cell.textContent)).toEqual(['Field', 'Expected', 'Observed']);
    expect(cells('Identity', 'Serial').map(cell => cell.textContent)).toEqual(['Serial', 'SYNTH-EX-01', 'SYNTH-EX-01']);
    expect(cells('Identity', 'Model').map(cell => cell.textContent)).toEqual(['Model', 'EX-C1', 'Example EX-C1']);
    // Spec records no SKU or power state: the expected cell is blank.
    expect(cells('Identity', 'SKU').map(cell => cell.textContent)).toEqual(['SKU', '', 'SYNTH-SKU-1']);
    expect(cells('Identity', 'Power')[2]).toHaveTextContent(/^On$/);
    expect(table('Identity').querySelector('[data-tone]')).toBeNull();
    expect(hardware()).not.toHaveTextContent(/verified/i);
    // The table states the system UUID; the plain fact is not repeated.
    expect(within(hardware()).queryByText('System UUID', { selector: 'dt' })).toBeNull();
  });

  it('marks a serial IdentityMatched rejects red, with the condition message as its tooltip and accessible text', () => {
    show(MISMATCHED);
    const observed = cells('Identity', 'Serial')[2];
    const mark = observed.querySelector('[data-tone="danger"]')!;
    expect(mark).toHaveTextContent('SYNTH-OTHER-99');
    expect(mark).toHaveAttribute('title', 'the System with spec.identity.systemUUID reports another serial; spec is not changed');
    expect(observed).toHaveTextContent('Records disagree: the System with spec.identity.systemUUID reports another serial');
    expect(cells('Identity', 'Model')[2].querySelector('[data-tone]')).toBeNull();
    expect(chips().find(chip => chip.textContent?.includes('Identity'))).toHaveAttribute('data-tone', 'danger');
  });
});

describe('Device page: discovery health', () => {
  it('shows the five conditions as short chips with reason and message in the tooltip', () => {
    show(MATCHED);
    expect(chips().map(chip => chip.firstChild?.nextSibling?.textContent)).toEqual(['Reachable', 'Auth', 'Inventory', 'Identity', 'Key unique']);
    for (const chip of chips()) expect(chip).not.toHaveAttribute('data-tone');
    expect(chips()[0]).toHaveAttribute('title', 'Reachable: True · ServiceRootRead · TLS handshake completed and the anonymous service root was read');
    expect(chips()[0]).toHaveTextContent('Reachable: True · ServiceRootRead');
    expect(fact('Last attempt')).toHaveTextContent(/^2 min ago$/);
    expect(fact('Last attempt').querySelector('time')!.getAttribute('title')).toMatch(/^\d{4}-\d{2}-\d{2}T/);
    expect(fact('Key approval')).toHaveTextContent(/^Approved/);
  });

  it('shows a shortened TLS fingerprint with the full value in the tooltip and a copy button', () => {
    show(MATCHED);
    const code = fact('TLS key').querySelector('code')!;
    expect(code).toHaveTextContent(/^3f3f3f3f…3f3f$/);
    expect(code).toHaveAttribute('title', '3f'.repeat(32));
    expect(within(fact('TLS key')).getByRole('button', { name: 'Copy TLS key fingerprint' })).toBeInTheDocument();
    expect(fact('Key expiry').querySelector('[data-tone]')).toBeNull();
  });

  it('copies the full fingerprint and says so in the button and its tooltip', async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } });
    try {
      show(MATCHED);
      const copy = within(fact('TLS key')).getByRole('button', { name: 'Copy TLS key fingerprint' });
      expect(copy).toHaveAttribute('title', 'Copy TLS key fingerprint');
      fireEvent.click(copy);
      await waitFor(() => expect(copy).toHaveTextContent(/^Copied$/));
      expect(copy).toHaveAttribute('title', 'Copied');
      expect(writeText).toHaveBeenCalledWith('3f'.repeat(32));
    } finally { vi.unstubAllGlobals(); }
  });

  it('shows PinNotApproved as not approved, amber, with the reason in the tooltip, and an expiring key amber', () => {
    const { container } = show(PENDING);
    const approval = fact('Key approval').querySelector('[data-tone="warning"]')!;
    expect(approval).toHaveTextContent(/^Not approved/);
    expect(approval.getAttribute('title')).toMatch(/^PinNotApproved · the observed public key is in status.discovery.tls/);
    expect(fact('Key expiry').querySelector('[data-tone="warning"]')).toHaveTextContent(/ · 1[12] d/);
    expect(chips().find(chip => chip.textContent?.startsWith('✕ Auth'))).toHaveAttribute('data-tone', 'danger');
    // One chip in the header strip.
    const chip = header(container).querySelectorAll('.inventory-discovery-chip');
    expect(chip).toHaveLength(1);
    expect(chip[0]).toHaveTextContent(/^BMC key not approved/);
    expect(chip[0]).toHaveAttribute('data-tone', 'danger');
  });
});

describe('Device page: NICs and MAC addresses', () => {
  it('shows each host NIC with its link, amber when it is not up', () => {
    show(PENDING);
    expect(cells('Host NICs', 'EthernetInterface0')[2]).toHaveTextContent(/^Up$/);
    expect(cells('Host NICs', 'EthernetInterface0')[2].querySelector('[data-tone]')).toBeNull();
    const down = cells('Host NICs', 'EthernetInterface1')[2].querySelector('[data-tone="warning"]')!;
    expect(down).toHaveTextContent(/^Down/);
    expect(down).toHaveAttribute('title', 'LinkDown');
  });

  it('lists the BMC with its firmware, Redfish service root and NICs', () => {
    show(MATCHED);
    expect(within(table('BMC')).getAllByRole('columnheader').map(cell => cell.textContent)).toEqual(['Manager', 'Model', 'Firmware', 'Redfish', 'Vendor', 'Product']);
    expect(cells('BMC', 'BMC').map(cell => cell.textContent)).toEqual(['BMC', 'Example BMC 9', '7.10.30', '1.11.0', 'Example Compute', 'Example Integrated BMC']);
    expect(cells('BMC NICs', 'eth0').map(cell => cell.textContent)).toEqual(['eth0', '00:00:5e:00:53:a1', 'Up', '1 Gb/s']);
  });

  it('puts spec interfaces beside observed NICs, matched by MAC address and never by name', () => {
    const { container } = show(MISMATCHED);
    // eth0 and eth1 are components, so their MACs stay in the component table; eth2 is not.
    expect(within(table('Interfaces not in the component list')).getAllByRole('row').slice(1).map(row => row.textContent)).toEqual(['eth200:00:5e:00:53:23']);
    expect(table('Host NICs').closest('.inventory-discovery-pair')).toBe(table('Interfaces not in the component list').closest('.inventory-discovery-pair'));
    // An observed MAC the spec records names that interface; EthernetInterface1 reports another MAC and names none.
    expect(cells('Host NICs', 'EthernetInterface0')[1]).toHaveTextContent(/^00:00:5e:00:53:21 · eth0$/);
    expect(cells('Host NICs', 'EthernetInterface2')[1]).toHaveTextContent(/^00:00:5e:00:53:23 · eth2$/);
    expect(cells('Host NICs', 'EthernetInterface1')[1]).toHaveTextContent(/^00:00:5e:00:53:29$/);
    // The spec MAC no NIC reports is amber in the component table, where it is listed.
    const components = within(container).getByRole('table', { name: /Components/ });
    const unseen = within(components).getByText('00:00:5e:00:53:22');
    expect(unseen).toHaveAttribute('data-tone', 'warning');
    expect(unseen).toHaveAttribute('title', 'No host NIC reports this MAC address');
    expect(within(components).getByText('00:00:5e:00:53:21')).not.toHaveAttribute('data-tone');
  });

  it('shows only the observed NICs when spec records no interfaces', () => {
    show(PENDING);
    expect(within(hardware()).queryByRole('table', { name: 'Interfaces not in the component list' })).toBeNull();
    expect(table('Host NICs').closest('.inventory-discovery-pair')).toBeNull();
  });
});

describe('Device page before discovery and when Devices are unreadable', () => {
  it('adds only a muted "Not discovered" when status is empty', () => {
    const { container } = show('example-srv-a02-02');
    expect(fact('Discovery')).toHaveTextContent(/^Not discovered$/);
    expect(fact('Discovery').firstElementChild).toHaveClass('inventory-muted');
    expect(within(hardware()).queryByRole('list', { name: 'Discovery conditions' })).toBeNull();
    expect(within(hardware()).queryByRole('table', { name: /Identity|Host NICs|BMC/ })).toBeNull();
    expect(header(container).querySelector('.inventory-discovery-chip')).toBeNull();
  });

  it.each([
    ['answers 403', { ...fixtureInventory(), issues: { Device: { status: 403 } } }],
    ['is redacted after a later denial', redactInventory(fixtureInventory(), { Device: { status: 403 } })],
  ])('shows no status when the Device collection %s', (_, inventory) => {
    const { container } = show(MATCHED, inventory as Inventory);
    expect(container.textContent).not.toMatch(/3f3f3f3f|SYNTH-SKU-1|EthernetInterface|BMC ✓|Key approval/);
    expect(container.querySelector('.inventory-discovery-chip')).toBeNull();
  });
});

describe('Device page: an observation from another endpoint', () => {
  it('marks the retained key and observed identity amber, with no red disagreement', () => {
    const inventory = structuredClone(fixtureInventory());
    const device = [...inventory.slots.flatMap(slot => slot.devices), ...inventory.unboundDevices].find(item => item.metadata.name === MISMATCHED)!;
    device.spec.bmc = { endpoint: 'https://192.0.2.250' };
    device.status!.conditions = device.status!.conditions!.map(condition => condition.type === 'IdentityMatched' ? { ...condition, status: 'Unknown', reason: 'StaleObservation', message: 'the retained inventory was read from another endpoint' } : condition);
    show(MISMATCHED, inventory);
    const serial = cells('Identity', 'Serial')[2].querySelector('[data-tone]')!;
    expect(serial).toHaveAttribute('data-tone', 'warning');
    expect(serial.getAttribute('title')).toMatch(/^Read from .*; spec.bmc.endpoint is https:\/\/192.0.2.250$/);
    expect(within(table('Identity')).queryAllByText((_, element) => element?.getAttribute('data-tone') === 'danger')).toHaveLength(0);
    expect(fact('TLS key').querySelector('[data-tone="warning"] code')).not.toBeNull();
  });
});
