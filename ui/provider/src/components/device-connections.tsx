import { Button } from '@datum-cloud/datum-ui/button';
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from 'react';
import { Link } from 'react-router';
import type { Device } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import {
  connectionGroups, deviceConnections, deviceTitle, DIAGRAM_LINK_LIMIT, faceGroups, faceLabel, linkStateGlyph, linkStateWords, localLabel, peerLabel, portLayout, portTiles,
  remotePortLabel, shortLabels, stateCounts, tileCounts, viewNames,
  type ConnectionGroup, type ConnectionLink, type LayoutFace, type LinkState, type PeerEnd, type PortTile, type TraceHop, type ViewNames,
} from '../lib/device-connections';

type Href = (kind: string, namespace?: string, name?: string) => string;
type View = 'ports' | 'diagram' | 'list';
type Group = { group: ConnectionGroup; title: string; links: ConnectionLink[]; tiles: PortTile[] };

const STATES: LinkState[] = ['cabled', 'none', 'conflict', 'unavailable', 'catalog'];
const LEFT: ConnectionGroup[] = ['power'];

// Short words for counts. "No cable recorded" counts as unknown, never free.
const countWords: Record<LinkState, string> = {
  cabled: 'recorded',
  none: 'unknown',
  conflict: 'disagree',
  unavailable: 'peer unavailable',
  catalog: 'catalog only',
};

// Tiles carry a glyph only where fill and outline alone could be confused.
const TILE_GLYPH: Partial<Record<LinkState, string>> = { conflict: '!', unavailable: '?' };

function Glyph({ state }: { state: LinkState }) {
  return <span className="inventory-connection-glyph" aria-hidden="true">{linkStateGlyph[state]}</span>;
}

function StateText({ state }: { state: LinkState }) {
  return <span className="inventory-connection-state" data-state={state}><Glyph state={state} /> {linkStateWords[state]}</span>;
}

function countsText(links: ConnectionLink[]) {
  const counts = stateCounts(links);
  return STATES.filter(state => counts[state]).map(state => `${counts[state]} ${linkStateWords[state].toLowerCase()}`).join(', ');
}

function tileSummary(tiles: PortTile[]) {
  const counts = tileCounts(tiles);
  return STATES.filter(state => counts[state]).map(state => `${counts[state]} ${countWords[state]}`).join(' · ');
}

function portsWord(count: number) {
  return `${count} ${count === 1 ? 'port' : 'ports'}`;
}

function linksWord(count: number) {
  return `${count} ${count === 1 ? 'link' : 'links'}`;
}

function cableText(link: ConnectionLink) {
  if (!link.cable) return link.state === 'catalog' ? 'Not recorded on this Device' : 'No cable recorded';
  const lane = link.cable.laneCount > 1 ? ` · lane ${link.cable.laneID} of ${link.cable.laneCount}` : '';
  return `${link.cable.medium ?? 'Medium not recorded'}${lane}`;
}

const peerText = (peer: PeerEnd | undefined, names: ViewNames) => peer ? peerLabel(peer, peer.access === 'denied' ? undefined : names.peer(peer)) : 'Unknown';

function PeerTarget({ peer, namespace, href, names, roving, label }: { peer?: PeerEnd; namespace?: string; href: Href; names: ViewNames; roving?: boolean; label?: string }) {
  if (!peer) return <span>Unknown</span>;
  if (peer.access !== 'readable') return <span>{peerText(peer, names)}</span>;
  return <Link to={href(peer.kind, namespace, peer.name)} data-connection-roving={roving ? '' : undefined} aria-label={label}>{peerText(peer, names)}</Link>;
}

function Trace({ hops, namespace, href, names, roving }: { hops: TraceHop[]; namespace?: string; href: Href; names: ViewNames; roving?: boolean }) {
  if (!hops.length) return null;
  return (
    <ol className="inventory-connection-trace" aria-label="Recorded path beyond the peer">
      {hops.map((hop, index) => (
        <li key={index}>
          <Glyph state={hop.state} /> via internal path to {hop.via.componentName ?? hop.via.componentID}{hop.via.position > 1 ? ` · position ${hop.via.position}` : ''}
          {hop.cable ? <>, then <Link to={href('Cable', namespace, hop.cable.name)} data-connection-roving={roving ? '' : undefined}>{names.cable(hop.cable)}</Link> to <PeerTarget peer={hop.peer} namespace={namespace} href={href} names={names} roving={roving} /></> : `: ${linkStateWords[hop.state].toLowerCase()}`}
          {hop.reasons.length > 0 && <small>{hop.reasons.join(' ')}</small>}
        </li>
      ))}
    </ol>
  );
}

function LocalDetail({ link }: { link: ConnectionLink }) {
  // The group already names the type; only its absence is worth saying.
  const type = link.component?.type ?? link.template?.type;
  const connector = link.component?.connector ?? link.template?.connector;
  return <small>{[type ? undefined : 'Not on this Device', connector ?? 'Connector not recorded', faceLabel(link)].filter(Boolean).join(' · ')}{link.internal ? ` · internal path to ${link.internal.componentName ?? link.internal.componentID}${link.internal.position > 1 ? ` position ${link.internal.position}` : ''}` : ''}</small>;
}

/** Only what adds to the peer's name: never the bare kind, which the group already says. */
function peerDetail(link: ConnectionLink) {
  if (link.state === 'none') return 'Unknown · not free';
  if (link.state === 'catalog') return 'Not recorded';
  if (!link.peer) return undefined;
  if (link.peer.access === 'denied') return 'You cannot read this peer';
  if (link.peer.access === 'missing') return `${link.peer.kind} record not found`;
  return link.peer.supply ? `Recorded rating ${link.peer.supply}` : undefined;
}

function linkLabel(link: ConnectionLink, names: ViewNames) {
  const cable = link.cable ? ` via cable ${names.cable(link.cable)}, ${cableText(link)}` : '';
  return `${localLabel(link)} to ${link.peer ? peerText(link.peer, names) : 'unknown peer'}${cable}: ${linkStateWords[link.state]}`;
}

// ---- Port tiles ----

function laneWords(link: ConnectionLink, names: ViewNames) {
  if (!link.cable) return link.state === 'none' ? 'unknown, not free' : '';
  return `to ${peerText(link.peer, names)}, via ${names.cable(link.cable)}`;
}

/** Everything the tile hides visually: port, state, and each peer and Cable. */
function tileLabel(tile: PortTile, names: ViewNames) {
  const state = `${tile.name}: ${linkStateWords[tile.state]}`;
  if (tile.lanes.length === 1 && tile.links.length === 1 && tile.positions <= 1) return [state, laneWords(tile.links[0], names)].filter(Boolean).join(', ');
  if (tile.lanes.length === 1 && tile.positions <= 1) return `${state}; ${tile.links.length} Cables: ${tile.links.map(link => laneWords(link, names)).join('; ')}`;
  return `${state}; ${tile.lanes.map(lane => `position ${lane.position} ${linkStateWords[lane.state].toLowerCase()}${lane.links.some(link => link.cable) ? ` ${lane.links.map(link => laneWords(link, names)).join(' and ')}` : ''}`).join('; ')}`;
}

const PREVIEW_LINKS = 4;

const catalogOnly = (tiles: PortTile[]) => tiles.every(tile => tile.state === 'catalog');

/** The Cable's medium, unless the group already says it ("Power" under Power). */
function mediumText(link: ConnectionLink) {
  const text = cableText(link);
  return link.group === 'power' && /^power$/i.test(link.cable?.medium ?? '') ? text.replace(/^Power( · )?/i, '') : text;
}

/**
 * The pop-up card for one port. As a hover or focus preview it is plain text
 * and hidden from assistive technology, because the tile's accessible name
 * already says the same. Pinned, it carries links to the Cable and peer and
 * the full resource names.
 */
function PortCard({ tile, pinned, namespace, href, names }: { tile: PortTile; pinned: boolean; namespace?: string; href: Href; names: ViewNames }) {
  const links = pinned ? tile.links : tile.links.slice(0, PREVIEW_LINKS);
  // A breakout's lanes share one Cable: name it once, then list each position's peer.
  const first = tile.links[0].cable;
  const shared = tile.links.length > 1 && first && tile.links.every(link => link.cable?.name === first.name) ? first : undefined;
  return (
    <>
      <p className="inventory-port-card-meta">{[tile.type ? undefined : 'Not on this Device', tile.connector ?? 'Connector not recorded', tile.positions > 1 ? `${tile.positions} positions` : undefined].filter(Boolean).join(' · ')}</p>
      {tile.links[0].internal && <p className="inventory-port-card-meta">Internal path to {tile.links[0].internal.componentName ?? tile.links[0].internal.componentID}{tile.links[0].internal.position > 1 ? ` · position ${tile.links[0].internal.position}` : ''}</p>}
      {shared && (
        <dl className="inventory-port-card-fields">
          <dt>Cable</dt>
          <dd>
            {pinned ? <Link to={href('Cable', namespace, shared.name)}>{names.cable(shared)}</Link> : names.cable(shared)}
            {shared.medium && <small>{shared.medium}</small>}
          </dd>
          {pinned && <><dt>Cable record</dt><dd><code>{shared.name}</code></dd></>}
        </dl>
      )}
      <ul className="inventory-port-card-links">
        {links.map(link => {
          const detail = peerDetail(link);
          return (
            <li key={link.key} data-state={link.state}>
              <p><StateText state={link.state} />{tile.positions > 1 || link.position > 1 ? <span className="inventory-muted"> · position {link.position}</span> : null}</p>
              {link.cable ? (
                <dl className="inventory-port-card-fields">
                  {!shared && <>
                    <dt>Cable</dt>
                    <dd>
                      {pinned ? <Link to={href('Cable', namespace, link.cable.name)}>{names.cable(link.cable)}</Link> : names.cable(link.cable)}
                      {mediumText(link) && <small>{mediumText(link)}</small>}
                    </dd>
                  </>}
                  <dt>Peer</dt>
                  <dd>
                    {pinned ? <PeerTarget peer={link.peer} namespace={namespace} href={href} names={names} /> : peerText(link.peer, names)}
                    {detail && <small>{detail}</small>}
                    {pinned && <Trace hops={link.trace} namespace={namespace} href={href} names={names} />}
                  </dd>
                  {pinned && link.reasons.length > 0 && <><dt>Why</dt><dd>{link.reasons.join(' ')}</dd></>}
                  {link.checks.length > 0 && <><dt>Check</dt><dd className="inventory-connection-check">{link.checks.join(' ').replace(/^Check: /, '')}</dd></>}
                  {pinned && !shared && <><dt>Cable record</dt><dd><code>{link.cable.name}</code></dd></>}
                  {pinned && link.peer && link.peer.access !== 'denied' && link.peer.name && <><dt>Peer record</dt><dd><code>{link.peer.name}</code></dd></>}
                </dl>
              ) : (
                <>
                  {detail && <small>{detail}</small>}
                  {pinned && link.reasons.length > 0 && <small>{link.reasons.join(' ')}</small>}
                </>
              )}
            </li>
          );
        })}
      </ul>
      {tile.links.length > links.length && <p className="inventory-muted">+{tile.links.length - links.length} more; open the port for all of them.</p>}
      <div className="inventory-port-card-health">
        <p className="inventory-port-card-heading">Health</p>
        {/* No inventory kind has observed status yet. Do not derive any. */}
        <p className="inventory-muted">No health reported</p>
      </div>
    </>
  );
}

/** Which tile an arrow key moves to, by the tiles' drawn positions when there is layout to read. */
function nextTile(items: HTMLElement[], index: number, key: string) {
  if (key === 'Home') return 0;
  if (key === 'End') return items.length - 1;
  if (key === 'ArrowRight') return index + 1;
  if (key === 'ArrowLeft') return index - 1;
  if (key !== 'ArrowDown' && key !== 'ArrowUp') return undefined;
  const step = key === 'ArrowDown' ? 1 : -1;
  const rects = items.map(item => item.getBoundingClientRect());
  const here = rects[index];
  if (!here.height) return index + step;
  const centre = (rect: DOMRect) => rect.left + rect.width / 2;
  const rows = rects.map((rect, at) => ({ at, rect })).filter(({ rect }) => step > 0 ? rect.top >= here.bottom - 1 : rect.bottom <= here.top + 1);
  if (!rows.length) return index;
  const rowTop = step > 0 ? Math.min(...rows.map(({ rect }) => rect.top)) : Math.max(...rows.map(({ rect }) => rect.top));
  const row = rows.filter(({ rect }) => Math.abs(rect.top - rowTop) < 1);
  return row.reduce((best, item) => Math.abs(centre(item.rect) - centre(here)) < Math.abs(centre(best.rect) - centre(here)) ? item : best).at;
}

function PortGrid({ faces, namespace, href, names }: { faces: LayoutFace[]; namespace?: string; href: Href; names: ViewNames }) {
  const container = useRef<HTMLDivElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const cardID = useId();
  const cardTitleID = useId();
  const [expanded, setExpanded] = useState<Set<ConnectionGroup>>(() => new Set());
  const tiles = useMemo(() => faces.flatMap(face => face.clusters.filter(cluster => !catalogOnly(cluster.tiles) || expanded.has(cluster.group)).flatMap(cluster => cluster.tiles)), [faces, expanded]);
  // Labels are unique within a cluster: its header carries the type.
  const labels = useMemo(() => new Map(faces.flatMap(face => face.clusters.flatMap(cluster => [...shortLabels(cluster.tiles)]))), [faces]);
  const [active, setActive] = useState(tiles[0]?.key);
  const [open, setOpen] = useState<string>();
  const [preview, setPreview] = useState<string>();
  const [at, setAt] = useState<{ left: number; top: number }>();
  const pointer = useRef<string | undefined>(undefined);
  const current = tiles.some(tile => tile.key === active) ? active : tiles[0]?.key;
  // A pinned card replaces the preview; there is never more than one card.
  const cardKey = open ?? preview;
  const card = tiles.find(tile => tile.key === cardKey);
  const showFaces = faces.length > 1 || faces[0]?.face !== 'unknown';

  const element = (key?: string) => key ? [...(container.current?.querySelectorAll<HTMLElement>('[data-tile]') ?? [])].find(item => item.dataset.tile === key) : undefined;
  const items = () => [...(container.current?.querySelectorAll<HTMLElement>('[data-tile]') ?? [])];

  // The card sits under its tile, kept inside the grid's width.
  useLayoutEffect(() => {
    const tile = element(cardKey);
    const box = container.current;
    if (!tile || !box || !cardRef.current) return;
    const width = cardRef.current.offsetWidth;
    const left = Math.max(0, Math.min(tile.offsetLeft, box.clientWidth - width));
    const next = { left, top: tile.offsetTop + tile.offsetHeight + 6 };
    setAt(previous => previous?.left === next.left && previous.top === next.top ? previous : next);
  }, [cardKey]);

  const close = () => {
    const key = open;
    setOpen(undefined);
    setPreview(undefined);
    element(key)?.focus();
  };
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      if (open || preview) { event.preventDefault(); event.stopPropagation(); if (open) close(); else setPreview(undefined); }
      return;
    }
    const list = items();
    const index = list.indexOf(document.activeElement as HTMLElement);
    if (index === -1) return;
    const next = nextTile(list, index, event.key);
    if (next === undefined) return;
    event.preventDefault();
    const target = list[Math.max(0, Math.min(list.length - 1, next))];
    const key = target.dataset.tile!;
    setActive(key);
    // A pinned card follows the focused tile; otherwise focus previews it.
    if (open) setOpen(key);
    else setPreview(key);
    target.focus();
  };

  const renderTile = (tile: PortTile) => (
    <li key={tile.key}>
      <button
        type="button"
        className="inventory-port"
        data-tile={tile.key}
        data-state={tile.state}
        data-shape={tile.shape}
        tabIndex={tile.key === current ? 0 : -1}
        aria-label={tileLabel(tile, names)}
        aria-expanded={open === tile.key}
        aria-controls={open === tile.key ? cardID : undefined}
        onPointerDown={event => { pointer.current = event.pointerType; }}
        onPointerEnter={event => { if (event.pointerType === 'mouse') setPreview(tile.key); }}
        onPointerLeave={event => { if (event.pointerType === 'mouse') setPreview(value => value === tile.key ? undefined : value); }}
        onFocus={() => {
          setActive(tile.key);
          // Touch has no hover: a tap opens the card through the click, not a preview.
          if (pointer.current !== 'touch' && pointer.current !== 'pen') setPreview(tile.key);
        }}
        onBlur={() => { pointer.current = undefined; setPreview(value => value === tile.key ? undefined : value); }}
        onClick={() => { setActive(tile.key); setPreview(undefined); setOpen(value => value === tile.key ? undefined : tile.key); }}
      >
        <span className="inventory-port-face" aria-hidden="true">
          <span className="inventory-port-label">{labels.get(tile.key)}</span>
          {TILE_GLYPH[tile.state] && <span className="inventory-port-glyph">{TILE_GLYPH[tile.state]}</span>}
        </span>
        {tile.positions > 1 && <span className="inventory-port-lanes" aria-hidden="true">{tile.lanes.map(lane => <span key={lane.position} data-state={lane.state} />)}</span>}
      </button>
    </li>
  );

  return (
    <div
      ref={container}
      className="inventory-port-grid"
      role="group"
      aria-label="Port tiles. Arrow keys move between ports; Enter opens a port's card; Escape closes it."
      onKeyDown={onKeyDown}
    >
      {faces.map(face => (
        <div key={face.face} className="inventory-port-face-group" data-face={face.face} role={showFaces ? 'group' : undefined} aria-label={showFaces ? face.title : undefined}>
          {showFaces && <p className="inventory-port-face-title">{face.title}</p>}
          <div className="inventory-port-plate">
            {face.clusters.filter(cluster => !catalogOnly(cluster.tiles) || expanded.has(cluster.group)).map(cluster => (
              <PortCluster key={cluster.group} title={cluster.title} group={cluster.group} tiles={cluster.tiles}>{cluster.tiles.map(renderTile)}</PortCluster>
            ))}
          </div>
          {face.clusters.some(cluster => catalogOnly(cluster.tiles) && !expanded.has(cluster.group)) && (
            <p className="inventory-port-hidden">
              {face.clusters.filter(cluster => catalogOnly(cluster.tiles) && !expanded.has(cluster.group)).map(cluster => (
                <button key={cluster.group} type="button" className="inventory-link-button" aria-expanded={false} onClick={() => setExpanded(value => new Set(value).add(cluster.group))}>
                  Show {cluster.title} · {portsWord(cluster.tiles.length)}, catalog only
                </button>
              ))}
            </p>
          )}
        </div>
      ))}
      {card && (
        <div
          ref={cardRef}
          id={open ? cardID : undefined}
          className="inventory-port-card"
          data-state={card.state}
          data-pinned={open ? '' : undefined}
          role={open ? 'dialog' : 'tooltip'}
          aria-modal={open ? false : undefined}
          aria-labelledby={open ? cardTitleID : undefined}
          aria-hidden={open ? undefined : true}
          style={at ? { '--card-left': `${at.left}px`, '--card-top': `${at.top}px` } as CSSProperties : { visibility: 'hidden' }}
          onKeyDown={event => { if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(); } }}
        >
          <div className="inventory-port-card-title">
            <h5 id={cardTitleID}>{card.name}</h5>
            {open && <Button type="secondary" theme="outline" size="small" htmlType="button" onClick={close} aria-label={`Close ${card.name}`}>Close</Button>}
          </div>
          <PortCard tile={card} pinned={Boolean(open)} namespace={namespace} href={href} names={names} />
        </div>
      )}
    </div>
  );
}

function PortCluster({ group, title, tiles, children }: { group: ConnectionGroup; title: string; tiles: PortTile[]; children: ReactNode }) {
  const titleID = useId();
  return (
    <div className="inventory-port-cluster" role="group" aria-labelledby={titleID} data-group={group}>
      <p className="inventory-port-cluster-title" id={titleID}>
        {title} · {tiles.length} <span className="inventory-count">· {tileSummary(tiles)}</span>
      </p>
      <ul className="inventory-port-row" aria-label={`${title} ports`}>{children}</ul>
      {group === 'power' && <p className="inventory-connection-group-note">Power redundancy unknown. Separate inlets or feeds do not prove independent supply.</p>}
    </div>
  );
}

// ---- Diagram (small devices) ----

function DiagramRow({ link, namespace, href, names }: { link: ConnectionLink; namespace?: string; href: Href; names: ViewNames }) {
  const detail = peerDetail(link);
  return (
    <li className="inventory-connection-row" data-state={link.state}>
      <div className="inventory-connection-line">
        <span className="inventory-connection-ports">
          <span className="inventory-connection-local">{localLabel(link)}</span>
          {link.chassisFace && <span className="inventory-connection-face">{faceLabel(link)}</span>}
          <span className="inventory-connection-remote">{link.cable ? remotePortLabel(link.peer) : ''}</span>
        </span>
        <span className="inventory-connection-wire" aria-hidden="true" />
        <span className="inventory-connection-cable">
          <Glyph state={link.state} />{' '}
          {link.cable
            ? <Link to={href('Cable', namespace, link.cable.name)} data-connection-roving="" aria-label={linkLabel(link, names)}>{names.cable(link.cable)}</Link>
            : <span>{linkStateWords[link.state]}</span>}
          {link.cable && link.state !== 'cabled' && <> · {linkStateWords[link.state].toLowerCase()}</>}
        </span>
      </div>
      <div className="inventory-connection-peer">
        {link.cable ? <PeerTarget peer={link.peer} namespace={namespace} href={href} names={names} roving label={link.peer?.access === 'readable' ? `Peer ${peerText(link.peer, names)}` : undefined} /> : <span>{link.state === 'catalog' ? 'Catalog only' : 'Unknown'}</span>}
        {detail && <small>{detail}</small>}
        <Trace hops={link.trace} namespace={namespace} href={href} names={names} roving />
        {link.checks.length > 0 && <small className="inventory-connection-check">{link.checks.join(' ')}</small>}
      </div>
    </li>
  );
}

function DiagramGroup({ group, title, links, tiles, namespace, href, names }: Group & { namespace?: string; href: Href; names: ViewNames }) {
  const list = (items: ConnectionLink[], label: string) => <ul className="inventory-connection-rows" aria-label={label}>{items.map(link => <DiagramRow key={link.key} link={link} namespace={namespace} href={href} names={names} />)}</ul>;
  // Group by chassis face only when the catalog records at least one face;
  // otherwise every port would sit under a single "Face unknown" heading.
  const faces = links.some(link => link.chassisFace) ? faceGroups(links) : undefined;
  const rows = faces
    ? <div className="inventory-connection-faces">{faces.map(item => <section key={item.face} className="inventory-connection-face-group" data-face={item.face} aria-label={`${title} · ${item.title}`}><p className="inventory-connection-face-title">{item.title} · {linksWord(item.links.length)}</p>{list(item.links, `${title} · ${item.title} links`)}</section>)}</div>
    : list(links, `${title} links`);
  return (
    <li className="inventory-connection-group" data-group={group}>
      <p className="inventory-connection-group-title">{title} · {tiles.length} <span className="inventory-count">· {tileSummary(tiles)}</span></p>
      {rows}
      {group === 'power' && <p className="inventory-connection-group-note">Power redundancy unknown. Separate inlets or feeds do not prove independent supply.</p>}
    </li>
  );
}

function Diagram({ title, summary, groups, namespace, href, names }: { title: string; summary: string; groups: Group[]; namespace?: string; href: Href; names: ViewNames }) {
  const container = useRef<HTMLDivElement>(null);
  const active = useRef<Element | null>(null);
  const visible = () => [...(container.current?.querySelectorAll<HTMLElement>('[data-connection-roving]') ?? [])];
  // One tab stop for the whole drawing; arrow keys move between its links.
  useLayoutEffect(() => {
    const items = visible();
    const current = items.find(item => item === active.current) ?? items[0];
    for (const item of items) item.tabIndex = item === current ? 0 : -1;
  });
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const items = visible();
    const index = items.indexOf(document.activeElement as HTMLElement);
    if (index === -1) return;
    const next = event.key === 'ArrowDown' || event.key === 'ArrowRight' ? index + 1 : event.key === 'ArrowUp' || event.key === 'ArrowLeft' ? index - 1 : event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : undefined;
    if (next === undefined) return;
    event.preventDefault();
    const target = items[Math.max(0, Math.min(items.length - 1, next))];
    for (const item of items) item.tabIndex = item === target ? 0 : -1;
    active.current = target;
    target.focus();
  };
  const side = (left: boolean) => groups.filter(item => LEFT.includes(item.group) === left);
  const renderSide = (left: boolean, label: string) => {
    const items = side(left);
    return items.length ? (
      <ul className="inventory-connection-side" data-side={left ? 'left' : 'right'} aria-label={label}>
        {items.map(item => <DiagramGroup key={item.group} {...item} namespace={namespace} href={href} names={names} />)}
      </ul>
    ) : <div className="inventory-connection-side" data-side={left ? 'left' : 'right'} aria-hidden="true" />;
  };
  return (
    <div
      ref={container}
      className="inventory-connection-diagram"
      data-left={side(true).length > 0 ? '' : undefined}
      role="group"
      aria-label="Connection diagram. Arrow keys move between cables and peers; Enter opens one."
      onKeyDown={onKeyDown}
      onFocus={event => { if ((event.target as HTMLElement).hasAttribute('data-connection-roving')) active.current = event.target; }}
    >
      {renderSide(true, 'Power feeds and power peers')}
      <div className="inventory-connection-device">
        <strong>{title}</strong>
        <small>{summary}</small>
      </div>
      {renderSide(false, 'Network, console and other peers')}
    </div>
  );
}

// ---- List ----

function LinkTable({ groups, namespace, href, names }: { groups: Group[]; namespace?: string; href: Href; names: ViewNames }) {
  return (
    <div className="inventory-connection-tables">
      {groups.flatMap(({ group, title: base, links: all }) => (all.some(link => link.chassisFace) ? faceGroups(all).map(item => ({ key: `${group}/${item.face}`, title: `${base} · ${item.title}`, links: item.links })) : [{ key: group, title: base, links: all }])).map(({ key, title, links }) => (
        <div className="inventory-scroll" key={key} role="region" tabIndex={0} aria-label={`${title} links table`}>
          <Table>
            <TableCaption>{title} · {linksWord(links.length)} <span className="inventory-count">{countsText(links)}</span></TableCaption>
            <TableHeader>
              <TableRow>
                <TableHead>Local port</TableHead>
                <TableHead>Record state</TableHead>
                <TableHead>Cable</TableHead>
                <TableHead>Far end</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {links.map(link => (
                <TableRow key={link.key} data-state={link.state}>
                  <TableCell>{localLabel(link)}<LocalDetail link={link} /></TableCell>
                  <TableCell><StateText state={link.state} />{link.reasons.length > 0 && <small>{link.reasons.join(' ')}</small>}{link.checks.length > 0 && <small className="inventory-connection-check">{link.checks.join(' ')}</small>}</TableCell>
                  <TableCell>{link.cable ? <><Link to={href('Cable', namespace, link.cable.name)}>{names.cable(link.cable)}</Link><small>{cableText(link)}</small></> : <span className="inventory-muted">{cableText(link)}</span>}</TableCell>
                  <TableCell>
                    {link.cable ? <PeerTarget peer={link.peer} namespace={namespace} href={href} names={names} /> : <span className="inventory-muted">{link.state === 'catalog' ? 'Catalog only' : 'Unknown'}</span>}
                    {peerDetail(link) && <small>{peerDetail(link)}</small>}
                    <Trace hops={link.trace} namespace={namespace} href={href} names={names} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ))}
    </div>
  );
}

function Legend({ id }: { id: string }) {
  return (
    <div id={id} className="inventory-connection-legend-panel">
      <ul className="inventory-connection-legend" aria-label="Record states">
        {STATES.map(state => <li key={state} data-state={state}><span className="inventory-port-swatch" data-state={state} aria-hidden="true">{TILE_GLYPH[state]}</span><StateText state={state} /></li>)}
      </ul>
      <p className="inventory-muted inventory-connection-caveat">Port faces come from the hardware catalog; rack faces are derived from the reported mounted face.</p>
    </div>
  );
}

const VIEW_WORDS: Record<View, string> = { ports: 'Ports', diagram: 'Diagram', list: 'List' };

export function DeviceConnections({ device, inventory, href }: { device: Device; inventory: Inventory; href: Href }) {
  const headingID = useId();
  const join = useMemo(() => deviceConnections(device, inventory), [device, inventory]);
  const names = useMemo(() => viewNames(join.links), [join]);
  const groups = useMemo<Group[]>(() => connectionGroups
    .map(item => { const links = join.links.filter(link => link.group === item.group); return { ...item, links, tiles: portTiles(links) }; })
    .filter(item => item.links.length), [join]);
  const faces = useMemo(() => portLayout(join.links), [join]);
  const diagramFits = join.links.length <= DIAGRAM_LINK_LIMIT;
  const [chosen, setView] = useState<View>('ports');
  const view = chosen === 'diagram' && !diagramFits ? 'ports' : chosen;
  const namespace = device.metadata.namespace;
  const tiles = groups.flatMap(group => group.tiles);
  const title = deviceTitle(device);
  const summary = `${portsWord(tiles.length)} · ${tileSummary(tiles)}`;
  const views: View[] = diagramFits ? ['ports', 'diagram', 'list'] : ['ports', 'list'];
  const [legend, setLegend] = useState(false);
  const legendID = useId();

  let body: ReactNode;
  if (join.cableAccess === 'denied') body = <p className="inventory-note">Cable records are unavailable to you, so this Device&rsquo;s connections are unknown. This does not mean its ports are free.</p>;
  else if (!join.links.length) body = <p className="inventory-muted">No typed components or Cable terminations are recorded for this Device, so there is nothing to join.</p>;
  else if (view === 'ports') body = <PortGrid faces={faces} namespace={namespace} href={href} names={names} />;
  else if (view === 'diagram') body = <Diagram title={title} summary={summary} groups={groups} namespace={namespace} href={href} names={names} />;
  else body = <LinkTable groups={groups} namespace={namespace} href={href} names={names} />;

  return (
    <section className="inventory-connections" aria-labelledby={headingID}>
      <div className="inventory-connections-heading">
        <div>
          <h4 id={headingID}>Connections</h4>
          <p className="inventory-muted">Cable records, not live link state</p>
        </div>
        {join.links.length > 0 && (
          <div className="inventory-face-switch" role="group" aria-label="Connection view">
            {views.map(value => (
              <Button key={value} type="secondary" theme={view === value ? 'light' : 'outline'} size="small" htmlType="button" aria-pressed={view === value} onClick={() => setView(value)}>
                {VIEW_WORDS[value]}
              </Button>
            ))}
          </div>
        )}
      </div>
      {join.cableAccess === 'readable' && join.links.length > 0 && (
        <p className="inventory-connection-summary">
          {summary}. <span className="inventory-muted">Unknown is not free; nothing here is verified.{join.catalog === 'unavailable' && ' Catalog unavailable, so catalog-only ports are not shown.'}</span>{' '}
          <button type="button" className="inventory-link-button" aria-expanded={legend} aria-controls={legendID} onClick={() => setLegend(value => !value)}>{legend ? 'Hide legend' : 'Legend'}</button>
        </p>
      )}
      {legend && join.cableAccess === 'readable' && join.links.length > 0 && <Legend id={legendID} />}
      {body}
    </section>
  );
}
