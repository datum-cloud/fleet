import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import { modelLabel, type Inventory } from '../inventory';

export function deviceCatalog(device: Device, inventory: Inventory): { model: PhysicalRecord; manufacturer: PhysicalRecord } | undefined {
  if (!device.metadata.namespace || !device.spec.deviceTypeRef || inventory.issues?.DeviceType || inventory.issues?.Manufacturer) return;
  const model = inventory.physical.find(record => record.kind === 'DeviceType' && record.metadata.namespace === device.metadata.namespace && record.metadata.name === device.spec.deviceTypeRef?.name && !record.metadata.deletionTimestamp);
  const manufacturer = model && inventory.physical.find(record => record.kind === 'Manufacturer' && record.metadata.namespace === device.metadata.namespace && record.metadata.name === model.spec.manufacturerRef?.name && !record.metadata.deletionTimestamp);
  if (model && manufacturer) return { model, manufacturer };
}

export function deviceModelLabel(device: Device, inventory: Inventory): string {
  const catalog = deviceCatalog(device, inventory);
  return catalog ? `${catalog.manufacturer.spec.canonicalName} ${catalog.model.spec.displayName}` : modelLabel(device.spec.identity);
}
