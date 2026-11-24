import type { Device, PhysicalRecord, ReportedPlacement } from '../../../shared/fleet-api';
import { ApiError, PROVIDER_PROJECT, proxyFetch } from './api';
import { inventoryPermission, INVENTORY_FIELD_MANAGER } from './inventory-management';
import type { InventoryOperation } from './inventory-operation';
import { readInventoryReference } from './inventory-read-access';
import { inInventoryNamespace } from './inventory-namespace';
import { InventoryValidationError } from './inventory-hardware';

export interface PlacementFields {
  facility: string;
  rack: string;
  position: string;
  height: string;
  face: string;
  fullDepth: string;
  mount: string;
  observedAt: string;
  reason: string;
  reference: string;
}

export function canCorrectPlacement(device: Device): boolean {
  return inInventoryNamespace(device) && !!device.metadata.uid && !!device.metadata.resourceVersion && !device.metadata.deletionTimestamp &&
    !Object.keys({ ...device.metadata.labels, ...device.metadata.annotations }).some(key => /fluxcd\.io|argocd/i.test(key)) &&
    !(device.metadata.managedFields ?? []).some(field => /flux|kustomize-controller|helm-controller|argocd/i.test(field.manager ?? ''));
}

export function placementFields(report?: ReportedPlacement): PlacementFields {
  return { facility: report?.facilityRef?.name ?? '', rack: report?.rackRef?.name ?? '', position: report?.position?.toString() ?? '', height: report?.height?.toString() ?? '', face: report?.face ?? '', fullDepth: report?.fullDepth?.toString() ?? '', mount: report?.mount ?? '', observedAt: new Date().toISOString(), reason: '', reference: '' };
}

export function placementPayload(original: Device, fields: PlacementFields, now = new Date().toISOString()): ReportedPlacement {
  const invalid = (message: string): never => { throw new InventoryValidationError(422, message); };
  const name = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/;
  for (const value of [fields.facility, fields.rack]) if (value && (!name.test(value) || value.length > 253)) invalid('Choose a valid location record.');
  if (!fields.reason.trim() || !fields.reference.trim() || fields.reason.trim().length > 1024 || fields.reference.trim().length > 1024) invalid('Reason and evidence reference are required (at most 1024 characters).');
  const observed = Date.parse(fields.observedAt);
  if (!Number.isFinite(observed) || observed > Date.parse(now)) invalid('Observation time must be valid and no later than the current time.');
  const report: ReportedPlacement = { revision: (original.spec.reportedPlacement?.revision ?? 0) + 1, observedAt: new Date(observed).toISOString(), reason: fields.reason.trim(), reference: fields.reference.trim() };
  if (fields.facility) report.facilityRef = { name: fields.facility };
  if (fields.rack) report.rackRef = { name: fields.rack };
  for (const field of ['position', 'height'] as const) {
    if (!fields[field].trim()) continue;
    const value = Number(fields[field]);
    if (!Number.isFinite(value) || value > 100 || (field === 'position' ? value < 1 : value <= 0)) invalid('Position must be 1–100 and height greater than zero, or blank for unknown.');
    report[field] = value;
  }
  if (fields.face) {
    if (fields.face !== 'front' && fields.face !== 'rear') invalid('Choose front, rear, or unknown.');
    report.face = fields.face as 'front' | 'rear';
  }
  if (fields.fullDepth) {
    if (!['true', 'false'].includes(fields.fullDepth)) invalid('Choose full depth, partial depth, or unknown.');
    report.fullDepth = fields.fullDepth === 'true';
  }
  if (fields.mount.trim()) {
    if (fields.mount.trim().length > 253) invalid('Mount is too long.');
    report.mount = fields.mount.trim();
  }
  const units = report.position !== undefined || report.height !== undefined || report.face !== undefined || report.fullDepth !== undefined;
  if ((units || report.mount) && !report.rackRef) invalid('Geometry requires a rack.');
  if (units && report.mount) invalid('Non-unit mount and unit geometry cannot be combined.');
  if (report.position !== undefined && report.height !== undefined && report.position + report.height > 101) invalid('Geometry exceeds supported rack bounds.');
  return report;
}

export async function correctDevicePlacement(original: Device, fields: PlacementFields, operation: InventoryOperation) {
  operation.assertCurrent();
  if (!canCorrectPlacement(original)) throw new ApiError(409, 'This record is deleting, incomplete, or managed by GitOps.');
  const namespace = original.metadata.namespace ?? '';
  if (!namespace) throw new ApiError(422, 'A namespace is required.');
  const permitted = await inventoryPermission('Device', namespace, 'patch', original.metadata.name).catch(error => { operation.assertCurrent(); throw error; });
  operation.assertCurrent();
  if (!permitted) throw new ApiError(403, 'Management permission is required.');
  const current = await readInventoryReference<Device>('Device', namespace, original.metadata.name, operation);
  operation.assertCurrent();
  if (current.metadata.uid !== original.metadata.uid || current.metadata.resourceVersion !== original.metadata.resourceVersion || !canCorrectPlacement(current)) throw new ApiError(409, 'The Device changed. Refresh and reopen the form.');
  const report = placementPayload(original, fields);
  if (report.facilityRef) {
    const facility = await readInventoryReference<PhysicalRecord>('Facility', namespace, report.facilityRef.name, operation);
    operation.assertCurrent();
    if (!facility.metadata.uid || facility.metadata.deletionTimestamp) throw new ApiError(409, 'Facility is unavailable.');
  }
  if (report.rackRef) {
    const rack = await readInventoryReference<PhysicalRecord>('Rack', namespace, report.rackRef.name, operation);
    operation.assertCurrent();
    if (!rack.metadata.uid || rack.metadata.deletionTimestamp || (report.facilityRef && rack.spec.facilityRef?.name !== report.facilityRef.name)) throw new ApiError(409, 'Rack or facility changed.');
    const height = rack.spec.heightUnits;
    if (height !== undefined && ((report.position !== undefined && report.position > height) || (report.height !== undefined && report.height > height) || (report.position !== undefined && report.height !== undefined && report.position + report.height - 1 > height))) throw new InventoryValidationError(422, 'Geometry exceeds the recorded rack height.');
  }
  const path = `/apis/fleet.datumapis.com/v1alpha1/namespaces/${encodeURIComponent(namespace)}/devices/${encodeURIComponent(original.metadata.name)}?fieldManager=${INVENTORY_FIELD_MANAGER}`;
  const patch = [
    { op: 'test', path: '/metadata/uid', value: original.metadata.uid },
    { op: 'test', path: '/metadata/resourceVersion', value: original.metadata.resourceVersion },
    { op: 'add', path: '/spec/reportedPlacement', value: report },
  ];
  operation.dispatch();
  const saved = await proxyFetch<Device>(PROVIDER_PROJECT, path, { method: 'PATCH', contentType: 'application/json-patch+json', body: patch }).catch(error => {
    operation.assertCurrent();
    if (error instanceof ApiError && error.status === 422) throw new ApiError(409, 'Patch preconditions or admission rejected this baseline. Reopen the form.');
    throw error;
  });
  operation.assertCurrent();
  if (saved?.metadata?.uid !== original.metadata.uid || !saved.metadata.resourceVersion || saved.metadata.resourceVersion === original.metadata.resourceVersion || Object.entries(report).some(([key, value]) => JSON.stringify(saved.spec?.reportedPlacement?.[key as keyof ReportedPlacement]) !== JSON.stringify(value))) throw new Error('Save outcome is unknown.');
  return saved;
}
