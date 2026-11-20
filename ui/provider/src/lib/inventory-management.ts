import { useQuery } from '@tanstack/react-query';
import { ApiError, PLUGIN_ID, PROVIDER_PROJECT, proxyFetch } from './api';
import type { PhysicalRecord } from '../../../shared/fleet-api';

export type ManagedKind = 'Facility' | 'Rack';
export const INVENTORY_FIELD_MANAGER = 'fleet-staff-inventory-ui';
const resources = { Facility: 'facilities', Rack: 'racks' } as const;
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
  return useQuery({ queryKey: [PLUGIN_ID, 'inventory-permission', PROVIDER_PROJECT, kind, namespace, verb, name], queryFn: () => inventoryPermission(kind, namespace, verb, name), enabled: !!namespace, retry: false, gcTime: 0, staleTime: 0, refetchOnMount: 'always' });
}

export function canEditInventoryLabel(record: PhysicalRecord): boolean {
  const { metadata, spec } = record;
  return (record.kind === 'Facility' || record.kind === 'Rack') && !!metadata.uid && !!metadata.resourceVersion && !metadata.deletionTimestamp && !spec.provenance.source && spec.provenance.method === 'Manual' &&
    !Object.keys({ ...metadata.labels, ...metadata.annotations }).some(key => /fluxcd\.io|argocd|inventory.*owner/i.test(key)) &&
    !!metadata.managedFields?.length && metadata.managedFields.every(field => field.manager === INVENTORY_FIELD_MANAGER && !field.subresource);
}

export interface InventoryFormValues {
  name: string;
  namespace: string;
  displayName: string;
  recordedBy: string;
  reference: string;
  facility: string;
  height: string;
}

export function inventoryPayload(kind: ManagedKind, values: InventoryFormValues, now = new Date().toISOString()): PhysicalRecord & { apiVersion: string } {
  const resourceName = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/;
  if (!resourceName.test(values.name) || values.name.length > 253 || !/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(values.namespace) || values.namespace.length > 63) throw new ApiError(422, 'Enter a valid record name and namespace.');
  if (!values.displayName.trim() || values.displayName.trim().length > 253 || !values.recordedBy.trim() || values.recordedBy.trim().length > 253) throw new ApiError(422, 'Display name and recorder are required (at most 253 characters each).');
  if (kind === 'Rack' && !resourceName.test(values.facility)) throw new ApiError(422, 'Choose a facility in this namespace.');
  if (kind === 'Rack' && values.height && (!/^\d+$/.test(values.height) || Number(values.height) < 1 || Number(values.height) > 100)) throw new ApiError(422, 'Rack height must be a whole number from 1 to 100, or left blank when unknown.');
  return { apiVersion: 'fleet.datumapis.com/v1alpha1', kind, metadata: { name: values.name, namespace: values.namespace }, spec: {
    displayName: values.displayName.trim(), provenance: { method: 'Manual', recordedBy: values.recordedBy.trim(), recordedAt: now, ...(values.reference.trim() && { reference: values.reference.trim() }) },
    ...(kind === 'Rack' && { facilityRef: { name: values.facility }, ...(values.height && { heightUnits: Number(values.height) }) }),
  } };
}

async function requirePermission(kind: ManagedKind, namespace: string, verb: 'create' | 'patch', name?: string) {
  if (!await inventoryPermission(kind, namespace, verb, name)) throw new ApiError(403, 'Inventory management permission is not available.');
}

export async function createInventoryRecord(kind: ManagedKind, values: InventoryFormValues) {
  const payload = inventoryPayload(kind, values);
  await requirePermission(kind, values.namespace, 'create');
  if (kind === 'Rack') {
    const facility = await proxyFetch<PhysicalRecord>(PROVIDER_PROJECT, `${resourcePath('Facility', values.namespace)}/${encodeURIComponent(values.facility)}`);
    if (!facility?.metadata?.uid || facility.metadata.deletionTimestamp) throw new ApiError(409, 'Facility is not available. Refresh inventory before continuing.');
  }
  const record = await proxyFetch<PhysicalRecord>(PROVIDER_PROJECT, `${resourcePath(kind, values.namespace)}?fieldManager=${INVENTORY_FIELD_MANAGER}`, { method: 'POST', body: payload });
  if (!record?.metadata?.uid) throw new Error('Save outcome could not be confirmed.');
  return record;
}

export async function editInventoryLabel(original: PhysicalRecord, values: InventoryFormValues) {
  if (!canEditInventoryLabel(original)) throw new ApiError(409, 'This record needs an explicit ownership review before editing.');
  const kind = original.kind as ManagedKind;
  const namespace = original.metadata.namespace ?? '';
  await requirePermission(kind, namespace, 'patch', original.metadata.name);
  const path = `${resourcePath(kind, namespace)}/${encodeURIComponent(original.metadata.name)}`;
  const current = await proxyFetch<PhysicalRecord>(PROVIDER_PROJECT, path);
  if (!current || current.metadata.uid !== original.metadata.uid || current.metadata.resourceVersion !== original.metadata.resourceVersion || !canEditInventoryLabel(current)) throw new ApiError(409, 'The record or its ownership changed. Reload it before editing.');
  const now = new Date().toISOString();
  if (now <= current.spec.provenance.recordedAt) throw new ApiError(409, 'The recorded timestamp is not earlier than this clock. Review the record before editing.');
  const validated = inventoryPayload('Facility', { ...values, name: original.metadata.name, namespace }, now);
  const record = await proxyFetch<PhysicalRecord>(PROVIDER_PROJECT, `${path}?fieldManager=${INVENTORY_FIELD_MANAGER}`, { method: 'PATCH', contentType: 'application/json-patch+json', body: [
    { op: 'test', path: '/metadata/uid', value: current.metadata.uid },
    { op: 'test', path: '/metadata/resourceVersion', value: current.metadata.resourceVersion },
    { op: 'replace', path: '/spec/displayName', value: validated.spec.displayName },
    { op: 'replace', path: '/spec/provenance', value: validated.spec.provenance },
  ] });
  if (!record?.metadata?.uid) throw new Error('Save outcome could not be confirmed.');
  return record;
}
