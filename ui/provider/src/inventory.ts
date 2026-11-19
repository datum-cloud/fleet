// View model for the inventory pages. A slot is one DeviceClaim with every
// Device that names it in the same namespace; a replacement leaves more than
// one. Devices whose claim is missing are kept separately so they stay visible.

import type { Device as RawDevice, DeviceClaim as RawDeviceClaim, List } from '../../shared/fleet-api';

export type { RawDevice, RawDeviceClaim };
export type RawDeviceList = List<RawDevice>;
export type RawDeviceClaimList = List<RawDeviceClaim>;

export interface InventorySlot {
  key: string;
  claim: RawDeviceClaim;
  devices: RawDevice[];
}

export interface Inventory {
  slots: InventorySlot[];
  unboundDevices: RawDevice[];
}

export function slotKey(namespace: string | undefined, name: string): string {
  return `${namespace ?? ''}/${name}`;
}

function byRecordedAt(a: RawDevice, b: RawDevice): number {
  return a.spec.provenance.recordedAt.localeCompare(b.spec.provenance.recordedAt);
}

export function toInventory(claims: RawDeviceClaim[], devices: RawDevice[]): Inventory {
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
  for (const slot of slots.values()) slot.devices.sort(byRecordedAt);
  return {
    slots: [...slots.values()].sort((a, b) => a.key.localeCompare(b.key)),
    unboundDevices: unboundDevices.sort((a, b) => a.metadata.name.localeCompare(b.metadata.name)),
  };
}

export function placementLabel(claim: RawDeviceClaim): string {
  const { facility, rack, position, height = 1, face } = claim.spec.placement;
  const units = height > 1 ? `U${position}–U${position + height - 1}` : `U${position}`;
  return `${facility} · ${rack} · ${units} ${face}`;
}

export function modelLabel(identity: { manufacturer?: string; model: string }): string {
  return identity.manufacturer ? `${identity.manufacturer} ${identity.model}` : identity.model;
}

// The newest Device bound to a slot is the one currently recorded there.
export function currentDevice(slot: InventorySlot): RawDevice | undefined {
  return slot.devices.at(-1);
}
