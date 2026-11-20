import { currentDevice, placementLabel, toInventory } from './inventory';
import { fixtureDeviceClaims, fixtureDevices } from './lib/fixtures';
import { describe, expect, it } from 'vitest';

describe('toInventory', () => {
  it('binds Devices to claims in the same namespace and keeps orphans visible', () => {
    const inventory = toInventory(fixtureDeviceClaims, fixtureDevices);
    expect(inventory.slots.map((s) => s.claim.metadata.name)).toEqual(['rack-01-u10', 'rack-01-u12']);
    const u12 = inventory.slots.find((s) => s.claim.metadata.name === 'rack-01-u12')!;
    expect(currentDevice(u12)?.metadata.name).toBe('rack-01-u12-synthetic-0001');
    expect(currentDevice(inventory.slots[0])).toBeUndefined();
    expect(inventory.unboundDevices.map((d) => d.metadata.name)).toEqual(['orphan-synthetic-0009']);
  });

  it('does not bind a Device to a same-named claim in another namespace', () => {
    const elsewhere = { ...fixtureDevices[0], metadata: { ...fixtureDevices[0].metadata, namespace: 'other' } };
    const inventory = toInventory(fixtureDeviceClaims, [elsewhere]);
    expect(inventory.slots.every((s) => s.devices.length === 0)).toBe(true);
    expect(inventory.unboundDevices).toHaveLength(1);
  });

  it('does not infer active hardware from the most recently edited record', () => {
    const replacement = {
      ...fixtureDevices[0],
      metadata: { ...fixtureDevices[0].metadata, name: 'rack-01-u12-synthetic-0002' },
      spec: { ...fixtureDevices[0].spec, provenance: { ...fixtureDevices[0].spec.provenance, recordedAt: '2026-02-01T00:00:00Z' } },
    };
    const inventory = toInventory(fixtureDeviceClaims, [replacement, fixtureDevices[0]]);
    const u12 = inventory.slots.find((s) => s.claim.metadata.name === 'rack-01-u12')!;
    expect(u12.devices).toHaveLength(2);
    expect(currentDevice(u12)).toBeUndefined();
  });

  it('labels single and multi-unit placements', () => {
    expect(placementLabel(fixtureDeviceClaims[0])).toBe('example-facility · rack-01 · U12 front');
    const tall = { ...fixtureDeviceClaims[0], spec: { ...fixtureDeviceClaims[0].spec, placement: { ...fixtureDeviceClaims[0].spec.placement, height: 2 } } };
    expect(placementLabel(tall)).toBe('example-facility · rack-01 · U12–U13 front');
  });
});
