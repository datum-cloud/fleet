import { useQuery } from '@tanstack/react-query';
import { ApiError, PLUGIN_ID, PROVIDER_PROJECT, proxyFetch } from './api';
import type { Device, DeviceClaim, PhysicalRecord } from '../../../shared/fleet-api';
import type { InventoryOperation } from './inventory-operation';
import { hardwarePayload, validateHardwareReferences, type HardwareFields } from './inventory-hardware';
import { inventoryResources } from '../inventory';
import { readInventoryReference } from './inventory-read-access';
import { useInventoryRead } from './inventory-read';

export type ManagedKind = 'Facility' | 'Rack' | 'Device' | 'Cable' | 'DeviceClaim';
export type ManagedRecord = PhysicalRecord | Device | DeviceClaim;
export const INVENTORY_FIELD_MANAGER = 'fleet-staff-inventory-ui';
const resources = inventoryResources;
const fixtures = import.meta.env.VITE_FLEET_FIXTURES === 'true';
const resourcePath = (kind: ManagedKind, namespace: string) => `/apis/fleet.datumapis.com/v1alpha1/namespaces/${encodeURIComponent(namespace)}/${resources[kind]}`;

export async function inventoryPermission(kind: ManagedKind, namespace: string, verb: 'create' | 'patch', name?: string): Promise<boolean> {
  if (!namespace || fixtures) return false;
  const review = await proxyFetch<{ status?: { allowed?: boolean; denied?: boolean; evaluationError?: string } }>(PROVIDER_PROJECT, '/apis/authorization.k8s.io/v1/selfsubjectaccessreviews', {
    method: 'POST', body: { apiVersion: 'authorization.k8s.io/v1', kind: 'SelfSubjectAccessReview', spec: { resourceAttributes: { group: 'fleet.datumapis.com', version: 'v1alpha1', resource: resources[kind], namespace, verb, ...(name && { name }) } } },
  });
  return review?.status?.allowed === true && review.status.denied !== true && !review.status.evaluationError;
}

export function useInventoryPermission(kind: ManagedKind, namespace: string, verb: 'create' | 'patch', name?: string) {
  const read = useInventoryRead(() => inventoryPermission(kind, namespace, verb, name));
  return useQuery({ queryKey: [PLUGIN_ID, 'inventory-permission', PROVIDER_PROJECT, kind, namespace, verb, name], queryFn: read, enabled: !!namespace, retry: false, gcTime: 0, staleTime: 0, refetchOnMount: 'always' });
}

export function canEditInventoryLabel(record: ManagedRecord): boolean {
  const { metadata, spec } = record;
  return ['Facility', 'Rack', 'Device', 'Cable'].includes(record.kind ?? '') && !!metadata.uid && !!metadata.resourceVersion && !metadata.deletionTimestamp &&
    !Object.keys({ ...metadata.labels, ...metadata.annotations }).some(key => /fluxcd\.io|argocd|inventory.*owner/i.test(key)) &&
    !!metadata.managedFields?.length && metadata.managedFields.every(field => field.manager === INVENTORY_FIELD_MANAGER && !field.subresource);
}

export interface InventoryFormValues extends HardwareFields {
  name: string;
  namespace: string;
  displayName: string;
  facility: string;
  height: string;
}

export function inventoryPayload(kind: 'Facility' | 'Rack', values: InventoryFormValues): PhysicalRecord & { apiVersion: string } {
  const resourceName = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/;
  if (!resourceName.test(values.name) || values.name.length > 253 || !/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(values.namespace) || values.namespace.length > 63) throw new ApiError(422, 'Enter a valid record name and namespace.');
  if (!values.displayName.trim() || values.displayName.trim().length > 253) throw new ApiError(422, 'A display name is required (at most 253 characters).');
  if (kind === 'Rack' && !resourceName.test(values.facility)) throw new ApiError(422, 'Choose a facility in this namespace.');
  if (kind === 'Rack' && values.height && (!/^\d+$/.test(values.height) || Number(values.height) < 1 || Number(values.height) > 100)) throw new ApiError(422, 'Rack height must be a whole number from 1 to 100, or left blank when unknown.');
  return { apiVersion: 'fleet.datumapis.com/v1alpha1', kind, metadata: { name: values.name, namespace: values.namespace }, spec: {
    displayName: values.displayName.trim(),
    ...(kind === 'Rack' && { facilityRef: { name: values.facility }, ...(values.height && { heightUnits: Number(values.height) }) }),
  } };
}

async function requirePermission(kind: ManagedKind, namespace: string, verb: 'create' | 'patch', name?: string) {
  if (!await inventoryPermission(kind, namespace, verb, name)) throw new ApiError(403, 'Inventory management permission is not available.');
}

export async function createInventoryRecord(kind: ManagedKind, values: InventoryFormValues, operation: InventoryOperation) {
  operation.assertCurrent();
  const common = inventoryPayload(kind === 'Rack' ? 'Rack' : 'Facility', values);
  const payload = kind === 'Device' || kind === 'Cable' || kind === 'DeviceClaim' ? hardwarePayload(kind, values, common) : common;
  await requirePermission(kind, values.namespace, 'create');
  operation.assertCurrent();
  if (kind === 'Device' || kind === 'Cable' || kind === 'DeviceClaim') await validateHardwareReferences(kind, values, operation);
  operation.assertCurrent();
  if (kind === 'Rack') {
    const facility = await readInventoryReference<PhysicalRecord>('Facility', values.namespace, values.facility, operation);
    operation.assertCurrent();
    if (!facility?.metadata?.uid || facility.metadata.deletionTimestamp) throw new ApiError(409, 'Facility is not available. Refresh inventory before continuing.');
  }
  operation.dispatch();
  const record = await proxyFetch<PhysicalRecord>(PROVIDER_PROJECT, `${resourcePath(kind, values.namespace)}?fieldManager=${INVENTORY_FIELD_MANAGER}`, { method: 'POST', body: payload }).catch(error => { operation.assertCurrent(); throw error; });
  operation.assertCurrent();
  if (!record?.metadata?.uid) throw new Error('Save outcome could not be confirmed.');
  return record;
}

export async function editInventoryLabel(original: ManagedRecord, values: InventoryFormValues, operation: InventoryOperation) {
  operation.assertCurrent();
  if (!canEditInventoryLabel(original)) throw new ApiError(409, 'This record needs an explicit ownership review before editing.');
  const kind = original.kind as ManagedKind;
  const namespace = original.metadata.namespace ?? '';
  await requirePermission(kind, namespace, 'patch', original.metadata.name);
  operation.assertCurrent();
  const path = `${resourcePath(kind, namespace)}/${encodeURIComponent(original.metadata.name)}`;
  const current = await readInventoryReference<ManagedRecord>(kind, namespace, original.metadata.name, operation);
  operation.assertCurrent();
  if (!current || current.metadata.uid !== original.metadata.uid || current.metadata.resourceVersion !== original.metadata.resourceVersion || !canEditInventoryLabel(current)) throw new ApiError(409, 'The record or its ownership changed. Reload it before editing.');
  const validated = inventoryPayload('Facility', { ...values, name: original.metadata.name, namespace });
  operation.dispatch();
  const record = await proxyFetch<PhysicalRecord>(PROVIDER_PROJECT, `${path}?fieldManager=${INVENTORY_FIELD_MANAGER}`, { method: 'PATCH', contentType: 'application/json-patch+json', body: [
    { op: 'test', path: '/metadata/uid', value: current.metadata.uid },
    { op: 'test', path: '/metadata/resourceVersion', value: current.metadata.resourceVersion },
    { op: current.spec.displayName === undefined ? 'add' : 'replace', path: '/spec/displayName', value: validated.spec.displayName },
  ] }).catch(error => { operation.assertCurrent(); throw error; });
  operation.assertCurrent();
  if (!record?.metadata?.uid) throw new Error('Save outcome could not be confirmed.');
  return record;
}
