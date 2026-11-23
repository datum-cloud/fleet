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

  it('never guesses: unrecognised, ambiguous or absent text is Other', () => {
    for (const text of [undefined, '', '  ', 'LC duplex', 'MPO-12', 'QSFP', 'QSFP+', 'serial', 'Type C', 'C130 custom', 'RJ450', 'XSFP28', 'fan', '2.5in bay', 'SFP56'])
      expect([text, connectorKind(text)]).toEqual([text, 'Other']);
    expect(connectorKindMapped('LC duplex')).toBe(false);
    expect(connectorKindMapped('SFP28')).toBe(true);
  });

  it('takes the first row in table order, so longer family names win', () => {
    expect(CONNECTOR_KINDS.map(row => row.kind)).toEqual(['QSFP-DD', 'OSFP', 'QSFP28', 'SFP28', 'SFP', 'RJ45', 'C13', 'C14', 'C19', 'C20', 'USB', 'DB9', 'VGA']);
    expect(connectorKind('RJ45 to DB9 console')).toBe('RJ45');
    expect(connectorKind('QSFP28 to 4x SFP28 breakout')).toBe('QSFP28');
  });

  it('gives every kind a nominal size and drives the tile outline', () => {
    for (const row of CONNECTOR_KINDS) expect(row.widthMM > 0 && row.heightMM > 0).toBe(true);
    expect(['OSFP', 'QSFP', 'USB-A', 'VGA'].map(connectorShape)).toEqual(['qsfp', 'generic', 'generic', 'generic']);
  });
});
