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
  it.each(inventory.physical)('renders $kind details without creation metadata', record => {
    const data = structuredClone(inventory);
    for (const item of data.physical) delete item.metadata.creationTimestamp;
    browse(`?kind=${record.kind}&record=${record.metadata.name}&namespace=${record.metadata.namespace}`, data);
    expect(screen.getByText('Record history & verification')).toBeInTheDocument();
    expect(screen.queryByText(/Unexpected Application Error/)).not.toBeInTheDocument();
  });
  it('does not turn no-read permission into empty inventory or zero counts', () => {
    browse('', { ...inventory, issues: { Site: { status: 403 }, Device: { status: 403 }, DeviceClaim: { status: 403 }, Facility: { status: 403 }, Rack: { status: 403 }, Cable: { status: 403 }, PowerFeed: { status: 403 } } });
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
    expect(screen.queryByRole('link', { name: /Devices/ })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: /Facilities 1/ }));
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
    for (const name of ['Devices', 'Operational sites', 'Facilities', 'Racks', 'Claims & registrations', 'Cables', 'Power feeds']) expect(navigation.getByRole('link', { name: new RegExp(name) })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /create|edit|delete|apply|sync|import/i })).not.toBeInTheDocument();
    expect(container.querySelector('input[type="file"]')).toBeNull();
    expect(screen.queryByText(/GitOps|Flux/)).not.toBeInTheDocument();
  });

  it('searches identity and resets the filter when navigating categories', () => {
    browse();
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'EXAMPLE-001' } });
    expect(screen.getByRole('link', { name: 'Example compute server' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'orphan-synthetic-0009' })).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'no-such-serial' } });
    expect(screen.getByText('No matching records')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Clear search' }));
    fireEvent.click(screen.getByRole('link', { name: /Facilities 1/ }));
    expect(screen.getByRole('searchbox')).toHaveValue('');
    expect(screen.getByRole('link', { name: 'Example facility' })).toBeInTheDocument();
  });

  it('drills from facility through rack and claim to hardware without claiming free units', () => {
    browse('?kind=Facility&record=example-facility&namespace=default');
    fireEvent.click(screen.getByRole('link', { name: 'Rack 01' }));
    expect(screen.getByText(/recorded placement is not verified occupancy/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: 'Server installation · U12' }));
    fireEvent.click(screen.getByRole('link', { name: 'Example compute server' }));
    expect(screen.getByText('SYNTHETIC-0001')).toBeInTheDocument();
    expect(screen.getByText('NetworkPort')).toBeInTheDocument();
    expect(screen.getByText('PowerInlet')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Server power cable' })).toBeInTheDocument();
  });

  it('follows typed cable endpoints to the supply and retains power uncertainty', () => {
    browse('?kind=Cable&record=power-01&namespace=default');
    expect(screen.getByText('psu-1 · position 1')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: 'Feed A' }));
    expect(screen.getByText('PowerOutlet')).toBeInTheDocument();
    expect(screen.getByText(/supply ratings do not establish available power/)).toBeInTheDocument();
    expect(screen.getByText('example-provider/Supply/synthetic-supply-a')).toBeInTheDocument();
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

  it('keeps same-named assets in different namespaces independently navigable', () => {
    const original = fixturePhysicalDevices[0];
    const other = { ...original, metadata: { ...original.metadata, namespace: 'other' }, spec: { ...original.spec, displayName: 'Other namespace server', identity: { ...original.spec.identity, serial: 'OTHER-SYNTHETIC-001' } } };
    browse('?kind=Device', toInventory(fixturePhysicalClaims, [original, other]));
    fireEvent.click(screen.getByRole('link', { name: 'Other namespace server' }));
    expect(screen.getByText('OTHER-SYNTHETIC-001')).toBeInTheDocument();
    expect(screen.queryByText('SYNTHETIC-0001')).not.toBeInTheDocument();
    expect(screen.getByText('Claim record not found')).toBeInTheDocument();
  });

  it('does not fabricate a rack relationship from legacy location text', () => {
    const legacy = { ...fixturePhysicalClaims[0], spec: { ...fixturePhysicalClaims[0].spec, placement: { facility: 'example-facility', rack: 'rack-01', position: 12 } } };
    browse('?kind=Rack&record=rack-01&namespace=default', toInventory([legacy], [], [fixturePhysicalRecords[1]]));
    expect(screen.queryByRole('link', { name: /Server installation/ })).not.toBeInTheDocument();
    expect(screen.getByText(/No related records found/)).toBeInTheDocument();
  });
});
