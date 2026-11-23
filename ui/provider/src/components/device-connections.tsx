import { Button } from '@datum-cloud/datum-ui/button';
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router';
import type { ChassisFace, Device, PhysicalComponent, RackFace } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import {
  connectionGroups, deviceConnections, deviceTitle, DIAGRAM_LINK_LIMIT, faceGroups, faceLabel, groupOf, linkStateWords, localLabel, portLayout, portTiles, remotePortLabel, typeTitle, unmatchedLinks, viewNames,
  type ConnectionGroup, type ConnectionLink, type DeviceConnections as JoinResult, type LinkState, type PortTile, type ViewNames,
} from '../lib/device-connections';
import { componentChassisFace, componentRackFace, deviceMountedFace } from '../lib/rack-faces';
import {
  cableText, Glyph, linkLabel, linksWord, peerDetail, PeerTarget, peerText, portsWord, STATES, StateText, TILE_GLYPH, tileSummary, Trace, type Href,
} from './connection-parts';
import { PortFaceplate } from './device-faceplate';
import { usePortTelemetry } from './power-telemetry';
import { deviceDrawingModel, FaceplateDrawing, LayoutNotRecorded, moduleStateWords } from './faceplate-drawing';

/** The URL search parameter holding the connection view. Unset is the picture. */
export const CONNECTION_VIEW_PARAM = 'view';

type View = 'picture' | 'table' | 'diagram';
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
      state: tile?.state, links: tile?.links ?? [], serial: component.serial, assetTag: component.assetTag,
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

const positionPrefix = (row: ComponentRow, link: ConnectionLink) => row.unmatched || (row.positions <= 1 && link.position === 1) ? '' : `Position ${link.position} · `;
const unique = (items: string[]) => [...new Set(items)];

function CableCell({ row, namespace, href, names }: { row: ComponentRow; namespace?: string; href: Href; names: ViewNames }) {
  const cabled = row.links.filter(link => link.cable);
  const open = row.links.filter(link => !link.cable);
  if (!row.links.length) return null;
  return (
    <ul className="inventory-connection-cells">
      {cabled.map(link => <li key={link.key}>{positionPrefix(row, link)}<Link to={href('Cable', namespace, link.cable!.name)}>{names.cable(link.cable!)}</Link><small>{cableText(link)}</small></li>)}
      {open.length > 0 && <li className="inventory-muted">{cableText(open[0])}{row.positions > 1 && cabled.length > 0 ? ` at position ${open.map(link => link.position).join(', ')}` : ''}</li>}
    </ul>
  );
}

function PeerCell({ row, namespace, href, names }: { row: ComponentRow; namespace?: string; href: Href; names: ViewNames }) {
  const cabled = row.links.filter(link => link.cable);
  const none = row.links.find(link => link.state === 'none');
  return (
    <ul className="inventory-connection-cells">
      {cabled.map(link => (
        <li key={link.key}>
          {positionPrefix(row, link)}<PeerTarget peer={link.peer} namespace={namespace} href={href} names={names} />
          {peerDetail(link) && <small>{peerDetail(link)}</small>}
          <Trace hops={link.trace} namespace={namespace} href={href} names={names} />
        </li>
      ))}
      {none && <li className="inventory-muted">{peerDetail(none)}</li>}
    </ul>
  );
}

/**
 * The components, their record state, Cable and peer in one table, one row
 * per component. With Cables denied, only the components are listed: their
 * connections are unknown, and the section says so above the table.
 */
function ComponentTable({ rows, cablesDenied, namespace, href, names }: { rows: ComponentRow[]; cablesDenied: boolean; namespace?: string; href: Href; names: ViewNames }) {
  const serials = rows.some(row => row.serial);
  const tags = rows.some(row => row.assetTag);
  return (
    <div className="inventory-scroll inventory-connection-table" role="region" tabIndex={0} aria-label="Components table">
      <Table>
        <TableCaption>Components{cablesDenied ? '' : ' and Cables'}</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Type and connector</TableHead>
            <TableHead>Face and position</TableHead>
            {!cablesDenied && <><TableHead>Record state</TableHead><TableHead>Cable</TableHead><TableHead>Peer</TableHead></>}
            {serials && <TableHead>Serial</TableHead>}
            {tags && <TableHead>Asset tag</TableHead>}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map(row => (
            <TableRow key={row.key} data-state={row.state} data-unmatched={row.unmatched ? '' : undefined}>
              <TableCell>
                {row.name}
                {(row.unmatched || row.name !== row.id) && <small>{[row.name !== row.id && row.id, row.unmatched && `Position ${row.unmatched.position ?? 'not recorded'}`].filter(Boolean).join(' · ')}</small>}
              </TableCell>
              <TableCell>{row.type ? typeTitle(row.type, 1) : 'Not on this Device'}<small>{row.connector ?? 'Connector not recorded'}</small></TableCell>
              <TableCell>{faceLabel(row)}{row.positions > 1 && <small>{row.positions} positions</small>}</TableCell>
              {!cablesDenied && <>
                <TableCell>
                  {row.state ? <StateText state={row.state} /> : <span className="inventory-muted">{moduleStateWords.recorded}</span>}
                  {row.links.some(link => link.reasons.length) && <small>{unique(row.links.flatMap(link => link.reasons)).join(' ')}</small>}
                  {row.links.some(link => link.checks.length) && <small className="inventory-connection-check">{unique(row.links.flatMap(link => link.checks)).join(' ')}</small>}
                </TableCell>
                <TableCell><CableCell row={row} namespace={namespace} href={href} names={names} /></TableCell>
                <TableCell><PeerCell row={row} namespace={namespace} href={href} names={names} /></TableCell>
              </>}
              {serials && <TableCell>{row.serial}</TableCell>}
              {tags && <TableCell>{row.assetTag}</TableCell>}
            </TableRow>
          ))}
        </TableBody>
      </Table>
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

const VIEW_WORDS: Record<View, string> = { picture: 'Picture', table: 'Table', diagram: 'Diagram' };

export function DeviceConnections({ device, inventory, href }: { device: Device; inventory: Inventory; href: Href }) {
  const headingID = useId();
  const join = useMemo(() => deviceConnections(device, inventory), [device, inventory]);
  const unmatched = useMemo(() => unmatchedLinks(device, inventory, join.links), [device, inventory, join]);
  const rows = useMemo(() => componentRows(device, inventory, join, unmatched), [device, inventory, join, unmatched]);
  const names = useMemo(() => viewNames([...join.links, ...unmatched]), [join, unmatched]);
  const groups = useMemo<Group[]>(() => connectionGroups
    .map(item => { const links = join.links.filter(link => link.group === item.group); return { ...item, links, tiles: portTiles(links) }; })
    .filter(item => item.links.length), [join]);
  const faces = useMemo(() => portLayout(join.links), [join]);
  // One picture: the drawing when the model records a layout, otherwise the
  // grouped tiles labelled "Layout not recorded". Never both.
  const drawing = useMemo(() => deviceDrawingModel(device, inventory), [device, inventory]);
  const drawn = drawing.layout.recorded;
  const views: View[] = ['picture', 'table', ...(join.links.length <= DIAGRAM_LINK_LIMIT ? ['diagram' as const] : [])];
  // The view is held in the URL (?view=), like the record's tabs; a value
  // this Device does not offer falls back to the picture.
  const [params, setParams] = useSearchParams();
  const view = views.find(value => value === params.get(CONNECTION_VIEW_PARAM)) ?? 'picture';
  const setView = (value: View) => setParams(current => { const next = new URLSearchParams(current); if (value === 'picture') next.delete(CONNECTION_VIEW_PARAM); else next.set(CONNECTION_VIEW_PARAM, value); return next; }, { replace: true });
  const namespace = device.metadata.namespace;
  const tiles = groups.flatMap(group => group.tiles);
  const title = deviceTitle(device);
  const summary = `${portsWord(tiles.length)} · ${tileSummary(tiles)}`;
  const [legend, setLegend] = useState(false);
  const legendID = useId();
  const telemetry = usePortTelemetry({ kind: 'Device', namespace: device.metadata.namespace, name: device.metadata.name, label: title }, { cablesDenied: join.cableAccess === 'denied' });
  const table = <ComponentTable rows={rows} cablesDenied={join.cableAccess === 'denied'} namespace={namespace} href={href} names={names} />;
  const switchable = join.cableAccess === 'readable' && join.links.length > 0;

  let body: ReactNode;
  if (join.cableAccess === 'denied') body = <><p className="inventory-note">Cable records are unavailable to you, so this Device&rsquo;s connections are unknown. This does not mean its ports are free.</p>{rows.length > 0 && table}</>;
  else if (!rows.length) body = <p className="inventory-muted">No typed components or Cable terminations are recorded for this Device, so there is nothing to join.</p>;
  else if (!switchable || view === 'table') body = table;
  else if (view === 'picture') body = drawn
    ? <FaceplateDrawing drawing={drawing} namespace={namespace} href={href} telemetry={telemetry} />
    : <><LayoutNotRecorded /><PortFaceplate faces={faces} namespace={namespace} href={href} names={names} collapseCatalogOnly telemetry={telemetry} /></>;
  else body = <Diagram title={title} groups={groups} namespace={namespace} href={href} names={names} />;

  return (
    <section className="inventory-connections" aria-labelledby={headingID}>
      <div className="inventory-connections-heading">
        <div>
          <h4 id={headingID}>Connections</h4>
          <p className="inventory-muted">Cable records, not live link state</p>
        </div>
        {switchable && (
          <div className="inventory-face-switch" role="group" aria-label="Connection view">
            {views.map(value => (
              <Button key={value} type="secondary" theme={view === value ? 'light' : 'outline'} size="small" htmlType="button" aria-pressed={view === value} onClick={() => setView(value)}>
                {VIEW_WORDS[value]}
              </Button>
            ))}
          </div>
        )}
      </div>
      {switchable && (
        <p className="inventory-connection-summary">
          {summary}. <span className="inventory-muted">Unknown is not free; nothing here is verified.{join.catalog === 'unavailable' && ' Catalog unavailable, so catalog-only ports are not shown.'}</span>{' '}
          <button type="button" className="inventory-link-button" aria-expanded={legend} aria-controls={legendID} onClick={() => setLegend(value => !value)}>{legend ? 'Hide legend' : 'Legend'}</button>
        </p>
      )}
      {legend && switchable && <Legend id={legendID} />}
      {body}
    </section>
  );
}
