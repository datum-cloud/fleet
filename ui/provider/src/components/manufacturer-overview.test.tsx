import { fireEvent, render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, useNavigate } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import { toInventory, type Inventory } from '../inventory';
import { fixturePhysicalDevices, fixturePhysicalClaims } from '../lib/fixtures';
import { manufacturerInventory } from '../lib/manufacturer-inventory';
import { PhysicalInventory } from './physical-inventory';
import { InventoryHardwareFields } from './inventory-hardware-fields';

afterEach(() => vi.unstubAllGlobals());

function fixture() {
  const manufacturer: PhysicalRecord = { kind: 'Manufacturer', metadata: { name: 'vendor-key', namespace: 'default', uid: 'vendor-uid', resourceVersion: '1' }, spec: { displayName: 'Acme hardware', canonicalName: 'acme', aliases: ['Old Acme'] } };
  const model: PhysicalRecord = { kind: 'DeviceType', metadata: { name: 'model-key', namespace: 'default', uid: 'model-uid', resourceVersion: '1' }, spec: { displayName: 'Compute model', manufacturerRef: { name: 'vendor-key' }, model: 'compute', modelShape: { heightUnits: 8 } } };
  const facility: PhysicalRecord = { kind: 'Facility', metadata: { name: 'premises-key', namespace: 'default' }, spec: { displayName: 'Reported premises' } };
  const rack: PhysicalRecord = { kind: 'Rack', metadata: { name: 'rack-key', namespace: 'default' }, spec: { displayName: 'Reported rack' } };
  const device = structuredClone(fixturePhysicalDevices[0]);
  device.metadata = { ...device.metadata, namespace: 'default', name: 'device-key' };
  device.spec.displayName = 'Physical server';
  device.spec.deviceTypeRef = { name: 'model-key' };
  device.spec.reportedPlacement = { revision: 1, observedAt: '2026-11-22T00:00:00Z', reason: 'Recorded location', reference: 'synthetic-ticket', facilityRef: { name: 'premises-key' }, rackRef: { name: 'rack-key' }, position: 4, face: 'front' };
  const inventory = toInventory([], [device], [manufacturer, model, facility, rack]);
  return { manufacturer, model, device, inventory };
}

function History() {
  const navigate = useNavigate();
  return <><button onClick={() => navigate(-1)}>History back</button><button onClick={() => navigate(1)}>History forward</button></>;
}

function show(inventory: Inventory, query = '?kind=Manufacturer&namespace=default&record=vendor-key&q=Acme') {
  const client = new QueryClient();
  const view = (data: Inventory) => <QueryClientProvider client={client}><MemoryRouter initialEntries={[`/inventory${query}`]}><History /><PhysicalInventory inventory={data} /></MemoryRouter></QueryClientProvider>;
  const result = render(view(inventory));
  return { ...result, update: (data: Inventory) => result.rerender(view(data)) };
}

describe('manufacturer detail', () => {
  it('shows linked models, deduplicated device counts and real reported-location links', () => {
    const { inventory, manufacturer, model, device } = fixture();
    inventory.physical.push(model);
    inventory.unboundDevices.push(device);
    inventory.slots.push({ key: 'default/plan', claim: structuredClone(fixturePhysicalClaims[0]), devices: [device] });
    expect(manufacturerInventory(manufacturer, inventory).devices).toHaveLength(1);
    show(inventory);
    const models = within(screen.getByRole('region', { name: 'Manufacturer device types table' }));
    expect(models.getAllByRole('row')).toHaveLength(2);
    expect(models.getByRole('cell', { name: '1' })).toBeInTheDocument();
    const devices = within(screen.getByRole('region', { name: 'Manufacturer devices table' }));
    expect(devices.getAllByRole('row')).toHaveLength(2);
    for (const [label, kind, name] of [['Physical server', 'Device', 'device-key'], ['Compute model', 'DeviceType', 'model-key'], ['Reported premises', 'Facility', 'premises-key'], ['Reported rack', 'Rack', 'rack-key']]) {
      const params = new URL(devices.getByRole('link', { name: label }).getAttribute('href')!, 'https://inventory.test').searchParams;
      expect(params.get('kind')).toBe(kind);
      expect(params.get('record')).toBe(name);
      expect(params.get('namespace')).toBe('default');
    }
    expect(devices.getByText(/U4/)).toBeInTheDocument();
    expect(screen.queryByText(/Manufacturers \/ vendor-key/)).not.toBeInTheDocument();
    expect(screen.getByText('vendor-key').closest('details')).toHaveTextContent('About this record');
  });

  it('uses only same-namespace references, not text, templates or dangling targets', () => {
    const { inventory, manufacturer, model, device } = fixture();
    inventory.physical.push({ ...model, metadata: { ...model.metadata, name: 'other-model' }, spec: { ...model.spec, manufacturerRef: { name: 'other-vendor' } } });
    inventory.unboundDevices = [
      { ...device, spec: { ...device.spec, deviceTypeRef: undefined, identity: { ...device.spec.identity, manufacturer: 'acme' } } },
      { ...device, metadata: { ...device.metadata, name: 'unrelated' }, spec: { ...device.spec, deviceTypeRef: { name: 'other-model' } } },
      { ...device, metadata: { ...device.metadata, name: 'dangling' }, spec: { ...device.spec, deviceTypeRef: { name: 'missing-secret-model' } } },
      { ...device, metadata: { ...device.metadata, namespace: 'other' } },
    ];
    expect(manufacturerInventory(manufacturer, inventory).devices).toEqual([]);
    show(inventory);
    expect(screen.getByText(/Some device type references are missing/)).toBeInTheDocument();
    expect(screen.getByText('No resolved device relationships found.')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Physical server' })).not.toBeInTheDocument();
    expect(screen.queryByText('missing-secret-model')).not.toBeInTheDocument();
  });

  it.each(['DeviceType', 'Device'] as const)('keeps %s failures unknown instead of zero and hides stale rows', kind => {
    const { inventory } = fixture();
    const result = show(inventory);
    result.update({ ...inventory, issues: { [kind]: { status: 403 } } });
    expect(screen.getByText(/Access denied/)).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Physical server' })).not.toBeInTheDocument();
    expect(screen.queryByRole('cell', { name: '0' })).not.toBeInTheDocument();
    if (kind === 'DeviceType') expect(screen.queryByRole('link', { name: 'Compute model' })).not.toBeInTheDocument();
    else expect(screen.getByRole('cell', { name: 'Unknown' })).toBeInTheDocument();
    result.update({ ...inventory, issues: { [kind]: { status: 502 } } });
    expect(screen.getByText(/Records unavailable/)).toBeInTheDocument();
    expect(screen.queryByText(/No devices explicitly/)).not.toBeInTheDocument();
  });

  it('redacts a denied manufacturer and its dependent catalog entirely', () => {
    const { inventory } = fixture();
    const result = show(inventory);
    result.update({ ...inventory, issues: { Manufacturer: { status: 403 } } });
    expect(screen.getByText('Inventory access needed')).toBeInTheDocument();
    expect(result.container.textContent).not.toMatch(/Acme|Compute model|Physical server|vendor-key/);
  });

  it('retains linked devices when locations are denied, unavailable or missing', () => {
    const { inventory } = fixture();
    inventory.issues = { Facility: { status: 403 }, Rack: { status: 502 } };
    const result = show(inventory);
    expect(screen.getByText('Facility access denied')).toBeInTheDocument();
    expect(screen.getByText('Rack unavailable')).toBeInTheDocument();
    expect(result.container.textContent).not.toMatch(/Reported premises|Reported rack|premises-key|rack-key/);
    expect(screen.getByRole('link', { name: 'Physical server' })).toBeInTheDocument();
    result.update({ ...inventory, issues: {}, physical: inventory.physical.filter(record => record.kind !== 'Facility' && record.kind !== 'Rack') });
    expect(screen.getByText('Facility reference missing')).toBeInTheDocument();
    expect(screen.getByText('Rack reference missing')).toBeInTheDocument();
  });

  it('never substitutes claim placement or nominal model geometry', () => {
    const { inventory, device } = fixture();
    delete device.spec.reportedPlacement;
    inventory.slots.push({ key: `default/${device.spec.claimRef.name}`, claim: structuredClone(fixturePhysicalClaims[0]), devices: [device] });
    show(inventory);
    const devices = within(screen.getByRole('region', { name: 'Manufacturer devices table' }));
    expect(devices.getByRole('cell', { name: 'Not recorded' })).toBeInTheDocument();
    expect(devices.queryByText(/8U|Claim-derived/)).not.toBeInTheDocument();
  });

  it('distinguishes empty records from unresolved and deleting references', () => {
    const { inventory, manufacturer, model } = fixture();
    inventory.unboundDevices = [];
    const result = show(inventory);
    expect(screen.getByRole('cell', { name: '0' })).toBeInTheDocument();
    expect(screen.getByText('No devices explicitly linked to these device types.')).toBeInTheDocument();
    result.update({ ...inventory, physical: [manufacturer] });
    expect(screen.getByText('No device types linked to this manufacturer.')).toBeInTheDocument();
    const fresh = fixture();
    fresh.model.metadata.deletionTimestamp = '2026-11-22T00:00:00Z';
    expect(manufacturerInventory(fresh.manufacturer, fresh.inventory).unresolved).toBe(true);
    model.metadata.namespace = 'elsewhere';
    expect(manufacturerInventory(manufacturer, inventory).models).toHaveLength(0);
  });

  it('uses display labels, fallback names and contextual duplicate identifiers', () => {
    const { inventory, model, device } = fixture();
    inventory.physical.push({ ...model, metadata: { ...model.metadata, name: 'second-model' } });
    inventory.unboundDevices.push({ ...device, metadata: { ...device.metadata, name: 'second-device' } });
    show(inventory);
    expect(screen.getByRole('link', { name: 'Physical server (device-key)' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Physical server (second-device)' })).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: 'Compute model (model-key)' }).length).toBeGreaterThan(0);
    expect(screen.getByRole('link', { name: 'Compute model (second-model)' })).toBeInTheDocument();
  });

  it('uses fallback labels in the overview and picker without canonical-name substitution', () => {
    const { inventory, manufacturer, model } = fixture();
    manufacturer.spec.displayName = '';
    model.spec.displayName = '';
    show(inventory);
    expect(screen.getByRole('heading', { name: 'vendor-key' })).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: 'model-key' })).toHaveLength(2);
    render(<InventoryHardwareFields kind="Device" values={{ name: '', namespace: 'default', displayName: '', height: '', facility: '' }} onChange={() => {}} namespace="default" claims={[]} devices={[]} feeds={[]} locations={[]} catalog={[manufacturer, model]} />);
    expect(screen.getByRole('option', { name: 'vendor-key · model-key' })).toBeInTheDocument();
  });

  it('preserves filters and history and adds no network reads or visible-focus effects', () => {
    const { inventory } = fixture();
    const fetch = vi.fn();
    vi.stubGlobal('fetch', fetch);
    show(inventory);
    fireEvent.click(within(screen.getByRole('navigation', { name: 'breadcrumb' })).getByRole('link', { name: 'Manufacturers' }));
    expect(screen.getByRole('searchbox')).toHaveValue('Acme');
    expect(screen.queryByText('vendor-key')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: 'Acme hardware' }));
    fireEvent.click(screen.getByRole('link', { name: 'Physical server' }));
    fireEvent.click(screen.getByRole('button', { name: 'History back' }));
    expect(screen.getByRole('region', { name: 'Manufacturer devices table' })).toBeInTheDocument();
    fireEvent.blur(window);
    fireEvent.focus(window);
    fireEvent.click(screen.getByRole('button', { name: 'History forward' }));
    expect(screen.getByRole('heading', { name: 'Physical server' })).toBeInTheDocument();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('disambiguates picker choices only when display labels collide', () => {
    const { manufacturer, model } = fixture();
    const secondManufacturer = { ...manufacturer, metadata: { ...manufacturer.metadata, name: 'other-vendor', uid: 'other-vendor-uid' } };
    const secondModel = { ...model, metadata: { ...model.metadata, name: 'other-model', uid: 'other-model-uid' } };
    const otherVendorModel = { ...model, metadata: { ...model.metadata, name: 'third-model', uid: 'third-model-uid' }, spec: { ...model.spec, manufacturerRef: { name: 'other-vendor' } } };
    render(<InventoryHardwareFields kind="Device" values={{ name: '', namespace: 'default', displayName: '', height: '', facility: '' }} onChange={() => {}} namespace="default" claims={[]} devices={[]} feeds={[]} locations={[]} catalog={[manufacturer, secondManufacturer, model, secondModel, otherVendorModel]} />);
    expect(screen.getByRole('option', { name: 'Acme hardware (vendor-key) · Compute model (model-key)' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Acme hardware (vendor-key) · Compute model (other-model)' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Acme hardware (other-vendor) · Compute model' })).toBeInTheDocument();
  });

  it('retains authorized devices when claims are denied and rejects foreign deep links', () => {
    const { inventory, device } = fixture();
    inventory.unboundDevices = [];
    inventory.slots.push({ key: 'default/plan', claim: structuredClone(fixturePhysicalClaims[0]), devices: [device] });
    inventory.issues = { DeviceClaim: { status: 403 } };
    const result = show(inventory);
    expect(screen.getByRole('link', { name: 'Physical server' })).toBeInTheDocument();
    result.unmount();
    show(inventory, '?kind=Manufacturer&namespace=elsewhere&record=vendor-key');
    expect(screen.getByText(/only supports the default namespace/)).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Manufacturer devices table' })).not.toBeInTheDocument();
  });
});
