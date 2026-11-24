// Recorded power paths: which feed each PSU reaches through Cable records and
// PDU internal mappings, and the power path of one PowerFeed.
//
// Everything here comes from records, never from telemetry. A path that is
// not fully recorded is unknown. Two PSUs on different feeds in the records
// are "separate feeds (recorded)", never redundant: records cannot show that
// the feeds are independent upstream, and telemetry never verifies it.

import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { deviceConnections, feedConnections, peerName, portTiles, type ConnectionLink, type PortTile } from './device-connections';
import { humanName } from './display-names';

export interface RecordName {
  /** The resource name, for links only. */
  name: string;
  /** A human name; never a generated resource name. */
  label: string;
}

/** One PSU (a PowerInlet) and the feed its recorded path reaches. */
export interface PsuFeed {
  componentID: string;
  /** The component name, such as "PSU 1". */
  name: string;
  /** The feed reached; undefined means unknown. */
  feed?: RecordName;
  /** The PDU and outlet on the way, when the PSU is cabled to a PDU. */
  via?: RecordName & { outletID: string; outlet: string };
  /** Why the feed is unknown, in a few words. Names nothing the reader cannot read. */
  unknown?: string;
}

const deviceLabel = (device: Device) => humanName(device) ?? 'Unnamed device';

function inletFeed(componentID: string, name: string, links: ConnectionLink[]): PsuFeed {
  const base = { componentID, name };
  if (!links.length) return { ...base, unknown: 'No cable recorded' };
  if (links.length > 1 || links.some(link => link.state === 'conflict')) return { ...base, unknown: 'Records disagree' };
  const [link] = links;
  if (link.state === 'none') return { ...base, unknown: 'No cable recorded' };
  const peer = link.peer;
  if (!peer || link.state === 'unavailable' || peer.access !== 'readable') return { ...base, unknown: peer?.access === 'missing' ? 'Peer record not found' : 'Peer unavailable' };
  if (peer.kind === 'PowerFeed') return { ...base, feed: { name: peer.name!, label: peerName(peer) } };
  // Cabled to a Device: follow its internal mapping (a PDU outlet to its inlet) to a feed.
  const via = { name: peer.name!, label: peerName(peer), outletID: peer.componentID ?? '', outlet: peer.componentName ?? peer.componentID ?? '' };
  const hop = link.trace.find(item => item.peer?.kind === 'PowerFeed');
  if (hop?.state === 'cabled' && hop.peer?.access === 'readable') return { ...base, via, feed: { name: hop.peer.name!, label: peerName(hop.peer) } };
  const last = link.trace.at(-1);
  if (!last) return { ...base, via, unknown: `No internal path recorded in ${via.label}` };
  if (last.state === 'none') return { ...base, via, unknown: `No cable recorded beyond ${via.label}` };
  if (last.state === 'conflict') return { ...base, via, unknown: `Records disagree beyond ${via.label}` };
  if (last.state === 'unavailable') return { ...base, via, unknown: `Path beyond ${via.label} unavailable` };
  return { ...base, via, unknown: `Path beyond ${via.label} not recorded` };
}

/**
 * The feed each of a Device's PSUs reaches in the records. Undefined when
 * Cables are unreadable, so callers say "unknown" instead of listing PSUs as
 * unconnected.
 */
export function psuFeeds(device: Device, inventory: Inventory): PsuFeed[] | undefined {
  const inlets = (device.spec.components ?? []).filter(component => component.type === 'PowerInlet');
  const connections = deviceConnections(device, inventory);
  if (connections.cableAccess === 'denied') return undefined;
  return inlets.map(inlet => inletFeed(inlet.id, inlet.name, connections.links.filter(link => link.componentID === inlet.id && link.component?.type === 'PowerInlet')));
}

export type FeedClass = 'separate' | 'single' | 'partial' | 'unknown';

export const FEED_CLASS_WORDS: Record<FeedClass, string> = {
  separate: 'Separate feeds (recorded)',
  single: 'Single feed (recorded)',
  partial: 'Feed unknown for some PSUs',
  unknown: 'Feed unknown',
};

/**
 * How a Device's PSUs spread across feeds in the records. Two or more feeds
 * is "separate (recorded)"; one feed for every PSU is "single (recorded)";
 * one known feed with other PSUs unknown is "partial"; nothing known is
 * "unknown". None of these is a claim about real redundancy.
 */
export function classifyFeeds(psus: PsuFeed[] | undefined): { class: FeedClass; feeds: string[]; unknown: number } | undefined {
  if (!psus) return { class: 'unknown', feeds: [], unknown: 0 };
  if (!psus.length) return undefined;
  const feeds = [...new Set(psus.flatMap(psu => psu.feed ? [psu.feed.name] : []))];
  const unknown = psus.filter(psu => !psu.feed).length;
  const kind: FeedClass = feeds.length >= 2 ? 'separate' : feeds.length === 1 ? unknown ? 'partial' : 'single' : 'unknown';
  return { class: kind, feeds, unknown };
}

// ---- One feed's power path ----

export interface PathPdu {
  device: Device;
  label: string;
  /** Inlets first, then outlets, as port tiles. */
  tiles: PortTile[];
  links: ConnectionLink[];
}

export interface PathDevice {
  device: Device;
  label: string;
  /** Every PSU of the Device, with the feed each reaches. */
  psus: PsuFeed[];
  /** PSUs whose recorded path reaches this feed. */
  onFeed: PsuFeed[];
  class: FeedClass;
}

export interface FeedPath {
  cableAccess: 'readable' | 'denied';
  /** Device records are unreadable: PDUs and devices are unknown. */
  devicesDenied: boolean;
  /** The feed's terminations as tiles. */
  terminations: PortTile[];
  links: ConnectionLink[];
  pdus: PathPdu[];
  devices: PathDevice[];
}

function namespaceDevices(inventory: Inventory, namespace: string | undefined) {
  const seen = new Map<string, Device>();
  for (const device of [...inventory.slots.flatMap(slot => slot.devices), ...inventory.unboundDevices]) if (device.metadata.namespace === namespace) seen.set(device.metadata.name, device);
  return seen;
}

const isPdu = (device: Device) => (device.spec.components ?? []).some(component => component.type === 'PowerOutlet');

/**
 * A feed's recorded power path: its terminations, the PDUs cabled to them,
 * and every Device whose PSUs are cabled to either. Each Device appears once.
 */
export function feedPath(feed: PhysicalRecord, inventory: Inventory): FeedPath {
  const joined = feedConnections(feed, inventory);
  const devicesDenied = !!inventory.issues?.Device;
  // With Cables unreadable the terminations are still drawn, each unknown, never "no cable".
  const placeholders: ConnectionLink[] = (feed.spec.terminations ?? []).map(component => ({
    key: component.id, group: 'power', state: 'unavailable', componentID: component.id, component, position: 1, positions: component.positions ?? 1, trace: [], reasons: [], checks: [],
  }));
  const links = joined.cableAccess === 'denied' ? placeholders : joined.links;
  const base = { cableAccess: joined.cableAccess, devicesDenied, terminations: portTiles(links), links: joined.links };
  if (joined.cableAccess === 'denied' || devicesDenied) return { ...base, pdus: [], devices: [] };
  const devices = namespaceDevices(inventory, feed.metadata.namespace);
  const pdus = new Map<string, PathPdu>();
  const reached = new Map<string, Device>();
  for (const link of joined.links) {
    if (link.state !== 'cabled' || link.peer?.kind !== 'Device' || !link.peer.name) continue;
    const device = devices.get(link.peer.name);
    if (!device) continue;
    if (!isPdu(device)) { reached.set(device.metadata.name, device); continue; }
    if (pdus.has(device.metadata.name)) continue;
    const connections = deviceConnections(device, inventory);
    const power = connections.links.filter(item => item.component?.type === 'PowerInlet' || item.component?.type === 'PowerOutlet');
    const tiles = portTiles(power).sort((a, b) => Number(a.type === 'PowerOutlet') - Number(b.type === 'PowerOutlet'));
    pdus.set(device.metadata.name, { device, label: deviceLabel(device), tiles, links: power });
    for (const outlet of power) {
      if (outlet.component?.type !== 'PowerOutlet' || outlet.state !== 'cabled' || outlet.peer?.kind !== 'Device' || !outlet.peer.name) continue;
      const target = devices.get(outlet.peer.name);
      if (target) reached.set(target.metadata.name, target);
    }
  }
  const rows: PathDevice[] = [...reached.values()].map(device => {
    const psus = psuFeeds(device, inventory) ?? [];
    return { device, label: deviceLabel(device), psus, onFeed: psus.filter(psu => psu.feed?.name === feed.metadata.name), class: classifyFeeds(psus)?.class ?? 'unknown' };
  });
  return { ...base, pdus: [...pdus.values()], devices: rows };
}
