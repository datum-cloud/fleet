// How observed power telemetry is shown. The display rules:
//   - every value shows its age;
//   - stale values are greyed out and labelled stale, never green;
//   - a missing value is "Not reported", never 0;
//   - over-threshold values get a glyph as well as colour;
//   - observed load where no Cable is recorded says "observed and recorded
//     disagree" and links to the records, which it never changes;
//   - sparklines appear only in cards;
//   - accessible names carry the value, unit and age.
// Health is shown only from an observation; otherwise "No health reported".

import { useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import {
  formatAge, formatWindow, freshness, loadTelemetrySource, noTelemetry, NOT_REPORTED, observedDisagrees, TELEMETRY_LOADS_ASYNC, TelemetryContext, valid,
  type HealthValue, type Observed, type PowerTelemetry, type SeriesPoint, type TelemetryCollector, type TelemetrySource, type TelemetrySubject,
} from '../lib/telemetry';
import { feedCapacity, formatCurrent, formatPower, LOAD_GLYPH, LOAD_WORDS, loadLevel, ratingWords, type FeedCapacity } from '../lib/power-capacity';
import type { PortTelemetry, TileLoad } from './device-faceplate';
import type { PortTile } from '../lib/device-connections';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import { inventoryHref } from './inventory-location';

// ---- Source ----

/**
 * Supplies the telemetry source to every view below it. In a normal build
 * the source is `none` at once. In a fixture build the mock is loaded lazily
 * first, so a page never flashes "Not reported" before synthetic values.
 */
export function TelemetryProvider({ children, pending = null }: { children: ReactNode; pending?: ReactNode }) {
  const [source, setSource] = useState<TelemetrySource | undefined>(TELEMETRY_LOADS_ASYNC ? undefined : noTelemetry);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (source) return;
    let live = true;
    loadTelemetrySource().then(loaded => { if (live) { setNow(Date.now()); setSource(loaded); } }, () => { if (live) setSource(noTelemetry); });
    return () => { live = false; };
  }, [source]);
  // Ages move on while the page is open.
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, []);
  const value = useMemo(() => source && { source, now }, [source, now]);
  if (!value) return <>{pending}</>;
  return <TelemetryContext.Provider value={value}>{children}</TelemetryContext.Provider>;
}

export function useTelemetry() {
  return useContext(TelemetryContext);
}

// ---- Values ----

const COLLECTOR_WORDS: Record<TelemetryCollector, string> = { pdu: 'PDU collector', bmc: 'BMC collector' };

/** A value with its age. Stale is greyed and says so. */
export function ObservedValue({ observed, now, format }: { observed?: Observed<number>; now: number; format: (value: number) => string }) {
  const value = valid(observed, now);
  if (!value) return <span className="inventory-telemetry-value" data-freshness="missing">{NOT_REPORTED}</span>;
  return <span className="inventory-telemetry-value" data-freshness={value.state}>
    {format(value.value)}
    <small>{value.state === 'stale' ? <><span aria-hidden="true">◷ </span>Stale · {formatAge(value.ageSeconds)}</> : formatAge(value.ageSeconds)}</small>
  </span>;
}

/** "0.91 A · 182 W · 40 s ago", or "stale: … · 3 h ago", or undefined when nothing is reported. */
export function readingWords(reading: PowerTelemetry | undefined, now: number) {
  const current = valid(reading?.currentA, now);
  const power = valid(reading?.powerW, now);
  const state = valid(reading?.outputState, now);
  const parts = [current && formatCurrent(current.value), power && formatPower(power.value), state?.value === 'Off' && 'off'].filter(Boolean);
  const first = current ?? power ?? state;
  if (!first || !parts.length) return undefined;
  const stale = [current, power, state].some(item => item?.state === 'stale');
  return `${stale ? 'stale: ' : ''}${parts.join(' · ')} · ${formatAge(first.ageSeconds)}`;
}

/** A compact line for strip rows: values and age, greyed when stale. */
export function ReadingLine({ reading, now }: { reading?: PowerTelemetry; now: number }) {
  const words = readingWords(reading, now);
  if (!words) return <span className="inventory-telemetry-line" data-freshness="missing">{NOT_REPORTED}</span>;
  const stale = words.startsWith('stale: ');
  return <span className="inventory-telemetry-line" data-freshness={stale ? 'stale' : 'fresh'}>{stale && <span aria-hidden="true">◷ </span>}{stale ? `Stale · ${words.slice(7)}` : words}</span>;
}

// ---- Sparkline (cards only) ----

export function Sparkline({ series, unit, stale, label }: { series: SeriesPoint[]; unit: 'W' | 'A'; stale?: boolean; label: string }) {
  const values = series.map(point => point.value).filter(Number.isFinite);
  if (values.length < 2) return null;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min || 1;
  const points = values.map((value, index) => `${(index / (values.length - 1)) * 100},${28 - ((value - min) / span) * 24}`).join(' ');
  const format = (value: number) => unit === 'W' ? formatPower(value) : formatCurrent(value);
  const words = `${label}, last 24 hours: low ${format(min)}, high ${format(max)}${stale ? ', stale' : ''}`;
  return <figure className="inventory-sparkline" data-freshness={stale ? 'stale' : 'fresh'}>
    <svg viewBox="0 0 100 30" preserveAspectRatio="none" role="img" aria-label={words}>
      <polyline points={points} fill="none" vectorEffect="non-scaling-stroke" />
    </svg>
    <figcaption>24 h · {format(min)} to {format(max)}</figcaption>
  </figure>;
}

// ---- Card sections ----

/** Which collector reported a reading, when, and for how long it stays fresh. Shown behind a card's More. */
export function sourceWords(reading: PowerTelemetry | undefined, now: number) {
  if (!reading) return undefined;
  const first = [reading.currentA, reading.powerW, reading.apparentPowerVA, reading.outputState, reading.health].find(Boolean);
  const fresh = freshness(first, now);
  if (!first || !fresh) return undefined;
  return `${COLLECTOR_WORDS[first.origin.collector]} (${first.origin.name}${first.origin.synthetic ? ', synthetic' : ''}) · observed ${first.observedAt.replace('T', ' ').replace(/\.\d+Z$|Z$/, ' UTC')} · fresh for ${formatWindow(first.staleAfterSeconds)}`;
}

export interface MismatchContext {
  /** What disagrees, in words, e.g. "Outlet 6 of PDU a01". */
  what: string;
  /** Links to the records to review. */
  records: { label: string; href: string }[];
}

/** "Observed and recorded disagree", with links to the records. */
export function MismatchNote({ context }: { context: MismatchContext }) {
  return <p className="inventory-telemetry-mismatch" role="note">
    <span aria-hidden="true">≠ </span><strong>Observed and recorded disagree:</strong> load is observed on {context.what}, but no Cable records it.{' '}
    {context.records.map((record, index) => <span key={record.href}>{index > 0 && ' · '}<Link to={record.href}>{record.label}</Link></span>)}
  </p>;
}

/** The card's Telemetry section. */
export function TelemetrySection({ reading, source, now, kind, mismatch }: { reading?: PowerTelemetry; source: TelemetrySource; now: number; kind: 'outlet' | 'psu'; mismatch?: MismatchContext }) {
  const power = valid(reading?.powerW, now);
  const current = valid(reading?.currentA, now);
  const state = valid(reading?.outputState, now);
  const any = power || current || state;
  const history = power?.series ? { series: power.series, unit: 'W' as const, label: 'Power' } : current?.series ? { series: current.series, unit: 'A' as const, label: 'Current' } : undefined;
  return <div className="inventory-port-card-telemetry">
    <p className="inventory-port-card-heading">Telemetry</p>
    {!any ? <p className="inventory-muted">{NOT_REPORTED}{source.kind === 'none' ? ` · ${source.description}` : ''}</p> : <>
      <dl className="inventory-port-card-fields">
        {kind === 'outlet' && <><dt>Current</dt><dd><ObservedValue observed={reading?.currentA} now={now} format={formatCurrent} /></dd></>}
        <dt>{kind === 'psu' ? 'Input power' : 'Power'}</dt><dd><ObservedValue observed={reading?.powerW} now={now} format={value => formatPower(value)} /></dd>
        {kind === 'outlet' && <><dt>Output</dt><dd>{state ? <span className="inventory-telemetry-value" data-freshness={state.state}>{state.value}<small>{state.state === 'stale' ? `Stale · ${formatAge(state.ageSeconds)}` : formatAge(state.ageSeconds)}</small></span> : <span className="inventory-telemetry-value" data-freshness="missing">{NOT_REPORTED}</span>}</dd></>}
      </dl>
      {history && <Sparkline series={history.series} unit={history.unit} label={history.label} stale={(power ?? current)?.state === 'stale'} />}
    </>}
    {mismatch && <MismatchNote context={mismatch} />}
    {any && <p className="inventory-port-card-meta">Observed only; verifies nothing and changes no record.</p>}
  </div>;
}

const HEALTH_GLYPH: Record<HealthValue['state'], string> = { OK: '▲', Fault: '✕', Absent: '▼' };

/** Health from an observation, or undefined so the card keeps "No health reported". */
export function healthNode(reading: PowerTelemetry | undefined, now: number): ReactNode | undefined {
  const health = valid(reading?.health, now);
  if (!health) return undefined;
  const stale = health.state === 'stale';
  return <p className="inventory-telemetry-health" data-health={stale ? 'stale' : health.value.state.toLowerCase()}>
    <span aria-hidden="true">{stale ? '◷' : HEALTH_GLYPH[health.value.state]} </span>
    {stale ? `Stale: last ${health.value.state}` : `Observed ${health.value.state}`}{health.value.reason ? ` · ${health.value.reason}` : ''} · {formatAge(health.ageSeconds)}
    <small>Observed only</small>
  </p>;
}

// ---- Tiles ----

/** A load tint from current against a continuous limit (the feed's), with marks. */
export function tileLoad(reading: PowerTelemetry | undefined, now: number, continuousA: number | undefined, mismatch: boolean): TileLoad | undefined {
  const current = valid(reading?.currentA, now);
  const power = valid(reading?.powerW, now);
  const state = valid(reading?.outputState, now);
  const health = valid(reading?.health, now);
  const value = current ?? power;
  const fault = health?.state === 'fresh' && health.value.state === 'Fault';
  if (!value && !state && !health) return undefined;
  if ([value, state, health].some(item => item?.state === 'stale')) return { tint: 'stale', fraction: 1, mark: '◷', markKind: 'stale' };
  if (mismatch) return { tint: 'observed', fraction: 1, mark: '≠', markKind: 'mismatch' };
  const faultMark = fault ? { mark: '✕', markKind: 'fault' as const } : {};
  if (state?.value === 'Off') return { tint: 'off', fraction: 0, ...faultMark };
  if (!current || !continuousA) return value ? { tint: value.value > 0 ? 'observed' : 'idle', fraction: value.value > 0 ? 1 : 0, ...faultMark } : fault ? { tint: 'idle', fraction: 0, ...faultMark } : undefined;
  const fraction = current.value / continuousA;
  const level = loadLevel(current.value, continuousA);
  const tint: TileLoad['tint'] = level === 'over' ? 'over' : level === 'high' ? 'high' : current.value <= 0.02 ? 'idle' : fraction < 0.1 ? 'low' : fraction < 0.3 ? 'mid' : 'strong';
  const levelMark = level && level !== 'normal' ? { mark: LOAD_GLYPH[level], markKind: level } : {};
  return { tint, fraction: Math.min(1, fraction), ...faultMark, ...levelMark };
}

/**
 * Words for a tile's accessible name: value, unit and age. Undefined when
 * nothing is observed, so the record-state name is unchanged; the card says
 * "Not reported".
 */
export function tileWords(reading: PowerTelemetry | undefined, now: number, continuousA: number | undefined, mismatch: boolean) {
  const words = readingWords(reading, now);
  const health = valid(reading?.health, now);
  if (!words && !health && !mismatch) return undefined;
  const current = valid(reading?.currentA, now);
  const level = current?.state === 'fresh' ? loadLevel(current.value, continuousA) : undefined;
  return [
    words ? `telemetry ${words}` : 'load not reported',
    level && level !== 'normal' && LOAD_WORDS[level],
    mismatch && 'observed and recorded disagree: load observed, no cable recorded',
    health && `${health.state === 'stale' ? 'stale ' : ''}status ${health.value.state}${health.value.reason ? ` (${health.value.reason})` : ''}, ${formatAge(health.ageSeconds)}`,
  ].filter(Boolean).join(', ');
}

export interface TelemetryHolder {
  kind: TelemetrySubject['kind'];
  namespace?: string;
  name: string;
  /** A human name for the holder, used in disagreement notes. */
  label?: string;
}

/**
 * Telemetry for the power tiles of one Device or PowerFeed, for the shared
 * faceplate and the power strips. Other tiles get nothing. When Cables are
 * unreadable, record state is unknown, so nothing is called a disagreement.
 */
export function usePortTelemetry(holder: TelemetryHolder, options: { continuousA?: number; cablesDenied?: boolean } = {}) {
  const { source, now } = useTelemetry();
  const { kind, namespace, name, label } = holder;
  const { continuousA, cablesDenied } = options;
  return useCallback((tile: PortTile): PortTelemetry | undefined => {
    if (tile.group !== 'power') return undefined;
    const reading = source.read({ kind, namespace, name, componentID: tile.componentID });
    const mismatch = !cablesDenied && observedDisagrees(tile.state, reading, now);
    const context: MismatchContext | undefined = mismatch ? {
      what: `${tile.name}${label ? ` of ${label}` : ''}`,
      records: [{ label: `${label ?? 'This record'}`, href: inventoryHref(kind, namespace, name) }, { label: 'Cable records', href: `?kind=Cable&q=${encodeURIComponent(name)}` }],
    } : undefined;
    const psu = tile.type === 'PowerInlet' && kind === 'Device';
    return {
      load: tileLoad(reading, now, continuousA, mismatch),
      words: tileWords(reading, now, continuousA, mismatch),
      section: <TelemetrySection reading={reading} source={source} now={now} kind={psu ? 'psu' : 'outlet'} mismatch={context} />,
      health: healthNode(reading, now),
      source: sourceWords(reading, now),
    };
  }, [source, now, kind, namespace, name, label, continuousA, cablesDenied]);
}

// ---- Capacity ----

/**
 * A feed's rated capacity with the continuous-load line, filled by measured
 * load when telemetry reports it. Without telemetry it reads "No load
 * reported", never empty or 0.
 */
export function CapacityBar({ feed, reading, now, source, compact = false }: { feed: PhysicalRecord; reading?: PowerTelemetry; now: number; source: TelemetrySource; compact?: boolean }) {
  const capacity: FeedCapacity = feedCapacity(feed.spec.supply);
  const apparent = valid(reading?.apparentPowerVA, now);
  const real = valid(reading?.powerW, now);
  const measured = capacity.phase === 'dc' ? real : apparent ?? real;
  const unit = capacity.phase === 'dc' || !apparent ? 'W' : 'VA';
  if (capacity.ratedVA === undefined || capacity.continuousVA === undefined) {
    return <div className="inventory-capacity" data-compact={compact ? '' : undefined} data-level="unknown">
      <p className="inventory-capacity-text">{capacity.missing ?? 'Rating unknown'} · capacity unknown</p>
      {measured && <p className="inventory-capacity-text"><ReadingLine reading={reading} now={now} /></p>}
    </div>;
  }
  const level = measured?.state === 'fresh' ? loadLevel(measured.value, capacity.continuousVA) : undefined;
  const percent = measured ? Math.round((measured.value / capacity.continuousVA) * 100) : undefined;
  const fill = measured ? Math.min(100, (measured.value / capacity.ratedVA) * 100) : 0;
  const freshnessState = measured ? measured.state : 'missing';
  // The full reading; compact headers show only the load and percentage, with this as the tooltip.
  const full = measured && `${formatPower(measured.value, unit)} of ${formatPower(capacity.ratedVA, capacity.phase === 'dc' ? 'W' : 'VA')} rated · ${percent}% of continuous limit${level && level !== 'normal' ? ` · ${LOAD_WORDS[level]}` : ''} · ${measured.state === 'stale' ? `Stale · ${formatAge(measured.ageSeconds)}` : formatAge(measured.ageSeconds)}`;
  const text = !measured
    ? <>No load reported{source.kind === 'none' && !compact ? ` · ${source.description}` : ''}</>
    : <>
      {level && LOAD_GLYPH[level] && <span className="inventory-capacity-glyph" aria-hidden="true">{LOAD_GLYPH[level]} </span>}
      {measured.state === 'stale' && <span aria-hidden="true">◷ </span>}
      {compact ? <><span aria-hidden="true">{formatPower(measured.value, unit)} · {percent}%</span><span className="inventory-visually-hidden">{full}</span></> : full}
    </>;
  return <div className="inventory-capacity" data-compact={compact ? '' : undefined} data-level={level ?? (measured ? 'stale' : 'missing')} data-freshness={freshnessState}>
    <div className="inventory-capacity-bar" aria-hidden="true">
      <span className="inventory-capacity-fill" style={{ width: `${fill}%` }} />
      <span className="inventory-capacity-limit" style={{ left: `${(capacity.continuousVA / capacity.ratedVA) * 100}%` }} />
    </div>
    <p className="inventory-capacity-text" title={compact && full ? full : undefined}>{text}</p>
    {!compact && <p className="inventory-capacity-formula">{ratingWords(capacity)} · {capacity.formula}. The line marks the continuous limit.{unit === 'W' && measured && capacity.phase !== 'dc' ? ' Apparent power not reported; real power shown.' : ''}</p>}
  </div>;
}
