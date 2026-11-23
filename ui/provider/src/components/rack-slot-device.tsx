import { Link } from 'react-router';
import type { EquipmentPlacement } from '../lib/device-placement';

/**
 * What the elevation draws inside a recorded unit span: today a labelled
 * bar. This is the single place a device-in-slot is rendered, so a mini
 * faceplate can replace the bar once DeviceTypes record a layout.
 */
export function RackSlotDevice({ slot, name, range, height, label, side, overlap, far, depthUnknown, to, selected }: {
  slot: EquipmentPlacement;
  /** The record's display name; `far` adds the "rear of" prefix. */
  name: string;
  range: string;
  height: number;
  label: string;
  side: 'front' | 'rear';
  overlap: boolean;
  /** Drawn on the face opposite the mounted face: the chassis rear. */
  far: boolean;
  depthUnknown: boolean;
  to: string;
  selected?: boolean;
}) {
  const detail = [height > 1 ? `${range} · ${height}U` : undefined, depthUnknown ? 'depth unknown' : undefined].filter(Boolean).join(' · ');
  return <Link title={label} to={to} aria-label={`Review ${label}, ${side}`} aria-current={selected ? 'true' : undefined} data-slot-key={slot.key}>
    <span className="inventory-rack-plan-heading">{overlap && <span className="inventory-rack-overlap" aria-hidden="true">!</span>}<span className="inventory-rack-plan-name">{far ? <span className="inventory-rack-plan-side">rear of </span> : null}{name}</span></span>
    {detail && <small>{detail}</small>}
  </Link>;
}
