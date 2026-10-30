// Hand-written subset of the proposed fleet.datumapis.com/v1alpha1 MachineClaim
// (docs/enhancements/machine-lifecycle/README.md). Only the fields the claim
// list reads; the API is provisional and has no generated types yet.

export interface Condition {
  type: string;
  status: 'True' | 'False' | 'Unknown';
  reason?: string;
  message?: string;
}

export interface LocalRef {
  name: string;
}

export interface RawMachineClaim {
  metadata: {
    name: string;
    namespace?: string;
    uid?: string;
    creationTimestamp?: string;
  };
  spec: {
    machineClassRef: LocalRef;
    machineProfileRef: LocalRef;
    locationRef: LocalRef;
    powerState?: 'On' | 'Off';
  };
  status?: {
    allocatedHardware?: {
      hardware?: {
        cpu?: { architecture?: string; cores?: number };
        memory?: { capacity?: string };
      };
    };
    power?: { state?: 'On' | 'Off' };
    conditions?: Condition[];
  };
}

export interface RawMachineClaimList {
  items?: RawMachineClaim[];
}

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
  powerObserved?: 'On' | 'Off';
  cpu?: string;
  memory?: string;
}

function condition(conditions: Condition[], type: string): Condition | undefined {
  return conditions.find((c) => c.type === type);
}

// An absent condition means Unknown (status contract), so only an explicit
// True moves a claim past Pending.
export function claimPhase(conditions: Condition[]): { phase: ClaimPhase; reason?: string } {
  const isTrue = (type: string) => condition(conditions, type)?.status === 'True';
  if (isTrue('Releasing')) return { phase: 'Releasing' };
  const accepted = condition(conditions, 'Accepted');
  if (accepted?.status === 'False') return { phase: 'Rejected', reason: accepted.reason };
  if (isTrue('Ready')) return { phase: 'Ready' };
  if (isTrue('Bound')) return { phase: 'Bound', reason: condition(conditions, 'Ready')?.reason };
  return { phase: 'Pending', reason: condition(conditions, 'Bound')?.reason };
}

export function toMachineClaim(raw: RawMachineClaim): MachineClaim {
  const { phase, reason } = claimPhase(raw.status?.conditions ?? []);
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
