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
    /** Immutable Device name. Registration records the Device UID and version in annotations. */
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
// both specs. DeviceClaim status is reserved; Device status is written only by
// BMC discovery, through the status subresource.

export const INVENTORY_API_GROUP = 'fleet.datumapis.com';
export const INVENTORY_API_VERSION = 'v1alpha1';

export interface DeviceClaim {
  apiVersion?: string;
  kind?: string;
  metadata: ObjectMeta;
  spec: {
    expectedIdentity: { manufacturer?: string; model: string; serial?: string };
    lifecycle: 'Installation';
    displayName?: string;
    cableRefs?: LocalReference[];
    placement: { scope: 'Facility' | 'RackMembership' | 'RackUnits' | 'NonRackUnitMount'; facilityRef?: LocalReference; rackRef?: LocalReference; position?: number; height?: number; face?: RackFace; mount?: string };
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
    // unidentified records deliberately that no serial or system UUID exists;
    // it excludes both and requires unidentifiedReason.
    identity: { manufacturer?: string; model: string; serial?: string; systemUUID?: string; unidentified?: boolean; unidentifiedReason?: string };
    assetTag?: string;
    displayName?: string;
    modelShape?: ModelShape;
    providerHostRef?: ExternalReference;
    topologyAuthority?: 'Cable';
    components?: PhysicalComponent[];
    internalMappings?: { from: ComponentPosition; to: ComponentPosition }[];
    serviceAssociations?: { serviceRef: ExternalReference; componentID: string }[];
    bmc?: { endpoint: string };
    interfaces?: { name: string; macAddress?: string }[];
    unmappedMACAddresses?: string[];
    // lifecycle defaults to Active. Active and Decommissioning move between
    // each other, Decommissioning moves to Retired, and Retired is terminal.
    lifecycle?: DeviceLifecycle;
  };
  status?: DeviceStatus;
}

/** What read-only BMC discovery observed. spec.identity stays authoritative. */
export interface DeviceStatus {
  observedGeneration?: number;
  /** A failed attempt never clears it: each observation keeps its own time and endpoint. */
  discovery?: DeviceDiscovery;
  /** Reachable, Authenticated, Discovered, IdentityMatched and PublicKeyUnique. */
  conditions?: Condition[];
}

export interface DeviceDiscovery {
  lastAttemptAt?: string;
  lastAttemptEndpoint?: string;
  tls?: ObservedTLS;
  serviceRoot?: ObservedServiceRoot;
  inventory?: ObservedInventory;
}

export interface ObservedTLS {
  observedAt: string;
  endpoint: string;
  /** Lowercase hex SHA-256 of the leaf public key. */
  publicKeySHA256: string;
  notAfter?: string;
  selfSigned?: boolean;
}

export interface ObservedServiceRoot {
  observedAt: string;
  endpoint: string;
  redfishVersion?: string;
  uuid?: string;
  vendor?: string;
  product?: string;
}

export interface ObservedInventory {
  observedAt: string;
  endpoint: string;
  managers?: ObservedManager[];
  systems?: ObservedSystem[];
}

export interface ObservedManager {
  id: string;
  model?: string;
  firmwareVersion?: string;
  networkInterfaces?: ObservedNetworkInterface[];
}

export interface ObservedSystem {
  id: string;
  uuid?: string;
  manufacturer?: string;
  model?: string;
  serialNumber?: string;
  sku?: string;
  powerState?: string;
  networkInterfaces?: ObservedNetworkInterface[];
  /** The ComputerSystem.Reset types the BMC advertises, read but never invoked. */
  resetTypes?: string[];
}

export interface ObservedNetworkInterface {
  id: string;
  macAddress?: string;
  linkStatus?: string;
  speedMbps?: number;
}

export type DeviceLifecycle = 'Active' | 'Decommissioning' | 'Retired';

export interface ReportedPlacement {
  revision: number;
  observedAt: string;
  reason: string;
  reference: string;
  facilityRef?: LocalReference;
  rackRef?: LocalReference;
  position?: number;
  height?: number;
  face?: RackFace;
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
// The mounted rack face: the rack face the chassis front points toward.
export type RackFace = 'front' | 'rear';
// A side of the chassis itself, never of the rack. Unset means unknown.
export type ChassisFace = 'Front' | 'Rear';
// A DeviceType component template. A Device component inherits its face from
// the template whose id equals the component id.
export interface ComponentTemplate extends Omit<PhysicalComponent, 'serial' | 'assetTag'> {
  face?: ChassisFace;
  // Cells of one faceplate bank on the template's own face. Unset means its
  // position is not recorded.
  layout?: TemplateLayout;
}
// A nominal bank-grid position: 1-based row and column; span counts columns
// and is 1 when unset.
export interface TemplateLayout { bank: string; row: number; column: number; span?: number }
// Nominal cell size in millimetres, for module cells such as PSUs and bays.
export interface CellSize { width: number; height: number }
// One grid of cells on one chassis face. offset is the left edge as a
// fraction of the usable face width; on a face it is set on every bank or
// none, and with none the banks flow left to right in list order.
export interface FaceplateBank { id: string; face: ChassisFace; rows: number; columns: number; offset?: number; cellMM?: CellSize }
// DeviceType only. Unset means the layout is not recorded.
export interface Faceplate { banks: FaceplateBank[] }
// Nominal chassis width. Unset is unknown; drawings assume Full and say so.
export type ChassisWidth = 'Full' | 'Half';
export interface ModelShape { heightUnits?: number; fullDepth?: boolean; width?: ChassisWidth }
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
    modelShape?: ModelShape;
    // DeviceType only. modelShape.fullDepth unset means depth unknown; a
    // Device's own value overrides it.
    componentTemplates?: ComponentTemplate[];
    faceplate?: Faceplate;
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

// Device custody and power operations: fleet.datumapis.com/v1alpha1
// DeviceCustody and DevicePowerOperation, transcribed from
// internal/api/operations/v1alpha1. The coordinator is the only writer of
// custody and of operation status; a requester writes an operation's spec
// once, at create.

export type CustodyHolderType = 'None' | 'Allocation' | 'Release' | 'Service' | 'Retired';
export type OperationPhase = 'Pending' | 'Admitted' | 'Dispatched' | 'Verifying' | 'Succeeded' | 'Failed' | 'Cancelled' | 'OutcomeUnknown';

export interface OperationReference { kind: string; name: string; uid: string }
export interface OperationCreator { username: string; uid: string }

export interface DeviceCustody {
  metadata: ObjectMeta;
  spec: {
    deviceRef: { name: string; uid: string };
    state: 'Verified' | 'Unverified';
    holder: { type: CustodyHolderType; allocationRef?: LocalReference; openGates?: string[] };
    fence: number;
    activeOperation?: OperationReference & { fence: number; phase: OperationPhase; admittedAt: string; deadline: string; creator: OperationCreator };
    lastResult?: OperationReference & { fence: number; phase: OperationPhase; reason?: string; message?: string; finishedAt: string };
    recoveryHold?: OperationReference & { fence: number; since: string; reason: string; deadline?: string; retries?: number };
    conflicts?: string[];
  };
}

/** One command; each value is the Redfish ComputerSystem.Reset type of the same name. */
export type PowerAction = 'On' | 'ForceOff' | 'ForceRestart' | 'PowerCycle';
export type ObservedPowerState = 'On' | 'Off' | 'PoweringOn' | 'PoweringOff' | 'Paused';

/** Power result evidence. It never carries a credential or BMC response content. */
export interface PowerEvidence {
  requestSentAt?: string;
  power?: { before?: { state: ObservedPowerState; observedAt: string }; after?: { state: ObservedPowerState; observedAt: string } };
  resetEvidence?: 'LastResetTime' | 'ObservedOff';
}

export interface DevicePowerOperation {
  apiVersion?: string;
  kind?: string;
  metadata: ObjectMeta & { generateName?: string };
  spec: {
    creator: OperationCreator;
    deviceRef: { name: string; uid: string };
    /** Exactly one: a staff serviceRequest, or the Metal provider's allocationRef. */
    authority: { serviceRequest?: { reason: string }; allocationRef?: LocalReference };
    action: PowerAction;
    /** Staff only: the server staff expect behind the BMC. */
    expectedSystemUUID?: string;
    /** Provider only. */
    machineGeneration?: number;
  };
  status?: {
    phase?: OperationPhase;
    reason?: string;
    message?: string;
    fence?: number;
    admittedAt?: string;
    deadline?: string;
    finishedAt?: string;
    evidence?: PowerEvidence;
  };
}
