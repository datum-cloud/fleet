import { useMemo } from 'react';
import { Link } from 'react-router';
import type { Device } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import type { EquipmentPlacement } from '../lib/device-placement';
import { countsWords, deviceDrawingModel, portProblemCounts } from './faceplate-drawing';

/** The one Device a slot draws: the reported Device, or a claim's only registration. */
function slotDevice(slot: EquipmentPlacement): Device | undefined {
  if (slot.kind === 'Device') return slot.record as Device;
  return slot.devices.length === 1 ? slot.devices[0] : undefined;
}

/**
 * What the elevation draws inside a recorded unit span: the device's name
 * only; the drawing shows its units, and its range, height and depth are the
 * tooltip and accessible name. This is the single
 * place a device-in-slot is rendered, and it is one tab stop. A slot that
 * needs checking is colour coded instead of marked: red when records
 * disagree (overlapping placements, or ports more than one Cable names),
 * amber when a connection reaches a missing or unreadable record. The
 * reason is the tooltip and part of the link's accessible name.
 */
export function RackSlotDevice({ slot, name, label, side, overlap, far, to, selected, inventory }: {
  slot: EquipmentPlacement;
  /** The record's display name; `far` adds the "rear of" prefix. */
  name: string;
  /** Name, range, height, basis and depth: the tooltip and accessible name. */
  label: string;
  side: 'front' | 'rear';
  overlap: boolean;
  /** Drawn on the face opposite the mounted face: the chassis rear. */
  far: boolean;
  to: string;
  selected?: boolean;
  /** Needed for the port problem counts; without it only overlap is coloured. */
  inventory?: Inventory;
}) {
  const device = slotDevice(slot);
  const counts = useMemo(() => device && inventory ? portProblemCounts(deviceDrawingModel(device, inventory)) : undefined, [device, inventory]);
  const countsText = counts ? countsWords(counts) : '';
  const accessible = [`Review ${label}, ${side}`, countsText].filter(Boolean).join(', ');
  const attention = overlap || (counts?.conflict ?? 0) > 0 ? 'conflict' : (counts?.unavailable ?? 0) > 0 ? 'unavailable' : undefined;
  const title = [label, countsText].filter(Boolean).join(' · ');
  return <Link title={title} to={to} aria-label={accessible} aria-current={selected ? 'true' : undefined} data-slot-key={slot.key} data-attention={attention}>
    <span className="inventory-rack-plan-text">
      <span className="inventory-rack-plan-heading">
        <span className="inventory-rack-plan-name">{far ? <span className="inventory-rack-plan-side">rear of </span> : null}{name}</span>
      </span>
    </span>
  </Link>;
}
