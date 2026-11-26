// What BMC discovery wrote to Device.status, shaped for the Device page.
// Discovery is the only writer of status (internal/bmcdiscovery); this module
// only reads it. The comparisons mirror internal/bmcdiscovery/status.go so the
// page marks the same fields the IdentityMatched condition does.
import type { Condition } from '../../../shared/conditions';
import type { Device, ObservedNetworkInterface, ObservedSystem } from '../../../shared/fleet-api';

export type Tone = 'danger' | 'warning' | undefined;

/** The five Device status conditions, in the order discovery evaluates them. */
export const DISCOVERY_CONDITIONS = [
  { type: 'Reachable', word: 'Reachable' },
  { type: 'Authenticated', word: 'Auth' },
  { type: 'Discovered', word: 'Inventory' },
  { type: 'IdentityMatched', word: 'Identity' },
  { type: 'PublicKeyUnique', word: 'Key unique' },
] as const;

export interface ConditionChip {
  type: string;
  word: string;
  status: Condition['status'];
  tone: Tone;
  /** "Reachable: False · ConnectFailed · no TLS handshake completed …" */
  reason: string;
}

/** True when discovery has written anything for this Device. */
export const discovered = (device: Device) => !!device.status?.discovery || !!device.status?.conditions?.length;

const find = (device: Device, type: string) => device.status?.conditions?.find(condition => condition.type === type);

/** One chip per condition. False is red, Unknown (or absent) amber, True plain. */
export function conditionChips(device: Device): ConditionChip[] {
  return DISCOVERY_CONDITIONS.map(({ type, word }) => {
    const condition = find(device, type);
    const status = condition?.status ?? 'Unknown';
    const reason = [`${type}: ${status}`, condition ? condition.reason : 'not reported', condition?.message].filter(Boolean).join(' · ');
    return { type, word, status, tone: status === 'False' ? 'danger' : status === 'Unknown' ? 'warning' : undefined, reason };
  });
}

/**
 * The one header chip: the first failing condition in discovery order, else
 * the first unknown one, else "BMC ✓". Undefined before discovery has run.
 */
export function bmcChip(device: Device): { label: string; tone: Tone; reason: string } | undefined {
  if (!discovered(device)) return undefined;
  const chips = conditionChips(device);
  const failed = chips.find(chip => chip.tone === 'danger');
  if (failed) {
    const reason = find(device, failed.type)?.reason;
    const label = failed.type === 'Reachable' ? 'BMC unreachable'
      : failed.type === 'Authenticated' ? reason === 'PinNotApproved' ? 'BMC key not approved' : reason === 'PinMismatch' ? 'BMC key changed' : 'BMC auth failed'
        : failed.type === 'Discovered' ? 'BMC not read'
          : failed.type === 'IdentityMatched' ? 'Identity mismatch'
            : 'BMC key shared';
    return { label, tone: 'danger', reason: failed.reason };
  }
  const unknown = chips.find(chip => chip.tone === 'warning');
  if (unknown) return { label: 'BMC ?', tone: 'warning', reason: unknown.reason };
  return { label: 'BMC ✓', tone: undefined, reason: chips.map(chip => chip.reason).join('\n') };
}

/**
 * Whether the observed TLS key is approved as the credential pin. Only the
 * Authenticated condition says so: True means the pinned connection worked.
 */
export function keyApproval(device: Device): { label: string; tone: Tone; reason: string } | undefined {
  const condition = find(device, 'Authenticated');
  if (!condition) return undefined;
  const reason = [condition.reason, condition.message].filter(Boolean).join(' · ');
  if (condition.status === 'True') return { label: 'Approved', tone: undefined, reason };
  if (condition.reason === 'PinNotApproved') return { label: 'Not approved', tone: 'warning', reason };
  if (condition.reason === 'PinMismatch') return { label: 'Does not match the approved key', tone: 'danger', reason };
  return undefined;
}

const DAY = 86_400_000;

/** TLS certificate expiry: red once expired, amber within 30 days. */
export function tlsExpiry(notAfter: string | undefined, now: number): { label: string; tone: Tone; title: string } | undefined {
  const at = notAfter ? Date.parse(notAfter) : NaN;
  if (!Number.isFinite(at)) return undefined;
  const days = Math.floor((at - now) / DAY);
  const date = notAfter!.slice(0, 10);
  if (at <= now) return { label: `Expired ${date}`, tone: 'danger', title: notAfter! };
  if (at - now <= 30 * DAY) return { label: `${date} · ${days < 1 ? 'under a day' : `${days} d`}`, tone: 'warning', title: `Expires ${notAfter}` };
  return { label: date, tone: undefined, title: `Expires ${notAfter}` };
}

/** "3f9a1c2e…77ab": the start and end of a fingerprint. */
export const shortFingerprint = (value: string) => value.length > 16 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value;

const POWER_WORDS: Record<string, string> = { On: 'On', Off: 'Off', PoweringOn: 'Starting', PoweringOff: 'Stopping', Paused: 'Paused' };
export const powerWord = (state?: string) => state ? POWER_WORDS[state] ?? state : undefined;

/** Redfish LinkStatus as a word, amber unless the link is up. */
export function linkState(nic: ObservedNetworkInterface): { label: string; tone: Tone } {
  const status = nic.linkStatus ?? '';
  if (/^(linkup|up)$/i.test(status)) return { label: 'Up', tone: undefined };
  if (/^(linkdown|down)$/i.test(status)) return { label: 'Down', tone: 'warning' };
  if (/^nolink$/i.test(status)) return { label: 'No link', tone: 'warning' };
  return { label: status || 'Not reported', tone: 'warning' };
}

export const speedWords = (mbps?: number) => !mbps ? undefined : mbps >= 1000 ? `${mbps / 1000} Gb/s` : `${mbps} Mb/s`;

// ---- Identity: expected (spec) next to observed (discovery) ----

const normalize = (value?: string) => (value ?? '').toLowerCase().split(/\s+/).filter(Boolean).join(' ');
/** A vendor family prefix is allowed on either side, as in modelMatches. */
const modelMatches = (expected?: string, observed?: string) => {
  const e = normalize(expected), o = normalize(observed);
  return e === o || (!!e && o.endsWith(` ${e}`)) || (!!o && e.endsWith(` ${o}`));
};
/** Some vendors report the chassis service tag as the SKU, as in serialMatches. */
const serialMatches = (expected: string | undefined, system: ObservedSystem) => {
  const e = normalize(expected);
  return !!e && (e === normalize(system.serialNumber) || e === normalize(system.sku));
};

/**
 * The System discovery compares with spec: the one reporting the spec system
 * UUID, else the spec serial, else the only System. Undefined when none or
 * more than one could be this Device.
 */
export function comparedSystem(device: Device): ObservedSystem | undefined {
  const systems = device.status?.discovery?.inventory?.systems ?? [];
  const want = device.spec.identity;
  const candidates = want.systemUUID ? systems.filter(system => system.uuid === want.systemUUID)
    : want.serial ? systems.filter(system => serialMatches(want.serial, system))
      : systems;
  if (candidates.length === 1) return candidates[0];
  return systems.length === 1 ? systems[0] : undefined;
}

/**
 * Why an observation can't be compared with spec: it was read from an endpoint
 * other than spec.bmc.endpoint, or spec records no endpoint. Discovery reports
 * IdentityMatched Unknown with reason StaleObservation then, so the page marks
 * nothing as a disagreement.
 */
export function staleReason(observation: { endpoint: string } | undefined, device: Device): string | undefined {
  const expected = device.spec.bmc?.endpoint;
  if (!observation || observation.endpoint === expected) return undefined;
  return `Read from ${observation.endpoint}; spec.bmc.endpoint is ${expected ?? 'not set'}`;
}

export interface IdentityRow {
  field: string;
  expected?: string;
  observed?: string;
  /** Set when the two disagree; the reason is the tooltip. */
  mismatch?: string;
  /** Set when the observation came from another endpoint: nothing is compared, and the reason is the tooltip. */
  stale?: string;
}

const FIELD_REASON: Record<string, string> = { SerialMismatch: 'Serial', ModelMismatch: 'Model', SystemUUIDMismatch: 'System UUID' };

/**
 * Serial, model, manufacturer, system UUID and SKU. Power is the header's. A field is red when
 * IdentityMatched names it in a False reason, or when the two values differ by
 * the rule discovery uses. Manufacturer is shown but not compared, as in
 * discovery. Rows with neither value are left out.
 */
export function identityRows(device: Device): IdentityRow[] {
  const system = comparedSystem(device);
  if (!system) return [];
  const want = device.spec.identity;
  const matched = find(device, 'IdentityMatched');
  const flagged = matched?.status === 'False' && matched.reason ? FIELD_REASON[matched.reason] : undefined;
  const stale = staleReason(device.status?.discovery?.inventory, device);
  const differs = (field: string, expected: string | undefined, observed: string | undefined, same: boolean) => {
    if (flagged === field) return matched?.message ?? matched?.reason;
    if (stale) return undefined;
    if (!expected || same) return undefined;
    return observed ? `Discovery reports ${observed}; spec records ${expected}` : `Discovery reports no ${field.toLowerCase()}; spec records ${expected}`;
  };
  const rows: IdentityRow[] = [
    { field: 'Serial', expected: want.serial, observed: system.serialNumber, mismatch: differs('Serial', want.serial, system.serialNumber, serialMatches(want.serial, system)) },
    { field: 'Model', expected: want.model, observed: system.model, mismatch: differs('Model', want.model, system.model, modelMatches(want.model, system.model)) },
    { field: 'Manufacturer', expected: want.manufacturer, observed: system.manufacturer },
    { field: 'System UUID', expected: want.systemUUID, observed: system.uuid, mismatch: differs('System UUID', want.systemUUID, system.uuid, want.systemUUID === system.uuid) },
    { field: 'SKU', observed: system.sku },
  ];
  return rows.filter(row => row.expected || row.observed).map(row => stale && row.observed ? { ...row, stale } : row);
}

// ---- MAC addresses ----

const mac = (value?: string) => (value ?? '').toLowerCase().replace(/[^0-9a-f]/g, '');

/** Host NICs: the compared System's, or every System's when none is singled out. */
export function hostNics(device: Device): (ObservedNetworkInterface & { system?: string })[] {
  const systems = device.status?.discovery?.inventory?.systems ?? [];
  const system = comparedSystem(device);
  if (system) return system.networkInterfaces ?? [];
  return systems.flatMap(item => (item.networkInterfaces ?? []).map(nic => ({ ...nic, system: item.id })));
}

/**
 * Spec interfaces whose MAC no host NIC reports, keyed by interface name, with
 * the reason. Empty before an inventory read, so nothing is marked unobserved
 * until discovery has looked.
 */
export function unobservedMacs(device: Device): Map<string, string> {
  const out = new Map<string, string>();
  const inventory = device.status?.discovery?.inventory;
  if (!inventory || staleReason(inventory, device)) return out;
  const seen = new Set(hostNics(device).map(nic => mac(nic.macAddress)).filter(Boolean));
  for (const iface of device.spec.interfaces ?? []) if (iface.macAddress && !seen.has(mac(iface.macAddress))) out.set(iface.name, 'No host NIC reports this MAC address');
  return out;
}

/** The spec interface names recording an observed MAC address. Matching is by address only, never by name. */
export function specNamesFor(device: Device, macAddress?: string): string[] {
  const key = mac(macAddress);
  return key ? (device.spec.interfaces ?? []).filter(iface => mac(iface.macAddress) === key).map(iface => iface.name) : [];
}
