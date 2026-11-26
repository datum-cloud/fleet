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
  { name: 'capacity-list', path: '/plugins/metal-datumapis-com', ready: '[data-testid="fleet-plugin-capacity-table"] table' },
  { name: 'capacity-pool', path: '/plugins/metal-datumapis-com?namespace=fleet-provider&pool=central-metal', ready: '[data-testid="fleet-plugin-capacity-filter"]', viewports: ['desktop-light'] },
  { name: 'profile-list', path: '/plugins/metal-datumapis-com/profiles', ready: '[data-testid="fleet-plugin-profile-table"] table' },
  { name: 'class-list', path: '/plugins/metal-datumapis-com/classes', ready: '[data-testid="fleet-plugin-class-table"] table' },
  { name: 'rack-elevation', path: record('Rack', 'example-rack-a01'), ready: '.inventory-rack-layout' },
  { name: 'rack-overlap', path: record('Rack', 'example-rack-a02'), ready: '.inventory-rack-layout' },
  { name: 'rack-page-elevation', path: record('Rack', 'example-rack-a01'), ready: '.inventory-rack-stage' },
  { name: 'rack-page-selected', path: record('Rack', 'example-rack-a01', '&selectedKind=Device&selected=example-leaf-a01'), ready: '.inventory-rack-panel[data-variant="side"] .inventory-faceplate', viewports: ['desktop-light', 'desktop-dark'] },
  { name: 'rack-page-equipment', path: record('Rack', 'example-rack-a01', '&tab=equipment'), ready: '.inventory-rack-tab[data-tab="equipment"] table' },
  { name: 'rack-power-redundancy', path: record('Rack', 'example-rack-a01', '&tab=power'), ready: '.inventory-rack-tab[data-tab="power"]' },
  { name: 'rack-page-cables', path: record('Rack', 'example-rack-a01', '&tab=cables'), ready: '.inventory-rack-tab[data-tab="cables"] table' },
  { name: 'rack-page-phone-sheet', path: record('Rack', 'example-rack-a01', '&selectedKind=Device&selected=example-srv-a01-01'), ready: '.inventory-rack-panel[data-variant="sheet"] .inventory-faceplate', viewports: ['phone'] },
  { name: 'server-detail', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-connections' },
  // The secondary tab and the Record section that closes every record page.
  { name: 'device-hardware', path: record('Device', 'example-srv-a01-01', '&tab=hardware'), ready: '[data-tab="hardware"] .inventory-fact-section', element: '.inventory-record-tabs', viewports: ['desktop-light', 'desktop-dark', 'phone'] },
  // BMC discovery in the Hardware & management tab: matched, identity mismatch, key not approved.
  { name: 'device-discovery-matched', path: record('Device', 'example-srv-a01-01', '&tab=hardware'), ready: '[data-tab="hardware"] .inventory-discovery-chips', viewports: ['desktop-light', 'desktop-dark', 'phone'] },
  { name: 'device-discovery-mismatch', path: record('Device', 'example-srv-a01-02', '&tab=hardware'), ready: '[data-tab="hardware"] .inventory-discovery-chips', element: '.inventory-record-tabs', viewports: ['desktop-light', 'phone'] },
  { name: 'device-discovery-pin', path: record('Device', 'example-srv-a01-05', '&tab=hardware'), ready: '[data-tab="hardware"] .inventory-discovery-chips', viewports: ['desktop-light', 'phone'] },
  { name: 'device-discovery-none', path: record('Device', 'example-srv-a02-02', '&tab=hardware'), ready: '[data-tab="hardware"] .inventory-fact-section', element: '.inventory-record-tabs', viewports: ['desktop-light', 'phone'] },
  // Staff power: the header control and fact, the dialog and its destructive confirmation, the Operations tab, and a held server.
  { name: 'device-power-header', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-power-control button', element: '.inventory-record-header', viewports: ['desktop-light', 'desktop-dark', 'phone'] },
  { name: 'device-power-dialog', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-power-control button:not([disabled])', click: '.inventory-power-control button', viewports: ['desktop-light', 'phone'] },
  { name: 'device-power-operations', path: record('Device', 'example-srv-a01-01', '&tab=operations'), ready: '[data-tab="operations"] table', element: '.inventory-record-tabs', viewports: ['desktop-light', 'desktop-dark', 'phone'] },
  { name: 'device-power-held', path: record('Device', 'example-srv-a01-05', '&tab=operations'), ready: '[data-tab="operations"] table', viewports: ['desktop-light', 'phone'] },
  { name: 'device-location', path: record('Device', 'example-srv-a01-01', '&tab=location'), ready: '[data-tab="location"] .inventory-fields', element: '.inventory-record-tabs', viewports: ['desktop-light', 'phone'] },
  { name: 'record-footer', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-record-footer', element: '.inventory-record-footer', viewports: ['desktop-light', 'phone'] },
  // Device detail for a model without a layout (grouped tiles), and the one component table.
  { name: 'device-detail-no-layout', path: record('Device', 'example-app-a01'), ready: '.inventory-connections' },
  // `view=table` is no longer a choice: it falls back to the default page, picture and table.
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
  // A one-lane Cable is one line; a duplicate's shared end is red.
  { name: 'cable-detail-dac', path: record('Cable', 'example-dac-a01-01-eth0'), ready: '.inventory-cable-line' },
  { name: 'cable-detail-conflict', path: record('Cable', 'example-dac-a01-01-eth1-dup'), ready: '.inventory-cable-line' },
  { name: 'power-feed-detail', path: record('PowerFeed', 'example-feed-a'), ready: '.inventory-page-title' },
  { name: 'facility-detail', path: record('Facility', 'example-dc1'), ready: '.inventory-page-title' },
  { name: 'site-detail', path: record('Site', 'example-site-one'), ready: '.inventory-page-title' },
  { name: 'device-type-detail', path: record('DeviceType', 'example-server-1u'), ready: '.inventory-page-title' },
  { name: 'manufacturer-detail', path: record('Manufacturer', 'example-compute'), ready: '.inventory-page-title' },
  { name: 'site-devices', path: record('Site', 'example-site-one', '&tab=devices'), ready: '[data-tab="devices"] table' },
  { name: 'site-plans', path: record('Site', 'example-site-one', '&tab=plans'), ready: '[data-tab="plans"] table' },
  { name: 'facility-unracked', path: record('Facility', 'example-dc1', '&tab=unracked'), ready: '[data-tab="unracked"] table' },
  { name: 'facility-feeds', path: record('Facility', 'example-dc1', '&tab=feeds'), ready: '[data-tab="feeds"] table' },
  { name: 'facility-cables', path: record('Facility', 'example-dc1', '&tab=cables'), ready: '[data-tab="cables"] table' },
  { name: 'device-type-leaf', path: record('DeviceType', 'example-leaf-48x25-8x100'), ready: '.inventory-drawing' },
  { name: 'device-type-pdu', path: record('DeviceType', 'example-pdu-8'), ready: '.inventory-page-title' },
  { name: 'manufacturer-devices', path: record('Manufacturer', 'example-compute', '&tab=devices'), ready: '[data-tab="devices"] table' },
  { name: 'claim-detail', path: record('DeviceClaim', 'example-srv-a01-01-install'), ready: '.inventory-page-title' },
  { name: 'devices-list', path: `${INVENTORY}?kind=Device`, ready: '[data-testid="fleet-plugin-inventory-table"]' },
  { name: 'cables-list', path: `${INVENTORY}?kind=Cable`, ready: '[data-testid="fleet-plugin-inventory-table"]' },
  { name: 'racks-list', path: `${INVENTORY}?kind=Rack`, ready: '[data-testid="fleet-plugin-inventory-table"]' },
  { name: 'denied-peer-connections', path: record('Device', 'example-srv-a01-01', '&fixture=power-feeds-denied'), ready: '.inventory-connections', element: '.inventory-connections' },
  // Port tiles: the 1U server, the 48+8-port leaf, an open card, and a denied peer's card.
  { name: 'ports-server', path: record('Device', 'example-srv-a01-01'), ready: '.inventory-port-grid', element: '.inventory-connections', viewports: CONNECTION_VIEWPORTS },
  { name: 'ports-leaf', path: record('Device', 'example-leaf-a01'), ready: '.inventory-port-grid', element: '.inventory-connections', viewports: CONNECTION_VIEWPORTS },
  { name: 'ports-leaf-card', path: record('Device', 'example-leaf-a01'), ready: '.inventory-port-grid', click: tile('swp49'), viewports: CONNECTION_VIEWPORTS },
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
  // A server whose PSUs both reach Feed A through one PDU: "Single feed (recorded)".
  { name: 'device-single-feed', path: record('Device', 'example-srv-a01-05'), ready: '.inventory-connections' },
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
  // Imported records whose component ids match no template id: drawn by name
  // and type, with the duplicate-name PSUs in the tray.
  { name: 'faceplate-imported', path: record('Device', 'example-imported-sw-01'), ready: '.inventory-drawing', element: '.inventory-connections', viewports: CONNECTION_VIEWPORTS },
  { name: 'faceplate-imported-phone', path: record('Device', 'example-imported-sw-01'), ready: '.inventory-drawing', viewports: ['phone'] },
  { name: 'rack-panel-faceplate', path: record('Rack', 'example-rack-a01', '&selectedKind=Device&selected=example-srv-a01-01'), ready: '.inventory-faceplate .inventory-drawing', viewports: ['desktop-light', 'desktop-dark', 'phone'] },
];

/** The `telemetry=` values pages may use: the mock scenarios and `none`. */
export const TELEMETRY_SCENARIOS = ['normal', 'high-load', 'stale', 'missing', 'mismatch', 'none'];
