import { describe, expect, it } from 'vitest';
import type { Device, PhysicalRecord, RackFace } from '../../../shared/fleet-api';
import { toInventory, type Inventory } from '../inventory';
import { depthClaims, depthDevices, depthRecords, DEPTH_RACK } from './depth-fixtures';
import { depthLabel, deviceDepth, equipmentDepth } from './device-depth';
import { equipmentPlacements, reportedPlacementLabel } from './device-placement';
import { componentChassisFace, componentRackFace, deviceMountedFace, faceOccupancy, oppositeFace, rackOccupants } from './rack-faces';

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
