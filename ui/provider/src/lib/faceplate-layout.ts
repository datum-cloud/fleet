// Faceplate geometry from a DeviceType's recorded layout: one pure function
// that every view draws from (Device detail, the rack panel and the
// elevation's mini faceplates), so no view invents its own arrangement.
//
// Everything here is nominal catalog data in millimetres. It says where a
// template sits, never whether it is cabled or working. Layout the CRD should
// have rejected (an older record, a bank on the other face, an overlap) is
// not drawn: the template goes to the "Not on the faceplate" tray with the
// reason, and the face carries a "records disagree" warning.

import type { ChassisFace, ChassisWidth, ComponentTemplate, FaceplateBank, PhysicalComponent, PhysicalRecord } from '../../../shared/fleet-api';
import { connectorKindRow } from './connector-kind';

/**
 * The dense-face threshold, shared by the tile clusters and the drawing:
 * above this many ports in one cluster or bank, ports carry no text, only
 * state, with faint printed-style numbers at bank edges.
 */
export const TEXT_TILE_LIMIT = 12;

export const U_MM = 44.45;
/** The usable width between the rack ears of a full-width chassis. */
export const FACE_WIDTH_MM = 450;
/** Space between cells in a bank, and the least space between flowed banks. */
export const CELL_GAP_MM = 0.8;
export const BANK_GAP_MM = 4;

/** Types drawn as module cells (drive bays, fans, accessory ports) rather than cabled ports. */
export const MODULE_TYPES = new Set<PhysicalComponent['type']>(['Module', 'InventoryItem', 'DeviceBay']);

export interface LayoutCell {
  template: ComponentTemplate;
  row: number;
  column: number;
  span: number;
  /** Millimetres from the face's top-left corner, after any fit scaling. */
  x: number; y: number; w: number; h: number;
}

export interface LayoutBank {
  id: string;
  rows: number;
  columns: number;
  x: number; y: number; w: number; h: number;
  /** More cells than TEXT_TILE_LIMIT: no text on ports. */
  dense: boolean;
  /** Cells are sized as modules (recorded cellMM), not as connectors. */
  module: boolean;
  cells: LayoutCell[];
}

export interface FaceGeometry {
  face: ChassisFace;
  widthMM: number;
  heightMM: number;
  /** 1 unless the recorded banks did not fit the face and were scaled down. */
  scale: number;
  banks: LayoutBank[];
  /** Short, user-facing; each is a "records disagree" layout warning. */
  warnings: string[];
}

export type TrayReason = 'not-laid-out' | 'no-face' | 'unknown-bank' | 'outside-bank' | 'overlap' | 'no-template';

export interface TrayEntry {
  id: string;
  name: string;
  type?: PhysicalComponent['type'];
  reason: TrayReason;
  /** Why it is in the tray, in words. */
  detail: string;
}

export interface ModelLayout {
  /** The DeviceType records a faceplate. */
  recorded: boolean;
  width: ChassisWidth;
  /** No width recorded: drawn full width, and said so. */
  widthAssumed: boolean;
  /** The chassis height in U; undefined when not recorded. */
  heightUnits?: number;
  /** Zero-U equipment: drawn as a one-unit strip because its length is not recorded. */
  strip: boolean;
  /** Faces with at least one bank, Front first. */
  faces: FaceGeometry[];
  /** Templates that are not on the drawing, with the reason. */
  tray: TrayEntry[];
}

const nominal = (template: ComponentTemplate) => connectorKindRow(template.connector);

/** A bank's cell size: the recorded module size, else the largest nominal connector in it. */
function cellSize(bank: FaceplateBank, templates: ComponentTemplate[]) {
  if (bank.cellMM) return { w: bank.cellMM.width, h: bank.cellMM.height, module: true };
  const rows = templates.length ? templates.map(nominal) : [connectorKindRow(undefined)];
  return { w: Math.max(...rows.map(row => row.widthMM)), h: Math.max(...rows.map(row => row.heightMM)), module: false };
}

/** Checks each laid-out template against the banks, and returns the ones that can be drawn. */
function placeable(model: PhysicalRecord, banks: FaceplateBank[]) {
  const tray: TrayEntry[] = [];
  const placed = new Map<string, ComponentTemplate[]>();
  const taken = new Map<string, ComponentTemplate>();
  const entry = (template: ComponentTemplate, reason: TrayReason, detail: string) => tray.push({ id: template.id, name: template.name, type: template.type, reason, detail });
  for (const template of model.spec.componentTemplates ?? []) {
    const layout = template.layout;
    if (!layout) { entry(template, 'not-laid-out', 'Position on the face not recorded'); continue; }
    if (!template.face) { entry(template, 'no-face', 'Records disagree: layout without a face'); continue; }
    const bank = banks.find(item => item.id === layout.bank);
    if (!bank || bank.face !== template.face) { entry(template, 'unknown-bank', `Records disagree: no ${template.face.toLowerCase()} bank named ${layout.bank}`); continue; }
    const span = layout.span ?? 1;
    if (layout.row < 1 || layout.column < 1 || span < 1 || layout.row > bank.rows || layout.column + span - 1 > bank.columns) {
      entry(template, 'outside-bank', `Records disagree: outside bank ${bank.id}`);
      continue;
    }
    const cells = Array.from({ length: span }, (_, index) => `${bank.id}/${layout.row}/${layout.column + index}`);
    const clash = cells.map(cell => taken.get(cell)).find(Boolean);
    if (clash) { entry(template, 'overlap', `Records disagree: overlaps ${clash.name}`); continue; }
    for (const cell of cells) taken.set(cell, template);
    placed.set(bank.id, [...(placed.get(bank.id) ?? []), template]);
  }
  return { tray, placed };
}

/**
 * The geometry of one face. Banks with offsets sit at offset × face width;
 * banks without flow left to right with even gaps. Each is centred
 * vertically. If the banks do not fit, the whole face is scaled down and a
 * warning says so: CEL cannot check physical fit.
 */
export function faceGeometry(model: PhysicalRecord, face: ChassisFace, placed = placeable(model, model.spec.faceplate?.banks ?? []).placed): FaceGeometry {
  const shape = model.spec.modelShape;
  const widthMM = shape?.width === 'Half' ? FACE_WIDTH_MM / 2 : FACE_WIDTH_MM;
  const heightMM = Math.max(1, shape?.heightUnits ?? 0) * U_MM;
  const warnings: string[] = [];
  const recorded = (model.spec.faceplate?.banks ?? []).filter(bank => bank.face === face);
  const sized = recorded.map(bank => {
    const templates = placed.get(bank.id) ?? [];
    const cell = cellSize(bank, templates);
    return { bank, templates, cell, w: bank.columns * cell.w + (bank.columns - 1) * CELL_GAP_MM, h: bank.rows * cell.h + (bank.rows - 1) * CELL_GAP_MM };
  });
  const offsets = sized.length > 0 && sized.every(item => item.bank.offset !== undefined);
  if (!offsets && sized.some(item => item.bank.offset !== undefined)) warnings.push('Records disagree: offsets are set on only some banks; drawn in list order');
  let lefts: number[];
  if (offsets) lefts = sized.map(item => item.bank.offset! * widthMM);
  else {
    const total = sized.reduce((sum, item) => sum + item.w, 0);
    const gap = Math.max(BANK_GAP_MM, (widthMM - total) / (sized.length + 1));
    let x = gap;
    lefts = sized.map(item => { const left = x; x += item.w + gap; return left; });
  }
  const right = Math.max(widthMM, ...sized.map((item, index) => lefts[index] + item.w + (offsets ? 0 : BANK_GAP_MM)));
  const bottom = Math.max(heightMM, ...sized.map(item => item.h + 2));
  const scale = Math.min(1, widthMM / right, heightMM / bottom);
  if (scale < 1) warnings.push(right > widthMM ? 'Layout is wider than the face; drawn scaled' : 'Layout is taller than the face; drawn scaled');
  const banks: LayoutBank[] = sized.map((item, index) => {
    const cw = item.cell.w * scale, ch = item.cell.h * scale, gap = CELL_GAP_MM * scale;
    const w = item.w * scale, h = item.h * scale;
    const x = lefts[index] * scale, y = (heightMM - h) / 2;
    return {
      id: item.bank.id, rows: item.bank.rows, columns: item.bank.columns, x, y, w, h, module: item.cell.module,
      dense: item.templates.length > TEXT_TILE_LIMIT,
      cells: item.templates.map(template => {
        const { row, column } = template.layout!;
        const span = template.layout!.span ?? 1;
        return { template, row, column, span, x: x + (column - 1) * (cw + gap), y: y + (row - 1) * (ch + gap), w: span * cw + (span - 1) * gap, h: ch };
      }),
    };
  });
  if (offsets) {
    const clash = banks.some((a, i) => banks.some((b, j) => j > i && a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h));
    if (clash) warnings.push('Records disagree: banks overlap on the face');
  }
  return { face, widthMM, heightMM, scale, banks, warnings };
}

/** The whole model's layout, or `recorded: false` when it has none. */
export function modelLayout(model: PhysicalRecord | undefined): ModelLayout {
  const shape = model?.spec.modelShape;
  const base = { width: shape?.width ?? 'Full', widthAssumed: !shape?.width, heightUnits: shape?.heightUnits, strip: shape?.heightUnits === 0 } as const;
  const banks = model?.spec.faceplate?.banks ?? [];
  if (!model || !banks.length || shape?.heightUnits === undefined) {
    return { ...base, recorded: false, faces: [], tray: (model?.spec.componentTemplates ?? []).map(template => ({ id: template.id, name: template.name, type: template.type, reason: 'not-laid-out' as const, detail: 'Position on the face not recorded' })) };
  }
  const { tray, placed } = placeable(model, banks);
  const faces = (['Front', 'Rear'] as const).filter(face => banks.some(bank => bank.face === face)).map(face => faceGeometry(model, face, placed));
  return { ...base, recorded: true, faces, tray };
}

/** How a Device component was joined to a DeviceType template. */
export type TemplateMatchBasis = 'id' | 'name+type';

export interface TemplateMatch {
  componentID: string;
  templateID: string;
  basis: TemplateMatchBasis;
}

export interface TemplateJoin {
  byComponent: Map<string, TemplateMatch>;
  byTemplate: Map<string, TemplateMatch>;
}

/** Names compare after trimming and collapsing runs of whitespace; case is kept. */
export const matchName = (name: string | undefined) => (name ?? '').trim().replace(/\s+/g, ' ');

/**
 * Joins a Device's components to its DeviceType's templates, for drawing
 * only. The id join comes first. A component with no template of the same id
 * falls back to the one template with the same name and type, when exactly
 * one template matches, that template is not joined by id to another
 * component, and no other component falls back to it. Anything else stays
 * unjoined and goes to the tray.
 *
 * Importers can give components instance ids (`interfaces-3362`) that differ
 * from the template ids (`interface-templates-34`). A name match positions the
 * component on the drawing and nothing else: record state, Cable joins,
 * chassis faces for checks and every other view still join by id.
 */
export function joinTemplates(templates: ComponentTemplate[] | undefined, components: PhysicalComponent[] | undefined): TemplateJoin {
  const byComponent = new Map<string, TemplateMatch>();
  const byTemplate = new Map<string, TemplateMatch>();
  const add = (match: TemplateMatch) => { byComponent.set(match.componentID, match); byTemplate.set(match.templateID, match); };
  const ids = new Set((templates ?? []).map(template => template.id));
  for (const component of components ?? []) {
    if (ids.has(component.id) && !byTemplate.has(component.id)) add({ componentID: component.id, templateID: component.id, basis: 'id' });
  }
  // Template id → the components that fall back to it by name and type.
  const wanted = new Map<string, string[]>();
  for (const component of components ?? []) {
    if (ids.has(component.id)) continue;
    const name = matchName(component.name);
    if (!name) continue;
    const candidates = (templates ?? []).filter(template => template.type === component.type && matchName(template.name) === name);
    if (candidates.length !== 1 || byTemplate.has(candidates[0].id)) continue;
    wanted.set(candidates[0].id, [...(wanted.get(candidates[0].id) ?? []), component.id]);
  }
  for (const [templateID, claimants] of wanted) if (claimants.length === 1) add({ componentID: claimants[0], templateID, basis: 'name+type' });
  return { byComponent, byTemplate };
}

/**
 * The "Not on the faceplate" tray for one Device: the model's templates that
 * are not drawn (under the joined component's id when one is joined), plus
 * the Device's components with no joined template, whose position on the
 * chassis is unknown.
 */
export function deviceTray(layout: ModelLayout, model: PhysicalRecord | undefined, components: PhysicalComponent[] | undefined, join = joinTemplates(model?.spec.componentTemplates, components)): TrayEntry[] {
  const templates = layout.tray.map(entry => ({ ...entry, id: join.byTemplate.get(entry.id)?.componentID ?? entry.id }));
  const untemplated = (components ?? []).filter(component => !join.byComponent.has(component.id)).map(component => ({ id: component.id, name: component.name, type: component.type, reason: 'no-template' as const, detail: 'Not in the model, so its position is unknown' }));
  return [...templates, ...untemplated];
}

/** The number to print at a dense bank's edge: the trailing number of the name or id. */
export function portNumber(template: ComponentTemplate) {
  return template.name.match(/(\d+)\s*$/)?.[1] ?? template.id.match(/(\d+)$/)?.[1] ?? '';
}

/** Cells at the bank's left and right edges in its top and bottom rows: where a faceplate prints numbers. */
export function edgeNumbers(bank: LayoutBank): { cell: LayoutCell; where: 'above' | 'below' }[] {
  const edge = (cell: LayoutCell) => cell.column === 1 || cell.column + cell.span - 1 === bank.columns;
  return bank.cells.flatMap(cell => [
    ...(cell.row === 1 && edge(cell) ? [{ cell, where: 'above' as const }] : []),
    ...(cell.row === bank.rows && bank.rows > 1 && edge(cell) ? [{ cell, where: 'below' as const }] : []),
  ]);
}
