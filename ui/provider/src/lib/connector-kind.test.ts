import { describe, expect, it } from 'vitest';
import { CONNECTOR_KINDS, connectorKind, connectorKindMapped } from './connector-kind';
import { connectorShape } from './device-connections';

describe('connectorKind', () => {
  it('maps recorded text to one kind from the fixed table, case-insensitively', () => {
    const cases: [string, string][] = [
      ['RJ45', 'RJ45'], ['rj-45 keystone', 'RJ45'], ['8P8C', 'RJ45'], ['1000base-t RJ45', 'RJ45'],
      ['SFP', 'SFP'], ['SFP+', 'SFP'], ['10gbase-x-sfpp', 'SFP'], ['1000base-x-sfp', 'SFP'],
      ['SFP28', 'SFP28'], ['25gbase-x-sfp28', 'SFP28'],
      ['QSFP28', 'QSFP28'], ['100gbase-x-qsfp28', 'QSFP28'],
      ['QSFP-DD', 'QSFP-DD'], ['qsfpdd', 'QSFP-DD'], ['400gbase-x-qsfpdd', 'QSFP-DD'],
      ['OSFP', 'OSFP'], ['800gbase-x-osfp', 'OSFP'],
      ['IEC C13', 'C13'], ['iec-60320-c14', 'C14'], ['C19', 'C19'], ['IEC 60320 C20', 'C20'],
      ['USB-A', 'USB'], ['usb-c', 'USB'], ['USB', 'USB'],
      ['DB9', 'DB9'], ['de-9', 'DB9'], ['VGA', 'VGA'], ['DE-15', 'VGA'],
    ];
    for (const [text, kind] of cases) expect([text, connectorKind(text)]).toEqual([text, kind]);
  });

  it('maps copper NetBox interface types to RJ45', () => {
    for (const text of ['1000base-t', '100base-tx', '10gbase-t', '2.5gbase-t', '5gbase-t', '1000BASE-T', '10base-t', '1000base-tx', '25gbase-t', '40gbase-t'])
      expect([text, connectorKind(text)]).toEqual([text, 'RJ45']);
    // Single-pair Ethernet is not an 8P8C jack; a bare "base-t" names no rate.
    for (const text of ['100base-t1', '1000base-t1', 'base-t', 'gbase-t'])
      expect([text, connectorKind(text)]).toEqual([text, 'Other']);
    // A copper module in an SFP cage is drawn as the cage.
    expect(connectorKind('1000base-t SFP')).toBe('SFP');
  });

  it('maps QSFP56 to its own kind, drawn in the QSFP cage', () => {
    for (const text of ['QSFP56', 'qsfp56', '200gbase-x-qsfp56', '200GBASE X QSFP56'])
      expect([text, connectorKind(text)]).toEqual([text, 'QSFP56']);
    const row = (kind: string) => CONNECTOR_KINDS.find(item => item.kind === kind)!;
    expect([row('QSFP56').widthMM, row('QSFP56').heightMM]).toEqual([row('QSFP28').widthMM, row('QSFP28').heightMM]);
    expect(connectorShape('200gbase-x-qsfp56')).toBe('qsfp');
  });

  it('maps LC to a duplex fiber kind and C22 to an IEC inlet', () => {
    for (const text of ['lc', 'LC', 'LC duplex', 'lc-upc', 'LC/APC', 'lc_pc'])
      expect([text, connectorKind(text)]).toEqual([text, 'LC']);
    expect(connectorShape('lc')).toBe('lc');
    for (const text of ['iec-60320-c22', 'IEC C22', 'C22'])
      expect([text, connectorKind(text)]).toEqual([text, 'C22']);
    expect(connectorShape('iec-60320-c22')).toBe('iec');
    expect(connectorKind('LCD panel')).toBe('Other');
    expect(connectorKind('C220')).toBe('Other');
  });

  it('already maps the NetBox cage types by their form factor token', () => {
    const cases: [string, string][] = [
      ['800gbase-x-osfp', 'OSFP'], ['400gbase-x-osfp', 'OSFP'], ['400gbase-x-qsfpdd', 'QSFP-DD'], ['800gbase-x-qsfpdd', 'QSFP-DD'],
      ['100gbase-x-qsfp28', 'QSFP28'], ['25gbase-x-sfp28', 'SFP28'], ['10gbase-x-sfpp', 'SFP'], ['1000base-x-sfp', 'SFP'],
    ];
    for (const [text, kind] of cases) expect([text, connectorKind(text)]).toEqual([text, kind]);
  });

  it('never guesses: unrecognised, ambiguous or absent text is Other', () => {
    for (const text of [undefined, '', '  ', 'MPO-12', 'QSFP', 'QSFP+', 'serial', 'Type C', 'C130 custom', 'RJ450', 'XSFP28', 'fan', '2.5in bay', 'SFP56'])
      expect([text, connectorKind(text)]).toEqual([text, 'Other']);
    expect(connectorKindMapped('')).toBe(false);
    expect(connectorKindMapped('MPO-12')).toBe(false);
    expect(connectorKindMapped('SFP28')).toBe(true);
    expect(connectorKindMapped('LC duplex')).toBe(true);
  });

  it('takes the first row in table order, so longer family names win', () => {
    expect(CONNECTOR_KINDS.map(row => row.kind)).toEqual(['QSFP-DD', 'OSFP', 'QSFP56', 'QSFP28', 'SFP28', 'SFP', 'RJ45', 'LC', 'C13', 'C14', 'C19', 'C20', 'C22', 'USB', 'DB9', 'VGA']);
    expect(connectorKind('RJ45 to DB9 console')).toBe('RJ45');
    expect(connectorKind('QSFP28 to 4x SFP28 breakout')).toBe('QSFP28');
    expect(connectorKind('SFP28 LC duplex')).toBe('SFP28');
  });

  it('gives every kind a nominal size and drives the tile outline', () => {
    for (const row of CONNECTOR_KINDS) expect(row.widthMM > 0 && row.heightMM > 0).toBe(true);
    expect(['OSFP', 'QSFP', 'USB-A', 'VGA'].map(connectorShape)).toEqual(['qsfp', 'generic', 'generic', 'generic']);
  });
});
