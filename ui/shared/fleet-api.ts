// Shared by ui/consumer and ui/provider. Keep this directory free of package
// imports: each plugin has its own node_modules, and a bare import here would
// not resolve from either.
//
// Wire shapes of fleet.datumapis.com/v1alpha1, transcribed by hand from
// internal/api/v1alpha1/types.go and config/base/crd/fleet.datumapis.com.yaml
// as of api/machine-allocation-model (#44), which renames MachineClaim to
// Machine and Machine to MachineCapacity. Fields
// keep the Go JSON names; `omitempty` fields are optional. Installation is left
// out until a page reads it.

import type { Condition } from './conditions';

export const FLEET_API_GROUP = 'fleet.datumapis.com';
export const FLEET_API_VERSION = 'v1alpha1';

export interface ObjectMeta {
  name: string;
  namespace?: string;
  uid?: string;
  resourceVersion?: string;
  generation?: number;
  creationTimestamp?: string;
  /** Set while finalizers (such as Machine release) are still running. */
  deletionTimestamp?: string;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  finalizers?: string[];
}

export interface List<T> {
  items?: T[];
  metadata?: { continue?: string; resourceVersion?: string };
}

export interface LocalReference {
  name: string;
}

export interface TypedLocalReference {
  apiGroup: string;
  kind: string;
  name: string;
}

export interface TypedNamespacedReference extends TypedLocalReference {
  namespace: string;
}

export interface LabelSelector {
  matchLabels?: Record<string, string>;
  matchExpressions?: { key: string; operator: string; values?: string[] }[];
}

// ---- Observed hardware (MachineCapacity status and Machine allocatedHardware) ----

export interface Hardware {
  cpu?: { architecture?: string; model?: string; sockets?: number; cores?: number; threads?: number };
  memory?: { capacity?: string };
  /** Omitted when empty, so an absent list does not prove there are no GPUs. */
  gpus?: { name: string; vendor?: string; model?: string; memory?: string }[];
  disks?: { name: string; capacity?: string; media?: string; interface?: string }[];
  interfaces?: { name: string; type?: string; speed?: string }[];
}

// ---- MachineClass (cluster-scoped) ----

export interface ProfilePolicy {
  allowedProfiles?: LocalReference[];
  customProfiles?: { enabled?: boolean };
}

export interface AccessPolicy {
  consoleModes?: string[];
  powerControl?: boolean;
}

export interface MachineClass {
  metadata: ObjectMeta;
  spec?: {
    /** Set on a consumer project's class that publishes a provider's class. */
    source?: { project: string; name: string };
    profilePolicy?: ProfilePolicy;
    access?: AccessPolicy;
    /** Set on the provider's class: where its MachineCapacities live. */
    capacity?: {
      machineCapacities: { namespace: string; selector?: LabelSelector };
    };
  };
  status?: {
    evaluatedRevision?: string;
    resolvedClass?: { profilePolicy?: ProfilePolicy; access?: AccessPolicy };
    conditions?: Condition[];
  };
}

// ---- MachineProfile (cluster-scoped) ----

export interface Constraint {
  min?: number;
  max?: number;
  exact?: number;
}

/** Kubernetes quantities, such as "128Gi". */
export interface QuantityConstraint {
  min?: string;
  max?: string;
  exact?: string;
}

export interface MachineProfile {
  metadata: ObjectMeta;
  spec: {
    cpu: { architecture: string; cores?: Constraint };
    memory: { capacity: QuantityConstraint };
    gpus?: { count: Constraint; vendor?: string; models?: string[]; memoryPerDevice?: QuantityConstraint };
    disks?: { name: string; count: Constraint; capacity: QuantityConstraint; media?: string; interface?: string }[];
    interfaces?: { name: string; type?: string; speed?: QuantityConstraint }[];
  };
  status?: { conditions?: Condition[] };
}

// ---- MachineCapacity (namespaced, provider project only) ----

export type AllocationPolicy = 'Enabled' | 'Disabled';

export interface MachineCapacity {
  metadata: ObjectMeta;
  spec: {
    /** Immutable. Names the physical Device; nothing resolves it yet. */
    deviceRef: LocalReference;
    /** Defaults to Disabled. */
    allocationPolicy: AllocationPolicy;
    cleaningPolicyRef: LocalReference;
    reclaimPolicy: 'Retain';
  };
  status?: {
    hardware?: Hardware;
    /** Prepared, Healthy and Available. */
    conditions?: Condition[];
  };
}

// ---- Machine (namespaced, consumer project) ----

export type PowerState = 'On' | 'Off';

export interface NetworkInterfaceRequest {
  name: string;
  network: { namespace?: string; name: string };
  ipFamilies?: ('IPv4' | 'IPv6')[];
  addresses?: { classRef: LocalReference }[];
  reclaimPolicy?: 'Delete' | 'Retain';
}

export interface NetworkInterfaceStatus {
  name: string;
  network: TypedNamespacedReference;
  macAddress?: string;
  mtu?: number;
  addresses?: { address: string; family: string; gateway?: string; primary?: boolean }[];
  externalAddresses?: { address: string; family: string; classRef: LocalReference }[];
  routes?: { destination: string; gateway?: string }[];
  dns?: { servers?: string[]; searchDomains?: string[] };
  conditions?: Condition[];
}

export interface Machine {
  metadata: ObjectMeta;
  spec: {
    machineClassRef: LocalReference;
    machineProfileRef: LocalReference;
    locationRef: LocalReference;
    initialInstallationRef?: LocalReference;
    networkInterfaces?: NetworkInterfaceRequest[];
    /** Defaults to Off. */
    powerState?: PowerState;
  };
  status?: {
    /** Free-form; the controllers write Pending and Ready so far. */
    phase?: string;
    placement?: { locationRef: LocalReference };
    acceptedProfileRef?: LocalReference;
    allocatedHardware?: { observedAt?: string; hardware: Hardware };
    currentInstallationRef?: LocalReference;
    network?: { revision?: string; interfaces?: NetworkInterfaceStatus[] };
    power?: { state: PowerState | 'Unknown' };
    /** Accepted, Bound, NetworkReady and Ready. */
    conditions?: Condition[];
  };
}

/** Held on a Machine until Fleet's release and cleanup complete. */
export const MACHINE_FINALIZER = 'fleet.datumapis.com/machine-protection';
