// View model for the claim list. The wire shape is the shared
// fleet.datumapis.com/v1alpha1 MachineClaim in ui/shared/fleet-api.ts.

import { findCondition, isConditionTrue, type Condition } from '../../shared/conditions';
import type {
  List,
  MachineClaim as RawMachineClaim,
  MachineClass as RawMachineClass,
  MachineProfile as RawMachineProfile,
} from '../../shared/fleet-api';
import { summarizeProfile, type ProfileSummary } from '../../shared/profile-summary';

export type { Condition, RawMachineClaim };
export type RawMachineClaimList = List<RawMachineClaim>;

export type ClaimPhase = 'Pending' | 'Bound' | 'Ready' | 'Releasing' | 'Rejected';

export interface MachineClaim {
  name: string;
  phase: ClaimPhase;
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
}

// Only an explicit True moves a claim past Pending. A deleted claim stays
// listed, held by Fleet's finalizer, until release and cleanup complete.
export function claimPhase(
  conditions: Condition[],
  deleting = false
): { phase: ClaimPhase; reason?: string } {
  const isTrue = (type: string) => isConditionTrue(conditions, type);
  if (deleting) return { phase: 'Releasing' };
  const accepted = findCondition(conditions, 'Accepted');
  if (accepted?.status === 'False') return { phase: 'Rejected', reason: accepted.reason };
  if (isTrue('Ready')) return { phase: 'Ready' };
  if (isTrue('Bound')) return { phase: 'Bound', reason: findCondition(conditions, 'Ready')?.reason };
  return { phase: 'Pending', reason: findCondition(conditions, 'Bound')?.reason };
}

export function toMachineClaim(raw: RawMachineClaim): MachineClaim {
  const { phase, reason } = claimPhase(raw.status?.conditions ?? [], !!raw.metadata.deletionTimestamp);
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
  };
}

// ---- Catalog: what this project can claim ----

export type { RawMachineClass, RawMachineProfile };
export type RawMachineClassList = List<RawMachineClass>;
export type RawMachineProfileList = List<RawMachineProfile>;

export interface CatalogProfile extends ProfileSummary {
  name: string;
  /** False when the class allows it but no profile of that name is in the project yet. */
  published: boolean;
}

export interface CatalogOffering {
  /** The project-local class name a claim references. */
  className: string;
  /** "datum-cloud/standard-metal" for a published class; absent for the project's own. */
  source?: string;
  /** Unavailable offerings are listed so a missing publication is visible, not silent. */
  available: boolean;
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

// ---- Claim detail ----

/** The conditions Fleet writes on a claim, in lifecycle order. */
export const CLAIM_CONDITIONS = ['Accepted', 'Bound', 'NetworkReady', 'Ready'] as const;

export interface ClaimCondition {
  type: string;
  status: 'True' | 'False' | 'Unknown';
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
}

export interface ClaimInterface {
  name: string;
  network: string;
  addresses: string[];
  ready?: 'True' | 'False' | 'Unknown';
}

export interface ClaimDetail extends MachineClaim {
  createdAt?: string;
  /** Set while Fleet releases the machine after deletion. */
  deletingSince?: string;
  /** The profile Fleet accepted; differs from the request only if Fleet says so. */
  acceptedProfile?: string;
  placement?: string;
  /** Allocated hardware, one line per part; absent until a machine is bound. */
  hardware?: { label: string; value: string }[];
  hardwareObservedAt?: string;
  interfaces: ClaimInterface[];
  installation?: string;
  conditions: ClaimCondition[];
}

function hardwareLines(hw: NonNullable<NonNullable<RawMachineClaim['status']>['allocatedHardware']>['hardware']) {
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

export function toClaimDetail(raw: RawMachineClaim): ClaimDetail {
  const status = raw.status ?? {};
  const conditions = status.conditions ?? [];
  const known = new Set<string>(CLAIM_CONDITIONS);
  return {
    ...toMachineClaim(raw),
    createdAt: raw.metadata.creationTimestamp,
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
      ...CLAIM_CONDITIONS.map(
        (type) => findCondition(conditions, type) ?? { type, status: 'Unknown' as const }
      ),
      ...conditions.filter((c) => !known.has(c.type)),
    ],
  };
}
