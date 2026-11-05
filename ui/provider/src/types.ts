// View model for the machine list. The wire shape is the shared
// fleet.datumapis.com/v1alpha1 Machine in ui/shared/fleet-api.ts.

import { findCondition, type Condition } from '../../shared/conditions';
import { formatConstraint, summarizeProfile, type ProfileSummary } from '../../shared/profile-summary';
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

export { formatConstraint };

export interface MachineProfile extends ProfileSummary {
  name: string;
  createdAt?: string;
}

export function toMachineProfile(raw: RawMachineProfile): MachineProfile {
  return {
    name: raw.metadata.name,
    ...summarizeProfile(raw.spec),
    createdAt: raw.metadata.creationTimestamp,
  };
}
