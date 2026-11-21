import { describe, expect, it } from 'vitest';
import { toInventory } from '../inventory';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from './fixtures';
import { devicePlacement, equipmentGeometry, equipmentPlacements, placementFacility, reportedPlacementLabel } from './device-placement';
import type { ReportedPlacement } from '../../../shared/fleet-api';

const report: ReportedPlacement = { revision: 1, observedAt: '2026-11-21T00:00:00Z', snapshotDigest: `sha256:${'a'.repeat(64)}`, rackRef: { name: 'rack-01' }, position: 12, height: 4, face: 'rear', fullDepth: true };
const device = { ...fixturePhysicalDevices[0], spec: { ...fixturePhysicalDevices[0].spec, reportedPlacement: report } };
const inventory = toInventory(fixturePhysicalClaims, [device], fixturePhysicalRecords);

describe('Device observational placement projection', () => {
  it('prefers the report without changing the claim or inventory inputs', () => {
    const before = JSON.stringify(inventory);
    const entry = devicePlacement(device, inventory)!;
    expect(entry.basis).toBe('Reported placement');
    expect(entry.placement.position).toBe(12);
    expect(entry.placement.face).toBe('rear');
    expect(JSON.stringify(inventory)).toBe(before);
    expect(equipmentPlacements(inventory).filter(item => item.claim?.metadata.name === device.spec.claimRef.name)).toHaveLength(1);
  });
  it('never borrows geometry from a claim to complete a partial report', () => {
    const partial = { ...device, spec: { ...device.spec, reportedPlacement: { ...report, position: undefined, height: undefined, face: undefined } } };
    const entry = devicePlacement(partial, inventory)!;
    expect(entry.placement.position).toBeUndefined();
    expect(equipmentGeometry(entry, fixturePhysicalRecords[1])).toBeUndefined();
    expect(reportedPlacementLabel(entry, inventory)).toContain('Position not recorded');
  });
  it.each([401, 403, 404, 500])('preserves reports when claims cannot be read (%i)', status => {
    const denied = { ...inventory, issues: { DeviceClaim: { status } } };
    expect(devicePlacement(device, denied)?.claim).toBeUndefined();
    expect(devicePlacement(device, denied)?.basis).toBe('Reported placement');
    expect(devicePlacement(fixturePhysicalDevices[0], denied)).toBeUndefined();
  });
  it('masks denied location names and hides denied devices', () => {
    const denied = { ...inventory, issues: { Rack: { status: 403 }, Facility: { status: 403 } } };
    const label = reportedPlacementLabel(devicePlacement(device, denied)!, denied);
    expect(label).toContain('Rack unavailable');
    expect(label).not.toContain('rack-01');
    expect(placementFacility(report, device.metadata.namespace, denied)).toBeUndefined();
    expect(devicePlacement(device, { ...inventory, issues: { Device: { status: 403 } } })).toBeUndefined();
  });
  it('retains orphan reports and does not infer a missing claim', () => {
    const orphan = toInventory([], [device], fixturePhysicalRecords);
    expect(equipmentPlacements(orphan)[0].kind).toBe('Device');
    expect(equipmentPlacements(orphan)[0].claim).toBeUndefined();
  });
  it('keeps multiple registrations separate without choosing the newest active device', () => {
    const older = { ...fixturePhysicalDevices[0], metadata: { ...device.metadata, name: 'older' } };
    const mixed = toInventory(fixturePhysicalClaims, [older, device], fixturePhysicalRecords);
    const entries = equipmentPlacements(mixed).filter(entry => entry.claim?.metadata.name === device.spec.claimRef.name);
    expect(entries).toHaveLength(2);
    expect(entries.find(entry => entry.kind === 'DeviceClaim')?.devices.map(item => item.metadata.name)).toEqual(['older']);
    expect(entries.every(entry => reportedPlacementLabel(entry, mixed).includes('active device unknown'))).toBe(true);
  });
  it.each([
    { position: 41, height: 4 }, { position: 12.5 }, { height: 0.5 }, { position: undefined }, { height: undefined }, { face: undefined }, { position: 0 }, { height: 0 },
  ])('does not draw unknown, non-integral or out-of-range geometry: %j', fields => {
    const entry = devicePlacement({ ...device, spec: { ...device.spec, reportedPlacement: { ...report, ...fields } } }, inventory)!;
    expect(equipmentGeometry(entry, fixturePhysicalRecords[1])).toBeUndefined();
  });
});
