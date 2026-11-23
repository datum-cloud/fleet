import type { CableEndpoint, Device, DeviceClaim, PhysicalComponent, PhysicalRecord } from '../../../shared/fleet-api';
import { ApiError } from './api';
import { readInventoryReference } from './inventory-read-access';
import type { InventoryOperation } from './inventory-operation';
import type { InventoryFormValues } from './inventory-management';

export interface ReferencedEndpoint {
  kind: 'Device' | 'PowerFeed';
  name: string;
  uid: string;
  resourceVersion: string;
  componentID: string;
  position: number;
}
export interface HardwareFields {
  catalog?: { namespace: string; name: string; uid: string; resourceVersion: string; manufacturer: { name: string; uid: string; resourceVersion: string } };
  planLocation?: { kind: 'Facility' | 'Rack'; name: string; uid: string; resourceVersion: string };
  claim?: { name: string; uid: string; resourceVersion: string };
  manufacturer?: string;
  model?: string;
  serial?: string;
  systemUUID?: string;
  assetTag?: string;
  components?: PhysicalComponent[];
  medium?: string;
  /** aSource and bSource hold the device or feed picked for an end before its port (`Kind/uid`). */
  cableLanes?: { id: string; a?: ReferencedEndpoint; b?: ReferencedEndpoint; aSource?: string; bSource?: string }[];
}
export const cableMedia = ['Copper', 'Fiber', 'Power', 'Console', 'Unknown'];
export const componentTypes: PhysicalComponent['type'][] = ['NetworkPort', 'PowerInlet', 'PowerOutlet', 'ConsolePort', 'ConsoleServerPort', 'FrontPort', 'RearPort', 'Module', 'InventoryItem', 'DeviceBay'];
export function isTermination(component: PhysicalComponent) { return !['Module', 'InventoryItem', 'DeviceBay'].includes(component.type); }
const namePattern = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/;
export const systemUUIDPattern = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}';

/**
 * A rule the form checks before any request. Its message names the problem in
 * the operator's terms and is shown as written; it never comes from the API.
 */
export class InventoryValidationError extends ApiError {}

/** A record name derived from a display name: lowercase letters, digits, '-' and '.'. */
export function recordNameFrom(displayName: string) {
  return displayName.toLowerCase().replace(/[^a-z0-9.-]+/g, '-').replace(/-{2,}/g, '-').slice(0, 253).replace(/^[^a-z0-9]+|[^a-z0-9]+$/g, '');
}

/** The serial-or-UUID rule, as the message the Serial and System UUID fields show. */
export function deviceIdentityProblems(values: Pick<HardwareFields, 'serial' | 'systemUUID'>) {
  return {
    serial: !values.serial?.trim() && !values.systemUUID ? 'Enter a serial number or a system UUID.' : '',
    systemUUID: values.systemUUID && !new RegExp(`^${systemUUIDPattern}$`).test(values.systemUUID) ? 'Enter a lowercase UUID, such as 00000000-0000-4000-8000-000000000000.' : '',
  };
}

/** Recorded cable lanes by endpoint, so a picker can mark a port that already has a cable. */
export function cabledEndpoints(cables: PhysicalRecord[]) {
  const cabled = new Map<string, string>();
  for (const cable of cables) for (const lane of cable.spec.lanes ?? []) for (const end of [lane.a, lane.b]) {
    const ref = end?.deviceRef ? `Device/${end.deviceRef.name}` : end?.powerFeedRef ? `PowerFeed/${end.powerFeedRef.name}` : undefined;
    if (ref && end) cabled.set(`${ref}/${end.componentID}/${end.position}`, cable.spec.displayName || cable.metadata.name);
  }
  return cabled;
}
export const endpointKey = (endpoint: Pick<ReferencedEndpoint, 'kind' | 'name' | 'componentID' | 'position'>) => `${endpoint.kind}/${endpoint.name}/${endpoint.componentID}/${endpoint.position}`;

export function hardwarePayload(kind: 'Device' | 'Cable' | 'DeviceClaim', values: InventoryFormValues, common: PhysicalRecord & { apiVersion: string }): Device | PhysicalRecord | DeviceClaim {
  if (kind === 'DeviceClaim') {
    const location = values.planLocation;
    if (!location?.uid || !location.resourceVersion || !['Facility', 'Rack'].includes(location.kind) || !namePattern.test(location.name) || !values.model?.trim() || [values.model, values.manufacturer, values.serial].some(value => (value?.length ?? 0) > 253)) throw new InventoryValidationError(422, 'Choose a recorded location and an expected hardware model.');
    return { ...common, kind, spec: { displayName: values.displayName.trim(), lifecycle: 'Installation', expectedIdentity: { model: values.model.trim(), ...(values.manufacturer?.trim() && { manufacturer: values.manufacturer.trim() }), ...(values.serial?.trim() && { serial: values.serial.trim() }) }, placement: location.kind === 'Facility' ? { scope: 'Facility', facilityRef: { name: location.name } } : { scope: 'RackMembership', rackRef: { name: location.name } } } };
  }
  if (kind === 'Device') {
    if (values.catalog && (values.catalog.namespace !== values.namespace || !values.catalog.uid || !values.catalog.resourceVersion || !values.catalog.manufacturer?.uid || !values.catalog.manufacturer.resourceVersion || !namePattern.test(values.catalog.name) || !namePattern.test(values.catalog.manufacturer.name))) throw new InventoryValidationError(422, 'Choose a readable catalog model in this namespace.');
    if (!values.claim?.uid || !values.claim.resourceVersion || !namePattern.test(values.claim.name) || !values.model?.trim() || (!values.serial?.trim() && !values.systemUUID)) throw new InventoryValidationError(422, 'Choose an installation plan and enter a model and a serial or system UUID.');
    if (values.systemUUID && !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(values.systemUUID)) throw new InventoryValidationError(422, 'Enter a valid lowercase system UUID.');
    if ([values.manufacturer, values.model, values.serial, values.assetTag].some(value => (value?.length ?? 0) > 253)) throw new InventoryValidationError(422, 'Hardware fields must be at most 253 characters.');
    const components = values.components ?? [];
    if (components.length > 256 || new Set(components.map(component => component.id)).size !== components.length || components.some(component => !component.id.trim() || component.id.length > 63 || !component.name.trim() || component.name.length > 253 || !componentTypes.includes(component.type) || (component.connector?.length ?? 0) > 253 || !Number.isInteger(component.positions) || component.positions! < 1 || component.positions! > 256)) throw new InventoryValidationError(422, 'Review component IDs, names, types, and positions.');
    return { ...common, kind, spec: { displayName: values.displayName.trim(), claimRef: { name: values.claim.name }, ...(values.catalog && { deviceTypeRef: { name: values.catalog.name } }), identity: { model: values.model.trim(), ...(values.manufacturer?.trim() && { manufacturer: values.manufacturer.trim() }), ...(values.serial?.trim() && { serial: values.serial.trim() }), ...(values.systemUUID && { systemUUID: values.systemUUID }) }, ...(values.assetTag?.trim() && { assetTag: values.assetTag.trim() }), topologyAuthority: 'Cable', components } };
  }
  const lanes = values.cableLanes ?? [];
  if (!cableMedia.includes(values.medium ?? '') || !lanes.length || lanes.length > 64 || new Set(lanes.map(lane => lane.id)).size !== lanes.length) throw new InventoryValidationError(422, 'Choose a medium and one or more uniquely named lanes.');
  const used = new Set<string>();
  const endpoint = (value?: ReferencedEndpoint): CableEndpoint => {
    if (!value || !['Device', 'PowerFeed'].includes(value.kind) || !value.uid || !value.resourceVersion || !namePattern.test(value.name) || !value.componentID.trim() || value.componentID.length > 63 || !Number.isInteger(value.position) || value.position < 1 || value.position > 256 || used.has(endpointKey(value))) throw new InventoryValidationError(422, 'Choose distinct, fully recorded endpoints for every lane.');
    used.add(endpointKey(value));
    return { ...(value.kind === 'Device' ? { deviceRef: { name: value.name } } : { powerFeedRef: { name: value.name } }), componentID: value.componentID, position: value.position };
  };
  return { ...common, kind, spec: { ...common.spec, medium: values.medium, lanes: lanes.map(lane => { if (!lane.id.trim() || lane.id.length > 63) throw new InventoryValidationError(422, 'Lane IDs are required.'); return { id: lane.id, a: endpoint(lane.a), b: endpoint(lane.b) }; }) } };
}

export async function validateHardwareReferences(kind: 'Device' | 'Cable' | 'DeviceClaim', values: InventoryFormValues, operation: InventoryOperation) {
  if (kind === 'DeviceClaim') {
    const snapshot = values.planLocation!;
    const location = await readInventoryReference<PhysicalRecord>(snapshot.kind, values.namespace, snapshot.name, operation);
    operation.assertCurrent();
    if (!location || location.metadata.uid !== snapshot.uid || location.metadata.resourceVersion !== snapshot.resourceVersion || location.metadata.deletionTimestamp) throw new InventoryValidationError(409, 'The location changed. Review the installation plan.');
    return;
  }
  if (kind === 'Device') {
    const snapshot = values.claim!;
    const claim = await readInventoryReference<DeviceClaim>('DeviceClaim', values.namespace, snapshot.name, operation);
    operation.assertCurrent();
    if (!claim || claim.metadata.uid !== snapshot.uid || claim.metadata.resourceVersion !== snapshot.resourceVersion || claim.metadata.deletionTimestamp) throw new InventoryValidationError(409, 'The installation plan changed. Review it before registration.');
    if (values.catalog) {
      const selected = values.catalog;
      const model = await readInventoryReference<PhysicalRecord>('DeviceType', values.namespace, selected.name, operation);
      operation.assertCurrent();
      if (selected.namespace !== values.namespace || model.metadata.namespace !== values.namespace || model.metadata.name !== selected.name || model.metadata.uid !== selected.uid || model.metadata.resourceVersion !== selected.resourceVersion || model.metadata.deletionTimestamp || model.spec.manufacturerRef?.name !== selected.manufacturer.name) throw new InventoryValidationError(409, 'The catalog changed. Refresh and review the selection.');
      const manufacturer = await readInventoryReference<PhysicalRecord>('Manufacturer', values.namespace, selected.manufacturer.name, operation);
      operation.assertCurrent();
      if (manufacturer.metadata.namespace !== values.namespace || manufacturer.metadata.name !== selected.manufacturer.name || manufacturer.metadata.uid !== selected.manufacturer.uid || manufacturer.metadata.resourceVersion !== selected.manufacturer.resourceVersion || manufacturer.metadata.deletionTimestamp) throw new InventoryValidationError(409, 'The catalog changed. Refresh and review the selection.');
    }
    return;
  }
  for (const lane of values.cableLanes ?? []) for (const endpoint of [lane.a!, lane.b!]) {
    const record = await readInventoryReference<Device | PhysicalRecord>(endpoint.kind, values.namespace, endpoint.name, operation);
    operation.assertCurrent();
    if (!record || record.metadata.uid !== endpoint.uid || record.metadata.resourceVersion !== endpoint.resourceVersion || record.metadata.deletionTimestamp) throw new InventoryValidationError(409, 'An endpoint changed. Review the cable before recording.');
    const components = endpoint.kind === 'Device' ? (record as Device).spec.components : (record as PhysicalRecord).spec.terminations;
    const component = components?.find(item => item.id === endpoint.componentID);
    if (!component || !isTermination(component) || component.positions === undefined || endpoint.position > component.positions) throw new InventoryValidationError(409, 'An endpoint position is no longer recorded.');
  }
}
