import { describe, expect, it } from 'vitest';
import { PAGES } from '../../scripts/screenshot-pages';
import { assertFixtureMode } from '../../scripts/fixture-mode';
import { deviceConnections } from './device-connections';
import { equipmentGeometry, equipmentPlacements } from './device-placement';
import { equipmentDepth } from './device-depth';
import { FIXTURE_NAMES, FIXTURE_RACKS, FIXTURE_SCENARIOS, fixtureInventory, inventoryFixture } from './inventory-fixtures';

const inventory = fixtureInventory();
const device = (name: string) => inventoryFixture.devices.find(item => item.metadata.name === name)!;
const physical = (kind: string) => inventoryFixture.physical.filter(record => record.kind === kind);

describe('preview inventory fixture', () => {
  it('has one facility with three 42U racks, two feeds and about fifteen devices', () => {
    expect(physical('Facility')).toHaveLength(1);
    expect(physical('Rack').map(rack => [rack.metadata.name, rack.spec.heightUnits])).toEqual(FIXTURE_RACKS.map(name => [name, 42]));
    expect(physical('PowerFeed')).toHaveLength(2);
    expect(inventoryFixture.devices.length).toBeGreaterThanOrEqual(14);
    expect(new Set(inventoryFixture.devices.map(item => item.spec.lifecycle))).toEqual(new Set([undefined, 'Decommissioning', 'Retired']));
  });

  it('uses unique names and only synthetic identities', () => {
    const names = [...inventoryFixture.devices, ...inventoryFixture.claims, ...inventoryFixture.physical].map(record => `${record.kind}/${record.metadata.name}`);
    expect(new Set(names).size).toBe(names.length);
    for (const name of names) expect(name.split('/')[1]).toMatch(/^example-/);
    const text = JSON.stringify(inventoryFixture);
    for (const address of text.match(/\b\d{1,3}(\.\d{1,3}){3}\b/g) ?? []) expect(address).toMatch(/^192\.0\.2\./);
    for (const mac of text.match(/\b([0-9a-f]{2}:){5}[0-9a-f]{2}\b/gi) ?? []) expect(mac.toLowerCase()).toMatch(/^00:00:5e:00:53:/);
  });

  it('references only recorded catalog entries, racks and the facility', () => {
    const types = new Set(physical('DeviceType').map(item => item.metadata.name));
    const makers = new Set(physical('Manufacturer').map(item => item.metadata.name));
    for (const type of physical('DeviceType')) expect(makers).toContain(type.spec.manufacturerRef?.name);
    for (const item of inventoryFixture.devices) {
      expect(types).toContain(item.spec.deviceTypeRef?.name);
      const report = item.spec.reportedPlacement;
      if (report?.rackRef) expect(FIXTURE_RACKS).toContain(report.rackRef.name);
      if (report) expect(report.facilityRef?.name).toBe(FIXTURE_NAMES.facility);
    }
  });

  it('covers templates for network, power, console and BMC ports', () => {
    const templates = physical('DeviceType').flatMap(type => type.spec.componentTemplates ?? []);
    for (const type of ['NetworkPort', 'PowerInlet', 'PowerOutlet', 'ConsolePort', 'FrontPort', 'RearPort']) expect(templates.map(item => item.type)).toContain(type);
    expect(templates.map(item => item.id)).toContain('bmc');
  });

  it('places equipment front, reversed, half-depth rear, overlapping, unpositioned and non-racked', () => {
    const entries = equipmentPlacements(inventory);
    const entry = (name: string) => entries.find(item => item.record.metadata.name === name)!;
    expect(entry(FIXTURE_NAMES.leaf).placement).toMatchObject({ face: 'rear', fullDepth: true });
    expect(entry(FIXTURE_NAMES.patchPanel).placement).toMatchObject({ face: 'rear', fullDepth: false });
    expect(entry(FIXTURE_NAMES.pdu).placement.scope).toBe('NonRackUnitMount');
    const rack = (name: string) => physical('Rack').find(item => item.metadata.name === name)!;
    expect(equipmentGeometry(entry('example-srv-a03-01'), rack('example-rack-a03'))).toBeUndefined();
    expect(entry('example-srv-a03-01').placement.rackRef?.name).toBe('example-rack-a03');
    expect(entries.find(item => item.record.metadata.name === 'example-srv-unplaced-01')).toBeUndefined();
    const a02 = entries.map(item => ({ item, geometry: equipmentGeometry(item, rack('example-rack-a02')) })).filter(({ item, geometry }) => item.placement.rackRef?.name === 'example-rack-a02' && geometry && item.kind === 'Device');
    const overlaps = a02.filter(({ geometry: a }, index) => a02.some(({ geometry: b }, other) => other !== index && a!.position <= b!.position + b!.height - 1 && b!.position <= a!.position + a!.height - 1));
    expect(overlaps.length).toBeGreaterThanOrEqual(2);
  });

  it('exercises every connection state on the server and lists the large switch first', () => {
    const server = deviceConnections(device(FIXTURE_NAMES.server), inventory);
    expect(new Set(server.links.map(link => link.state))).toEqual(new Set(['cabled', 'conflict', 'catalog']));
    expect(server.links.find(link => link.componentID === 'psu-1')?.trace.at(-1)?.peer).toMatchObject({ kind: 'PowerFeed', access: 'readable' });
    expect(server.links.find(link => link.componentID === 'bmc')?.trace[0]).toMatchObject({ state: 'cabled', peer: { name: 'example-mgmt-a01' } });
    const missing = deviceConnections(device('example-srv-a01-02'), inventory).links.find(link => link.componentID === 'eth1');
    expect(missing).toMatchObject({ state: 'unavailable', peer: { access: 'missing' } });
    const leaf = deviceConnections(device(FIXTURE_NAMES.leaf), inventory);
    expect(leaf.links.length).toBeGreaterThan(16);
    expect(leaf.links.filter(link => link.componentID === 'swp49' && link.state === 'cabled').map(link => link.cable?.laneCount)).toEqual([4, 4, 4, 4]);
    expect(leaf.links.some(link => link.state === 'none')).toBe(true);
  });

  it('exercises depth and faces: catalog, reported, unknown, reversed and opposite-face cables', () => {
    const entries = equipmentPlacements(inventory);
    const depth = (name: string) => equipmentDepth(entries.find(item => item.record.metadata.name === name)!, inventory);
    expect(depth(FIXTURE_NAMES.server)).toEqual({ full: true, source: 'catalog' });
    expect(depth(FIXTURE_NAMES.leaf)).toEqual({ full: true, source: 'reported' });
    expect(depth(FIXTURE_NAMES.patchPanel)).toEqual({ full: false, source: 'reported' });
    expect(depth('example-app-a01')).toEqual({ full: true, source: 'unknown' });
    const templates = physical('DeviceType').flatMap(type => type.spec.componentTemplates ?? []);
    expect(new Set(templates.map(item => item.face))).toEqual(new Set(['Front', 'Rear']));
    const leaf = deviceConnections(device(FIXTURE_NAMES.leaf), inventory).links;
    expect(leaf.find(link => link.componentID === 'swp1')).toMatchObject({ chassisFace: 'Front', rackFace: 'rear', checks: [] });
    const app = deviceConnections(device('example-app-a01'), inventory).links.find(link => link.componentID === 'net0');
    expect(app).toMatchObject({ state: 'cabled', rackFace: 'front', peer: { rackFace: 'rear' } });
    expect(app?.checks).toHaveLength(1);
  });

  it('denies whole collections in read scenarios', () => {
    const denied = fixtureInventory('power-feeds-denied');
    expect(denied.issues).toEqual({ PowerFeed: { status: 403 } });
    expect(denied.physical.some(record => record.kind === 'PowerFeed')).toBe(false);
    expect(deviceConnections(device(FIXTURE_NAMES.server), denied).links.find(link => link.componentID === 'psu-2')?.peer).toEqual({ kind: 'PowerFeed', access: 'denied' });
    expect(fixtureInventory('unknown-scenario').issues).toBeUndefined();
    for (const [scenario, kinds] of Object.entries(FIXTURE_SCENARIOS)) expect(Object.keys(fixtureInventory(scenario).issues ?? {})).toEqual(kinds);
  });
});

describe('screenshot page list', () => {
  it('names only records and scenarios the fixture contains', () => {
    const records = new Set([...inventoryFixture.devices, ...inventoryFixture.claims, ...inventoryFixture.physical].map(record => `${record.kind}/${record.metadata.name}`));
    expect(new Set(PAGES.map(page => page.name)).size).toBe(PAGES.length);
    for (const page of PAGES) {
      expect(page.name).toMatch(/^[a-z0-9-]+$/);
      const params = new URL(page.path, 'http://preview.invalid').searchParams;
      if (params.has('record')) expect(records).toContain(`${params.get('kind')}/${params.get('record')}`);
      if (params.has('fixture')) expect(Object.keys(FIXTURE_SCENARIOS)).toContain(params.get('fixture'));
      expect(params.has('theme')).toBe(false);
    }
  });
});

describe('fixture mode guard', () => {
  it('only compiles fixtures in fixtures mode and refuses them elsewhere', () => {
    expect(assertFixtureMode({ command: 'build', mode: 'production', flag: undefined })).toBe(false);
    expect(assertFixtureMode({ command: 'build', mode: 'fixtures', flag: 'true' })).toBe(true);
    expect(assertFixtureMode({ command: 'serve', mode: 'fixtures', flag: 'true' })).toBe(true);
    expect(() => assertFixtureMode({ command: 'build', mode: 'production', flag: 'true' })).toThrow(/--mode fixtures/);
    expect(() => assertFixtureMode({ command: 'serve', mode: 'development', flag: 'true' })).toThrow(/dev server/);
    expect(() => assertFixtureMode({ command: 'build', mode: 'fixtures', flag: '1' })).toThrow(/"true" or unset/);
    expect(assertFixtureMode({ command: 'serve', mode: 'production', isPreview: true, flag: 'true' })).toBe(false);
    expect(assertFixtureMode({ command: 'build', mode: 'fixtures', flag: undefined })).toBe(false);
  });
});
