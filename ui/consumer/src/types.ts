// View model for the claim list. The wire shape is the shared
// fleet.datumapis.com/v1alpha1 MachineClaim in ui/shared/fleet-api.ts.

import { findCondition, isConditionTrue, type Condition } from '../../shared/conditions';
import type { List, MachineClaim as RawMachineClaim } from '../../shared/fleet-api';

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
