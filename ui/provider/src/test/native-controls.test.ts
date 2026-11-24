// @vitest-environment node
//
// Form controls and tables come from datum-ui so every control looks and
// behaves the same. A native <select>, <input>, <textarea>, <button> or <table>
// in a component fails this check unless it sits inside a function listed
// below with the reason it is exempt.

import { readdir, readFile } from 'node:fs/promises';
import { join, relative, resolve } from 'node:path';
import { expect, it } from 'vitest';

const SRC = resolve(import.meta.dirname, '..');
const NATIVE = /<(select|input|textarea|button|table)\b/g;
/** Exemptions: a native element allowed only inside one named top-level function of one file (relative to src). */
const ALLOWED: { file: string; within: string; element: string; reason: string }[] = [
  { file: 'components/device-faceplate.tsx', within: 'PortTileButton', element: 'button', reason: 'Port tiles are drawn faceplate tiles whose look is the drawing.' },
];

async function components(directory: string): Promise<string[]> {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = await Promise.all(entries.map(entry => entry.isDirectory() ? components(join(directory, entry.name)) : entry.name.endsWith('.tsx') && !entry.name.endsWith('.test.tsx') ? [join(directory, entry.name)] : []));
  return files.flat();
}

/** Where a top-level function's body starts and ends: its declaration to the next closing brace at column 0. */
function functionRange(source: string, name: string): [number, number] | undefined {
  const start = source.search(new RegExp(`^(export )?function ${name}\\b`, 'm'));
  if (start < 0) return undefined;
  const end = source.indexOf('\n}\n', start);
  return [start, end < 0 ? source.length : end];
}

function nativeControls(name: string, source: string): string[] {
  const ranges = ALLOWED.filter(allowed => allowed.file === name).map(allowed => ({ element: allowed.element, range: functionRange(source, allowed.within) }));
  const found: string[] = [];
  for (const match of source.matchAll(NATIVE)) {
    const exempt = ranges.some(({ element, range }) => element === match[1] && range && match.index >= range[0] && match.index < range[1]);
    if (!exempt) found.push(`${name}: <${match[1]}>`);
  }
  return found;
}

it('uses datum-ui controls instead of native form elements and tables', async () => {
  const found: string[] = [];
  for (const file of await components(SRC)) {
    const name = relative(SRC, file);
    found.push(...nativeControls(name, await readFile(file, 'utf8')));
  }
  expect(found).toEqual([]);
});

it('exempts the port tile only, not the rest of its file', async () => {
  const source = await readFile(join(SRC, 'components/device-faceplate.tsx'), 'utf8');
  expect(nativeControls('components/device-faceplate.tsx', source)).toEqual([]);
  expect(nativeControls('components/device-faceplate.tsx', `${source}\nexport function Planted() { return <button type="button" />; }\n`)).toEqual(['components/device-faceplate.tsx: <button>']);
  expect(nativeControls('components/device-faceplate.tsx', `${source}\nexport function Planted() { return <input />; }\n`)).toEqual(['components/device-faceplate.tsx: <input>']);
});
