// View model for the capacity list. The wire shape is the shared
// metal.datumapis.com/v1alpha1 MachineCapacity in ui/shared/fleet-api.ts.

import { findCondition, type Condition } from '../../shared/conditions';
import { formatConstraint, summarizeProfile, type ProfileSummary } from '../../shared/profile-summary';
import type {
  List,
  MachineCapacity as RawMachineCapacity,
  MachineClass as RawMachineClass,
  MachineProfile as RawMachineProfile,
} from '../../shared/fleet-api';

export type { Condition, RawMachineCapacity, RawMachineClass, RawMachineProfile };
export type RawMachineClassList = List<RawMachineClass>;
export type RawMachineCapacityList = List<RawMachineCapacity>;
export type RawMachineProfileList = List<RawMachineProfile>;

export const CAPACITY_POOL_LABEL = 'fleet.datumapis.com/capacity-pool';

export type ConditionState = 'True' | 'False' | 'Unknown';

export interface ConditionSummary {
  state: ConditionState;
  reason?: string;
}

export interface MachineCapacity {
  name: string;
  registrationProtected?: boolean;
  namespace?: string;
  /** The backing Device's record name, in the capacity's namespace. */
  device?: string;
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

function cpuSummary(cpu: NonNullable<NonNullable<RawMachineCapacity['status']>['hardware']>['cpu']) {
  if (cpu?.cores === undefined) return undefined;
  const threads = cpu.threads !== undefined ? ` / ${cpu.threads} threads` : '';
  const arch = cpu.architecture ? ` · ${cpu.architecture}` : '';
  return `${cpu.cores} cores${threads}${arch}`;
}

export type CapacityState = 'Available' | 'Allocated' | 'Disabled' | 'Unhealthy' | 'Unavailable' | 'Unknown';

/**
 * One word for what an operator scans for. Healthy=False is a problem even on
 * Disabled capacity; Disabled capacity is otherwise expected to be idle.
 * Enabled capacity reads Available only when Fleet reports all three
 * conditions True; anything unreported reads Unknown, never free.
 */
export function capacityState(capacity: Pick<MachineCapacity, 'allocationPolicy' | 'prepared' | 'healthy' | 'available'>): CapacityState {
  const { prepared, healthy, available } = capacity;
  if (healthy.state === 'False') return 'Unhealthy';
  if (capacity.allocationPolicy === 'Disabled') return 'Disabled';
  if (available.state === 'False') return available.reason === 'Allocated' ? 'Allocated' : 'Unavailable';
  if (available.state === 'True' && prepared.state === 'True' && healthy.state === 'True') return 'Available';
  return 'Unknown';
}

export function toMachineCapacity(raw: RawMachineCapacity): MachineCapacity {
  const conditions = raw.status?.conditions ?? [];
  const hw = raw.status?.hardware;
  return {
    name: raw.metadata.name,
    ...(raw.metadata.annotations?.['fleet.datumapis.com/registration-operation'] && { registrationProtected: true }),
    namespace: raw.metadata.namespace,
    device: raw.spec.deviceRef?.name || undefined,
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

// ---- MachineClass ----

export interface MachineClass {
  name: string;
  /** A class published from another project; staff rarely see these here. */
  source?: string;
  allowedProfiles: string[];
  customProfiles: boolean;
  /** The MachineCapacities namespace and the capacity pool label value the class selects. */
  capacityNamespace?: string;
  capacityPool?: string;
  powerControl: boolean;
  accepted: ConditionSummary;
  createdAt?: string;
}

export function toMachineClass(raw: RawMachineClass): MachineClass {
  const spec = raw.spec ?? {};
  const capacities = spec.capacity?.machineCapacities;
  const pool = capacities?.selector?.matchLabels?.[CAPACITY_POOL_LABEL];
  return {
    name: raw.metadata.name,
    source: spec.source && `${spec.source.project}/${spec.source.name}`,
    allowedProfiles: (spec.profilePolicy?.allowedProfiles ?? []).map((p) => p.name),
    customProfiles: !!spec.profilePolicy?.customProfiles?.enabled,
    capacityNamespace: capacities?.namespace,
    capacityPool: pool,
    powerControl: !!spec.access?.powerControl,
    accepted: summarize(raw.status?.conditions ?? [], 'Accepted'),
    createdAt: raw.metadata.creationTimestamp,
  };
}
