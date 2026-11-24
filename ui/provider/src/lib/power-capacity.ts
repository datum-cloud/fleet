// Rated capacity of a PowerFeed, from its recorded supply rating only.
//
// A rating is a nameplate fact, not available power: it says nothing about
// what upstream can deliver, what other feeds share, or whether a second feed
// is independent. Views state that once, next to the bar.
//
// Rated apparent power:
//   single-phase AC   S = V × I
//   three-phase AC    S = √3 × V × I   (V is the recorded line-to-line voltage)
//   DC                P = V × I
// The continuous-load limit is 80% of the rating (the usual derating for a
// continuous load on a branch circuit). A measured load is "high" above 80% of
// that limit and "over" above the limit itself.

import type { PhysicalRecord } from '../../../shared/fleet-api';

type Supply = NonNullable<PhysicalRecord['spec']['supply']>;

/** The share of the rating a continuous load may use. */
export const CONTINUOUS_LOAD_FACTOR = 0.8;
/** The share of the continuous limit above which a load reads as high. */
export const HIGH_LOAD_FACTOR = 0.8;

export type SupplyPhase = 'single' | 'three' | 'dc' | 'unknown';

/** The phase arrangement in the recorded free text. Anything unclear is unknown, never assumed. */
export function supplyPhase(supply: Pick<Supply, 'type' | 'phase'>): SupplyPhase {
  if (/^\s*dc\s*$/i.test(supply.type)) return 'dc';
  const phase = (supply.phase ?? '').toLowerCase().replace(/[\s_-]+/g, '');
  if (['single', 'singlephase', '1', '1phase', '1ph', 'one', 'onephase'].includes(phase)) return 'single';
  if (['three', 'threephase', '3', '3phase', '3ph'].includes(phase)) return 'three';
  return 'unknown';
}

export interface FeedCapacity {
  phase: SupplyPhase;
  /** Rated apparent power in VA (W for DC). Undefined when the rating is incomplete. */
  ratedVA?: number;
  /** CONTINUOUS_LOAD_FACTOR × ratedVA. */
  continuousVA?: number;
  /** Rated and continuous current per conductor, in A. */
  ratedA?: number;
  continuousA?: number;
  /** The arithmetic, in words: "208 V × 30 A = 6,240 VA". */
  formula?: string;
  /** Why the rating is unknown, when it is. */
  missing?: string;
}

const integer = new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 });

/** Rated and continuous capacity from the recorded supply. Missing inputs leave it unknown. */
export function feedCapacity(supply: Supply | undefined): FeedCapacity {
  if (!supply) return { phase: 'unknown', missing: 'Supply not recorded' };
  const phase = supplyPhase(supply);
  const { voltageV: volts, currentA: amps } = supply;
  const absent = [!volts && 'voltage', !amps && 'current', phase === 'unknown' && 'phase'].filter(Boolean);
  const ratedA = amps || undefined;
  const continuousA = ratedA !== undefined ? ratedA * CONTINUOUS_LOAD_FACTOR : undefined;
  if (absent.length || !volts || !amps) return { phase, ratedA, continuousA, missing: `Rating incomplete: ${absent.join(', ')} not recorded` };
  const ratedVA = (phase === 'three' ? Math.sqrt(3) : 1) * volts * amps;
  const unit = phase === 'dc' ? 'W' : 'VA';
  const formula = `${phase === 'three' ? '√3 × ' : ''}${volts} V × ${amps} A = ${integer.format(ratedVA)} ${unit}`;
  return { phase, ratedVA, continuousVA: ratedVA * CONTINUOUS_LOAD_FACTOR, ratedA, continuousA, formula };
}

export type LoadLevel = 'normal' | 'high' | 'over';

/**
 * How a measured load compares with a continuous limit: over the limit,
 * above HIGH_LOAD_FACTOR of it, or normal. Undefined without both numbers.
 */
export function loadLevel(measured: number | undefined, continuous: number | undefined): LoadLevel | undefined {
  if (measured === undefined || !continuous || continuous <= 0 || !Number.isFinite(measured)) return undefined;
  const ratio = measured / continuous;
  if (ratio > 1) return 'over';
  if (ratio > HIGH_LOAD_FACTOR) return 'high';
  return 'normal';
}

// Over-threshold values carry a glyph as well as colour.
export const LOAD_GLYPH: Record<LoadLevel, string> = { normal: '', high: '↑', over: '⚠︎' };
export const LOAD_WORDS: Record<LoadLevel, string> = {
  normal: 'within the continuous limit',
  high: `above ${Math.round(HIGH_LOAD_FACTOR * 100)}% of the continuous limit`,
  over: 'over the continuous limit',
};

/** "212 W", "4.30 kW". */
export function formatPower(value: number, unit: 'W' | 'VA' = 'W') {
  if (Math.abs(value) >= 1000) return `${(value / 1000).toFixed(2)} k${unit}`;
  return `${Math.round(value)} ${unit}`;
}

/** "0.91 A", "12.4 A". */
export function formatCurrent(value: number) {
  return `${Math.abs(value) < 10 ? value.toFixed(2) : value.toFixed(1)} A`;
}

/** "6.24 kVA rated · 4.99 kVA continuous (80%)". */
export function ratingWords(capacity: FeedCapacity) {
  if (capacity.ratedVA === undefined || capacity.continuousVA === undefined) return capacity.missing ?? 'Rating unknown';
  const unit = capacity.phase === 'dc' ? 'W' : 'VA';
  return `${formatPower(capacity.ratedVA, unit)} rated · ${formatPower(capacity.continuousVA, unit)} continuous (${Math.round(CONTINUOUS_LOAD_FACTOR * 100)}%)`;
}
