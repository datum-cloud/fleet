// Synthetic Device custody and power operations for fixture builds and tests.
// Imported lazily behind the fixture flag, so production bundles drop it.
// a01-01 is free with a history covering each outcome, a01-02 is allocated,
// and a01-05 is held for a power cycle whose outcome is unknown.
import type { DeviceCustody, DevicePowerOperation, OperationPhase, PowerAction, PowerEvidence } from '../../../shared/fleet-api';
import { FIXTURE_NAMES, inventoryFixture } from './inventory-fixtures';

const LOADED = Date.now();
const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const at = (offsetMs: number) => new Date(LOADED + offsetMs).toISOString().replace(/\.\d{3}Z$/, 'Z');
const systemUUID = (name: string) => inventoryFixture.devices.find(device => device.metadata.name === name)?.spec.identity.systemUUID;

const deviceRef = (name: string) => ({ name, uid: `synthetic-${name}` });

function operation(device: string, suffix: string, created: number, action: PowerAction, reason: string, status: { phase: OperationPhase; reason?: string; message?: string; evidence?: PowerEvidence }, creator = 'staff@example.com'): DevicePowerOperation {
  const name = `${device}-power-${suffix}`;
  const ended = ['Succeeded', 'Failed', 'Cancelled', 'OutcomeUnknown'].includes(status.phase);
  return {
    apiVersion: 'fleet.datumapis.com/v1alpha1', kind: 'DevicePowerOperation',
    metadata: { name, namespace: 'default', uid: `synthetic-${name}`, creationTimestamp: at(created) },
    spec: {
      creator: { username: creator, uid: `synthetic-${creator}` }, deviceRef: deviceRef(device),
      authority: { serviceRequest: { reason } }, action, expectedSystemUUID: systemUUID(device),
    },
    status: { ...status, ...(ended && { finishedAt: at(created + 2 * MINUTE) }) },
  };
}

const observed = (state: 'On' | 'Off', offset: number) => ({ state, observedAt: at(offset) });

const server = FIXTURE_NAMES.server;
const held = FIXTURE_NAMES.singleFeedServer;

export const fixturePowerOperations: DevicePowerOperation[] = [
  operation(server, 'r7k2q', -20 * MINUTE, 'ForceRestart', 'Hung in firmware during qualification', {
    phase: 'Succeeded',
    evidence: { requestSentAt: at(-20 * MINUTE + 5_000), power: { before: observed('On', -20 * MINUTE + 4_000), after: observed('On', -20 * MINUTE + 40_000) }, resetEvidence: 'LastResetTime' },
  }),
  operation(server, 'm3x8v', -3 * HOUR, 'PowerCycle', 'Retry after the BMC replacement', {
    phase: 'Failed', reason: 'SystemUUIDMismatch', message: 'the BMC reports system 00000000-0000-4000-8000-00000000ffff, not the expected system UUID',
  }, 'tech@example.com'),
  operation(server, 'p9d4w', -26 * HOUR, 'ForceOff', 'Move to rack a02', {
    phase: 'Cancelled', reason: 'Expired', message: 'a staff operation must be admitted within 5m0s',
  }, 'tech@example.com'),
  operation(server, 'a1b2c', -50 * HOUR, 'On', 'First power-on after racking', {
    phase: 'Succeeded',
    evidence: { requestSentAt: at(-50 * HOUR + 5_000), power: { before: observed('Off', -50 * HOUR + 4_000), after: observed('On', -50 * HOUR + 60_000) } },
  }),
  operation(held, 'h5j6k', -40 * MINUTE, 'PowerCycle', 'Clear a stuck PCIe device', {
    phase: 'OutcomeUnknown', reason: 'SiteCouldNotProve', message: 'no reset evidence within 3m0s',
    evidence: { requestSentAt: at(-40 * MINUTE + 5_000), power: { before: observed('On', -40 * MINUTE + 4_000) } },
  }),
];

const custody = (name: string, spec: Partial<DeviceCustody['spec']>): DeviceCustody => ({
  metadata: { name, namespace: 'default', uid: `synthetic-custody-${name}` },
  spec: { deviceRef: deviceRef(name), state: 'Verified', holder: { type: 'None' }, fence: 4, ...spec },
});

const heldOperation = fixturePowerOperations.find(item => item.metadata.name === `${held}-power-h5j6k`)!;

const CUSTODY: Record<string, DeviceCustody> = {
  [server]: custody(server, {}),
  'example-srv-a01-02': custody('example-srv-a01-02', { holder: { type: 'Allocation', allocationRef: { name: 'example-allocation' } } }),
  [held]: custody(held, {
    holder: { type: 'Service' },
    recoveryHold: { kind: 'DevicePowerOperation', name: heldOperation.metadata.name, uid: heldOperation.metadata.uid!, fence: 7, since: heldOperation.status!.finishedAt!, reason: 'SiteCouldNotProve' },
  }),
};

/** The custody fixture for a Device, or null when none exists. */
export const fixtureCustody = (name: string): DeviceCustody | null => CUSTODY[name] ?? null;
