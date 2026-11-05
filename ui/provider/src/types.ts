// View model for the machine list. The wire shape is the shared
// fleet.datumapis.com/v1alpha1 Machine in ui/shared/fleet-api.ts.

import { findCondition, type Condition } from '../../shared/conditions';
import type {
  List,
  Machine as RawMachine,
  MachineProfile as RawMachineProfile,
} from '../../shared/fleet-api';

export type { Condition, RawMachine, RawMachineProfile };
export type RawMachineList = List<RawMachine>;
export type RawMachineProfileList = List<RawMachineProfile>;

export const CAPACITY_POOL_LABEL = 'fleet.datumapis.com/capacity-pool';

export type ConditionState = 'True' | 'False' | 'Unknown';

export interface ConditionSummary {
  state: ConditionState;
  reason?: string;
}

export interface Machine {
  name: string;
  namespace?: string;
  pool?: string;
  allocationPolicy: string;
  cpu?: string;
  memory?: string;
  prepared: ConditionSummary;
  healthy: ConditionSummary;
  available: ConditionSummary;
}

// An absent condition means Unknown under the status contract.
function summarize(conditions: Condition[], type: string): ConditionSummary {
  const c = findCondition(conditions, type);
  return { state: c?.status ?? 'Unknown', reason: c?.reason };
}

function cpuSummary(cpu: NonNullable<NonNullable<RawMachine['status']>['hardware']>['cpu']) {
  if (cpu?.cores === undefined) return undefined;
  const threads = cpu.threads !== undefined ? ` / ${cpu.threads} threads` : '';
  const arch = cpu.architecture ? ` · ${cpu.architecture}` : '';
  return `${cpu.cores} cores${threads}${arch}`;
}

export function toMachine(raw: RawMachine): Machine {
  const conditions = raw.status?.conditions ?? [];
  const hw = raw.status?.hardware;
  return {
    name: raw.metadata.name,
    namespace: raw.metadata.namespace,
    pool: raw.metadata.labels?.[CAPACITY_POOL_LABEL],
    allocationPolicy: raw.spec.allocationPolicy ?? 'Disabled',
    cpu: cpuSummary(hw?.cpu),
    memory: hw?.memory?.capacity,
    prepared: summarize(conditions, 'Prepared'),
    healthy: summarize(conditions, 'Healthy'),
    available: summarize(conditions, 'Available'),
  };
}

// ---- MachineProfile ----

export interface MachineProfile {
  name: string;
  /** e.g. "≥ 24 cores · amd64" */
  cpu: string;
  memory: string;
  /** Absent when the profile asks for no GPUs. */
  gpus?: string;
  disks: number;
  interfaces: number;
  createdAt?: string;
}

type Bounds<T> = { min?: T; max?: T; exact?: T };

// Renders a min/max/exact constraint; an empty one means "any".
export function formatConstraint<T extends string | number>(c: Bounds<T> | undefined, unit = ''): string {
  const u = (v: T) => `${v}${unit}`;
  if (!c) return 'Any';
  if (c.exact !== undefined) return u(c.exact);
  if (c.min !== undefined && c.max !== undefined) return `${u(c.min)}–${u(c.max)}`;
  if (c.min !== undefined) return `≥ ${u(c.min)}`;
  if (c.max !== undefined) return `≤ ${u(c.max)}`;
  return 'Any';
}

function gpuSummary(gpus: RawMachineProfile['spec']['gpus']): string | undefined {
  if (!gpus) return undefined;
  const count = formatConstraint(gpus.count);
  const what = [gpus.vendor, gpus.models?.join(' / ')].filter(Boolean).join(' ');
  return what ? `${count} × ${what}` : count;
}

export function toMachineProfile(raw: RawMachineProfile): MachineProfile {
  const { cpu, memory, gpus, disks, interfaces } = raw.spec;
  return {
    name: raw.metadata.name,
    cpu: `${formatConstraint(cpu.cores, ' cores')} · ${cpu.architecture}`,
    memory: formatConstraint(memory.capacity),
    gpus: gpuSummary(gpus),
    disks: disks?.length ?? 0,
    interfaces: interfaces?.length ?? 0,
    createdAt: raw.metadata.creationTimestamp,
  };
}
