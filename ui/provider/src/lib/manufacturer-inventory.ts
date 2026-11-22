import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import { slotKey, type Inventory } from '../inventory';
import { INVENTORY_NAMESPACE } from './inventory-namespace';

type NamedRecord = { metadata: { name: string; namespace?: string }; spec: { displayName?: string } };

export function catalogLabel(record: NamedRecord, peers: NamedRecord[] = []): string {
  const label = record.spec.displayName || record.metadata.name;
  const ambiguous = peers.some(peer => slotKey(peer.metadata.namespace, peer.metadata.name) !== slotKey(record.metadata.namespace, record.metadata.name) && (peer.spec.displayName || peer.metadata.name) === label);
  return ambiguous ? `${label} (${record.metadata.name})` : label;
}

function unique<Record extends NamedRecord>(records: Record[]): Record[] {
  return [...new Map(records.map(record => [slotKey(record.metadata.namespace, record.metadata.name), record])).values()];
}

export function manufacturerInventory(manufacturer: PhysicalRecord, inventory: Inventory) {
  const namespace = manufacturer.metadata.namespace;
  const readable = namespace === INVENTORY_NAMESPACE && !manufacturer.metadata.deletionTimestamp && !inventory.issues?.Manufacturer;
  const models = readable && !inventory.issues?.DeviceType ? unique(inventory.physical.filter(record => record.kind === 'DeviceType' && record.metadata.namespace === namespace && !record.metadata.deletionTimestamp)) : [];
  const linkedModels = models.filter(model => model.spec.manufacturerRef?.name === manufacturer.metadata.name);
  const devices = readable && !inventory.issues?.Device && !inventory.issues?.DeviceType ? unique([...inventory.slots.flatMap(slot => slot.devices), ...inventory.unboundDevices].filter(device => device.metadata.namespace === namespace && !device.metadata.deletionTimestamp)) : [];
  const related: { device: Device; model: PhysicalRecord }[] = devices.flatMap(device => {
    const model = linkedModels.find(candidate => candidate.metadata.name === device.spec.deviceTypeRef?.name);
    return model ? [{ device, model }] : [];
  });
  const unresolved = devices.some(device => device.spec.deviceTypeRef && !models.some(model => model.metadata.name === device.spec.deviceTypeRef?.name));
  return { models: linkedModels, devices: related, unresolved };
}
