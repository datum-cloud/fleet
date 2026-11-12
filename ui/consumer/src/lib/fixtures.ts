import { ApiError } from './api';
import { MACHINE_FINALIZER } from '../../../shared/fleet-api';
import type { RawMachine, RawMachineClass, RawMachineProfile } from '../types';

// Synthetic machines used while the Fleet API cannot be read
// (VITE_FLEET_FIXTURES=true). Class, profile and location names come from
// config/standalone/fixtures.yaml, and each status is what Fleet's standalone
// runner writes (internal/standalone/runner.go): a powered-off handoff, a machine
// waiting for capacity, and a deleted machine still held by the release
// finalizer.

const SPEC = {
  machineClassRef: { name: 'standard-metal' },
  machineProfileRef: { name: 'general-purpose-24c-128g' },
  locationRef: { name: 'us-central-1' },
  powerState: 'Off',
} as const;

const PLACEMENT = { locationRef: { name: 'us-central-1' } };

const BOUND_STATUS: RawMachine['status'] = {
  phase: 'Ready',
  placement: PLACEMENT,
  acceptedProfileRef: { name: 'general-purpose-24c-128g' },
  allocatedHardware: {
    observedAt: '2026-11-05T12:00:00Z',
    hardware: {
      cpu: { architecture: 'amd64', sockets: 2, cores: 24, threads: 48 },
      memory: { capacity: '128Gi' },
    },
  },
  power: { state: 'Off' },
  conditions: [
    { type: 'Accepted', status: 'True', reason: 'SyntheticFixtureAccepted' },
    { type: 'Bound', status: 'True', reason: 'ExclusiveBinding' },
    { type: 'NetworkReady', status: 'True', reason: 'NoAttachmentsRequired' },
    { type: 'Ready', status: 'True', reason: 'PoweredOffHandoff' },
  ],
};

export const FIXTURE_MACHINES: RawMachine[] = [
  {
    metadata: { name: 'example', namespace: 'default', finalizers: [MACHINE_FINALIZER] },
    spec: SPEC,
    status: BOUND_STATUS,
  },
  {
    metadata: { name: 'build-runner', namespace: 'default', finalizers: [MACHINE_FINALIZER] },
    spec: SPEC,
    status: {
      phase: 'Pending',
      placement: PLACEMENT,
      conditions: [
        { type: 'Accepted', status: 'True', reason: 'SyntheticFixtureAccepted' },
        {
          type: 'Bound',
          status: 'False',
          reason: 'InsufficientCapacity',
          message: 'The only synthetic machine is allocated to another consumer.',
        },
        { type: 'Ready', status: 'False', reason: 'InsufficientCapacity' },
      ],
    },
  },
  {
    metadata: {
      name: 'old-worker',
      namespace: 'default',
      deletionTimestamp: '2026-11-05T12:30:00Z',
      finalizers: [MACHINE_FINALIZER],
    },
    spec: SPEC,
    status: BOUND_STATUS,
  },
];

// Machines created in fixture mode are kept in memory until the page reloads.
// A new machine has no status, so it lists as Pending, as it would before
// Fleet admits it.
const machines = [...FIXTURE_MACHINES];

export const fixtureMachines = {
  list: (): RawMachine[] => [...machines],
  create: (machine: RawMachine): RawMachine => {
    if (machines.some((c) => c.metadata.name === machine.metadata.name)) {
      throw new ApiError(409, `machines "${machine.metadata.name}" already exists`);
    }
    const created = {
      ...machine,
      metadata: { ...machine.metadata, creationTimestamp: new Date().toISOString() },
    };
    machines.push(created);
    return created;
  },
  // Releasing keeps the Machine until Fleet frees its capacity and removes the
  // finalizer; nothing does that in fixture mode, so it stays Releasing.
  release: (name: string): void => {
    const index = machines.findIndex((c) => c.metadata.name === name);
    if (index === -1) throw new ApiError(404, `machines "${name}" not found`);
    const machine = machines[index];
    if (machine.metadata.deletionTimestamp) return;
    machines[index] = {
      ...machine,
      metadata: { ...machine.metadata, deletionTimestamp: new Date().toISOString() },
    };
  },
};

// A consumer project's catalog: standard-metal published from datum-cloud
// with two profiles, and a second class whose publication has not resolved.
export const FIXTURE_CLASSES: RawMachineClass[] = [
  {
    metadata: { name: 'standard-metal' },
    spec: { source: { project: 'datum-cloud', name: 'standard-metal' } },
    status: {
      evaluatedRevision: 'class-revision-example',
      resolvedClass: {
        profilePolicy: {
          allowedProfiles: [{ name: 'general-purpose-24c-128g' }, { name: 'accelerated-32c-256g' }],
          customProfiles: { enabled: false },
        },
        access: { consoleModes: [], powerControl: true },
      },
      conditions: [{ type: 'SourceResolved', status: 'True' }],
    },
  },
  {
    metadata: { name: 'high-memory-metal' },
    spec: { source: { project: 'datum-cloud', name: 'high-memory-metal' } },
    status: { conditions: [{ type: 'SourceResolved', status: 'Unknown' }] },
  },
];

export const FIXTURE_PROFILES: RawMachineProfile[] = [
  {
    metadata: { name: 'general-purpose-24c-128g' },
    spec: { cpu: { architecture: 'amd64', cores: { min: 24 } }, memory: { capacity: { min: '128Gi' } } },
  },
  {
    metadata: { name: 'accelerated-32c-256g' },
    spec: {
      cpu: { architecture: 'amd64', cores: { min: 32 } },
      memory: { capacity: { min: '256Gi' } },
      gpus: { count: { exact: 4 }, vendor: 'nvidia' },
    },
  },
];
