// A Device's faceplate: its port tiles laid out by chassis face, then by type,
// using the same join and layout as the connection view (portLayout), so the
// rack panel and the connection view never disagree. Nothing here is
// observed: there is no link, power or health status on any inventory kind.
//
// Face: the chassis face comes from the DeviceType template with the same id
// (componentTemplates[].face). A component with no readable template face is
// placed under Face unknown rather than guessed from its type.

import type { Device } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { componentChassisFace, componentRackFace, deviceMountedFace } from './rack-faces';
import { deviceConnections, groupOf, portLayout, UNCABLED_TYPES, type ConnectionLink, type DeviceConnections, type LayoutFace } from './device-connections';

export interface Faceplate {
  cableAccess: DeviceConnections['cableAccess'];
  faces: LayoutFace[];
  /** The readable links; empty when Cables are denied. */
  links: ConnectionLink[];
}

export function faceplate(device: Device, inventory: Inventory): Faceplate {
  const connections = deviceConnections(device, inventory);
  if (connections.cableAccess === 'readable') return { cableAccess: 'readable', faces: portLayout(connections.links), links: connections.links };
  // Cables are denied: draw the recorded components with every state
  // unknown. The view marks them as cable details unavailable, never as
  // "no cable recorded".
  const mounted = deviceMountedFace(device, inventory);
  const placeholders: ConnectionLink[] = (device.spec.components ?? []).filter(component => !UNCABLED_TYPES.has(component.type)).map(component => {
    const chassisFace = componentChassisFace(device, inventory, component.id);
    return {
      key: component.id, group: groupOf[component.type], state: 'unavailable', componentID: component.id, component,
      position: 1, positions: component.positions ?? 1, chassisFace, rackFace: componentRackFace(mounted, chassisFace), trace: [], reasons: [], checks: [],
    };
  });
  return { cableAccess: 'denied', faces: portLayout(placeholders), links: [] };
}
