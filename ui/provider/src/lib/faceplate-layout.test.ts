import { describe, expect, it } from 'vitest';
import type { ComponentTemplate, FaceplateBank, PhysicalComponent, PhysicalRecord } from '../../../shared/fleet-api';
import { BANK_GAP_MM, CELL_GAP_MM, deviceTray, edgeNumbers, faceGeometry, FACE_WIDTH_MM, joinTemplates, matchName, modelLayout, portNumber, TEXT_TILE_LIMIT, U_MM } from './faceplate-layout';

// Synthetic catalog records only.
const T = (id: string, face: 'Front' | 'Rear' | undefined, layout?: ComponentTemplate['layout'], connector = 'RJ45', type: ComponentTemplate['type'] = 'NetworkPort'): ComponentTemplate =>
  ({ id, name: id, type, connector, ...(face ? { face } : {}), ...(layout ? { layout } : {}) });
const model = (banks: FaceplateBank[] | undefined, templates: ComponentTemplate[], shape: PhysicalRecord['spec']['modelShape'] = { heightUnits: 1 }): PhysicalRecord => ({
  kind: 'DeviceType', metadata: { name: 'example-model', namespace: 'default' },
  spec: { displayName: 'Example', manufacturerRef: { name: 'example' }, model: 'example', modelShape: shape, componentTemplates: templates, ...(banks ? { faceplate: { banks } } : {}) },
});

describe('faceGeometry', () => {
  it('turns a bank grid into cells, with odd-top even-bottom numbering and spans', () => {
    const templates = [1, 2, 3, 4].map(port => T(`p${port}`, 'Front', { bank: 'a', row: 2 - (port % 2), column: Math.ceil(port / 2) }));
    templates.push(T('wide', 'Front', { bank: 'a', row: 1, column: 3, span: 2 }));
    const face = faceGeometry(model([{ id: 'a', face: 'Front', rows: 2, columns: 4 }], templates), 'Front');
    const [bank] = face.banks;
    const cell = (id: string) => bank.cells.find(item => item.template.id === id)!;
    // RJ45 nominal cell: 13.2 × 11.6 mm.
    expect(bank.w).toBeCloseTo(4 * 13.2 + 3 * CELL_GAP_MM);
    expect(bank.h).toBeCloseTo(2 * 11.6 + CELL_GAP_MM);
    expect(cell('p1').y).toBeLessThan(cell('p2').y);
    expect(cell('p3').x).toBeCloseTo(cell('p1').x + 13.2 + CELL_GAP_MM);
    expect(cell('wide').w).toBeCloseTo(2 * 13.2 + CELL_GAP_MM);
    expect(face.heightMM).toBe(U_MM);
    expect(face.warnings).toEqual([]);
  });

  it('flows banks without offsets left to right with even gaps, and places offset banks', () => {
    const flowed = faceGeometry(model([{ id: 'a', face: 'Rear', rows: 1, columns: 1 }, { id: 'b', face: 'Rear', rows: 1, columns: 1 }], [T('x', 'Rear', { bank: 'a', row: 1, column: 1 }), T('y', 'Rear', { bank: 'b', row: 1, column: 1 })]), 'Rear');
    const [a, b] = flowed.banks;
    const gap = (FACE_WIDTH_MM - a.w - b.w) / 3;
    expect(a.x).toBeCloseTo(gap);
    expect(b.x - (a.x + a.w)).toBeCloseTo(gap);
    const placed = faceGeometry(model([{ id: 'a', face: 'Rear', rows: 1, columns: 1, offset: 0 }, { id: 'b', face: 'Rear', rows: 1, columns: 1, offset: 0.9, cellMM: { width: 40, height: 38 } }], [T('x', 'Rear', { bank: 'a', row: 1, column: 1 }), T('psu', 'Rear', { bank: 'b', row: 1, column: 1 }, 'IEC C14', 'PowerInlet')]), 'Rear');
    expect(placed.banks.map(bank => bank.x)).toEqual([0, 0.9 * FACE_WIDTH_MM]);
    expect(placed.banks[1]).toMatchObject({ w: 40, h: 38, module: true });
    // Banks on the other face are not drawn here.
    expect(faceGeometry(model([{ id: 'a', face: 'Rear', rows: 1, columns: 1 }], []), 'Front').banks).toEqual([]);
  });

  it('scales a layout wider than the face and warns, since CEL cannot check fit', () => {
    const banks: FaceplateBank[] = [{ id: 'a', face: 'Front', rows: 1, columns: 40, cellMM: { width: 14, height: 10 } }];
    const face = faceGeometry(model(banks, [T('p', 'Front', { bank: 'a', row: 1, column: 1 })]), 'Front');
    const natural = 40 * 14 + 39 * CELL_GAP_MM + 2 * BANK_GAP_MM;
    expect(face.scale).toBeCloseTo(FACE_WIDTH_MM / natural);
    expect(face.banks[0].x + face.banks[0].w).toBeLessThanOrEqual(FACE_WIDTH_MM);
    expect(face.warnings).toEqual(['Layout is wider than the face; drawn scaled']);
    const half = faceGeometry(model([{ id: 'a', face: 'Front', rows: 1, columns: 1 }], [], { heightUnits: 1, width: 'Half' }), 'Front');
    expect(half.widthMM).toBe(FACE_WIDTH_MM / 2);
  });

  it('warns when offset banks overlap each other or offsets are partial', () => {
    const overlapping = faceGeometry(model([{ id: 'a', face: 'Front', rows: 1, columns: 4, offset: 0.1 }, { id: 'b', face: 'Front', rows: 1, columns: 4, offset: 0.12 }], []), 'Front');
    expect(overlapping.warnings).toContain('Records disagree: banks overlap on the face');
    const partial = faceGeometry(model([{ id: 'a', face: 'Front', rows: 1, columns: 1, offset: 0.1 }, { id: 'b', face: 'Front', rows: 1, columns: 1 }], []), 'Front');
    expect(partial.warnings).toContain('Records disagree: offsets are set on only some banks; drawn in list order');
  });

  it('marks banks above the shared dense threshold, and finds their edge numbers', () => {
    const count = TEXT_TILE_LIMIT + 1;
    const templates = Array.from({ length: count }, (_, index) => T(`swp${index + 1}`, 'Front', { bank: 'a', row: 1, column: index + 1 }));
    const [dense] = faceGeometry(model([{ id: 'a', face: 'Front', rows: 1, columns: count }], templates), 'Front').banks;
    expect(dense.dense).toBe(true);
    expect(edgeNumbers(dense).map(({ cell, where }) => `${portNumber(cell.template)} ${where}`)).toEqual(['1 above', `${count} above`]);
    const [small] = faceGeometry(model([{ id: 'a', face: 'Front', rows: 1, columns: TEXT_TILE_LIMIT }], templates.slice(0, TEXT_TILE_LIMIT)), 'Front').banks;
    expect(small.dense).toBe(false);
    const twoRows = Array.from({ length: 4 }, (_, index) => T(`ge${index + 1}`, 'Front', { bank: 'a', row: 2 - ((index + 1) % 2), column: Math.ceil((index + 1) / 2) }));
    const [grid] = faceGeometry(model([{ id: 'a', face: 'Front', rows: 2, columns: 2 }], twoRows), 'Front').banks;
    expect(edgeNumbers(grid).map(({ cell, where }) => `${cell.template.id} ${where}`)).toEqual(['ge1 above', 'ge2 below', 'ge3 above', 'ge4 below']);
  });
});

describe('modelLayout', () => {
  it('reports no layout, and every template untrayed as not laid out, when the model records none', () => {
    const layout = modelLayout(model(undefined, [T('eth0', 'Rear')]));
    expect(layout).toMatchObject({ recorded: false, faces: [], widthAssumed: true });
    expect(layout.tray.map(entry => entry.detail)).toEqual(['Position on the face not recorded']);
    // A height is needed to draw a chassis.
    expect(modelLayout(model([{ id: 'a', face: 'Front', rows: 1, columns: 1 }], [], {})).recorded).toBe(false);
    expect(modelLayout(undefined).recorded).toBe(false);
  });

  it('puts unplaced and disagreeing templates in the tray with the reason, never on the face', () => {
    const banks: FaceplateBank[] = [{ id: 'front', face: 'Front', rows: 1, columns: 2 }, { id: 'rear', face: 'Rear', rows: 1, columns: 1 }];
    const layout = modelLayout(model(banks, [
      T('a', 'Front', { bank: 'front', row: 1, column: 1 }),
      T('clash', 'Front', { bank: 'front', row: 1, column: 1 }),
      T('outside', 'Front', { bank: 'front', row: 1, column: 2, span: 2 }),
      T('wrong-face', 'Front', { bank: 'rear', row: 1, column: 1 }),
      T('faceless', undefined, { bank: 'front', row: 1, column: 2 }),
      T('console', 'Rear'),
    ]));
    expect(layout.faces.map(face => face.face)).toEqual(['Front', 'Rear']);
    expect(layout.faces[0].banks[0].cells.map(cell => cell.template.id)).toEqual(['a']);
    expect(layout.faces[1].banks[0].cells).toEqual([]);
    expect(layout.tray.map(entry => [entry.id, entry.reason, entry.detail])).toEqual([
      ['clash', 'overlap', 'Records disagree: overlaps a'],
      ['outside', 'outside-bank', 'Records disagree: outside bank front'],
      ['wrong-face', 'unknown-bank', 'Records disagree: no front bank named rear'],
      ['faceless', 'no-face', 'Records disagree: layout without a face'],
      ['console', 'not-laid-out', 'Position on the face not recorded'],
    ]);
  });

  it('draws zero-U equipment as a one-unit strip and says width is assumed', () => {
    const layout = modelLayout(model([{ id: 'o', face: 'Rear', rows: 1, columns: 1 }], [T('o1', 'Rear', { bank: 'o', row: 1, column: 1 }, 'IEC C13', 'PowerOutlet')], { heightUnits: 0 }));
    expect(layout).toMatchObject({ recorded: true, strip: true, widthAssumed: true, width: 'Full' });
    expect(layout.faces[0].heightMM).toBe(U_MM);
  });
});

describe('deviceTray', () => {
  it('adds Device components with no matching template, whose position is unknown', () => {
    const catalog = model([{ id: 'a', face: 'Rear', rows: 1, columns: 1 }], [T('eth0', 'Rear', { bank: 'a', row: 1, column: 1 }), T('bmc', 'Rear')]);
    const tray = deviceTray(modelLayout(catalog), catalog, [{ id: 'eth0', name: 'eth0', type: 'NetworkPort' }, { id: 'usb9', name: 'Front USB', type: 'InventoryItem' }]);
    expect(tray.map(entry => [entry.id, entry.reason])).toEqual([['bmc', 'not-laid-out'], ['usb9', 'no-template']]);
  });

  it('uses a name-matched component for a template entry, and keeps ambiguous components in the tray', () => {
    const catalog = model([{ id: 'a', face: 'Rear', rows: 1, columns: 1 }], [
      named('interface-templates-1', 'Ethernet1', 'Rear', { bank: 'a', row: 1, column: 1 }),
      named('console-port-templates-2', 'Console', 'Rear', undefined, 'ConsolePort'),
      named('power-port-templates-3', 'PSU', 'Rear', undefined, 'PowerInlet'),
      named('power-port-templates-4', 'PSU', 'Rear', undefined, 'PowerInlet'),
    ]);
    const tray = deviceTray(modelLayout(catalog), catalog, [C('interfaces-11', 'Ethernet1'), C('console-ports-12', 'Console', 'ConsolePort'), C('power-ports-13', 'PSU', 'PowerInlet'), C('power-ports-14', 'PSU', 'PowerInlet')]);
    expect(tray.map(entry => [entry.id, entry.reason])).toEqual([
      // Not laid out: listed once, under the joined component's id.
      ['console-ports-12', 'not-laid-out'],
      ['power-port-templates-3', 'not-laid-out'],
      ['power-port-templates-4', 'not-laid-out'],
      // Two templates share the name: neither PSU is joined.
      ['power-ports-13', 'no-template'],
      ['power-ports-14', 'no-template'],
    ]);
  });
});

// Synthetic importer-shaped records: instance ids that differ from template ids.
const named = (id: string, name: string, face: 'Front' | 'Rear' | undefined, layout?: ComponentTemplate['layout'], type: ComponentTemplate['type'] = 'NetworkPort'): ComponentTemplate =>
  ({ ...T(id, face, layout, 'RJ45', type), name });
const C = (id: string, name: string, type: PhysicalComponent['type'] = 'NetworkPort'): PhysicalComponent => ({ id, name, type });
const joined = (templates: ComponentTemplate[], components: PhysicalComponent[]) =>
  [...joinTemplates(templates, components).byComponent.values()].map(match => [match.componentID, match.templateID, match.basis]);

describe('joinTemplates', () => {
  it('joins by id first, and records the basis', () => {
    expect(joined([named('eth0', 'Ethernet 0', 'Rear'), named('eth1', 'Ethernet 1', 'Rear')], [C('eth0', 'Ethernet 0'), C('eth1', 'renamed')]))
      .toEqual([['eth0', 'eth0', 'id'], ['eth1', 'eth1', 'id']]);
  });

  it('falls back to one template with the same name and type when no template has the id', () => {
    const templates = [named('interface-templates-34', 'Ethernet1', 'Front'), named('interface-templates-35', 'Ethernet2', 'Front')];
    const join = joinTemplates(templates, [C('interfaces-3362', 'Ethernet1'), C('interfaces-3363', ' Ethernet2 ')]);
    expect([...join.byComponent.values()].map(match => [match.componentID, match.templateID, match.basis]))
      .toEqual([['interfaces-3362', 'interface-templates-34', 'name+type'], ['interfaces-3363', 'interface-templates-35', 'name+type']]);
    expect(join.byTemplate.get('interface-templates-34')?.componentID).toBe('interfaces-3362');
  });

  it('needs the type to match as well as the name', () => {
    expect(joined([named('console-port-templates-1', 'Console', 'Front', undefined, 'ConsolePort')], [C('interfaces-1', 'Console', 'NetworkPort')])).toEqual([]);
  });

  it('leaves a component unjoined when several templates share its name and type', () => {
    const templates = [named('power-port-templates-1', 'PSU', 'Rear', undefined, 'PowerInlet'), named('power-port-templates-2', 'PSU', 'Rear', undefined, 'PowerInlet')];
    expect(joined(templates, [C('power-ports-1', 'PSU', 'PowerInlet')])).toEqual([]);
  });

  it('leaves a component unjoined when no template matches, or its name is blank', () => {
    expect(joined([named('interface-templates-1', 'Ethernet1', 'Front')], [C('interfaces-1', 'Ethernet9'), C('interfaces-2', '  ')])).toEqual([]);
  });

  it('never reuses a template already joined by id to another component', () => {
    const templates = [named('eth0', 'Ethernet 0', 'Rear')];
    // interfaces-7 has the same name and type, but eth0 already has its component.
    expect(joined(templates, [C('interfaces-7', 'Ethernet 0'), C('eth0', 'Ethernet 0')])).toEqual([['eth0', 'eth0', 'id']]);
  });

  it('never gives one template to two components that both fall back to it', () => {
    const templates = [named('interface-templates-1', 'Ethernet1', 'Front')];
    expect(joined(templates, [C('interfaces-1', 'Ethernet1'), C('interfaces-2', 'Ethernet1')])).toEqual([]);
  });

  it('keeps case and inner words, only trimming and collapsing whitespace', () => {
    expect(matchName('  Ethernet   1 ')).toBe('Ethernet 1');
    expect(joined([named('interface-templates-1', 'Ethernet1', 'Front')], [C('interfaces-1', 'ethernet1')])).toEqual([]);
    expect(joined([named('interface-templates-1', 'Ethernet 1', 'Front')], [C('interfaces-1', 'Ethernet  1')])).toEqual([['interfaces-1', 'interface-templates-1', 'name+type']]);
  });
});
