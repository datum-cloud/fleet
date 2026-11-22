// View model for the inventory pages. A slot is one DeviceClaim with every
// Device that names it in the same namespace; a replacement leaves more than
// one. Devices whose claim is missing are kept separately so they stay visible.

import type { CableEndpoint, Device as RawDevice, DeviceClaim as RawDeviceClaim, List, PhysicalRecord } from '../../shared/fleet-api';

export type { RawDevice, RawDeviceClaim };
export type RawDeviceList = List<RawDevice>;
export type RawDeviceClaimList = List<RawDeviceClaim>;
export type InventoryKind = 'Site' | 'Device' | 'DeviceClaim' | 'Facility' | 'Rack' | 'Cable' | 'PowerFeed' | 'Manufacturer' | 'DeviceType';
export const inventoryResources: Record<InventoryKind, string> = {
  Manufacturer: 'manufacturers', DeviceType: 'devicetypes',
  Site: 'sites', Device: 'devices', DeviceClaim: 'deviceclaims', Facility: 'facilities', Rack: 'racks', Cable: 'cables', PowerFeed: 'powerfeeds',
};

export interface InventorySlot {
  key: string;
  claim: RawDeviceClaim;
  devices: RawDevice[];
}

export interface Inventory {
  slots: InventorySlot[];
  unboundDevices: RawDevice[];
  physical: PhysicalRecord[];
  issues?: Partial<Record<InventoryKind, { status: number }>>;
}

export function slotKey(namespace: string | undefined, name: string): string {
  return `${namespace ?? ''}/${name}`;
}

function byCreationTimestamp(a: RawDevice, b: RawDevice): number {
  return (a.metadata.creationTimestamp ?? '').localeCompare(b.metadata.creationTimestamp ?? '') || slotKey(a.metadata.namespace, a.metadata.name).localeCompare(slotKey(b.metadata.namespace, b.metadata.name));
}

export function toInventory(claims: RawDeviceClaim[], devices: RawDevice[], physical: PhysicalRecord[] = []): Inventory {
  const slots = new Map<string, InventorySlot>();
  for (const claim of claims) {
    const key = slotKey(claim.metadata.namespace, claim.metadata.name);
    slots.set(key, { key, claim, devices: [] });
  }
  const unboundDevices: RawDevice[] = [];
  for (const device of devices) {
    const slot = slots.get(slotKey(device.metadata.namespace, device.spec.claimRef.name));
    if (slot) slot.devices.push(device);
    else unboundDevices.push(device);
  }
  for (const slot of slots.values()) slot.devices.sort(byCreationTimestamp);
  return {
    physical,
    slots: [...slots.values()].sort((a, b) => a.key.localeCompare(b.key)),
    unboundDevices: unboundDevices.sort((a, b) => a.metadata.name.localeCompare(b.metadata.name)),
  };
}

export function placementLabel(claim: RawDeviceClaim, issues?: Inventory['issues']): string {
  const { facility, rack, position, height = 1, face, scope, facilityRef, rackRef, mount } = claim.spec.placement;
  const facilityName = issues?.Facility ? 'Facility unavailable' : facility ?? facilityRef?.name;
  const rackName = issues?.Rack ? 'Rack unavailable' : rack ?? rackRef?.name;
  if (scope === 'Facility') return `${facilityName} · rack and position Unknown`;
  if (scope === 'RackMembership') return `${rackName} · position Unknown (no reserved units)`;
  if (scope === 'NonRackUnitMount') return `${rackName} · mount ${mount}`;
  if (position === undefined) return 'Placement Unknown';
  const units = height > 1 ? `U${position}–U${position + height - 1}` : `U${position}`;
  return `${facilityName ?? 'Facility via rack'} · ${rackName} · ${units} ${face}`;
}

export function endpointLabel(endpoint: CableEndpoint, issues?: Inventory['issues']): string {
  if ((endpoint.deviceRef && issues?.Device) || (endpoint.powerFeedRef && issues?.PowerFeed)) return 'Related endpoint unavailable';
  return `${endpoint.deviceRef?.name ?? endpoint.powerFeedRef?.name ?? 'Unknown'}/${endpoint.componentID}:${endpoint.position}`;
}

export function legacyPeerLabel(peer: { device: string; port: string }, issues?: Inventory['issues'], separator = '/'): string {
  return issues?.Device ? 'Related device unavailable' : `${peer.device}${separator}${peer.port}`;
}

export function modelLabel(identity: { manufacturer?: string; model: string }): string {
  return identity.manufacturer ? `${identity.manufacturer} ${identity.model}` : identity.model;
}

// Multiple historical registrations cannot establish the active binding.
export function currentDevice(slot: InventorySlot): RawDevice | undefined {
  return slot.devices.length === 1 ? slot.devices[0] : undefined;
}
