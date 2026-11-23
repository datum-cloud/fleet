import { describe, expect, it } from 'vitest';
import type { Device, PhysicalRecord, RackFace } from '../../../shared/fleet-api';
import { toInventory, type Inventory } from '../inventory';
import { depthClaims, depthDevices, depthRecords, DEPTH_RACK } from './depth-fixtures';
import { depthLabel, deviceDepth, equipmentDepth, type Depth } from './device-depth';
import { equipmentPlacements, reportedPlacementLabel, type EquipmentPlacement } from './device-placement';
import { componentChassisFace, componentRackFace, coveredUnits, deviceMountedFace, emptyRuns, faceOccupancy, MIN_EMPTY_RUN, oppositeFace, RACK_FACES, rackOccupants, runRange, type EmptyRun, type RackOccupant } from './rack-faces';

// Synthetic records only.
const rack = depthRecords.find(record => record.kind === 'Rack')!;
const byName = (name: string) => depthDevices.find(record => record.metadata.name === name)!;
const inventory = (devices: Device[] = depthDevices, physical: PhysicalRecord[] = depthRecords, issues?: Inventory['issues']): Inventory => ({ ...toInventory(depthClaims, devices, physical), issues });
const occupancy = (data: Inventory) => rackOccupants(equipmentPlacements(data).filter(entry => entry.placement.rackRef?.name === DEPTH_RACK), rack, data);
const names = (data: Inventory, side: RackFace) => faceOccupancy(occupancy(data), side).map(item => `${item.entry.record.metadata.name}${item.far ? ' (far)' : ''}${item.conflicts.length ? ' !' : ''}`).sort();
const placed = (name: string, position: number, height: number, face: RackFace, extra: Partial<Device['spec']> = {}): Device => {
  const base = byName('compute-07');
  return { ...base, metadata: { ...base.metadata, name }, spec: { ...base.spec, claimRef: { name: `${name}-install` }, displayName: name, ...extra, reportedPlacement: { ...base.spec.reportedPlacement!, position, height, face, ...extra.reportedPlacement } } };
};

describe('componentRackFace', () => {
  it.each([
    ['front', 'Front', 'front'], ['front', 'Rear', 'rear'],
    ['rear', 'Front', 'rear'], ['rear', 'Rear', 'front'],
    [undefined, 'Front', undefined], ['front', undefined, undefined], [undefined, undefined, undefined],
  ] as const)('mounted %s with a chassis %s port lands on the rack %s', (mounted, chassis, expected) => {
    expect(componentRackFace(mounted, chassis)).toBe(expected);
  });
  it('flips faces', () => {
    expect(oppositeFace('front')).toBe('rear');
    expect(oppositeFace('rear')).toBe('front');
  });
  it('puts a reversed switch\'s chassis-front ports on the rack rear', () => {
    const data = inventory();
    const leaf = byName('leaf-01');
    expect(deviceMountedFace(leaf, data)).toBe('rear');
    expect(componentChassisFace(leaf, data, 'swp1')).toBe('Front');
    expect(componentRackFace(deviceMountedFace(leaf, data), componentChassisFace(leaf, data, 'swp1'))).toBe('rear');
    expect(componentRackFace(deviceMountedFace(leaf, data), componentChassisFace(leaf, data, 'psu-1'))).toBe('front');
  });
  it('leaves the chassis face unknown without a readable catalog or a template', () => {
    const leaf = byName('leaf-01');
    expect(componentChassisFace(leaf, inventory(undefined, undefined, { DeviceType: { status: 403 } }), 'swp1')).toBeUndefined();
    expect(componentChassisFace(leaf, inventory(undefined, undefined, { Manufacturer: { status: 403 } }), 'swp1')).toBeUndefined();
    expect(componentChassisFace(leaf, inventory(), 'not-a-template')).toBeUndefined();
    expect(componentChassisFace(byName('appliance-03'), inventory(), 'net0')).toBeUndefined();
  });
});

describe('deviceDepth', () => {
  const data = inventory();
  it('reads full depth from the catalog', () => {
    expect(deviceDepth(byName('compute-07'), data)).toEqual({ full: true, source: 'catalog' });
    expect(depthLabel(deviceDepth(byName('compute-07'), data))).toBe('Full depth (catalog)');
  });
  it('never defaults a DeviceType without fullDepth: depth is unknown unless the Device records it', () => {
    const edge = { ...byName('compute-07'), spec: { ...byName('compute-07').spec, deviceTypeRef: { name: 'example-edge-1u' } } };
    expect(deviceDepth(edge, data)).toEqual({ full: true, source: 'unknown' });
    expect(deviceDepth({ ...edge, spec: { ...edge.spec, modelShape: { fullDepth: false } } }, data)).toEqual({ full: false, source: 'device' });
  });
  it('reads half depth from the catalog', () => {
    expect(deviceDepth(byName('panel-01'), data)).toEqual({ full: false, source: 'catalog' });
  });
  it('prefers the report, then the Device shape, over the catalog', () => {
    const panel = byName('panel-01');
    expect(deviceDepth({ ...panel, spec: { ...panel.spec, modelShape: { fullDepth: true } } }, data)).toEqual({ full: true, source: 'device' });
    expect(deviceDepth({ ...panel, spec: { ...panel.spec, modelShape: { fullDepth: true }, reportedPlacement: { ...panel.spec.reportedPlacement!, fullDepth: false } } }, data)).toEqual({ full: false, source: 'reported' });
  });
  it.each([
    ['no DeviceType reference', byName('appliance-03'), {}],
    ['DeviceType missing', { ...byName('compute-07'), spec: { ...byName('compute-07').spec, deviceTypeRef: { name: 'absent' } } }, {}],
    ['DeviceType unreadable', byName('compute-07'), { DeviceType: { status: 403 } }],
  ] as const)('is unknown, counted as full depth, with %s', (_, device, issues) => {
    const depth = deviceDepth(device, inventory(undefined, undefined, issues));
    expect(depth).toEqual({ full: true, source: 'unknown' });
    expect(depthLabel(depth)).toBe('Depth unknown');
  });
  it('lets a claim fallback borrow depth only when every registration agrees', () => {
    const claim = { ...depthClaims[0], spec: { ...depthClaims[0].spec, placement: { scope: 'RackUnits' as const, rackRef: { name: DEPTH_RACK }, position: 5, height: 1, face: 'front' as const } } };
    const unreported = (name: string, type: string) => ({ ...byName('compute-07'), metadata: { ...byName('compute-07').metadata, name }, spec: { ...byName('compute-07').spec, reportedPlacement: undefined, deviceTypeRef: { name: type } } });
    const agree = toInventory([claim], [unreported('a', 'example-panel-12'), unreported('b', 'example-panel-12')], depthRecords);
    expect(equipmentDepth(equipmentPlacements(agree)[0], agree)).toEqual({ full: false, source: 'catalog' });
    const disagree = toInventory([claim], [unreported('a', 'example-panel-12'), unreported('b', 'example-leaf-48')], depthRecords);
    expect(equipmentDepth(equipmentPlacements(disagree)[0], disagree).source).toBe('unknown');
    const none = toInventory([claim], [], depthRecords);
    expect(equipmentDepth(equipmentPlacements(none)[0], none)).toEqual({ full: true, source: 'unknown' });
  });
  it('labels placements with the resolved depth', () => {
    const entry = equipmentPlacements(data).find(item => item.record.metadata.name === 'appliance-03')!;
    expect(reportedPlacementLabel(entry, data)).toContain('Depth unknown');
  });
});

describe('face-aware rack occupancy', () => {
  it('draws the fixture rack on the faces each depth occupies', () => {
    const data = inventory();
    expect(names(data, 'front')).toEqual(['appliance-03', 'compute-07', 'leaf-01 (far)', 'panel-01']);
    expect(names(data, 'rear')).toEqual(['appliance-03 (far) !', 'compute-07 (far)', 'console-01 !', 'leaf-01', 'panel-02']);
  });
  it('lets half-depth devices share units on opposite faces without disagreement', () => {
    const data = inventory([placed('half-front', 5, 1, 'front', { deviceTypeRef: { name: 'example-panel-12' } }), placed('half-rear', 5, 1, 'rear', { deviceTypeRef: { name: 'example-panel-12' } })]);
    expect(names(data, 'front')).toEqual(['half-front']);
    expect(names(data, 'rear')).toEqual(['half-rear']);
  });
  it('flags a full-depth device against a half-depth device on its far face', () => {
    const data = inventory([placed('full-front', 5, 2, 'front'), placed('half-rear', 6, 1, 'rear', { deviceTypeRef: { name: 'example-panel-12' } })]);
    expect(names(data, 'front')).toEqual(['full-front']);
    expect(names(data, 'rear')).toEqual(['full-front (far) !', 'half-rear !']);
  });
  it('flags two full-depth devices on both faces', () => {
    const data = inventory([placed('one', 5, 1, 'front'), placed('two', 5, 1, 'rear')]);
    expect(names(data, 'front')).toEqual(['one !', 'two (far) !']);
    expect(names(data, 'rear')).toEqual(['one (far) !', 'two !']);
  });
  it('treats unknown depth as full depth, so the far face is never shown as clear', () => {
    const data = inventory([placed('mystery', 5, 1, 'front', { deviceTypeRef: undefined }), placed('half-rear', 5, 1, 'rear', { deviceTypeRef: { name: 'example-panel-12' } })]);
    expect(names(data, 'rear')).toEqual(['half-rear !', 'mystery (far) !']);
    expect(faceOccupancy(occupancy(data), 'rear').find(item => item.far)?.depth.source).toBe('unknown');
  });
  it('does not flag adjacent units', () => {
    const data = inventory([placed('lower', 5, 2, 'front'), placed('upper', 7, 1, 'front')]);
    expect(names(data, 'front')).toEqual(['lower', 'upper']);
  });
  it.each([{ position: 5.5, height: 1 }, { position: 42, height: 2 }, { position: 5, height: 0.5 }])('still draws only whole, in-range positions: %j', ({ position, height }) => {
    const data = inventory([placed('odd', position, height, 'front')]);
    expect(occupancy(data)).toEqual([]);
  });
});

describe('empty runs', () => {
  // Occupants built directly, so each case states its own geometry.
  const DEPTHS: Record<'full' | 'half' | 'unknown', Depth> = { full: { full: true, source: 'catalog' }, half: { full: false, source: 'catalog' }, unknown: { full: true, source: 'unknown' } };
  const at = (key: string, position: number, height: number, mounted: RackFace, depth: keyof typeof DEPTHS): RackOccupant => ({
    entry: { key } as EquipmentPlacement, position, height, mounted, depth: DEPTHS[depth], faces: DEPTHS[depth].full ? ['front', 'rear'] : [mounted],
  });
  const spans = (runs: EmptyRun[]) => runs.map(runRange);

  it('collapses runs of three or more units, top first, and keeps shorter gaps as units', () => {
    const occupants = [at('a', 10, 1, 'front', 'full'), at('b', 13, 2, 'front', 'full'), at('c', 38, 1, 'front', 'full')];
    // U11–U12 is two units: not collapsed. U1 and U42 edges are included.
    expect(spans(emptyRuns(occupants, 42, RACK_FACES))).toEqual(['U39–U42', 'U15–U37', 'U1–U9']);
    expect(emptyRuns(occupants, 42, RACK_FACES)[1]).toEqual({ top: 37, bottom: 15, units: 23, key: 'U15-U37' });
    expect(MIN_EMPTY_RUN).toBe(3);
  });

  it('ends a run exactly at the units next to a device, above and below', () => {
    const runs = emptyRuns([at('a', 20, 2, 'front', 'full')], 42, ['front']);
    expect(runs.map(run => [run.bottom, run.top])).toEqual([[22, 42], [1, 19]]);
    // A device at the top or bottom unit leaves no empty unit outside the rack.
    expect(spans(emptyRuns([at('top', 42, 1, 'front', 'full'), at('bottom', 1, 1, 'front', 'full')], 42, ['front']))).toEqual(['U2–U41']);
    expect(emptyRuns([at('all', 1, 42, 'front', 'full')], 42, RACK_FACES)).toEqual([]);
  });

  it('computes each face on its own: a half-depth device leaves the other face empty', () => {
    const occupants = [at('panel', 20, 1, 'rear', 'half')];
    expect(spans(emptyRuns(occupants, 42, ['front']))).toEqual(['U1–U42']);
    expect(spans(emptyRuns(occupants, 42, ['rear']))).toEqual(['U21–U42', 'U1–U19']);
  });

  it('aligns both faces by collapsing only units that are empty on both', () => {
    const occupants = [at('front-panel', 30, 1, 'front', 'half'), at('rear-panel', 10, 2, 'rear', 'half')];
    expect(spans(emptyRuns(occupants, 42, ['front']))).toEqual(['U31–U42', 'U1–U29']);
    expect(spans(emptyRuns(occupants, 42, ['rear']))).toEqual(['U12–U42', 'U1–U9']);
    // Front + rear: a unit occupied on either face breaks the run on both, so rows line up.
    const both = emptyRuns(occupants, 42, RACK_FACES);
    expect(spans(both)).toEqual(['U31–U42', 'U12–U29', 'U1–U9']);
    for (const run of both) for (let unit = run.bottom; unit <= run.top; unit++) expect(coveredUnits(occupants, RACK_FACES).has(unit)).toBe(false);
  });

  it('counts overlapping placements once, and never starts a run inside either', () => {
    const occupants = [at('a', 20, 3, 'front', 'full'), at('b', 21, 4, 'front', 'full')];
    expect([...coveredUnits(occupants, ['front'])].sort((x, y) => x - y)).toEqual([20, 21, 22, 23, 24]);
    expect(spans(emptyRuns(occupants, 42, ['front']))).toEqual(['U25–U42', 'U1–U19']);
  });

  it('treats unknown depth as occupying both faces, from the records', () => {
    // depth-fixtures: appliance-03 has no DeviceType, so its depth is unknown, mounted front at U20–U21.
    const data = inventory();
    const appliance = occupancy(data).find(item => item.entry.record.metadata.name === 'appliance-03')!;
    expect(appliance.depth.source).toBe('unknown');
    expect(coveredUnits([appliance], ['rear'])).toEqual(new Set([20, 21]));
    expect(spans(emptyRuns([appliance], 42, ['rear']))).toEqual(['U22–U42', 'U1–U19']);
    // The whole depth rack: U10, U20–U21, U40 and U42 are recorded on both faces.
    expect(spans(emptyRuns(occupancy(data), 42, RACK_FACES))).toEqual(['U22–U39', 'U11–U19', 'U1–U9']);
    expect(spans(emptyRuns(occupancy(data), 42, ['front']))).toEqual(spans(emptyRuns(occupancy(data), 42, ['rear'])));
  });
});
