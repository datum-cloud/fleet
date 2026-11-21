import { describe, expect, it } from 'vitest';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import { toInventory } from '../inventory';
import { redactInventory } from './inventory-read-access';
import { facilityIdentifiers, facilitySites, siteFacilities } from './site-membership';

const provenance = { method: 'Manual' as const, recordedBy: 'synthetic', recordedAt: '2026-01-01T00:00:00Z' };
const facility: PhysicalRecord = { kind: 'Facility', metadata: { name: 'premises-stable', namespace: 'inventory', uid: 'premises-uid' }, spec: { displayName: 'Physical premises', provenance, identifiers: [{ provider: 'example-a', type: 'PremisesCode', value: 'EX1' }, { provider: 'example-b', type: 'PremisesCode', value: 'EX1' }] } };
const site: PhysicalRecord = { kind: 'Site', metadata: { name: 'site-stable', namespace: 'inventory', uid: 'site-uid' }, spec: { displayName: 'Operational identity', code: 'EX3', provenance, facilityRefs: [{ name: facility.metadata.name }] } };

describe('observational Site membership', () => {
  it('keeps legacy Facilities explicitly unmapped and provider codes qualified', () => {
    expect(facilitySites(facility, toInventory([], [], [facility]))).toContain('Unmapped');
    expect(facilityIdentifiers(facility)).toContain('example-a / EX1');
    expect(facilityIdentifiers(facility)).toContain('example-b / EX1');
  });

  it('resolves same-namespace names despite human label changes', () => {
    const renamed = { ...site, spec: { ...site.spec, code: 'NEW' } };
    const inventory = toInventory([], [], [renamed, facility]);
    expect(facilitySites(facility, inventory)).toContain('NEW');
    expect(siteFacilities(renamed, inventory)[0].facility?.metadata.uid).toBe('premises-uid');
    expect(siteFacilities(site, toInventory([], [], [{ ...facility, metadata: { ...facility.metadata, namespace: 'other' } }]))[0].facility).toBeUndefined();
    expect(siteFacilities({ ...site, metadata: { ...site.metadata, deletionTimestamp: '2026-01-02T00:00:00Z' } }, inventory)[0].facility).toBeUndefined();
  });

  it('resolves a recreated name to its replacement without an incarnation guarantee', () => {
    const replacement = { ...facility, metadata: { ...facility.metadata, uid: 'replacement' } };
    const inventory = toInventory([], [], [site, replacement]);
    expect(siteFacilities(site, inventory)[0].facility?.metadata.uid).toBe('replacement');
    expect(facilitySites(replacement, inventory)).toContain('EX3');
    expect(site.spec.facilityRefs).toEqual([{ name: 'premises-stable' }]);
    expect(siteFacilities(site, toInventory([], [], [site]))[0].facility).toBeUndefined();
    expect(siteFacilities(site, toInventory([], [], [{ ...replacement, metadata: { ...replacement.metadata, deletionTimestamp: '2026-01-02T00:00:00Z' } }]))[0].facility).toBeUndefined();
  });

  it('supports many Sites per premises without inferring equipment membership', () => {
    const other = { ...site, metadata: { ...site.metadata, name: 'other-site', uid: 'other-uid' }, spec: { ...site.spec, code: 'EX4' } };
    const inventory = toInventory([], [], [site, other, facility]);
    expect(facilitySites(facility, inventory)).toContain('EX3');
    expect(facilitySites(facility, inventory)).toContain('EX4');
    expect(inventory.slots).toEqual([]);
  });

  it.each([401, 403, 404, 503])('redacts relationship data after read failure %s', status => {
    const inventory = toInventory([], [], [site, facility]);
    const siteDenied = redactInventory(inventory, { Site: { status } });
    expect(facilitySites(facility, siteDenied)).toBe('Operational Site details unavailable');
    expect(siteDenied.physical.some(record => record.kind === 'Site')).toBe(false);
    const facilityDenied = redactInventory(inventory, { Facility: { status } });
    expect(siteFacilities(site, facilityDenied)).toEqual([{ state: 'Facility details unavailable' }]);
  });
});
