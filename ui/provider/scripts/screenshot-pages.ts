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
  /** Captured only for pages that list it in `viewports`. */
  optIn?: boolean;
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
  /** Viewport names to capture; defaults to every viewport that is not opt-in. */
  viewports?: string[];
  /** Selectors to click (tap, on touch viewports) in order after `ready`, such as a port tile and then its card's More. */
  click?: string | string[];
}

export const VIEWPORTS: Viewport[] = [
  { name: 'desktop-light', width: 1280, height: 900, theme: 'light' },
  { name: 'desktop-dark', width: 1280, height: 900, theme: 'dark' },
  { name: 'phone', width: 390, height: 844, theme: 'light', mobile: true, checkOverflow: true },
  { name: 'phone-dark', width: 390, height: 844, theme: 'dark', mobile: true, checkOverflow: true, optIn: true },
];

const CONNECTION_VIEWPORTS = ['desktop-light', 'desktop-dark', 'phone-dark'];
const tile = (componentID: string) => `.inventory-port[data-tile$="/${componentID}"]`;

const INVENTORY = '/plugins/metal-datumapis-com/inventory';
const record = (kind: string, name: string, extra = '') => `${INVENTORY}?kind=${kind}&record=${name}&namespace=default${extra}`;

export const PAGES: CapturePage[] = [
  { name: 'inventory-list', path: INVENTORY, ready: '[data-testid="fleet-plugin-inventory-list"] table' },
  { name: 'rack-elevation', path: record('Rack', 'example-rack-a01'), ready: '.inventory-rack-layout' },
  { name: 'rack-overlap', path: record('Rack', 'example-rack-a02'), ready: '.inventory-rack-layout' },
  { name: 'rack-page-elevation', path: record('Rack', 'example-rack-a01'), ready: '.inventory-rack-stage' },
  { name: 'rack-page-selected', path: record('Rack', 'example-rack-a01', '&selectedKind=Device&selected=example-leaf-a01'), ready: '.inventory-rack-panel[data-variant="side"] .inventory-faceplate', viewports: ['desktop-light', 'desktop-dark'] },
  { name: 'rack-page-equipment', path: record('Rack', 'example-rack-a01', '&tab=equipment'), ready: '.inventory-rack-tab[data-tab="equipment"] table' },
  { name: 'rack-page-cables', path: record('Rack', 'example-rack-a01', '&tab=cables'), ready: '.inventory-rack-tab[data-tab="cables"] table' },
  { name: 'rack-page-phone-sheet', path: record('Rack', 'example-rack-a01', '&selectedKind=Device&selected=example-srv-a01-01'), ready: '.inventory-rack-panel[data-variant="sheet"] .inventory-faceplate', viewports: ['phone'] },
  { name: 'server-detail', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-connections' },
  { name: 'leaf-connections', path: record('Device', 'example-leaf-a01'), ready: '.inventory-connections', element: '.inventory-connections' },
  // Both rack faces: full depth on both (muted far side), reversed leaf,
  // half-depth rear equipment and unknown depth.
  { name: 'rack-depth-faces', path: record('Rack', 'example-rack-a01'), ready: '.inventory-rack-faces', element: '.inventory-elevation' },
  // Ports grouped by chassis face, with derived rack faces.
  { name: 'server-connections-by-face', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-connections', element: '.inventory-connections' },
  // A Cable whose ends land on opposite rack faces carries a check note, shown in the port's card.
  { name: 'opposite-face-check', path: record('Device', 'example-app-a01'), ready: '.inventory-port-grid', click: tile('net0') },
  // Detail and list pages for each kind, for page-design review.
  { name: 'cable-detail', path: record('Cable', 'example-brk-swp49'), ready: '.inventory-page-title' },
  { name: 'power-feed-detail', path: record('PowerFeed', 'example-feed-a'), ready: '.inventory-page-title' },
  { name: 'facility-detail', path: record('Facility', 'example-dc1'), ready: '.inventory-page-title' },
  { name: 'site-detail', path: record('Site', 'example-site-one'), ready: '.inventory-page-title' },
  { name: 'device-type-detail', path: record('DeviceType', 'example-server-1u'), ready: '.inventory-page-title' },
  { name: 'manufacturer-detail', path: record('Manufacturer', 'example-compute'), ready: '.inventory-page-title' },
  { name: 'claim-detail', path: record('DeviceClaim', 'example-srv-a01-01-install'), ready: '.inventory-page-title' },
  { name: 'devices-list', path: `${INVENTORY}?kind=Device`, ready: '[data-testid="fleet-plugin-inventory-table"]' },
  { name: 'cables-list', path: `${INVENTORY}?kind=Cable`, ready: '[data-testid="fleet-plugin-inventory-table"]' },
  { name: 'racks-list', path: `${INVENTORY}?kind=Rack`, ready: '[data-testid="fleet-plugin-inventory-table"]' },
  { name: 'denied-peer-connections', path: record('Device', 'example-srv-a01-01', '&fixture=power-feeds-denied'), ready: '.inventory-connections', element: '.inventory-connections' },
  // Port tiles: the 1U server, the 48+8-port leaf, an open card, and a denied peer's card.
  { name: 'ports-server', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-port-grid', element: '.inventory-connections', viewports: CONNECTION_VIEWPORTS },
  { name: 'ports-leaf', path: record('Device', 'example-leaf-a01'), ready: '.inventory-port-grid', element: '.inventory-connections', viewports: CONNECTION_VIEWPORTS },
  { name: 'ports-leaf-card', path: record('Device', 'example-leaf-a01'), ready: '.inventory-port-grid', click: tile('swp49'), viewports: CONNECTION_VIEWPORTS },
  // The same breakout card with More open: record names, connector and source.
  { name: 'ports-leaf-card-more', path: record('Device', 'example-leaf-a01'), ready: '.inventory-port-grid', click: [tile('swp49'), '.inventory-port-card-more > button'], viewports: CONNECTION_VIEWPORTS },
  { name: 'ports-server-conflict-card', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-port-grid', click: tile('eth1'), viewports: CONNECTION_VIEWPORTS },
  { name: 'ports-denied-peer-card', path: record('Device', 'example-srv-a01-01', '&fixture=power-feeds-denied'), ready: '.inventory-port-grid', click: tile('psu-2'), viewports: CONNECTION_VIEWPORTS },
  // The same shared faceplate in the rack side panel: the leaf's unlabelled tiles, and an open card.
  { name: 'rack-leaf-faceplate', path: record('Rack', 'example-rack-a01', '&selectedKind=Device&selected=example-leaf-a01'), ready: '.inventory-faceplate .inventory-port-grid', element: '.inventory-faceplate', viewports: CONNECTION_VIEWPORTS },
  { name: 'rack-leaf-card', path: record('Rack', 'example-rack-a01', '&selectedKind=Device&selected=example-leaf-a01'), ready: '.inventory-faceplate .inventory-port-grid', click: `.inventory-faceplate ${tile('swp37')}`, viewports: CONNECTION_VIEWPORTS },
];
