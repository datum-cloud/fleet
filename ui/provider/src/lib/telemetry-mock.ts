// Synthetic power telemetry for the fixture preview ONLY.
//
// This module is imported lazily by loadTelemetrySource() behind the
// build-time fixture flag, so a normal build (staging, production) never
// contains it; src/test/production-bundle.test.ts builds the production bundle
// and fails if MOCK_TELEMETRY_MARKER appears in it. Do not import it from any
// other module.
//
// Values are keyed by the synthetic identities in inventory-fixtures.ts and
// are deterministic: ages are fixed offsets from the time the source is
// created, and the 24-hour series is a fixed curve.
//
// Scenarios (`?telemetry=<name>` on a fixture preview URL):
//   normal     every feed well inside its limit; PSUs report OK
//   high-load  Feed A above 80% of its continuous (derated) limit
//   stale      Feed A, its PDU and the PSUs last reported hours ago
//   missing    only some PDU outlets report; feeds and PSUs do not
//   mismatch   load is observed on outlets that no Cable records
//   none       (handled by the loader) the production view: nothing reported

import type { HealthValue, Observed, OutputState, PowerTelemetry, SeriesPoint, TelemetryOrigin, TelemetrySource, TelemetrySubject } from './telemetry';
import { telemetryKey } from './telemetry';

/** Present only in this module; the production bundle test looks for it. */
// Typed as string so declaration files do not carry the literal.
export const MOCK_TELEMETRY_MARKER: string = 'fleet-synthetic-telemetry-preview-only';

export const MOCK_SCENARIOS = ['normal', 'high-load', 'stale', 'missing', 'mismatch'] as const;
export type MockScenario = (typeof MOCK_SCENARIOS)[number];

const NS = 'default';
const PDU_COLLECTOR: TelemetryOrigin = { collector: 'pdu', name: 'synthetic-pdu-collector', synthetic: true };
const BMC_COLLECTOR: TelemetryOrigin = { collector: 'bmc', name: 'synthetic-bmc-collector', synthetic: true };
// Collector windows: PDUs are polled every 60 s, BMCs every 120 s; a value is
// stale after five polls.
const PDU_WINDOW = 300;
const BMC_WINDOW = 600;
const VOLTS = 208;
const POWER_FACTOR = 0.95;

// Per-PSU input power in the normal scenario, W.
const PSU_DRAW: Record<string, Record<string, number>> = {
  'example-srv-a01-01': { 'psu-1': 182, 'psu-2': 176 },
  'example-srv-a01-02': { 'psu-1': 338 },
  'example-leaf-a01': { 'psu-1': 151, 'psu-2': 147 },
};
// PDU a01 outlets and the PSU each one is cabled to in inventory-fixtures.ts.
const PDU = 'example-pdu-a01';
const PDU_OUTLETS: Record<string, [string, string] | undefined> = {
  'outlet-1': ['example-srv-a01-01', 'psu-1'],
  'outlet-2': ['example-srv-a01-02', 'psu-1'],
  'outlet-3': ['example-leaf-a01', 'psu-1'],
  'outlet-4': undefined, 'outlet-5': undefined, 'outlet-6': undefined, 'outlet-7': undefined, 'outlet-8': undefined,
};
// Feed terminations cabled directly to a PSU, and the PDU inlet on Feed A.
const FEED_B: Record<string, [string, string] | undefined> = {
  'outlet-1': ['example-srv-a01-01', 'psu-2'],
  'outlet-2': ['example-leaf-a01', 'psu-2'],
  'outlet-3': undefined, 'outlet-4': undefined,
};

interface Plan {
  /** Multiplies the PSUs behind PDU a01 (Feed A). */
  feedAScale: number;
  /** Seconds since Feed A, the PDU and PSUs last reported. */
  feedAAge: number;
  /** Load seen on outlets that no Cable records, W. */
  unrecorded: { kind: 'PowerFeed' | 'Device'; name: string; componentID: string; watts: number }[];
  /** Which PDU outlets report at all; undefined means every outlet. */
  outletsReporting?: string[];
  feeds: boolean;
  psus: boolean;
  psuFault?: [string, string, string];
}

const PLANS: Record<MockScenario, Plan> = {
  normal: { feedAScale: 1, feedAAge: 40, unrecorded: [], feeds: true, psus: true },
  // 6.2× the normal load puts Feed A near 88% of its 4,992 VA continuous limit.
  'high-load': { feedAScale: 6.2, feedAAge: 35, unrecorded: [], feeds: true, psus: true, psuFault: ['example-srv-a01-02', 'psu-1', 'Input over temperature'] },
  stale: { feedAScale: 1, feedAAge: 3 * 3600 + 420, unrecorded: [], feeds: true, psus: true },
  missing: { feedAScale: 1, feedAAge: 50, unrecorded: [], outletsReporting: ['outlet-1', 'outlet-2', 'outlet-3'], feeds: false, psus: false },
  mismatch: {
    feedAScale: 1, feedAAge: 40, feeds: true, psus: true,
    unrecorded: [{ kind: 'Device', name: PDU, componentID: 'outlet-6', watts: 164 }, { kind: 'PowerFeed', name: 'example-feed-a', componentID: 'outlet-3', watts: 96 }],
  },
};

/** A fixed 24-hour curve ending at `value`: 48 half-hourly points. */
function series(value: number, end: number, seed: number): SeriesPoint[] {
  return Array.from({ length: 48 }, (_, index) => {
    const step = 47 - index;
    const wave = index === 47 ? 1 : 1 + 0.14 * Math.sin((index / 48) * 2 * Math.PI + seed) + 0.04 * Math.sin(index * 1.7 + seed);
    return { at: new Date(end - step * 1800_000).toISOString(), value: Math.round(value * wave * 100) / 100 };
  });
}

function observed<T>(value: T, origin: TelemetryOrigin, at: number, window: number, history?: SeriesPoint[]): Observed<T> {
  return { value, origin, observedAt: new Date(at).toISOString(), staleAfterSeconds: window, ...(history ? { series: history } : {}) };
}

/** Power, apparent power and current for one metered point, from its real power. */
function metered(watts: number, origin: TelemetryOrigin, at: number, window: number, seed: number, state?: OutputState): PowerTelemetry {
  const va = watts / POWER_FACTOR;
  return {
    powerW: observed(Math.round(watts), origin, at, window, series(watts, at, seed)),
    apparentPowerVA: observed(Math.round(va), origin, at, window),
    currentA: observed(Math.round((va / VOLTS) * 100) / 100, origin, at, window, series(va / VOLTS, at, seed)),
    ...(state ? { outputState: observed(state, origin, at, window) } : {}),
  };
}

const sum = (values: number[]) => values.reduce((total, value) => total + value, 0);

const scenarioOf = (requested: string | undefined): MockScenario => (MOCK_SCENARIOS as readonly string[]).includes(requested ?? '') ? requested as MockScenario : 'normal';

/** Every reading of a scenario, by telemetryKey(). */
export function mockTelemetryValues(scenario: MockScenario, now: number): Map<string, PowerTelemetry> {
  const plan = PLANS[scenario];
  const values = new Map<string, PowerTelemetry>();
  const set = (subject: Omit<TelemetrySubject, 'namespace'>, reading: PowerTelemetry) => values.set(telemetryKey({ ...subject, namespace: NS }), reading);
  const aAt = now - plan.feedAAge * 1000;
  const bAt = now - 25_000;
  const draw = (device: string, psu: string, scale: number) => (PSU_DRAW[device]?.[psu] ?? 0) * scale;
  let seed = 0;

  // PSUs, from the BMC collector. Those behind PDU a01 share Feed A's scale and age.
  if (plan.psus) {
    for (const [device, psus] of Object.entries(PSU_DRAW)) {
      const viaA = Object.values(PDU_OUTLETS).filter(Boolean).map(entry => entry!.join('/'));
      let total = 0;
      let latest = 0;
      for (const psu of Object.keys(psus)) {
        const onA = viaA.includes(`${device}/${psu}`);
        const watts = draw(device, psu, onA ? plan.feedAScale : 1);
        const at = onA ? aAt : bAt;
        const fault = plan.psuFault && plan.psuFault[0] === device && plan.psuFault[1] === psu;
        const health: HealthValue = fault ? { state: 'Fault', reason: plan.psuFault![2] } : { state: 'OK' };
        set({ kind: 'Device', name: device, componentID: psu }, {
          powerW: observed(Math.round(watts), BMC_COLLECTOR, at, BMC_WINDOW, series(watts, at, ++seed)),
          health: observed(health, BMC_COLLECTOR, at, BMC_WINDOW),
        });
        total += watts;
        latest = Math.max(latest, at);
      }
      set({ kind: 'Device', name: device }, { powerW: observed(Math.round(total), BMC_COLLECTOR, Math.min(latest, aAt), BMC_WINDOW) });
    }
  }

  // PDU a01 outlets and its total, from the PDU collector.
  const outletWatts = (outlet: string) => {
    const entry = PDU_OUTLETS[outlet];
    const extra = plan.unrecorded.find(item => item.kind === 'Device' && item.componentID === outlet)?.watts ?? 0;
    return (entry ? draw(entry[0], entry[1], plan.feedAScale) : 0) + extra;
  };
  const reporting = plan.outletsReporting ?? Object.keys(PDU_OUTLETS);
  for (const outlet of reporting) set({ kind: 'Device', name: PDU, componentID: outlet }, metered(outletWatts(outlet), PDU_COLLECTOR, aAt, PDU_WINDOW, ++seed, 'On'));
  const pduWatts = sum(Object.keys(PDU_OUTLETS).map(outletWatts));
  if (!plan.outletsReporting) {
    set({ kind: 'Device', name: PDU }, metered(pduWatts, PDU_COLLECTOR, aAt, PDU_WINDOW, ++seed));
    set({ kind: 'Device', name: PDU, componentID: 'inlet' }, metered(pduWatts, PDU_COLLECTOR, aAt, PDU_WINDOW, seed));
  }

  // Feeds, from the meter at each termination (the PDU collector).
  if (plan.feeds) {
    const feedA: Record<string, number> = { 'outlet-1': pduWatts, 'outlet-2': 0, 'outlet-3': 0, 'outlet-4': 0 };
    for (const item of plan.unrecorded) if (item.kind === 'PowerFeed') feedA[item.componentID] += item.watts;
    for (const [termination, watts] of Object.entries(feedA)) set({ kind: 'PowerFeed', name: 'example-feed-a', componentID: termination }, metered(watts, PDU_COLLECTOR, aAt, PDU_WINDOW, ++seed, 'On'));
    set({ kind: 'PowerFeed', name: 'example-feed-a' }, metered(sum(Object.values(feedA)), PDU_COLLECTOR, aAt, PDU_WINDOW, ++seed));
    const feedB = Object.fromEntries(Object.entries(FEED_B).map(([termination, entry]) => [termination, entry ? draw(entry[0], entry[1], 1) : 0]));
    for (const [termination, watts] of Object.entries(feedB)) set({ kind: 'PowerFeed', name: 'example-feed-b', componentID: termination }, metered(watts, PDU_COLLECTOR, bAt, PDU_WINDOW, ++seed, 'On'));
    set({ kind: 'PowerFeed', name: 'example-feed-b' }, metered(sum(Object.values(feedB)), PDU_COLLECTOR, bAt, PDU_WINDOW, ++seed));
  }

  return values;
}

/** The mock source for a scenario; unknown names fall back to `normal`. */
export function mockTelemetrySource(requested: string | undefined, now = Date.now()): TelemetrySource {
  const scenario = scenarioOf(requested);
  const values = mockTelemetryValues(scenario, now);
  return {
    kind: 'mock',
    id: MOCK_TELEMETRY_MARKER,
    scenario,
    description: `Synthetic preview telemetry (${scenario}), fixture builds only`,
    read: subject => values.get(telemetryKey({ ...subject, namespace: subject.namespace ?? NS })),
  };
}
