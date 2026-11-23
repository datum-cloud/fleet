// The pages `pnpm screenshots` captures. Add a view by appending an entry.
//
// Each page is captured at every viewport in VIEWPORTS (desktop light,
// desktop dark, phone) unless `viewports` narrows it. Paths are preview URLs
// served from the fixture build; record names come from
// src/lib/inventory-fixtures.ts (FIXTURE_NAMES), and a unit test checks that
// every `record=` named here exists in the fixture dataset.
//
// Keep this file free of imports: Node runs the capture script directly by
// stripping types, and the app's modules use bundler-style imports.

export interface Viewport {
  name: string;
  width: number;
  height: number;
  theme: 'light' | 'dark';
  mobile?: boolean;
  /** Phone-width checks: the page must not scroll horizontally. */
  checkOverflow?: boolean;
}

export interface CapturePage {
  /** File name stem: <name>-<viewport>.png. Lowercase and hyphenated. */
  name: string;
  /** Path and query on the preview server, without `theme`. */
  path: string;
  /** CSS selector that must be visible before capture. */
  ready: string;
  /** Capture only this element instead of the full page. */
  element?: string;
  /** Viewport names to capture; defaults to all. */
  viewports?: string[];
}

export const VIEWPORTS: Viewport[] = [
  { name: 'desktop-light', width: 1280, height: 900, theme: 'light' },
  { name: 'desktop-dark', width: 1280, height: 900, theme: 'dark' },
  { name: 'phone', width: 390, height: 844, theme: 'light', mobile: true, checkOverflow: true },
];

const INVENTORY = '/plugins/metal-datumapis-com/inventory';
const record = (kind: string, name: string, extra = '') => `${INVENTORY}?kind=${kind}&record=${name}&namespace=default${extra}`;

export const PAGES: CapturePage[] = [
  { name: 'inventory-list', path: INVENTORY, ready: '[data-testid="fleet-plugin-inventory-list"] table' },
  { name: 'rack-elevation', path: record('Rack', 'example-rack-a01'), ready: '.inventory-rack-layout' },
  { name: 'rack-overlap', path: record('Rack', 'example-rack-a02'), ready: '.inventory-rack-layout' },
  { name: 'server-detail', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-connections' },
  { name: 'leaf-connections', path: record('Device', 'example-leaf-a01'), ready: '.inventory-connections', element: '.inventory-connections' },
  { name: 'denied-peer-connections', path: record('Device', 'example-srv-a01-01', '&fixture=power-feeds-denied'), ready: '.inventory-connections', element: '.inventory-connections' },
];
