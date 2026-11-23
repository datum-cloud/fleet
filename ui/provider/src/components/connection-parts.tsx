// Small pieces shared by the component table and the port faceplate: state
// words and glyphs, condensed Cable and peer names, traces.

import { Link } from 'react-router';
import {
  linkStateGlyph, linkStateWords, peerLabel, tileCounts,
  type ConnectionLink, type LinkState, type PeerEnd, type PortTile, type TraceHop, type ViewNames,
} from '../lib/device-connections';

export type Href = (kind: string, namespace?: string, name?: string) => string;

export const STATES: LinkState[] = ['cabled', 'none', 'conflict', 'unavailable', 'catalog'];

// Short words for counts. "No cable recorded" counts as unknown, never free.
export const countWords: Record<LinkState, string> = {
  cabled: 'recorded',
  none: 'unknown',
  conflict: 'disagree',
  unavailable: 'peer unavailable',
  catalog: 'catalog only',
};

// Tiles carry a glyph only where fill and outline alone could be confused.
export const TILE_GLYPH: Partial<Record<LinkState, string>> = { conflict: '!', unavailable: '?' };

export function Glyph({ state }: { state: LinkState }) {
  return <span className="inventory-connection-glyph" aria-hidden="true">{linkStateGlyph[state]}</span>;
}

export function StateText({ state }: { state: LinkState }) {
  return <span className="inventory-connection-state" data-state={state}><Glyph state={state} /> {linkStateWords[state]}</span>;
}

export function tileSummary(tiles: PortTile[]) {
  const counts = tileCounts(tiles);
  return STATES.filter(state => counts[state]).map(state => `${counts[state]} ${countWords[state]}`).join(' · ');
}

/** The device page's count line: like tileSummary, with the disagreement count marked. */
export function TileSummary({ tiles }: { tiles: PortTile[] }) {
  const counts = tileCounts(tiles);
  return <>{STATES.filter(state => counts[state]).map((state, index) => <span key={state} data-state={state}>{index > 0 && ' · '}{counts[state]} {countWords[state]}</span>)}</>;
}

export function portsWord(count: number) {
  return `${count} ${count === 1 ? 'port' : 'ports'}`;
}

export function cableText(link: ConnectionLink) {
  if (!link.cable) return link.state === 'catalog' ? 'Not recorded on this Device' : 'No cable recorded';
  const lane = link.cable.laneCount > 1 ? ` · lane ${link.cable.laneID} of ${link.cable.laneCount}` : '';
  return `${link.cable.medium ?? 'Medium not recorded'}${lane}`;
}

export const peerText = (peer: PeerEnd | undefined, names: ViewNames) => peer ? peerLabel(peer, peer.access === 'denied' ? undefined : names.peer(peer)) : 'Unknown';

export function PeerTarget({ peer, namespace, href, names }: { peer?: PeerEnd; namespace?: string; href: Href; names: ViewNames }) {
  if (!peer) return <span>Unknown</span>;
  if (peer.access !== 'readable') return <span>{peerText(peer, names)}</span>;
  return <Link to={href(peer.kind, namespace, peer.name)}>{peerText(peer, names)}</Link>;
}

export function Trace({ hops, namespace, href, names }: { hops: TraceHop[]; namespace?: string; href: Href; names: ViewNames }) {
  if (!hops.length) return null;
  return (
    <ol className="inventory-connection-trace" aria-label="Recorded path beyond the peer">
      {hops.map((hop, index) => (
        <li key={index}>
          <Glyph state={hop.state} /> via internal path to {hop.via.componentName ?? hop.via.componentID}{hop.via.position > 1 ? ` · position ${hop.via.position}` : ''}
          {hop.cable ? <>, then <Link to={href('Cable', namespace, hop.cable.name)}>{names.cable(hop.cable)}</Link> to <PeerTarget peer={hop.peer} namespace={namespace} href={href} names={names} /></> : `: ${linkStateWords[hop.state].toLowerCase()}`}
          {hop.reasons.length > 0 && <small>{hop.reasons.join(' ')}</small>}
        </li>
      ))}
    </ol>
  );
}

/** Only what adds to the peer's name: never the bare kind, which the group already says. */
export function peerDetail(link: ConnectionLink) {
  if (link.state === 'none') return 'No cable recorded';
  if (link.state === 'catalog') return 'Not recorded';
  if (!link.peer) return undefined;
  if (link.peer.access === 'denied') return 'You cannot read this peer';
  if (link.peer.access === 'missing') return `${link.peer.kind} record not found`;
  return link.peer.supply ? `Recorded rating ${link.peer.supply}` : undefined;
}
