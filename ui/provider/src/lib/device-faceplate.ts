// Groups a Device's components into faceplate tiles: by chassis face, then by
// component type. Built on deviceConnections, so tile state is the same
// recorded Cable state the connection view uses. Nothing here is observed:
// there is no link, power or health status on any inventory kind.
//
// Face: the chassis face comes from the DeviceType template with the same id
// (componentTemplates[].face). A component with no readable template face is
// placed under "Face not recorded" rather than guessed from its type.

import type { ChassisFace, Device, PhysicalComponent, RackFace } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { componentChassisFace, componentRackFace, deviceMountedFace } from './rack-faces';
import { connectionGroups, deviceConnections, type ConnectionGroup, type ConnectionLink, type DeviceConnections, type LinkState } from './device-connections';

export type ChassisSide = 'front' | 'rear' | 'unrecorded';
export const chassisFaces: { face: ChassisSide; title: string }[] = [
  { face: 'front', title: 'Front' },
  { face: 'rear', title: 'Rear' },
  { face: 'unrecorded', title: 'Face not recorded' },
];

function sideOf(chassis: ChassisFace | undefined): ChassisSide {
  return chassis === 'Front' ? 'front' : chassis === 'Rear' ? 'rear' : 'unrecorded';
}

export interface FaceplateTile {
  /** Component ID: one tile per component, however many positions it has. */
  id: string;
  name: string;
  type?: PhysicalComponent['type'];
  connector?: string;
  positions: number;
  component?: PhysicalComponent;
  /** Every link for this component, one or more per position. */
  links: ConnectionLink[];
  /** The most serious state across positions. */
  state: LinkState;
  /** Chassis face from the DeviceType template; unset is unknown. */
  chassisFace?: ChassisFace;
  /** The rack face it lands on, from the mounted face; unset is unknown. */
  rackFace?: RackFace;
}

export interface FaceplateCluster { group: ConnectionGroup; title: string; tiles: FaceplateTile[] }
export interface Faceplate {
  cableAccess: DeviceConnections['cableAccess'];
  faces: { face: ChassisSide; title: string; clusters: FaceplateCluster[] }[];
  links: ConnectionLink[];
}

// Most serious first, so one disagreeing lane on a breakout marks its tile.
const SEVERITY: LinkState[] = ['conflict', 'unavailable', 'cabled', 'none', 'catalog'];

export function faceplate(device: Device, inventory: Inventory): Faceplate {
  const connections = deviceConnections(device, inventory);
  const tiles = new Map<string, FaceplateTile>();
  const mounted = deviceMountedFace(device, inventory);
  const facesOf = (id: string) => { const chassisFace = componentChassisFace(device, inventory, id); return { chassisFace, rackFace: componentRackFace(mounted, chassisFace) }; };
  // Components the Device records, in record order, even when Cables are
  // denied (their state is then unknown, never "no cable").
  for (const component of device.spec.components ?? []) {
    if (component.type === 'Module' || component.type === 'InventoryItem' || component.type === 'DeviceBay') continue;
    tiles.set(component.id, { id: component.id, name: component.name, type: component.type, connector: component.connector, positions: component.positions ?? 1, component, links: [], state: connections.cableAccess === 'denied' ? 'unavailable' : 'none', ...facesOf(component.id) });
  }
  for (const link of connections.links) {
    const existing = tiles.get(link.componentID);
    const tile = existing ?? { id: link.componentID, name: link.component?.name ?? link.template?.name ?? link.componentID, type: link.component?.type ?? link.template?.type, connector: link.component?.connector ?? link.template?.connector, positions: link.positions, component: link.component, links: [], state: link.state, chassisFace: link.chassisFace, rackFace: link.rackFace };
    tile.links.push(link);
    tile.state = SEVERITY.find(state => tile.links.some(item => item.state === state)) ?? tile.state;
    tiles.set(link.componentID, tile);
  }
  const groupOf = (tile: FaceplateTile): ConnectionGroup => tile.links[0]?.group ?? (tile.type === 'PowerInlet' || tile.type === 'PowerOutlet' ? 'power' : tile.type === 'NetworkPort' ? 'network' : tile.type === 'FrontPort' || tile.type === 'RearPort' ? 'passthrough' : tile.type === 'ConsolePort' || tile.type === 'ConsoleServerPort' ? 'console' : 'other');
  const faces = chassisFaces.map(({ face, title }) => ({
    face, title,
    clusters: connectionGroups.map(({ group, title: groupTitle }) => ({ group, title: groupTitle, tiles: [...tiles.values()].filter(tile => sideOf(tile.chassisFace) === face && groupOf(tile) === group) })).filter(cluster => cluster.tiles.length),
  })).filter(face => face.face !== 'unrecorded' || face.clusters.length);
  return { cableAccess: connections.cableAccess, faces, links: connections.links };
}
