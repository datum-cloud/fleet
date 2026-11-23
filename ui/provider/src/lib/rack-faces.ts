// Rack faces, depth and face-aware occupancy, derived from records only.
//
// A placement's `face` is the mounted rack face: the rack face the chassis
// front points toward. A half-depth device sits on that face's rails and
// occupies only that face. A full-depth device occupies its units on both
// faces. A component template's `face` is relative to the chassis, so its
// rack face is the mounted face for Front and the opposite face for Rear.
//
// Placement is reported, never verified. Overlaps are shown as records that
// disagree, never blocked, and unknown depth is never shown as clearance.

import type { ChassisFace, Device, PhysicalRecord, RackFace } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { deviceCatalog } from './hardware-catalog';
import { equipmentDepth, type Depth } from './device-depth';
import { devicePlacement, equipmentGeometry, type EquipmentPlacement } from './device-placement';

export const RACK_FACES: readonly RackFace[] = ['front', 'rear'];

export function oppositeFace(face: RackFace): RackFace {
  return face === 'front' ? 'rear' : 'front';
}

/**
 * The rack face a component lands on. Unknown when either the mounted face
 * or the component's chassis face is unknown.
 */
export function componentRackFace(mounted: RackFace | undefined, chassis: ChassisFace | undefined): RackFace | undefined {
  if (!mounted || !chassis) return undefined;
  return chassis === 'Front' ? mounted : oppositeFace(mounted);
}

export interface RackOccupant {
  entry: EquipmentPlacement;
  position: number;
  height: number;
  mounted: RackFace;
  depth: Depth;
  faces: RackFace[];
}

/** Occupants with whole, in-range geometry. Everything else is not drawn. */
export function rackOccupants(entries: EquipmentPlacement[], rack: PhysicalRecord, inventory: Inventory): RackOccupant[] {
  const occupants: RackOccupant[] = [];
  for (const entry of entries) {
    const geometry = equipmentGeometry(entry, rack);
    if (!geometry) continue;
    const mounted = geometry.face as RackFace;
    const depth = equipmentDepth(entry, inventory);
    occupants.push({ entry, position: geometry.position, height: geometry.height, mounted, depth, faces: depth.full ? [...RACK_FACES] : [mounted] });
  }
  return occupants;
}

export interface FaceOccupant extends RackOccupant {
  side: RackFace;
  /** True on the face opposite the mounted face: the chassis rear. */
  far: boolean;
  /** Keys of other occupants whose units overlap on this face. */
  conflicts: string[];
}

const overlaps = (a: RackOccupant, b: RackOccupant) => a.position < b.position + b.height && b.position < a.position + a.height;

/** What one rack face shows, with same-face overlaps as records disagree. */
export function faceOccupancy(occupants: RackOccupant[], side: RackFace): FaceOccupant[] {
  const here = occupants.filter(occupant => occupant.faces.includes(side));
  return here.map(occupant => ({
    ...occupant,
    side,
    far: occupant.mounted !== side,
    conflicts: here.filter(other => other.entry.key !== occupant.entry.key && overlaps(occupant, other)).map(other => other.entry.key),
  }));
}

/** The mounted rack face of a Device, from the same placement the elevation draws. */
export function deviceMountedFace(device: Device, inventory: Inventory): RackFace | undefined {
  const face = devicePlacement(device, inventory)?.placement.face;
  return face === 'front' || face === 'rear' ? face : undefined;
}

/**
 * The chassis face of a Device component, inherited from the DeviceType
 * template with the same id. Unknown without a readable catalog.
 */
export function componentChassisFace(device: Device, inventory: Inventory, componentID: string): ChassisFace | undefined {
  return deviceCatalog(device, inventory)?.model.spec.componentTemplates?.find(template => template.id === componentID)?.face;
}

// ---- Empty runs in the elevation ----

/** Runs of at least this many units with no recorded placement collapse into one row. */
export const MIN_EMPTY_RUN = 3;

/**
 * Consecutive units with no recorded placement. Blank is never free: a run
 * only says that no record places anything there.
 */
export interface EmptyRun {
  /** The highest unit in the run. */
  top: number;
  /** The lowest unit in the run. */
  bottom: number;
  units: number;
  /** Stable for the same span, so expansion survives re-renders. */
  key: string;
}

/** Units that a recorded placement covers on any of `faces`. */
export function coveredUnits(occupants: RackOccupant[], faces: readonly RackFace[]): Set<number> {
  const covered = new Set<number>();
  for (const side of faces) {
    for (const occupant of faceOccupancy(occupants, side)) {
      for (let unit = occupant.position; unit < occupant.position + occupant.height; unit++) covered.add(unit);
    }
  }
  return covered;
}

/**
 * Runs of `min` or more units, top first, with no recorded placement on any
 * of `faces`. Pass one face for a single-face view. Pass both faces for
 * "Front + rear": a unit then counts as empty only when it is empty on both
 * faces, so the two columns collapse the same units and their rows stay
 * aligned. Full-depth and unknown-depth equipment occupy both faces.
 */
export function emptyRuns(occupants: RackOccupant[], height: number, faces: readonly RackFace[], min = MIN_EMPTY_RUN): EmptyRun[] {
  const covered = coveredUnits(occupants, faces);
  const runs: EmptyRun[] = [];
  let top: number | undefined;
  for (let unit = height; unit >= 0; unit--) {
    const empty = unit >= 1 && !covered.has(unit);
    if (empty) { top ??= unit; continue; }
    if (top === undefined) continue;
    const bottom = unit + 1;
    const units = top - bottom + 1;
    if (units >= min) runs.push({ top, bottom, units, key: `U${bottom}-U${top}` });
    top = undefined;
  }
  return runs;
}

/** "U22–U39", lowest unit first. */
export const runRange = (run: EmptyRun) => `U${run.bottom}–U${run.top}`;
