import { describe, expect, it } from 'vitest';
import { CONTINUOUS_LOAD_FACTOR, feedCapacity, formatCurrent, formatPower, loadLevel, supplyPhase } from './power-capacity';
import {
  formatAge, freshness, loadTelemetrySource, noTelemetry, observedDisagrees, observesLoad, telemetryKey, telemetrySourceFor, valid,
  type Observed, type PowerTelemetry,
} from './telemetry';
import { MOCK_SCENARIOS, MOCK_TELEMETRY_MARKER, mockTelemetrySource, mockTelemetryValues } from './telemetry-mock';
import { classifyFeeds, feedPath, psuFeeds, type PsuFeed } from './power-paths';
import { FIXTURE_NAMES, fixtureInventory, inventoryFixture } from './inventory-fixtures';
import { PAGES, TELEMETRY_SCENARIOS } from '../../scripts/screenshot-pages';

const NOW = Date.parse('2026-11-22T12:00:00Z');
const origin = { collector: 'pdu' as const, name: 'example-collector', synthetic: true };
const at = (secondsAgo: number) => new Date(NOW - secondsAgo * 1000).toISOString();
const value = (number: number, secondsAgo = 30, window = 300): Observed<number> => ({ value: number, origin, observedAt: at(secondsAgo), staleAfterSeconds: window });

describe('feed capacity', () => {
  it('rates single-phase AC as V × A, with an 80% continuous limit', () => {
    const capacity = feedCapacity({ type: 'AC', voltageV: 208, currentA: 30, phase: 'Single' });
    expect(capacity).toMatchObject({ phase: 'single', ratedVA: 6240, ratedA: 30, continuousA: 24, formula: '208 V × 30 A = 6,240 VA' });
    expect(capacity.continuousVA).toBeCloseTo(4992);
    expect(CONTINUOUS_LOAD_FACTOR).toBe(0.8);
  });

  it('rates three-phase AC as √3 × V(line-to-line) × A', () => {
    const capacity = feedCapacity({ type: 'AC', voltageV: 415, currentA: 32, phase: '3-phase' });
    expect(capacity.phase).toBe('three');
    expect(capacity.ratedVA).toBeCloseTo(Math.sqrt(3) * 415 * 32);
    expect(capacity.ratedVA).toBeCloseTo(23001.6, 0);
    expect(capacity.continuousVA).toBeCloseTo(0.8 * Math.sqrt(3) * 415 * 32);
    expect(capacity.formula).toBe('√3 × 415 V × 32 A = 23,002 VA');
  });

  it('rates DC as V × A in watts', () => {
    expect(feedCapacity({ type: 'DC', voltageV: 48, currentA: 60 })).toMatchObject({ phase: 'dc', ratedVA: 2880, formula: '48 V × 60 A = 2,880 W' });
  });

  it('leaves the rating unknown when phase, voltage, current or the supply is not recorded', () => {
    expect(feedCapacity(undefined)).toEqual({ phase: 'unknown', missing: 'Supply not recorded' });
    const noPhase = feedCapacity({ type: 'AC', voltageV: 208, currentA: 30 });
    expect(noPhase.ratedVA).toBeUndefined();
    expect(noPhase.missing).toBe('Rating incomplete: phase not recorded');
    const noVoltage = feedCapacity({ type: 'AC', currentA: 30, phase: 'single' });
    expect(noVoltage.ratedVA).toBeUndefined();
    expect(noVoltage.missing).toBe('Rating incomplete: voltage not recorded');
    expect(supplyPhase({ type: 'AC', phase: 'split' })).toBe('unknown');
    expect(supplyPhase({ type: 'AC', phase: 'Three phase' })).toBe('three');
  });

  it('classifies load against the continuous limit: high above 80% of it, over above it', () => {
    const continuous = 4992;
    expect(loadLevel(0.8 * continuous, continuous)).toBe('normal');
    expect(loadLevel(0.8 * continuous + 1, continuous)).toBe('high');
    expect(loadLevel(continuous, continuous)).toBe('high');
    expect(loadLevel(continuous + 1, continuous)).toBe('over');
    expect(loadLevel(undefined, continuous)).toBeUndefined();
    expect(loadLevel(100, undefined)).toBeUndefined();
    expect(formatPower(4370, 'VA')).toBe('4.37 kVA');
    expect(formatPower(182)).toBe('182 W');
    expect(formatCurrent(0.915)).toBe('0.92 A');
  });
});

describe('telemetry values', () => {
  it('marks a value stale only after its collector window, and ages it', () => {
    expect(freshness(value(1, 299, 300), NOW)).toEqual({ state: 'fresh', ageSeconds: 299 });
    expect(freshness(value(1, 300, 300), NOW)).toEqual({ state: 'fresh', ageSeconds: 300 });
    expect(freshness(value(1, 301, 300), NOW)).toEqual({ state: 'stale', ageSeconds: 301 });
    expect(formatAge(40)).toBe('40 s ago');
    expect(formatAge(3 * 3600 + 420)).toBe('3 h ago');
    expect(formatAge(4 * 86400)).toBe('4 d ago');
  });

  it('treats missing or invalid values as not reported, never as 0', () => {
    expect(valid(undefined, NOW)).toBeUndefined();
    expect(valid({ ...value(5), staleAfterSeconds: 0 }, NOW)).toBeUndefined();
    expect(valid({ ...value(5), observedAt: 'yesterday' }, NOW)).toBeUndefined();
    expect(valid({ ...value(Number.NaN) }, NOW)).toBeUndefined();
    expect(valid(value(0), NOW)).toMatchObject({ value: 0, state: 'fresh' });
  });

  it('calls observed load on an unrecorded outlet a disagreement, but not stale, idle or unknown records', () => {
    const load: PowerTelemetry = { powerW: value(164), currentA: value(0.83) };
    expect(observesLoad(load, NOW)).toBe(true);
    expect(observedDisagrees('none', load, NOW)).toBe(true);
    expect(observedDisagrees('cabled', load, NOW)).toBe(false);
    // Cables denied: the record state is unknown, so nothing is compared.
    expect(observedDisagrees('unavailable', load, NOW)).toBe(false);
    expect(observedDisagrees('none', { powerW: value(164, 900, 300) }, NOW)).toBe(false);
    expect(observedDisagrees('none', { powerW: value(0), currentA: value(0) }, NOW)).toBe(false);
    expect(observedDisagrees('none', undefined, NOW)).toBe(false);
  });

  it('keys values by inventory identity', () => {
    expect(telemetryKey({ kind: 'PowerFeed', namespace: 'default', name: 'feed-a' })).toBe('PowerFeed/default/feed-a');
    expect(telemetryKey({ kind: 'Device', namespace: 'default', name: 'pdu-1', componentID: 'outlet-6' })).toBe('Device/default/pdu-1/outlet-6');
  });
});

describe('telemetry source selection', () => {
  const mock = () => mockTelemetrySource('normal', NOW);
  it('uses none unless the build is a fixture build with the mock loaded', () => {
    expect(telemetrySourceFor({ fixtures: false, scenario: 'high-load', mock })).toBe(noTelemetry);
    expect(telemetrySourceFor({ fixtures: false })).toBe(noTelemetry);
    expect(telemetrySourceFor({ fixtures: true })).toBe(noTelemetry);
    expect(telemetrySourceFor({ fixtures: true, scenario: 'none', mock })).toBe(noTelemetry);
    expect(telemetrySourceFor({ fixtures: true, scenario: 'stale', mock }).kind).toBe('mock');
  });

  it('loads none in a normal build, whatever the URL asks for', async () => {
    // The test build is not a fixture build: VITE_FLEET_FIXTURES is unset.
    expect(import.meta.env.VITE_FLEET_FIXTURES).not.toBe('true');
    await expect(loadTelemetrySource('?telemetry=high-load')).resolves.toBe(noTelemetry);
    expect(noTelemetry.read({ kind: 'PowerFeed', name: 'example-feed-a', namespace: 'default' })).toBeUndefined();
    expect(noTelemetry.description).toBe('No telemetry source configured');
  });
});

describe('mock telemetry (fixtures only)', () => {
  const feedA = { kind: 'PowerFeed' as const, namespace: 'default', name: 'example-feed-a' };
  it('is deterministic and identifies itself as synthetic', () => {
    const a = mockTelemetrySource('normal', NOW);
    const b = mockTelemetrySource('normal', NOW);
    expect(a.read(feedA)).toEqual(b.read(feedA));
    expect(a.id).toBe(MOCK_TELEMETRY_MARKER);
    expect(a.description).toMatch(/Synthetic preview telemetry/);
    expect(a.read(feedA)?.powerW?.origin.synthetic).toBe(true);
    expect(a.read(feedA)?.powerW?.series).toHaveLength(48);
    expect(mockTelemetrySource('no-such-scenario', NOW).scenario).toBe('normal');
  });

  it('covers normal, high, stale, missing and disagreeing scenarios', () => {
    const capacity = feedCapacity(inventoryFixture.physical.find(record => record.metadata.name === 'example-feed-a')!.spec.supply);
    const load = (scenario: string) => mockTelemetrySource(scenario, NOW).read(feedA)?.apparentPowerVA;
    expect(loadLevel(load('normal')!.value, capacity.continuousVA)).toBe('normal');
    expect(loadLevel(load('high-load')!.value, capacity.continuousVA)).toBe('high');
    expect(freshness(load('stale'), NOW)?.state).toBe('stale');
    expect(load('missing')).toBeUndefined();
    const inventory = fixtureInventory();
    const path = feedPath(inventory.physical.find(record => record.metadata.name === 'example-feed-a')!, inventory);
    const disagreeing = (scenario: string) => path.pdus.flatMap(pdu => pdu.tiles.filter(tile => observedDisagrees(tile.state, mockTelemetrySource(scenario, NOW).read({ kind: 'Device', namespace: 'default', name: pdu.device.metadata.name, componentID: tile.componentID }), NOW)).map(tile => tile.componentID));
    expect(disagreeing('mismatch')).toEqual(['outlet-6']);
    expect(disagreeing('normal')).toEqual([]);
  });

  it('names only identities the fixture records', () => {
    const components = new Map<string, string[]>([
      ...inventoryFixture.devices.map(item => [`Device/default/${item.metadata.name}`, (item.spec.components ?? []).map(component => component.id)] as [string, string[]]),
      ...inventoryFixture.physical.filter(item => item.kind === 'PowerFeed').map(item => [`PowerFeed/default/${item.metadata.name}`, (item.spec.terminations ?? []).map(component => component.id)] as [string, string[]]),
    ]);
    for (const scenario of MOCK_SCENARIOS) {
      const keys = [...mockTelemetryValues(scenario, NOW).keys()];
      expect(keys.length).toBeGreaterThan(0);
      for (const key of keys) {
        const [kind, namespace, name, componentID] = key.split('/');
        const recorded = components.get(`${kind}/${namespace}/${name}`);
        expect(recorded, key).toBeDefined();
        if (componentID) expect(recorded, key).toContain(componentID);
      }
    }
  });

  it('screenshot pages ask only for known telemetry scenarios', () => {
    expect(TELEMETRY_SCENARIOS).toEqual([...MOCK_SCENARIOS, 'none']);
    for (const page of PAGES) {
      const scenario = new URL(page.path, 'http://preview.invalid').searchParams.get('telemetry');
      if (scenario) expect(TELEMETRY_SCENARIOS).toContain(scenario);
    }
  });
});

describe('recorded feed paths and redundancy', () => {
  const psu = (name: string, feed?: string): PsuFeed => ({ componentID: name, name, ...(feed ? { feed: { name: feed, label: feed } } : { unknown: 'No cable recorded' }) });
  it('classifies PSUs by the feeds their recorded paths reach, never as verified', () => {
    expect(classifyFeeds([psu('a', 'f1'), psu('b', 'f2')])?.class).toBe('separate');
    expect(classifyFeeds([psu('a', 'f1'), psu('b', 'f1')])?.class).toBe('single');
    expect(classifyFeeds([psu('a', 'f1')])?.class).toBe('single');
    expect(classifyFeeds([psu('a', 'f1'), psu('b')])?.class).toBe('partial');
    expect(classifyFeeds([psu('a', 'f1'), psu('b', 'f2'), psu('c')])?.class).toBe('separate');
    expect(classifyFeeds([psu('a'), psu('b')])?.class).toBe('unknown');
    // Cables unreadable: unknown, not "no PSUs".
    expect(classifyFeeds(undefined)?.class).toBe('unknown');
    expect(classifyFeeds([])).toBeUndefined();
  });

  it('traces PSU → PDU outlet → PDU inlet → feed in the fixture', () => {
    const inventory = fixtureInventory();
    const device = (name: string) => inventoryFixture.devices.find(item => item.metadata.name === name)!;
    expect(psuFeeds(device(FIXTURE_NAMES.server), inventory)).toEqual([
      { componentID: 'psu-1', name: 'PSU 1', via: { name: FIXTURE_NAMES.pdu, label: 'PDU a01 (rear left)', outletID: 'outlet-1', outlet: 'Outlet 1' }, feed: { name: 'example-feed-a', label: 'Feed A' } },
      { componentID: 'psu-2', name: 'PSU 2', feed: { name: 'example-feed-b', label: 'Feed B' } },
    ]);
    expect(classifyFeeds(psuFeeds(device(FIXTURE_NAMES.pdu), inventory))?.class).toBe('single');
    expect(classifyFeeds(psuFeeds(device('example-srv-a01-02'), inventory))?.class).toBe('partial');
    expect(classifyFeeds(psuFeeds(device('example-sto-a01-04'), inventory))?.class).toBe('unknown');
  });

  it('names no feed when power feeds are denied, and is unknown when Cables are denied', () => {
    const device = inventoryFixture.devices.find(item => item.metadata.name === FIXTURE_NAMES.server)!;
    const denied = psuFeeds(device, fixtureInventory('power-feeds-denied'))!;
    expect(denied.every(item => !item.feed)).toBe(true);
    expect(JSON.stringify(denied)).not.toMatch(/Feed A|Feed B|example-feed/);
    expect(psuFeeds(device, fixtureInventory('cables-denied'))).toBeUndefined();
  });

  it('lists each device on a feed once, with PDUs as strips', () => {
    const inventory = fixtureInventory();
    const path = feedPath(inventory.physical.find(record => record.metadata.name === 'example-feed-a')!, inventory);
    expect(path.pdus.map(pdu => pdu.device.metadata.name)).toEqual([FIXTURE_NAMES.pdu]);
    expect(path.pdus[0].tiles[0].type).toBe('PowerInlet');
    expect(path.devices.map(row => row.device.metadata.name)).toEqual([FIXTURE_NAMES.server, 'example-srv-a01-02', FIXTURE_NAMES.leaf]);
    const cablesDenied = feedPath(inventory.physical.find(record => record.metadata.name === 'example-feed-a')!, fixtureInventory('cables-denied'));
    expect(cablesDenied.terminations.map(tile => tile.state)).toEqual(['unavailable', 'unavailable', 'unavailable', 'unavailable']);
    expect(cablesDenied.devices).toEqual([]);
  });
});
