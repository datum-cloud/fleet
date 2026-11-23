// Joins a Device's typed components to the Cable lanes that name them.
//
// Everything here is derived from records, not from the wire. Status is
// empty on every inventory kind, so a link can be "cable recorded" at best;
// nothing is ever verified. "No cable recorded" means unknown, never free.
// A denied collection read is never treated as an absent record.

import type { CableEndpoint, ChassisFace, ComponentPosition, ComponentTemplate, Device, PhysicalComponent, PhysicalRecord, RackFace } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { condensedName, humanName, isGeneratedName } from './display-names';
import { deviceCatalog } from './hardware-catalog';
import { connectorKind, type ConnectorKind } from './connector-kind';
import { componentChassisFace, componentRackFace, deviceMountedFace } from './rack-faces';

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

export const groupOf: Record<PhysicalComponent['type'], ConnectionGroup> = {
  PowerInlet: 'power', PowerOutlet: 'power',
  NetworkPort: 'network',
  ConsolePort: 'console', ConsoleServerPort: 'console',
  FrontPort: 'passthrough', RearPort: 'passthrough',
  Module: 'other', InventoryItem: 'other', DeviceBay: 'other',
};

const TYPE_TITLES: Record<PhysicalComponent['type'], [string, string]> = {
  NetworkPort: ['Network port', 'Network ports'],
  PowerInlet: ['Power inlet', 'Power inlets'],
  PowerOutlet: ['Power outlet', 'Power outlets'],
  ConsolePort: ['Console port', 'Console ports'],
  ConsoleServerPort: ['Console server port', 'Console server ports'],
  FrontPort: ['Front pass-through port', 'Front pass-through ports'],
  RearPort: ['Rear pass-through port', 'Rear pass-through ports'],
  Module: ['Module', 'Modules'],
  InventoryItem: ['Inventory item', 'Inventory items'],
  DeviceBay: ['Device bay', 'Device bays'],
};
/** Component types in display order. */
export const componentTypes = Object.keys(TYPE_TITLES) as PhysicalComponent['type'][];
/** "Network port" for one, "Network ports" otherwise. */
export const typeTitle = (type: PhysicalComponent['type'], count = 2) => (TYPE_TITLES[type] ?? [type, type])[count === 1 ? 0 : 1];

// Modules, items and bays rarely terminate cables. They join the view only
// when a Cable names them.
export const UNCABLED_TYPES = new Set<PhysicalComponent['type']>(['Module', 'InventoryItem', 'DeviceBay']);

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
  /** The resource name. Views show it only in the detail panel. */
  name?: string;
  /** A human name, when the record has one. Never a generated resource name. */
  label?: string;
  componentID?: string;
  componentName?: string;
  position?: number;
  /** True when the peer component records more than one position. */
  multiPosition?: boolean;
  /** Recorded supply rating, for PowerFeed peers. */
  supply?: string;
  /** The rack face the peer component lands on, when both faces are known. */
  rackFace?: RackFace;
}

export interface CableRef {
  /** The resource name. Views show it only in the detail panel. */
  name: string;
  /** A human name, when the record has one. Never a generated resource name. */
  label?: string;
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
  template?: ComponentTemplate;
  /** Inherited from the DeviceType template with the same id. Unset is unknown. */
  chassisFace?: ChassisFace;
  /** The chassis face transformed by the mounted rack face. Unset is unknown. */
  rackFace?: RackFace;
  position: number;
  positions: number;
  /** This Device's own internal mapping from this position, if recorded. */
  internal?: { componentID: string; componentName?: string; position: number };
  cable?: CableRef;
  peer?: PeerEnd;
  trace: TraceHop[];
  reasons: string[];
  /** Soft notes worth a look. They never change the state. */
  checks: string[];
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

// Generated resource names are shown only in a port's pinned card; every
// other label comes from humanName() in display-names.ts.

/** A Device's title, never its generated resource name. */
export function deviceTitle(device: Device) {
  return humanName(device) ?? ([device.spec.identity.manufacturer, device.spec.identity.model].filter(Boolean).join(' ') || 'Unnamed device');
}

const recordLabel = humanName;

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
    return this.terminationsFor('Device', device.metadata.name);
  }

  terminationsFor(kind: PeerKind, name: string) {
    const prefix = `${kind}/${name}/`;
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
      rackFace: kind === 'Device' && component ? componentRackFace(deviceMountedFace(record as Device, this.inventory), componentChassisFace(record as Device, this.inventory, far.componentID)) : undefined,
    };
    const reasons: string[] = [];
    if (!component) reasons.push(`The peer does not record component "${far.componentID}".`);
    else if (far.position > positionsOf(component)) reasons.push(`The Cable names position ${far.position}; the peer component records ${positionsOf(component)}.`);
    const claims = this.terminationsAt(kind, name, far.componentID, far.position);
    if (claims.length > 1) reasons.push(`The peer port is named by ${claims.length} Cable lanes: ${[...new Set(claims.map(claim => cableName(cableRef(claim.cable, claim.laneID))))].join(', ')}.`);
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
  const mounted = deviceMountedFace(device, inventory);
  const facesOf = (componentID: string) => {
    const chassisFace = templates.find(template => template.id === componentID)?.face;
    return { chassisFace, rackFace: componentRackFace(mounted, chassisFace) };
  };

  const internalFor = (componentID: string, position: number) => {
    const mapped = findMapping(device, componentID, position);
    return mapped && { componentID: mapped.componentID, componentName: byID.get(mapped.componentID)?.name, position: mapped.position };
  };

  const linkFor = (base: Omit<ConnectionLink, 'state' | 'cable' | 'peer' | 'trace' | 'reasons' | 'checks' | 'key'>, termination: Termination, claims: number, localReasons: string[], index: number): ConnectionLink => {
    const resolved = join.peer(termination.far);
    const reasons = [...localReasons];
    if (claims > 1) reasons.push(`${claims} Cable lanes name this position.`);
    reasons.push(...resolved.reasons);
    const unavailable = resolved.peer.access !== 'readable';
    const state: LinkState = localReasons.length || claims > 1 ? 'conflict' : unavailable ? 'unavailable' : resolved.reasons.length ? 'conflict' : 'cabled';
    const trace = state === 'cabled' ? join.trace(resolved.device, termination.far, new Set([`${device.metadata.name}/${base.componentID}/${base.position}`])) : [];
    const checks = oppositeFaceCheck(base.rackFace, resolved.peer.rackFace);
    return { ...base, key: `${base.componentID}/${base.position}/${termination.cable.metadata.name}/${termination.laneID}/${index}`, state, cable: cableRef(termination.cable, termination.laneID), peer: resolved.peer, trace, reasons, checks };
  };

  for (const component of components) {
    const positions = positionsOf(component);
    for (let position = 1; position <= positions; position++) {
      const claims = join.terminationsAt('Device', device.metadata.name, component.id, position);
      const base = { group: groupOf[component.type], componentID: component.id, component, position, positions, internal: internalFor(component.id, position), ...facesOf(component.id) };
      if (!claims.length) {
        if (!UNCABLED_TYPES.has(component.type)) links.push({ ...base, key: `${component.id}/${position}`, state: 'none', trace: [], reasons: [], checks: [] });
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
    links.push(linkFor({ group: component ? groupOf[component.type] : 'unrecorded', componentID: termination.mine.componentID, component, template, position: termination.mine.position, positions: positionsOf(component), ...facesOf(termination.mine.componentID) }, termination, 1, [reason], index));
  });

  const orphanIDs = new Set(orphans.map(termination => termination.mine.componentID));
  for (const template of templates) {
    if (byID.has(template.id) || orphanIDs.has(template.id)) continue;
    links.push({ key: `catalog/${template.id}`, group: groupOf[template.type], state: 'catalog', componentID: template.id, template, position: 1, positions: positionsOf(template), ...facesOf(template.id), trace: [], reasons: [], checks: [] });
  }

  return { cableAccess: 'readable', catalog: catalogState, links };
}

const laneEndKey = (cable: string, laneID: string, componentID: string, position: number) => `${cable}/${laneID}/${componentID}/${position}`;

/**
 * Cable lane ends that name this Device but that `links` does not show: a
 * recorded component at a position below 1, a fractional one, or none (an
 * omitted position reads as 0). Each comes back as a disagreeing link with
 * its peer resolved by the same access rules, so a table can list every
 * Cable that names the Device. Empty when Cables are denied.
 */
export function unmatchedLinks(device: Device, inventory: Inventory, links: ConnectionLink[]): ConnectionLink[] {
  if (inventory.issues?.Cable) return [];
  const join = new Join(inventory, device.metadata.namespace);
  const shown = new Set(links.flatMap(link => link.cable ? [laneEndKey(link.cable.name, link.cable.laneID, link.componentID, link.position)] : []));
  const byID = new Map((device.spec.components ?? []).map(component => [component.id, component]));
  return join.terminationsOn(device)
    .filter(termination => !shown.has(laneEndKey(termination.cable.metadata.name, termination.laneID, termination.mine.componentID, termination.mine.position)))
    .map((termination, index) => {
      const component = byID.get(termination.mine.componentID);
      const resolved = join.peer(termination.far);
      const position = termination.mine.position;
      const reason = `The Cable names ${typeof position === 'number' && Number.isFinite(position) ? `position ${position}` : 'no position'}${component ? `; this Device records ${positionsOf(component) === 1 ? 'one position' : `positions 1 to ${positionsOf(component)}`} for "${component.id}"` : ''}.`;
      return {
        key: `unmatched/${termination.cable.metadata.name}/${termination.laneID}/${index}`, group: component ? groupOf[component.type] : 'unrecorded', state: 'conflict',
        componentID: termination.mine.componentID, component, position, positions: positionsOf(component),
        cable: cableRef(termination.cable, termination.laneID), peer: resolved.peer, trace: [], reasons: [reason, ...resolved.reasons], checks: [],
      };
    });
}

/**
 * A PowerFeed's terminations joined to the Cable lanes that name them, with
 * the same record states as a Device's components. Traces are not followed:
 * a feed fans out through PDUs, which the power path draws as their own
 * strips. Cables that name a termination the feed does not record disagree.
 */
export function feedConnections(feed: PhysicalRecord, inventory: Inventory): DeviceConnections {
  if (inventory.issues?.Cable) return { cableAccess: 'denied', catalog: 'unassigned', links: [] };
  const join = new Join(inventory, feed.metadata.namespace);
  const terminations = feed.spec.terminations ?? [];
  const byID = new Map(terminations.map(component => [component.id, component]));
  const links: ConnectionLink[] = [];
  const add = (base: Omit<ConnectionLink, 'key' | 'state' | 'cable' | 'peer' | 'trace' | 'reasons' | 'checks'>, claim: Termination, claims: number, local: string[], index: number) => {
    const resolved = join.peer(claim.far);
    const reasons = [...local, ...(claims > 1 ? [`${claims} Cable lanes name this position.`] : []), ...resolved.reasons];
    const state: LinkState = local.length || claims > 1 ? 'conflict' : resolved.peer.access !== 'readable' ? 'unavailable' : resolved.reasons.length ? 'conflict' : 'cabled';
    links.push({ ...base, key: `${base.componentID}/${base.position}/${claim.cable.metadata.name}/${claim.laneID}/${index}`, state, cable: cableRef(claim.cable, claim.laneID), peer: resolved.peer, trace: [], reasons, checks: [] });
  };
  for (const component of terminations) {
    const positions = positionsOf(component);
    for (let position = 1; position <= positions; position++) {
      const base = { group: groupOf[component.type] ?? 'power', componentID: component.id, component, position, positions };
      const claims = join.terminationsAt('PowerFeed', feed.metadata.name, component.id, position);
      if (!claims.length) links.push({ ...base, key: `${component.id}/${position}`, state: 'none', trace: [], reasons: [], checks: [] });
      claims.forEach((claim, index) => add(base, claim, claims.length, [], index));
    }
  }
  join.terminationsFor('PowerFeed', feed.metadata.name)
    .filter(claim => !byID.has(claim.mine.componentID) || claim.mine.position > positionsOf(byID.get(claim.mine.componentID)))
    .forEach((claim, index) => {
      const component = byID.get(claim.mine.componentID);
      const reason = component ? `The Cable names position ${claim.mine.position}; this feed records ${positionsOf(component)} for "${component.id}".` : `The Cable names termination "${claim.mine.componentID}", which this feed does not record.`;
      add({ group: component ? groupOf[component.type] : 'unrecorded', componentID: claim.mine.componentID, component, position: claim.mine.position, positions: positionsOf(component) }, claim, 1, [reason], index);
    });
  return { cableAccess: 'readable', catalog: 'unassigned', links };
}

/**
 * A Cable whose ends land on opposite rack faces is possible (a pass-through
 * or a reversed peer) but worth confirming. It is a note, never an error.
 */
export function oppositeFaceCheck(local: RackFace | undefined, far: RackFace | undefined): string[] {
  if (!local || !far || local === far) return [];
  return [`Check: this end is on the rack ${local} and the far end on the rack ${far}. Confirm the records; opposite faces can be correct.`];
}

export type FaceGroup = ChassisFace | 'unknown';

export const faceGroupTitles: Record<FaceGroup, string> = { Front: 'Front', Rear: 'Rear', unknown: 'Face unknown' };

/**
 * The short face label for the Device page's picture and table: "Front", or
 * "Front · rack rear" only when the Device is mounted reversed, because the
 * matching rack face goes without saying. Unknown faces say so.
 */
export function shortFaceLabel(face: FaceGroup | undefined, rack?: RackFace) {
  if (!face || face === 'unknown') return faceGroupTitles.unknown;
  return rack && rack !== face.toLowerCase() ? `${face} · rack ${rack}` : face;
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

/** The peer record's name in words. Never a generated resource name; nothing at all for a denied peer. */
export function peerName(peer: PeerEnd) {
  if (peer.access === 'denied') return peer.kind === 'PowerFeed' ? 'Power feed unavailable' : 'Device unavailable';
  if (peer.label) return peer.label;
  // A missing record has only the name the Cable gives it.
  if (peer.access === 'missing' && peer.name && !isGeneratedName(peer.name)) return peer.name;
  return peer.kind === 'PowerFeed' ? 'Unnamed power feed' : 'Unnamed device';
}

/** A Cable's name in words. Never a generated resource name. */
export function cableName(cable: CableRef) {
  return cable.label ?? (cable.medium ? `${cable.medium} cable` : 'Unnamed cable');
}

/**
 * The far end in words. A denied peer yields no identifying detail. `name`
 * replaces the record's name, for a disambiguated one from viewNames().
 */
export function peerLabel(peer: PeerEnd | undefined, name?: string) {
  if (!peer) return 'Unknown';
  if (peer.access === 'denied') return peerName(peer);
  const port = peer.componentName ?? peer.componentID;
  const position = peer.multiPosition ? ` · position ${peer.position}` : '';
  if (peer.access === 'missing') return `${name ?? peerName(peer)} · ${peer.componentID}${position} (record not found)`;
  return `${name ?? peerName(peer)} · ${port}${position}`;
}

/** A short, stable suffix from a resource name, to tell two same-named records apart. */
export function shortSuffix(name: string) {
  return name.replace(/[^a-z0-9]/gi, '').slice(-4).toLowerCase();
}

export interface ViewNames {
  cable: (cable: CableRef) => string;
  peer: (peer: PeerEnd) => string;
}

/**
 * Names for every Cable and peer in one view. Two different records that
 * would read the same get a short suffix from their resource names (`Copper
 * cable #295e`). A denied peer has no name, so it is never disambiguated.
 */
export function viewNames(links: ConnectionLink[]): ViewNames {
  const cables = new Map<string, Set<string>>();
  const peers = new Map<string, Set<string>>();
  const add = (map: Map<string, Set<string>>, label: string, name: string) => map.set(label, (map.get(label) ?? new Set()).add(name));
  const visit = (cable?: CableRef, peer?: PeerEnd) => {
    if (cable) add(cables, cableName(cable), cable.name);
    if (peer && peer.access !== 'denied' && peer.name) add(peers, `${peer.kind}/${peerName(peer)}`, peer.name);
  };
  for (const link of links) {
    visit(link.cable, link.peer);
    for (const hop of link.trace) visit(hop.cable, hop.peer);
  }
  return {
    cable: cable => {
      const label = cableName(cable);
      return (cables.get(label)?.size ?? 0) > 1 ? `${label} #${shortSuffix(cable.name)}` : label;
    },
    peer: peer => {
      const label = peerName(peer);
      if (peer.access === 'denied' || !peer.name) return label;
      return (peers.get(`${peer.kind}/${label}`)?.size ?? 0) > 1 ? `${label} #${shortSuffix(peer.name)}` : label;
    },
  };
}

// ---- Port tiles ----

/** One position of a port tile, with every link that names it. */
export interface TileLane {
  position: number;
  state: LinkState;
  links: ConnectionLink[];
}

/** One component drawn as a port tile. A multi-position component has one lane per position. */
export interface PortTile {
  key: string;
  group: ConnectionGroup;
  componentID: string;
  /** The full local name, such as "PSU 1" or "swp49". */
  name: string;
  type?: PhysicalComponent['type'];
  /** The recorded free-text connector, such as "SFP28" or "IEC C14". */
  connector?: string;
  shape: ConnectorShape;
  /** Inherited from the template; unset is unknown. */
  chassisFace?: ChassisFace;
  /** Derived from the chassis face and the mounted rack face; unset is unknown. */
  rackFace?: RackFace;
  positions: number;
  state: LinkState;
  lanes: TileLane[];
  links: ConnectionLink[];
}

export type ConnectorShape = 'rj45' | 'sfp' | 'qsfp' | 'lc' | 'iec' | 'generic';

const SHAPE_OF: Record<ConnectorKind, ConnectorShape> = {
  'QSFP-DD': 'qsfp', OSFP: 'qsfp', QSFP56: 'qsfp', QSFP28: 'qsfp', SFP28: 'sfp', SFP: 'sfp', RJ45: 'rj45', LC: 'lc',
  C13: 'iec', C14: 'iec', C19: 'iec', C20: 'iec', C22: 'iec', USB: 'generic', DB9: 'generic', VGA: 'generic', Other: 'generic',
};

/**
 * A tile outline for the recorded connector, from the one connector-kind
 * table (connector-kind.ts). Text the table does not map is a generic tile,
 * so the drawing never implies more than the free text says.
 */
export function connectorShape(connector: string | undefined): ConnectorShape {
  return SHAPE_OF[connectorKind(connector)];
}

/** A face of the drawn chassis, and the type clusters on it in layout order. */
export interface LayoutFace {
  face: FaceGroup;
  /** "Front · rack rear": the chassis face, and the rack face it is mounted toward when known. */
  title: string;
  clusters: { group: ConnectionGroup; title: string; tiles: PortTile[] }[];
}

/**
 * Lays tiles out as a faceplate: by chassis face (Front, Rear, Face unknown),
 * then clustered by type, keeping the order the components are recorded in
 * (which follows the DeviceType's template order). This is the one place that
 * decides placement, so recorded layout data (rows, columns, banks) can
 * replace template order here without touching the view. `faceOf` supplies a
 * tile's chassis face; by default it is the face inherited from the template.
 */
export function portLayout(links: ConnectionLink[], faceOf: (tile: PortTile) => ChassisFace | undefined = tile => tile.chassisFace): LayoutFace[] {
  const tiles = portTiles(links);
  const faces: LayoutFace[] = [];
  for (const face of ['Front', 'Rear', 'unknown'] as const) {
    const onFace = tiles.filter(tile => (faceOf(tile) ?? 'unknown') === face);
    if (!onFace.length) continue;
    // Clusters follow the first appearance of their type, so a switch leads
    // with its ports and a server rear reads PSUs, NICs, BMC.
    const clusters = connectionGroups
      .map(({ group, title }) => ({ group, title, tiles: onFace.filter(tile => tile.group === group) }))
      .filter(cluster => cluster.tiles.length)
      .sort((a, b) => onFace.indexOf(a.tiles[0]) - onFace.indexOf(b.tiles[0]));
    // Every port on one chassis face shares the Device's mounted face, so the
    // derived rack face is stated once, on the face.
    const rack = face === 'unknown' ? undefined : onFace.find(tile => tile.rackFace)?.rackFace;
    const title = face === 'unknown' ? faceGroupTitles.unknown : `${faceGroupTitles[face]} · ${rack ? `rack ${rack}` : 'rack face unknown'}`;
    faces.push({ face, title, clusters });
  }
  return faces;
}

// A tile shows the most urgent state among its lanes: a disagreement or an
// unreadable peer outranks a recorded lane, and a recorded lane outranks an
// unknown one. There is no verified state to rise to.
const TILE_ORDER: LinkState[] = ['conflict', 'unavailable', 'cabled', 'none', 'catalog'];

/** One tile per component, in the order the links list them. */
export function portTiles(links: ConnectionLink[]): PortTile[] {
  const tiles = new Map<string, PortTile>();
  for (const link of links) {
    const key = `${link.group}/${link.componentID}`;
    let tile = tiles.get(key);
    if (!tile) {
      const source = link.component ?? link.template;
      tile = {
        key, group: link.group, componentID: link.componentID, name: source?.name ?? link.componentID, type: source?.type, connector: source?.connector,
        shape: connectorShape(source?.connector), chassisFace: link.chassisFace, rackFace: link.rackFace, positions: link.positions, state: link.state, lanes: [], links: [],
      };
      tiles.set(key, tile);
    }
    tile.links.push(link);
    const lane = tile.lanes.find(item => item.position === link.position);
    if (lane) lane.links.push(link);
    else tile.lanes.push({ position: link.position, state: link.state, links: [link] });
  }
  for (const tile of tiles.values()) {
    tile.lanes.sort((a, b) => a.position - b.position);
    for (const lane of tile.lanes) lane.state = lane.links.length > 1 ? 'conflict' : lane.links[0].state;
    tile.state = TILE_ORDER.find(state => tile.lanes.some(lane => lane.state === state)) ?? 'none';
  }
  return [...tiles.values()];
}

export function tileCounts(tiles: PortTile[]) {
  const counts: Record<LinkState, number> = { cabled: 0, none: 0, conflict: 0, unavailable: 0, catalog: 0 };
  for (const tile of tiles) counts[tile.state]++;
  return counts;
}

const MAX_SHORT = 7;

function labelOptions(tile: PortTile) {
  const words = tile.name.trim().split(/[\s_-]+/).filter(Boolean);
  const number = tile.name.match(/(\d+[a-z]?)$/i)?.[1];
  // The shared abbreviations (eth0, O3, F1, mgmt) come first, when they give one short word.
  const condensed = condensedName(tile.name);
  const options = [
    condensed !== tile.name && condensed.length <= MAX_SHORT && !/\s/.test(condensed) ? condensed : undefined,
    tile.name.length <= MAX_SHORT ? tile.name : undefined,
    tile.componentID.length <= MAX_SHORT ? tile.componentID : undefined,
    number,
    number && words.length > 1 ? `${words.slice(0, -1).map(word => word[0].toUpperCase()).join('')}${number}` : undefined,
    words[0] && words[0].length <= MAX_SHORT ? words[0] : undefined,
    tile.name.length > MAX_SHORT ? `${tile.name.slice(0, MAX_SHORT - 1)}…` : tile.name,
  ];
  return [...new Set(options.filter((item): item is string => Boolean(item)))];
}

/**
 * Short faceplate labels, unique within the tiles given: condensedName()
 * when it gives one short word (`eth0`, `O24`, `F1`), else the name or id when
 * it fits (`PSU 1`, `swp49`), else its trailing number, else initials and
 * number (`FP1` and `RP1` for "Front port 1" and "Rear port 1"), else a
 * trimmed name. A label that would repeat moves on to the
 * next option. The full name is always in the accessible name and details.
 */
export function shortLabels(tiles: PortTile[]): Map<string, string> {
  const options = new Map(tiles.map(tile => [tile.key, labelOptions(tile)]));
  const level = new Map(tiles.map(tile => [tile.key, 0]));
  const chosen = new Map<string, string>();
  let pending = tiles.map(tile => tile.key);
  while (pending.length) {
    const proposal = new Map(pending.map(key => [key, options.get(key)![level.get(key)!]]));
    const taken = new Set(chosen.values());
    const counts = new Map<string, number>();
    for (const label of proposal.values()) counts.set(label, (counts.get(label) ?? 0) + 1);
    const next: string[] = [];
    const exhausted: string[] = [];
    for (const key of pending) {
      const label = proposal.get(key)!;
      if (!taken.has(label) && counts.get(label) === 1) chosen.set(key, label);
      else if (level.get(key)! < options.get(key)!.length - 1) { level.set(key, level.get(key)! + 1); next.push(key); }
      else exhausted.push(key);
    }
    // Same names with no other option left: number them in order.
    exhausted.forEach((key, index) => chosen.set(key, `${proposal.get(key)}·${index + 1}`));
    pending = next;
  }
  return chosen;
}

/**
 * Recorded network interfaces matched to components by name or id. The
 * component table shows a matched MAC beside its port; `unplaced` lists the
 * interfaces no component names.
 */
export function interfaceAddresses(device: Device): { byComponent: Map<string, string>; unplaced: { name: string; macAddress?: string }[] } {
  const byComponent = new Map<string, string>();
  const unplaced: { name: string; macAddress?: string }[] = [];
  const components = device.spec.components ?? [];
  for (const iface of device.spec.interfaces ?? []) {
    const component = components.find(item => item.name === iface.name || item.id === iface.name);
    if (component && iface.macAddress) byComponent.set(component.id, iface.macAddress);
    else if (!component) unplaced.push(iface);
  }
  return { byComponent, unplaced };
}
