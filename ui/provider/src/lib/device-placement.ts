import type { Device, DeviceClaim, PhysicalRecord, ReportedPlacement } from '../../../shared/fleet-api';
import { slotKey, type Inventory } from '../inventory';
import { depthLabel, equipmentDepth } from './device-depth';

export type Placement = DeviceClaim['spec']['placement'] & { fullDepth?: boolean };
export interface EquipmentPlacement {
  key: string;
  kind: 'Device' | 'DeviceClaim';
  record: Device | DeviceClaim;
  placement: Placement;
  basis: 'Reported placement' | 'Claim-derived fallback';
  devices: Device[];
  claim?: DeviceClaim;
  multipleRegistrations?: boolean;
}

function reportedPlacement(report: ReportedPlacement): Placement {
  return { ...report, scope: report.mount ? 'NonRackUnitMount' : report.rackRef ? 'RackUnits' : 'Facility' };
}

export function devicePlacement(device: Device, inventory: Inventory): EquipmentPlacement | undefined {
  if (inventory.issues?.Device) return undefined;
  const slot = !inventory.issues?.DeviceClaim ? inventory.slots.find(candidate => candidate.key === slotKey(device.metadata.namespace, device.spec.claimRef.name)) : undefined;
  if (device.spec.reportedPlacement) return { key: `Device/${slotKey(device.metadata.namespace, device.metadata.name)}`, kind: 'Device', record: device, placement: reportedPlacement(device.spec.reportedPlacement), basis: 'Reported placement', devices: [device], claim: slot?.claim, multipleRegistrations: !!slot && slot.devices.length > 1 };
  if (!slot) return undefined;
  return { key: `DeviceClaim/${slot.key}`, kind: 'DeviceClaim', record: slot.claim, placement: slot.claim.spec.placement, basis: 'Claim-derived fallback', devices: slot.devices.filter(candidate => !candidate.spec.reportedPlacement), claim: slot.claim, multipleRegistrations: slot.devices.length > 1 };
}

export function equipmentPlacements(inventory: Inventory): EquipmentPlacement[] {
  const entries = new Map<string, EquipmentPlacement>();
  if (!inventory.issues?.Device) for (const device of [...inventory.slots.flatMap(slot => slot.devices), ...inventory.unboundDevices]) {
    const entry = devicePlacement(device, inventory);
    if (entry) entries.set(entry.key, entry);
  }
  if (!inventory.issues?.DeviceClaim) for (const slot of inventory.slots) {
    if (slot.devices.length && !inventory.issues?.Device) continue;
    entries.set(`DeviceClaim/${slot.key}`, { key: `DeviceClaim/${slot.key}`, kind: 'DeviceClaim', record: slot.claim, placement: slot.claim.spec.placement, basis: 'Claim-derived fallback', devices: [], claim: slot.claim });
  }
  return [...entries.values()];
}

export function placementFacility(placement: Placement, namespace: string | undefined, inventory: Inventory): string | undefined {
  if (inventory.issues?.Facility) return undefined;
  if (placement.facilityRef) return placement.facilityRef.name;
  if (inventory.issues?.Rack) return undefined;
  return inventory.physical.find(record => record.kind === 'Rack' && record.metadata.namespace === namespace && record.metadata.name === placement.rackRef?.name)?.spec.facilityRef?.name;
}

export function reportedPlacementLabel(entry: EquipmentPlacement, inventory: Inventory): string {
  const { placement, record } = entry;
  const facilityName = placementFacility(placement, record.metadata.namespace, inventory);
  const display = (kind: 'Facility' | 'Rack', name?: string) => inventory.issues?.[kind] ? `${kind} unavailable` : inventory.physical.find(item => item.kind === kind && item.metadata.namespace === record.metadata.namespace && item.metadata.name === name)?.spec.displayName || name;
  return [entry.basis, display('Facility', facilityName), display('Rack', placement.rackRef?.name), placement.mount ? `Mount: ${placement.mount}` : placement.position === undefined ? 'Position not recorded' : `U${placement.position}`, placement.height === undefined ? 'Height unknown' : `${placement.height}U`, placement.face ?? 'Face unknown', depthLabel(equipmentDepth(entry, inventory)), entry.multipleRegistrations ? 'multiple registrations · active device unknown' : undefined].filter(Boolean).join(' · ');
}

export function equipmentGeometry(entry: EquipmentPlacement, rack: PhysicalRecord) {
  const { position, height, face, fullDepth } = entry.placement;
  if (entry.placement.scope !== 'RackUnits' || !Number.isInteger(position) || !Number.isInteger(height) || !position || !height || position < 1 || height < 1 || !rack.spec.heightUnits || position + height - 1 > rack.spec.heightUnits || (face !== 'front' && face !== 'rear')) return undefined;
  return { position, height, face, fullDepth };
}
