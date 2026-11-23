// The PowerFeed page's primary view: the recorded power path, read left to
// right (top to bottom on phones): upstream sources → the feed and its rating
// → PDUs drawn as outlet strips → the devices whose PSUs the path reaches.
//
// Outlets are tiles, never rows of text: a PDU whose model records a layout
// is drawn by the shared faceplate renderer (faceplate-drawing.tsx), and
// anything else is a strip of the shared port tile shaped by connector kind
// (device-faceplate.tsx), so record states, accessible names, tooltips and
// cards match the Device faceplate. Telemetry adds a load tint and marks on
// each tile, one "updated" line per group, and the numbers in the card; it
// never changes a record state.

import { useMemo, type ReactNode } from 'react';
import { Link } from 'react-router';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { shortLabels, viewNames, type PortTile, type ViewNames } from '../lib/device-connections';
import { feedPath, FEED_CLASS_GLYPH, FEED_CLASS_WORDS, type PathDevice, type PsuFeed } from '../lib/power-paths';
import { feedCapacity, formatPower } from '../lib/power-capacity';
import { formatAge, NOT_REPORTED, valid, type PowerTelemetry, type TelemetrySource } from '../lib/telemetry';
import { TEXT_TILE_LIMIT } from '../lib/faceplate-layout';
import { sharedValue } from '../lib/display-names';
import { numbering, usePortGrid, type ComponentOwner, type PortTelemetry } from './device-faceplate';
import { deviceDrawingModel, FaceplateDrawing } from './faceplate-drawing';
import type { Href } from './connection-parts';
import { CapacityBar, ReadingLine, usePortTelemetry, useTelemetry } from './power-telemetry';
import { TabLink } from './inventory-record-layout';

// ---- One strip of outlets ----

export interface OutletTilesProps {
  /** The group's accessible name, such as "Power outlets". */
  label: string;
  tiles: PortTile[];
  names: ViewNames;
  namespace?: string;
  href: Href;
  cablesDenied: boolean;
  telemetry: (tile: PortTile) => PortTelemetry | undefined;
  /** The record that holds the outlets, for the card's wording. */
  owner: ComponentOwner;
}

/**
 * Outlets as a strip of port tiles, for a holder without a drawn layout (a
 * PowerFeed's terminations, or a PDU whose model records none). Tiles are
 * the shared PortTileButton shaped by connector kind, with the shared
 * tooltip, card and keyboard model (usePortGrid). The dense rule is the
 * faceplate's: above TEXT_TILE_LIMIT the tiles carry no text and faint
 * numbers mark the edges. Load is a tint and a mark; numbers are in the card.
 */
export function OutletTiles({ label, tiles, names, namespace, href, cablesDenied, telemetry, owner }: OutletTilesProps) {
  const grid = usePortGrid({ tiles, names, namespace, href, cablesDenied, telemetry, owner });
  const dense = tiles.length > TEXT_TILE_LIMIT;
  const labels = useMemo(() => shortLabels(tiles), [tiles]);
  const marks = useMemo(() => dense ? numbering(tiles, labels) : new Map<string, string>(), [dense, tiles, labels]);
  return (
    <div ref={grid.container} className="inventory-port-grid inventory-outlet-tiles" role="group" aria-label={`${label}. Arrow keys move between outlets; Enter opens an outlet's card; Escape closes it.`} onKeyDown={grid.onKeyDown}>
      <ul className="inventory-port-row" aria-label={label} data-compact={dense ? '' : undefined}>
        {tiles.map(tile => (
          <li key={tile.key}>
            {grid.tileButton(tile, { label: dense ? undefined : labels.get(tile.key) })}
            {dense && <span className="inventory-port-number" aria-hidden="true">{marks.get(tile.key) ?? ''}</span>}
          </li>
        ))}
      </ul>
      {grid.overlay}
    </div>
  );
}

/**
 * When a group's readings were taken, said once for the group: "Updated
 * 40 s ago", "Stale · updated 3 h ago", plus how many are stale or not
 * reported. "Not reported" when none is, never 0.
 */
export function groupFreshness(readings: (PowerTelemetry | undefined)[], now: number): { text: string; freshness: 'fresh' | 'stale' | 'missing' } {
  let fresh = 0;
  let stale = 0;
  let missing = 0;
  let freshAge = 0;
  let staleAge = 0;
  for (const reading of readings) {
    const values = [valid(reading?.currentA, now), valid(reading?.powerW, now), valid(reading?.apparentPowerVA, now), valid(reading?.outputState, now), valid(reading?.health, now)].filter(value => value !== undefined);
    if (!values.length) { missing++; continue; }
    const age = Math.max(...values.map(value => value.ageSeconds));
    if (values.some(value => value.state === 'stale')) { stale++; staleAge = Math.max(staleAge, age); } else { fresh++; freshAge = Math.max(freshAge, age); }
  }
  if (!fresh && !stale) return { text: NOT_REPORTED, freshness: 'missing' };
  const notReported = missing ? ` · ${missing} not reported` : '';
  if (!fresh) return { text: `Stale · updated ${formatAge(staleAge)}${notReported}`, freshness: 'stale' };
  return { text: `Updated ${formatAge(freshAge)}${stale ? ` · ${stale} stale, last ${formatAge(staleAge)}` : ''}${notReported}`, freshness: stale ? 'stale' : 'fresh' };
}

/** The group's one freshness line. */
export function GroupFreshness({ readings }: { readings: (PowerTelemetry | undefined)[] }) {
  const { now } = useTelemetry();
  const line = groupFreshness(readings, now);
  return <p className="inventory-power-updated" data-freshness={line.freshness}>{line.freshness === 'stale' && <span aria-hidden="true">◷ </span>}{line.text}</p>;
}

/** A group of outlets on the power path: its heading, its tiles or drawing, and one freshness line. */
function PowerGroup({ title, label, count, readings, children, meta }: { title: ReactNode; label: string; count?: string; readings: (PowerTelemetry | undefined)[]; children: ReactNode; meta?: ReactNode }) {
  return (
    <section className="inventory-power-strip" aria-label={label}>
      <h4>{title}{count && <> <span className="inventory-count">{count}</span></>}</h4>
      {meta}
      {children}
      <GroupFreshness readings={readings} />
    </section>
  );
}

// ---- Devices on the path ----

/** "Draws ~358 W · 40 s ago" from the Device's own reading or the sum of its PSUs. */
export function drawWords(source: TelemetrySource, now: number, device: Device): { text: string; freshness: 'fresh' | 'stale' | 'missing'; short?: string } {
  const subject = { kind: 'Device' as const, namespace: device.metadata.namespace, name: device.metadata.name };
  const own = valid(source.read(subject)?.powerW, now);
  if (own) return { text: own.state === 'stale' ? `Stale: last drew ~${formatPower(own.value)} · ${formatAge(own.ageSeconds)}` : `Draws ~${formatPower(own.value)} · ${formatAge(own.ageSeconds)}`, freshness: own.state, short: formatPower(own.value) };
  const psus = (device.spec.components ?? []).filter(component => component.type === 'PowerInlet');
  const readings = psus.map(psu => valid(source.read({ ...subject, componentID: psu.id })?.powerW, now)).filter(item => item !== undefined);
  if (!readings.length) return { text: `Power draw: ${NOT_REPORTED}`, freshness: 'missing' };
  const total = readings.reduce((sum, item) => sum + item.value, 0);
  const oldest = Math.max(...readings.map(item => item.ageSeconds));
  const stale = readings.some(item => item.state === 'stale');
  const partial = readings.length < psus.length ? ` (${readings.length} of ${psus.length} PSUs report)` : '';
  return { text: `${stale ? 'Stale: ' : ''}Draws ${partial ? '≥' : '~'}${formatPower(total)}${partial} · ${formatAge(oldest)}`, freshness: stale ? 'stale' : 'fresh', short: `${partial ? '≥' : ''}${formatPower(total)}` };
}

/**
 * The draw as one short fact in the Device page's header strip ("Draw
 * 358 W"); its age is the tooltip and accessible text. Stale reads are
 * marked. Hidden when nothing reports a draw, as empty facts are; each PSU's
 * card still says "Not reported".
 */
export function DeviceDrawFact({ device }: { device: Device }) {
  const { source, now } = useTelemetry();
  const draw = drawWords(source, now, device);
  if (draw.freshness === 'missing') return null;
  return <div><dt>Draw</dt><dd className="inventory-telemetry-line inventory-numeric" data-freshness={draw.freshness} title={draw.text}>
    {draw.freshness === 'stale' && <span aria-hidden="true">◷ </span>}<span aria-hidden="true">{draw.short ?? draw.text}</span><span className="inventory-visually-hidden">{draw.text}</span>
  </dd></div>;
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

  const termReadings = terminations.map(tile => source.read({ kind: 'PowerFeed', namespace, name: feed.metadata.name, componentID: tile.componentID }));

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
          <PowerGroup title="Power outlets" label="Power outlets" count={`${terminations.length}${connector ? ` · ${connector}` : ''}`} readings={termReadings}>
            {terminations.length
              ? <OutletTiles label="Power outlets" tiles={terminations} names={names} namespace={namespace} href={href} cablesDenied={cablesDenied} telemetry={feedTelemetry} owner="PowerFeed" />
              : <p className="inventory-muted">No outlets recorded.</p>}
          </PowerGroup>
        </li>
        <li className="inventory-power-stage" data-stage="pdu">
          <h4>PDUs <span className="inventory-count">{cablesDenied || path.devicesDenied ? '?' : path.pdus.length}</span></h4>
          {cablesDenied ? <p>Cable details unavailable. PDUs on this feed are unknown.</p>
            : path.devicesDenied ? <p>Device details unavailable. PDUs on this feed are unknown.</p>
              : path.pdus.length ? path.pdus.map(pdu => <PduStrip key={pdu.device.metadata.name} pdu={pdu} inventory={inventory} names={names} namespace={namespace} href={href} continuousA={capacity.continuousA} />)
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

/**
 * One PDU on the path. With a recorded layout it is drawn by the shared
 * faceplate renderer, cropped to its banks; otherwise its inlet and outlets
 * are a tile strip. Either way the tiles, tooltip and card are the shared ones.
 */
function PduStrip({ pdu, inventory, names, namespace, href, continuousA }: { pdu: ReturnType<typeof feedPath>['pdus'][number]; inventory: Inventory; names: ViewNames; namespace?: string; href: Href; continuousA?: number }) {
  const { source } = useTelemetry();
  const name = pdu.device.metadata.name;
  const telemetry = usePortTelemetry({ kind: 'Device', namespace, name, label: pdu.label }, { continuousA });
  const drawing = useMemo(() => deviceDrawingModel(pdu.device, inventory), [pdu.device, inventory]);
  const outlets = pdu.tiles.filter(tile => tile.type === 'PowerOutlet');
  const connector = sharedValue(outlets.map(tile => tile.connector));
  const label = `${pdu.label} outlets`;
  const readings = pdu.tiles.map(tile => source.read({ kind: 'Device', namespace, name, componentID: tile.componentID }));
  return <PowerGroup
    title={<Link to={href('Device', namespace, name)}>{pdu.label}</Link>}
    label={label} count={`${outlets.length} outlets${connector ? ` · ${connector}` : ''}`} readings={readings}
    meta={<p className="inventory-power-strip-meta">Fed by {pdu.fedBy.join(', ')}</p>}
  >
    {drawing.layout.recorded
      ? <FaceplateDrawing drawing={drawing} namespace={namespace} href={href} telemetry={telemetry} fit label={label} />
      : <OutletTiles label={label} tiles={pdu.tiles} names={names} namespace={namespace} href={href} cablesDenied={false} telemetry={telemetry} owner="Device" />}
  </PowerGroup>;
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
