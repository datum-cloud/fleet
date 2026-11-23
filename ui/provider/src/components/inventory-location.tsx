import { Button } from '@datum-cloud/datum-ui/button';
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@datum-cloud/datum-ui/table';
import { Title } from '@datum-cloud/datum-ui/typography';
import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router';
import { slotKey, type Inventory, type InventorySlot } from '../inventory';
import type { PhysicalRecord, RackFace as RackFaceName } from '../../../shared/fleet-api';
import { depthLabel } from '../lib/device-depth';
import { detailLevel, type DetailLevel } from '../lib/faceplate-layout';
import { emptyRuns, faceOccupancy, RACK_FACES, rackOccupants, runRange, type EmptyRun, type RackOccupant } from '../lib/rack-faces';
import { equipmentGeometry, equipmentPlacements, reportedPlacementLabel, type EquipmentPlacement } from '../lib/device-placement';
import { RackSlotDevice } from './rack-slot-device';

/** How the elevation links a device-in-slot: to its record, or to a selection on the rack page. */
export interface RackSelection { href: (slot: EquipmentPlacement) => string; selectedKey?: string }

export function inventoryHref(kind: string, namespace?: string, name?: string) {
  const params = new URLSearchParams({ kind });
  if (name !== undefined) { params.set('record', name); params.set('namespace', namespace ?? ''); }
  return `?${params}`;
}

export function rackGeometry(slot: InventorySlot, rack: PhysicalRecord) {
  return equipmentGeometry({ ...slot, kind: 'DeviceClaim', record: slot.claim, placement: slot.claim.spec.placement, basis: 'Claim-derived fallback' }, rack);
}

function Equipment({ slots, devicesDenied, inventory }: { slots: EquipmentPlacement[]; devicesDenied: boolean; inventory: Inventory }) {
  return <div className="inventory-scroll"><Table aria-label="Recorded equipment"><TableHeader><TableRow><TableHead>Equipment</TableHead><TableHead>Claim / registration history</TableHead><TableHead>Location / Reported placement</TableHead></TableRow></TableHeader><TableBody>
    {slots.map(slot => <TableRow key={slot.key}><TableCell>{devicesDenied ? 'Device details unavailable' : slot.devices.length ? slot.devices.map(device => <div key={device.metadata.name}><Link to={inventoryHref('Device', device.metadata.namespace, device.metadata.name)}>{device.spec.displayName || device.metadata.name}</Link></div>) : 'No device registration recorded'}{slot.devices.length > 1 && <small>Multiple registrations · active device unknown</small>}</TableCell><TableCell>{slot.claim ? <Link to={inventoryHref('DeviceClaim', slot.claim.metadata.namespace, slot.claim.metadata.name)}>{slot.claim.spec.displayName || slot.claim.metadata.name}</Link> : 'Claim details unavailable'}</TableCell><TableCell>{reportedPlacementLabel(slot, inventory)}</TableCell></TableRow>)}
  </TableBody></Table></div>;
}

/** A collapsed run's row height. CSS raises it to a 44 px touch target on coarse pointers. */
const RUN_ROW = 'var(--inventory-run-row, 24px)';

/** "16px 16px 24px 0px 0px" → "repeat(2, 16px) 24px repeat(2, 0px)". */
function rowTemplate(sizes: string[]) {
  const parts: string[] = [];
  for (let index = 0; index < sizes.length;) {
    let end = index;
    while (end + 1 < sizes.length && sizes[end + 1] === sizes[index]) end++;
    const count = end - index + 1;
    parts.push(count > 1 ? `repeat(${count}, ${sizes[index]})` : sizes[index]);
    index = end + 1;
  }
  return parts.join(' ');
}

/** Enter toggles a run explicitly, so it never depends on the browser turning Enter into a click. */
function enter(event: KeyboardEvent<HTMLButtonElement>, action: () => void) {
  if (event.key !== 'Enter' || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  event.preventDefault();
  action();
}

/** What a run says about its units, on the face it is drawn on. */
function runWords(run: EmptyRun) {
  return `${runRange(run)} · ${run.units} units · no placement recorded (not free)`;
}

/** Collapsed and expanded empty runs, shared by both faces so their rows stay aligned. */
export interface RunState {
  runs: EmptyRun[];
  /** True when the runs are empty on both faces ("Front + rear"). */
  aligned: boolean;
  expanded: ReadonlySet<string>;
  toggle: (run: EmptyRun, side: RackFaceName) => void;
}

/**
 * One rack face. Grid rows stay one per unit (row = height − unit + 1), so a
 * placement's row never moves. A collapsed run gives its top unit's row a
 * compact height and the rest zero, and draws one row in their place.
 * Entries are in visual order, top to bottom, so arrow keys follow the
 * drawing across collapsed rows.
 */
function RackFace({ occupants, height, side, selection, inventory, unit, runs }: { occupants: RackOccupant[]; height: number; side: RackFaceName; selection?: RackSelection; inventory: Inventory; unit: number; runs: RunState }) {
  const plans = faceOccupancy(occupants, side)
    .sort((first, second) => first.position - second.position || first.entry.key.localeCompare(second.entry.key));
  const laneEnds: number[] = [];
  const placed = plans.map(plan => {
    let lane = laneEnds.findIndex(end => end < plan.position);
    if (lane === -1) lane = laneEnds.length;
    laneEnds[lane] = plan.position + plan.height - 1;
    return { ...plan, lane };
  });
  const collapsed = runs.runs.filter(run => !runs.expanded.has(run.key));
  const expandedRuns = runs.runs.filter(run => runs.expanded.has(run.key));
  const collapsedAt = (value: number) => collapsed.find(run => value <= run.top && value >= run.bottom);
  const sizes = Array.from({ length: height }, (_, index) => {
    const run = collapsedAt(height - index);
    return !run ? `${unit}px` : run.top === height - index ? RUN_ROW : '0px';
  });
  const scope = runs.aligned ? 'on either face' : `on the ${side} face`;
  const entries: ReactNode[] = [];
  for (let value = height; value >= 1; value--) {
    const run = collapsedAt(value);
    if (run) {
      if (run.top !== value) continue;
      entries.push(<li key={`run-${run.key}`} className="inventory-rack-run" style={{ gridRow: `${height - run.top + 1} / span ${run.units}`, gridColumn: '1 / -1' }}>
        <button type="button" className="inventory-rack-run-button" data-elevation-entry="" data-run={run.key} aria-expanded={false}
          aria-label={`${runWords(run)}, ${scope}. Show each unit`} title={`${runWords(run)} ${scope}`} onClick={() => runs.toggle(run, side)} onKeyDown={event => enter(event, () => runs.toggle(run, side))}>
          <span aria-hidden="true">{runWords(run)}</span>
        </button>
      </li>);
      continue;
    }
    const recorded = plans.some(plan => value >= plan.position && value < plan.position + plan.height);
    const opened = expandedRuns.find(item => item.top === value);
    entries.push(<li key={value} className="inventory-rack-unit" style={{ gridRow: height - value + 1, gridColumn: '1 / -1' }}>
      <span>U{value}</span>{!recorded && <span className="inventory-muted" aria-label="Occupancy unverified">—</span>}
      {opened && <button type="button" className="inventory-rack-run-collapse" data-elevation-entry="" data-run={opened.key} aria-expanded aria-label={`Collapse ${runRange(opened)}, ${opened.units} units with no placement recorded ${scope}`} onClick={() => runs.toggle(opened, side)} onKeyDown={event => enter(event, () => runs.toggle(opened, side))}>Collapse {runRange(opened)}</button>}
    </li>);
    for (const plan of placed) if (plan.position + plan.height - 1 === value) entries.push(planEntry(plan));
  }

  function planEntry(plan: (typeof placed)[number]) {
        const top = plan.position + plan.height - 1;
        const overlap = plan.conflicts.length > 0;
        const record = plan.entry.record;
        const recordName = record.spec.displayName || record.metadata.name;
        // The far face of a full-depth (or unknown-depth) device shows its
        // chassis rear, whichever rack face it is mounted on.
        const name = plan.far ? `Rear of ${recordName}` : recordName;
        const range = plan.height === 1 ? `U${plan.position}` : `U${plan.position}–U${top}`;
        const label = [name, range, `${plan.height}U`, plan.entry.basis, plan.far ? `mounted ${plan.mounted}` : undefined, depthLabel(plan.depth), overlap ? 'records disagree: overlapping placements on this face' : undefined].filter(Boolean).join(' · ');
        const selected = selection?.selectedKey === plan.entry.key;
        return <li key={plan.entry.key} className={`inventory-rack-plan${plan.far ? ' inventory-rack-plan-far' : ''}`} data-far={plan.far ? '' : undefined} data-depth={plan.depth.source === 'unknown' ? 'unknown' : plan.depth.full ? 'full' : 'half'} data-selected={selected ? '' : undefined} style={{ gridRow: `${height - top + 1} / span ${plan.height}`, gridColumn: plan.lane + 2 }}>
          <RackSlotDevice slot={plan.entry} name={recordName} range={range} height={plan.height} label={label} side={side} overlap={overlap} far={plan.far} depthUnknown={plan.depth.source === 'unknown'} to={selection ? selection.href(plan.entry) : inventoryHref(plan.entry.kind, record.metadata.namespace, record.metadata.name)} selected={selected} inventory={inventory} pxPerUnit={unit} />
        </li>;
  }

  return <div><h4>{side === 'front' ? 'Front' : 'Rear'}</h4>
    <ol className="inventory-rack-units" aria-label={`${side} recorded placements`} data-detail={detailLevel(unit)} data-face={side} style={{ gridTemplateRows: rowTemplate(sizes), gridTemplateColumns: `28px repeat(${Math.max(1, laneEnds.length)}, minmax(0, 1fr))` }}>
      {entries}
    </ol>
  </div>;
}

/** The rack's recorded placements within this rack, the elevation's input. */
export function rackPlacements(rack: PhysicalRecord, inventory: Inventory) {
  return equipmentPlacements(inventory).filter(slot => slot.record.metadata.namespace === rack.metadata.namespace && slot.placement.rackRef?.name === rack.metadata.name);
}

/**
 * The rack elevation. The rack page wraps it: selection, tabs and issue
 * banners live in rack-detail.tsx. `toolbar` sits beside the face switch,
 * where a later 2D/3D view toggle belongs.
 */
/** Pixels per rack unit for each level of detail the elevation offers. */
export const UNIT_PX: Record<DetailLevel, number> = { silhouette: 12, reduced: 16, full: 44 };
const DETAIL_WORDS: Record<DetailLevel, string> = { silhouette: 'Outline', reduced: 'Ports', full: 'Full' };

export function RackOverview({ rack, inventory, selection, toolbar }: { rack: PhysicalRecord; inventory: Inventory; selection?: RackSelection; toolbar?: ReactNode }) {
  const [face, setFace] = useState<'both' | 'front' | 'rear'>('both');
  // The level of detail is held in the URL (?detail=), so a zoomed rack can be linked.
  const [params, setParams] = useSearchParams();
  const requested = params.get('detail');
  const detail: DetailLevel = requested === 'silhouette' || requested === 'full' ? requested : 'reduced';
  const setDetail = (value: DetailLevel) => setParams(current => { const next = new URLSearchParams(current); if (value === 'reduced') next.delete('detail'); else next.set('detail', value); return next; }, { replace: true });
  const slots = rackPlacements(rack, inventory);
  const positioned = slots.filter(slot => equipmentGeometry(slot, rack));
  const occupants = rackOccupants(positioned, rack, inventory);
  const denied = !!inventory.issues?.DeviceClaim && !slots.length;
  const height = rack.spec.heightUnits;
  // "Front + rear" collapses only units empty on both faces, so both columns
  // share one row template; a single face collapses its own empty units.
  const sides = face === 'both' ? RACK_FACES : [face];
  const runs = height ? emptyRuns(occupants, height, sides) : [];
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  const faces = useRef<HTMLDivElement>(null);
  // After a run expands or collapses, focus moves to its replacement control on the same face.
  const refocus = useRef<{ key: string; side: RackFaceName } | undefined>(undefined);
  useEffect(() => {
    const target = refocus.current;
    if (!target) return;
    refocus.current = undefined;
    faces.current?.querySelector<HTMLElement>(`ol[data-face="${target.side}"] [data-run="${target.key}"]`)?.focus();
  }, [expanded]);
  const runState: RunState = {
    runs, aligned: sides.length > 1, expanded,
    toggle: (run, side) => {
      refocus.current = { key: run.key, side };
      setExpanded(current => { const next = new Set(current); if (next.has(run.key)) next.delete(run.key); else next.add(run.key); return next; });
    },
  };
  return <section className="inventory-rack-layout" aria-label="Rack layout">
    <aside className="inventory-elevation">
      <div className="inventory-list-heading"><Title as="h3" level={5}>Rack elevation</Title><span>{height ? `${height}U` : 'Height unknown'}</span></div>
      <div className="inventory-elevation-toolbar"><div className="inventory-face-switch" role="group" aria-label="Rack faces">{(['both', 'front', 'rear'] as const).map(value => <Button type="secondary" theme={face === value ? 'light' : 'outline'} size="small" key={value} htmlType="button" aria-pressed={face === value} onClick={() => setFace(value)}>{value === 'both' ? 'Front + rear' : value === 'front' ? 'Front' : 'Rear'}</Button>)}</div><div className="inventory-face-switch" role="group" aria-label="Elevation detail">{(['silhouette', 'reduced', 'full'] as const).map(value => <Button type="secondary" theme={detail === value ? 'light' : 'outline'} size="small" key={value} htmlType="button" aria-pressed={detail === value} onClick={() => setDetail(value)}>{DETAIL_WORDS[value]}</Button>)}</div>{toolbar}</div>
      <p className="inventory-muted">Reported, not verified. Blank units are not free space; runs of three or more blank units collapse into one row{face === 'both' ? ' where both faces are blank' : ''}. Full-depth equipment shows on both faces, muted where its chassis rear shows; half-depth only on its mounted face. Unknown depth is drawn as full depth. ! means records disagree: placements overlap on the same face. Beside a name, ! and ? count ports whose records disagree or whose peer is unavailable. Faceplates are drawn from each model's recorded layout.</p>
      {denied ? <p>Installation details unavailable. Rack positions cannot be shown.</p> : !height ? <p>Rack height is not recorded. Equipment remains listed without invented slots.</p> : <div ref={faces} className="inventory-rack-faces">{(['front', 'rear'] as const).filter(value => face === 'both' || face === value).map(side => <RackFace key={side} occupants={occupants} height={height} side={side} selection={selection} inventory={inventory} unit={UNIT_PX[detail]} runs={runState} />)}</div>}
    </aside>
  </section>;
}

const facilityRacks = (facility: PhysicalRecord, inventory: Inventory) => inventory.physical.filter(record => record.kind === 'Rack' && record.metadata.namespace === facility.metadata.namespace && record.spec.facilityRef?.name === facility.metadata.name);
const facilityOnly = (facility: PhysicalRecord, inventory: Inventory) => equipmentPlacements(inventory).filter(slot => slot.record.metadata.namespace === facility.metadata.namespace && !slot.placement.rackRef && slot.placement.facilityRef?.name === facility.metadata.name);

/** A Facility's primary view: its racks. Free space is stated once, as unknown. */
export function FacilityRacks({ facility, inventory }: { facility: PhysicalRecord; inventory: Inventory }) {
  const racks = facilityRacks(facility, inventory);
  const equipment = equipmentPlacements(inventory);
  const incomplete = !!inventory.issues?.DeviceClaim || !!inventory.issues?.Device;
  return <section className="inventory-equipment-sections" aria-label="Facility contents">
    <Title as="h3" level={5}>Racks <span className="inventory-count">{inventory.issues?.Rack ? 'unknown' : racks.length}</span></Title>
    {inventory.issues?.Rack ? <p>Rack details unavailable. The facility's rack count is unknown.</p> : racks.length ? <div className="inventory-scroll inventory-fit"><Table aria-label="Facility racks"><TableHeader><TableRow><TableHead>Rack</TableHead><TableHead>Height</TableHead><TableHead>Equipment records</TableHead></TableRow></TableHeader><TableBody>{racks.map(rack => <TableRow key={slotKey(rack.metadata.namespace, rack.metadata.name)}><TableCell><Link to={inventoryHref('Rack', rack.metadata.namespace, rack.metadata.name)}>{rack.spec.displayName || rack.metadata.name}</Link></TableCell><TableCell>{rack.spec.heightUnits ? `${rack.spec.heightUnits}U` : 'Not recorded'}</TableCell><TableCell>{incomplete ? 'Incomplete' : equipment.filter(slot => slot.record.metadata.namespace === rack.metadata.namespace && slot.placement.rackRef?.name === rack.metadata.name).length}</TableCell></TableRow>)}</TableBody></Table></div> : <p className="inventory-muted">No racks recorded in this facility.</p>}
    {racks.length > 0 && <p className="inventory-legend">Equipment counts are reports and claim fallback, not verified occupancy. Available space is not verified: unknown, not free.</p>}
  </section>;
}

/** Equipment recorded at the facility without a rack. */
export function FacilityUnrackedEquipment({ facility, inventory }: { facility: PhysicalRecord; inventory: Inventory }) {
  const slots = facilityOnly(facility, inventory);
  return slots.length ? <Equipment slots={slots} devicesDenied={!!inventory.issues?.Device} inventory={inventory} /> : inventory.issues?.DeviceClaim || inventory.issues?.Device ? <p>Equipment details unavailable.</p> : <p className="inventory-muted">No facility-only reports or claims recorded.</p>;
}

/** Facility-only equipment count, for the tab label. */
export function unrackedCount(facility: PhysicalRecord, inventory: Inventory) {
  return facilityOnly(facility, inventory).length;
}
