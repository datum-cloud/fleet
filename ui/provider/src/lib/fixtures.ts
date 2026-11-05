import type { RawMachine } from '../types';

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
