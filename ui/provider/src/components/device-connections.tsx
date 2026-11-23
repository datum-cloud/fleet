import { Button } from '@datum-cloud/datum-ui/button';
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { Link } from 'react-router';
import type { Device } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import {
  connectionGroups, deviceConnections, deviceTitle, DIAGRAM_LINK_LIMIT, faceGroups, faceLabel, linkStateWords, localLabel, portLayout, portTiles, remotePortLabel, viewNames,
  type ConnectionGroup, type ConnectionLink, type PortTile, type ViewNames,
} from '../lib/device-connections';
import {
  cableText, countsText, Glyph, linkLabel, linksWord, LocalDetail, peerDetail, PeerTarget, peerText, portsWord, STATES, StateText, TILE_GLYPH, tileSummary, Trace, type Href,
} from './connection-parts';
import { PortFaceplate } from './device-faceplate';
import { usePortTelemetry } from './power-telemetry';
import { deviceDrawingModel, FaceplateDrawing, LayoutNotRecorded } from './faceplate-drawing';

type View = 'faceplate' | 'ports' | 'diagram' | 'list';
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

const VIEW_WORDS: Record<View, string> = { faceplate: 'Faceplate', ports: 'Ports', diagram: 'Diagram', list: 'List' };

export function DeviceConnections({ device, inventory, href }: { device: Device; inventory: Inventory; href: Href }) {
  const headingID = useId();
  const join = useMemo(() => deviceConnections(device, inventory), [device, inventory]);
  const names = useMemo(() => viewNames(join.links), [join]);
  const groups = useMemo<Group[]>(() => connectionGroups
    .map(item => { const links = join.links.filter(link => link.group === item.group); return { ...item, links, tiles: portTiles(links) }; })
    .filter(item => item.links.length), [join]);
  const faces = useMemo(() => portLayout(join.links), [join]);
  const diagramFits = join.links.length <= DIAGRAM_LINK_LIMIT;
  // The drawing leads when the model records a layout; otherwise grouped tiles.
  const drawing = useMemo(() => deviceDrawingModel(device, inventory), [device, inventory]);
  const drawn = drawing.layout.recorded;
  const [chosen, setView] = useState<View>(drawn ? 'faceplate' : 'ports');
  const view = (chosen === 'diagram' && !diagramFits) || (chosen === 'faceplate' && !drawn) ? 'ports' : chosen;
  const namespace = device.metadata.namespace;
  const tiles = groups.flatMap(group => group.tiles);
  const title = deviceTitle(device);
  const summary = `${portsWord(tiles.length)} · ${tileSummary(tiles)}`;
  const views: View[] = [...(drawn ? ['faceplate' as const] : []), 'ports', ...(diagramFits ? ['diagram' as const] : []), 'list'];
  const [legend, setLegend] = useState(false);
  const legendID = useId();
  const telemetry = usePortTelemetry({ kind: 'Device', namespace: device.metadata.namespace, name: device.metadata.name, label: title }, { cablesDenied: join.cableAccess === 'denied' });

  let body: ReactNode;
  if (join.cableAccess === 'denied') body = <p className="inventory-note">Cable records are unavailable to you, so this Device&rsquo;s connections are unknown. This does not mean its ports are free.</p>;
  else if (!join.links.length) body = <p className="inventory-muted">No typed components or Cable terminations are recorded for this Device, so there is nothing to join.</p>;
  else if (view === 'faceplate') body = <FaceplateDrawing drawing={drawing} namespace={namespace} href={href} telemetry={telemetry} />;
  else if (view === 'ports') body = <>{!drawn && <LayoutNotRecorded />}<PortFaceplate faces={faces} namespace={namespace} href={href} names={names} collapseCatalogOnly telemetry={telemetry} /></>;
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
