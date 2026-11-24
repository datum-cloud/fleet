// Observed power telemetry, keyed by inventory identity.
//
// Telemetry is an observation with a source and a time. It is never a record:
// it does not change Cable or PowerFeed records, establish a connection, or
// verify redundancy. Time series belong in the platform metrics store, not in
// CRD status (see docs/development/inventory-telemetry.md, proposed).
//
// Two sources exist:
//   none  the default in every build. Everything reads "Not reported".
//   mock  synthetic values for the fixture preview only. It is loaded lazily
//         behind the same build-time flag as the fixture inventory
//         (VITE_FLEET_FIXTURES, honoured only in --mode fixtures), so a normal
//         build never contains it. src/test/production-bundle.test.ts proves it.
// There is no real metrics client yet.

import { createContext } from 'react';

/** Which collector wrote a value. Each kind has one authoritative writer. */
export type TelemetryCollector = 'pdu' | 'bmc';

export interface TelemetryOrigin {
  collector: TelemetryCollector;
  /** The collector instance, as an opaque name. Never an endpoint or credential. */
  name: string;
  /** Synthetic preview data, never shown outside fixture builds. */
  synthetic?: boolean;
}

export interface SeriesPoint {
  at: string;
  value: number;
}

/** One observed value: what, from where, when, and for how long it is fresh. */
export interface Observed<T> {
  value: T;
  origin: TelemetryOrigin;
  /** RFC 3339 time of the observation. */
  observedAt: string;
  /** The collector's own freshness window. Required: the UI never picks one. */
  staleAfterSeconds: number;
  /** Optional 24-hour history, oldest first. Shown only in cards. */
  series?: SeriesPoint[];
}

export type OutputState = 'On' | 'Off';
export interface HealthValue {
  state: 'OK' | 'Fault' | 'Absent';
  /** A short collector reason, such as "Input lost" or "Breaker tripped". */
  reason?: string;
}

/** Power telemetry for one subject. Any field may be absent: absent is "Not reported". */
export interface PowerTelemetry {
  /** Real power, W. */
  powerW?: Observed<number>;
  /** Apparent power, VA. Compared with a feed's rated VA. */
  apparentPowerVA?: Observed<number>;
  /** RMS current, A. */
  currentA?: Observed<number>;
  /** Switched outlet state, for PDU outlets. */
  outputState?: Observed<OutputState>;
  /** PSU status or outlet breaker state, as the collector reports it. */
  health?: Observed<HealthValue>;
}

/**
 * An inventory identity: a PowerFeed, a Device (a PDU or any other device),
 * or one component of either (a feed termination, a PDU outlet, a PSU).
 */
export interface TelemetrySubject {
  kind: 'PowerFeed' | 'Device';
  namespace?: string;
  name: string;
  componentID?: string;
}

export function telemetryKey(subject: TelemetrySubject) {
  return [subject.kind, subject.namespace ?? '', subject.name, subject.componentID].filter(part => part !== undefined).join('/');
}

export interface TelemetrySource {
  kind: 'none' | 'mock';
  /** A stable identifier for the implementation. */
  id: string;
  /** One line for views: "No telemetry source configured". */
  description: string;
  /** The mock scenario, for the preview only. */
  scenario?: string;
  read(subject: TelemetrySubject): PowerTelemetry | undefined;
}

export const NO_TELEMETRY_SOURCE = 'No telemetry source configured';

/** The default source: nothing is reported. */
export const noTelemetry: TelemetrySource = Object.freeze({
  kind: 'none' as const,
  id: 'none',
  description: NO_TELEMETRY_SOURCE,
  read: () => undefined,
});

export const NOT_REPORTED = 'Not reported';

// ---- Evaluation ----

export interface Freshness {
  state: 'fresh' | 'stale';
  /** Seconds since observedAt, never negative. */
  ageSeconds: number;
}

/**
 * Whether a value is still inside its collector's window. A value without a
 * parseable time or a positive window is invalid and treated as not reported.
 */
export function freshness(observed: Pick<Observed<unknown>, 'observedAt' | 'staleAfterSeconds'> | undefined, now: number): Freshness | undefined {
  if (!observed) return undefined;
  const at = Date.parse(observed.observedAt);
  if (!Number.isFinite(at) || !(observed.staleAfterSeconds > 0)) return undefined;
  const ageSeconds = Math.max(0, Math.round((now - at) / 1000));
  return { state: ageSeconds > observed.staleAfterSeconds ? 'stale' : 'fresh', ageSeconds };
}

/** A value only when it is valid; otherwise undefined, which views show as "Not reported". */
export function valid<T>(observed: Observed<T> | undefined, now: number): (Observed<T> & Freshness) | undefined {
  const fresh = freshness(observed, now);
  if (!observed || !fresh) return undefined;
  if (typeof observed.value === 'number' && !Number.isFinite(observed.value)) return undefined;
  return { ...observed, ...fresh };
}

/** "40 s ago", "3 min ago", "2 h ago", "3 d ago". */
export function formatAge(seconds: number) {
  if (seconds < 60) return `${Math.max(0, Math.round(seconds))} s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} h ago`;
  return `${Math.floor(seconds / 86400)} d ago`;
}

/** "5 min", for a staleness window. */
export function formatWindow(seconds: number) {
  if (seconds < 60) return `${seconds} s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`;
  return `${Math.round(seconds / 3600)} h`;
}

// Load at or below these is "nothing observed", not a disagreement.
const IDLE_W = 1;
const IDLE_A = 0.02;

/** A fresh observation of load: power or current above idle. Stale values never count. */
export function observesLoad(reading: PowerTelemetry | undefined, now: number) {
  const power = valid(reading?.powerW, now);
  const current = valid(reading?.currentA, now);
  return (power?.state === 'fresh' && power.value > IDLE_W) || (current?.state === 'fresh' && current.value > IDLE_A);
}

/**
 * Observed and recorded disagree when load is seen on a component that no
 * readable Cable records. Records are never changed; the view says so and
 * links to them. Unknown record state (denied Cables) is never compared.
 */
export function observedDisagrees(recordState: string | undefined, reading: PowerTelemetry | undefined, now: number) {
  return recordState === 'none' && observesLoad(reading, now);
}

// ---- Source selection ----

/** `?telemetry=<scenario>` on a fixture preview URL; `none` shows the production view. */
export const TELEMETRY_PARAM = 'telemetry';

export interface SourceRequest {
  /** True only in a fixture build (VITE_FLEET_FIXTURES compiled to "true"). */
  fixtures: boolean;
  scenario?: string | null;
  /** The mock factory; only supplied after the lazy import in a fixture build. */
  mock?: (scenario: string | undefined) => TelemetrySource;
}

/** The mock only in a fixture build and only when not asked for none; otherwise none. */
export function telemetrySourceFor({ fixtures, scenario, mock }: SourceRequest): TelemetrySource {
  if (!fixtures || !mock || scenario === 'none') return noTelemetry;
  return mock(scenario ?? undefined);
}

const FIXTURES = import.meta.env.VITE_FLEET_FIXTURES === 'true';

/**
 * The source for this page load. In a normal build FIXTURES is the constant
 * false, so the dynamic import below is dead code and the mock is not bundled.
 */
export async function loadTelemetrySource(search = globalThis.location?.search ?? ''): Promise<TelemetrySource> {
  if (!FIXTURES) return noTelemetry;
  const scenario = new URLSearchParams(search).get(TELEMETRY_PARAM);
  if (scenario === 'none') return noTelemetry;
  const { mockTelemetrySource } = await import('./telemetry-mock');
  return telemetrySourceFor({ fixtures: FIXTURES, scenario, mock: mockTelemetrySource });
}

/** True when the fixture build may load the mock; the provider waits for it. */
export const TELEMETRY_LOADS_ASYNC = FIXTURES;

export interface TelemetryContextValue {
  source: TelemetrySource;
  /** The clock values are aged against, in ms. */
  now: number;
}

export const TelemetryContext = createContext<TelemetryContextValue>({ source: noTelemetry, now: Date.now() });
