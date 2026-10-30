import { claimPhase, toMachineClaim } from './types';
import { FIXTURE_CLAIMS } from './lib/fixtures';
import { describe, expect, it } from 'vitest';

describe('claimPhase', () => {
  it('treats absent conditions as Pending', () => {
    expect(claimPhase([]).phase).toBe('Pending');
  });

  it('reports Pending with the Bound reason while unbound', () => {
    expect(
      claimPhase([
        { type: 'Accepted', status: 'True' },
        { type: 'Bound', status: 'False', reason: 'NoCapacity' },
      ])
    ).toEqual({ phase: 'Pending', reason: 'NoCapacity' });
  });

  it('reports Bound until Ready is True', () => {
    expect(
      claimPhase([
        { type: 'Bound', status: 'True' },
        { type: 'Ready', status: 'Unknown', reason: 'OutcomeUnknown' },
      ])
    ).toEqual({ phase: 'Bound', reason: 'OutcomeUnknown' });
  });

  it('reports Rejected when Accepted is False', () => {
    expect(
      claimPhase([{ type: 'Accepted', status: 'False', reason: 'AuthorizationUnavailable' }])
    ).toEqual({ phase: 'Rejected', reason: 'AuthorizationUnavailable' });
  });

  it('lets Releasing win over Ready', () => {
    expect(
      claimPhase([
        { type: 'Ready', status: 'True' },
        { type: 'Releasing', status: 'True' },
      ]).phase
    ).toBe('Releasing');
  });
});

describe('toMachineClaim', () => {
  it('maps the fixtures to Ready, Pending and Releasing', () => {
    expect(FIXTURE_CLAIMS.map(toMachineClaim).map((c) => c.phase)).toEqual([
      'Ready',
      'Pending',
      'Releasing',
    ]);
  });

  it('defaults power intent to Off and leaves unreported hardware absent', () => {
    const claim = toMachineClaim(FIXTURE_CLAIMS[1]);
    expect(claim.powerIntent).toBe('Off');
    expect(claim.powerObserved).toBeUndefined();
    expect(claim.cpu).toBeUndefined();
  });

  it('summarizes allocated CPU and memory', () => {
    const claim = toMachineClaim(FIXTURE_CLAIMS[0]);
    expect(claim.cpu).toBe('24 cores · amd64');
    expect(claim.memory).toBe('128Gi');
  });
});
