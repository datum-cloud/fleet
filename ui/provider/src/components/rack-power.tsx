// The rack Power tab's redundancy view: the rack's feeds side by side, and
// each device once, with its PSUs under the feed their recorded path reaches
// (through PDU outlets and internal mappings). Devices whose PSUs all reach
// one feed are flagged "Single feed (recorded)"; unrecorded paths read "Feed
// unknown". Nothing here claims or verifies redundancy.
//
// This grid is the whole Power tab. Each feed's column carries its outlets as
// the shared tiles (peer, Cable and readings in each tile's name, tooltip and
// card) and links to the feed's power path; each PDU's row says how many of
// its outlets have a cable recorded and links to the PDU's faceplate.

import { useMemo, type CSSProperties } from 'react';
import { Link } from 'react-router';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { classifyFeeds, feedPath, FEED_CLASS_WORDS, psuFeeds, type FeedClass, type PsuFeed } from '../lib/power-paths';
import { viewNames } from '../lib/device-connections';
import { feedCapacity } from '../lib/power-capacity';
import { displayLabel, humanName } from '../lib/display-names';
import { CapacityBar, usePortTelemetry, useTelemetry } from './power-telemetry';
import { OutletTiles } from './power-feed-path';
import { inventoryHref } from './inventory-location';

interface Row {
  device: Device;
  psus: PsuFeed[] | undefined;
  class: FeedClass;
}

interface Column {
  key: string;
  label: string;
  feed?: PhysicalRecord;
}

const UNKNOWN = 'unknown';

function rows(devices: Device[], inventory: Inventory): Row[] {
  return devices
    .filter(device => (device.spec.components ?? []).some(component => component.type === 'PowerInlet'))
    .map(device => {
      const psus = psuFeeds(device, inventory);
      return { device, psus, class: classifyFeeds(psus)?.class ?? 'unknown' };
    });
}

function psuText(psu: PsuFeed) {
  if (psu.feed) return psu.via ? `via ${psu.via.label} · ${psu.via.outlet}` : 'direct';
  return psu.unknown ?? 'Path not recorded';
}

/** A feed's outlets as tiles, for its column header. */
function FeedOutlets({ feed, label, inventory }: { feed: PhysicalRecord; label: string; inventory: Inventory }) {
  const path = useMemo(() => feedPath(feed, inventory), [feed, inventory]);
  const names = useMemo(() => viewNames(path.links), [path]);
  const cablesDenied = path.cableAccess === 'denied';
  const telemetry = usePortTelemetry({ kind: 'PowerFeed', namespace: feed.metadata.namespace, name: feed.metadata.name, label }, { continuousA: feedCapacity(feed.spec.supply).continuousA, cablesDenied });
  if (!path.terminations.length) return <small>No outlets recorded</small>;
  return <div className="inventory-redundancy-outlets">
    <OutletTiles label={`${label} outlets`} tiles={path.terminations} names={names} namespace={feed.metadata.namespace} href={inventoryHref} cablesDenied={cablesDenied} telemetry={telemetry} owner="PowerFeed" />
  </div>;
}

/**
 * The feeds-and-PSUs grid. `feeds` are the readable feeds that name this
 * rack; a feed reached from a device here but recorded elsewhere gets its
 * own column too.
 */
export function RackPowerRedundancy({ rack, feeds, devices, inventory }: { rack: PhysicalRecord; feeds: PhysicalRecord[]; devices: Device[] | undefined; inventory: Inventory }) {
  const { source, now } = useTelemetry();
  if (!devices) return <p>Device details unavailable. PSU paths for this rack are unknown.</p>;
  if (inventory.issues?.Cable) return <p>Cable details unavailable. PSU paths for this rack are unknown, not absent.</p>;
  const table = rows(devices, inventory);
  const columns: Column[] = feeds.map(feed => ({ key: feed.metadata.name, label: humanName(feed) ?? 'Unnamed power feed', feed }));
  for (const psu of table.flatMap(row => row.psus ?? [])) {
    if (!psu.feed || columns.some(column => column.key === psu.feed!.name)) continue;
    const feed = inventory.physical.find(record => record.kind === 'PowerFeed' && record.metadata.namespace === rack.metadata.namespace && record.metadata.name === psu.feed!.name);
    columns.push({ key: psu.feed.name, label: psu.feed.label, feed });
  }
  if (table.some(row => !row.psus || row.psus.some(psu => !psu.feed))) columns.push({ key: UNKNOWN, label: 'Feed unknown' });
  const inColumn = (row: Row, column: Column) => (row.psus ?? []).filter(psu => column.key === UNKNOWN ? !psu.feed : psu.feed?.name === column.key);
  const single = table.filter(row => row.class === 'single').length;
  const style = { '--feed-columns': String(columns.length) } as CSSProperties;

  return <section className="inventory-redundancy" aria-label="Feeds and PSUs">
    <h4>Feeds and PSUs <span className="inventory-count">{table.length} {table.length === 1 ? 'device' : 'devices'}{single ? ` · ${single} on a single feed` : ''}</span></h4>
    {table.length ? <div className="inventory-redundancy-grid" role="table" aria-label="Device PSUs by recorded feed" style={style}>
      <div role="row" className="inventory-redundancy-head">
        <span role="columnheader" className="inventory-redundancy-corner">Device</span>
        {columns.map(column => <div role="columnheader" key={column.key} className="inventory-redundancy-feed" data-feed={column.key === UNKNOWN ? 'unknown' : undefined}>
          {column.feed ? <>
            <Link to={inventoryHref('PowerFeed', column.feed.metadata.namespace, column.feed.metadata.name)}>{column.label}</Link>
            <small>{column.feed.spec.supply ? `${column.feed.spec.supply.type} · ${column.feed.spec.supply.voltageV ?? '?'} V · ${column.feed.spec.supply.currentA ?? '?'} A` : 'Supply not recorded'}{column.feed.spec.rackRef?.name !== rack.metadata.name ? ' · names another rack' : ''}</small>
            <CapacityBar feed={column.feed} reading={source.read({ kind: 'PowerFeed', namespace: column.feed.metadata.namespace, name: column.feed.metadata.name })} now={now} source={source} compact />
            <FeedOutlets feed={column.feed} label={column.label} inventory={inventory} />
          </> : <strong>{column.label}</strong>}
        </div>)}
      </div>
      {/* The columns show which feeds each PSU reaches; a single-feed row is marked by colour, with the words as tooltip and accessible text. */}
      {table.map(row => <div role="row" key={row.device.metadata.name} className="inventory-redundancy-row" data-class={row.class} title={row.class === 'single' ? FEED_CLASS_WORDS.single : undefined}>
        <div role="rowheader" className="inventory-redundancy-device">
          <Link to={inventoryHref('Device', row.device.metadata.namespace, row.device.metadata.name)}>{displayLabel(row.device)}</Link>
          <span className="inventory-visually-hidden">{FEED_CLASS_WORDS[row.class]}</span>
        </div>
        {columns.map(column => {
          const psus = inColumn(row, column);
          return <div role="cell" key={column.key} className="inventory-redundancy-cell" data-empty={psus.length ? undefined : ''}>
            <span className="inventory-redundancy-cell-feed">{column.label}</span>
            {psus.length ? <ul>{psus.map(psu => <li key={psu.componentID}><strong>{psu.name}</strong>{psu.feed && <> <small>{psuText(psu)}</small></>}</li>)}</ul>
              : <span className="inventory-muted"><span aria-hidden="true">–</span><span className="inventory-visually-hidden">No PSU</span></span>}
          </div>;
        })}
      </div>)}
    </div> : <p className="inventory-muted">No device in this rack records a power inlet.</p>}
  </section>;
}
