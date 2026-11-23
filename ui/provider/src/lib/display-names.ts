// Short, human names for dense views (rack panels, port tiles, grouped
// cable lists). Generated record names stay out of these views; callers show
// the full record name only in a details disclosure or the record's own page.

interface Named { metadata: { name: string }; spec: { displayName?: string } }

/** The display name, or the record name when no display name is recorded. */
export function displayLabel(record: Named): string {
  return record.spec.displayName || record.metadata.name;
}

const ABBREVIATIONS: [RegExp, string][] = [
  [/^Ethernet\s*/i, 'eth'],
  [/^Management\b\s*/i, 'mgmt'],
  [/^Serial console$/i, 'console'],
  [/^Outlet\s+/i, 'O'],
  [/^Front\s+/i, 'F'],
  [/^Rear\s+/i, 'R'],
];

/**
 * A condensed component name for a tile, e.g. "Ethernet 0" → "eth0",
 * "Outlet 3" → "O3", "Front 1" → "F1". Names with no known pattern are
 * returned unchanged. The full name always remains in the tile's accessible
 * name and pop-up card.
 */
export function condensedName(name: string): string {
  for (const [pattern, short] of ABBREVIATIONS) if (pattern.test(name)) return `${short}${name.replace(pattern, '')}`.trim();
  return name;
}
