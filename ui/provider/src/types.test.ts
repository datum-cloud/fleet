import { formatConstraint, toMachine, toMachineProfile, type RawMachine } from './types';
import { FIXTURE_MACHINES, fixtureProfiles } from './lib/fixtures';
import { describe, expect, it } from 'vitest';

function bare(spec: Partial<RawMachine['spec']> = {}): RawMachine {
  return {
    metadata: { name: 'bare' },
    spec: {
      providerRef: { apiGroup: 'example.com', kind: 'ExampleMachine', name: 'asset-1' },
      allocationPolicy: 'Disabled',
      cleaningPolicyRef: { name: 'wipe' },
      reclaimPolicy: 'Retain',
      ...spec,
    },
  };
}

describe('toMachine', () => {
  it('maps a synthetic machine written by the standalone runner', () => {
    expect(toMachine(FIXTURE_MACHINES[0])).toEqual({
      name: 'central1-baremetal-01',
      namespace: 'fleet-provider',
      pool: 'central-metal',
      allocationPolicy: 'Enabled',
      cpu: '24 cores / 48 threads · amd64',
      memory: '128Gi',
      prepared: { state: 'True', reason: 'SyntheticEvidence' },
      healthy: { state: 'True', reason: 'SyntheticEvidence' },
      available: { state: 'True', reason: 'SyntheticEvidence' },
    });
  });

  it('treats absent conditions as Unknown', () => {
    const machine = toMachine(bare());
    expect(machine.prepared).toEqual({ state: 'Unknown', reason: undefined });
    expect(machine.available.state).toBe('Unknown');
  });

  it('leaves unobserved hardware absent', () => {
    const machine = toMachine(bare());
    expect(machine.cpu).toBeUndefined();
    expect(machine.memory).toBeUndefined();
  });
});

describe('formatConstraint', () => {
  it('renders exact, ranges, floors, ceilings and any', () => {
    expect(formatConstraint({ exact: 2 })).toBe('2');
    expect(formatConstraint({ min: 16, max: 32 }, ' cores')).toBe('16 cores–32 cores');
    expect(formatConstraint({ min: '128Gi' })).toBe('≥ 128Gi');
    expect(formatConstraint({ max: 4 })).toBe('≤ 4');
    expect(formatConstraint({})).toBe('Any');
    expect(formatConstraint(undefined)).toBe('Any');
  });
});

describe('toMachineProfile', () => {
  it('maps the standalone fixture profile', () => {
    expect(toMachineProfile(fixtureProfiles.list()[0])).toEqual({
      name: 'general-purpose-24c-128g',
      cpu: '≥ 24 cores · amd64',
      memory: '≥ 128Gi',
      gpus: undefined,
      disks: 0,
      interfaces: 0,
      createdAt: '2026-11-05T12:00:00Z',
    });
  });

  it('summarizes GPU requirements', () => {
    const profile = toMachineProfile({
      metadata: { name: 'gpu' },
      spec: {
        cpu: { architecture: 'amd64' },
        memory: { capacity: { exact: '256Gi' } },
        gpus: { count: { exact: 4 }, vendor: 'nvidia', models: ['h100'] },
      },
    });
    expect(profile.cpu).toBe('Any · amd64');
    expect(profile.gpus).toBe('4 × nvidia h100');
  });
});
