// Captures the fixture preview for review: `pnpm screenshots`.
//
// Builds the standalone harness with synthetic inventory (--mode fixtures),
// serves it with `vite preview`, and captures every page in
// scripts/screenshot-pages.ts at every viewport. Fails when a page logs a
// console error, throws, calls the API, renders without the fixture banner,
// or scrolls horizontally at phone width.
//
// Environment:
//   SCREENSHOT_DIR         output directory (default: ui/provider/screenshots)
//   SCREENSHOT_PORT        preview port (default: 7782, beside `pnpm preview`)
//   SCREENSHOT_ONLY        comma-separated page names to capture
//   SCREENSHOT_SKIP_BUILD  "1" to reuse dist/ from `pnpm build:fixtures`
//
// Node runs this file directly by stripping its types.

import { mkdir, readdir, rm } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, type Browser, type Page } from 'playwright';
import { build, preview } from 'vite';
import { PAGES, VIEWPORTS, type CapturePage, type Viewport } from './screenshot-pages.ts';

const ROOT = resolve(import.meta.dirname, '..');
const OUT = resolve(process.env.SCREENSHOT_DIR || resolve(ROOT, 'screenshots'));
const PORT = Number(process.env.SCREENSHOT_PORT || 7782);
const ONLY = new Set((process.env.SCREENSHOT_ONLY ?? '').split(',').map(name => name.trim()).filter(Boolean));
const FIXTURE_BANNER = 'Synthetic example data';

const pages = PAGES.filter(page => !ONLY.size || ONLY.has(page.name));
if (ONLY.size && pages.length !== ONLY.size) {
  const known = new Set(PAGES.map(page => page.name));
  console.error(`Unknown page in SCREENSHOT_ONLY: ${[...ONLY].filter(name => !known.has(name)).join(', ')}`);
  process.exit(2);
}

async function capture(browser: Browser, base: string, entry: CapturePage, viewport: Viewport): Promise<{ file?: string; problems: string[] }> {
  const context = await browser.newContext({
    viewport: { width: viewport.width, height: viewport.height },
    deviceScaleFactor: viewport.mobile ? 2 : 1,
    isMobile: viewport.mobile ?? false,
    hasTouch: viewport.mobile ?? false,
    colorScheme: viewport.theme,
    reducedMotion: 'reduce',
  });
  const page: Page = await context.newPage();
  const problems: string[] = [];
  page.on('console', message => { if (message.type() === 'error') problems.push(`console error: ${message.text()}`); });
  page.on('pageerror', error => problems.push(`uncaught: ${error.message}`));
  page.on('request', request => { if (new URL(request.url()).pathname.startsWith('/api/')) problems.push(`API request in fixture preview: ${request.url()}`); });

  const url = new URL(entry.path, base);
  url.searchParams.set('theme', viewport.theme);
  const path = resolve(OUT, `${entry.name}-${viewport.name}.png`);
  let file: string | undefined;
  try {
    await page.goto(url.href, { waitUntil: 'networkidle' });
    await page.locator(entry.ready).first().waitFor({ state: 'visible', timeout: 15_000 });
    await page.evaluate(() => document.fonts.ready);
    const state = await page.evaluate(banner => ({
      dark: document.documentElement.classList.contains('dark'),
      fixtures: document.body.innerText.includes(banner),
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
      wide: [...document.querySelectorAll('body *')]
        .filter(element => element.getBoundingClientRect().right > document.documentElement.clientWidth + 1)
        .filter(element => !element.parentElement || element.parentElement.getBoundingClientRect().right <= document.documentElement.clientWidth + 1)
        .slice(0, 5)
        .map(element => `${element.tagName.toLowerCase()}${element.className && typeof element.className === 'string' ? `.${element.className.trim().split(/\s+/).slice(0, 3).join('.')}` : ''} (right ${Math.round(element.getBoundingClientRect().right)}px)`),
    }), FIXTURE_BANNER);
    if (!state.fixtures) problems.push('the page is not a fixture build (banner missing); run without SCREENSHOT_SKIP_BUILD');
    if (state.dark !== (viewport.theme === 'dark')) problems.push(`theme is not ${viewport.theme}`);
    if (viewport.checkOverflow && state.scrollWidth > state.clientWidth) {
      problems.push(`horizontal overflow at ${viewport.width}px: document is ${state.scrollWidth}px wide${state.wide.length ? `; widest: ${state.wide.join(', ')}` : ''}`);
    }
    const target = entry.element ? page.locator(entry.element).first() : page;
    await target.screenshot({ path, animations: 'disabled', ...(entry.element ? {} : { fullPage: true }) });
    file = path;
  } catch (error) {
    problems.push(error instanceof Error ? error.message.split('\n')[0] : String(error));
  } finally {
    await context.close();
  }
  return { file, problems };
}

async function main() {
  if (process.env.SCREENSHOT_SKIP_BUILD !== '1') {
    process.env.VITE_FLEET_FIXTURES = 'true';
    await build({ root: ROOT, mode: 'fixtures', logLevel: 'warn' });
  }
  const server = await preview({ root: ROOT, logLevel: 'warn', preview: { port: PORT, strictPort: true, open: false } });
  const base = server.resolvedUrls?.local[0] ?? `http://localhost:${PORT}/`;
  let browser: Browser | undefined;
  const failures: string[] = [];
  const written: string[] = [];
  try {
    browser = await chromium.launch().catch(error => {
      throw new Error(`${error instanceof Error ? error.message.split('\n')[0] : error}\nInstall the browser with: pnpm exec playwright install chromium`);
    });
    // Clear earlier captures only: SCREENSHOT_DIR may point at a shared directory.
    await mkdir(OUT, { recursive: true });
    for (const name of await readdir(OUT)) if (PAGES.some(page => name.startsWith(`${page.name}-`)) && name.endsWith('.png')) await rm(resolve(OUT, name));
    for (const entry of pages) {
      for (const viewport of VIEWPORTS.filter(item => !entry.viewports || entry.viewports.includes(item.name))) {
        const { file, problems } = await capture(browser, base, entry, viewport);
        failures.push(...[...new Set(problems)].map(problem => `${entry.name} @ ${viewport.name}: ${problem}`));
        if (file) written.push(file);
      }
    }
  } finally {
    await browser?.close();
    await server.close();
  }
  for (const file of written) console.log(file);
  if (failures.length) {
    console.error(`\n${failures.length} problem(s):\n${failures.map(failure => `  - ${failure}`).join('\n')}`);
    process.exit(1);
  }
  console.log(`\n${written.length} screenshot(s) in ${OUT}`);
}

await main();
