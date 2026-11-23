// @vitest-environment node
//
// Mock telemetry must never reach a normal build (staging or production). It
// is guarded like fixture data: the mock module is imported only behind the
// build-time VITE_FLEET_FIXTURES flag, which vite.config.ts compiles to false
// outside `--mode fixtures`. This builds both bundles into temporary
// directories and checks the strings only the mock contains: absent from the
// production build, present in the fixture build (so the check can fail).

import { mkdtemp, readdir, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { build } from 'vite';
import { afterAll, describe, expect, it } from 'vitest';

const ROOT = resolve(import.meta.dirname, '../..');
// Strings that exist only in src/lib/telemetry-mock.ts.
const MOCK_ONLY = ['fleet-synthetic-telemetry-preview-only', 'Synthetic preview telemetry', 'synthetic-pdu-collector', 'synthetic-bmc-collector'];
const TEXT = /\.(js|mjs|html|json|css|ts)$/;
const directories: string[] = [];

async function files(dir: string): Promise<string[]> {
  const entries = await readdir(dir, { withFileTypes: true });
  return (await Promise.all(entries.map(entry => entry.isDirectory() ? files(join(dir, entry.name)) : [join(dir, entry.name)]))).flat();
}

async function bundle(mode: 'production' | 'fixtures') {
  const outDir = await mkdtemp(join(tmpdir(), `fleet-bundle-${mode}-`));
  directories.push(outDir);
  const previous = process.env.VITE_FLEET_FIXTURES;
  if (mode === 'fixtures') process.env.VITE_FLEET_FIXTURES = 'true';
  else delete process.env.VITE_FLEET_FIXTURES;
  try {
    await build({ root: ROOT, configFile: resolve(ROOT, 'vite.config.ts'), mode, logLevel: 'silent', build: { outDir, emptyOutDir: true } });
  } finally {
    if (previous === undefined) delete process.env.VITE_FLEET_FIXTURES;
    else process.env.VITE_FLEET_FIXTURES = previous;
  }
  const texts = await Promise.all((await files(outDir)).filter(file => TEXT.test(file)).map(async file => ({ file, text: await readFile(file, 'utf8') })));
  return (needle: string) => texts.filter(item => item.text.includes(needle)).map(item => item.file.slice(outDir.length + 1));
}

afterAll(async () => { await Promise.all(directories.map(dir => rm(dir, { recursive: true, force: true }))); });

describe('mock telemetry production guard', () => {
  it('leaves every mock-only string out of the production bundle, and the fixture bundle proves the check works', async () => {
    const production = await bundle('production');
    for (const needle of MOCK_ONLY) expect(production(needle), needle).toEqual([]);
    // The production bundle still ships the default source's words.
    expect(production('No telemetry source configured').length).toBeGreaterThan(0);

    const fixtures = await bundle('fixtures');
    expect(fixtures(MOCK_ONLY[0]).some(file => file.endsWith('.js'))).toBe(true);
  }, 240_000);
});
