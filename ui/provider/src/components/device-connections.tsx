// The Device page's primary view: the device picture, then one dense table
// of its components. The picture is the drawn faceplate when the DeviceType
// records a layout, otherwise the grouped port tiles; it is never toggled.
// Hovering or focusing a tile outlines its row, and the row outlines its
// tile. See "Device page: picture and component table" in
// docs/development/staff-inventory-ui.md.
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { useId, useMemo, useState } from 'react';
import { Link } from 'react-router';
import type { ChassisFace, Device, PhysicalComponent, RackFace } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import {
  connectionGroups, deviceConnections, deviceTitle, groupOf, interfaceAddresses, linkStateWords, portTiles, shortFaceLabel, unmatchedLinks, viewNames,
  type ConnectionGroup, type ConnectionLink, type DeviceConnections as JoinResult, type LinkState, type TraceHop, type ViewNames,
} from '../lib/device-connections';
import { faceplate } from '../lib/device-faceplate';
import { componentChassisFace, componentRackFace, deviceMountedFace } from '../lib/rack-faces';
import { cableText, peerDetail, PeerTarget, peerText, portsWord, StateText, TileSummary, type Href } from './connection-parts';
import { PortFaceplate, type ComponentHighlight } from './device-faceplate';
import { usePortTelemetry } from './power-telemetry';
import { deviceDrawingModel, FaceplateDrawing, LayoutNotRecorded, moduleStateGlyph, moduleStateWords } from './faceplate-drawing';

/**
 * One table row: a recorded component, a catalog template the Device does
 * not record, a component a Cable names that the Device does not record, or
 * a Cable lane end that matches no recorded position.
 */
interface ComponentRow {
  key: string;
  name: string;
  id: string;
  type?: PhysicalComponent['type'];
  connector?: string;
  chassisFace?: ChassisFace;
  rackFace?: RackFace;
  positions: number;
  /** The most urgent state among its links; unset for a module or item that no Cable names. */
  state?: LinkState;
  links: ConnectionLink[];
  serial?: string;
  assetTag?: string;
  /** The recorded interface MAC address for a port of the same name. */
  mac?: string;
  /** The position the Cable names, for a lane end that matches no recorded position. */
  unmatched?: { position?: number };
}

const GROUP_ORDER = connectionGroups.map(item => item.group);

/**
 * Every component the Device records, then catalog templates and components
 * only a Cable names (grouped as the picture groups them), then Cable lane
 * ends that `deviceConnections()` cannot place, so every Cable naming the
 * Device has a row.
 */
function componentRows(device: Device, inventory: Inventory, join: JoinResult, unmatched: ConnectionLink[]): ComponentRow[] {
  const tiles = new Map(portTiles(join.links).map(tile => [tile.key, tile]));
  const macs = interfaceAddresses(device).byComponent;
  const mounted = deviceMountedFace(device, inventory);
  const used = new Set<string>();
  const rows: (ComponentRow & { group: ConnectionGroup })[] = (device.spec.components ?? []).map((component, index) => {
    const group = groupOf[component.type];
    const tile = tiles.get(`${group}/${component.id}`);
    if (tile) used.add(tile.key);
    const chassisFace = componentChassisFace(device, inventory, component.id);
    return {
      key: `component/${index}`, group, name: component.name, id: component.id, type: component.type, connector: component.connector,
      chassisFace, rackFace: componentRackFace(mounted, chassisFace), positions: component.positions ?? 1,
      state: tile?.state, links: tile?.links ?? [], serial: component.serial, assetTag: component.assetTag, mac: macs.get(component.id),
    };
  });
  for (const tile of tiles.values()) {
    if (used.has(tile.key)) continue;
    rows.push({ key: `tile/${tile.key}`, group: tile.group, name: tile.name, id: tile.componentID, type: tile.type, connector: tile.connector, chassisFace: tile.chassisFace, rackFace: tile.rackFace, positions: tile.positions, state: tile.state, links: tile.links });
  }
  rows.sort((a, b) => GROUP_ORDER.indexOf(a.group) - GROUP_ORDER.indexOf(b.group));
  return [...rows, ...unmatched.map(link => ({
    key: link.key, name: link.component?.name ?? link.componentID, id: link.componentID, type: link.component?.type, connector: link.component?.connector,
    chassisFace: link.chassisFace, rackFace: link.rackFace, positions: link.positions, state: link.state, links: [link], unmatched: { position: link.position },
  }))];
}

/** Short type words for the dense table; the picture's group headers keep the long ones. */
const TYPE_WORDS: Record<PhysicalComponent['type'], string> = {
  NetworkPort: 'Network', PowerInlet: 'Power inlet', PowerOutlet: 'Power outlet', ConsolePort: 'Console', ConsoleServerPort: 'Console server',
  FrontPort: 'Front pass-through', RearPort: 'Rear pass-through', Module: 'Module', InventoryItem: 'Item', DeviceBay: 'Device bay',
};

const positionPrefix = (row: ComponentRow, link: ConnectionLink) => row.unmatched || (row.positions <= 1 && link.position === 1) ? '' : `Position ${link.position} · `;
const unique = (items: string[]) => [...new Set(items)];

/** The path beyond the peer in words, for a tooltip: a denied peer stays nameless. */
function traceWords(hops: TraceHop[], names: ViewNames) {
  return hops.map(hop => `via internal path to ${hop.via.componentName ?? hop.via.componentID}${hop.via.position > 1 ? ` · position ${hop.via.position}` : ''}${hop.cable ? `, then ${names.cable(hop.cable)} to ${peerText(hop.peer, names)}` : `: ${linkStateWords[hop.state].toLowerCase()}`}`).join('; ');
}

/**
 * Words a cell keeps out of sight but not out of reach: the hover tooltip
 * and, hidden visually, the accessible text.
 */
function Aside({ text }: { text?: string }) {
  return text ? <span className="inventory-visually-hidden"> {text}</span> : null;
}

/** The peer for each cabled position, the Cable as a small secondary link, and the onward path as a count. */
function ConnectedCell({ row, namespace, href, names }: { row: ComponentRow; namespace?: string; href: Href; names: ViewNames }) {
  const cabled = row.links.filter(link => link.cable);
  const open = row.links.filter(link => !link.cable);
  if (!cabled.length) {
    // No cable recorded reads Unknown, never empty: an empty cell would look free.
    if (row.state === 'none') return <span className="inventory-muted">Unknown</span>;
    return null;
  }
  return (
    <ul className="inventory-component-links">
      {cabled.map(link => {
        const detail = peerDetail(link);
        const onward = link.trace.length ? traceWords(link.trace, names) : undefined;
        return (
          <li key={link.key} title={[detail, onward && `Onward: ${onward}`].filter(Boolean).join('\n') || undefined}>
            {positionPrefix(row, link)}<PeerTarget peer={link.peer} namespace={namespace} href={href} names={names} />
            <Aside text={detail} />
            {' '}<span className="inventory-component-via">via <Link to={href('Cable', namespace, link.cable!.name)} title={cableText(link)}>{names.cable(link.cable!)}</Link></span>
            {onward && <span className="inventory-component-via"> · +{link.trace.length} {link.trace.length === 1 ? 'hop' : 'hops'}<Aside text={onward} /></span>}
          </li>
        );
      })}
      {open.length > 0 && row.positions > 1 && <li className="inventory-muted">No cable recorded at position {open.map(link => link.position).join(', ')}</li>}
    </ul>
  );
}

function StateCell({ row }: { row: ComponentRow }) {
  const reasons = unique(row.links.flatMap(link => link.reasons)).join(' ');
  const checks = unique(row.links.flatMap(link => link.checks)).join(' ');
  return (
    <span title={[reasons, checks].filter(Boolean).join('\n') || undefined}>
      {row.state ? <StateText state={row.state} /> : <span className="inventory-muted"><span className="inventory-connection-glyph" aria-hidden="true">{moduleStateGlyph.recorded}</span> {moduleStateWords.recorded}</span>}
      <Aside text={reasons} />
      {checks && <span className="inventory-connection-check"> · Check<Aside text={checks.replace(/^Check:\s*/, '')} /></span>}
    </span>
  );
}

/**
 * The dense component table: one row per component, about 30 px each. The
 * component id, the reasons behind a state and the Cable's medium are
 * tooltips (and accessible text), not columns. With Cables denied, only the
 * components are listed: no state and no peer.
 */
function ComponentTable({ rows, cablesDenied, namespace, href, names, highlight, describedBy }: { rows: ComponentRow[]; cablesDenied: boolean; namespace?: string; href: Href; names: ViewNames; highlight: ComponentHighlight; describedBy: string }) {
  const serials = rows.some(row => row.serial);
  const tags = rows.some(row => row.assetTag);
  const macs = rows.some(row => row.mac);
  return (
    <div className="inventory-scroll inventory-connection-table" role="region" tabIndex={0} aria-label="Components table" aria-describedby={describedBy}>
      <Table className="inventory-dense-table">
        <TableCaption className="inventory-visually-hidden">Components{cablesDenied ? '' : ' and Cables'}</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>Component</TableHead>
            <TableHead>Type</TableHead>
            <TableHead>Face</TableHead>
            {!cablesDenied && <><TableHead>State</TableHead><TableHead>Connected to</TableHead></>}
            {macs && <TableHead>MAC address</TableHead>}
            {serials && <TableHead>Serial</TableHead>}
            {tags && <TableHead>Asset tag</TableHead>}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map(row => (
            <TableRow
              key={row.key}
              data-state={row.state}
              data-unmatched={row.unmatched ? '' : undefined}
              data-highlight={highlight.id === row.id ? '' : undefined}
              onPointerEnter={event => { if (event.pointerType === 'mouse') highlight.set(row.id); }}
              onPointerLeave={event => { if (event.pointerType === 'mouse') highlight.clear(row.id); }}
              onFocus={() => highlight.set(row.id)}
              onBlur={() => highlight.clear(row.id)}
            >
              <TableCell>
                <span title={row.name !== row.id ? row.id : undefined}>{row.name}</span>
                {row.unmatched && <span className="inventory-muted"> · {row.unmatched.position === undefined ? 'position not recorded' : `position ${row.unmatched.position}`}</span>}
              </TableCell>
              <TableCell>
                {row.type ? TYPE_WORDS[row.type] : 'Not on this Device'}
                {(row.connector || row.positions > 1) && <span className="inventory-muted"> {[row.connector, row.positions > 1 && `${row.positions} positions`].filter(Boolean).join(' · ')}</span>}
              </TableCell>
              <TableCell>{shortFaceLabel(row.chassisFace, row.rackFace)}</TableCell>
              {!cablesDenied && <>
                <TableCell><StateCell row={row} /></TableCell>
                <TableCell><ConnectedCell row={row} namespace={namespace} href={href} names={names} /></TableCell>
              </>}
              {macs && <TableCell className="inventory-numeric">{row.mac}</TableCell>}
              {serials && <TableCell className="inventory-numeric">{row.serial}</TableCell>}
              {tags && <TableCell className="inventory-numeric">{row.assetTag}</TableCell>}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

export function DeviceConnections({ device, inventory, href }: { device: Device; inventory: Inventory; href: Href }) {
  const headingID = useId();
  const summaryID = useId();
  const join = useMemo(() => deviceConnections(device, inventory), [device, inventory]);
  const unmatched = useMemo(() => unmatchedLinks(device, inventory, join.links), [device, inventory, join]);
  const rows = useMemo(() => componentRows(device, inventory, join, unmatched), [device, inventory, join, unmatched]);
  const names = useMemo(() => viewNames([...join.links, ...unmatched]), [join, unmatched]);
  const tiles = useMemo(() => connectionGroups.flatMap(item => portTiles(join.links.filter(link => link.group === item.group))), [join]);
  // The faces come from faceplate(), which draws recorded components with
  // unknown states when Cables are denied.
  const plate = useMemo(() => faceplate(device, inventory), [device, inventory]);
  // One picture: the drawing when the model records a layout, otherwise the
  // grouped tiles labelled "Layout not recorded". Never both, never toggled.
  const drawing = useMemo(() => deviceDrawingModel(device, inventory), [device, inventory]);
  const drawn = drawing.layout.recorded;
  const cablesDenied = join.cableAccess === 'denied';
  const namespace = device.metadata.namespace;
  const title = deviceTitle(device);
  const [highlighted, setHighlighted] = useState<string>();
  const highlight = useMemo<ComponentHighlight>(() => ({ id: highlighted, set: setHighlighted, clear: id => setHighlighted(value => value === id ? undefined : value) }), [highlighted]);
  const telemetry = usePortTelemetry({ kind: 'Device', namespace: device.metadata.namespace, name: device.metadata.name, label: title }, { cablesDenied });

  const picture = drawn
    ? <FaceplateDrawing drawing={drawing} namespace={namespace} href={href} telemetry={telemetry} dense highlight={highlight} />
    : plate.faces.length ? <><LayoutNotRecorded /><PortFaceplate faces={plate.faces} namespace={namespace} href={href} names={names} cablesDenied={cablesDenied} collapseCatalogOnly={!cablesDenied} telemetry={telemetry} dense highlight={highlight} /></> : null;

  // The one count line. Unreadable Cables make every connection unknown, never zero.
  const summary = cablesDenied
    ? <>{rows.length} {rows.length === 1 ? 'component' : 'components'} · <span className="inventory-muted">Cable records are unavailable to you.</span></>
    : <>{portsWord(tiles.length)}{tiles.length > 0 && <> · <TileSummary tiles={tiles} /></>}{join.catalog === 'unavailable' && <span className="inventory-muted"> · catalog unavailable</span>}</>;

  return (
    <section className="inventory-connections" aria-labelledby={headingID}>
      <h4 id={headingID} className="inventory-visually-hidden">Components and connections</h4>
      {picture && <div className="inventory-device-picture">{picture}</div>}
      {rows.length ? (
        <div className="inventory-component-panel">
          <div className="inventory-component-bar">
            <p id={summaryID} className="inventory-connection-summary">{summary}</p>
          </div>
          <ComponentTable rows={rows} cablesDenied={cablesDenied} namespace={namespace} href={href} names={names} highlight={highlight} describedBy={summaryID} />
        </div>
      ) : <p className="inventory-muted">No typed components or Cable terminations are recorded for this Device.</p>}
    </section>
  );
}
