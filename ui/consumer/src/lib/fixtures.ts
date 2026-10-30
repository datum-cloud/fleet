import type { RawMachineClaim } from '../types';

// Synthetic claims shaped after the design doc's examples, used while the
// Fleet API is not yet served (VITE_FLEET_FIXTURES=true).
export const FIXTURE_CLAIMS: RawMachineClaim[] = [
  {
    metadata: { name: 'example', namespace: 'default' },
    spec: {
      machineClassRef: { name: 'standard' },
      machineProfileRef: { name: 'accelerated-16c-64g' },
      locationRef: { name: 'lab' },
      powerState: 'On',
    },
    status: {
      allocatedHardware: {
        hardware: { cpu: { architecture: 'amd64', cores: 24 }, memory: { capacity: '128Gi' } },
      },
      power: { state: 'On' },
      conditions: [
        { type: 'Accepted', status: 'True' },
        { type: 'Bound', status: 'True' },
        { type: 'Ready', status: 'True' },
        { type: 'Releasing', status: 'False' },
      ],
    },
  },
  {
    metadata: { name: 'build-runner', namespace: 'default' },
    spec: {
      machineClassRef: { name: 'standard' },
      machineProfileRef: { name: 'accelerated-16c-64g' },
      locationRef: { name: 'lab' },
    },
    status: {
      conditions: [
        { type: 'Accepted', status: 'True' },
        { type: 'Bound', status: 'False', reason: 'NoCapacity' },
      ],
    },
  },
  {
    metadata: { name: 'old-worker', namespace: 'default' },
    spec: {
      machineClassRef: { name: 'standard' },
      machineProfileRef: { name: 'accelerated-16c-64g' },
      locationRef: { name: 'lab' },
      powerState: 'Off',
    },
    status: {
      allocatedHardware: {
        hardware: { cpu: { architecture: 'amd64', cores: 24 }, memory: { capacity: '128Gi' } },
      },
      power: { state: 'Off' },
      conditions: [
        { type: 'Accepted', status: 'True' },
        { type: 'Bound', status: 'True' },
        { type: 'Ready', status: 'False' },
        { type: 'Releasing', status: 'True' },
      ],
    },
  },
];
