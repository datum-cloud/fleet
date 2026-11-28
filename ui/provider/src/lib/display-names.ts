// Short, human names for dense views (rack panels, port tiles, grouped
// cable lists). Generated record names stay out of these views; callers show
// the full record name only in a details disclosure or the record's own page.

interface Named { metadata: { name: string }; spec: { displayName?: string } }

/** The display name, or the record name when no display name is recorded. */
export function displayLabel(record: Named): string {
  return record.spec.displayName || record.metadata.name;
}

// Names that importers generate (`netbox-ca213d185ea9153b853f295e`), and UIDs.
// They identify a record but mean nothing to a reader.
const GENERATED_NAME = /[0-9a-f]{12,}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}/i;
export const isGeneratedName = (name: string) => GENERATED_NAME.test(name);

/**
 * The display name, or the resource name when it reads as a human name
 * (`pdu-a.rack-01.ex1`). Never a generated name: undefined when there is none.
 */
export function humanName(record: Named): string | undefined {
  for (const candidate of [record.spec.displayName?.trim(), record.metadata.name]) if (candidate && !isGeneratedName(candidate)) return candidate;
  return undefined;
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

// Context helpers for detail pages: a row should not repeat what the page's
// header or breadcrumb already states. They only drop repeated words; they
// never invent or reorder identity. See "Page design rules" in
// docs/development/staff-inventory-ui.md.

const SEPARATOR = /^[\s·:/›>-]+/;

/**
 * Remove a leading `context` from `name`, with any separator after it.
 * Returns `name` unchanged when it does not start with the context or when
 * nothing would be left.
 */
export function withoutLeadingContext(name: string, context: string | undefined): string {
  if (!context) return name;
  if (!name.toLowerCase().startsWith(context.toLowerCase())) return name;
  const rest = name.slice(context.length);
  if (rest && !SEPARATOR.test(rest)) return name;
  const trimmed = rest.replace(SEPARATOR, '');
  return trimmed || name;
}

/** Drop parts that equal a name the page already states, and empty parts. */
export function withoutContext(parts: (string | undefined | false)[], context: (string | undefined)[]): string[] {
  const known = new Set(context.filter(Boolean).map(value => value!.toLowerCase()));
  return parts.filter((part): part is string => !!part && !known.has(part.toLowerCase()));
}

/**
 * The text every value in `values` shares, or undefined. Lets a group header
 * say a word once ("IEC C19") instead of on every row.
 */
export function sharedValue<T>(values: T[]): T | undefined {
  if (!values.length) return undefined;
  return values.every(value => value === values[0]) ? values[0] : undefined;
}
