import { ApiError } from './api';
import type { RawMachine, RawMachineClass, RawMachineProfile } from '../types';

// Synthetic Machines used while the Fleet API cannot be read
// (VITE_FLEET_FIXTURES=true). The namespace, pool label and provider come from
// config/standalone/fixtures.yaml, and the hardware and conditions are what
// Fleet's standalone runner writes (internal/standalone/runner.go): one
// machine free, one allocated, and one just enrolled with nothing observed.

const NAMESPACE = 'fleet-provider';
const LABELS = { 'fleet.datumapis.com/capacity-pool': 'central-metal' };

function spec(asset: string, allocationPolicy: RawMachine['spec']['allocationPolicy']): RawMachine['spec'] {
  return {
    providerRef: { apiGroup: 'synthetic.fleet.datumapis.com', kind: 'SyntheticMachine', name: asset },
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

export const FIXTURE_MACHINES: RawMachine[] = [
  {
    metadata: { name: 'central1-baremetal-01', namespace: NAMESPACE, labels: LABELS },
    spec: spec('asset-central-01', 'Enabled'),
    status: {
      hardware: HARDWARE,
      conditions: [...EVIDENCE_CURRENT, { type: 'Available', status: 'True', reason: 'SyntheticEvidence' }],
    },
  },
  {
    metadata: { name: 'central1-baremetal-02', namespace: NAMESPACE, labels: LABELS },
    spec: spec('asset-central-02', 'Enabled'),
    status: {
      hardware: HARDWARE,
      conditions: [...EVIDENCE_CURRENT, { type: 'Available', status: 'False', reason: 'Allocated' }],
    },
  },
  {
    metadata: { name: 'central1-baremetal-03', namespace: NAMESPACE, labels: LABELS },
    spec: spec('asset-central-03', 'Disabled'),
  },
];

// The standalone fixture's profile. Created profiles are kept in memory so
// the create flow can be tried without the API; a reload resets them.
const profiles: RawMachineProfile[] = [
  {
    metadata: { name: 'general-purpose-24c-128g', creationTimestamp: '2026-11-05T12:00:00Z' },
    spec: { cpu: { architecture: 'amd64', cores: { min: 24 } }, memory: { capacity: { min: '128Gi' } } },
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
        machines: {
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
};
