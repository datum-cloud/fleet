import { claimPhase, toCatalog, toCatalogOffering, toMachineClaim, type RawMachineProfile } from './types';
import { FIXTURE_CLAIMS, FIXTURE_CLASSES, FIXTURE_PROFILES } from './lib/fixtures';
import { describe, expect, it } from 'vitest';

describe('claimPhase', () => {
  it('treats absent conditions as Pending', () => {
    expect(claimPhase([]).phase).toBe('Pending');
  });

  it('reports Pending with the Bound reason while unbound', () => {
    expect(
      claimPhase([
        { type: 'Accepted', status: 'True' },
        { type: 'Bound', status: 'False', reason: 'InsufficientCapacity' },
      ])
    ).toEqual({ phase: 'Pending', reason: 'InsufficientCapacity' });
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

  it('lets a pending deletion win over Ready', () => {
    expect(claimPhase([{ type: 'Ready', status: 'True' }], true).phase).toBe('Releasing');
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
    const { powerState: _, ...spec } = FIXTURE_CLAIMS[1].spec;
    const claim = toMachineClaim({ ...FIXTURE_CLAIMS[1], spec });
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

describe('toCatalogOffering', () => {
  const profile: RawMachineProfile = {
    metadata: { name: 'general-purpose-24c-128g' },
    spec: { cpu: { architecture: 'amd64', cores: { min: 24 } }, memory: { capacity: { min: '128Gi' } } },
  };

  it('reads a published class from its resolved status', () => {
    const [offering] = toCatalog(FIXTURE_CLASSES.slice(0, 1), FIXTURE_PROFILES);
    expect(offering).toMatchObject({
      className: 'standard-metal',
      source: 'datum-cloud/standard-metal',
      available: true,
      powerControl: true,
    });
    expect(offering.profiles.map((p) => [p.name, p.gpus])).toEqual([
      ['general-purpose-24c-128g', undefined],
      ['accelerated-32c-256g', '4 × nvidia'],
    ]);
  });

  it('keeps an unresolved published class pending, ignoring its spec', () => {
    const offering = toCatalogOffering(FIXTURE_CLASSES[1], FIXTURE_PROFILES);
    expect(offering.available).toBe(false);
    expect(offering.unavailableReason).toMatch(/Waiting/);
    expect(offering.profiles).toEqual([]);
  });

  it("reads the project's own class from its spec", () => {
    const offering = toCatalogOffering(
      {
        metadata: { name: 'standard-metal' },
        spec: { profilePolicy: { allowedProfiles: [{ name: profile.metadata.name }] } },
      },
      [profile]
    );
    expect(offering.source).toBeUndefined();
    expect(offering.available).toBe(true);
    expect(offering.profiles[0]).toMatchObject({ published: true, cpu: '≥ 24 cores · amd64' });
  });

  it('marks an allowed profile that is missing from the project', () => {
    const offering = toCatalogOffering(
      { metadata: { name: 'c' }, spec: { profilePolicy: { allowedProfiles: [{ name: 'gone' }] } } },
      []
    );
    expect(offering.profiles[0]).toMatchObject({ name: 'gone', published: false });
  });

  it('reports a failed resolution with its message', () => {
    const offering = toCatalogOffering(
      {
        metadata: { name: 'c' },
        spec: { source: { project: 'datum-cloud', name: 'c' } },
        status: {
          conditions: [{ type: 'SourceResolved', status: 'False', reason: 'NotFound', message: 'No such class.' }],
        },
      },
      []
    );
    expect(offering).toMatchObject({ available: false, unavailableReason: 'No such class.' });
  });
});
