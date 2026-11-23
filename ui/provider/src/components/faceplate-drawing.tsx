// The drawn faceplate: a Device's front and rear faces generated from its
// DeviceType's recorded layout, at full detail on Device detail and the rack
// panel, and as mini faceplates in the rack elevation.
//
// Ports are the shared PortTileButton, with the shared tooltip and PortCard,
// through usePortGrid, so the drawing and the grouped tiles never drift. A
// model without a layout gets the grouped tiles, labelled "Layout not
// recorded"; nothing is invented. Positions are nominal catalog data and
// states are Cable records: nothing here is observed.

import { useMemo, type CSSProperties } from 'react';
import type { ChassisFace, Device, PhysicalComponent, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { connectorKind } from '../lib/connector-kind';
import { shortLabels, viewNames, type LinkState, type PortTile, type ViewNames } from '../lib/device-connections';
import { faceplate } from '../lib/device-faceplate';
import { deviceTray, edgeNumbers, modelLayout, MODULE_TYPES, portNumber, type DetailLevel, type FaceGeometry, type LayoutBank, type ModelLayout, type TrayEntry } from '../lib/faceplate-layout';
import { deviceCatalog } from '../lib/hardware-catalog';
import { componentRackFace, deviceMountedFace } from '../lib/rack-faces';
import { TILE_GLYPH, type Href } from './connection-parts';
import { PortFaceplate, usePortGrid, type PortTelemetry } from './device-faceplate';
import { inventoryHref } from './inventory-location';
import { usePortTelemetry } from './power-telemetry';

/** Drawing scale. 450 mm is about 855 px, so an RJ45 cell stays about 25 px wide. */
export const PX_PER_MM = 1.9;
const px = (mm: number) => `${Math.round(mm * PX_PER_MM * 10) / 10}px`;

/** A module cell (drive bay, fan, accessory port) has no Cable state: it is recorded or catalog only. */
export type ModuleState = 'recorded' | 'catalog';
export const moduleStateWords: Record<ModuleState, string> = { recorded: 'Component recorded', catalog: 'Catalog only' };
export const moduleStateGlyph: Record<ModuleState, string> = { recorded: '■', catalog: '◇' };

const TYPE_WORDS: Partial<Record<PhysicalComponent['type'], string>> = { Module: 'module', InventoryItem: 'accessory', DeviceBay: 'device bay' };

/** What one Device's drawing needs, joined once. */
export interface DeviceDrawingModel {
  layout: ModelLayout;
  model?: PhysicalRecord;
  /** Port tiles by component id. */
  tiles: Map<string, PortTile>;
  /** Component ids the Device records. */
  recorded: Set<string>;
  tray: TrayEntry[];
  /** "Front · rack rear" per chassis face. */
  titles: Record<ChassisFace, string>;
  names: ViewNames;
  cablesDenied: boolean;
}

export function deviceDrawingModel(device: Device, inventory: Inventory): DeviceDrawingModel {
  const plate = faceplate(device, inventory);
  const model = deviceCatalog(device, inventory)?.model;
  const layout = modelLayout(model);
  const tiles = new Map(plate.faces.flatMap(face => face.clusters.flatMap(cluster => cluster.tiles)).map(tile => [tile.componentID, tile]));
  const mounted = deviceMountedFace(device, inventory);
  const title = (face: ChassisFace) => { const rack = componentRackFace(mounted, face); return `${face} · ${rack ? `rack ${rack}` : 'rack face unknown'}`; };
  return {
    layout, model, tiles,
    recorded: new Set((device.spec.components ?? []).map(component => component.id)),
    tray: deviceTray(layout, model, device.spec.components),
    titles: { Front: title('Front'), Rear: title('Rear') },
    names: viewNames(plate.links),
    cablesDenied: plate.cableAccess === 'denied',
  };
}

function ModuleCell({ name, type, state, label }: { name: string; type?: PhysicalComponent['type']; state: ModuleState; label?: string }) {
  return (
    <span className="inventory-chassis-module" role="img" data-state={state} aria-label={`${name}, ${(type && TYPE_WORDS[type]) ?? 'component'}, ${moduleStateWords[state]}`}>
      <span aria-hidden="true">{label ?? ''}{state === 'catalog' ? <span className="inventory-chassis-module-glyph">{moduleStateGlyph.catalog}</span> : null}</span>
    </span>
  );
}

function DrawnFace({ geometry, drawing, title, tileButton }: { geometry: FaceGeometry; drawing: DeviceDrawingModel; title: string; tileButton: ReturnType<typeof usePortGrid>['tileButton'] }) {
  return (
    <div className="inventory-drawn-face" role="group" aria-label={`${title} face`} data-face={geometry.face}>
      <p className="inventory-port-face-title">{title}</p>
      {geometry.warnings.map(warning => <p key={warning} className="inventory-faceplate-warning"><span aria-hidden="true">! </span>{warning}</p>)}
      <div className="inventory-chassis-scroll">
        <div className="inventory-chassis" style={{ width: px(geometry.widthMM), height: px(geometry.heightMM) }} data-strip={drawing.layout.strip ? '' : undefined}>
          {geometry.banks.map(bank => <Bank key={bank.id} bank={bank} drawing={drawing} tileButton={tileButton} />)}
        </div>
      </div>
    </div>
  );
}

function Bank({ bank, drawing, tileButton }: { bank: LayoutBank; drawing: DeviceDrawingModel; tileButton: ReturnType<typeof usePortGrid>['tileButton'] }) {
  // Row-major DOM order, so Left and Right move along a drawn row.
  const cells = [...bank.cells].sort((a, b) => a.row - b.row || a.column - b.column);
  const labels = useMemo(() => shortLabels(cells.flatMap(cell => { const tile = drawing.tiles.get(cell.template.id); return tile ? [tile] : []; })), [cells, drawing.tiles]);
  const shortOf = (id: string) => drawing.tiles.get(id)?.key;
  const numbers = bank.dense ? edgeNumbers(bank) : [];
  return (
    <>
      <span className="inventory-chassis-bank" aria-hidden="true" style={{ left: px(bank.x), top: px(bank.y), width: px(bank.w), height: px(bank.h) }} />
      {cells.map(cell => {
        const tile = MODULE_TYPES.has(cell.template.type) ? undefined : drawing.tiles.get(cell.template.id);
        const key = shortOf(cell.template.id);
        const label = bank.dense ? undefined : (key && labels.get(key)) ?? (cell.template.name.length <= 8 ? cell.template.name : cell.template.id.slice(0, 8));
        // Small connector cells print their label above (top row) or below (bottom row); module cells carry it inside.
        const printed = label && tile && !bank.module ? (cell.row === 1 ? 'above' : cell.row === bank.rows ? 'below' : undefined) : undefined;
        const style: CSSProperties = { left: px(cell.x), top: px(cell.y), width: px(cell.w), height: px(cell.h) };
        return (
          <div key={cell.template.id} className="inventory-chassis-cell" data-kind={connectorKind(cell.template.connector)} data-module={bank.module ? '' : undefined} style={style}>
            {tile
              ? tileButton(tile, { label: bank.module ? label : undefined, className: 'inventory-chassis-port' })
              : <ModuleCell name={cell.template.name} type={cell.template.type} state={drawing.recorded.has(cell.template.id) ? 'recorded' : 'catalog'} label={label} />}
            {printed && <span className="inventory-chassis-print" data-where={printed} aria-hidden="true">{label}</span>}
          </div>
        );
      })}
      {numbers.map(({ cell, where }) => (
        <span key={`${cell.template.id}-${where}`} className="inventory-chassis-number" data-where={where} aria-hidden="true" style={{ left: px(cell.x), top: px(where === 'above' ? cell.y : cell.y + cell.h), width: px(cell.w) }}>{portNumber(cell.template)}</span>
      ))}
    </>
  );
}

function Tray({ drawing, tileButton }: { drawing: DeviceDrawingModel; tileButton: ReturnType<typeof usePortGrid>['tileButton'] }) {
  if (!drawing.tray.length) return null;
  // The tray is not dense: each entry shows its full name beside an unlabelled tile.
  return (
    <div className="inventory-faceplate-tray" role="group" aria-label="Not on the faceplate">
      <p className="inventory-port-cluster-title">Not on the faceplate · {drawing.tray.length}</p>
      <ul className="inventory-faceplate-tray-list">
        {drawing.tray.map(entry => {
          const tile = MODULE_TYPES.has(entry.type ?? 'Module') ? undefined : drawing.tiles.get(entry.id);
          return (
            <li key={entry.id} data-reason={entry.reason}>
              {tile ? tileButton(tile) : <ModuleCell name={entry.name} type={entry.type} state={drawing.recorded.has(entry.id) ? 'recorded' : 'catalog'} />}
              <span className="inventory-faceplate-tray-name" aria-hidden="true">{entry.name}</span>
              <small>{entry.detail}</small>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/**
 * The full-detail faceplate: each recorded face drawn from the layout, then
 * the "Not on the faceplate" tray. One tab stop for every port; arrows move
 * between drawn ports and the tray; Enter opens the shared card.
 */
export function FaceplateDrawing({ drawing, namespace, href, telemetry }: { drawing: DeviceDrawingModel; namespace?: string; href: Href; telemetry?: (tile: PortTile) => PortTelemetry | undefined }) {
  // Every port the drawing shows, faces first, then the tray, in DOM order.
  const tiles = useMemo(() => {
    const drawn = drawing.layout.faces.flatMap(face => face.banks.flatMap(bank => [...bank.cells].sort((a, b) => a.row - b.row || a.column - b.column).map(cell => cell.template)))
      .filter(template => !MODULE_TYPES.has(template.type)).map(template => template.id);
    const ids = [...drawn, ...drawing.tray.map(entry => entry.id)];
    return ids.flatMap(id => { const tile = drawing.tiles.get(id); return tile ? [tile] : []; });
  }, [drawing]);
  const grid = usePortGrid({ tiles, names: drawing.names, namespace, href, cablesDenied: drawing.cablesDenied, telemetry });
  const notes = [
    drawing.layout.widthAssumed ? 'Width not recorded; drawn full width' : undefined,
    drawing.layout.strip ? 'Zero-U: drawn as a one-unit strip; length not recorded' : undefined,
  ].filter(Boolean);
  return (
    <div
      ref={grid.container}
      className="inventory-port-grid inventory-drawing"
      role="group"
      aria-label="Faceplate drawing. Arrow keys move between ports; Enter opens a port's card; Escape closes it."
      onKeyDown={grid.onKeyDown}
    >
      {notes.length > 0 && <p className="inventory-muted inventory-drawing-note">{notes.join(' · ')}</p>}
      {drawing.layout.faces.map(face => <DrawnFace key={face.face} geometry={face} drawing={drawing} title={drawing.titles[face.face]} tileButton={grid.tileButton} />)}
      <Tray drawing={drawing} tileButton={grid.tileButton} />
      {grid.overlay}
    </div>
  );
}

/** The caption above grouped tiles when the model records no layout. */
export function LayoutNotRecorded() {
  return <p className="inventory-faceplate-fallback"><span className="inventory-faceplate-fallback-label">Layout not recorded</span> <span className="inventory-muted">Grouped by face and type, not by position on the chassis.</span></p>;
}

/**
 * A Device's faceplate for the rack side panel: the drawing when the model
 * records a layout, otherwise the grouped tiles labelled "Layout not
 * recorded". When Cables are denied, the recorded components are still
 * drawn, with every state unknown.
 */
export function DeviceFaceplate({ device, inventory }: { device: Device; inventory: Inventory }) {
  const plate = useMemo(() => faceplate(device, inventory), [device, inventory]);
  const drawing = useMemo(() => deviceDrawingModel(device, inventory), [device, inventory]);
  const names = useMemo(() => viewNames(plate.links), [plate]);
  const cablesDenied = plate.cableAccess === 'denied';
  const telemetry = usePortTelemetry({ kind: 'Device', namespace: device.metadata.namespace, name: device.metadata.name }, { cablesDenied });
  let body;
  if (drawing.layout.recorded) body = <FaceplateDrawing drawing={drawing} namespace={device.metadata.namespace} href={inventoryHref} telemetry={telemetry} />;
  else if (plate.faces.length) body = <><LayoutNotRecorded /><PortFaceplate faces={plate.faces} names={names} namespace={device.metadata.namespace} href={inventoryHref} cablesDenied={cablesDenied} showEmptyFaces telemetry={telemetry} /></>;
  else body = <p className="inventory-muted">No ports are recorded for this Device.</p>;
  return (
    <section className="inventory-faceplate" aria-label="Faceplate">
      {body}
      {cablesDenied && <p className="inventory-muted">Cable details unavailable. Component states are unknown.</p>}
    </section>
  );
}

// ---- Mini faceplates for the rack elevation ----

/** One cell's state in a mini faceplate. */
export type MiniState = LinkState | ModuleState;

export interface MiniCounts { conflict: number; unavailable: number }

/** The visible face's cell states, and the "!" and "?" counts the elevation label carries. */
export function miniFaceplate(drawing: DeviceDrawingModel, face: ChassisFace) {
  const geometry = drawing.layout.faces.find(item => item.face === face);
  const states = new Map<string, MiniState>();
  for (const bank of geometry?.banks ?? []) {
    for (const cell of bank.cells) {
      const tile = MODULE_TYPES.has(cell.template.type) ? undefined : drawing.tiles.get(cell.template.id);
      states.set(cell.template.id, tile ? (drawing.cablesDenied ? 'unavailable' : tile.state) : drawing.recorded.has(cell.template.id) ? 'recorded' : 'catalog');
    }
  }
  // Counts cover the whole Device, not one face: a problem on the hidden face still counts.
  const all = [...drawing.tiles.values()].map(tile => drawing.cablesDenied ? 'unavailable' : tile.state);
  const counts: MiniCounts = { conflict: all.filter(state => state === 'conflict').length, unavailable: all.filter(state => state === 'unavailable').length };
  return { geometry, states, counts };
}

export const countsWords = (counts: MiniCounts) => [counts.conflict ? `${counts.conflict} ${counts.conflict === 1 ? 'port disagrees' : 'ports disagree'}` : undefined, counts.unavailable ? `${counts.unavailable} ${counts.unavailable === 1 ? 'peer' : 'peers'} unavailable` : undefined].filter(Boolean).join(', ');

/**
 * A visual-only faceplate for one elevation slot, hidden from assistive
 * technology: the slot's link carries the name and counts in words. Silhouette
 * draws the chassis and bank outlines; reduced adds one block per cell, with
 * the state as fill and outline (solid recorded, hollow none, destructive
 * disagree, dashed peer unavailable, dotted catalog only); full adds the "!"
 * and "?" glyphs. Hatching is not used: at 16 px per unit it is noise, and
 * the slot's text counts carry the problems without colour. A model without
 * a layout is a plain chassis.
 */
export function MiniFaceplate({ geometry, states, level, widthMM, heightMM }: { geometry?: FaceGeometry; states: Map<string, MiniState>; level: DetailLevel; widthMM: number; heightMM: number }) {
  return (
    <svg className="inventory-mini-faceplate" data-level={level} viewBox={`0 0 ${widthMM} ${heightMM}`} preserveAspectRatio="xMidYMid meet" aria-hidden="true" focusable="false">
      <rect className="inventory-mini-chassis" x="0.5" y="0.5" width={widthMM - 1} height={heightMM - 1} rx="1.5" />
      {geometry?.banks.map(bank => (
        <g key={bank.id}>
          {level === 'silhouette'
            ? <rect className="inventory-mini-bank" x={bank.x} y={bank.y} width={bank.w} height={bank.h} rx="0.8" />
            : bank.cells.map(cell => {
              const state = states.get(cell.template.id) ?? 'catalog';
              const glyph = level === 'full' ? TILE_GLYPH[state as LinkState] : undefined;
              return (
                <g key={cell.template.id}>
                  <rect className="inventory-mini-cell" data-state={state} data-kind={connectorKind(cell.template.connector)} x={cell.x} y={cell.y} width={cell.w} height={cell.h} rx={level === 'full' ? 0.8 : 0.3} />
                  {glyph && <text className="inventory-mini-glyph" x={cell.x + cell.w / 2} y={cell.y + cell.h / 2} textAnchor="middle" dominantBaseline="central" fontSize={Math.min(cell.h, cell.w) * 0.8}>{glyph}</text>}
                </g>
              );
            })}
        </g>
      ))}
    </svg>
  );
}
