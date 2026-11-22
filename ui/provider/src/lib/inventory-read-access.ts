import { createContext } from 'react';
import { ApiError, PROVIDER_PROJECT, proxyFetch } from './api';
import { inventoryResources, type Inventory, type InventoryKind } from '../inventory';
import type { InventoryOperation } from './inventory-operation';

export class InventoryReferenceReadError extends ApiError {
  constructor(readonly kind: InventoryKind, readonly namespace: string, status: number) {
    super(status, 'A related inventory record is unavailable.');
  }
}

export const InventoryReadAccessContext = createContext<(error: InventoryReferenceReadError) => void>(() => {});

export function readFailureIssues(error: InventoryReferenceReadError): Inventory['issues'] {
  return error.status === 401
    ? Object.fromEntries(Object.keys(inventoryResources).map(kind => [kind, { status: 401 }]))
    : { [error.kind]: { status: error.status } };
}

export function redactInventory(source: Inventory, issues: Inventory['issues']): Inventory {
  const combined = { ...source.issues, ...issues };
  if (combined.Manufacturer) combined.DeviceType = combined.Manufacturer;
  return {
    issues: combined,
    slots: combined.DeviceClaim ? [] : source.slots.map(slot => ({ ...slot, devices: combined.Device ? [] : slot.devices })),
    unboundDevices: combined.Device ? [] : [...source.unboundDevices, ...(combined.DeviceClaim ? source.slots.flatMap(slot => slot.devices) : [])],
    physical: source.physical.filter(record => !combined[record.kind]),
  };
}

export async function readInventoryReference<T>(kind: InventoryKind, namespace: string, name: string, operation: InventoryOperation): Promise<T> {
  operation.assertCurrent();
  try {
    const record = await proxyFetch<T>(PROVIDER_PROJECT, `/apis/fleet.datumapis.com/v1alpha1/namespaces/${encodeURIComponent(namespace)}/${inventoryResources[kind]}/${encodeURIComponent(name)}`);
    operation.assertCurrent();
    return record;
  } catch (error) {
    operation.assertCurrent();
    throw new InventoryReferenceReadError(kind, namespace, error instanceof ApiError ? error.status : 0);
  }
}
