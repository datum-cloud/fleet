import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { MemoryRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { PhysicalInventory } from './physical-inventory';
import { toInventory } from '../inventory';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';

const inventory = toInventory(fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords);

function browse(query = '', data = inventory) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={[`/inventory${query}`]}><PhysicalInventory inventory={data} /></MemoryRouter></QueryClientProvider>);
}

describe('hardware directory', () => {
  it('uses Datum inputs, underline route tabs and table slots without nesting interactive controls', () => {
    browse();
    expect(screen.getByRole('searchbox', { name: 'Search devices' })).toHaveAttribute('data-slot', 'input');
    const navigation = screen.getByRole('navigation', { name: 'Inventory categories' });
    const devices = within(navigation).getByRole('tab', { name: /^Devices/ });
    expect(devices.closest('[role="tablist"]')).toHaveAttribute('data-variant', 'line');
    expect(devices.querySelector('[data-slot="tabs-indicator"]')).not.toBeNull();
    expect(devices).toHaveAttribute('aria-current', 'page');
    expect(navigation.querySelector('button')).toBeNull();
    const table = screen.getByRole('table');
    expect(table).toHaveAttribute('data-slot', 'table');
    expect(within(table).getAllByRole('columnheader')[0]).toHaveAttribute('data-slot', 'table-head');
    fireEvent.click(within(navigation).getByRole('tab', { name: 'Locations' }));
    fireEvent.click(within(navigation).getByRole('tab', { name: 'Facilities' }));
    expect(screen.getByRole('searchbox', { name: 'Search facilities' })).toHaveValue('');
  });

  it('uses the Datum card access state while keeping retry and region semantics', () => {
    browse('', { ...inventory, issues: { Device: { status: 403 }, Site: { status: 403 }, Facility: { status: 403 }, Rack: { status: 403 }, DeviceClaim: { status: 403 }, Cable: { status: 403 }, PowerFeed: { status: 403 }, Manufacturer: { status: 403 }, DeviceType: { status: 403 } } });
    expect(screen.getByRole('region', { name: 'Inventory access needed' })).toHaveAttribute('data-slot', 'card');
    expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument();
  });

  it.each(inventory.physical)('renders $kind details without creation metadata', record => {
    const data = structuredClone(inventory);
    for (const item of data.physical) delete item.metadata.creationTimestamp;
    browse(`?kind=${record.kind}&record=${record.metadata.name}&namespace=${record.metadata.namespace}${record.kind === 'Rack' ? '&tab=history' : ''}`, data);
    expect(screen.getByText('About this record').closest('details')).not.toHaveAttribute('open');
    if (record.kind === 'Rack') expect(screen.getByText('Record history & verification')).toBeInTheDocument();
    else expect(screen.getByRole('tab', { name: 'History & verification' })).toBeInTheDocument();
    expect(screen.queryByText(/Unexpected Application Error/)).not.toBeInTheDocument();
  });
  it('does not turn no-read permission into empty inventory or zero counts', () => {
    browse('', { ...inventory, issues: { Manufacturer: { status: 403 }, DeviceType: { status: 403 }, Site: { status: 403 }, Device: { status: 403 }, DeviceClaim: { status: 403 }, Facility: { status: 403 }, Rack: { status: 403 }, Cable: { status: 403 }, PowerFeed: { status: 403 } } });
    expect(screen.getByRole('heading', { name: 'Inventory access needed' })).toBeInTheDocument();
    expect(screen.queryByText('No devices recorded')).not.toBeInTheDocument();
    expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Devices 0/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /retry|try again/i })).not.toBeInTheDocument();
  });

  it('preserves permitted facilities and handles a denied device deep link without echoing identity', () => {
    browse('?kind=Device&record=private-device&namespace=private', { ...inventory, issues: { Device: { status: 403 } } });
    expect(screen.getByText('Inventory access needed')).toBeInTheDocument();
    expect(screen.queryByText(/private-device/)).not.toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Devices' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: 'Locations' }));
    fireEvent.click(screen.getByRole('tab', { name: 'Facilities' }));
    expect(screen.getByRole('link', { name: 'Example facility' })).toBeInTheDocument();
  });

  it('removes previously visible identities when a permission result changes', () => {
    const client = new QueryClient();
    const view = (data: typeof inventory) => <QueryClientProvider client={client}><MemoryRouter initialEntries={['/inventory?kind=Device&record=rack-01-u12-synthetic-0001&namespace=default']}><PhysicalInventory inventory={data} /></MemoryRouter></QueryClientProvider>;
    const result = render(view(inventory));
    expect(screen.getByText('SYNTHETIC-0001')).toBeInTheDocument();
    result.rerender(view({ ...inventory, issues: { Device: { status: 403 } } }));
    expect(screen.queryByText('SYNTHETIC-0001')).not.toBeInTheDocument();
    expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
    expect(screen.getByText('Inventory access needed')).toBeInTheDocument();
  });

  it.each([[404, 'Inventory section unavailable'], [503, 'Inventory is temporarily unavailable']])('distinguishes collection failure %s', (status, title) => {
    browse('?kind=Device', { ...inventory, issues: { Device: { status: Number(status) } } });
    expect(screen.getByRole('heading', { name: String(title) })).toBeInTheDocument();
    expect(screen.queryByText('No devices recorded')).not.toBeInTheDocument();
  });

  it('offers physical kinds and operational Sites without writes or a report upload', () => {
    const { container } = browse();
    const navigation = within(screen.getByRole('navigation', { name: 'Inventory categories' }));
    for (const name of ['Equipment', 'Locations', 'Installation plans', 'Hardware catalog', 'Devices', 'Cables', 'Power feeds']) expect(navigation.getByRole('tab', { name })).toBeInTheDocument();
    expect(navigation.queryByRole('tab', { name: 'Facilities' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /create/i })).toBeDisabled();
    expect(screen.queryByRole('button', { name: /edit|delete|apply(?! search)|sync|import/i })).not.toBeInTheDocument();
    expect(container.querySelector('input[type="file"]')).toBeNull();
    expect(screen.queryByText(/GitOps|Flux/)).not.toBeInTheDocument();
  });

  it('searches identity and resets the filter when navigating categories', () => {
    browse();
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'EXAMPLE-001' } });
    fireEvent.click(screen.getByRole('button', { name: 'Apply search' }));
    expect(screen.getByRole('link', { name: 'Example compute server' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'orphan-synthetic-0009' })).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'no-such-serial' } });
    fireEvent.click(screen.getByRole('button', { name: 'Apply search' }));
    expect(screen.getByText('No matching records')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Clear search' }));
    fireEvent.click(screen.getByRole('tab', { name: 'Locations' }));
    fireEvent.click(screen.getByRole('tab', { name: 'Facilities' }));
    expect(screen.getByRole('searchbox')).toHaveValue('');
    expect(screen.getByRole('link', { name: 'Example facility' })).toBeInTheDocument();
  });

  it('drills from facility through rack and claim to hardware without claiming free units', () => {
    browse('?kind=Facility&record=example-facility&namespace=default');
    fireEvent.click(screen.getByRole('link', { name: 'Rack 01' }));
    expect(screen.getByText(/recorded placement is not verified occupancy/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: /^Equipment · / }));
    fireEvent.click(screen.getByRole('link', { name: 'Server installation · U12' }));
    fireEvent.click(screen.getByRole('link', { name: 'Example compute server' }));
    expect(screen.getByText('SYNTHETIC-0001')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Table' }));
    const rows = within(screen.getByRole('table', { name: 'Components and Cables' })).getAllByRole('row').slice(1);
    expect(rows.map(row => within(row).getAllByRole('cell')[1].textContent)).toEqual(['Power inletIEC C14', 'Network portSFP28']);
    expect(within(rows[0]).getByRole('link', { name: 'Server power cable' })).toBeInTheDocument();
  });

  it('follows typed cable endpoints to the supply and retains power uncertainty', () => {
    browse('?kind=Cable&record=power-01&namespace=default');
    expect(screen.getByText('psu-1')).toBeInTheDocument();
    expect(screen.getByText(/Cable records, not live link state/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: 'Feed A' }));
    expect(screen.getByRole('heading', { name: /^Power outlets?\b/ })).toBeInTheDocument();
    expect(screen.getByText(/supply ratings do not establish available power/)).toBeInTheDocument();
    expect(screen.getByText(/unknown, not free/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: /^Upstream sources/ }));
    expect(screen.getByRole('region', { name: 'Upstream sources' })).toHaveTextContent('example-provider/Supply/synthetic-supply-a');
  });

  it('retains replacements without choosing an active registration', () => {
    const original = fixturePhysicalDevices[0];
    const replacement = { ...original, metadata: { ...original.metadata, name: 'replacement' }, spec: { ...original.spec, displayName: 'Replacement server' } };
    browse('?kind=DeviceClaim&record=rack-01-u12&namespace=default', toInventory(fixturePhysicalClaims, [original, replacement]));
    expect(screen.getByText(/the active device and its placement are unknown/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Example compute server' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Replacement server' })).toBeInTheDocument();
  });

  it('does not resolve relationships across namespaces or infer typed refs from legacy names', () => {
    const foreignRack = { ...fixturePhysicalRecords[1], metadata: { name: 'rack-01', namespace: 'other' } };
    browse('?kind=DeviceClaim&record=rack-01-u12&namespace=default', toInventory(fixturePhysicalClaims, [], [foreignRack]));
    expect(screen.getByText('(record not found)')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Rack 01' })).not.toBeInTheDocument();
  });

  it('handles category emptiness and deleted deep links explicitly', () => {
    const result = browse('?kind=Rack', toInventory([], []));
    expect(screen.getByText('No racks recorded')).toBeInTheDocument();
    result.unmount();
    browse('?kind=Device&record=missing&namespace=default');
    expect(screen.getByRole('heading', { name: 'Record not found' })).toBeInTheDocument();
  });

  it('excludes same-named assets in other namespaces from the directory', () => {
    const original = fixturePhysicalDevices[0];
    const other = { ...original, metadata: { ...original.metadata, namespace: 'other' }, spec: { ...original.spec, displayName: 'Other namespace server', identity: { ...original.spec.identity, serial: 'OTHER-SYNTHETIC-001' } } };
    browse('?kind=Device', toInventory(fixturePhysicalClaims, [original, other]));
    expect(screen.queryByRole('link', { name: 'Other namespace server' })).not.toBeInTheDocument();
    expect(screen.queryByText('OTHER-SYNTHETIC-001')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: 'Example compute server' }));
    expect(screen.getByText('SYNTHETIC-0001')).toBeInTheDocument();
  });

  it('shows the explicit no-hardware-identity marker instead of a missing serial', () => {
    const original = fixturePhysicalDevices[0];
    const panel = { ...original, metadata: { ...original.metadata, name: 'synthetic-patch-panel' }, spec: { ...original.spec, displayName: 'Synthetic patch panel', bmc: undefined, identity: { model: 'Example 24-port panel', unidentified: true, unidentifiedReason: 'passive panel; no serial on the chassis' } } };
    browse('?kind=Device', toInventory(fixturePhysicalClaims, [panel]));
    expect(screen.getByText('No hardware identity')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: 'Synthetic patch panel' }));
    expect(screen.getByText(/passive panel; no serial on the chassis · cannot back machine capacity/)).toBeInTheDocument();
  });

  it('does not fabricate a rack relationship from legacy location text', () => {
    const legacy = { ...fixturePhysicalClaims[0], spec: { ...fixturePhysicalClaims[0].spec, placement: { facility: 'example-facility', rack: 'rack-01', position: 12 } } };
    browse('?kind=Rack&record=rack-01&namespace=default', toInventory([legacy], [], [fixturePhysicalRecords[1]]));
    expect(screen.queryByRole('link', { name: /Server installation/ })).not.toBeInTheDocument();
    expect(screen.getByText(/No related records found/)).toBeInTheDocument();
  });
});
