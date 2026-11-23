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
  { name: 'rack-power-redundancy', path: record('Rack', 'example-rack-a01', '&tab=power'), ready: '.inventory-rack-tab[data-tab="power"]' },
  { name: 'rack-page-cables', path: record('Rack', 'example-rack-a01', '&tab=cables'), ready: '.inventory-rack-tab[data-tab="cables"] table' },
  { name: 'rack-page-phone-sheet', path: record('Rack', 'example-rack-a01', '&selectedKind=Device&selected=example-srv-a01-01'), ready: '.inventory-rack-panel[data-variant="sheet"] .inventory-faceplate', viewports: ['phone'] },
  { name: 'server-detail', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-connections' },
  // Device detail for a model without a layout (grouped tiles), and the one component table.
  { name: 'device-detail-no-layout', path: record('Device', 'example-app-a01'), ready: '.inventory-connections' },
  { name: 'device-table', path: record('Device', 'example-srv-a01-01', '&view=table'), ready: '.inventory-connection-table', element: '.inventory-connections', viewports: CONNECTION_VIEWPORTS },
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
  // Power: the feed's power path with synthetic telemetry (`telemetry=` picks a
  // mock scenario; fixture builds only), and the production-like view with none.
  { name: 'power-feed-normal', path: record('PowerFeed', 'example-feed-a'), ready: '.inventory-power-path' },
  { name: 'power-feed-high-load', path: record('PowerFeed', 'example-feed-a', '&telemetry=high-load'), ready: '.inventory-power-path' },
  { name: 'power-feed-stale', path: record('PowerFeed', 'example-feed-a', '&telemetry=stale'), ready: '.inventory-power-path' },
  { name: 'power-feed-mismatch', path: record('PowerFeed', 'example-feed-a', '&telemetry=mismatch'), ready: '.inventory-power-path' },
  { name: 'power-feed-no-telemetry', path: record('PowerFeed', 'example-feed-a', '&telemetry=none'), ready: '.inventory-power-path' },
  { name: 'power-feed-outlet-card', path: record('PowerFeed', 'example-feed-a'), ready: '.inventory-power-path', click: `.inventory-power-strip[aria-label="PDU a01 (rear left) outlets"] ${tile('outlet-1')}` },
  { name: 'device-psu-card', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-port-grid', click: tile('psu-1') },
  // The outlet card with More open: owner-kind wording and the telemetry source.
  { name: 'power-feed-outlet-card-more', path: record('PowerFeed', 'example-feed-a'), ready: '.inventory-power-path', click: [`.inventory-power-strip[aria-label="Power outlets"] ${tile('outlet-1')}`, '.inventory-port-card-more > button'] },
  // A server whose PSUs both reach Feed A through one PDU: "Single feed (recorded)".
  { name: 'device-single-feed', path: record('Device', 'example-srv-a01-05'), ready: '.inventory-connections' },
  // Blank units collapse into one row per run; a click expands the run in place on both faces.
  { name: 'rack-elevation-expanded', path: record('Rack', 'example-rack-a01'), ready: '.inventory-rack-run-button', element: '.inventory-elevation', click: '.inventory-rack-units[data-face="front"] .inventory-rack-run-button[data-run="U22-U39"]' },
  // Faceplates drawn from DeviceType layouts: the 1U server and the 48+8 leaf,
  // an open card on a dense face, the no-layout fallback, mini faceplates in
  // the elevation at each level of detail, and the drawing in the rack panel.
  { name: 'faceplate-server', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-drawing', element: '.inventory-connections', viewports: CONNECTION_VIEWPORTS },
  { name: 'faceplate-storage', path: record('Device', 'example-sto-a01-03'), ready: '.inventory-drawing', element: '.inventory-connections', viewports: ['desktop-light'] },
  { name: 'faceplate-switch', path: record('Device', 'example-leaf-a01'), ready: '.inventory-drawing', element: '.inventory-connections', viewports: CONNECTION_VIEWPORTS },
  { name: 'faceplate-switch-card', path: record('Device', 'example-leaf-a01'), ready: '.inventory-drawing', click: `.inventory-drawing ${tile('swp49')}`, viewports: CONNECTION_VIEWPORTS },
  { name: 'faceplate-patch-pdu', path: record('Device', 'example-patch-a01'), ready: '.inventory-drawing', element: '.inventory-connections', viewports: ['desktop-light'] },
  { name: 'faceplate-pdu', path: record('Device', 'example-pdu-a01'), ready: '.inventory-drawing', element: '.inventory-connections', viewports: ['desktop-dark'] },
  { name: 'faceplate-fallback', path: record('Device', 'example-app-a01'), ready: '.inventory-faceplate-fallback', element: '.inventory-connections', viewports: CONNECTION_VIEWPORTS },
  { name: 'rack-mini-reduced', path: record('Rack', 'example-rack-a01'), ready: '.inventory-mini-faceplate', element: '.inventory-elevation', viewports: ['desktop-light', 'desktop-dark'] },
  { name: 'rack-mini-full', path: record('Rack', 'example-rack-a01', '&detail=full'), ready: '.inventory-mini-faceplate[data-level="full"]', element: '.inventory-elevation', viewports: ['desktop-light', 'desktop-dark'] },
  { name: 'rack-mini-silhouette', path: record('Rack', 'example-rack-a01', '&detail=silhouette'), ready: '.inventory-mini-faceplate[data-level="silhouette"]', element: '.inventory-elevation', viewports: ['desktop-dark'] },
  // Imported records whose component ids match no template id: drawn by name
  // and type, the duplicate-name PSUs in the tray, and the card's More saying so.
  { name: 'faceplate-imported', path: record('Device', 'example-imported-sw-01'), ready: '.inventory-drawing', element: '.inventory-connections', viewports: CONNECTION_VIEWPORTS },
  { name: 'faceplate-imported-phone', path: record('Device', 'example-imported-sw-01'), ready: '.inventory-drawing', viewports: ['phone'] },
  { name: 'faceplate-imported-card-more', path: record('Device', 'example-imported-sw-01'), ready: '.inventory-drawing', click: [`.inventory-drawing ${tile('interfaces-9101')}`, '.inventory-port-card-more > button'], viewports: ['desktop-light', 'desktop-dark'] },
  { name: 'rack-panel-faceplate', path: record('Rack', 'example-rack-a01', '&selectedKind=Device&selected=example-srv-a01-01'), ready: '.inventory-faceplate .inventory-drawing', viewports: ['desktop-light', 'desktop-dark', 'phone'] },
];

/** The `telemetry=` values pages may use: the mock scenarios and `none`. */
export const TELEMETRY_SCENARIOS = ['normal', 'high-load', 'stale', 'missing', 'mismatch', 'none'];
