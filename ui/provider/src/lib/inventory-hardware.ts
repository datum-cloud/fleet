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
  planLocation?: { kind: 'Facility' | 'Rack'; name: string; uid: string; resourceVersion: string };
  claim?: { name: string; uid: string; resourceVersion: string };
  manufacturer?: string;
  model?: string;
  serial?: string;
  systemUUID?: string;
  assetTag?: string;
  components?: PhysicalComponent[];
  medium?: string;
  cableLanes?: { id: string; a?: ReferencedEndpoint; b?: ReferencedEndpoint }[];
}
export const cableMedia = ['Copper', 'Fiber', 'Power', 'Console', 'Unknown'];
export const componentTypes: PhysicalComponent['type'][] = ['NetworkPort', 'PowerInlet', 'PowerOutlet', 'ConsolePort', 'ConsoleServerPort', 'FrontPort', 'RearPort', 'Module', 'InventoryItem', 'DeviceBay'];
export function isTermination(component: PhysicalComponent) { return !['Module', 'InventoryItem', 'DeviceBay'].includes(component.type); }
const namePattern = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/;
const endpointKey = (endpoint: ReferencedEndpoint) => `${endpoint.kind}/${endpoint.name}/${endpoint.componentID}/${endpoint.position}`;

export function hardwarePayload(kind: 'Device' | 'Cable' | 'DeviceClaim', values: InventoryFormValues, common: PhysicalRecord & { apiVersion: string }): Device | PhysicalRecord | DeviceClaim {
  if (kind === 'DeviceClaim') {
    const location = values.planLocation;
    if (!location?.uid || !location.resourceVersion || !['Facility', 'Rack'].includes(location.kind) || !namePattern.test(location.name) || !values.model?.trim() || [values.model, values.manufacturer, values.serial].some(value => (value?.length ?? 0) > 253)) throw new ApiError(422, 'Choose a recorded location and an expected hardware model.');
    return { ...common, kind, spec: { displayName: values.displayName.trim(), lifecycle: 'Installation', expectedIdentity: { model: values.model.trim(), ...(values.manufacturer?.trim() && { manufacturer: values.manufacturer.trim() }), ...(values.serial?.trim() && { serial: values.serial.trim() }) }, placement: location.kind === 'Facility' ? { scope: 'Facility', facilityRef: { name: location.name } } : { scope: 'RackMembership', rackRef: { name: location.name } } } };
  }
  if (kind === 'Device') {
    if (!values.claim?.uid || !values.claim.resourceVersion || !namePattern.test(values.claim.name) || !values.model?.trim() || (!values.serial?.trim() && !values.systemUUID)) throw new ApiError(422, 'Choose an installation plan and enter a model and a serial or system UUID.');
    if (values.systemUUID && !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(values.systemUUID)) throw new ApiError(422, 'Enter a valid lowercase system UUID.');
    if ([values.manufacturer, values.model, values.serial, values.assetTag].some(value => (value?.length ?? 0) > 253)) throw new ApiError(422, 'Hardware fields must be at most 253 characters.');
    const components = values.components ?? [];
    if (components.length > 256 || new Set(components.map(component => component.id)).size !== components.length || components.some(component => !component.id.trim() || component.id.length > 63 || !component.name.trim() || component.name.length > 253 || !componentTypes.includes(component.type) || (component.connector?.length ?? 0) > 253 || !Number.isInteger(component.positions) || component.positions! < 1 || component.positions! > 256)) throw new ApiError(422, 'Review component IDs, names, types, and positions.');
    return { ...common, kind, spec: { displayName: values.displayName.trim(), claimRef: { name: values.claim.name }, identity: { model: values.model.trim(), ...(values.manufacturer?.trim() && { manufacturer: values.manufacturer.trim() }), ...(values.serial?.trim() && { serial: values.serial.trim() }), ...(values.systemUUID && { systemUUID: values.systemUUID }) }, ...(values.assetTag?.trim() && { assetTag: values.assetTag.trim() }), topologyAuthority: 'Cable', components } };
  }
  const lanes = values.cableLanes ?? [];
  if (!cableMedia.includes(values.medium ?? '') || !lanes.length || lanes.length > 64 || new Set(lanes.map(lane => lane.id)).size !== lanes.length) throw new ApiError(422, 'Choose a medium and one or more uniquely named lanes.');
  const used = new Set<string>();
  const endpoint = (value?: ReferencedEndpoint): CableEndpoint => {
    if (!value || !['Device', 'PowerFeed'].includes(value.kind) || !value.uid || !value.resourceVersion || !namePattern.test(value.name) || !value.componentID.trim() || value.componentID.length > 63 || !Number.isInteger(value.position) || value.position < 1 || value.position > 256 || used.has(endpointKey(value))) throw new ApiError(422, 'Choose distinct, fully recorded endpoints for every lane.');
    used.add(endpointKey(value));
    return { ...(value.kind === 'Device' ? { deviceRef: { name: value.name } } : { powerFeedRef: { name: value.name } }), componentID: value.componentID, position: value.position };
  };
  return { ...common, kind, spec: { ...common.spec, medium: values.medium, lanes: lanes.map(lane => { if (!lane.id.trim() || lane.id.length > 63) throw new ApiError(422, 'Lane IDs are required.'); return { id: lane.id, a: endpoint(lane.a), b: endpoint(lane.b) }; }) } };
}

export async function validateHardwareReferences(kind: 'Device' | 'Cable' | 'DeviceClaim', values: InventoryFormValues, operation: InventoryOperation) {
  if (kind === 'DeviceClaim') {
    const snapshot = values.planLocation!;
    const location = await readInventoryReference<PhysicalRecord>(snapshot.kind, values.namespace, snapshot.name, operation);
    operation.assertCurrent();
    if (!location || location.metadata.uid !== snapshot.uid || location.metadata.resourceVersion !== snapshot.resourceVersion || location.metadata.deletionTimestamp) throw new ApiError(409, 'The location changed. Review the installation plan.');
    return;
  }
  if (kind === 'Device') {
    const snapshot = values.claim!;
    const claim = await readInventoryReference<DeviceClaim>('DeviceClaim', values.namespace, snapshot.name, operation);
    operation.assertCurrent();
    if (!claim || claim.metadata.uid !== snapshot.uid || claim.metadata.resourceVersion !== snapshot.resourceVersion || claim.metadata.deletionTimestamp || claim.spec.lifecycle !== 'Installation') throw new ApiError(409, 'The installation plan changed or is a legacy binding. Review it before registration.');
    return;
  }
  for (const lane of values.cableLanes ?? []) for (const endpoint of [lane.a!, lane.b!]) {
    const record = await readInventoryReference<Device | PhysicalRecord>(endpoint.kind, values.namespace, endpoint.name, operation);
    operation.assertCurrent();
    if (!record || record.metadata.uid !== endpoint.uid || record.metadata.resourceVersion !== endpoint.resourceVersion || record.metadata.deletionTimestamp) throw new ApiError(409, 'An endpoint changed. Review the cable before recording.');
    const components = endpoint.kind === 'Device' ? (record as Device).spec.components : (record as PhysicalRecord).spec.terminations;
    const component = components?.find(item => item.id === endpoint.componentID);
    if (!component || !isTermination(component) || component.positions === undefined || endpoint.position > component.positions) throw new ApiError(409, 'An endpoint position is no longer recorded.');
  }
}
