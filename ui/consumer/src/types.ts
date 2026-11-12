// View model for the machine list. The wire shape is the shared
// fleet.datumapis.com/v1alpha1 Machine in ui/shared/fleet-api.ts.

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
  cpu?: string;
  memory?: string;
  createdAt?: string;
  /** Accepted but no capacity bound yet: the common Pending case. */
  waitingForCapacity: boolean;
}

export type StatusTone = 'success' | 'warning' | 'danger' | 'muted';

/** The label and colour a Machine's phase shows as, in the list and on its page. */
export function machineStatus(machine: Pick<Machine, 'phase' | 'waitingForCapacity'>): {
  tone: StatusTone;
  label: string;
} {
  switch (machine.phase) {
    case 'Ready':
      return { tone: 'success', label: 'Ready' };
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
  const hw = raw.status?.allocatedHardware?.hardware;
  return {
    name: raw.metadata.name,
    phase,
    phaseReason: reason,
    machineClass: raw.spec.machineClassRef.name,
    machineProfile: raw.spec.machineProfileRef.name,
    location: raw.spec.locationRef.name,
    powerIntent: raw.spec.powerState ?? 'Off',
    powerObserved: raw.status?.power?.state,
    cpu: hw?.cpu?.cores !== undefined ? `${hw.cpu.cores} cores${hw.cpu.architecture ? ` · ${hw.cpu.architecture}` : ''}` : undefined,
    memory: hw?.memory?.capacity,
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
  /** "datum-cloud/standard-metal" for a published class; absent for the project's own. */
  source?: string;
  /** Unavailable offerings are listed so a missing publication is visible, not silent. */
  available: boolean;
  /** Fleet said this class can't resolve or was rejected, as opposed to not having answered yet. */
  failed: boolean;
  unavailableReason?: string;
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
  const unavailableReason = failed
    ? (failed.message ?? failed.reason)
    : !policy
      ? "Waiting for the provider's offering to resolve."
      : undefined;

  const byName = new Map(profiles.map((p) => [p.metadata.name, p]));
  return {
    className: raw.metadata.name,
    source: source && `${source.project}/${source.name}`,
    available,
    failed: !!failed,
    unavailableReason,
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

export interface MachineCondition {
  type: string;
  status: 'True' | 'False' | 'Unknown';
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
}

export interface MachineInterface {
  name: string;
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
  installation?: string;
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
  const known = new Set<string>(MACHINE_CONDITIONS);
  return {
    ...toMachine(raw),
    deletingSince: raw.metadata.deletionTimestamp,
    acceptedProfile: status.acceptedProfileRef?.name,
    placement: status.placement?.locationRef.name,
    hardware: status.allocatedHardware && hardwareLines(status.allocatedHardware.hardware),
    hardwareObservedAt: status.allocatedHardware?.observedAt,
    interfaces: (status.network?.interfaces ?? []).map((i) => ({
      name: i.name,
      network: i.network.namespace ? `${i.network.namespace}/${i.network.name}` : i.network.name,
      addresses: (i.addresses ?? []).map((a) => a.address),
      ready: findCondition(i.conditions ?? [], 'Ready')?.status,
    })),
    installation: status.currentInstallationRef?.name,
    // An absent condition is Unknown; extra conditions follow the known ones.
    conditions: [
      ...MACHINE_CONDITIONS.map(
        (type) => findCondition(conditions, type) ?? { type, status: 'Unknown' as const }
      ),
      ...conditions.filter((c) => !known.has(c.type)),
    ],
  };
}

// ---- Progress: the four lifecycle conditions as ordered steps ----

export type StepState = 'done' | 'current' | 'upcoming' | 'failed';

export interface MachineStep {
  type: string;
  label: string;
  state: StepState;
  /** The condition's message, or its reason; for upcoming steps, what they wait for. */
  detail?: string;
  at?: string;
}

const STEP_LABELS: Record<string, string> = {
  Accepted: 'Accepted',
  Bound: 'Bound',
  NetworkReady: 'Network ready',
  Ready: 'Ready',
};

// A condition Fleet hasn't written yet is a step still to come, not "Unknown".
// The first step that isn't True is the current one; a False Accepted means
// the request was rejected.
export function machineSteps(conditions: MachineCondition[]): MachineStep[] {
  let reachedCurrent = false;
  return MACHINE_CONDITIONS.map((type, i) => {
    const c = conditions.find((x) => x.type === type);
    const label = STEP_LABELS[type];
    if (c?.status === 'True' && !reachedCurrent) {
      return { type, label, state: 'done', detail: c.message ?? c.reason, at: c.lastTransitionTime };
    }
    if (reachedCurrent) {
      return { type, label, state: 'upcoming', detail: `After ${STEP_LABELS[MACHINE_CONDITIONS[i - 1]].toLowerCase()}` };
    }
    reachedCurrent = true;
    const failed = type === 'Accepted' && c?.status === 'False';
    return {
      type,
      label,
      state: failed ? 'failed' : 'current',
      detail: c?.message ?? c?.reason ?? 'In progress',
      at: c?.lastTransitionTime,
    };
  });
}
