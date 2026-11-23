// The rack Power tab's redundancy view: the rack's feeds side by side, and
// each device once, with its PSUs under the feed their recorded path reaches
// (through PDU outlets and internal mappings). Devices whose PSUs all reach
// one feed are flagged "Single feed (recorded)"; unrecorded paths read "Feed
// unknown". Nothing here claims or verifies redundancy.

import type { CSSProperties } from 'react';
import { Link } from 'react-router';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { classifyFeeds, psuFeeds, type FeedClass, type PsuFeed } from '../lib/power-paths';
import { displayLabel, humanName } from '../lib/display-names';
import { CapacityBar, useTelemetry } from './power-telemetry';
import { FeedClassBadge } from './power-feed-path';
import { inventoryHref } from './inventory-location';
import { ViewLegend } from './inventory-record-layout';

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
    <h4>Feeds and PSUs <span className="inventory-count">{table.length} {table.length === 1 ? 'device' : 'devices'}{single ? ` · ${single} on a single feed (recorded)` : ''}</span></h4>
    {table.length ? <div className="inventory-redundancy-grid" role="table" aria-label="Device PSUs by recorded feed" style={style}>
      <div role="row" className="inventory-redundancy-head">
        <span role="columnheader" className="inventory-redundancy-corner">Device</span>
        {columns.map(column => <div role="columnheader" key={column.key} className="inventory-redundancy-feed" data-feed={column.key === UNKNOWN ? 'unknown' : undefined}>
          {column.feed ? <>
            <Link to={inventoryHref('PowerFeed', column.feed.metadata.namespace, column.feed.metadata.name)}>{column.label}</Link>
            <small>{column.feed.spec.supply ? `${column.feed.spec.supply.type} · ${column.feed.spec.supply.voltageV ?? '?'} V · ${column.feed.spec.supply.currentA ?? '?'} A` : 'Supply not recorded'}{column.feed.spec.rackRef?.name !== rack.metadata.name ? ' · names another rack' : ''}</small>
            <CapacityBar feed={column.feed} reading={source.read({ kind: 'PowerFeed', namespace: column.feed.metadata.namespace, name: column.feed.metadata.name })} now={now} source={source} compact />
          </> : <><strong><span aria-hidden="true">? </span>{column.label}</strong><small>Path not fully recorded</small></>}
        </div>)}
      </div>
      {table.map(row => <div role="row" key={row.device.metadata.name} className="inventory-redundancy-row" data-class={row.class}>
        <div role="rowheader" className="inventory-redundancy-device">
          <Link to={inventoryHref('Device', row.device.metadata.namespace, row.device.metadata.name)}>{displayLabel(row.device)}</Link>
          <FeedClassBadge value={row.class} />
        </div>
        {columns.map(column => {
          const psus = inColumn(row, column);
          return <div role="cell" key={column.key} className="inventory-redundancy-cell" data-empty={psus.length ? undefined : ''}>
            <span className="inventory-redundancy-cell-feed">{column.label}</span>
            {psus.length ? <ul>{psus.map(psu => <li key={psu.componentID}><strong>{psu.name}</strong> <small>{psuText(psu)}</small></li>)}</ul>
              : <span className="inventory-muted"><span aria-hidden="true">–</span><span className="inventory-visually-hidden">No PSU</span></span>}
          </div>;
        })}
      </div>)}
    </div> : <p className="inventory-muted">No device in this rack records a power inlet.</p>}
    <ViewLegend>
      <span aria-hidden="true">⚑ </span>Single feed (recorded): every PSU reaches the same feed in the records. <span aria-hidden="true">⇉ </span>Separate feeds (recorded). <span aria-hidden="true">? </span>Feed unknown: the path is not fully recorded.
      {' '}Separate feeds in records do not prove independent supply; nothing here is verified.
    </ViewLegend>
  </section>;
}
