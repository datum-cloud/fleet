// Equipment depth, derived from records only. Nothing here is measured.

import type { Device } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import type { EquipmentPlacement } from './device-placement';
import { deviceCatalog } from './hardware-catalog';

export type DepthSource = 'reported' | 'device' | 'catalog' | 'unknown';

/**
 * `full` is what conflict checks use. Unknown depth is treated as full depth
 * so it can never open space on the far face.
 */
export interface Depth { full: boolean; source: DepthSource }

const UNKNOWN: Depth = { full: true, source: 'unknown' };

/**
 * Depth precedence: a Device value overrides its DeviceType. The Device's
 * placement report comes first, then the Device's recorded model shape, then
 * the DeviceType's model shape. Otherwise depth is unknown, including when
 * the DeviceType is missing or unreadable. Unset is never defaulted.
 */
export function deviceDepth(device: Device, inventory: Inventory): Depth {
  const reported = device.spec.reportedPlacement?.fullDepth;
  if (reported !== undefined) return { full: reported, source: 'reported' };
  const recorded = device.spec.modelShape?.fullDepth;
  if (recorded !== undefined) return { full: recorded, source: 'device' };
  const catalog = deviceCatalog(device, inventory);
  const nominal = catalog?.model.spec.modelShape?.fullDepth;
  if (nominal !== undefined) return { full: nominal, source: 'catalog' };
  return UNKNOWN;
}

/**
 * A claim-derived fallback borrows depth from its registered Devices only
 * when they all agree. Otherwise, or with no registration, it is unknown.
 */
export function equipmentDepth(entry: EquipmentPlacement, inventory: Inventory): Depth {
  if (entry.kind === 'Device') return deviceDepth(entry.record as Device, inventory);
  const depths = entry.devices.map(device => deviceDepth(device, inventory));
  if (!depths.length || depths.some(depth => depth.source === 'unknown' || depth.full !== depths[0].full)) return UNKNOWN;
  return depths[0];
}

export function depthLabel(depth: Depth): string {
  if (depth.source === 'unknown') return 'Depth unknown';
  const word = depth.full ? 'Full depth' : 'Half depth';
  return depth.source === 'reported' ? `${word} (reported)` : depth.source === 'device' ? `${word} (recorded on Device)` : `${word} (catalog)`;
}
