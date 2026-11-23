// Synthetic rack, catalog and cabling records that exercise device depth and
// component faces. Used by tests and by the local fixture preview. Every name,
// serial and identifier is invented.

import type { RawDevice, RawDeviceClaim } from '../inventory';
import type { CableEndpoint, ComponentTemplate, PhysicalComponent, PhysicalRecord, RackFace } from '../../../shared/fleet-api';

const NS = 'default';
export const DEPTH_RACK = 'rack-02';

type Template = ComponentTemplate;
const T = (id: string, name: string, type: Template['type'], face?: Template['face'], extra: Partial<Template> = {}): Template => ({ id, name, type, ...(face ? { face } : {}), ...extra });
const asComponent = ({ face: _face, ...template }: Template): PhysicalComponent => template;

function deviceType(name: string, displayName: string, model: string, templates: Template[], fullDepth?: boolean): PhysicalRecord {
  return {
    kind: 'DeviceType', metadata: { name, namespace: NS },
    spec: { displayName, manufacturerRef: { name: 'example-vendor' }, model, modelShape: { heightUnits: 1, ...(fullDepth === undefined ? {} : { fullDepth }) }, componentTemplates: templates },
  };
}

const computeTemplates = [
  T('psu-1', 'Power supply 1', 'PowerInlet', 'Rear', { connector: 'IEC C14' }),
  T('psu-2', 'Power supply 2', 'PowerInlet', 'Rear', { connector: 'IEC C14' }),
  T('eth0', 'Ethernet 0', 'NetworkPort', 'Rear', { connector: 'SFP28' }),
  T('eth1', 'Ethernet 1', 'NetworkPort', 'Rear', { connector: 'SFP28' }),
  T('bmc', 'Management port', 'NetworkPort', 'Rear', { connector: 'RJ45' }),
  T('console', 'Front console', 'ConsolePort', 'Front', { connector: 'USB-C' }),
];
// A switch whose chassis front is its port side.
const leafTemplates = [
  ...Array.from({ length: 4 }, (_, index) => T(`swp${index + 1}`, `swp${index + 1}`, 'NetworkPort', 'Front', { connector: 'SFP28' })),
  T('mgmt0', 'mgmt0', 'NetworkPort', 'Front', { connector: 'RJ45' }),
  T('psu-1', 'Power supply 1', 'PowerInlet', 'Rear', { connector: 'IEC C14' }),
  T('psu-2', 'Power supply 2', 'PowerInlet', 'Rear', { connector: 'IEC C14' }),
];
const panelTemplates = [
  T('front', 'Front ports', 'FrontPort', 'Front', { connector: 'LC', positions: 12 }),
  T('rear', 'Rear trunk', 'RearPort', 'Rear', { connector: 'MPO', positions: 12 }),
];

export const depthCatalog: PhysicalRecord[] = [
  { kind: 'Manufacturer', metadata: { name: 'example-vendor', namespace: NS }, spec: { displayName: 'Example Vendor', canonicalName: 'Example Vendor' } },
  deviceType('example-compute-1u', 'Example compute 1U', 'example-compute-1u', computeTemplates, true),
  // fullDepth is unset, so a Device of this model has unknown depth unless
  // the Device records its own.
  deviceType('example-edge-1u', 'Example edge 1U', 'example-edge-1u', [T('eth0', 'eth0', 'NetworkPort', 'Rear')]),
  deviceType('example-leaf-48', 'Example leaf 48', 'example-leaf-48', leafTemplates, true),
  deviceType('example-panel-12', 'Example patch panel 12', 'example-panel-12', panelTemplates, false),
  deviceType('example-console-8', 'Example console server 8', 'example-console-8', [T('serial', 'Serial ports', 'ConsoleServerPort', 'Front', { positions: 8 }), T('psu-1', 'Power supply', 'PowerInlet', 'Rear')], false),
];

function device(name: string, displayName: string, type: string | undefined, components: Template[], placement: { position: number; height: number; face: RackFace }): RawDevice {
  return {
    metadata: { name, namespace: NS, creationTimestamp: '2026-11-01T00:00:00Z' },
    spec: {
      claimRef: { name: `${name}-install` },
      identity: { manufacturer: 'Example Vendor', model: type ?? 'example-appliance', serial: `SYNTH-${name.toUpperCase()}` },
      displayName,
      ...(type ? { deviceTypeRef: { name: type } } : {}),
      topologyAuthority: 'Cable',
      components: components.map(asComponent),
      reportedPlacement: { revision: 1, observedAt: '2026-11-01T00:00:00Z', reason: 'Synthetic survey', reference: 'ticket/synthetic-depth', rackRef: { name: DEPTH_RACK }, ...placement },
    },
  };
}

export const depthDevices: RawDevice[] = [
  // Full depth from the catalog default, chassis front to the rack front.
  device('compute-07', 'Example compute 07', 'example-compute-1u', computeTemplates, { position: 10, height: 1, face: 'front' }),
  // Reversed: full depth, chassis front (ports) toward the rack rear.
  device('leaf-01', 'Example leaf 01', 'example-leaf-48', leafTemplates, { position: 40, height: 1, face: 'rear' }),
  // Two half-depth panels share U42, one on each face.
  device('panel-01', 'Example patch panel 01', 'example-panel-12', panelTemplates, { position: 42, height: 1, face: 'front' }),
  device('panel-02', 'Example patch panel 02', 'example-panel-12', panelTemplates, { position: 42, height: 1, face: 'rear' }),
  // No DeviceType: depth unknown, drawn as full depth.
  device('appliance-03', 'Example appliance 03', undefined, [T('net0', 'net0', 'NetworkPort')], { position: 20, height: 2, face: 'front' }),
  // Half depth on the rear, overlapping the unknown-depth appliance.
  device('console-01', 'Example console server 01', 'example-console-8', [T('serial', 'Serial ports', 'ConsoleServerPort', 'Front', { positions: 8 }), T('psu-1', 'Power supply', 'PowerInlet', 'Rear')], { position: 21, height: 1, face: 'rear' }),
];

export const depthClaims: RawDeviceClaim[] = depthDevices.map(record => ({
  metadata: { name: record.spec.claimRef.name, namespace: NS, creationTimestamp: '2026-10-01T00:00:00Z' },
  spec: { lifecycle: 'Installation', displayName: `${record.spec.displayName} installation`, expectedIdentity: { manufacturer: 'Example Vendor', model: record.spec.identity.model }, placement: { scope: 'RackMembership', rackRef: { name: DEPTH_RACK } } },
}));

const D = (name: string, componentID: string, position = 1): CableEndpoint => ({ deviceRef: { name }, componentID, position });
const cable = (name: string, displayName: string, medium: string, a: CableEndpoint, b: CableEndpoint): PhysicalRecord => ({ kind: 'Cable', metadata: { name, namespace: NS }, spec: { displayName, medium, lanes: [{ id: 'l1', a, b }] } });

export const depthRecords: PhysicalRecord[] = [
  { kind: 'Rack', metadata: { name: DEPTH_RACK, namespace: NS }, spec: { displayName: 'Rack 02 · depth examples', facilityRef: { name: 'example-facility' }, heightUnits: 42 } },
  { kind: 'PowerFeed', metadata: { name: 'feed-b', namespace: NS }, spec: { displayName: 'Feed B', facilityRef: { name: 'example-facility' }, rackRef: { name: DEPTH_RACK }, supply: { type: 'AC', voltageV: 208, currentA: 30 }, terminations: [{ id: 'outlet-1', name: 'Outlet 1', type: 'PowerOutlet' }, { id: 'outlet-2', name: 'Outlet 2', type: 'PowerOutlet' }] } },
  ...depthCatalog,
  // Rear to rear: the reversed leaf's front ports face the rack rear.
  cable('dac-07-0', 'Compute 07 eth0 to leaf', 'Copper', D('compute-07', 'eth0'), D('leaf-01', 'swp1')),
  // Rear to front: a soft check note, not an error.
  cable('fiber-07-1', 'Compute 07 eth1 to panel', 'Fiber', D('compute-07', 'eth1'), D('panel-01', 'front', 3)),
  cable('pwr-07-1', 'Compute 07 power 1', 'Power', { powerFeedRef: { name: 'feed-b' }, componentID: 'outlet-1', position: 1 }, D('compute-07', 'psu-1')),
];
