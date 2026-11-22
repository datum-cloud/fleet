import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import { PhysicalInventory } from './physical-inventory';
import { toInventory, type Inventory } from '../inventory';
import { deviceCatalog, deviceModelLabel } from '../lib/hardware-catalog';
import { fixturePhysicalDevices } from '../lib/fixtures';
import { InventoryHardwareFields } from './inventory-hardware-fields';
import type { InventoryFormValues } from '../lib/inventory-management';

function fixture() {
  const device = structuredClone(fixturePhysicalDevices[0]);
  device.spec.deviceTypeRef = { name: 'private-model-key' };
  const manufacturer: PhysicalRecord = { kind: 'Manufacturer', metadata: { name: 'private-vendor-key', namespace: device.metadata.namespace }, spec: { displayName: 'Private vendor label', canonicalName: 'Private canonical vendor', aliases: ['Private old vendor'], provenance: device.spec.provenance } };
  const model: PhysicalRecord = { kind: 'DeviceType', metadata: { name: 'private-model-key', namespace: device.metadata.namespace }, spec: { displayName: 'Private model label', model: 'private-normalized-model', manufacturerRef: { name: manufacturer.metadata.name }, modelShape: { heightUnits: 8 }, componentTemplates: [{ id: 'template-only', name: 'Template-only port', type: 'NetworkPort' }], provenance: device.spec.provenance } };
  return { device, manufacturer, model, inventory: toInventory([], [device], [manufacturer, model]) };
}

function show(inventory: Inventory, query = '?kind=Device') {
  return render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={[`/${query}`]}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
}

describe('hardware catalog readers', () => {
  it('selects only readable same-namespace records without populating observed hardware', () => {
    const { manufacturer, model } = fixture();
    manufacturer.metadata = { ...manufacturer.metadata, uid: 'vendor-uid', resourceVersion: '1' };
    model.metadata = { ...model.metadata, uid: 'model-uid', resourceVersion: '2' };
    const other = { ...model, metadata: { ...model.metadata, uid: 'other-uid', namespace: 'another' }, spec: { ...model.spec, displayName: 'Other namespace model' } };
    const values: InventoryFormValues = { name: 'new-device', namespace: 'default', displayName: 'New hardware', recordedBy: 'synthetic', reference: '', height: '', facility: '', manufacturer: 'Observed vendor', model: 'Observed model', components: [] };
    const change = vi.fn();
    const form = (current: InventoryFormValues, catalog: PhysicalRecord[]) => <InventoryHardwareFields kind="Device" values={current} namespace="default" claims={[]} devices={[]} feeds={[]} locations={[]} catalog={catalog} onChange={change} />;
    const result = render(form(values, [manufacturer, model, other]));
    expect(screen.queryByRole('option', { name: /Other namespace/ })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Shared catalog model (optional)'), { target: { value: 'model-uid' } });
    const selected = change.mock.calls[0][0];
    expect(selected.catalog).toEqual({ namespace: 'default', name: model.metadata.name, uid: 'model-uid', resourceVersion: '2', manufacturer: { name: manufacturer.metadata.name, uid: 'vendor-uid', resourceVersion: '1' } });
    expect(selected.model).toBe(values.model);
    expect(selected.manufacturer).toBe(values.manufacturer);
    expect(selected.components).toEqual([]);
    result.rerender(form(selected, []));
    expect(screen.getByRole('option', { name: /Selected catalog unavailable/ })).toBeInTheDocument();
    expect(result.container.textContent).not.toMatch(/Private|private-model|private-vendor/);
  });

  it('prefers available same-namespace catalog without mutating source observations or instances', () => {
    const { device, model, inventory } = fixture();
    const before = structuredClone(device);
    expect(deviceModelLabel(device, inventory)).toBe('Private canonical vendor Private model label');
    model.spec.displayName = 'Changed model label';
    model.spec.componentTemplates!.push({ id: 'new-port', name: 'New reference port', type: 'NetworkPort' });
    expect(deviceModelLabel(device, inventory)).toContain('Changed model label');
    expect(device).toEqual(before);
    expect(device.spec.components).not.toContainEqual(expect.objectContaining({ id: 'new-port' }));
  });

  it('keeps legacy source fallback through missing, cross-namespace and recreated references', () => {
    const { device, manufacturer, model, inventory } = fixture();
    inventory.physical = [];
    expect(deviceModelLabel(device, inventory)).toContain(device.spec.identity.model);
    inventory.physical = [manufacturer, { ...model, metadata: { ...model.metadata, namespace: 'another' } }];
    expect(deviceCatalog(device, inventory)).toBeUndefined();
    inventory.physical = [manufacturer, { ...model, metadata: { ...model.metadata, uid: 'recreated' } }];
    expect(deviceCatalog(device, inventory)?.model.metadata.uid).toBe('recreated');
    inventory.physical[1].metadata.deletionTimestamp = '2026-11-21T00:00:00Z';
    expect(deviceCatalog(device, inventory)).toBeUndefined();
  });

  it.each(['Manufacturer', 'DeviceType'] as const)('redacts denied %s references from search, filters and details', kind => {
    const { device, inventory } = fixture();
    inventory.issues = { [kind]: { status: 403 } };
    const result = show(inventory);
    expect(result.container.textContent).not.toMatch(/Private|private-model|private-vendor|private-normalized/);
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'Private model label' } });
    expect(screen.queryByRole('link', { name: device.spec.displayName })).not.toBeInTheDocument();
  });

  it('hides stale catalog labels immediately on permission change', () => {
    const { device, inventory } = fixture();
    const client = new QueryClient();
    const view = (data: Inventory) => <QueryClientProvider client={client}><MemoryRouter initialEntries={[`/?kind=Device&record=${device.metadata.name}&namespace=${device.metadata.namespace}`]}><PhysicalInventory inventory={data} /></MemoryRouter></QueryClientProvider>;
    const result = render(view(inventory));
    expect(result.container.textContent).toContain('Private model label');
    result.rerender(view({ ...inventory, issues: { Manufacturer: { status: 403 } } }));
    expect(result.container.textContent).not.toMatch(/Private|private-model|private-vendor/);
    expect(screen.getByText('Catalog unavailable · showing source observation')).toBeInTheDocument();
  });

  it('browses aliases and nominal templates without inventing installed components', () => {
    const { inventory, model } = fixture();
    show(inventory, '?kind=Manufacturer');
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'private old vendor' } });
    expect(screen.getByRole('link', { name: 'Private vendor label' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: /Device types/ }));
    fireEvent.click(screen.getByRole('link', { name: model.spec.displayName }));
    expect(screen.getByText(/Template-only port/)).toBeInTheDocument();
    expect(screen.getByText(/not installed|No installed assets/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /create|edit|delete|apply|select/i })).not.toBeInTheDocument();
  });

  it('never echoes a denied catalog deep link', () => {
    const { inventory } = fixture();
    inventory.issues = { Manufacturer: { status: 403 } };
    const result = show(inventory, '?kind=DeviceType&namespace=default&record=private-model-key');
    expect(screen.getByText('Inventory access needed')).toBeInTheDocument();
    expect(result.container.textContent).not.toMatch(/Private|private-model|private-vendor/);
  });
});
