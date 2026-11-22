import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import { modelLabel, type Inventory } from '../inventory';
import { catalogLabel } from './manufacturer-inventory';

export function deviceCatalog(device: Device, inventory: Inventory): { model: PhysicalRecord; manufacturer: PhysicalRecord } | undefined {
  if (!device.metadata.namespace || !device.spec.deviceTypeRef || inventory.issues?.DeviceType || inventory.issues?.Manufacturer) return;
  const model = inventory.physical.find(record => record.kind === 'DeviceType' && record.metadata.namespace === device.metadata.namespace && record.metadata.name === device.spec.deviceTypeRef?.name && !record.metadata.deletionTimestamp);
  const manufacturer = model && inventory.physical.find(record => record.kind === 'Manufacturer' && record.metadata.namespace === device.metadata.namespace && record.metadata.name === model.spec.manufacturerRef?.name && !record.metadata.deletionTimestamp);
  if (model && manufacturer) return { model, manufacturer };
}

export function deviceModelLabel(device: Device, inventory: Inventory): string {
  const catalog = deviceCatalog(device, inventory);
  if (!catalog) return modelLabel(device.spec.identity);
  const peers = inventory.physical.filter(record => record.metadata.namespace === device.metadata.namespace && !record.metadata.deletionTimestamp);
  return `${catalogLabel(catalog.manufacturer, peers.filter(record => record.kind === 'Manufacturer'))} ${catalogLabel(catalog.model, peers.filter(record => record.kind === 'DeviceType' && record.spec.manufacturerRef?.name === catalog.manufacturer.metadata.name))}`;
}
