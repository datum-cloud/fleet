// View model for the machine list. The wire shape is the shared
// metal.datumapis.com/v1alpha1 Machine in ui/shared/fleet-api.ts.

import { findCondition, isConditionTrue, type Condition } from '../../shared/conditions';
import type {
  List,
  Machine as RawMachine,
  MachineClass as RawMachineClass,
  MachineProfile as RawMachineProfile,
} from '../../shared/fleet-api';
import { summarizeProfile, type ProfileSummary } from '../../shared/profile-summary';

export type { Condition, RawMachine };
export type RawMachineList = List<RawMachine>;

export type MachinePhase = 'Pending' | 'Bound' | 'Ready' | 'Releasing' | 'Rejected';

export interface Machine {
  name: string;
  phase: MachinePhase;
  phaseReason?: string;
  machineClass: string;
  machineProfile: string;
  location: string;
  /** Requested power state (spec). */
  powerIntent: 'On' | 'Off';
  /** Observed power state (status); absent until reported. */
  powerObserved?: 'On' | 'Off' | 'Unknown';
  /** Requested CPU pinned in status.requestedHardware; absent until resolved. */
  cpu?: string;
  /** Requested memory pinned in status.requestedHardware; absent until resolved. */
  memory?: string;
  /** Every interface address Fleet reports, in interface order. */
  addresses: string[];
  createdAt?: string;
  /** Accepted but no capacity bound yet: the common Pending case. */
  waitingForCapacity: boolean;
}

// Ready is 'ok', drawn in a neutral colour: green is kept for verified
// state, which the consumer UI has none of.
export type StatusTone = 'ok' | 'warning' | 'danger' | 'muted';

/** The label and colour a Machine's phase shows as, in the list and on its page. */
export function machineStatus(machine: Pick<Machine, 'phase' | 'waitingForCapacity'>): {
  tone: StatusTone;
  label: string;
} {
  switch (machine.phase) {
    case 'Ready':
      return { tone: 'ok', label: 'Ready' };
    case 'Bound':
      return { tone: 'warning', label: 'Preparing' };
    case 'Releasing':
      return { tone: 'muted', label: 'Releasing' };
    case 'Rejected':
      return { tone: 'danger', label: 'Rejected' };
    default:
      return machine.waitingForCapacity
        ? { tone: 'warning', label: 'Waiting for capacity' }
        : { tone: 'muted', label: 'Pending' };
  }
}

// ---- Customer words for Fleet's condition reasons ----

// Condition messages are written for operators and can describe other
// tenants' capacity ("…allocated to another consumer"), so the consumer UI
// never renders one. Known reasons map to a short word; any other reason
// shows a generic word, with the reason itself only in a tooltip.
const REASON_WORDS: Record<string, string> = {
  SyntheticFixtureAccepted: 'Accepted',
  ExclusiveBinding: 'Dedicated to you',
  MachineAllocationCommitted: 'Dedicated to you',
  InsufficientCapacity: 'Waiting for capacity',
  NoAttachmentsRequired: 'No network requested',
  AttachmentsVerified: 'Connected',
  NetworkPending: 'Connecting',
  NetworkNotAvailableInLocation: 'Network not offered here',
  AddressFamilyNotCarried: 'Address type not offered',
  PoweredOffHandoff: 'Handed over',
  SyntheticHandoffReady: 'Handed over',
  ProfileNotAllowed: 'Profile not offered',
  ProfileNotFound: 'Profile not offered',
  ProfileInvalid: 'Profile not offered',
  ProfileUnsupported: 'Profile not offered',
  QuotaPending: 'Checking quota',
  QuotaDenied: 'Over quota',
  AuthorizationUnavailable: "Couldn't check access",
  OutcomeUnknown: 'Checking',
  EvidenceStale: 'Checking',
  DependenciesPending: 'In progress',
  CleanupBlocked: 'Release delayed',
};

// A Kubernetes reason is one CamelCase token. Anything else may be message
// text, so it is never shown, not even in a tooltip.
const REASON_TOKEN = /^[A-Za-z][A-Za-z0-9]*$/;

export interface ReasonText {
  word: string;
  /** The raw reason, only when it is unmapped and a plain token. */
  tooltip?: string;
}

/** The customer word for a condition reason; `fallback` when it has none. */
export function reasonText(reason: string | undefined, fallback: string): ReasonText {
  if (reason && REASON_WORDS[reason]) return { word: REASON_WORDS[reason] };
  return { word: fallback, tooltip: reason && REASON_TOKEN.test(reason) ? reason : undefined };
}

// Only an explicit True moves a machine past Pending. A deleted machine stays
// listed, held by Fleet's finalizer, until release and cleanup complete.
export function machinePhase(
  conditions: Condition[],
  deleting = false
): { phase: MachinePhase; reason?: string } {
  const isTrue = (type: string) => isConditionTrue(conditions, type);
  if (deleting) return { phase: 'Releasing' };
  const accepted = findCondition(conditions, 'Accepted');
  if (accepted?.status === 'False') return { phase: 'Rejected', reason: accepted.reason };
  if (isTrue('Ready')) return { phase: 'Ready' };
  if (isTrue('Bound')) return { phase: 'Bound', reason: findCondition(conditions, 'Ready')?.reason };
  return { phase: 'Pending', reason: findCondition(conditions, 'Bound')?.reason };
}

export function toMachine(raw: RawMachine): Machine {
  const conditions = raw.status?.conditions ?? [];
  const { phase, reason } = machinePhase(conditions, !!raw.metadata.deletionTimestamp);
  // Listings show what was requested, never a guess: an unresolved request
  // stays blank rather than falling back to the profile or allocation.
  const requested = raw.status?.requestedHardware;
  return {
    name: raw.metadata.name,
    phase,
    phaseReason: reason,
    machineClass: raw.spec.machineClassRef.name,
    machineProfile: raw.spec.machineProfileRef.name,
    location: raw.spec.locationRef.name,
    powerIntent: raw.spec.powerState ?? 'Off',
    powerObserved: raw.status?.power?.state,
    cpu: requested ? `${requested.cpu.cores} cores · ${requested.cpu.architecture}` : undefined,
    memory: requested?.memory.capacity,
    addresses: (raw.status?.network?.interfaces ?? []).flatMap((i) => (i.addresses ?? []).map((a) => a.address)),
    createdAt: raw.metadata.creationTimestamp,
    waitingForCapacity: phase === 'Pending' && isConditionTrue(conditions, 'Accepted'),
  };
}

// ---- Catalog: what this project can deploy ----

export type { RawMachineClass, RawMachineProfile };
export type RawMachineClassList = List<RawMachineClass>;
export type RawMachineProfileList = List<RawMachineProfile>;

export interface CatalogProfile extends ProfileSummary {
  name: string;
  /** False when the class allows it but no profile of that name is in the project yet. */
  published: boolean;
}

export interface CatalogOffering {
  /** The project-local class name a machine references. */
  className: string;
  /** Unavailable offerings are listed so a missing publication is visible, not silent. */
  available: boolean;
  /** Fleet said this class can't resolve or was rejected, as opposed to not having answered yet. */
  failed: boolean;
  powerControl: boolean;
  profiles: CatalogProfile[];
}

// A published class (spec.source set) is only usable once Fleet resolves it
// into status.resolvedClass. The provider project's own class needs no
// resolution, so its spec policy stands in until status catches up.
export function toCatalogOffering(
  raw: RawMachineClass,
  profiles: RawMachineProfile[]
): CatalogOffering {
  const conditions = raw.status?.conditions ?? [];
  const source = raw.spec?.source;
  const resolved = raw.status?.resolvedClass;
  const policy = resolved?.profilePolicy ?? (source ? undefined : raw.spec?.profilePolicy);
  const access = resolved?.access ?? (source ? undefined : raw.spec?.access);

  const failed = [source ? 'SourceResolved' : 'Accepted']
    .map((type) => findCondition(conditions, type))
    .find((c) => c?.status === 'False');
  const available = !!policy && !failed;

  const byName = new Map(profiles.map((p) => [p.metadata.name, p]));
  return {
    className: raw.metadata.name,
    available,
    failed: !!failed,
    powerControl: !!access?.powerControl,
    profiles: (policy?.allowedProfiles ?? []).map(({ name }) => {
      const profile = byName.get(name);
      return profile
        ? { name, published: true, ...summarizeProfile(profile.spec) }
        : { name, published: false, cpu: '—', memory: '—', disks: 0, interfaces: 0 };
    }),
  };
}

export function toCatalog(classes: RawMachineClass[], profiles: RawMachineProfile[]): CatalogOffering[] {
  return classes
    .map((c) => toCatalogOffering(c, profiles))
    .sort((a, b) => a.className.localeCompare(b.className));
}

// ---- Machine detail ----

/** The conditions Fleet writes on a machine, in lifecycle order. */
export const MACHINE_CONDITIONS = ['Accepted', 'Bound', 'NetworkReady', 'Ready'] as const;

/** A lifecycle condition without its message, which is never shown. */
export interface MachineCondition {
  type: string;
  status: 'True' | 'False' | 'Unknown';
  reason?: string;
  lastTransitionTime?: string;
}

export interface MachineInterface {
  name: string;
  /** The network's name; its namespace is internal. */
  network: string;
  addresses: string[];
  ready?: 'True' | 'False' | 'Unknown';
}

export interface MachineDetail extends Machine {
  /** Set while Fleet releases the machine after deletion. */
  deletingSince?: string;
  /** The profile Fleet accepted; differs from the request only if Fleet says so. */
  acceptedProfile?: string;
  placement?: string;
  /** Allocated hardware, one line per part; absent until a machine is bound. */
  hardware?: { label: string; value: string }[];
  hardwareObservedAt?: string;
  interfaces: MachineInterface[];
  /** The four lifecycle conditions, in order; others are not shown. */
  conditions: MachineCondition[];
}

function hardwareLines(hw: NonNullable<NonNullable<RawMachine['status']>['allocatedHardware']>['hardware']) {
  const lines: { label: string; value: string }[] = [];
  const cpu = hw.cpu;
  if (cpu) {
    const counts = [
      cpu.sockets !== undefined && `${cpu.sockets} sockets`,
      cpu.cores !== undefined && `${cpu.cores} cores`,
      cpu.threads !== undefined && `${cpu.threads} threads`,
    ].filter(Boolean);
    const value = [cpu.model, counts.join(' / '), cpu.architecture].filter(Boolean).join(' · ');
    if (value) lines.push({ label: 'CPU', value });
  }
  if (hw.memory?.capacity) lines.push({ label: 'Memory', value: hw.memory.capacity });
  for (const gpu of hw.gpus ?? []) {
    lines.push({ label: `GPU ${gpu.name}`, value: [gpu.vendor, gpu.model, gpu.memory].filter(Boolean).join(' · ') || '—' });
  }
  for (const disk of hw.disks ?? []) {
    lines.push({ label: `Disk ${disk.name}`, value: [disk.capacity, disk.media, disk.interface].filter(Boolean).join(' · ') || '—' });
  }
  for (const nic of hw.interfaces ?? []) {
    lines.push({ label: `NIC ${nic.name}`, value: [nic.type, nic.speed].filter(Boolean).join(' · ') || '—' });
  }
  return lines;
}

export function toMachineDetail(raw: RawMachine): MachineDetail {
  const status = raw.status ?? {};
  const conditions = status.conditions ?? [];
  return {
    ...toMachine(raw),
    deletingSince: raw.metadata.deletionTimestamp,
    acceptedProfile: status.acceptedProfileRef?.name,
    placement: status.placement?.locationRef.name,
    hardware: status.allocatedHardware && hardwareLines(status.allocatedHardware.hardware),
    hardwareObservedAt: status.allocatedHardware?.observedAt,
    interfaces: (status.network?.interfaces ?? []).map((i) => ({
      name: i.name,
      network: i.network.name,
      addresses: (i.addresses ?? []).map((a) => a.address),
      ready: findCondition(i.conditions ?? [], 'Ready')?.status,
    })),
    // An absent condition is Unknown. The message is dropped here so no
    // component can render it.
    conditions: MACHINE_CONDITIONS.map((type) => {
      const c = findCondition(conditions, type);
      if (!c) return { type, status: 'Unknown' as const };
      return { type, status: c.status, reason: c.reason, lastTransitionTime: c.lastTransitionTime };
    }),
  };
}

// ---- Progress: the four lifecycle conditions as ordered steps ----

export type StepState = 'done' | 'current' | 'upcoming' | 'failed';

export interface MachineStep {
  type: string;
  label: string;
  state: StepState;
  /** A customer word for the condition's reason; blank on upcoming steps. */
  detail?: string;
  /** The raw reason, when it has no customer word and is a plain token. */
  detailTooltip?: string;
  at?: string;
}

const STEP_LABELS: Record<string, string> = {
  Accepted: 'Requested',
  Bound: 'Server assigned',
  NetworkReady: 'Network',
  Ready: 'Ready',
};

const STEP_FALLBACK: Record<Exclude<StepState, 'upcoming'>, string> = {
  done: 'Done',
  current: 'In progress',
  failed: 'Not accepted',
};

// A condition Fleet hasn't written yet is a step still to come, not "Unknown".
// The first step that isn't True is the current one; a False Accepted means
// the request was rejected. Messages are never shown (see reasonText).
export function machineSteps(conditions: MachineCondition[]): MachineStep[] {
  let reachedCurrent = false;
  return MACHINE_CONDITIONS.map((type) => {
    const c = conditions.find((x) => x.type === type);
    const label = STEP_LABELS[type];
    if (reachedCurrent) return { type, label, state: 'upcoming' };
    const state: StepState =
      c?.status === 'True' ? 'done' : type === 'Accepted' && c?.status === 'False' ? 'failed' : 'current';
    if (state !== 'done') reachedCurrent = true;
    const { word, tooltip } = reasonText(c?.reason, STEP_FALLBACK[state]);
    return { type, label, state, detail: word, detailTooltip: tooltip, at: c?.lastTransitionTime };
  });
}
