import { Popover, PopoverContent, PopoverTrigger } from '@datum-cloud/datum-ui/popover';
import { Link } from 'react-router';
import type { Device } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { linkStateGlyph, linkStateWords, localLabel, peerLabel, type LinkState } from '../lib/device-connections';
import { faceplate, type FaceplateTile } from '../lib/device-faceplate';
import { condensedName } from '../lib/display-names';
import { inventoryHref } from './inventory-location';

const TYPE_WORDS: Record<string, string> = {
  NetworkPort: 'Network port', PowerInlet: 'Power inlet', PowerOutlet: 'Power outlet', ConsolePort: 'Console port',
  ConsoleServerPort: 'Console server port', FrontPort: 'Front pass-through port', RearPort: 'Rear pass-through port',
};

function StateWords({ state }: { state: LinkState }) {
  return <span data-state={state} className="inventory-faceplate-state"><span aria-hidden="true">{linkStateGlyph[state]}</span> {linkStateWords[state]}</span>;
}

/**
 * The pop-up card for one component: what is recorded about it and its
 * Cables. Health is never inferred: no inventory kind reports it.
 */
function ComponentCard({ tile, device, cablesDenied }: { tile: FaceplateTile; device: Device; cablesDenied: boolean }) {
  const namespace = device.metadata.namespace;
  return <div className="inventory-component-card-body">
    <p className="inventory-component-card-title">{tile.name}</p>
    <dl>
      <div><dt>Type</dt><dd>{(tile.type && TYPE_WORDS[tile.type]) ?? tile.type ?? 'Not recorded'} · {tile.connector ?? 'Connector not recorded'}</dd></div>
      {tile.rackFace && <div><dt>Lands on rack face</dt><dd>{tile.rackFace}</dd></div>}
      {tile.positions > 1 && <div><dt>Positions</dt><dd>{tile.positions}</dd></div>}
      {!tile.component && <div><dt>Record</dt><dd>Catalog template only; this Device does not record it</dd></div>}
      {tile.component && (tile.component.serial || tile.component.assetTag) && <div><dt>Identity</dt><dd>{[tile.component.serial, tile.component.assetTag].filter(Boolean).join(' · ')}</dd></div>}
    </dl>
    <section aria-label="Cables">
      <p className="inventory-component-card-heading">Cables</p>
      {cablesDenied ? <p>Cable details unavailable. Connections are unknown.</p> : <ul>
        {tile.links.map(link => <li key={link.key}>
          {tile.positions > 1 && <span>{localLabel(link)}: </span>}
          <StateWords state={link.state} />
          {link.cable && <> → {peerLabel(link.peer)} <small>via <Link to={inventoryHref('Cable', namespace, link.cable.name)}>{link.cable.label}</Link>{link.cable.laneCount > 1 ? ` · lane ${link.cable.laneID} of ${link.cable.laneCount}` : ''}</small></>}
          {link.state === 'none' && <small>Unknown, not free.</small>}
          {link.reasons.length > 0 && <small>{link.reasons.join(' ')}</small>}
          {link.checks.length > 0 && <small>Check: {link.checks.join(' ')}</small>}
        </li>)}
        {!tile.links.length && <li><StateWords state="none" /> <small>Unknown, not free.</small></li>}
      </ul>}
    </section>
    <section aria-label="Health">
      <p className="inventory-component-card-heading">Health</p>
      <p>No health reported</p>
    </section>
  </div>;
}

function Tile({ tile, device, cablesDenied }: { tile: FaceplateTile; device: Device; cablesDenied: boolean }) {
  const short = condensedName(tile.name);
  const state = linkStateWords[tile.state];
  return <Popover>
    <PopoverTrigger asChild>
      <button type="button" className="inventory-faceplate-tile" data-state={tile.state} aria-label={`${tile.name}, ${(tile.type && TYPE_WORDS[tile.type]) ?? 'component'}, ${cablesDenied ? 'cable details unavailable' : state}`} title={tile.name}>
        <span aria-hidden="true" className="inventory-faceplate-glyph">{linkStateGlyph[tile.state]}</span>
        <span aria-hidden="true" className="inventory-faceplate-name">{short}</span>
      </button>
    </PopoverTrigger>
    <PopoverContent className="inventory-component-card" aria-label={`${tile.name} details`} data-component-card="">
      <ComponentCard tile={tile} device={device} cablesDenied={cablesDenied} />
    </PopoverContent>
  </Popover>;
}

/**
 * A compact faceplate of a Device's recorded components: clustered by face,
 * then by type. Each tile opens a pop-up card. Faces are recorded only for
 * pass-through ports; everything else sits under "Face not recorded".
 */
export function DeviceFaceplate({ device, inventory }: { device: Device; inventory: Inventory }) {
  const plate = faceplate(device, inventory);
  const cablesDenied = plate.cableAccess === 'denied';
  // When no component records a face, one caption says so instead of two empty faces.
  const sided = plate.faces.some(face => face.face !== 'unrecorded' && face.clusters.length);
  const faces = sided ? plate.faces : plate.faces.filter(face => face.face === 'unrecorded').map(face => ({ ...face, title: 'Component faces not recorded' }));
  return <section className="inventory-faceplate" aria-label="Faceplate">
    {faces.map(face => <div key={face.face} className="inventory-faceplate-face" data-face={face.face}>
      <p className="inventory-faceplate-face-title">{face.title}</p>
      {face.clusters.length ? face.clusters.map(cluster => <div key={cluster.group} className="inventory-faceplate-cluster" role="group" aria-label={`${face.title} · ${cluster.title}`}>
        <span className="inventory-faceplate-cluster-title">{cluster.title}</span>
        <div className="inventory-faceplate-tiles">{cluster.tiles.map(tile => <Tile key={tile.id} tile={tile} device={device} cablesDenied={cablesDenied} />)}</div>
      </div>) : <p className="inventory-muted">No component records this face.</p>}
    </div>)}
    {cablesDenied && <p className="inventory-muted">Cable details unavailable. Component states are unknown.</p>}
  </section>;
}
