import type { Device, List, MachineCapacity } from '../../../shared/fleet-api';
import { ApiError, PROVIDER_PROJECT, proxyFetch } from './api';
import type { InventoryOperation } from './inventory-operation';

export const REGISTRATION_DEVICE_UID = 'fleet.datumapis.com/registration-device-uid';
export const REGISTRATION_DEVICE_VERSION = 'fleet.datumapis.com/registration-device-version';
export const REGISTRATION_OPERATION = 'fleet.datumapis.com/registration-operation';
const devicesPath = '/apis/fleet.datumapis.com/v1alpha1/namespaces/default/devices';
const capacitiesPath = '/apis/metal.datumapis.com/v1alpha1/namespaces/default/machinecapacities';

export function ineligibleReason(device: Device): string | undefined {
  if (device.metadata.namespace !== 'default' || !device.metadata.uid || !device.metadata.resourceVersion || device.metadata.deletionTimestamp) return 'Device incarnation is unavailable or deleting';
  if (device.spec.identity.unidentified || !device.spec.identity.serial?.trim() || !device.spec.identity.systemUUID) return 'Both serial and system UUID are required; partial identity is ambiguous';
  if (['unknown', 'none', 'n/a', 'default string', 'to be filled by o.e.m.'].includes(device.spec.identity.serial.trim().toLowerCase()) || ['00000000-0000-0000-0000-000000000000', 'ffffffff-ffff-ffff-ffff-ffffffffffff'].includes(device.spec.identity.systemUUID)) return 'Placeholder identity is ineligible';
  return undefined;
}

export async function registrationPermission(): Promise<boolean> {
  if (import.meta.env.VITE_FLEET_FIXTURES === 'true') return false;
  const result = await proxyFetch<{ status?: { allowed?: boolean; denied?: boolean; evaluationError?: string } }>(PROVIDER_PROJECT, '/apis/authorization.k8s.io/v1/selfsubjectaccessreviews', {
    method: 'POST', body: { apiVersion: 'authorization.k8s.io/v1', kind: 'SelfSubjectAccessReview', spec: { resourceAttributes: { group: 'metal.datumapis.com', version: 'v1alpha1', resource: 'machinecapacities', namespace: 'default', verb: 'create' } } },
  });
  return result.status?.allowed === true && result.status.denied !== true && !result.status.evaluationError;
}

export async function registrationDevices(): Promise<Device[]> {
  if (import.meta.env.VITE_FLEET_FIXTURES === 'true') return [];
  const result = await proxyFetch<List<Device>>(PROVIDER_PROJECT, devicesPath);
  return result.items ?? [];
}

export class RegistrationUnknown extends Error {
  constructor(name: string, readonly operationID: string, detail: string) {
    super(`Registration of ${name} is not confirmed. ${detail} Reservations may be retained. Do not retry under another name or delete capacity to roll back; ask an operator to reconcile operation ${operationID}.`);
  }
}

export async function registerCapacity(name: string, selected: Device, operationID: string, operation: InventoryOperation): Promise<MachineCapacity> {
  operation.assertCurrent();
  if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name) || name.length > 63 || !operationID) throw new Error('Enter a valid authored capacity name.');
  if (ineligibleReason(selected)) throw new Error(ineligibleReason(selected));
  if (!await registrationPermission()) throw new ApiError(403, 'Capacity registration permission is denied or unavailable.');
  operation.assertCurrent();
  const current = await proxyFetch<Device>(PROVIDER_PROJECT, `${devicesPath}/${encodeURIComponent(selected.metadata.name)}`);
  operation.assertCurrent();
  if (ineligibleReason(current) || current.metadata.uid !== selected.metadata.uid || current.metadata.resourceVersion !== selected.metadata.resourceVersion) throw new Error('The selected Device changed. Close the form, inspect inventory, and explicitly select it again. No registration was dispatched.');
  const body: MachineCapacity & { apiVersion: string; kind: string } = {
    apiVersion: 'metal.datumapis.com/v1alpha1', kind: 'MachineCapacity',
    metadata: { name, namespace: 'default', annotations: { [REGISTRATION_DEVICE_UID]: selected.metadata.uid!, [REGISTRATION_DEVICE_VERSION]: selected.metadata.resourceVersion!, [REGISTRATION_OPERATION]: operationID } },
    spec: { deviceRef: { name: selected.metadata.name }, allocationPolicy: 'Disabled', cleaningPolicyRef: { name: '' }, reclaimPolicy: 'Retain' },
  };
  operation.dispatch();
  try {
    const result = await proxyFetch<MachineCapacity>(PROVIDER_PROJECT, `${capacitiesPath}?fieldManager=fleet-capacity-registration&fieldValidation=Strict`, { method: 'POST', body });
    operation.assertCurrent();
    if (!result.metadata.uid || result.metadata.name !== name || result.metadata.namespace !== 'default' || result.metadata.annotations?.[REGISTRATION_OPERATION] !== operationID || result.metadata.annotations?.[REGISTRATION_DEVICE_UID] !== selected.metadata.uid || result.metadata.annotations?.[REGISTRATION_DEVICE_VERSION] !== selected.metadata.resourceVersion || result.spec.deviceRef.name !== selected.metadata.name || result.spec.allocationPolicy !== 'Disabled' || result.spec.reclaimPolicy !== 'Retain' || result.spec.cleaningPolicyRef.name !== '') throw new Error('The returned record does not match the exact requested binding.');
    return result;
  } catch (error) {
    throw new RegistrationUnknown(name, operationID, error instanceof Error ? error.message : 'The API did not return a confirmed result.');
  }
}
