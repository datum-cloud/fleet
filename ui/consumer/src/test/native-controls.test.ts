// Form controls come from datum-ui so every control looks and behaves the
// same. A native <select>, <input>, <textarea>, <button> or <table> in a component
// fails this check unless it is listed below with the reason it is exempt.

import { readdir, readFile } from 'node:fs/promises';
import { join, relative, resolve } from 'node:path';
import { expect, it } from 'vitest';

const SRC = resolve(import.meta.dirname, '..');
const NATIVE = /<(select|input|textarea|button|table)\b/g;
/** File (relative to src) → native elements it may use, and why. */
const ALLOWED: Record<string, { elements: string[]; reason: string }> = {};

async function components(directory: string): Promise<string[]> {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = await Promise.all(entries.map(entry => entry.isDirectory() ? components(join(directory, entry.name)) : entry.name.endsWith('.tsx') && !entry.name.endsWith('.test.tsx') ? [join(directory, entry.name)] : []));
  return files.flat();
}

it('uses datum-ui controls instead of native form elements and tables', async () => {
  const found: string[] = [];
  for (const file of await components(SRC)) {
    const name = relative(SRC, file);
    for (const [, element] of (await readFile(file, 'utf8')).matchAll(NATIVE)) if (!ALLOWED[name]?.elements.includes(element)) found.push(`${name}: <${element}>`);
  }
  expect(found).toEqual([]);
});
