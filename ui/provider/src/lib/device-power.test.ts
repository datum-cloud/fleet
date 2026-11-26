import { describe, expect, it } from 'vitest';
import type { Device } from '../../../shared/fleet-api';
import { ApiError } from './api';
import { offeredActions, powerBlock, powerRequest } from './device-power';
import { FIXTURE_NAMES, inventoryFixture } from './inventory-fixtures';
import { fixtureCustody } from './power-fixtures';

const named = (name: string): Device => structuredClone(inventoryFixture.devices.find(device => device.metadata.name === name)!);
const caller = { username: 'staff@example.com', uid: 'staff-uid' };

describe('powerRequest', () => {
  it('refuses an empty or overlong reason, and an action discovery does not offer', () => {
    const device = named('example-srv-a01-02');
    expect(offeredActions(device)).toEqual(['On', 'ForceOff', 'ForceRestart']);
    expect(() => powerRequest(device, 'On', '   ', caller)).toThrow(ApiError);
    expect(() => powerRequest(device, 'On', 'x'.repeat(513), caller)).toThrow(ApiError);
    expect(() => powerRequest(device, 'PowerCycle', 'Reason', caller)).toThrow('Power cycle is not offered.');
  });

  it('lowercases the recorded system UUID, as the CRD pattern requires', () => {
    const device = named(FIXTURE_NAMES.server);
    device.spec.identity.systemUUID = device.spec.identity.systemUUID!.toUpperCase();
    device.status!.discovery!.inventory!.systems![0].uuid = device.spec.identity.systemUUID;
    expect(powerRequest(device, 'On', 'Reason', caller).spec.expectedSystemUUID).toBe(device.spec.identity.systemUUID.toLowerCase());
  });

  it('keeps the generated name inside the name limit for a long Device name', () => {
    const device = named(FIXTURE_NAMES.server);
    device.metadata.name = 'a'.repeat(253);
    expect(powerRequest(device, 'On', 'Reason', caller).metadata.generateName.length).toBeLessThanOrEqual(63);
  });
});

describe('fixture custody', () => {
  it('covers a free, an allocated and a held Device', () => {
    expect(powerBlock(named(FIXTURE_NAMES.server), { custody: fixtureCustody(FIXTURE_NAMES.server) })).toBeUndefined();
    expect(powerBlock(named(FIXTURE_NAMES.server), { custody: fixtureCustody('example-srv-a01-02') })).toBe('Allocated to a customer');
    // a01-02's own discovery reports a serial mismatch, which the coordinator refuses first.
    expect(powerBlock(named('example-srv-a01-02'), { custody: fixtureCustody('example-srv-a01-02') })).toBe('Identity not matched');
    expect(powerBlock(named(FIXTURE_NAMES.singleFeedServer), { custody: fixtureCustody(FIXTURE_NAMES.singleFeedServer) })).toMatch(/^Held: DevicePowerOperation /);
  });
});
