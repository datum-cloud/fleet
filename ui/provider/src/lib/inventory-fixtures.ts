// Synthetic physical inventory for the local preview (VITE_FLEET_FIXTURES=true).
//
// Every name, serial, address and MAC here is invented: documentation-range
// addresses (192.0.2.0/24), the IANA documentation MAC block (00:00:5e:00:53),
// and `example-` vendors. Nothing describes a real facility.
//
// The dataset is one facility with three 42U racks and enough equipment to
// exercise each inventory view: rack elevation (front, rear, reversed,
// half-depth, overlapping, unpositioned and non-unit mounts), lifecycle
// states, and every connection-view state (recorded, two Cables on one port,
// missing peer, 4-lane breakout, PSU → PDU → feed trace, patch-panel
// pass-through and catalog-only components). The denied-peer state is a
// read-access scenario: see FIXTURE_SCENARIOS.
//
// Everything is typed against ../../../shared/fleet-api.ts, so a wire type
// change that the fixtures no longer satisfy fails `pnpm typecheck`.
//
// Depth and faces: templates go through `template()`, which sets each
// component's chassis face; Device placement goes through `placed()`, whose
// `face` is the mounted rack face; and DeviceType shape goes through
// `deviceType()`. Devices take their depth from the report when it records
// one, otherwise from the DeviceType. One model leaves fullDepth unset, so its
// Device's depth is unknown.

import type { Device, DeviceClaim, DeviceLifecycle, PhysicalComponent, PhysicalRecord, ReportedPlacement } from '../../../shared/fleet-api';
import { toInventory, type Inventory, type InventoryKind } from '../inventory';

const NS = 'default';
const CREATED = '2026-10-01T09:00:00Z';
const OBSERVED = '2026-11-02T14:30:00Z';

export const FIXTURE_FACILITY = 'example-dc1';
export const FIXTURE_RACKS = ['example-rack-a01', 'example-rack-a02', 'example-rack-a03'] as const;
type RackName = (typeof FIXTURE_RACKS)[number];

/** Stable record names the preview pages and screenshot list link to. */
export const FIXTURE_NAMES = {
  facility: FIXTURE_FACILITY,
  rack: 'example-rack-a01',
  server: 'example-srv-a01-01',
  leaf: 'example-leaf-a01',
  patchPanel: 'example-patch-a01',
  pdu: 'example-pdu-a01',
} as const;

const meta = (name: string, extra: Partial<PhysicalRecord['metadata']> = {}) => ({
  name, namespace: NS, uid: `synthetic-${name}`, resourceVersion: '1', creationTimestamp: CREATED, ...extra,
});

// ---- Catalog ----

type Template = NonNullable<PhysicalRecord['spec']['componentTemplates']>[number];
type Face = 'Front' | 'Rear';

/** One component template. `face` is the side of the chassis, not the rack. */
function template(id: string, name: string, type: PhysicalComponent['type'], connector: string, face: Face, positions?: number): Template {
  return { id, name, type, connector, face, ...(positions ? { positions } : {}) };
}

function range(count: number, start = 1) {
  return Array.from({ length: count }, (_, index) => index + start);
}

const manufacturer = (name: string, displayName: string): PhysicalRecord => ({
  kind: 'Manufacturer', metadata: meta(name), spec: { displayName, canonicalName: displayName, aliases: [name] },
});

function deviceType(name: string, displayName: string, maker: string, model: string, shape: { heightUnits: number; fullDepth?: boolean }, componentTemplates: Template[]): PhysicalRecord {
  return { kind: 'DeviceType', metadata: meta(name), spec: { displayName, manufacturerRef: { name: maker }, model, modelShape: shape, componentTemplates } };
}

const serverPorts = (network: number): Template[] => [
  template('psu-1', 'PSU 1', 'PowerInlet', 'IEC C14', 'Rear'),
  template('psu-2', 'PSU 2', 'PowerInlet', 'IEC C14', 'Rear'),
  ...range(network).map(index => template(`eth${index - 1}`, `Ethernet ${index - 1}`, 'NetworkPort', 'SFP28', 'Rear')),
  template('bmc', 'BMC', 'NetworkPort', 'RJ45', 'Rear'),
  template('console', 'Serial console', 'ConsolePort', 'RJ45', 'Rear'),
];

const TYPES = {
  server1U: deviceType('example-server-1u', 'Example 1U compute', 'example-compute', 'EX-C1', { heightUnits: 1, fullDepth: true }, serverPorts(2)),
  server2U: deviceType('example-server-2u', 'Example 2U storage', 'example-compute', 'EX-S2', { heightUnits: 2, fullDepth: true }, serverPorts(4)),
  leaf: deviceType('example-leaf-48x25-8x100', 'Example 48×25G + 8×100G leaf', 'example-networks', 'EX-L4856', { heightUnits: 1, fullDepth: true }, [
    ...range(48).map(index => template(`swp${index}`, `swp${index}`, 'NetworkPort', 'SFP28', 'Front')),
    // Each 100G port carries four lanes, so it can break out to 4×25G.
    ...range(8, 49).map(index => template(`swp${index}`, `swp${index}`, 'NetworkPort', 'QSFP28', 'Front', 4)),
    template('mgmt0', 'Management', 'NetworkPort', 'RJ45', 'Front'),
    template('console', 'Console', 'ConsolePort', 'RJ45', 'Front'),
    template('psu-1', 'PSU 1', 'PowerInlet', 'IEC C14', 'Rear'),
    template('psu-2', 'PSU 2', 'PowerInlet', 'IEC C14', 'Rear'),
  ]),
  mgmt: deviceType('example-mgmt-24x1g', 'Example 24×1G management switch', 'example-networks', 'EX-M24', { heightUnits: 1, fullDepth: false }, [
    ...range(24).map(index => template(`ge${index}`, `ge${index}`, 'NetworkPort', 'RJ45', 'Front')),
    template('psu-1', 'PSU 1', 'PowerInlet', 'IEC C14', 'Rear'),
  ]),
  patch: deviceType('example-patch-12', 'Example 12-port patch panel', 'example-cabling', 'EX-PP12', { heightUnits: 1, fullDepth: false }, [
    ...range(12).map(index => template(`front-${index}`, `Front ${index}`, 'FrontPort', 'RJ45', 'Front')),
    ...range(12).map(index => template(`rear-${index}`, `Rear ${index}`, 'RearPort', 'RJ45 keystone', 'Rear')),
  ]),
  // fullDepth is unset: depth unknown, drawn as full depth and labelled.
  appliance: deviceType('example-appliance-2u', 'Example 2U appliance', 'example-compute', 'EX-A2', { heightUnits: 2 }, [
    template('net0', 'net0', 'NetworkPort', 'SFP28', 'Front'),
    template('psu-1', 'PSU 1', 'PowerInlet', 'IEC C14', 'Rear'),
  ]),
  pdu: deviceType('example-pdu-8', 'Example 8-outlet PDU', 'example-power', 'EX-PDU8', { heightUnits: 0, fullDepth: false }, [
    template('inlet', 'Inlet', 'PowerInlet', 'IEC C20', 'Rear'),
    ...range(8).map(index => template(`outlet-${index}`, `Outlet ${index}`, 'PowerOutlet', 'IEC C13', 'Rear')),
  ]),
};

const catalog: PhysicalRecord[] = [
  manufacturer('example-compute', 'Example Compute Co.'),
  manufacturer('example-networks', 'Example Networks'),
  manufacturer('example-cabling', 'Example Cabling'),
  manufacturer('example-power', 'Example Power'),
  ...Object.values(TYPES),
];

// ---- Locations and power ----

const locations: PhysicalRecord[] = [
  { kind: 'Site', metadata: meta('example-site-one'), spec: { displayName: 'Example operational site', code: 'EXS1', facilityRefs: [{ name: FIXTURE_FACILITY }] } },
  { kind: 'Facility', metadata: meta(FIXTURE_FACILITY), spec: { displayName: 'Example Facility One', locationRef: { name: 'example-region' }, identifiers: [{ provider: 'example-colo', type: 'PremisesCode', value: 'EXDC1' }] } },
  ...FIXTURE_RACKS.map((name, index): PhysicalRecord => ({
    kind: 'Rack', metadata: meta(name), spec: { displayName: `Rack A0${index + 1}`, facilityRef: { name: FIXTURE_FACILITY }, heightUnits: 42 },
  })),
];

const feed = (name: string, displayName: string): PhysicalRecord => ({
  kind: 'PowerFeed', metadata: meta(name),
  spec: {
    displayName, facilityRef: { name: FIXTURE_FACILITY }, rackRef: { name: 'example-rack-a01' },
    supply: { type: 'AC', voltageV: 208, currentA: 30, phase: 'Single' },
    terminations: range(4).map(index => ({ id: `outlet-${index}`, name: `Outlet ${index}`, type: 'PowerOutlet', connector: 'IEC C19', positions: 1 })),
    upstreamSources: [{ system: 'example-colo', type: 'Circuit', id: `synthetic-${name}` }],
  },
});

const feeds: PhysicalRecord[] = [feed('example-feed-a', 'Feed A'), feed('example-feed-b', 'Feed B')];

// ---- Devices ----

type Placement = Omit<ReportedPlacement, 'revision' | 'observedAt' | 'reason' | 'reference' | 'rackRef' | 'facilityRef'> & { rack?: RackName };

/**
 * A reported placement. `face` is the mounted rack face, the face the
 * chassis front points toward: `rear` on a full-depth Device means it is
 * mounted reversed.
 */
function placed({ rack, ...placement }: Placement): ReportedPlacement {
  return {
    revision: 1, observedAt: OBSERVED, reason: 'Synthetic install survey', reference: 'example-survey-001',
    facilityRef: { name: FIXTURE_FACILITY }, ...(rack ? { rackRef: { name: rack } } : {}), ...placement,
  };
}

let serial = 0;
function components(type: PhysicalRecord, omit: string[] = []): PhysicalComponent[] {
  return (type.spec.componentTemplates ?? []).filter(item => !omit.includes(item.id)).map(item => ({ ...item }));
}

interface DeviceInput {
  name: string;
  displayName: string;
  type: PhysicalRecord;
  placement?: ReportedPlacement;
  claim?: string;
  lifecycle?: DeviceLifecycle;
  /** Template components this Device's record leaves out (shown as catalog only). */
  omit?: string[];
  extra?: Partial<Device['spec']>;
}

function device({ name, displayName, type, placement, claim, lifecycle, omit, extra }: DeviceInput): Device {
  serial++;
  const index = String(serial).padStart(2, '0');
  return {
    kind: 'Device', metadata: meta(name, { creationTimestamp: `2026-10-${index}T10:00:00Z` }),
    spec: {
      displayName,
      claimRef: { name: claim ?? `${name}-install` },
      deviceTypeRef: { name: type.metadata.name },
      identity: { manufacturer: type.spec.manufacturerRef?.name, model: type.spec.model ?? 'unknown', serial: `SYNTH-EX-${index}`, systemUUID: `00000000-0000-4000-8000-0000000000${index}` },
      assetTag: `EX-ASSET-${index}`,
      topologyAuthority: 'Cable',
      components: components(type, omit),
      ...(placement ? { reportedPlacement: placement } : {}),
      ...(lifecycle ? { lifecycle } : {}),
      ...extra,
    },
  };
}

const server = (name: string, rack: RackName, position: number, extra: Partial<DeviceInput> = {}) => device({
  name, displayName: `Compute ${name.split("-").slice(-2).join("-")}`, type: TYPES.server1U,
  // Depth comes from the DeviceType.
  placement: placed({ rack, position, height: 1, face: 'front' }),
  extra: { bmc: { endpoint: `https://192.0.2.${10 + serial}:443` } }, ...extra,
});

const storage = (name: string, rack: RackName, position: number, extra: Partial<DeviceInput> = {}) => device({
  name, displayName: `Storage ${name.split("-").slice(-2).join("-")}`, type: TYPES.server2U,
  placement: placed({ rack, position, height: 2, face: 'front' }), ...extra,
});

const devices: Device[] = [
  // Rack A01: the fully described rack.
  server(FIXTURE_NAMES.server, 'example-rack-a01', 2, {
    displayName: 'Compute a01-01',
    // The record omits the console port, so it appears as catalog only.
    omit: ['console'],
    extra: {
      bmc: { endpoint: 'https://192.0.2.11:443' },
      interfaces: [{ name: 'eth0', macAddress: '00:00:5e:00:53:11' }, { name: 'eth1', macAddress: '00:00:5e:00:53:12' }],
      providerHostRef: { system: 'example-provider', type: 'Host', id: 'synthetic-host-0001' },
    },
  }),
  server('example-srv-a01-02', 'example-rack-a01', 3),
  storage('example-sto-a01-03', 'example-rack-a01', 5),
  storage('example-sto-a01-04', 'example-rack-a01', 7),
  device({
    name: FIXTURE_NAMES.leaf, displayName: 'Leaf a01', type: TYPES.leaf,
    // Mounted reversed: ports face the rear (hot) aisle.
    placement: placed({ rack: 'example-rack-a01', position: 40, height: 1, face: 'rear', fullDepth: true }),
  }),
  device({
    name: 'example-mgmt-a01', displayName: 'Management a01', type: TYPES.mgmt,
    placement: placed({ rack: 'example-rack-a01', position: 41, height: 1, face: 'rear', fullDepth: false }),
  }),
  device({
    name: FIXTURE_NAMES.patchPanel, displayName: 'Patch panel a01', type: TYPES.patch,
    // Half-depth on the rear rails; the front of U42 stays unrecorded.
    placement: placed({ rack: 'example-rack-a01', position: 42, height: 1, face: 'rear', fullDepth: false }),
    extra: { internalMappings: range(12).map(index => ({ from: { componentID: `front-${index}`, position: 1 }, to: { componentID: `rear-${index}`, position: 1 } })) },
  }),
  device({
    name: 'example-app-a01', displayName: 'Appliance a01 (depth unknown)', type: TYPES.appliance,
    placement: placed({ rack: 'example-rack-a01', position: 20, height: 2, face: 'front' }),
  }),
  device({
    name: FIXTURE_NAMES.pdu, displayName: 'PDU a01 (rear left)', type: TYPES.pdu,
    placement: placed({ rack: 'example-rack-a01', mount: 'Rear left vertical' }),
    extra: { internalMappings: range(8).map(index => ({ from: { componentID: `outlet-${index}`, position: 1 }, to: { componentID: 'inlet', position: 1 } })) },
  }),

  // Rack A02: two recorded placements overlap at U21.
  storage('example-sto-a02-01', 'example-rack-a02', 20),
  server('example-srv-a02-02', 'example-rack-a02', 21),
  server('example-srv-a02-03', 'example-rack-a02', 30),

  // Rack A03: partial and lifecycle records.
  device({
    name: 'example-srv-a03-01', displayName: 'Compute a03-01 (position unknown)', type: TYPES.server1U,
    placement: placed({ rack: 'example-rack-a03', height: 1 }),
  }),
  server('example-srv-a03-02', 'example-rack-a03', 10, { lifecycle: 'Decommissioning', displayName: 'Compute a03-02 (decommissioning)' }),
  device({
    name: 'example-srv-retired-01', displayName: 'Compute retired-01', type: TYPES.server1U, lifecycle: 'Retired',
    // Facility only: it left the rack before being retired.
    placement: placed({}),
  }),
  // No reported placement and no matching claim.
  device({ name: 'example-srv-unplaced-01', displayName: 'Compute unplaced-01', type: TYPES.server1U, claim: 'example-claim-not-recorded' }),
];

// Claims for the racked equipment. Devices with a reported placement use it;
// the claim is the installation plan and history.
function claim(name: string, rack: RackName, position: number, height: number, face: 'front' | 'rear', model: string): DeviceClaim {
  return {
    kind: 'DeviceClaim', metadata: meta(name),
    spec: {
      displayName: `Installation · ${rack.slice(-3).toUpperCase()} U${position}`,
      expectedIdentity: { manufacturer: 'example-compute', model },
      placement: { scope: 'RackUnits', facilityRef: { name: FIXTURE_FACILITY }, rackRef: { name: rack }, position, height, face },
    },
  };
}

const claims: DeviceClaim[] = [
  ...devices.flatMap(item => {
    const report = item.spec.reportedPlacement;
    if (!report?.rackRef || report.position === undefined || item.spec.claimRef.name !== `${item.metadata.name}-install`) return [];
    return [claim(item.spec.claimRef.name, report.rackRef.name as RackName, report.position, report.height ?? 1, report.face ?? 'front', item.spec.identity.model)];
  }),
  // A planned installation with no Device registered yet.
  claim('example-plan-a02-u35', 'example-rack-a02', 35, 1, 'front', 'EX-C1'),
];

// ---- Cables ----

type End = { deviceRef: { name: string }; componentID: string; position: number } | { powerFeedRef: { name: string }; componentID: string; position: number };
const D = (name: string, componentID: string, position = 1): End => ({ deviceRef: { name }, componentID, position });
const F = (name: string, componentID: string, position = 1): End => ({ powerFeedRef: { name }, componentID, position });

function cable(name: string, displayName: string, medium: string, lanes: [string, End, End][]): PhysicalRecord {
  return { kind: 'Cable', metadata: meta(name), spec: { displayName, medium, lanes: lanes.map(([id, a, b]) => ({ id, a, b })) } };
}
const one = (name: string, displayName: string, medium: string, a: End, b: End) => cable(name, displayName, medium, [['1', a, b]]);

const SRV = FIXTURE_NAMES.server;
const LEAF = FIXTURE_NAMES.leaf;
const PATCH = FIXTURE_NAMES.patchPanel;
const PDU = FIXTURE_NAMES.pdu;

const cables: PhysicalRecord[] = [
  // Recorded data links.
  one('example-dac-a01-01-eth0', 'DAC a01-01 eth0', 'Copper DAC', D(SRV, 'eth0'), D(LEAF, 'swp1')),
  one('example-dac-a01-02-eth0', 'DAC a01-02 eth0', 'Copper DAC', D('example-srv-a01-02', 'eth0'), D(LEAF, 'swp3')),
  one('example-dac-a02-03-eth0', 'DAC a02-03 eth0', 'Copper DAC', D('example-srv-a02-03', 'eth0'), D(LEAF, 'swp5')),
  // Front-facing appliance port to a rear-facing leaf port: a soft check.
  one('example-dac-app-a01-net0', 'DAC appliance a01 net0', 'Copper DAC', D('example-app-a01', 'net0'), D(LEAF, 'swp6')),
  // Two Cables claim the same server port: records disagree.
  one('example-dac-a01-01-eth1', 'DAC a01-01 eth1', 'Copper DAC', D(SRV, 'eth1'), D(LEAF, 'swp2')),
  one('example-dac-a01-01-eth1-dup', 'DAC a01-01 eth1 (duplicate record)', 'Copper DAC', D(SRV, 'eth1'), D(LEAF, 'swp4')),
  // The peer Device has no record: peer unavailable.
  one('example-dac-a01-02-eth1', 'DAC a01-02 eth1', 'Copper DAC', D('example-srv-a01-02', 'eth1'), D('example-leaf-legacy', 'swp7')),
  // A 4-lane breakout from one 100G leaf port to four servers.
  cable('example-brk-swp49', 'Breakout swp49', 'Copper DAC breakout 1×100G to 4×25G', [
    ['1', D(LEAF, 'swp49', 1), D('example-sto-a01-03', 'eth0')],
    ['2', D(LEAF, 'swp49', 2), D('example-sto-a01-04', 'eth0')],
    ['3', D(LEAF, 'swp49', 3), D('example-sto-a02-01', 'eth0')],
    ['4', D(LEAF, 'swp49', 4), D('example-srv-a02-02', 'eth0')],
  ]),
  // Management through the patch panel: server BMC → front port, rear port → management switch.
  one('example-cat6-a01-01-bmc', 'Cat6 a01-01 BMC', 'Cat6', D(SRV, 'bmc'), D(PATCH, 'front-1')),
  one('example-cat6-a01-02-bmc', 'Cat6 a01-02 BMC', 'Cat6', D('example-srv-a01-02', 'bmc'), D(PATCH, 'front-2')),
  one('example-cat6-patch-rear-1', 'Cat6 patch rear 1', 'Cat6', D(PATCH, 'rear-1'), D('example-mgmt-a01', 'ge1')),
  one('example-cat6-patch-rear-2', 'Cat6 patch rear 2', 'Cat6', D(PATCH, 'rear-2'), D('example-mgmt-a01', 'ge2')),
  one('example-cat6-leaf-mgmt', 'Cat6 leaf management', 'Cat6', D(LEAF, 'mgmt0'), D('example-mgmt-a01', 'ge24')),
  // Power: PSU → PDU outlet, PDU inlet → feed, and one PSU straight to a feed.
  one('example-pwr-a01-01-psu1', 'Power a01-01 PSU 1', 'Power', D(SRV, 'psu-1'), D(PDU, 'outlet-1')),
  one('example-pwr-a01-01-psu2', 'Power a01-01 PSU 2', 'Power', D(SRV, 'psu-2'), F('example-feed-b', 'outlet-1')),
  one('example-pwr-a01-02-psu1', 'Power a01-02 PSU 1', 'Power', D('example-srv-a01-02', 'psu-1'), D(PDU, 'outlet-2')),
  one('example-pwr-leaf-psu1', 'Power leaf PSU 1', 'Power', D(LEAF, 'psu-1'), D(PDU, 'outlet-3')),
  one('example-pwr-leaf-psu2', 'Power leaf PSU 2', 'Power', D(LEAF, 'psu-2'), F('example-feed-b', 'outlet-2')),
  one('example-pwr-pdu-inlet', 'Power PDU inlet', 'Power', D(PDU, 'inlet'), F('example-feed-a', 'outlet-1')),
];

/** The complete synthetic dataset, as the API would return it. */
export const inventoryFixture: { claims: DeviceClaim[]; devices: Device[]; physical: PhysicalRecord[] } = {
  claims,
  devices,
  physical: [...locations, ...catalog, ...feeds, ...cables],
};

/**
 * Read-access scenarios, selected with `?fixture=<name>` on the preview URL.
 * Each lists collections that answer 403, so views show their unavailable
 * states. The selection is read once per page load.
 */
export const FIXTURE_SCENARIOS: Record<string, InventoryKind[]> = {
  default: [],
  'power-feeds-denied': ['PowerFeed'],
  'devices-denied': ['Device'],
  'cables-denied': ['Cable'],
};

export function fixtureInventory(scenario: string | null | undefined = 'default'): Inventory {
  const denied = new Set(FIXTURE_SCENARIOS[scenario ?? 'default'] ?? []);
  const issues: Inventory['issues'] = Object.fromEntries([...denied].map(kind => [kind, { status: 403 }]));
  const inventory = toInventory(denied.has('DeviceClaim') ? [] : claims, denied.has('Device') ? [] : devices, inventoryFixture.physical.filter(record => !denied.has(record.kind)));
  return denied.size ? { ...inventory, issues } : inventory;
}
