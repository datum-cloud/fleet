import { ApiError } from './api';
import type { RawDevice, RawDeviceClaim } from '../inventory';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import type { RawMachineCapacity, RawMachineClass, RawMachineProfile } from '../types';

// Synthetic MachineCapacities used while the Fleet API cannot be read
// (VITE_FLEET_FIXTURES=true). The namespace, pool label and provider come from
// config/standalone/fixtures.yaml, and the hardware and conditions are what
// Fleet's standalone runner writes (internal/standalone/runner.go): one
// capacity free, one allocated, and one just enrolled with nothing observed.

const NAMESPACE = 'fleet-provider';
const LABELS = { 'fleet.datumapis.com/capacity-pool': 'central-metal' };

function spec(device: string, allocationPolicy: RawMachineCapacity['spec']['allocationPolicy']): RawMachineCapacity['spec'] {
  return {
    deviceRef: { name: device },
    allocationPolicy,
    cleaningPolicyRef: { name: 'synthetic-tenant-reuse-v1' },
    reclaimPolicy: 'Retain',
  };
}

const HARDWARE = {
  cpu: { architecture: 'amd64', sockets: 2, cores: 24, threads: 48 },
  memory: { capacity: '128Gi' },
};

const EVIDENCE_CURRENT = [
  { type: 'Prepared', status: 'True', reason: 'SyntheticEvidence' },
  { type: 'Healthy', status: 'True', reason: 'SyntheticEvidence' },
] as const;

export const FIXTURE_CAPACITIES: RawMachineCapacity[] = [
  {
    metadata: { name: 'central1-baremetal-01', namespace: NAMESPACE, labels: LABELS },
    spec: spec('synthetic-device-central-01', 'Enabled'),
    status: {
      hardware: HARDWARE,
      conditions: [...EVIDENCE_CURRENT, { type: 'Available', status: 'True', reason: 'SyntheticEvidence' }],
    },
  },
  {
    metadata: { name: 'central1-baremetal-02', namespace: NAMESPACE, labels: LABELS },
    spec: spec('synthetic-device-central-02', 'Enabled'),
    status: {
      hardware: HARDWARE,
      conditions: [...EVIDENCE_CURRENT, { type: 'Available', status: 'False', reason: 'Allocated' }],
    },
  },
  {
    metadata: { name: 'central1-baremetal-03', namespace: NAMESPACE, labels: LABELS },
    spec: spec('synthetic-device-central-03', 'Disabled'),
  },
];

// Created capacity is kept in memory until the page reloads. A new record
// has no status: only Fleet's discovery reports hardware and conditions.
const capacities = [...FIXTURE_CAPACITIES];

export const fixtureCapacities = {
  list: (): RawMachineCapacity[] => [...capacities],
  create: (capacity: RawMachineCapacity): RawMachineCapacity => {
    const { name, namespace } = capacity.metadata;
    if (capacities.some((m) => m.metadata.name === name && m.metadata.namespace === namespace)) {
      throw new ApiError(409, `machinecapacities "${name}" already exists`);
    }
    const created = {
      ...capacity,
      metadata: { ...capacity.metadata, creationTimestamp: new Date().toISOString() },
    };
    capacities.push(created);
    return created;
  },
  delete: (namespace: string, name: string): void =>
    remove(capacities, 'machinecapacities', name, (m) => m.metadata.name === name && m.metadata.namespace === namespace),
};

// Removes the first record matching `match`, or fails like the API does.
function remove<T extends { metadata: { name: string } }>(
  records: T[],
  resource: string,
  name: string,
  match: (record: T) => boolean
): void {
  const index = records.findIndex(match);
  if (index === -1) throw new ApiError(404, `${resource} "${name}" not found`);
  records.splice(index, 1);
}

// The standalone fixture's profile. Created profiles are kept in memory so
// the create flow can be tried without the API; a reload resets them.
const profiles: RawMachineProfile[] = [
  {
    metadata: { name: 'general-purpose-24c-128g', creationTimestamp: '2026-11-05T12:00:00Z' },
    spec: { cpu: { architecture: 'amd64', cores: 24 }, memory: { capacity: '128Gi' } },
  },
];

export const fixtureProfiles = {
  list: (): RawMachineProfile[] => [...profiles],
  create: (profile: RawMachineProfile): RawMachineProfile => {
    if (profiles.some((p) => p.metadata.name === profile.metadata.name)) {
      throw new ApiError(409, `machineprofiles "${profile.metadata.name}" already exists`);
    }
    const created = {
      ...profile,
      metadata: { ...profile.metadata, creationTimestamp: new Date().toISOString() },
    };
    profiles.push(created);
    return created;
  },
  delete: (name: string): void =>
    remove(profiles, 'machineprofiles', name, (p) => p.metadata.name === name),
};

// The standalone fixture's class, accepted by Fleet. Created classes are kept
// in memory like profiles, and have no status until Fleet evaluates them.
const classes: RawMachineClass[] = [
  {
    metadata: { name: 'standard-metal', creationTimestamp: '2026-11-05T12:00:00Z' },
    spec: {
      profilePolicy: {
        allowedProfiles: [{ name: 'general-purpose-24c-128g' }],
        customProfiles: { enabled: false },
      },
      access: { consoleModes: [], powerControl: false },
      capacity: {
        machineCapacities: {
          namespace: 'fleet-provider',
          selector: { matchLabels: { 'fleet.datumapis.com/capacity-pool': 'central-metal' } },
        },
      },
    },
    status: {
      evaluatedRevision: 'class-revision-example',
      conditions: [{ type: 'Accepted', status: 'True' }],
    },
  },
];

export const fixtureClasses = {
  list: (): RawMachineClass[] => [...classes],
  create: (machineClass: RawMachineClass): RawMachineClass => {
    if (classes.some((c) => c.metadata.name === machineClass.metadata.name)) {
      throw new ApiError(409, `machineclasses "${machineClass.metadata.name}" already exists`);
    }
    const created = {
      ...machineClass,
      metadata: { ...machineClass.metadata, creationTimestamp: new Date().toISOString() },
    };
    classes.push(created);
    return created;
  },
  delete: (name: string): void =>
    remove(classes, 'machineclasses', name, (c) => c.metadata.name === name),
};

// Synthetic inventory: two planned slots, one with a registered Device whose
// NIC MACs are not yet mapped to ports, plus a Device whose claim is missing.
// Documentation-range addresses and MACs only.

const INVENTORY_NAMESPACE = 'default';

function claim(name: string, position: number): RawDeviceClaim {
  return {
    metadata: { name, namespace: INVENTORY_NAMESPACE },
    spec: {
      provenance: { method: 'Manual', recordedBy: 'planner@example.com', recordedAt: '2026-01-02T03:04:05Z', reference: 'rack plan 1234' },
      expectedIdentity: { manufacturer: 'Example Vendor', model: 'Example 1U' },
      placement: { facility: 'example-facility', rack: 'rack-01', position, height: 1, face: 'front' },
      ports: [
        { name: 'eth0', type: '25gbase-x-sfp28', speedMbps: 25000, expectedPeer: { device: 'switch-01', port: `Ethernet1/${position}` } },
        { name: 'bmc0', type: '1000base-t', speedMbps: 1000, expectedPeer: { device: 'mgmt-switch-01', port: `Ethernet${position}` } },
      ],
    },
  };
}

export const fixtureDeviceClaims: RawDeviceClaim[] = [claim('rack-01-u12', 12), claim('rack-01-u10', 10)];

export const fixtureDevices: RawDevice[] = [
  {
    metadata: { name: 'rack-01-u12-synthetic-0001', namespace: INVENTORY_NAMESPACE },
    spec: {
      claimRef: { name: 'rack-01-u12' },
      provenance: { method: 'Manual', recordedBy: 'registrar@example.com', recordedAt: '2026-01-09T10:00:00Z', reference: 'BMC inventory read during rack turn-up' },
      identity: { manufacturer: 'Example Vendor', model: 'Example 1U', serial: 'SYNTHETIC-0001', systemUUID: '00000000-0000-4000-8000-000000000001' },
      bmc: { endpoint: 'https://192.0.2.100:443' },
      unmappedMACAddresses: ['00:00:5e:00:53:01', '00:00:5e:00:53:02'],
    },
  },
  {
    metadata: { name: 'orphan-synthetic-0009', namespace: INVENTORY_NAMESPACE },
    spec: {
      claimRef: { name: 'rack-02-u01' },
      provenance: { method: 'Manual', recordedBy: 'registrar@example.com', recordedAt: '2026-01-10T10:00:00Z' },
      identity: { model: 'Example 2U', systemUUID: '00000000-0000-4000-8000-000000000009' },
    },
  },
];

const inventoryProvenance = { method: 'Manual' as const, recordedBy: 'inventory@example.com', recordedAt: '2026-01-10T10:00:00Z' };

export const fixturePhysicalRecords: PhysicalRecord[] = [
  { kind: 'Facility', metadata: { name: 'example-facility', namespace: INVENTORY_NAMESPACE }, spec: { displayName: 'Example facility', provenance: inventoryProvenance, locationRef: { name: 'example-region' } } },
  { kind: 'Rack', metadata: { name: 'rack-01', namespace: INVENTORY_NAMESPACE }, spec: { displayName: 'Rack 01', provenance: inventoryProvenance, facilityRef: { name: 'example-facility' }, heightUnits: 42 } },
  { kind: 'PowerFeed', metadata: { name: 'feed-a', namespace: INVENTORY_NAMESPACE }, spec: { displayName: 'Feed A', provenance: inventoryProvenance, facilityRef: { name: 'example-facility' }, rackRef: { name: 'rack-01' }, supply: { type: 'AC', voltageV: 208, currentA: 30, phase: 'Single' }, terminations: [{ id: 'outlet-1', name: 'Outlet 1', type: 'PowerOutlet', connector: 'IEC C13', positions: 1 }], upstreamSources: [{ system: 'example-provider', type: 'Supply', id: 'synthetic-supply-a' }] } },
  { kind: 'Cable', metadata: { name: 'power-01', namespace: INVENTORY_NAMESPACE }, spec: { displayName: 'Server power cable', provenance: inventoryProvenance, medium: 'Power', lanes: [{ id: 'power', a: { powerFeedRef: { name: 'feed-a' }, componentID: 'outlet-1', position: 1 }, b: { deviceRef: { name: 'rack-01-u12-synthetic-0001' }, componentID: 'psu-1', position: 1 } }] } },
];

export const fixturePhysicalClaims: RawDeviceClaim[] = fixtureDeviceClaims.map(record => ({ ...record, spec: { ...record.spec, displayName: `Server installation · U${record.spec.placement.position}`, placement: { scope: 'RackUnits', rackRef: { name: 'rack-01' }, position: record.spec.placement.position, height: 1, face: 'front' } } }));

export const fixturePhysicalDevices: RawDevice[] = fixtureDevices.map((record, index) => index ? record : ({ ...record, spec: { ...record.spec, displayName: 'Example compute server', assetTag: 'EXAMPLE-001', topologyAuthority: 'Cable', modelShape: { heightUnits: 1, fullDepth: true }, components: [{ id: 'psu-1', name: 'Power supply 1', type: 'PowerInlet', connector: 'IEC C14', positions: 1 }, { id: 'eth0', name: 'Ethernet 0', type: 'NetworkPort', connector: 'SFP28', positions: 1 }] } }));
