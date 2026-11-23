// Joins a Device's typed components to the Cable lanes that name them.
//
// Everything here is derived from records, not from the wire. Status is
// empty on every inventory kind, so a link can be "cable recorded" at best;
// nothing is ever verified. "No cable recorded" means unknown, never free.
// A denied collection read is never treated as an absent record.

import type { CableEndpoint, ChassisFace, ComponentPosition, ComponentTemplate, Device, PhysicalComponent, PhysicalRecord, RackFace } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { deviceCatalog } from './hardware-catalog';
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

// The port tiles are the default view at every size. The line diagram is
// offered only up to this many links: above it the diagram is many times
// taller than the tiles that already show the same records.
export const DIAGRAM_LINK_LIMIT = 16;

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

// Names that importers generate (`netbox-ca213d185ea9153b853f295e`), and UIDs.
// They identify a record but mean nothing to a reader, so views show them
// only in the detail panel.
const GENERATED_NAME = /[0-9a-f]{12,}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}/i;
export const isGeneratedName = (name: string) => GENERATED_NAME.test(name);

/** The display name, or the resource name when it reads as a human name (`pdu-a.rk105.yul3`). */
export function humanName(record: { metadata: { name: string }; spec: { displayName?: string } }): string | undefined {
  for (const candidate of [record.spec.displayName?.trim(), record.metadata.name]) if (candidate && !isGeneratedName(candidate)) return candidate;
  return undefined;
}

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
      rackFace: kind === 'Device' && component ? componentRackFace(deviceMountedFace(record as Device, this.inventory), componentChassisFace(record as Device, this.inventory, far.componentID)) : undefined,
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
    if (claims > 1) reasons.push(`${claims} Cable lanes name this position. Neither is treated as current.`);
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

/** Splits links by chassis face, in the order Front, Rear, Face unknown. */
export function faceGroups<T extends Pick<ConnectionLink, 'chassisFace'>>(links: T[]): { face: FaceGroup; title: string; links: T[] }[] {
  return (['Front', 'Rear', 'unknown'] as const)
    .map(face => ({ face, title: faceGroupTitles[face], links: links.filter(link => (link.chassisFace ?? 'unknown') === face) }))
    .filter(group => group.links.length);
}

/** The chassis and rack face of a link's local end, in words. */
export function faceLabel(link: Pick<ConnectionLink, 'chassisFace' | 'rackFace'>) {
  if (!link.chassisFace) return 'Chassis face unknown';
  const chassis = `Chassis ${link.chassisFace.toLowerCase()}`;
  return link.rackFace ? `${chassis} · rack ${link.rackFace}` : `${chassis} · rack face unknown`;
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

export function remotePortLabel(peer: PeerEnd | undefined) {
  if (!peer || peer.access === 'denied') return 'Port unavailable';
  return `${peer.componentName ?? peer.componentID}${peer.multiPosition || (peer.position ?? 1) > 1 ? ` · position ${peer.position}` : ''}`;
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

export type ConnectorShape = 'rj45' | 'sfp' | 'qsfp' | 'iec' | 'generic';

/**
 * A tile outline for the recorded connector. Only connectors that clearly
 * name a family get a shape; anything else is a generic tile, so the drawing
 * never implies more than the free text says.
 */
export function connectorShape(connector: string | undefined): ConnectorShape {
  const text = connector?.toUpperCase() ?? '';
  const compact = text.replace(/[\s-]+/g, '');
  if (/QSFP/.test(compact)) return 'qsfp';
  if (/SFP/.test(compact)) return 'sfp';
  if (/RJ45|8P8C/.test(compact)) return 'rj45';
  if (/(^|[^A-Z0-9])C(13|14|19|20)([^0-9]|$)/.test(text)) return 'iec';
  return 'generic';
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
  const options = [
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
 * Short faceplate labels, unique within the tiles given: the name or id when
 * it fits (`PSU 1`, `swp49`, `eth0`), else its trailing number (`24` for
 * "Outlet 24"), else initials and number (`F1` and `R1` for "Front 1" and
 * "Rear 1"), else a trimmed name. A label that would repeat moves on to the
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
