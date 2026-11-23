// Joins a Device's typed components to the Cable lanes that name them.
//
// Everything here is derived from records, not from the wire. Status is
// empty on every inventory kind, so a link can be "cable recorded" at best;
// nothing is ever verified. "No cable recorded" means unknown, never free.
// A denied collection read is never treated as an absent record.

import type { CableEndpoint, ComponentPosition, Device, PhysicalComponent, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { deviceCatalog } from './hardware-catalog';

export type LinkState = 'cabled' | 'none' | 'conflict' | 'unavailable' | 'catalog';
export type ConnectionGroup = 'power' | 'network' | 'console' | 'passthrough' | 'other' | 'unrecorded';

export const linkStateWords: Record<LinkState, string> = {
  cabled: 'Cable recorded',
  none: 'No cable recorded',
  conflict: 'Records disagree',
  unavailable: 'Peer unavailable',
  catalog: 'Catalog only',
};

// Each state has a symbol as well as words and a line style, so colour is
// never the only signal.
export const linkStateGlyph: Record<LinkState, string> = {
  cabled: '●',
  none: '○',
  conflict: '!',
  unavailable: '?',
  catalog: '◇',
};

export const connectionGroups: { group: ConnectionGroup; title: string }[] = [
  { group: 'power', title: 'Power' },
  { group: 'network', title: 'Network' },
  { group: 'console', title: 'Console' },
  { group: 'passthrough', title: 'Pass-through' },
  { group: 'other', title: 'Modules and items' },
  { group: 'unrecorded', title: 'Not on this Device' },
];

const groupOf: Record<PhysicalComponent['type'], ConnectionGroup> = {
  PowerInlet: 'power', PowerOutlet: 'power',
  NetworkPort: 'network',
  ConsolePort: 'console', ConsoleServerPort: 'console',
  FrontPort: 'passthrough', RearPort: 'passthrough',
  Module: 'other', InventoryItem: 'other', DeviceBay: 'other',
};

// Modules, items and bays rarely terminate cables. They join the view only
// when a Cable names them.
const UNCABLED_TYPES = new Set<PhysicalComponent['type']>(['Module', 'InventoryItem', 'DeviceBay']);

// Above this many links the diagram stops being readable, so the view
// defaults to the list and collapses the diagram by group.
export const DIAGRAM_LINK_LIMIT = 32;

// Internal-path traces (patch panels, PDUs) stop after this many hops.
export const MAX_TRACE_HOPS = 4;

type PeerKind = 'Device' | 'PowerFeed';

/**
 * The far end of a Cable lane. When the peer collection is denied, only the
 * kind and access are kept: no name, component or position is exposed.
 */
export interface PeerEnd {
  kind: PeerKind;
  access: 'readable' | 'denied' | 'missing';
  name?: string;
  label?: string;
  componentID?: string;
  componentName?: string;
  position?: number;
  /** True when the peer component records more than one position. */
  multiPosition?: boolean;
  /** Recorded supply rating, for PowerFeed peers. */
  supply?: string;
}

export interface CableRef {
  name: string;
  label: string;
  medium?: string;
  laneID: string;
  laneCount: number;
}

/** One hop beyond a peer, through that peer's recorded internal mapping. */
export interface TraceHop {
  /** The peer component and position the internal mapping leads to. */
  via: { componentID: string; componentName?: string; position: number };
  state: Exclude<LinkState, 'catalog'>;
  cable?: CableRef;
  peer?: PeerEnd;
  reasons: string[];
}

export interface ConnectionLink {
  key: string;
  group: ConnectionGroup;
  state: LinkState;
  componentID: string;
  component?: PhysicalComponent;
  template?: Omit<PhysicalComponent, 'serial' | 'assetTag'>;
  position: number;
  positions: number;
  /** This Device's own internal mapping from this position, if recorded. */
  internal?: { componentID: string; componentName?: string; position: number };
  cable?: CableRef;
  peer?: PeerEnd;
  trace: TraceHop[];
  reasons: string[];
}

export interface DeviceConnections {
  /** When Cables are denied, connections are unknown and links is empty. */
  cableAccess: 'readable' | 'denied';
  catalog: 'compared' | 'unassigned' | 'unavailable';
  links: ConnectionLink[];
}

interface Termination {
  cable: PhysicalRecord;
  laneID: string;
  mine: CableEndpoint;
  far: CableEndpoint;
}

const positionsOf = (component?: { positions?: number }) => component?.positions ?? 1;
const recordLabel = (record: { metadata: { name: string }; spec: { displayName?: string } }) => record.spec.displayName || record.metadata.name;

function endKey(endpoint: CableEndpoint) {
  const kind = endpoint.deviceRef ? 'Device' : endpoint.powerFeedRef ? 'PowerFeed' : '';
  return `${kind}/${endpoint.deviceRef?.name ?? endpoint.powerFeedRef?.name ?? ''}/${endpoint.componentID}/${endpoint.position}`;
}

function inventoryDevices(inventory: Inventory): Device[] {
  return [...inventory.slots.flatMap(slot => slot.devices), ...inventory.unboundDevices];
}

function cableRef(cable: PhysicalRecord, laneID: string): CableRef {
  return { name: cable.metadata.name, label: recordLabel(cable), medium: cable.spec.medium, laneID, laneCount: cable.spec.lanes?.length ?? 0 };
}

function findMapping(device: Device, componentID: string, position: number) {
  for (const mapping of device.spec.internalMappings ?? []) {
    if (mapping.from.componentID === componentID && mapping.from.position === position) return mapping.to;
    if (mapping.to.componentID === componentID && mapping.to.position === position) return mapping.from;
  }
}

class Join {
  private readonly devices = new Map<string, Device>();
  private readonly feeds = new Map<string, PhysicalRecord>();
  // Every recorded termination in the namespace, so claims on the far port
  // are visible as well as claims on this Device's ports.
  private readonly index = new Map<string, Termination[]>();

  constructor(private readonly inventory: Inventory, namespace: string | undefined) {
    for (const device of inventoryDevices(inventory)) if (device.metadata.namespace === namespace) this.devices.set(device.metadata.name, device);
    for (const record of inventory.physical) {
      if (record.metadata.namespace !== namespace) continue;
      if (record.kind === 'PowerFeed') this.feeds.set(record.metadata.name, record);
      if (record.kind !== 'Cable') continue;
      for (const lane of record.spec.lanes ?? [])
        for (const [mine, far] of [[lane.a, lane.b], [lane.b, lane.a]] as const) {
          const key = endKey(mine);
          this.index.set(key, [...(this.index.get(key) ?? []), { cable: record, laneID: lane.id, mine, far }]);
        }
    }
  }

  terminationsAt(kind: PeerKind, name: string, componentID: string, position: number) {
    return this.index.get(`${kind}/${name}/${componentID}/${position}`) ?? [];
  }

  terminationsOn(device: Device) {
    const prefix = `Device/${device.metadata.name}/`;
    return [...this.index.entries()].filter(([key]) => key.startsWith(prefix)).flatMap(([, value]) => value);
  }

  /** Resolves a far end and lists any way the records contradict it. */
  peer(far: CableEndpoint): { peer: PeerEnd; reasons: string[]; device?: Device } {
    const kind: PeerKind = far.deviceRef ? 'Device' : 'PowerFeed';
    const name = far.deviceRef?.name ?? far.powerFeedRef?.name ?? '';
    if (this.inventory.issues?.[kind]) return { peer: { kind, access: 'denied' }, reasons: [] };
    const record = kind === 'Device' ? this.devices.get(name) : this.feeds.get(name);
    if (!record) return { peer: { kind, access: 'missing', name, componentID: far.componentID, position: far.position }, reasons: [] };
    const components = kind === 'Device' ? (record as Device).spec.components : (record as PhysicalRecord).spec.terminations;
    const component = components?.find(item => item.id === far.componentID);
    const supply = kind === 'PowerFeed' ? (record as PhysicalRecord).spec.supply : undefined;
    const peer: PeerEnd = {
      kind, access: 'readable', name, label: recordLabel(record), componentID: far.componentID, componentName: component?.name,
      position: far.position, multiPosition: positionsOf(component) > 1 || far.position > 1,
      supply: supply && `${supply.type} · ${supply.voltageV ?? 'unknown'} V · ${supply.currentA ?? 'unknown'} A`,
    };
    const reasons: string[] = [];
    if (!component) reasons.push(`The peer does not record component "${far.componentID}".`);
    else if (far.position > positionsOf(component)) reasons.push(`The Cable names position ${far.position}; the peer component records ${positionsOf(component)}.`);
    const claims = this.terminationsAt(kind, name, far.componentID, far.position);
    if (claims.length > 1) reasons.push(`The peer port is named by ${claims.length} Cable lanes: ${[...new Set(claims.map(claim => claim.cable.metadata.name))].join(', ')}.`);
    return { peer, reasons, device: kind === 'Device' ? record as Device : undefined };
  }

  /** Follows a peer Device's internal mapping (patch panel or PDU) onward. */
  trace(device: Device | undefined, far: CableEndpoint, seen: Set<string>): TraceHop[] {
    const hops: TraceHop[] = [];
    let current = device;
    let at: ComponentPosition = far;
    while (current && hops.length < MAX_TRACE_HOPS) {
      const mapped = findMapping(current, at.componentID, at.position);
      if (!mapped) break;
      const visit = `${current.metadata.name}/${mapped.componentID}/${mapped.position}`;
      if (seen.has(visit)) break;
      seen.add(visit);
      const via = { componentID: mapped.componentID, componentName: current.spec.components?.find(item => item.id === mapped.componentID)?.name, position: mapped.position };
      const onward = this.terminationsAt('Device', current.metadata.name, mapped.componentID, mapped.position);
      if (!onward.length) { hops.push({ via, state: 'none', reasons: [] }); break; }
      if (onward.length > 1) { hops.push({ via, state: 'conflict', reasons: [`${onward.length} Cable lanes name this position: ${[...new Set(onward.map(item => item.cable.metadata.name))].join(', ')}.`] }); break; }
      const next = this.peer(onward[0].far);
      hops.push({ via, state: next.peer.access !== 'readable' ? 'unavailable' : next.reasons.length ? 'conflict' : 'cabled', cable: cableRef(onward[0].cable, onward[0].laneID), peer: next.peer, reasons: next.reasons });
      if (next.peer.access !== 'readable' || next.reasons.length) break;
      current = next.device;
      at = onward[0].far;
    }
    return hops;
  }
}

/**
 * Maps a Device's components to the Cable lanes that name them. Returns one
 * link per component position and claiming lane, plus links for Cables that
 * name a component or position the Device does not record, and DeviceType
 * templates the Device does not record.
 */
export function deviceConnections(device: Device, inventory: Inventory): DeviceConnections {
  const catalogState: DeviceConnections['catalog'] = !device.spec.deviceTypeRef ? 'unassigned' : deviceCatalog(device, inventory) ? 'compared' : 'unavailable';
  if (inventory.issues?.Cable) return { cableAccess: 'denied', catalog: catalogState, links: [] };
  const join = new Join(inventory, device.metadata.namespace);
  const components = device.spec.components ?? [];
  const byID = new Map(components.map(component => [component.id, component]));
  const templates = deviceCatalog(device, inventory)?.model.spec.componentTemplates ?? [];
  const links: ConnectionLink[] = [];

  const internalFor = (componentID: string, position: number) => {
    const mapped = findMapping(device, componentID, position);
    return mapped && { componentID: mapped.componentID, componentName: byID.get(mapped.componentID)?.name, position: mapped.position };
  };

  const linkFor = (base: Omit<ConnectionLink, 'state' | 'cable' | 'peer' | 'trace' | 'reasons' | 'key'>, termination: Termination, claims: number, localReasons: string[], index: number): ConnectionLink => {
    const resolved = join.peer(termination.far);
    const reasons = [...localReasons];
    if (claims > 1) reasons.push(`${claims} Cable lanes name this position. Neither is treated as current.`);
    reasons.push(...resolved.reasons);
    const unavailable = resolved.peer.access !== 'readable';
    const state: LinkState = localReasons.length || claims > 1 ? 'conflict' : unavailable ? 'unavailable' : resolved.reasons.length ? 'conflict' : 'cabled';
    const trace = state === 'cabled' ? join.trace(resolved.device, termination.far, new Set([`${device.metadata.name}/${base.componentID}/${base.position}`])) : [];
    return { ...base, key: `${base.componentID}/${base.position}/${termination.cable.metadata.name}/${termination.laneID}/${index}`, state, cable: cableRef(termination.cable, termination.laneID), peer: resolved.peer, trace, reasons };
  };

  for (const component of components) {
    const positions = positionsOf(component);
    for (let position = 1; position <= positions; position++) {
      const claims = join.terminationsAt('Device', device.metadata.name, component.id, position);
      const base = { group: groupOf[component.type], componentID: component.id, component, position, positions, internal: internalFor(component.id, position) };
      if (!claims.length) {
        if (!UNCABLED_TYPES.has(component.type)) links.push({ ...base, key: `${component.id}/${position}`, state: 'none', trace: [], reasons: [] });
        continue;
      }
      claims.forEach((claim, index) => links.push(linkFor(base, claim, claims.length, [], index)));
    }
  }

  // Cables that name a component this Device lacks, or a position above the
  // component's recorded count.
  const orphans = join.terminationsOn(device).filter(termination => {
    const component = byID.get(termination.mine.componentID);
    return !component || termination.mine.position > positionsOf(component);
  });
  orphans.forEach((termination, index) => {
    const component = byID.get(termination.mine.componentID);
    const template = templates.find(item => item.id === termination.mine.componentID);
    const reason = component
      ? `The Cable names position ${termination.mine.position}; this Device records ${positionsOf(component)} for "${component.id}".`
      : `The Cable names component "${termination.mine.componentID}", which this Device does not record${template ? ' (it appears only in the catalog)' : ''}.`;
    links.push(linkFor({ group: component ? groupOf[component.type] : 'unrecorded', componentID: termination.mine.componentID, component, template, position: termination.mine.position, positions: positionsOf(component) }, termination, 1, [reason], index));
  });

  const orphanIDs = new Set(orphans.map(termination => termination.mine.componentID));
  for (const template of templates) {
    if (byID.has(template.id) || orphanIDs.has(template.id)) continue;
    links.push({ key: `catalog/${template.id}`, group: groupOf[template.type], state: 'catalog', componentID: template.id, template, position: 1, positions: positionsOf(template), trace: [], reasons: [] });
  }

  return { cableAccess: 'readable', catalog: catalogState, links };
}

export function stateCounts(links: ConnectionLink[]) {
  const counts: Record<LinkState, number> = { cabled: 0, none: 0, conflict: 0, unavailable: 0, catalog: 0 };
  for (const link of links) counts[link.state]++;
  return counts;
}

export function localLabel(link: Pick<ConnectionLink, 'component' | 'template' | 'componentID' | 'position' | 'positions'>) {
  const name = link.component?.name ?? link.template?.name ?? link.componentID;
  return link.positions > 1 || link.position > 1 ? `${name} · position ${link.position}` : name;
}

/** The far end in words. A denied peer yields no identifying detail. */
export function peerLabel(peer: PeerEnd | undefined) {
  if (!peer) return 'Unknown';
  if (peer.access === 'denied') return peer.kind === 'PowerFeed' ? 'Power feed unavailable' : 'Device unavailable';
  const port = peer.componentName ?? peer.componentID;
  const position = peer.multiPosition ? ` · position ${peer.position}` : '';
  if (peer.access === 'missing') return `${peer.name} · ${peer.componentID}${position} (record not found)`;
  return `${peer.label} · ${port}${position}`;
}

export function remotePortLabel(peer: PeerEnd | undefined) {
  if (!peer || peer.access === 'denied') return 'Port unavailable';
  return `${peer.componentName ?? peer.componentID}${peer.multiPosition || (peer.position ?? 1) > 1 ? ` · position ${peer.position}` : ''}`;
}
