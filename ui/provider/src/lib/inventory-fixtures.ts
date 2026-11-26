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
// missing peer, 4-lane breakout, PSU → PDU → feed trace, a server whose
// PSUs both reach one feed, patch-panel pass-through and catalog-only
// components). The denied-peer state is a
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

import type { Condition } from '../../../shared/conditions';
import type { Device, DeviceClaim, DeviceStatus, DeviceLifecycle, PhysicalComponent, PhysicalRecord, ReportedPlacement } from '../../../shared/fleet-api';
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
  imported: 'example-imported-sw-01',
  /** Both PSUs reach Feed A through the same PDU: "Single feed (recorded)". */
  singleFeedServer: 'example-srv-a01-05',
} as const;

const meta = (name: string, extra: Partial<PhysicalRecord['metadata']> = {}) => ({
  name, namespace: NS, uid: `synthetic-${name}`, resourceVersion: '1', creationTimestamp: CREATED, ...extra,
});

// ---- Catalog ----

type Template = NonNullable<PhysicalRecord['spec']['componentTemplates']>[number];
type Face = 'Front' | 'Rear';

/** One component template. `face` is the side of the chassis, not the rack. */
function template(id: string, name: string, type: PhysicalComponent['type'], connector: string, face: Face, positions?: number, layout?: Template['layout']): Template {
  return { id, name, type, connector, face, ...(positions ? { positions } : {}), ...(layout ? { layout } : {}) };
}

/** Lays templates into a bank: row-major, or odd-top/even-bottom as switches number ports. */
function laid(templates: Template[], bank: string, rows: number, order: 'row' | 'odd-top' = 'row', start = 0): Template[] {
  return templates.map((item, index) => {
    const at = index + start;
    const columns = Math.ceil((templates.length + start) / rows);
    const layout = order === 'odd-top' ? { bank, row: (at % 2) + 1, column: Math.floor(at / 2) + 1 } : { bank, row: Math.floor(at / columns) + 1, column: (at % columns) + 1 };
    return { ...item, layout };
  });
}

type Bank = NonNullable<PhysicalRecord['spec']['faceplate']>['banks'][number];
const bank = (id: string, face: Face, rows: number, columns: number, extra: Partial<Bank> = {}): Bank => ({ id, face, rows, columns, ...extra });

function range(count: number, start = 1) {
  return Array.from({ length: count }, (_, index) => index + start);
}

const manufacturer = (name: string, displayName: string): PhysicalRecord => ({
  kind: 'Manufacturer', metadata: meta(name), spec: { displayName, canonicalName: displayName, aliases: [name] },
});

function deviceType(name: string, displayName: string, maker: string, model: string, shape: NonNullable<PhysicalRecord['spec']['modelShape']>, componentTemplates: Template[], banks?: Bank[]): PhysicalRecord {
  return { kind: 'DeviceType', metadata: meta(name), spec: { displayName, manufacturerRef: { name: maker }, model, modelShape: shape, componentTemplates, ...(banks ? { faceplate: { banks } } : {}) } };
}

// Faceplate layouts are synthetic but shaped like real hardware: PSUs at the
// rear edges, ganged SFP cages numbered odd-top, a QSFP block, drive bays.
// Module cells (PSUs, bays, fans) record their size in millimetres.
const PSU = { width: 40, height: 38 };

// A 1U server. Front: eight 2.5in bays. Rear: PSUs at the edges, two SFP28,
// the BMC, three accessory ports. The console has no layout, so it is drawn
// in the "Not on the faceplate" tray.
const server1UTemplates: Template[] = [
  ...laid(range(8).map(index => template(`bay-${index}`, `Bay ${index}`, 'Module', '2.5in drive bay', 'Front')), 'bays', 1),
  template('psu-1', 'PSU 1', 'PowerInlet', 'IEC C14', 'Rear', undefined, { bank: 'psu-1', row: 1, column: 1 }),
  template('psu-2', 'PSU 2', 'PowerInlet', 'IEC C14', 'Rear', undefined, { bank: 'psu-2', row: 1, column: 1 }),
  ...laid(range(2).map(index => template(`eth${index - 1}`, `Ethernet ${index - 1}`, 'NetworkPort', 'SFP28', 'Rear')), 'ocp', 1),
  template('bmc', 'BMC', 'NetworkPort', 'RJ45', 'Rear', undefined, { bank: 'mgmt', row: 1, column: 1 }),
  ...laid([template('usb-1', 'USB 1', 'InventoryItem', 'USB-A', 'Rear'), template('usb-2', 'USB 2', 'InventoryItem', 'USB-A', 'Rear'), template('vga', 'VGA', 'InventoryItem', 'VGA', 'Rear')], 'io', 1),
  template('console', 'Serial console', 'ConsolePort', 'RJ45', 'Rear'),
];
const server1UBanks: Bank[] = [
  bank('bays', 'Front', 1, 8, { cellMM: { width: 46, height: 14 } }),
  bank('psu-1', 'Rear', 1, 1, { offset: 0, cellMM: PSU }),
  bank('ocp', 'Rear', 1, 2, { offset: 0.14 }),
  bank('mgmt', 'Rear', 1, 1, { offset: 0.24 }),
  bank('io', 'Rear', 1, 3, { offset: 0.33 }),
  bank('psu-2', 'Rear', 1, 1, { offset: 0.9, cellMM: PSU }),
];

// A 2U storage server. Front: twelve 3.5in bays in three rows. Rear: stacked
// PSUs, four SFP28, BMC and console, two boot bays.
const server2UTemplates: Template[] = [
  ...laid(range(12).map(index => template(`bay-${index}`, `Bay ${index}`, 'Module', '3.5in drive bay', 'Front')), 'bays', 3),
  ...laid([template('psu-1', 'PSU 1', 'PowerInlet', 'IEC C14', 'Rear'), template('psu-2', 'PSU 2', 'PowerInlet', 'IEC C14', 'Rear')], 'psu', 2),
  ...laid(range(4).map(index => template(`eth${index - 1}`, `Ethernet ${index - 1}`, 'NetworkPort', 'SFP28', 'Rear')), 'nic', 1),
  ...laid([template('bmc', 'BMC', 'NetworkPort', 'RJ45', 'Rear'), template('console', 'Serial console', 'ConsolePort', 'RJ45', 'Rear')], 'mgmt', 1),
  ...laid(range(2).map(index => template(`boot-${index}`, `Boot ${index}`, 'Module', 'M.2 boot bay', 'Rear')), 'boot', 2),
];
const server2UBanks: Bank[] = [
  bank('bays', 'Front', 3, 4, { cellMM: { width: 100, height: 26 } }),
  bank('psu', 'Rear', 2, 1, { offset: 0, cellMM: PSU }),
  bank('nic', 'Rear', 1, 4, { offset: 0.2 }),
  bank('mgmt', 'Rear', 1, 2, { offset: 0.37 }),
  bank('boot', 'Rear', 2, 1, { offset: 0.86, cellMM: { width: 30, height: 14 } }),
];

const TYPES = {
  server1U: deviceType('example-server-1u', 'Example 1U compute', 'example-compute', 'EX-C1', { heightUnits: 1, fullDepth: true, width: 'Full' }, server1UTemplates, server1UBanks),
  server2U: deviceType('example-server-2u', 'Example 2U storage', 'example-compute', 'EX-S2', { heightUnits: 2, fullDepth: true, width: 'Full' }, server2UTemplates, server2UBanks),
  // 48 SFP28 in two 2×12 blocks, 8 QSFP28 in a 2×4 block, then management
  // and console stacked; PSUs and fans at the rear.
  leaf: deviceType('example-leaf-48x25-8x100', 'Example 48×25G + 8×100G leaf', 'example-networks', 'EX-L4856', { heightUnits: 1, fullDepth: true, width: 'Full' }, [
    ...laid(range(24).map(index => template(`swp${index}`, `swp${index}`, 'NetworkPort', 'SFP28', 'Front')), 'sfp-a', 2, 'odd-top'),
    ...laid(range(24, 25).map(index => template(`swp${index}`, `swp${index}`, 'NetworkPort', 'SFP28', 'Front')), 'sfp-b', 2, 'odd-top'),
    // Each 100G port carries four lanes, so it can break out to 4×25G.
    ...laid(range(8, 49).map(index => template(`swp${index}`, `swp${index}`, 'NetworkPort', 'QSFP28', 'Front', 4)), 'qsfp', 2, 'odd-top'),
    ...laid([template('mgmt0', 'Management', 'NetworkPort', 'RJ45', 'Front'), template('console', 'Console', 'ConsolePort', 'RJ45', 'Front')], 'oob', 2),
    template('psu-1', 'PSU 1', 'PowerInlet', 'IEC C14', 'Rear', undefined, { bank: 'psu-1', row: 1, column: 1 }),
    ...laid(range(4).map(index => template(`fan-${index}`, `Fan ${index}`, 'Module', 'Fan tray', 'Rear')), 'fans', 1),
    template('psu-2', 'PSU 2', 'PowerInlet', 'IEC C14', 'Rear', undefined, { bank: 'psu-2', row: 1, column: 1 }),
  ], [
    bank('sfp-a', 'Front', 2, 12, { offset: 0 }),
    bank('sfp-b', 'Front', 2, 12, { offset: 0.39 }),
    bank('qsfp', 'Front', 2, 4, { offset: 0.78 }),
    bank('oob', 'Front', 2, 1, { offset: 0.96 }),
    bank('psu-1', 'Rear', 1, 1, { offset: 0, cellMM: PSU }),
    bank('fans', 'Rear', 1, 4, { offset: 0.3, cellMM: { width: 34, height: 34 } }),
    bank('psu-2', 'Rear', 1, 1, { offset: 0.9, cellMM: PSU }),
  ]),
  mgmt: deviceType('example-mgmt-24x1g', 'Example 24×1G management switch', 'example-networks', 'EX-M24', { heightUnits: 1, fullDepth: false }, [
    ...laid(range(24).map(index => template(`ge${index}`, `ge${index}`, 'NetworkPort', 'RJ45', 'Front')), 'rj45', 2, 'odd-top'),
    template('psu-1', 'PSU 1', 'PowerInlet', 'IEC C14', 'Rear', undefined, { bank: 'psu', row: 1, column: 1 }),
  ], [bank('rj45', 'Front', 2, 12), bank('psu', 'Rear', 1, 1, { cellMM: PSU })]),
  // 24 RJ45 jacks at the front, 24 keystones at the rear, mapped one to one.
  patch: deviceType('example-patch-24', 'Example 24-port patch panel', 'example-cabling', 'EX-PP24', { heightUnits: 1, fullDepth: false, width: 'Full' }, [
    ...laid(range(24).map(index => template(`front-${index}`, `Front ${index}`, 'FrontPort', 'RJ45', 'Front')), 'jacks', 1),
    ...laid(range(24).map(index => template(`rear-${index}`, `Rear ${index}`, 'RearPort', 'RJ45 keystone', 'Rear')), 'keystones', 1),
  ], [bank('jacks', 'Front', 1, 24), bank('keystones', 'Rear', 1, 24)]),
  // No layout: shown as grouped tiles labelled "Layout not recorded".
  // fullDepth is unset: depth unknown, drawn as full depth and labelled.
  appliance: deviceType('example-appliance-2u', 'Example 2U appliance', 'example-compute', 'EX-A2', { heightUnits: 2 }, [
    template('net0', 'net0', 'NetworkPort', 'SFP28', 'Front'),
    template('psu-1', 'PSU 1', 'PowerInlet', 'IEC C14', 'Rear'),
  ]),
  // A zero-U PDU: drawn as a one-unit strip, because its length is not recorded.
  pdu: deviceType('example-pdu-8', 'Example 8-outlet PDU', 'example-power', 'EX-PDU8', { heightUnits: 0, fullDepth: false }, [
    template('inlet', 'Inlet', 'PowerInlet', 'IEC C20', 'Rear', undefined, { bank: 'inlet', row: 1, column: 1 }),
    ...laid(range(8).map(index => template(`outlet-${index}`, `Outlet ${index}`, 'PowerOutlet', 'IEC C13', 'Rear')), 'outlets', 1),
  ], [bank('inlet', 'Rear', 1, 1, { offset: 0.02 }), bank('outlets', 'Rear', 1, 8, { offset: 0.2 })]),
  // Shaped like an importer's output: template ids are template-instance ids
  // (`interface-templates-101`) and connectors are NetBox interface type
  // strings. Its Device's components carry different ids, so they are drawn
  // by name and type. The two PSU templates share one name, so neither PSU
  // can be matched by name: both go to the tray. The console's connector is
  // blank, so it is drawn as Other.
  imported: deviceType('example-imported-switch-24', 'Example imported 24-port switch', 'example-networks', 'EX-IMP24', { heightUnits: 1, fullDepth: true, width: 'Full' }, [
    ...laid(range(16).map(index => template(`interface-templates-${100 + index}`, `Ethernet${index}`, 'NetworkPort', '10gbase-t', 'Front')), 'copper', 2, 'odd-top'),
    ...laid(range(4, 17).map(index => template(`interface-templates-${100 + index}`, `Ethernet${index}`, 'NetworkPort', '25gbase-x-sfp28', 'Front')), 'sfp', 2, 'odd-top'),
    ...laid(range(2, 21).map(index => template(`interface-templates-${100 + index}`, `Ethernet${index}`, 'NetworkPort', '200gbase-x-qsfp56', 'Front')), 'qsfp', 1),
    ...laid([template('interface-templates-123', 'Management1', 'NetworkPort', '100base-tx', 'Front'), template('console-port-templates-201', 'Console', 'ConsolePort', '', 'Front')], 'oob', 2),
    template('power-port-templates-301', 'PSU', 'PowerInlet', 'iec-60320-c14', 'Rear', undefined, { bank: 'psu-1', row: 1, column: 1 }),
    template('power-port-templates-302', 'PSU', 'PowerInlet', 'iec-60320-c14', 'Rear', undefined, { bank: 'psu-2', row: 1, column: 1 }),
  ], [
    bank('copper', 'Front', 2, 8, { offset: 0.02 }),
    bank('sfp', 'Front', 2, 2, { offset: 0.4 }),
    bank('qsfp', 'Front', 1, 2, { offset: 0.56 }),
    bank('oob', 'Front', 2, 1, { offset: 0.94 }),
    bank('psu-1', 'Rear', 1, 1, { offset: 0, cellMM: PSU }),
    bank('psu-2', 'Rear', 1, 1, { offset: 0.9, cellMM: PSU }),
  ]),
};

/**
 * The imported switch's components: the same names, types and connectors as
 * its templates, under importer instance ids (`interfaces-9101`) that match
 * no template id.
 */
const importedComponents: PhysicalComponent[] = (TYPES.imported.spec.componentTemplates ?? []).map((item, index) => {
  const kind = item.type === 'NetworkPort' ? 'interfaces' : item.type === 'ConsolePort' ? 'console-ports' : 'power-ports';
  return { id: `${kind}-${9101 + index}`, name: item.name, type: item.type, connector: item.connector };
});

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
    // The record omits the console port, two drive bays and a USB port, so they appear as catalog only.
    omit: ['console', 'bay-7', 'bay-8', 'usb-2'],
    extra: {
      bmc: { endpoint: 'https://192.0.2.11:443' },
      interfaces: [{ name: 'eth0', macAddress: '00:00:5e:00:53:11' }, { name: 'eth1', macAddress: '00:00:5e:00:53:12' }],
      providerHostRef: { system: 'example-provider', type: 'Host', id: 'synthetic-host-0001' },
    },
  }),
  server('example-srv-a01-02', 'example-rack-a01', 3, {
    extra: {
      bmc: { endpoint: 'https://192.0.2.100:443' },
      // eth1 records a MAC no host NIC reports; eth2 is not in the component list.
      interfaces: [{ name: 'eth0', macAddress: '00:00:5e:00:53:21' }, { name: 'eth1', macAddress: '00:00:5e:00:53:22' }, { name: 'eth2', macAddress: '00:00:5e:00:53:23' }],
    },
  }),
  // Both PSUs are cabled to the same PDU, so both reach Feed A: a single feed in the records.
  server(FIXTURE_NAMES.singleFeedServer, 'example-rack-a01', 4, { displayName: 'Compute a01-05' }),
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
  // Imported records: component ids differ from template ids. In the
  // facility, not a rack, so the elevations are unchanged.
  device({
    name: FIXTURE_NAMES.imported, displayName: 'Imported switch 01', type: TYPES.imported,
    placement: placed({}), extra: { components: importedComponents },
  }),
];

// ---- BMC discovery status ----
//
// Synthetic Device.status as BMC discovery writes it, relative to page load
// so ages and certificate expiry read the same on every capture:
//   example-srv-a01-01  matched, powered on, every NIC up, key approved
//   example-srv-a01-02  IdentityMatched False: the System reports another serial
//   example-srv-a01-05  key not approved (PinNotApproved), certificate expiring
//                       soon, one host NIC down; inventory retained from an
//                       earlier read
// Every other Device has no status: discovery has not run.

const LOADED = Date.now();
const at = (offsetMs: number) => new Date(LOADED + offsetMs).toISOString().replace(/\.\d{3}Z$/, 'Z');
const MINUTE = 60_000;
const DAY = 86_400_000;
const condition = (type: string, status: Condition['status'], reason: string, message: string): Condition => ({ type, status, reason, message, observedGeneration: 1, lastTransitionTime: at(-2 * MINUTE) });
const nic = (id: string, macAddress: string, linkStatus = 'LinkUp', speedMbps = 25000) => ({ id, macAddress, linkStatus, speedMbps });
const key = (byte: string) => byte.repeat(32);

const RESET_TYPES = ['On', 'ForceOff', 'GracefulShutdown', 'ForceRestart', 'PowerCycle', 'Nmi'];

function discovery(device: Device, { serial, key: publicKeySHA256, notAfter, nics, managerNic, conditions, resetTypes = RESET_TYPES }: { serial: string; key: string; notAfter: string; nics: ReturnType<typeof nic>[]; managerNic: ReturnType<typeof nic>; conditions: Condition[]; resetTypes?: string[] }): DeviceStatus {
  const endpoint = device.spec.bmc!.endpoint;
  return {
    observedGeneration: 1,
    discovery: {
      lastAttemptAt: at(-2 * MINUTE), lastAttemptEndpoint: endpoint,
      tls: { observedAt: at(-2 * MINUTE), endpoint, publicKeySHA256, notAfter, selfSigned: true },
      serviceRoot: { observedAt: at(-2 * MINUTE), endpoint, redfishVersion: '1.11.0', uuid: device.spec.identity.systemUUID, vendor: 'Example Compute', product: 'Example Integrated BMC' },
      inventory: {
        observedAt: at(-2 * MINUTE), endpoint,
        managers: [{ id: 'BMC', model: 'Example BMC 9', firmwareVersion: '7.10.30', networkInterfaces: [managerNic] }],
        systems: [{
          id: 'System.Embedded.1', uuid: device.spec.identity.systemUUID, manufacturer: 'Example Compute', model: `Example ${device.spec.identity.model}`,
          serialNumber: serial, sku: 'SYNTH-SKU-1', powerState: 'On', networkInterfaces: nics, resetTypes,
        }],
      },
    },
    conditions,
  };
}

const reachable = condition('Reachable', 'True', 'ServiceRootRead', 'TLS handshake completed and the anonymous service root was read');
const accepted = condition('Authenticated', 'True', 'CredentialAccepted', 'the BMC accepted the credential over the pinned connection');
const read = condition('Discovered', 'True', 'InventoryRead', 'managers, systems and their Ethernet interfaces were read');
const unique = condition('PublicKeyUnique', 'True', 'Unique', 'no other discovered Device presents this TLS public key');
const matched = condition('IdentityMatched', 'True', 'Matched', 'System System.Embedded.1 matches spec.identity (systemUUID, serial, model)');

const DISCOVERED: Record<string, (device: Device) => DeviceStatus> = {
  [FIXTURE_NAMES.server]: device => discovery(device, {
    serial: device.spec.identity.serial!, key: key('3f'), notAfter: at(400 * DAY),
    nics: [nic('EthernetInterface0', '00:00:5e:00:53:11'), nic('EthernetInterface1', '00:00:5e:00:53:12'), nic('EthernetInterface2', '00:00:5e:00:53:13', 'LinkUp', 1000)],
    managerNic: nic('eth0', '00:00:5e:00:53:a1', 'LinkUp', 1000),
    conditions: [reachable, accepted, read, matched, unique],
  }),
  'example-srv-a01-02': device => discovery(device, {
    serial: 'SYNTH-OTHER-99', key: key('5c'), notAfter: at(200 * DAY),
    nics: [nic('EthernetInterface0', '00:00:5e:00:53:21'), nic('EthernetInterface1', '00:00:5e:00:53:29'), nic('EthernetInterface2', '00:00:5e:00:53:23')],
    managerNic: nic('eth0', '00:00:5e:00:53:a2', 'LinkUp', 1000),
    resetTypes: ['On', 'ForceOff', 'ForceRestart'],
    conditions: [reachable, accepted, read, condition('IdentityMatched', 'False', 'SerialMismatch', 'the System with spec.identity.systemUUID reports another serial; spec is not changed'), unique],
  }),
  [FIXTURE_NAMES.singleFeedServer]: device => discovery(device, {
    serial: device.spec.identity.serial!, key: key('9d'), notAfter: at(12 * DAY),
    nics: [nic('EthernetInterface0', '00:00:5e:00:53:31'), nic('EthernetInterface1', '00:00:5e:00:53:32', 'LinkDown', 0)],
    managerNic: nic('eth0', '00:00:5e:00:53:a3', 'LinkUp', 1000),
    conditions: [reachable,
      condition('Authenticated', 'False', 'PinNotApproved', 'the observed public key is in status.discovery.tls; verify it out of band and approve it as tlsPublicKeySHA256 in the credential Secret'),
      condition('Discovered', 'False', 'PinNotApproved', 'the observed public key is in status.discovery.tls; verify it out of band and approve it as tlsPublicKeySHA256 in the credential Secret'),
      matched, unique],
  }),
};
for (const item of devices) {
  const status = DISCOVERED[item.metadata.name];
  if (status) item.status = status(item);
}

// Claims for the racked equipment. Devices with a reported placement use it;
// the claim is the installation plan and history.
function claim(name: string, rack: RackName, position: number, height: number, face: 'front' | 'rear', manufacturer: string, model: string): DeviceClaim {
  return {
    kind: 'DeviceClaim', metadata: meta(name),
    spec: {
      displayName: `Installation · ${rack.slice(-3).toUpperCase()} U${position}`,
      lifecycle: 'Installation',
      expectedIdentity: { manufacturer, model },
      placement: { scope: 'RackUnits', facilityRef: { name: FIXTURE_FACILITY }, rackRef: { name: rack }, position, height, face },
    },
  };
}

const claims: DeviceClaim[] = [
  ...devices.flatMap(item => {
    const report = item.spec.reportedPlacement;
    if (!report?.rackRef || report.position === undefined || item.spec.claimRef.name !== `${item.metadata.name}-install`) return [];
    return [claim(item.spec.claimRef.name, report.rackRef.name as RackName, report.position, report.height ?? 1, report.face ?? 'front', item.spec.identity.manufacturer ?? 'unknown', item.spec.identity.model)];
  }),
  // A planned installation with no Device registered yet.
  claim('example-plan-a02-u35', 'example-rack-a02', 35, 1, 'front', 'example-compute', 'EX-C1'),
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
  one('example-dac-a01-02-eth1', 'DAC a01-02 eth1', 'Copper DAC', D('example-srv-a01-02', 'eth1'), D('example-leaf-missing', 'swp7')),
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
  // Both PSUs of one server on the same PDU, and so on the same feed.
  one('example-pwr-a01-05-psu1', 'Power a01-05 PSU 1', 'Power', D(FIXTURE_NAMES.singleFeedServer, 'psu-1'), D(PDU, 'outlet-4')),
  one('example-pwr-a01-05-psu2', 'Power a01-05 PSU 2', 'Power', D(FIXTURE_NAMES.singleFeedServer, 'psu-2'), D(PDU, 'outlet-5')),
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
