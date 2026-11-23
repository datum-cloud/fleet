// The PowerFeed page's primary view: the recorded power path, read left to
// right (top to bottom on phones): upstream sources → the feed and its rating
// → PDUs drawn as outlet strips → the devices whose PSUs the path reaches.
//
// Outlet tiles reuse the shared port tile and card (device-faceplate.tsx), so
// record states, accessible names and cards match the Device faceplate.
// Telemetry adds a load tint, numbers in each row and a Telemetry section in
// the card; it never changes a record state.

import { Button } from '@datum-cloud/datum-ui/button';
import { useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from 'react';
import { Link } from 'react-router';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { linkStateWords, shortLabels, viewNames, type PortTile, type ViewNames } from '../lib/device-connections';
import { feedPath, FEED_CLASS_GLYPH, FEED_CLASS_WORDS, type PathDevice, type PsuFeed } from '../lib/power-paths';
import { feedCapacity, formatPower } from '../lib/power-capacity';
import { formatAge, NOT_REPORTED, valid, type TelemetrySource } from '../lib/telemetry';
import { sharedValue } from '../lib/display-names';
import { PortCard, PortTileButton, type PortTelemetry } from './device-faceplate';
import { peerText, type Href } from './connection-parts';
import { CapacityBar, ReadingLine, usePortTelemetry, useTelemetry } from './power-telemetry';
import { TabLink } from './inventory-record-layout';

// ---- One strip of outlets ----

export interface PowerStripProps {
  /** The heading, such as "Power outlets" or the PDU's name. */
  title: ReactNode;
  /** The region's accessible name. */
  label: string;
  count?: string;
  tiles: PortTile[];
  names: ViewNames;
  namespace?: string;
  href: Href;
  cablesDenied: boolean;
  telemetry: (tile: PortTile) => PortTelemetry | undefined;
  /** What the row's outlet is cabled to. */
  peer: (tile: PortTile) => ReactNode;
  reading: (tile: PortTile) => ReactNode;
  children?: ReactNode;
}

/**
 * A PDU-like strip: one row per outlet with its tile, what it is cabled to,
 * and its observed load. One tab stop; arrow keys, Home and End move between
 * tiles; Enter or a click opens the outlet's card; Escape closes it.
 */
export function PowerStrip({ title, label, count, tiles, names, namespace, href, cablesDenied, telemetry, peer, reading, children }: PowerStripProps) {
  const container = useRef<HTMLDivElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const cardID = useId();
  const labels = useMemo(() => shortLabels(tiles), [tiles]);
  const [active, setActive] = useState(tiles[0]?.key);
  const [open, setOpen] = useState<string>();
  const [cardAt, setCardAt] = useState<{ left: number; top: number }>();
  const current = tiles.some(tile => tile.key === active) ? active : tiles[0]?.key;
  const card = tiles.find(tile => tile.key === open);
  const items = () => [...(container.current?.querySelectorAll<HTMLElement>('[data-tile]') ?? [])];
  const element = (key?: string) => key ? items().find(item => item.dataset.tile === key) : undefined;

  useLayoutEffect(() => {
    const row = element(open)?.closest('li');
    if (!row || !cardRef.current) return;
    const next = { left: 0, top: row.offsetTop + row.offsetHeight + 4 };
    setCardAt(previous => previous?.top === next.top && previous.left === next.left ? previous : next);
  }, [open]);

  const close = () => { const key = open; setOpen(undefined); element(key)?.focus(); };
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') { if (open) { event.preventDefault(); event.stopPropagation(); close(); } return; }
    const list = items();
    const index = list.indexOf(document.activeElement as HTMLElement);
    if (index === -1) return;
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? list.length - 1
      : event.key === 'ArrowDown' || event.key === 'ArrowRight' ? index + 1 : event.key === 'ArrowUp' || event.key === 'ArrowLeft' ? index - 1 : undefined;
    if (next === undefined) return;
    event.preventDefault();
    const target = list[Math.max(0, Math.min(list.length - 1, next))];
    setActive(target.dataset.tile!);
    if (open) setOpen(target.dataset.tile);
    target.focus();
  };
  const cardTelemetry = card ? telemetry(card) : undefined;

  return (
    <section className="inventory-power-strip" aria-label={label}>
      <h4>{title}{count && <> <span className="inventory-count">{count}</span></>}</h4>
      {children}
      {tiles.length ? (
        <div ref={container} className="inventory-power-strip-body" role="group" aria-label={`${label}. Arrow keys move between outlets; Enter opens an outlet's card; Escape closes it.`} onKeyDown={onKeyDown}>
          <ul className="inventory-power-strip-rows">
            {tiles.map(tile => {
              const observed = telemetry(tile);
              return (
                <li key={tile.key} data-state={cablesDenied ? 'unavailable' : tile.state}>
                  <PortTileButton
                    tileKey={tile.key}
                    state={cablesDenied ? 'unavailable' : tile.state}
                    accessibleName={[cablesDenied ? `${tile.name}: Cable details unavailable, connections unknown` : `${tile.name}: ${linkStateWords[tile.state]}`, observed?.words].filter(Boolean).join('; ')}
                    label={labels.get(tile.key)}
                    shape={tile.shape}
                    load={observed?.load}
                    tabIndex={tile.key === current ? 0 : -1}
                    expanded={open === tile.key}
                    controls={open === tile.key ? cardID : undefined}
                    onFocus={() => setActive(tile.key)}
                    onClick={() => { setActive(tile.key); setOpen(value => value === tile.key ? undefined : tile.key); }}
                  />
                  <span className="inventory-power-strip-peer">{peer(tile)}</span>
                  <span className="inventory-power-strip-reading">{reading(tile)}{observed?.load?.markKind === 'mismatch' && <strong className="inventory-power-strip-mismatch"> · <span aria-hidden="true">≠ </span>observed and recorded disagree</strong>}</span>
                </li>
              );
            })}
          </ul>
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
              <PortCard tile={card} namespace={namespace} href={href} names={names} cablesDenied={cablesDenied} telemetry={cardTelemetry?.section} health={cardTelemetry?.health} />
            </div>
          )}
        </div>
      ) : <p className="inventory-muted">No outlets recorded.</p>}
    </section>
  );
}

// ---- Devices on the path ----

/** "Draws ~358 W · 40 s ago" from the Device's own reading or the sum of its PSUs. */
export function drawWords(source: TelemetrySource, now: number, device: Device): { text: string; freshness: 'fresh' | 'stale' | 'missing' } {
  const subject = { kind: 'Device' as const, namespace: device.metadata.namespace, name: device.metadata.name };
  const own = valid(source.read(subject)?.powerW, now);
  if (own) return { text: own.state === 'stale' ? `Stale: last drew ~${formatPower(own.value)} · ${formatAge(own.ageSeconds)}` : `Draws ~${formatPower(own.value)} · ${formatAge(own.ageSeconds)}`, freshness: own.state };
  const psus = (device.spec.components ?? []).filter(component => component.type === 'PowerInlet');
  const readings = psus.map(psu => valid(source.read({ ...subject, componentID: psu.id })?.powerW, now)).filter(item => item !== undefined);
  if (!readings.length) return { text: `Power draw: ${NOT_REPORTED}`, freshness: 'missing' };
  const total = readings.reduce((sum, item) => sum + item.value, 0);
  const oldest = Math.max(...readings.map(item => item.ageSeconds));
  const stale = readings.some(item => item.state === 'stale');
  const partial = readings.length < psus.length ? ` (${readings.length} of ${psus.length} PSUs report)` : '';
  return { text: `${stale ? 'Stale: ' : ''}Draws ${partial ? '≥' : '~'}${formatPower(total)}${partial} · ${formatAge(oldest)}`, freshness: stale ? 'stale' : 'fresh' };
}

/** The Device page's one-line draw summary. */
export function DeviceDraw({ device }: { device: Device }) {
  const { source, now } = useTelemetry();
  const draw = drawWords(source, now, device);
  return <span className="inventory-telemetry-line" data-freshness={draw.freshness}>{draw.freshness === 'stale' && <span aria-hidden="true">◷ </span>}{draw.text}{draw.freshness !== 'missing' && ' · observed, not verified'}</span>;
}

export function FeedClassBadge({ value }: { value: PathDevice['class'] }) {
  return <span className="inventory-feed-class" data-class={value}><span aria-hidden="true">{FEED_CLASS_GLYPH[value]} </span>{FEED_CLASS_WORDS[value]}</span>;
}

// ---- The path ----

export function PowerFeedPath({ feed, inventory, href }: { feed: PhysicalRecord; inventory: Inventory; href: Href }) {
  const { source, now } = useTelemetry();
  const namespace = feed.metadata.namespace;
  const path = useMemo(() => feedPath(feed, inventory), [feed, inventory]);
  const capacity = feedCapacity(feed.spec.supply);
  const cablesDenied = path.cableAccess === 'denied';
  const names = useMemo(() => viewNames([...path.links, ...path.pdus.flatMap(pdu => pdu.links)]), [path]);
  const feedLabel = feed.spec.displayName || 'This feed';
  const feedTelemetry = usePortTelemetry({ kind: 'PowerFeed', namespace, name: feed.metadata.name, label: feedLabel }, { continuousA: capacity.continuousA, cablesDenied });
  const sources = feed.spec.upstreamSources ?? [];
  const supply = feed.spec.supply;
  const terminations = path.terminations;
  const connector = sharedValue(terminations.map(tile => tile.connector));

  const termPeer = (tile: PortTile): ReactNode => {
    if (cablesDenied) return 'Unknown';
    if (!tile.links.some(link => link.cable)) return <span className="inventory-muted">No cable recorded</span>;
    return tile.links.map(link => {
      const peer = link.peer;
      const target = !peer ? 'Unknown' : peer.access === 'readable' ? <><Link to={href(peer.kind, namespace, peer.name)}>{names.peer(peer)}</Link> · {peer.componentName ?? peer.componentID}</> : peerText(peer, names);
      return <span key={link.key} className="inventory-outlet-link">{target}{link.cable && <small> via <Link to={href('Cable', namespace, link.cable.name)}>{names.cable(link.cable)}</Link></small>}{link.state === 'conflict' && <small> · {linkStateWords.conflict}</small>}</span>;
    });
  };
  const outletPeer = (tile: PortTile): ReactNode => {
    if (!tile.links.some(link => link.cable)) return <span className="inventory-muted">No cable recorded</span>;
    return tile.links.map(link => <span key={link.key} className="inventory-outlet-link">{link.peer ? peerText(link.peer, names) : 'Unknown'}{link.state !== 'cabled' && <small> · {linkStateWords[link.state]}</small>}</span>);
  };
  const strip = (kind: 'PowerFeed' | 'Device', name: string) => (tile: PortTile) => <ReadingLine reading={source.read({ kind, namespace, name, componentID: tile.componentID })} now={now} />;

  return (
    <div className="inventory-power-path" id="power-path">
      <ol className="inventory-power-stages" aria-label="Power path, from upstream to devices">
        <li className="inventory-power-stage" data-stage="upstream">
          <h4>Upstream</h4>
          <p>{sources.length ? <TabLink tab="upstream">{sources.length === 1 ? '1 source recorded' : `${sources.length} sources recorded`}</TabLink> : <span className="inventory-muted">No upstream source recorded</span>}</p>
        </li>
        <li className="inventory-power-stage" data-stage="feed">
          <h4>Feed</h4>
          <p className="inventory-power-rating">{supply ? `${supply.type} · ${supply.voltageV ?? 'Unknown'} V · ${supply.currentA ?? 'Unknown'} A · ${supply.phase ? `${supply.phase.toLowerCase()} phase` : 'phase not recorded'}` : 'Supply not recorded'}</p>
          <CapacityBar feed={feed} reading={source.read({ kind: 'PowerFeed', namespace, name: feed.metadata.name })} now={now} source={source} />
          <PowerStrip
            title="Power outlets" label="Power outlets" count={`${terminations.length}${connector ? ` · ${connector}` : ''}`}
            tiles={terminations} names={names} namespace={namespace} href={href} cablesDenied={cablesDenied}
            telemetry={feedTelemetry} peer={termPeer} reading={strip('PowerFeed', feed.metadata.name)}
          />
        </li>
        <li className="inventory-power-stage" data-stage="pdu">
          <h4>PDUs <span className="inventory-count">{cablesDenied || path.devicesDenied ? '?' : path.pdus.length}</span></h4>
          {cablesDenied ? <p>Cable details unavailable. PDUs on this feed are unknown.</p>
            : path.devicesDenied ? <p>Device details unavailable. PDUs on this feed are unknown.</p>
              : path.pdus.length ? path.pdus.map(pdu => <PduStrip key={pdu.device.metadata.name} pdu={pdu} names={names} namespace={namespace} href={href} continuousA={capacity.continuousA} />)
                : <p className="inventory-muted">No PDU is cabled to this feed in the records.</p>}
        </li>
        <li className="inventory-power-stage" data-stage="devices">
          <h4>Devices <span className="inventory-count">{cablesDenied || path.devicesDenied ? '?' : path.devices.length}</span></h4>
          {cablesDenied || path.devicesDenied ? <p>{cablesDenied ? 'Cable' : 'Device'} details unavailable. Devices on this feed are unknown.</p>
            : path.devices.length ? <ul className="inventory-power-devices">{path.devices.map(row => <PathDeviceRow key={row.device.metadata.name} row={row} feed={feed} terminations={terminations} href={href} />)}</ul>
              : <p className="inventory-muted">No device PSU is cabled to this feed in the records.</p>}
        </li>
      </ol>
    </div>
  );
}

function PduStrip({ pdu, names, namespace, href, continuousA }: { pdu: ReturnType<typeof feedPath>['pdus'][number]; names: ViewNames; namespace?: string; href: Href; continuousA?: number }) {
  const { source, now } = useTelemetry();
  const telemetry = usePortTelemetry({ kind: 'Device', namespace, name: pdu.device.metadata.name, label: pdu.label }, { continuousA });
  const outlets = pdu.tiles.filter(tile => tile.type === 'PowerOutlet');
  const connector = sharedValue(outlets.map(tile => tile.connector));
  const peer = (tile: PortTile): ReactNode => {
    if (!tile.links.some(link => link.cable)) return <span className="inventory-muted">No cable recorded</span>;
    return tile.links.map(link => {
      const target = link.peer;
      const text = !target ? 'Unknown' : target.access === 'readable' ? <><Link to={href(target.kind, namespace, target.name)}>{names.peer(target)}</Link> · {target.componentName ?? target.componentID}</> : peerText(target, names);
      return <span key={link.key} className="inventory-outlet-link">{tile.type === 'PowerInlet' ? '← ' : ''}{text}{link.state !== 'cabled' && <small> · {linkStateWords[link.state]}</small>}</span>;
    });
  };
  return <PowerStrip
    title={<Link to={href('Device', namespace, pdu.device.metadata.name)}>{pdu.label}</Link>}
    label={`${pdu.label} outlets`} count={`${outlets.length} outlets${connector ? ` · ${connector}` : ''}`}
    tiles={pdu.tiles} names={names} namespace={namespace} href={href} cablesDenied={false}
    telemetry={telemetry} peer={peer}
    reading={tile => <ReadingLine reading={source.read({ kind: 'Device', namespace, name: pdu.device.metadata.name, componentID: tile.componentID })} now={now} />}
  >
    <p className="inventory-power-strip-meta">Fed by {pdu.fedBy.join(', ')} · total <ReadingLine reading={source.read({ kind: 'Device', namespace, name: pdu.device.metadata.name })} now={now} /></p>
  </PowerStrip>;
}

function PathDeviceRow({ row, feed, terminations, href }: { row: PathDevice; feed: PhysicalRecord; terminations: PortTile[]; href: Href }) {
  const { source, now } = useTelemetry();
  const namespace = feed.metadata.namespace;
  const direct = (psu: PsuFeed) => terminations.find(tile => tile.links.some(link => link.peer?.name === row.device.metadata.name && link.peer.componentID === psu.componentID))?.name;
  return <li>
    <p className="inventory-power-device-title"><Link to={href('Device', namespace, row.device.metadata.name)}>{row.label}</Link> <FeedClassBadge value={row.class} /></p>
    <ul className="inventory-power-psus">
      {row.psus.map(psu => {
        const here = psu.feed?.name === feed.metadata.name;
        return <li key={psu.componentID} data-on-feed={here ? '' : undefined}>
          <span><strong>{psu.name}</strong>{' '}
            {here ? (psu.via ? <>via {psu.via.label} · {psu.via.outlet}</> : <>direct from {direct(psu) ?? 'this feed'}</>)
              : psu.feed ? <>on <Link to={href('PowerFeed', namespace, psu.feed.name)}>{psu.feed.label}</Link></> : <>Feed unknown · {psu.unknown}</>}
          </span>
          {here && <ReadingLine reading={source.read({ kind: 'Device', namespace, name: row.device.metadata.name, componentID: psu.componentID })} now={now} />}
        </li>;
      })}
    </ul>
  </li>;
}
