// The Device page's primary view: the device picture, then one dense table
// of its components. The picture is the drawn faceplate when the DeviceType
// records a layout, otherwise the grouped port tiles; it is never toggled.
// The link diagram is a secondary view that replaces the table, held in the
// URL as ?view=diagram. Hovering or focusing a tile outlines its row, and the
// row outlines its tile. See "Device page: picture and component table" in
// docs/development/staff-inventory-ui.md.
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { Link, useSearchParams } from 'react-router';
import type { ChassisFace, Device, PhysicalComponent, RackFace } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import {
  connectionGroups, deviceConnections, deviceTitle, DIAGRAM_LINK_LIMIT, faceGroups, faceLabel, groupOf, interfaceAddresses, linkStateWords, localLabel, portTiles, remotePortLabel, shortFaceLabel, unmatchedLinks, viewNames,
  type ConnectionGroup, type ConnectionLink, type DeviceConnections as JoinResult, type LinkState, type PortTile, type TraceHop, type ViewNames,
} from '../lib/device-connections';
import { faceplate } from '../lib/device-faceplate';
import { componentChassisFace, componentRackFace, deviceMountedFace } from '../lib/rack-faces';
import {
  cableText, Glyph, linkLabel, linksWord, peerDetail, PeerTarget, peerText, portsWord, STATES, StateText, TILE_GLYPH, TileSummary, tileSummary, Trace, type Href,
} from './connection-parts';
import { PortFaceplate, type ComponentHighlight } from './device-faceplate';
import { usePortTelemetry } from './power-telemetry';
import { deviceDrawingModel, FaceplateDrawing, LayoutNotRecorded, moduleStateGlyph, moduleStateWords } from './faceplate-drawing';

/**
 * The URL search parameter holding the secondary view. `diagram` swaps the
 * table for the link diagram; anything else, `table` included, is the
 * default page.
 */
export const CONNECTION_VIEW_PARAM = 'view';

type Group = { group: ConnectionGroup; title: string; links: ConnectionLink[]; tiles: PortTile[] };

const LEFT: ConnectionGroup[] = ['power'];

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

function Diagram({ title, groups, namespace, href, names }: { title: string; groups: Group[]; namespace?: string; href: Href; names: ViewNames }) {
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
      {/* The section header carries the counts; the drawn Device carries only its name. */}
      <div className="inventory-connection-device">
        <strong>{title}</strong>
      </div>
      {renderSide(false, 'Network, console and other peers')}
    </div>
  );
}

// ---- Table ----

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

/**
 * The legend popover: record states and the honesty notes the page states
 * once instead of on every row. Escape or a click outside closes it.
 */
function Legend({ id, drawn, power, catalogUnavailable, onClose }: { id: string; drawn: boolean; power: boolean; catalogUnavailable: boolean; onClose: () => void }) {
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const outside = (event: PointerEvent) => {
      const target = event.target as Node;
      if (!panel.current?.contains(target) && !(target instanceof Element && target.closest(`[aria-controls="${id}"]`))) onClose();
    };
    document.addEventListener('pointerdown', outside);
    return () => document.removeEventListener('pointerdown', outside);
  }, [id, onClose]);
  return (
    <div ref={panel} id={id} className="inventory-connection-legend-panel" role="group" aria-label="Legend">
      <ul className="inventory-connection-legend" aria-label="Record states">
        {STATES.map(state => <li key={state} data-state={state}><span className="inventory-port-swatch" data-state={state} aria-hidden="true">{TILE_GLYPH[state]}</span><StateText state={state} /></li>)}
      </ul>
      {drawn && <p className="inventory-muted">Drawn modules: {moduleStateGlyph.recorded} {moduleStateWords.recorded.toLowerCase()} · {moduleStateGlyph.catalog} {moduleStateWords.catalog.toLowerCase()}</p>}
      {catalogUnavailable && <p className="inventory-muted">Catalog unavailable, so catalog-only ports are not shown.</p>}
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
  const groups = useMemo<Group[]>(() => connectionGroups
    .map(item => { const links = join.links.filter(link => link.group === item.group); return { ...item, links, tiles: portTiles(links) }; })
    .filter(item => item.links.length), [join]);
  // The faces come from faceplate(), which draws recorded components with
  // unknown states when Cables are denied.
  const plate = useMemo(() => faceplate(device, inventory), [device, inventory]);
  // One picture: the drawing when the model records a layout, otherwise the
  // grouped tiles labelled "Layout not recorded". Never both, never toggled.
  const drawing = useMemo(() => deviceDrawingModel(device, inventory), [device, inventory]);
  const drawn = drawing.layout.recorded;
  const cablesDenied = join.cableAccess === 'denied';
  const offersDiagram = !cablesDenied && join.links.length > 0 && join.links.length <= DIAGRAM_LINK_LIMIT;
  // The diagram is held in the URL (?view=diagram), like the record's tabs;
  // any other value, `table` included, is the default page.
  const [params, setParams] = useSearchParams();
  const diagram = offersDiagram && params.get(CONNECTION_VIEW_PARAM) === 'diagram';
  const setDiagram = (value: boolean) => setParams(current => { const next = new URLSearchParams(current); if (value) next.set(CONNECTION_VIEW_PARAM, 'diagram'); else next.delete(CONNECTION_VIEW_PARAM); return next; }, { replace: true });
  const namespace = device.metadata.namespace;
  const tiles = groups.flatMap(group => group.tiles);
  const title = deviceTitle(device);
  const [legend, setLegend] = useState(false);
  const legendID = useId();
  const legendButton = useRef<HTMLButtonElement>(null);
  const [highlighted, setHighlighted] = useState<string>();
  const highlight = useMemo<ComponentHighlight>(() => ({ id: highlighted, set: setHighlighted, clear: id => setHighlighted(value => value === id ? undefined : value) }), [highlighted]);
  const telemetry = usePortTelemetry({ kind: 'Device', namespace: device.metadata.namespace, name: device.metadata.name, label: title }, { cablesDenied });
  const closeLegend = useMemo(() => () => setLegend(false), []);

  const picture = drawn
    ? <FaceplateDrawing drawing={drawing} namespace={namespace} href={href} telemetry={telemetry} dense highlight={highlight} />
    : plate.faces.length ? <><LayoutNotRecorded /><PortFaceplate faces={plate.faces} namespace={namespace} href={href} names={names} cablesDenied={cablesDenied} collapseCatalogOnly={!cablesDenied} telemetry={telemetry} dense highlight={highlight} /></> : null;

  // The one count line. Unreadable Cables make every connection unknown, never zero.
  const summary = cablesDenied
    ? <>{rows.length} {rows.length === 1 ? 'component' : 'components'} · <span className="inventory-muted">Cable records are unavailable to you.</span></>
    : <>{portsWord(tiles.length)}{tiles.length > 0 && <> · <TileSummary tiles={tiles} /></>}{join.catalog === 'unavailable' && <span className="inventory-muted"> · catalog unavailable</span>}</>;

  return (
    <section className="inventory-connections" aria-labelledby={headingID} onKeyDown={event => { if (legend && event.key === 'Escape') { event.stopPropagation(); setLegend(false); legendButton.current?.focus(); } }}>
      <h4 id={headingID} className="inventory-visually-hidden">Components and connections</h4>
      {picture && <div className="inventory-device-picture">{picture}</div>}
      {rows.length ? (
        <div className="inventory-component-panel">
          <div className="inventory-component-bar">
            <p id={summaryID} className="inventory-connection-summary">{summary}</p>
            <div className="inventory-component-actions">
              <button ref={legendButton} type="button" className="inventory-link-button" aria-expanded={legend} aria-controls={legendID} onClick={() => setLegend(value => !value)}>Legend</button>
              {offersDiagram && <button type="button" className="inventory-link-button" aria-pressed={diagram} onClick={() => setDiagram(!diagram)}>Link diagram</button>}
            </div>
            {legend && <Legend id={legendID} drawn={drawn} power={groups.some(group => group.group === 'power') || rows.some(row => row.type === 'PowerInlet')} catalogUnavailable={join.catalog === 'unavailable'} onClose={closeLegend} />}
          </div>
          {diagram
            ? <Diagram title={title} groups={groups} namespace={namespace} href={href} names={names} />
            : <ComponentTable rows={rows} cablesDenied={cablesDenied} namespace={namespace} href={href} names={names} highlight={highlight} describedBy={summaryID} />}
        </div>
      ) : <p className="inventory-muted">No typed components or Cable terminations are recorded for this Device.</p>}
    </section>
  );
}
