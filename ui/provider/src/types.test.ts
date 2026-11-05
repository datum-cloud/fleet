import { toMachine, type RawMachine } from './types';
import { FIXTURE_MACHINES } from './lib/fixtures';
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
      evidenceSource: 'Simulator',
      cpu: '24 cores / 48 threads · amd64',
      memory: '128Gi',
      gpus: undefined,
      prepared: { state: 'True', reason: 'SyntheticEvidence' },
      healthy: { state: 'True', reason: 'SyntheticEvidence' },
      available: { state: 'True', reason: 'SyntheticEvidence' },
    });
  });

  it('names a real provider by its kind', () => {
    expect(toMachine(bare()).evidenceSource).toBe('ExampleMachine');
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
    expect(machine.gpus).toBeUndefined();
  });
});
