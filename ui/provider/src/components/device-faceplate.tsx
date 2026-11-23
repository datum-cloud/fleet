// The port faceplate shared by the Device connection view and the rack side
// panel, so the two never drift apart. Tiles are drawn per chassis face, then
// per type, in the order portLayout() gives. State is a fill, outline or
// glyph as well as colour. Hover or focus shows a one-line tooltip; click,
// tap or Enter pins the port's card. Nothing here is observed: no inventory
// kind reports link state or health.

import { Button } from '@datum-cloud/datum-ui/button';
import { useId, useLayoutEffect, useMemo, useRef, useState, type ButtonHTMLAttributes, type CSSProperties, type KeyboardEvent, type ReactNode, type Ref } from 'react';
import { Link } from 'react-router';
import type { Device } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { linkStateWords, shortLabels, viewNames, type ConnectionGroup, type ConnectionLink, type ConnectorShape, type LayoutFace, type LinkState, type PortTile, type ViewNames } from '../lib/device-connections';
import { faceplate } from '../lib/device-faceplate';
import { cableText, peerDetail, PeerTarget, peerText, portsWord, StateText, TILE_GLYPH, tileSummary, Trace, type Href } from './connection-parts';
import { inventoryHref } from './inventory-location';

/**
 * Above this many tiles a cluster draws port-shaped squares with no text, as
 * a switch faceplate does, and prints faint numbers at bank edges instead.
 * Smaller clusters keep short labels (eth0, PSU 1, BMC).
 */
export const TEXT_TILE_LIMIT = 12;
/** In a large cluster, a faint port number every this many tiles. */
export const NUMBER_EVERY = 8;

const catalogOnly = (tiles: PortTile[]) => tiles.every(tile => tile.state === 'catalog');

function laneWords(link: ConnectionLink, names: ViewNames) {
  if (!link.cable) return link.state === 'none' ? 'unknown, not free' : '';
  return `to ${peerText(link.peer, names)}, via ${names.cable(link.cable)}`;
}

/** Everything the tile hides visually: port, state, and each peer and Cable. */
function tileLabel(tile: PortTile, names: ViewNames, cablesDenied: boolean) {
  if (cablesDenied) return `${tile.name}: Cable details unavailable, connections unknown`;
  const state = `${tile.name}: ${linkStateWords[tile.state]}`;
  if (tile.lanes.length === 1 && tile.links.length === 1 && tile.positions <= 1) return [state, laneWords(tile.links[0], names)].filter(Boolean).join(', ');
  if (tile.lanes.length === 1 && tile.positions <= 1) return `${state}; ${tile.links.length} Cables: ${tile.links.map(link => laneWords(link, names)).join('; ')}`;
  return `${state}; ${tile.lanes.map(lane => `position ${lane.position} ${linkStateWords[lane.state].toLowerCase()}${lane.links.some(link => link.cable) ? ` ${lane.links.map(link => laneWords(link, names)).join(' and ')}` : ''}`).join('; ')}`;
}

/** The one-line hover and focus tooltip, e.g. "swp37 · no cable recorded". */
function tileTip(tile: PortTile, names: ViewNames, cablesDenied: boolean) {
  if (cablesDenied) return `${tile.name} · cable details unavailable`;
  const state = linkStateWords[tile.state].toLowerCase();
  const [link] = tile.links;
  const peer = tile.links.length === 1 && link.cable ? ` → ${peerText(link.peer, names)}` : tile.links.length > 1 ? ` · ${tile.links.length} links` : '';
  return `${tile.name} · ${state}${peer}`;
}

/** The Cable's medium, unless the group already says it ("Power" under Power). */
function mediumText(link: ConnectionLink) {
  const text = cableText(link);
  return link.group === 'power' && /^power$/i.test(link.cable?.medium ?? '') ? text.replace(/^Power( · )?/i, '') : text;
}

/**
 * Faint faceplate numbering for a large cluster: the port number at the
 * first tile, every NUMBER_EVERY tiles, where the connector changes (a bank
 * edge) and at the last tile. A tile outside the cluster's main name series
 * (mgmt0 among swp1–swp56) shows its short label instead.
 */
function numbering(tiles: PortTile[], labels: Map<string, string>) {
  const series = (name: string) => name.match(/^(.*?)(\d+)$/);
  const prefixes = new Map<string, number>();
  for (const tile of tiles) { const prefix = series(tile.name)?.[1]; if (prefix !== undefined) prefixes.set(prefix, (prefixes.get(prefix) ?? 0) + 1); }
  const main = [...prefixes].sort((a, b) => b[1] - a[1])[0]?.[0];
  const marks = new Map<string, string>();
  tiles.forEach((tile, index) => {
    const edge = index === 0 || index === tiles.length - 1 || index % NUMBER_EVERY === 0 || tile.shape !== tiles[index - 1].shape;
    if (!edge) return;
    const match = series(tile.name);
    marks.set(tile.key, match && match[1] === main ? match[2] : labels.get(tile.key) ?? tile.name);
  });
  return marks;
}

/** The pinned card for one port: what is recorded about it and its Cables. */
export function PortCard({ tile, namespace, href, names, cablesDenied }: { tile: PortTile; namespace?: string; href: Href; names: ViewNames; cablesDenied: boolean }) {
  // A breakout's lanes share one Cable: name it once, then list each position's peer.
  const first = tile.links[0]?.cable;
  const shared = tile.links.length > 1 && first && tile.links.every(link => link.cable?.name === first.name) ? first : undefined;
  const component = tile.links[0]?.component;
  const internal = tile.links[0]?.internal;
  return (
    <>
      <p className="inventory-port-card-meta">{[tile.connector ?? 'Connector not recorded', tile.positions > 1 ? `${tile.positions} positions` : undefined].filter(Boolean).join(' · ')}</p>
      {!component && <p className="inventory-port-card-meta">Catalog template only; this Device does not record it</p>}
      {component && (component.serial || component.assetTag) && <p className="inventory-port-card-meta">{[component.serial, component.assetTag].filter(Boolean).join(' · ')}</p>}
      {internal && <p className="inventory-port-card-meta">Internal path to {internal.componentName ?? internal.componentID}{internal.position > 1 ? ` · position ${internal.position}` : ''}</p>}
      {cablesDenied ? <p>Cable details unavailable. Connections are unknown, not free.</p> : <>
        {shared && (
          <dl className="inventory-port-card-fields">
            <dt>Cable</dt>
            <dd><Link to={href('Cable', namespace, shared.name)}>{names.cable(shared)}</Link>{shared.medium && <small>{shared.medium}</small>}</dd>
            <dt>Cable record</dt><dd><code>{shared.name}</code></dd>
          </dl>
        )}
        <ul className="inventory-port-card-links">
          {tile.links.map(link => {
            const detail = peerDetail(link);
            return (
              <li key={link.key} data-state={link.state}>
                <p><StateText state={link.state} />{tile.positions > 1 || link.position > 1 ? <span className="inventory-muted"> · position {link.position}</span> : null}</p>
                {link.cable ? (
                  <dl className="inventory-port-card-fields">
                    {!shared && <><dt>Cable</dt><dd><Link to={href('Cable', namespace, link.cable.name)}>{names.cable(link.cable)}</Link>{mediumText(link) && <small>{mediumText(link)}</small>}</dd></>}
                    <dt>Peer</dt>
                    <dd>
                      <PeerTarget peer={link.peer} namespace={namespace} href={href} names={names} />
                      {detail && <small>{detail}</small>}
                      <Trace hops={link.trace} namespace={namespace} href={href} names={names} />
                    </dd>
                    {link.reasons.length > 0 && <><dt>Why</dt><dd>{link.reasons.join(' ')}</dd></>}
                    {link.checks.length > 0 && <><dt>Check</dt><dd className="inventory-connection-check">{link.checks.join(' ').replace(/^Check: /, '')}</dd></>}
                    {!shared && <><dt>Cable record</dt><dd><code>{link.cable.name}</code></dd></>}
                    {link.peer && link.peer.access !== 'denied' && link.peer.name && <><dt>Peer record</dt><dd><code>{link.peer.name}</code></dd></>}
                  </dl>
                ) : (
                  <>
                    {detail && <small>{detail}</small>}
                    {link.reasons.length > 0 && <small>{link.reasons.join(' ')}</small>}
                  </>
                )}
              </li>
            );
          })}
        </ul>
      </>}
      <div className="inventory-port-card-health">
        <p className="inventory-port-card-heading">Health</p>
        {/* No inventory kind has observed status yet. Do not derive any. */}
        <p className="inventory-muted">No health reported</p>
      </div>
    </>
  );
}

export interface PortTileButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'type' | 'aria-label' | 'aria-expanded' | 'aria-controls' | 'children'> {
  /** Record state. Drawn as fill, outline or glyph as well as colour. */
  state: LinkState;
  /** Port name, state and every peer and Cable: everything the tile hides. */
  accessibleName: string;
  /** A short label (eth0, PSU 1). Omit for an unlabelled, port-shaped square. */
  label?: string;
  /** The connector family, for a shaped outline; generic when omitted. */
  shape?: ConnectorShape;
  /** One state per position, drawn as lane bars under a multi-position tile. */
  lanes?: LinkState[];
  /** A stable key; roving focus and card placement find tiles by it. */
  tileKey?: string;
  expanded?: boolean;
  controls?: string;
}

/**
 * One port tile. It is a button: the caller owns roving focus (tabIndex),
 * the tooltip and the card. Visible content is hidden from assistive
 * technology because `accessibleName` carries it all.
 */
export function PortTileButton({ state, accessibleName, label, shape = 'generic', lanes, tileKey, expanded, controls, className, ...rest }: PortTileButtonProps) {
  return (
    <button
      type="button"
      {...rest}
      className={['inventory-port', className].filter(Boolean).join(' ')}
      data-tile={tileKey}
      data-state={state}
      data-shape={shape}
      data-labelled={label ? '' : undefined}
      aria-label={accessibleName}
      aria-expanded={expanded}
      aria-controls={controls}
    >
      <span className="inventory-port-face" aria-hidden="true">
        {label && <span className="inventory-port-label">{label}</span>}
        {TILE_GLYPH[state] && <span className="inventory-port-glyph">{TILE_GLYPH[state]}</span>}
      </span>
      {lanes && lanes.length > 1 && <span className="inventory-port-lanes" aria-hidden="true">{lanes.map((lane, index) => <span key={index} data-state={lane} />)}</span>}
    </button>
  );
}

/**
 * The one-line tooltip for a tile, positioned by the caller with
 * `--card-left` and `--card-top`. Hidden from assistive technology: it
 * repeats the tile's accessible name.
 */
export function PortTooltip({ text, at, ref }: { text: string; at?: { left: number; top: number }; ref?: Ref<HTMLDivElement> }) {
  return (
    <div ref={ref} className="inventory-port-tip" role="tooltip" aria-hidden="true" style={at ? { '--card-left': `${at.left}px`, '--card-top': `${at.top}px` } as CSSProperties : { visibility: 'hidden' }}>
      {text}
    </div>
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

function PortCluster({ label, group, title, tiles, compact, cablesDenied, children }: { label: string; group: ConnectionGroup; title: string; tiles: PortTile[]; compact: boolean; cablesDenied: boolean; children: ReactNode }) {
  return (
    <div className="inventory-port-cluster" role="group" aria-label={label} data-group={group}>
      <p className="inventory-port-cluster-title">
        {title} · {tiles.length} <span className="inventory-count">· {cablesDenied ? 'cable details unavailable' : tileSummary(tiles)}</span>
      </p>
      <ul className="inventory-port-row" aria-label={`${title} ports`} data-compact={compact ? '' : undefined}>{children}</ul>
      {group === 'power' && <p className="inventory-connection-group-note">Power redundancy unknown. Separate inlets or feeds do not prove independent supply.</p>}
    </div>
  );
}

export interface PortFaceplateProps {
  faces: LayoutFace[];
  names: ViewNames;
  namespace?: string;
  href: Href;
  /** Cables are unreadable: every state is unknown, never "no cable". */
  cablesDenied?: boolean;
  /** Hide clusters that are entirely catalog only behind a count. */
  collapseCatalogOnly?: boolean;
  /** Draw a recorded face even when no component is on it, as the rack panel does. */
  showEmptyFaces?: boolean;
}

export function PortFaceplate({ faces: laid, names, namespace, href, cablesDenied = false, collapseCatalogOnly = false, showEmptyFaces = false }: PortFaceplateProps) {
  const container = useRef<HTMLDivElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const tipRef = useRef<HTMLDivElement>(null);
  const cardID = useId();
  const [expanded, setExpanded] = useState<Set<ConnectionGroup>>(() => new Set());
  const known = laid.some(face => face.face !== 'unknown');
  // Without any recorded face, one caption says so instead of guessing.
  const faces = useMemo<LayoutFace[]>(() => {
    if (!known) return laid.map(face => ({ ...face, title: 'Component faces not recorded' }));
    if (!showEmptyFaces) return laid;
    return (['Front', 'Rear', 'unknown'] as const).flatMap(side => laid.find(face => face.face === side) ?? (side === 'unknown' ? [] : [{ face: side, title: side, clusters: [] }]));
  }, [laid, known, showEmptyFaces]);
  const hidden = (cluster: LayoutFace['clusters'][number]) => collapseCatalogOnly && catalogOnly(cluster.tiles) && !expanded.has(cluster.group);
  const tiles = useMemo(() => faces.flatMap(face => face.clusters.filter(cluster => !hidden(cluster)).flatMap(cluster => cluster.tiles)), [faces, expanded, collapseCatalogOnly]);
  // Labels are unique within a cluster: its header carries the type.
  const labels = useMemo(() => new Map(faces.flatMap(face => face.clusters.flatMap(cluster => [...shortLabels(cluster.tiles)]))), [faces]);
  const marks = useMemo(() => new Map(faces.flatMap(face => face.clusters.filter(cluster => cluster.tiles.length > TEXT_TILE_LIMIT).flatMap(cluster => [...numbering(cluster.tiles, labels)]))), [faces, labels]);
  const [active, setActive] = useState(tiles[0]?.key);
  const [open, setOpen] = useState<string>();
  const [tip, setTip] = useState<string>();
  const [cardAt, setCardAt] = useState<{ left: number; top: number }>();
  const [tipAt, setTipAt] = useState<{ left: number; top: number }>();
  const pointer = useRef<string | undefined>(undefined);
  const current = tiles.some(tile => tile.key === active) ? active : tiles[0]?.key;
  const card = tiles.find(tile => tile.key === open);
  // The tooltip never shows over the tile whose card is open.
  const tipTile = tip !== open ? tiles.find(tile => tile.key === tip) : undefined;

  const items = () => [...(container.current?.querySelectorAll<HTMLElement>('[data-tile]') ?? [])];
  const element = (key?: string) => key ? items().find(item => item.dataset.tile === key) : undefined;

  // Cards and tooltips sit under their tile, kept inside the faceplate's width.
  const place = (key: string | undefined, box: HTMLElement | null, set: (value: { left: number; top: number }) => void) => {
    const tile = element(key);
    const host = container.current;
    if (!tile || !host || !box) return;
    const left = Math.max(0, Math.min(tile.offsetLeft, host.clientWidth - box.offsetWidth));
    set({ left, top: tile.offsetTop + tile.offsetHeight + 4 });
  };
  const same = (next: { left: number; top: number }) => (previous?: { left: number; top: number }) => previous?.left === next.left && previous.top === next.top ? previous : next;
  useLayoutEffect(() => place(open, cardRef.current, next => setCardAt(same(next))), [open]);
  useLayoutEffect(() => place(tipTile?.key, tipRef.current, next => setTipAt(same(next))), [tipTile?.key]);

  const close = () => {
    const key = open;
    setOpen(undefined);
    setTip(undefined);
    element(key)?.focus();
  };
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      if (open || tip) { event.preventDefault(); event.stopPropagation(); if (open) close(); else setTip(undefined); }
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
    // A pinned card follows the focused tile; otherwise focus shows the tooltip.
    if (open) setOpen(key);
    else setTip(key);
    target.focus();
  };

  const renderTile = (tile: PortTile, compact: boolean) => (
    <li key={tile.key}>
      <PortTileButton
        tileKey={tile.key}
        state={cablesDenied ? 'unavailable' : tile.state}
        accessibleName={tileLabel(tile, names, cablesDenied)}
        label={compact ? undefined : labels.get(tile.key)}
        shape={tile.shape}
        lanes={tile.positions > 1 && !cablesDenied ? tile.lanes.map(lane => lane.state) : undefined}
        tabIndex={tile.key === current ? 0 : -1}
        expanded={open === tile.key}
        controls={open === tile.key ? cardID : undefined}
        onPointerDown={event => { pointer.current = event.pointerType; }}
        onPointerEnter={event => { if (event.pointerType === 'mouse') setTip(tile.key); }}
        onPointerLeave={event => { if (event.pointerType === 'mouse') setTip(value => value === tile.key ? undefined : value); }}
        onFocus={() => {
          setActive(tile.key);
          // Touch has no hover: a tap opens the card through the click, not a tooltip.
          if (pointer.current !== 'touch' && pointer.current !== 'pen') setTip(tile.key);
        }}
        onBlur={() => { pointer.current = undefined; setTip(value => value === tile.key ? undefined : value); }}
        onClick={() => { setActive(tile.key); setTip(undefined); setOpen(value => value === tile.key ? undefined : tile.key); }}
      />
      {compact && <span className="inventory-port-number" aria-hidden="true">{marks.get(tile.key) ?? ''}</span>}
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
        <div key={face.face} className="inventory-port-face-group" data-face={face.face} role="group" aria-label={face.title}>
          <p className="inventory-port-face-title">{face.title}</p>
          {face.clusters.length ? (
            <div className="inventory-port-plate">
              {face.clusters.filter(cluster => !hidden(cluster)).map(cluster => {
                const compact = cluster.tiles.length > TEXT_TILE_LIMIT;
                return (
                  <PortCluster key={cluster.group} label={`${face.title} · ${cluster.title}`} group={cluster.group} title={cluster.title} tiles={cluster.tiles} compact={compact} cablesDenied={cablesDenied}>
                    {cluster.tiles.map(tile => renderTile(tile, compact))}
                  </PortCluster>
                );
              })}
            </div>
          ) : <p className="inventory-muted">No component records this face.</p>}
          {face.clusters.some(hidden) && (
            <p className="inventory-port-hidden">
              {face.clusters.filter(hidden).map(cluster => (
                <button key={cluster.group} type="button" className="inventory-link-button" aria-expanded={false} onClick={() => setExpanded(value => new Set(value).add(cluster.group))}>
                  Show {cluster.title} · {portsWord(cluster.tiles.length)}, catalog only
                </button>
              ))}
            </p>
          )}
        </div>
      ))}
      {tipTile && <PortTooltip ref={tipRef} text={tileTip(tipTile, names, cablesDenied)} at={tipAt} />}
      {card && (
        <div
          ref={cardRef}
          id={cardID}
          className="inventory-port-card"
          data-state={cablesDenied ? 'unavailable' : card.state}
          data-pinned=""
          data-component-card=""
          role="dialog"
          aria-modal={false}
          aria-label={`${card.name} details`}
          style={cardAt ? { '--card-left': `${cardAt.left}px`, '--card-top': `${cardAt.top}px` } as CSSProperties : { visibility: 'hidden' }}
          onKeyDown={event => { if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(); } }}
        >
          <div className="inventory-port-card-title">
            <h5>{card.name}</h5>
            <Button type="secondary" theme="outline" size="small" htmlType="button" onClick={close} aria-label={`Close ${card.name}`}>Close</Button>
          </div>
          <PortCard tile={card} namespace={namespace} href={href} names={names} cablesDenied={cablesDenied} />
        </div>
      )}
    </div>
  );
}

/**
 * A Device's faceplate for the rack side panel: the same tiles and cards as
 * the connection view. When Cables are denied, the recorded components are
 * still drawn, with every state unknown.
 */
export function DeviceFaceplate({ device, inventory }: { device: Device; inventory: Inventory }) {
  const plate = useMemo(() => faceplate(device, inventory), [device, inventory]);
  const names = useMemo(() => viewNames(plate.links), [plate]);
  const cablesDenied = plate.cableAccess === 'denied';
  return (
    <section className="inventory-faceplate" aria-label="Faceplate">
      {plate.faces.length
        ? <PortFaceplate faces={plate.faces} names={names} namespace={device.metadata.namespace} href={inventoryHref} cablesDenied={cablesDenied} showEmptyFaces />
        : <p className="inventory-muted">No ports are recorded for this Device.</p>}
      {cablesDenied && <p className="inventory-muted">Cable details unavailable. Component states are unknown.</p>}
    </section>
  );
}
