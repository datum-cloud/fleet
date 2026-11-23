import { useMemo } from 'react';
import { Link } from 'react-router';
import type { Device } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import type { EquipmentPlacement } from '../lib/device-placement';
import { detailLevel, FACE_WIDTH_MM, U_MM } from '../lib/faceplate-layout';
import { countsWords, deviceDrawingModel, MiniFaceplate, miniFaceplate } from './faceplate-drawing';

/** The one Device a slot draws: the reported Device, or a claim's only registration. */
function slotDevice(slot: EquipmentPlacement): Device | undefined {
  if (slot.kind === 'Device') return slot.record as Device;
  return slot.devices.length === 1 ? slot.devices[0] : undefined;
}

/**
 * What the elevation draws inside a recorded unit span. This is the single
 * place a device-in-slot is rendered. With a readable Device it draws a mini
 * faceplate of the visible chassis face (front on the mounted face, rear on
 * the far face) at a level of detail chosen from pixels per unit: silhouette,
 * reduced or full. A model without a layout is a plain chassis labelled
 * "layout not recorded". The drawing is visual only; the link is one tab stop
 * and its name carries the "!" and "?" counts in words.
 */
export function RackSlotDevice({ slot, name, range, height, label, side, overlap, far, depthUnknown, to, selected, inventory, pxPerUnit = 16 }: {
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
  /** Needed for the mini faceplate; without it the slot is a labelled bar. */
  inventory?: Inventory;
  pxPerUnit?: number;
}) {
  const device = slotDevice(slot);
  const level = detailLevel(pxPerUnit);
  const mini = useMemo(() => {
    if (!device || !inventory) return undefined;
    const drawing = deviceDrawingModel(device, inventory);
    return { drawing, ...miniFaceplate(drawing, far ? 'Rear' : 'Front') };
  }, [device, inventory, far]);
  const counts = mini?.counts;
  const countsText = counts ? countsWords(counts) : '';
  const recorded = mini?.drawing.layout.recorded;
  const accessible = [`Review ${label}, ${side}`, countsText, mini && !recorded ? 'layout not recorded' : undefined].filter(Boolean).join(', ');
  const detail = [height > 1 ? `${range} · ${height}U` : undefined, depthUnknown ? 'depth unknown' : undefined].filter(Boolean).join(' · ');
  return <Link title={label} to={to} aria-label={accessible} aria-current={selected ? 'true' : undefined} data-slot-key={slot.key} data-level={mini ? level : undefined} className={mini ? 'inventory-rack-slot-device' : undefined}>
    <span className="inventory-rack-plan-text">
      <span className="inventory-rack-plan-heading">
        {overlap && <span className="inventory-rack-overlap" aria-hidden="true">!</span>}
        <span className="inventory-rack-plan-name">{far ? <span className="inventory-rack-plan-side">rear of </span> : null}{name}</span>
        {counts && (counts.conflict > 0 || counts.unavailable > 0) && (
          <span className="inventory-rack-counts" aria-hidden="true">
            {counts.conflict > 0 && <span data-state="conflict">! {counts.conflict}</span>}
            {counts.unavailable > 0 && <span data-state="unavailable">? {counts.unavailable}</span>}
          </span>
        )}
      </span>
      {detail && <small>{detail}</small>}
    </span>
    {mini && (
      <MiniFaceplate
        geometry={mini.geometry}
        states={mini.states}
        level={level}
        widthMM={mini.geometry?.widthMM ?? FACE_WIDTH_MM}
        heightMM={mini.geometry?.heightMM ?? Math.max(1, height) * U_MM}
      />
    )}
  </Link>;
}
