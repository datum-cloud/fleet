// Connector kinds derived from a component's free-text `connector`. The kind
// is never stored: this one fixed table is the only place text becomes a kind,
// and it is used for drawing only (outline and nominal size). Matching is
// case-insensitive on whole tokens, first row wins, and text that matches no
// row is Other. Nothing is guessed from a type, a name or a rate.

export type ConnectorKind =
  | 'QSFP-DD' | 'OSFP' | 'QSFP28' | 'SFP28' | 'SFP' | 'RJ45'
  | 'C13' | 'C14' | 'C19' | 'C20' | 'USB' | 'DB9' | 'VGA' | 'Other';

export interface ConnectorKindRow {
  kind: ConnectorKind;
  /** Words for the card and legend. */
  label: string;
  /** Matched against the upper-cased text with spaces and underscores as hyphens. */
  pattern: RegExp;
  /** Nominal face size in millimetres, for cells with no recorded size. */
  widthMM: number;
  heightMM: number;
}

// A token boundary: start, end, or any character that is not a letter or digit.
const token = (body: string) => new RegExp(`(?:^|[^A-Z0-9])(?:${body})(?:$|[^A-Z0-9])`);

/**
 * The table, in match order. QSFP-DD and OSFP come before QSFP28 and SFP28,
 * which come before SFP, so a longer name is never read as a shorter one.
 * `SFP+` and `SFPP` are SFP (same cage). `QSFP` and `QSFP+` match no row and
 * are Other: the table keeps only the families operators name.
 */
export const CONNECTOR_KINDS: ConnectorKindRow[] = [
  { kind: 'QSFP-DD', label: 'QSFP-DD', pattern: token('QSFP-?DD'), widthMM: 18.4, heightMM: 10.8 },
  { kind: 'OSFP', label: 'OSFP', pattern: token('OSFP'), widthMM: 22.6, heightMM: 13 },
  { kind: 'QSFP28', label: 'QSFP28', pattern: token('QSFP28'), widthMM: 18.4, heightMM: 9.4 },
  { kind: 'SFP28', label: 'SFP28', pattern: token('SFP28'), widthMM: 13.7, heightMM: 9.2 },
  { kind: 'SFP', label: 'SFP', pattern: token('SFP(?:\\+|P)?'), widthMM: 13.7, heightMM: 9.2 },
  { kind: 'RJ45', label: 'RJ45', pattern: token('RJ-?45|8P8C'), widthMM: 13.2, heightMM: 11.6 },
  { kind: 'C13', label: 'IEC C13', pattern: token('C13'), widthMM: 24, heightMM: 16.4 },
  { kind: 'C14', label: 'IEC C14', pattern: token('C14'), widthMM: 24, heightMM: 16.4 },
  { kind: 'C19', label: 'IEC C19', pattern: token('C19'), widthMM: 22, heightMM: 17 },
  { kind: 'C20', label: 'IEC C20', pattern: token('C20'), widthMM: 22, heightMM: 17 },
  { kind: 'USB', label: 'USB', pattern: token('USB(?:-?[ABC])?'), widthMM: 13.2, heightMM: 6 },
  { kind: 'DB9', label: 'Serial DB9', pattern: token('DB-?9|DE-?9'), widthMM: 31, heightMM: 12.5 },
  { kind: 'VGA', label: 'VGA', pattern: token('VGA|DE-?15|DB-?15'), widthMM: 31, heightMM: 12.5 },
];

export const OTHER_CONNECTOR: ConnectorKindRow = { kind: 'Other', label: 'Other', pattern: /$^/, widthMM: 14, heightMM: 12 };

const normalize = (text: string) => text.trim().toUpperCase().replace(/[\s_]+/g, '-');

/** The row for a recorded connector; Other when absent or unmatched. */
export function connectorKindRow(connector: string | undefined): ConnectorKindRow {
  if (!connector?.trim()) return OTHER_CONNECTOR;
  const text = normalize(connector);
  return CONNECTOR_KINDS.find(row => row.pattern.test(text)) ?? OTHER_CONNECTOR;
}

export const connectorKind = (connector: string | undefined): ConnectorKind => connectorKindRow(connector).kind;

/** Whether the text was matched, or fell through to Other. */
export const connectorKindMapped = (connector: string | undefined) => connectorKindRow(connector) !== OTHER_CONNECTOR;
