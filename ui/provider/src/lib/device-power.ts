// Staff power for one Device: which actions the page offers, why the control
// is unavailable, the DevicePowerOperation it creates, and how each operation
// reads. The coordinator decides admission (internal/custody/power.go and
// coordinator.go). The page repeats only the checks its records show, so it
// does not offer a request those would refuse; discovery age is the
// coordinator's alone, because the page does not know the configured interval.
// See "Device page: power" in docs/development/staff-inventory-ui.md.
import { useQuery } from '@tanstack/react-query';
import type { Device, DeviceCustody, DevicePowerOperation, List, ObservedSystem, PowerAction, PowerEvidence } from '../../../shared/fleet-api';
import { ApiError, PLUGIN_ID, PROVIDER_PROJECT, proxyFetch } from './api';
import { staleReason, type Tone } from './device-discovery';
import { INVENTORY_NAMESPACE } from './inventory-namespace';
import { PERMISSION_QUERY_KEY, selfSubjectAllowed } from './inventory-management';
import type { InventoryOperation } from './inventory-operation';
import { useInventoryRead } from './inventory-read';

const fixtures = import.meta.env.VITE_FLEET_FIXTURES === 'true';
const OPERATIONS = `/apis/fleet.datumapis.com/v1alpha1/namespaces/${INVENTORY_NAMESPACE}/devicepoweroperations`;
const CUSTODIES = `/apis/fleet.datumapis.com/v1alpha1/namespaces/${INVENTORY_NAMESPACE}/devicecustodies`;

/** Every action, in the order the menu lists them (ops.PowerActions). */
export const POWER_ACTIONS: PowerAction[] = ['On', 'ForceOff', 'ForceRestart', 'PowerCycle'];

export const ACTION_WORDS: Record<PowerAction, string> = { On: 'Power on', ForceOff: 'Force off', ForceRestart: 'Force restart', PowerCycle: 'Power cycle' };

/** Actions that stop or reset a running server, and so need a confirmation naming it. */
export const destructive = (action: PowerAction) => action !== 'On';

/**
 * The System the coordinator checks: the one discovery read with the Device's
 * recorded system UUID, compared without case (discoveredSystem).
 */
export function powerSystem(device: Device): ObservedSystem | undefined {
  const uuid = device.spec.identity.systemUUID?.toLowerCase();
  if (!uuid) return undefined;
  return device.status?.discovery?.inventory?.systems?.find(system => system.uuid?.toLowerCase() === uuid);
}

/** The actions discovery reports the System offers, in menu order. */
export function offeredActions(device: Device): PowerAction[] {
  const offered = powerSystem(device)?.resetTypes ?? [];
  return POWER_ACTIONS.filter(action => offered.includes(action));
}

const running = (operation: DevicePowerOperation) => !['Succeeded', 'Failed', 'Cancelled', 'OutcomeUnknown'].includes(operation.status?.phase ?? 'Pending');

export interface PowerContext {
  custody?: DeviceCustody | null;
  /** Set when custody could not be read; null custody means none exists. */
  custodyError?: ApiError | null;
  operations?: DevicePowerOperation[];
}

/**
 * Why staff cannot power this Device now, or undefined when the control is
 * available. The first reason wins: discovery, then custody.
 */
export function powerBlock(device: Device, { custody, custodyError, operations = [] }: PowerContext): string | undefined {
  const discovery = device.status?.discovery?.inventory;
  if (!discovery) return 'Not discovered';
  if (!device.spec.identity.systemUUID) return 'No system UUID recorded';
  if (discovery.endpoint !== device.spec.bmc?.endpoint) return 'Discovery read another BMC endpoint';
  if (!powerSystem(device)) return `Discovery has not read system ${device.spec.identity.systemUUID}`;
  if (!offeredActions(device).length) return 'The BMC offers no power actions';
  // The coordinator's identity rule: a matched identity at this generation, and a unique key read from this endpoint.
  const condition = (type: string) => device.status?.conditions?.find(item => item.type === type);
  const identity = condition('IdentityMatched');
  if (identity?.status !== 'True' || (device.metadata.generation !== undefined && identity.observedGeneration !== device.metadata.generation)) return 'Identity not matched';
  const tls = device.status?.discovery?.tls;
  if (!tls?.publicKeySHA256 || staleReason(tls, device)) return 'BMC key not read';
  if (condition('PublicKeyUnique')?.status === 'False') return 'BMC key shared';
  if (custodyError) return custodyError.status === 403 ? 'Custody unreadable' : 'Custody could not be read';
  if (custody === undefined) return 'Checking custody…';
  if (custody === null) return 'No custody record';
  const { state, holder, recoveryHold, activeOperation } = custody.spec;
  if (state !== 'Verified') return 'Custody unverified';
  if (holder.type === 'Allocation' || holder.type === 'Release') return 'Allocated to a customer';
  if (holder.type === 'Retired') return 'Retired';
  if (recoveryHold) return `Held: ${recoveryHold.kind} ${recoveryHold.name} outcome unknown`;
  if (activeOperation) return `${activeOperation.kind} ${activeOperation.name} is running`;
  if (holder.type !== 'None') return 'Held for a staff operation';
  const pending = operations.find(running);
  if (pending) return `${ACTION_WORDS[pending.spec.action] ?? pending.spec.action} is in progress`;
  return undefined;
}

/** The caller as the API server authenticates it; the creator webhook admits only this. */
export interface Caller { username: string; uid: string }

/**
 * The staff DevicePowerOperation: a generated name, the caller as creator,
 * the Device by name and UID, the reason as the service request, and the
 * recorded system UUID. Nothing here is typed by hand except the reason.
 */
export function powerRequest(device: Device, action: PowerAction, reason: string, caller: Caller) {
  const trimmed = reason.trim();
  if (!trimmed || trimmed.length > 512) throw new ApiError(422, 'A reason is required (at most 512 characters).');
  if (!offeredActions(device).includes(action)) throw new ApiError(422, `${ACTION_WORDS[action]} is not offered.`);
  const uuid = device.spec.identity.systemUUID;
  if (!device.metadata.uid || !uuid) throw new ApiError(409, 'The Device has no UID or system UUID on record.');
  return {
    apiVersion: 'fleet.datumapis.com/v1alpha1',
    kind: 'DevicePowerOperation',
    metadata: { generateName: `${device.metadata.name.slice(0, 50).replace(/[.-]+$/, '')}-power-`, namespace: INVENTORY_NAMESPACE },
    spec: {
      creator: { username: caller.username, uid: caller.uid },
      deviceRef: { name: device.metadata.name, uid: device.metadata.uid },
      authority: { serviceRequest: { reason: trimmed } },
      action,
      expectedSystemUUID: uuid.toLowerCase(),
    },
  };
}

// ---- How an operation reads ----

const PHASE_WORDS: Record<string, string> = {
  Pending: 'Pending', Admitted: 'Admitted', Dispatched: 'Sent', Verifying: 'Verifying',
  Succeeded: 'Succeeded', Failed: 'Refused', Cancelled: 'Cancelled', OutcomeUnknown: 'Outcome unknown',
};

export interface PowerOutcome {
  label: string;
  tone: Tone | 'progress';
  /** The reason and message: the tooltip and accessible text. */
  reason?: string;
  /** Set when the outcome is unknown and custody holds the Device for it. */
  hold?: string;
}

/**
 * The phase as a word, with its tone. A Failed operation that never sent a
 * command reads "Refused"; one that did reads "Failed". OutcomeUnknown is red
 * and names the recovery hold when custody holds this operation.
 */
export function powerOutcome(operation: DevicePowerOperation, custody?: DeviceCustody | null): PowerOutcome {
  const status = operation.status ?? {};
  const phase = status.phase ?? 'Pending';
  const reason = [status.reason, status.message].filter(Boolean).join(' · ') || undefined;
  const sent = !!status.evidence?.requestSentAt;
  switch (phase) {
    case 'Succeeded': return { label: 'Succeeded', tone: undefined, reason };
    case 'Failed': return { label: sent ? 'Failed' : 'Refused', tone: 'danger', reason };
    case 'Cancelled': return { label: status.reason === 'Expired' ? 'Expired' : 'Cancelled', tone: 'warning', reason };
    case 'OutcomeUnknown': {
      const held = custody?.spec.recoveryHold;
      const hold = held && held.kind === 'DevicePowerOperation' && held.name === operation.metadata.name && (!operation.metadata.uid || held.uid === operation.metadata.uid)
        ? `Recovery hold since ${held.since}` : undefined;
      return { label: 'Outcome unknown', tone: 'danger', reason, hold };
    }
    default: return { label: PHASE_WORDS[phase] ?? phase, tone: 'progress', reason };
  }
}

/** "On → Off", from the evidence; undefined when no state was read. */
export function powerChange(evidence?: PowerEvidence): string | undefined {
  const before = evidence?.power?.before?.state;
  const after = evidence?.power?.after?.state;
  if (!before && !after) return undefined;
  return `${before ?? '?'} → ${after ?? '?'}`;
}

/** The evidence details for the tooltip: when the command went, and how a reset was proved. */
export function evidenceNote(evidence?: PowerEvidence): string | undefined {
  if (!evidence) return undefined;
  const parts = [
    evidence.requestSentAt ? `Command sent ${evidence.requestSentAt}` : 'No command sent',
    evidence.power?.before && `Before ${evidence.power.before.state} at ${evidence.power.before.observedAt}`,
    evidence.power?.after && `After ${evidence.power.after.state} at ${evidence.power.after.observedAt}`,
    evidence.resetEvidence === 'LastResetTime' ? 'BMC reported a reset' : evidence.resetEvidence === 'ObservedOff' ? 'Seen off during the reset' : undefined,
  ];
  return parts.filter(Boolean).join('\n');
}

/** Newest first. */
export const byNewest = (left: DevicePowerOperation, right: DevicePowerOperation) =>
  (right.metadata.creationTimestamp ?? '').localeCompare(left.metadata.creationTimestamp ?? '') || right.metadata.name.localeCompare(left.metadata.name);

export const isFor = (device: Device) => (operation: DevicePowerOperation) =>
  operation.spec.deviceRef.name === device.metadata.name && (!device.metadata.uid || operation.spec.deviceRef.uid === device.metadata.uid);

// ---- Reads and the one write ----

/** In fixture builds, operations created on the page stay in memory; nothing is sent. */
const fixtureCreated: DevicePowerOperation[] = [];

export async function fetchPowerOperations(device: Device): Promise<DevicePowerOperation[]> {
  if (fixtures) {
    const { fixturePowerOperations } = await import('./power-fixtures');
    return [...fixtureCreated, ...fixturePowerOperations].filter(isFor(device)).sort(byNewest);
  }
  // Operations are listed and filtered here: the CRD has no selectable field for the Device.
  const items: DevicePowerOperation[] = [];
  const seen = new Set<string>();
  let continuation = '';
  do {
    const page = await proxyFetch<List<DevicePowerOperation> & { metadata?: { continue?: string } }>(PROVIDER_PROJECT, `${OPERATIONS}?limit=500${continuation ? `&continue=${encodeURIComponent(continuation)}` : ''}`);
    items.push(...(page.items ?? []));
    continuation = page.metadata?.continue ?? '';
    if (continuation && seen.has(continuation)) throw new ApiError(502, 'Power operation pagination repeated');
    seen.add(continuation);
  } while (continuation);
  return items.filter(isFor(device)).sort(byNewest);
}

/** The Device's custody, or null when none exists. */
export async function fetchCustody(device: Device): Promise<DeviceCustody | null> {
  if (fixtures) return (await import('./power-fixtures')).fixtureCustody(device.metadata.name);
  try {
    const custody = await proxyFetch<DeviceCustody>(PROVIDER_PROJECT, `${CUSTODIES}/${encodeURIComponent(device.metadata.name)}`);
    // Custody names its Device by UID: a custody for a removed Device of the same name is not this one's.
    return custody && (!device.metadata.uid || custody.spec.deviceRef.uid === device.metadata.uid) ? custody : null;
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) return null;
    throw error;
  }
}

/** The authenticated caller, from a SelfSubjectReview through the same proxy the create goes through. */
export async function fetchCaller(): Promise<Caller> {
  if (fixtures) return { username: 'staff@example.com', uid: 'example-staff-uid' };
  const review = await proxyFetch<{ status?: { userInfo?: { username?: string; uid?: string } } }>(PROVIDER_PROJECT, '/apis/authentication.k8s.io/v1/selfsubjectreviews', {
    method: 'POST', body: { apiVersion: 'authentication.k8s.io/v1', kind: 'SelfSubjectReview' },
  });
  const user = review?.status?.userInfo;
  if (!user?.username || !user.uid) throw new ApiError(502, 'The API server did not return your username and UID.');
  return { username: user.username, uid: user.uid };
}

/** Whether the caller may create DevicePowerOperations: the staff power operator role. */
export async function powerPermission(): Promise<boolean> {
  if (fixtures) return true;
  return selfSubjectAllowed({ group: 'fleet.datumapis.com', version: 'v1alpha1', resource: 'devicepoweroperations', namespace: INVENTORY_NAMESPACE, verb: 'create' });
}

/**
 * Creates the operation once. The name is generated, so a retried POST would
 * be a second operation: a caller that cannot confirm the outcome must not
 * send it again.
 */
export async function createPowerOperation(device: Device, action: PowerAction, reason: string, caller: Caller, operation: InventoryOperation): Promise<DevicePowerOperation> {
  operation.assertCurrent();
  const body = powerRequest(device, action, reason, caller);
  if (!await powerPermission()) throw new ApiError(403, 'Power operator permission is not available.');
  operation.dispatch();
  if (fixtures) {
    const created: DevicePowerOperation = { ...body, metadata: { ...body.metadata, name: `${body.metadata.generateName}${Math.random().toString(36).slice(2, 7)}`, uid: crypto.randomUUID(), creationTimestamp: new Date().toISOString() }, status: { phase: 'Pending' } };
    fixtureCreated.unshift(created);
    return created;
  }
  const created = await proxyFetch<DevicePowerOperation>(PROVIDER_PROJECT, OPERATIONS, { method: 'POST', body }).catch(error => { operation.assertCurrent(); throw error; });
  operation.assertCurrent();
  if (!created?.metadata?.uid) throw new Error('The create outcome could not be confirmed.');
  return created;
}

// ---- Queries ----

export const POWER_QUERY_KEY = [PLUGIN_ID, 'device-power', PROVIDER_PROJECT, INVENTORY_NAMESPACE] as const;
/** Polled at the Bare Metal tabs' interval, and faster while an operation runs. */
const IDLE_MS = 10_000;
const RUNNING_MS = 2_000;

export function usePowerOperations(device: Device | undefined) {
  const read = useInventoryRead(() => fetchPowerOperations(device!));
  return useQuery<DevicePowerOperation[], ApiError>({
    queryKey: [...POWER_QUERY_KEY, 'operations', device?.metadata.name, device?.metadata.uid],
    queryFn: read, enabled: !!device, retry: false,
    // A failed read keeps polling, so one blip does not leave the page stale.
    refetchInterval: query => query.state.data?.some(running) ? RUNNING_MS : IDLE_MS,
  });
}

export function useCustody(device: Device | undefined) {
  const read = useInventoryRead(() => fetchCustody(device!));
  return useQuery<DeviceCustody | null, ApiError>({
    queryKey: [...POWER_QUERY_KEY, 'custody', device?.metadata.name, device?.metadata.uid],
    queryFn: read, enabled: !!device, retry: false,
    refetchInterval: query => query.state.data?.spec.activeOperation ? RUNNING_MS : IDLE_MS,
  });
}

/** Checked once and reused like the inventory permissions, so Refresh inventory checks again. */
export function usePowerPermission(enabled: boolean) {
  const read = useInventoryRead(powerPermission);
  return useQuery({ queryKey: [...PERMISSION_QUERY_KEY, 'DevicePowerOperation', INVENTORY_NAMESPACE, 'create'], queryFn: read, enabled, retry: false, gcTime: 600_000, staleTime: 600_000 });
}

export function useCaller(enabled: boolean) {
  const read = useInventoryRead(fetchCaller);
  return useQuery({ queryKey: [...POWER_QUERY_KEY, 'caller'], queryFn: read, enabled, retry: false, gcTime: 600_000, staleTime: 600_000 });
}
