import { Button } from '@datum-cloud/datum-ui/button';
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { Link } from 'react-router';
import type { Device } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import {
  connectionGroups, deviceConnections, DIAGRAM_LINK_LIMIT, linkStateGlyph, linkStateWords, localLabel, peerLabel, remotePortLabel, stateCounts,
  type ConnectionGroup, type ConnectionLink, type LinkState, type PeerEnd, type TraceHop,
} from '../lib/device-connections';

type Href = (kind: string, namespace?: string, name?: string) => string;
type View = 'diagram' | 'list';

const STATES: LinkState[] = ['cabled', 'none', 'conflict', 'unavailable', 'catalog'];
const LEFT: ConnectionGroup[] = ['power'];

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

function linksWord(count: number) {
  return `${count} ${count === 1 ? 'link' : 'links'}`;
}

function cableText(link: ConnectionLink) {
  if (!link.cable) return link.state === 'catalog' ? 'Not recorded on this Device' : 'No cable recorded';
  const lane = link.cable.laneCount > 1 ? ` · lane ${link.cable.laneID} of ${link.cable.laneCount}` : '';
  return `${link.cable.medium ?? 'Medium not recorded'}${lane}`;
}

function PeerTarget({ peer, namespace, href, roving, label }: { peer?: PeerEnd; namespace?: string; href: Href; roving?: boolean; label?: string }) {
  if (!peer) return <span>Unknown</span>;
  if (peer.access !== 'readable') return <span>{peerLabel(peer)}</span>;
  return <Link to={href(peer.kind, namespace, peer.name)} data-connection-roving={roving ? '' : undefined} aria-label={label}>{peerLabel(peer)}</Link>;
}

function Trace({ hops, namespace, href, roving }: { hops: TraceHop[]; namespace?: string; href: Href; roving?: boolean }) {
  if (!hops.length) return null;
  return (
    <ol className="inventory-connection-trace" aria-label="Recorded path beyond the peer">
      {hops.map((hop, index) => (
        <li key={index}>
          <Glyph state={hop.state} /> via internal path to {hop.via.componentName ?? hop.via.componentID}{hop.via.position > 1 ? ` · position ${hop.via.position}` : ''}
          {hop.cable ? <>, then <Link to={href('Cable', namespace, hop.cable.name)} data-connection-roving={roving ? '' : undefined}>{hop.cable.label}</Link> to <PeerTarget peer={hop.peer} namespace={namespace} href={href} roving={roving} /></> : `: ${linkStateWords[hop.state].toLowerCase()}`}
          {hop.reasons.length > 0 && <small>{hop.reasons.join(' ')}</small>}
        </li>
      ))}
    </ol>
  );
}

function LocalDetail({ link }: { link: ConnectionLink }) {
  const type = link.component?.type ?? link.template?.type;
  const connector = link.component?.connector ?? link.template?.connector;
  return <small>{[type ?? 'Not on this Device', connector ?? 'Connector not recorded'].join(' · ')}{link.internal ? ` · internal path to ${link.internal.componentName ?? link.internal.componentID}${link.internal.position > 1 ? ` position ${link.internal.position}` : ''}` : ''}</small>;
}

function peerDetail(link: ConnectionLink) {
  if (link.state === 'none') return 'Unknown · not free';
  if (link.state === 'catalog') return 'Not recorded';
  if (!link.peer) return undefined;
  if (link.peer.access === 'denied') return 'You cannot read this peer';
  if (link.peer.access === 'missing') return `${link.peer.kind} record not found`;
  return link.peer.supply ? `Power feed · recorded rating ${link.peer.supply}` : link.peer.kind === 'PowerFeed' ? 'Power feed' : 'Device';
}

function linkLabel(link: ConnectionLink) {
  const cable = link.cable ? ` via cable ${link.cable.label}, ${cableText(link)}` : '';
  return `${localLabel(link)} to ${link.peer ? peerLabel(link.peer) : 'unknown peer'}${cable}: ${linkStateWords[link.state]}`;
}

function DiagramRow({ link, namespace, href }: { link: ConnectionLink; namespace?: string; href: Href }) {
  return (
    <li className="inventory-connection-row" data-state={link.state}>
      <div className="inventory-connection-line">
        <span className="inventory-connection-ports">
          <span className="inventory-connection-local">{localLabel(link)}</span>
          <span className="inventory-connection-remote">{link.cable ? remotePortLabel(link.peer) : ''}</span>
        </span>
        <span className="inventory-connection-wire" aria-hidden="true" />
        <span className="inventory-connection-cable">
          <Glyph state={link.state} />{' '}
          {link.cable
            ? <Link to={href('Cable', namespace, link.cable.name)} data-connection-roving="" aria-label={linkLabel(link)}>{link.cable.label}</Link>
            : <span>{linkStateWords[link.state]}</span>}
          {link.cable && <> · {cableText(link)} · {linkStateWords[link.state].toLowerCase()}</>}
        </span>
      </div>
      <div className="inventory-connection-peer">
        {link.cable ? <PeerTarget peer={link.peer} namespace={namespace} href={href} roving label={link.peer?.access === 'readable' ? `Peer ${peerLabel(link.peer)}` : undefined} /> : <span>{link.state === 'catalog' ? 'Catalog only' : 'Unknown'}</span>}
        <small>{peerDetail(link)}</small>
        <Trace hops={link.trace} namespace={namespace} href={href} roving />
        {link.reasons.length > 0 && <small className="inventory-connection-reason">{link.reasons.join(' ')}</small>}
      </div>
    </li>
  );
}

function DiagramGroup({ group, title, links, collapsed, namespace, href, onToggle }: { group: ConnectionGroup; title: string; links: ConnectionLink[]; collapsed: boolean; namespace?: string; href: Href; onToggle: () => void }) {
  const rows = <ul className="inventory-connection-rows" aria-label={`${title} links`}>{links.map(link => <DiagramRow key={link.key} link={link} namespace={namespace} href={href} />)}</ul>;
  const summary = `${title} · ${linksWord(links.length)}`;
  return (
    <li className="inventory-connection-group" data-group={group}>
      {collapsed ? (
        <details className="inventory-connection-collapse" onToggle={onToggle}>
          <summary>{summary} <span className="inventory-count">{countsText(links)}</span></summary>
          {rows}
        </details>
      ) : (
        <>
          <p className="inventory-connection-group-title">{summary} <span className="inventory-count">{countsText(links)}</span></p>
          {rows}
        </>
      )}
      {group === 'power' && <p className="inventory-connection-group-note">Power redundancy unknown. Separate inlets or feeds do not prove independent supply.</p>}
    </li>
  );
}

function Diagram({ device, groups, collapsed, namespace, href }: { device: Device; groups: { group: ConnectionGroup; title: string; links: ConnectionLink[] }[]; collapsed: boolean; namespace?: string; href: Href }) {
  const container = useRef<HTMLDivElement>(null);
  const active = useRef<Element | null>(null);
  const [, setRevision] = useState(0);
  const visible = () => [...(container.current?.querySelectorAll<HTMLElement>('[data-connection-roving]') ?? [])].filter(element => !element.closest('details:not([open])'));
  // One tab stop for the whole drawing; arrow keys move between its links.
  useLayoutEffect(() => {
    const items = visible();
    const current = items.find(item => item === active.current) ?? items[0];
    for (const item of items) item.tabIndex = item === current ? 0 : -1;
    for (const summary of container.current?.querySelectorAll<HTMLElement>('details:not([open]) [data-connection-roving]') ?? []) summary.tabIndex = -1;
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
  const all = groups.flatMap(item => item.links);
  const renderSide = (left: boolean, label: string) => {
    const items = side(left);
    return items.length ? (
      <ul className="inventory-connection-side" data-side={left ? 'left' : 'right'} aria-label={label}>
        {items.map(item => <DiagramGroup key={item.group} {...item} collapsed={collapsed} namespace={namespace} href={href} onToggle={() => setRevision(value => value + 1)} />)}
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
        <strong>{device.spec.displayName || device.metadata.name}</strong>
        <small>{device.metadata.name}</small>
        <small>{linksWord(all.length)} · {countsText(all)}</small>
      </div>
      {renderSide(false, 'Network, console and other peers')}
    </div>
  );
}

function LinkTable({ groups, namespace, href }: { groups: { group: ConnectionGroup; title: string; links: ConnectionLink[] }[]; namespace?: string; href: Href }) {
  return (
    <div className="inventory-connection-tables">
      {groups.map(({ group, title, links }) => (
        <div className="inventory-scroll" key={group} role="region" tabIndex={0} aria-label={`${title} links table`}>
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
                  <TableCell><StateText state={link.state} />{link.reasons.length > 0 && <small>{link.reasons.join(' ')}</small>}</TableCell>
                  <TableCell>{link.cable ? <><Link to={href('Cable', namespace, link.cable.name)}>{link.cable.label}</Link><small>{cableText(link)}</small></> : <span className="inventory-muted">{cableText(link)}</span>}</TableCell>
                  <TableCell>
                    {link.cable ? <PeerTarget peer={link.peer} namespace={namespace} href={href} /> : <span className="inventory-muted">{link.state === 'catalog' ? 'Catalog only' : 'Unknown'}</span>}
                    <small>{peerDetail(link)}</small>
                    <Trace hops={link.trace} namespace={namespace} href={href} />
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

function Legend() {
  return (
    <ul className="inventory-connection-legend" aria-label="Record states">
      {STATES.map(state => <li key={state} data-state={state}><span className="inventory-connection-swatch" aria-hidden="true" /><StateText state={state} /></li>)}
    </ul>
  );
}

export function DeviceConnections({ device, inventory, href }: { device: Device; inventory: Inventory; href: Href }) {
  const headingID = useId();
  const join = useMemo(() => deviceConnections(device, inventory), [device, inventory]);
  const large = join.links.length > DIAGRAM_LINK_LIMIT;
  const [view, setView] = useState<View>(large ? 'list' : 'diagram');
  const namespace = device.metadata.namespace;
  const groups = connectionGroups.map(item => ({ ...item, links: join.links.filter(link => link.group === item.group) })).filter(item => item.links.length);

  let body: ReactNode;
  if (join.cableAccess === 'denied') body = <p className="inventory-note">Cable records are unavailable to you, so this Device&rsquo;s connections are unknown. This does not mean its ports are free.</p>;
  else if (!join.links.length) body = <p className="inventory-muted">No typed components or Cable terminations are recorded for this Device, so there is nothing to join.</p>;
  else body = view === 'diagram'
    ? <Diagram device={device} groups={groups} collapsed={large} namespace={namespace} href={href} />
    : <LinkTable groups={groups} namespace={namespace} href={href} />;

  return (
    <section className="inventory-connections" aria-labelledby={headingID}>
      <div className="inventory-connections-heading">
        <div>
          <h4 id={headingID}>Connections</h4>
          <p className="inventory-muted">Cable records, not live link state</p>
        </div>
        {join.links.length > 0 && (
          <div className="inventory-face-switch" role="group" aria-label="Connection view">
            {(['diagram', 'list'] as const).map(value => (
              <Button key={value} type="secondary" theme={view === value ? 'light' : 'outline'} size="small" htmlType="button" aria-pressed={view === value} onClick={() => setView(value)}>
                {value === 'diagram' ? 'Diagram' : 'List'}
              </Button>
            ))}
          </div>
        )}
      </div>
      {join.cableAccess === 'readable' && join.links.length > 0 && <Legend />}
      <p className="inventory-muted inventory-connection-caveat">
        No cable recorded means unknown, not free. Nothing here is verified.
        {join.catalog === 'unavailable' && ' The hardware catalog is unavailable, so catalog-only components are not shown.'}
      </p>
      {large && join.cableAccess === 'readable' && (
        <p className="inventory-note">
          {linksWord(join.links.length)} is too many to draw clearly, so the list is shown first and the diagram is collapsed by group.
        </p>
      )}
      {body}
    </section>
  );
}
