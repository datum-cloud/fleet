// Shared by ui/consumer and ui/provider. Keep this directory free of package
// imports: each plugin has its own node_modules, and a bare import here would
// not resolve from either.
//
// Wire shapes of metal.datumapis.com/v1alpha1, transcribed by hand from
// internal/api/v1alpha1/types.go and config/base/crd/metal.datumapis.com.yaml
// as of api/machine-allocation-model (#44), which renames MachineClaim to
// Machine and Machine to MachineCapacity. Fields
// keep the Go JSON names; `omitempty` fields are optional. Installation is left
// out until a page reads it.

import type { Condition } from './conditions';

// The resources moved from fleet.datumapis.com in #46; label, annotation and
// finalizer keys below still use the fleet.datumapis.com prefix.
export const METAL_API_GROUP = 'metal.datumapis.com';
export const METAL_API_VERSION = 'v1alpha1';

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
  managedFields?: { manager?: string; subresource?: string }[];
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

/** Exact CPU request: physical cores across sockets. No ranges. */
export interface CPURequirement {
  architecture: string;
  cores: number;
}

/** Exact installed RAM as a positive integer byte quantity, such as "32Gi". */
export interface MemoryRequirement {
  capacity: string;
}

/** Ranges remain for GPU, disk and interface requirements only. */
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
    cpu: CPURequirement;
    memory: MemoryRequirement;
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

/**
 * Controller-maintained snapshot of the exact CPU and memory resolved from the
 * Machine's MachineProfile. Absent until resolved (see the RequestResolved
 * condition) and pinned afterwards: profile changes never rewrite it.
 */
export interface RequestedHardware {
  cpu: CPURequirement;
  memory: MemoryRequirement;
  source: {
    machineGeneration: number;
    profileRef: LocalReference;
    profileGeneration: number;
    /** "sha256:<hex>" over the complete resolved profile spec. */
    profileDigest: string;
  };
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
    /** What was asked for; independent of allocation. */
    requestedHardware?: RequestedHardware;
    /** What was actually assigned; absent until bound. */
    allocatedHardware?: { observedAt?: string; hardware: Hardware };
    currentInstallationRef?: LocalReference;
    network?: { revision?: string; interfaces?: NetworkInterfaceStatus[] };
    power?: { state: PowerState | 'Unknown' };
    /** RequestResolved, Accepted, Bound, NetworkReady and Ready. */
    conditions?: Condition[];
  };
}

/** Held on a Machine until Fleet's release and cleanup complete. */
export const MACHINE_FINALIZER = 'fleet.datumapis.com/machine-protection';

// Physical inventory: fleet.datumapis.com/v1alpha1 DeviceClaim and Device,
// transcribed from internal/api/inventory/v1alpha1/types.go and
// config/components/inventory/crds/fleet.datumapis.com.yaml. Operators write
// both; status is reserved and has no fields yet.

export const INVENTORY_API_GROUP = 'fleet.datumapis.com';
export const INVENTORY_API_VERSION = 'v1alpha1';

export interface PortPeer {
  device: string;
  port: string;
}

export interface DeviceClaim {
  apiVersion?: string;
  kind?: string;
  metadata: ObjectMeta;
  spec: {
    expectedIdentity: { manufacturer?: string; model: string; serial?: string };
    lifecycle?: 'Installation';
    displayName?: string;
    cableRefs?: LocalReference[];
    placement: { facility?: string; rack?: string; position?: number; height?: number; face?: 'front' | 'rear'; scope?: 'Facility' | 'RackMembership' | 'RackUnits' | 'NonRackUnitMount'; facilityRef?: LocalReference; rackRef?: LocalReference; mount?: string };
    ports?: { name: string; type?: string; speedMbps?: number; expectedPeer?: PortPeer }[];
  };
}

export interface Device {
  apiVersion?: string;
  kind?: string;
  metadata: ObjectMeta;
  spec: {
    reportedPlacement?: ReportedPlacement;
    deviceTypeRef?: LocalReference;
    claimRef: LocalReference;
    identity: { manufacturer?: string; model: string; serial?: string; systemUUID?: string };
    assetTag?: string;
    displayName?: string;
    modelShape?: { heightUnits?: number; fullDepth?: boolean };
    providerHostRef?: ExternalReference;
    topologyAuthority?: 'Cable';
    components?: PhysicalComponent[];
    internalMappings?: { from: ComponentPosition; to: ComponentPosition }[];
    serviceAssociations?: { serviceRef: ExternalReference; componentID: string }[];
    bmc?: { endpoint: string };
    interfaces?: { name: string; macAddress?: string; peer?: PortPeer }[];
    unmappedMACAddresses?: string[];
  };
}

export interface ReportedPlacement {
  revision: number;
  observedAt: string;
  reason: string;
  reference: string;
  facilityRef?: LocalReference;
  rackRef?: LocalReference;
  position?: number;
  height?: number;
  face?: 'front' | 'rear';
  fullDepth?: boolean;
  mount?: string;
}

export interface ExternalReference { system: string; type: string; id: string }
export interface ComponentPosition { componentID: string; position: number }
export interface PhysicalComponent {
  id: string;
  name: string;
  type: 'NetworkPort' | 'PowerInlet' | 'PowerOutlet' | 'ConsolePort' | 'ConsoleServerPort' | 'FrontPort' | 'RearPort' | 'Module' | 'InventoryItem' | 'DeviceBay';
  connector?: string;
  positions?: number;
  serial?: string;
  assetTag?: string;
}
export interface CableEndpoint extends ComponentPosition {
  deviceRef?: LocalReference;
  powerFeedRef?: LocalReference;
}
export interface PhysicalRecord {
  metadata: ObjectMeta;
  kind: 'Site' | 'Facility' | 'Rack' | 'Cable' | 'PowerFeed' | 'Manufacturer' | 'DeviceType';
  spec: {
    displayName: string;
    code?: string;
    canonicalName?: string;
    aliases?: string[];
    manufacturerRef?: LocalReference;
    model?: string;
    modelShape?: { heightUnits?: number; fullDepth?: boolean };
    componentTemplates?: Omit<PhysicalComponent, 'serial' | 'assetTag'>[];
    facilityRefs?: LocalReference[];
    identifiers?: { provider: string; type: 'PremisesCode' | 'PremisesAlias' | 'PremisesID'; value: string }[];
    facilityRef?: LocalReference;
    rackRef?: LocalReference;
    locationRef?: LocalReference;
    heightUnits?: number;
    medium?: string;
    lanes?: { id: string; a: CableEndpoint; b: CableEndpoint }[];
    supply?: { type: string; voltageV?: number; currentA?: number; phase?: string };
    terminations?: PhysicalComponent[];
    upstreamSources?: ExternalReference[];
  };
}
