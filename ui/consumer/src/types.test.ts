import {
  machineStatus,
  machineSteps,
  machinePhase,
  reasonText,
  toCatalog,
  toCatalogOffering,
  toMachineDetail,
  toMachine,
  type RawMachineProfile,
} from './types';
import { FIXTURE_MACHINES, FIXTURE_CLASSES, FIXTURE_PROFILES } from './lib/fixtures';
import { describe, expect, it } from 'vitest';

describe('machinePhase', () => {
  it('treats absent conditions as Pending', () => {
    expect(machinePhase([]).phase).toBe('Pending');
  });

  it('reports Pending with the Bound reason while unbound', () => {
    expect(
      machinePhase([
        { type: 'Accepted', status: 'True' },
        { type: 'Bound', status: 'False', reason: 'InsufficientCapacity' },
      ])
    ).toEqual({ phase: 'Pending', reason: 'InsufficientCapacity' });
  });

  it('reports Bound until Ready is True', () => {
    expect(
      machinePhase([
        { type: 'Bound', status: 'True' },
        { type: 'Ready', status: 'Unknown', reason: 'OutcomeUnknown' },
      ])
    ).toEqual({ phase: 'Bound', reason: 'OutcomeUnknown' });
  });

  it('reports Rejected when Accepted is False', () => {
    expect(
      machinePhase([{ type: 'Accepted', status: 'False', reason: 'AuthorizationUnavailable' }])
    ).toEqual({ phase: 'Rejected', reason: 'AuthorizationUnavailable' });
  });

  it('lets a pending deletion win over Ready', () => {
    expect(machinePhase([{ type: 'Ready', status: 'True' }], true).phase).toBe('Releasing');
  });
});

describe('toMachine', () => {
  it('maps the fixtures to Ready, Pending, Releasing and Bound', () => {
    expect(FIXTURE_MACHINES.map(toMachine).map((c) => c.phase)).toEqual([
      'Ready',
      'Pending',
      'Releasing',
      'Bound',
    ]);
  });

  it('gives every fixture a creation time', () => {
    expect(FIXTURE_MACHINES.map(toMachine).every((m) => !!m.createdAt)).toBe(true);
  });

  it('lists every interface address in order', () => {
    expect(toMachine(FIXTURE_MACHINES[3]).addresses).toEqual(['192.0.2.10', '2001:db8::10']);
    expect(toMachine(FIXTURE_MACHINES[0]).addresses).toEqual([]);
  });

  it('defaults power intent to Off and leaves an unresolved request absent', () => {
    const { powerState: _, ...spec } = FIXTURE_MACHINES[1].spec;
    const { requestedHardware: __, ...status } = FIXTURE_MACHINES[1].status ?? {};
    const machine = toMachine({ ...FIXTURE_MACHINES[1], spec, status });
    expect(machine.powerIntent).toBe('Off');
    expect(machine.powerObserved).toBeUndefined();
    expect(machine.cpu).toBeUndefined();
    expect(machine.memory).toBeUndefined();
  });

  it('summarizes requested CPU and memory, including before allocation', () => {
    for (const raw of FIXTURE_MACHINES.slice(0, 2)) {
      const machine = toMachine(raw);
      expect(machine.cpu).toBe('24 cores · amd64');
      expect(machine.memory).toBe('128Gi');
    }
  });

  it('does not take requested values from allocated hardware', () => {
    const { requestedHardware: _, ...status } = FIXTURE_MACHINES[0].status ?? {};
    const machine = toMachine({ ...FIXTURE_MACHINES[0], status });
    expect(machine.cpu).toBeUndefined();
    expect(machine.memory).toBeUndefined();
  });
});

describe('toCatalogOffering', () => {
  const profile: RawMachineProfile = {
    metadata: { name: 'general-purpose-24c-128g' },
    spec: { cpu: { architecture: 'amd64', cores: 24 }, memory: { capacity: '128Gi' } },
  };

  it('reads a published class from its resolved status', () => {
    const [offering] = toCatalog(FIXTURE_CLASSES.slice(0, 1), FIXTURE_PROFILES);
    expect(offering).toMatchObject({
      className: 'standard-metal',
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
    expect(offering).toMatchObject({ available: false, failed: false });
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
    expect(offering.available).toBe(true);
    expect(offering.profiles[0]).toMatchObject({ published: true, cpu: '24 cores · amd64' });
  });

  it('marks an allowed profile that is missing from the project', () => {
    const offering = toCatalogOffering(
      { metadata: { name: 'c' }, spec: { profilePolicy: { allowedProfiles: [{ name: 'gone' }] } } },
      []
    );
    expect(offering.profiles[0]).toMatchObject({ name: 'gone', published: false });
  });

  it('reports a failed resolution without its message', () => {
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
    expect(offering).toMatchObject({ available: false, failed: true });
    expect(JSON.stringify(offering)).not.toContain('No such class.');
  });
});

describe('toMachineDetail', () => {
  it('lists allocated hardware and the four lifecycle conditions in order', () => {
    const detail = toMachineDetail(FIXTURE_MACHINES[0]);
    expect(detail.placement).toBe('us-central-1');
    expect(detail.acceptedProfile).toBe('general-purpose-24c-128g');
    expect(detail.hardware).toEqual([
      { label: 'CPU', value: '2 sockets / 24 cores / 48 threads · amd64' },
      { label: 'Memory', value: '128Gi' },
    ]);
    expect(detail.conditions.map((c) => [c.type, c.status])).toEqual([
      ['Accepted', 'True'],
      ['Bound', 'True'],
      ['NetworkReady', 'True'],
      ['Ready', 'True'],
    ]);
  });

  it('fills absent conditions as Unknown and leaves hardware absent before binding', () => {
    const detail = toMachineDetail({ ...FIXTURE_MACHINES[1], status: undefined });
    expect(detail.hardware).toBeUndefined();
    expect(detail.conditions.every((c) => c.status === 'Unknown')).toBe(true);
  });

  it('drops conditions beyond the known four', () => {
    const detail = toMachineDetail({
      ...FIXTURE_MACHINES[1],
      status: { conditions: [{ type: 'PowerReady', status: 'False', reason: 'Waiting' }] },
    });
    expect(detail.conditions.map((c) => c.type)).toEqual(['Accepted', 'Bound', 'NetworkReady', 'Ready']);
  });

  it('drops every condition message', () => {
    const detail = toMachineDetail(FIXTURE_MACHINES[1]);
    expect(detail.conditions.every((c) => !('message' in c))).toBe(true);
    expect(JSON.stringify(detail)).not.toContain('another consumer');
  });

  it('summarizes network interfaces', () => {
    const detail = toMachineDetail({
      ...FIXTURE_MACHINES[0],
      status: {
        network: {
          interfaces: [
            {
              name: 'primary',
              network: { apiGroup: 'networking.datumapis.com', kind: 'Network', namespace: 'default', name: 'production' },
              addresses: [{ address: '2001:db8::10', family: 'IPv6', primary: true }],
              conditions: [{ type: 'Ready', status: 'True' }],
            },
          ],
        },
      },
    });
    expect(detail.interfaces).toEqual([
      { name: 'primary', network: 'production', addresses: ['2001:db8::10'], ready: 'True' },
    ]);
  });
});

describe('machineStatus', () => {
  it('names a pending machine that Fleet accepted as waiting for capacity', () => {
    expect(machineStatus(toMachine(FIXTURE_MACHINES[1]))).toEqual({ tone: 'warning', label: 'Waiting for capacity' });
    expect(machineStatus({ phase: 'Pending', waitingForCapacity: false })).toEqual({ tone: 'muted', label: 'Pending' });
    expect(machineStatus(toMachine(FIXTURE_MACHINES[0]))).toEqual({ tone: 'ok', label: 'Ready' });
  });
});

describe('machineSteps', () => {
  it('marks the first condition that is not True as current and the rest as upcoming', () => {
    const steps = machineSteps(toMachineDetail(FIXTURE_MACHINES[1]).conditions);
    expect(steps.map((s) => s.state)).toEqual(['done', 'current', 'upcoming', 'upcoming']);
    expect(steps.map((s) => s.label)).toEqual(['Requested', 'Server assigned', 'Network', 'Ready']);
    expect(steps[1].detail).toBe('Waiting for capacity');
    expect(steps[2].detail).toBeUndefined();
  });

  it('shows customer words for known reasons on done steps', () => {
    expect(machineSteps(toMachineDetail(FIXTURE_MACHINES[0]).conditions).map((s) => s.detail)).toEqual([
      'Accepted',
      'Dedicated to you',
      'No network requested',
      'Handed over',
    ]);
  });

  it('shows a rejected request as a failed first step', () => {
    const steps = machineSteps([{ type: 'Accepted', status: 'False', reason: 'ProfileNotAllowed' }]);
    expect(steps[0]).toMatchObject({ state: 'failed', detail: 'Profile not offered' });
  });

  it('gives an unmapped reason a generic word and keeps the reason for the tooltip', () => {
    const steps = machineSteps([
      { type: 'Accepted', status: 'True', reason: 'SomeNewReason' },
      { type: 'Bound', status: 'False', reason: 'AnotherNewReason' },
    ]);
    expect(steps[0]).toMatchObject({ detail: 'Done', detailTooltip: 'SomeNewReason' });
    expect(steps[1]).toMatchObject({ detail: 'In progress', detailTooltip: 'AnotherNewReason' });
  });

  it('treats a missing condition as still to come, not unknown', () => {
    expect(machineSteps([]).map((s) => s.state)).toEqual(['current', 'upcoming', 'upcoming', 'upcoming']);
  });
});

describe('reasonText', () => {
  it('maps a known reason to its word with no tooltip', () => {
    expect(reasonText('InsufficientCapacity', 'In progress')).toEqual({ word: 'Waiting for capacity' });
  });

  it('keeps an unmapped token reason for the tooltip only', () => {
    expect(reasonText('BrandNewReason', 'Done')).toEqual({ word: 'Done', tooltip: 'BrandNewReason' });
  });

  it('never passes on a reason that reads like message text', () => {
    for (const reason of ['Allocated to another consumer.', 'tenant-b holds it', 'Busy: srv1']) {
      expect(reasonText(reason, 'In progress')).toEqual({ word: 'In progress', tooltip: undefined });
    }
  });

  it('falls back with no tooltip when there is no reason', () => {
    expect(reasonText(undefined, 'In progress')).toEqual({ word: 'In progress', tooltip: undefined });
  });
});
