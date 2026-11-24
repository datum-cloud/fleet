// The Device page's view of BMC discovery: Device.status, next to the spec it
// is compared with. See "Device page" in docs/development/staff-inventory-ui.md.
// Red is a disagreement or failure, amber needs checking, and OK stays plain.
import { Button } from '@datum-cloud/datum-ui/button';
import { useState, type ReactNode } from 'react';
import type { Device } from '../../../shared/fleet-api';
import {
  bmcChip, conditionChips, hostNics, identityRows, keyApproval, linkState, shortFingerprint, specNamesFor, speedWords, staleReason, tlsExpiry, unobservedMacs, type Tone,
} from '../lib/device-discovery';
import { interfaceAddresses } from '../lib/device-connections';
import { formatAge } from '../lib/telemetry';
import { Facts, SmallTable } from './inventory-record-layout';

/** A value that carries its reason in the tooltip and in its accessible text. */
function Marked({ tone, reason, children }: { tone: Tone; reason?: string; children: ReactNode }) {
  if (!tone) return <span title={reason}>{children}</span>;
  return <span className="inventory-discovery-mark" data-tone={tone} title={reason}>
    <span aria-hidden="true">{tone === 'danger' ? '! ' : ''}</span>{children}
    {reason && <span className="inventory-visually-hidden">. {tone === 'danger' ? 'Records disagree' : 'Check'}: {reason}</span>}
  </span>;
}

/** The header's one BMC chip; nothing before discovery has run. */
export function BmcChip({ device }: { device: Device }) {
  const chip = bmcChip(device);
  if (!chip) return null;
  return <div><dt className="inventory-visually-hidden">BMC</dt><dd>
    <span className="inventory-discovery-chip" data-tone={chip.tone} title={chip.reason}>{chip.label}<span className="inventory-visually-hidden">. {chip.reason}</span></span>
  </dd></div>;
}

/** The five conditions as one line of chips; the reason and message are each chip's tooltip. */
export function ConditionChips({ device }: { device: Device }) {
  return <ul className="inventory-discovery-chips" aria-label="Discovery conditions">
    {conditionChips(device).map(chip => <li key={chip.type} className="inventory-discovery-chip" data-tone={chip.tone} data-status={chip.status} title={chip.reason}>
      <span aria-hidden="true">{chip.status === 'False' ? '✕ ' : chip.status === 'Unknown' ? '? ' : ''}</span>{chip.word}
      <span className="inventory-visually-hidden">. {chip.reason}</span>
    </li>)}
  </ul>;
}

function CopyValue({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return <Button type="secondary" theme="outline" size="xs" htmlType="button" className="inventory-copy" aria-label={`Copy ${label}`} title={copied ? 'Copied' : `Copy ${label}`}
    onClick={() => { void navigator.clipboard?.writeText(value).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500); }, () => undefined); }}>
    {copied ? 'Copied' : 'Copy'}
  </Button>;
}

/** Discovery health: conditions, last attempt, TLS key, expiry and approval. */
export function DiscoveryHealth({ device, now }: { device: Device; now: number }) {
  const discovery = device.status?.discovery;
  const attempt = discovery?.lastAttemptAt ? Date.parse(discovery.lastAttemptAt) : NaN;
  const tls = discovery?.tls;
  const expiry = tlsExpiry(tls?.notAfter, now);
  const approval = keyApproval(device);
  // The endpoint is spec.bmc.endpoint's row unless the last attempt went elsewhere.
  const otherEndpoint = discovery?.lastAttemptEndpoint && discovery.lastAttemptEndpoint !== device.spec.bmc?.endpoint ? discovery.lastAttemptEndpoint : undefined;
  return <>
    <ConditionChips device={device} />
    <Facts label="Discovery" items={[
      { label: 'Last attempt', value: Number.isFinite(attempt) && <>
        <time dateTime={discovery!.lastAttemptAt} title={discovery!.lastAttemptAt}>{formatAge((now - attempt) / 1000)}</time>
        {otherEndpoint && <> · <Marked tone="warning" reason={`spec.bmc.endpoint is ${device.spec.bmc?.endpoint ?? 'not set'}`}>{otherEndpoint}</Marked></>}
      </> },
      { label: 'TLS key', value: tls && <span className="inventory-discovery-key">
        {/* A key read from another endpoint is not this BMC's: mark it before anyone approves it. */}
        <Marked tone={staleReason(tls, device) ? 'warning' : undefined} reason={staleReason(tls, device)}><code title={tls.publicKeySHA256}>{shortFingerprint(tls.publicKeySHA256)}</code></Marked>
        <span className="inventory-visually-hidden"> SHA-256 {tls.publicKeySHA256}</span>
        <CopyValue value={tls.publicKeySHA256} label="TLS key fingerprint" />
      </span> },
      { label: 'Key expiry', value: expiry && <Marked tone={expiry.tone} reason={expiry.title}>{expiry.label}</Marked> },
      { label: 'Key approval', value: approval && <Marked tone={approval.tone} reason={approval.reason}>{approval.label}</Marked> },
    ]} />
  </>;
}

/** Field | Expected (spec) | Observed (discovery); a disagreement is red with its reason. */
export function IdentityComparison({ device }: { device: Device }) {
  const rows = identityRows(device);
  if (!rows.length) return null;
  return <div className="inventory-discovery-identity"><SmallTable label="Identity" head={['Field', 'Expected', 'Observed']} rows={rows.map(row => [
    row.field,
    <span className="inventory-numeric">{row.expected}</span>,
    <span className="inventory-numeric"><Marked tone={row.mismatch ? 'danger' : row.stale ? 'warning' : undefined} reason={row.mismatch ?? row.stale}>{row.observed}</Marked></span>,
  ])} /></div>;
}

function nicRows(nics: ReturnType<typeof hostNics>, mark?: (macAddress?: string) => ReactNode) {
  const speed = nics.some(nic => nic.speedMbps);
  const system = nics.some(nic => nic.system);
  const head = [...(system ? ['System'] : []), 'NIC', 'MAC address', 'Link', ...(speed ? ['Speed'] : [])];
  const rows = nics.map(nic => {
    const link = linkState(nic);
    return [
      ...(system ? [nic.system] : []),
      nic.id,
      <span className="inventory-numeric">{nic.macAddress}{mark?.(nic.macAddress)}</span>,
      <Marked tone={link.tone} reason={nic.linkStatus}>{link.label}</Marked>,
      ...(speed ? [<span className="inventory-numeric">{speedWords(nic.speedMbps)}</span>] : []),
    ];
  });
  return { head, rows };
}

/**
 * Spec interfaces next to observed host NICs. Names are never matched: a MAC
 * address an observed NIC shares with a spec interface names that interface,
 * and a spec MAC no NIC reports is amber. Spec interfaces that match a
 * component keep their MAC in the component table, so only the others are
 * listed here; with none, only the observed NICs show.
 */
export function NicComparison({ device }: { device: Device }) {
  const nics = hostNics(device);
  const spec = interfaceAddresses(device).unplaced;
  if (!device.status?.discovery?.inventory || (!nics.length && !spec.length)) return null;
  const unseen = unobservedMacs(device);
  const observed = nicRows(nics, address => {
    const names = specNamesFor(device, address);
    return names.length ? <span className="inventory-muted" title={`spec.interfaces records this MAC address on ${names.join(', ')}`}> · {names.join(', ')}</span> : null;
  });
  const observedTable = nics.length ? <SmallTable label="Host NICs" head={observed.head} rows={observed.rows} /> : <p className="inventory-muted">No host NICs reported</p>;
  if (!spec.length) return observedTable;
  return <div className="inventory-discovery-pair">
    <SmallTable label="Interfaces not in the component list" head={['Interface', 'MAC address']} rows={spec.map(iface => [
      iface.name,
      iface.macAddress ? <span className="inventory-numeric"><Marked tone={unseen.has(iface.name) ? 'warning' : undefined} reason={unseen.get(iface.name)}>{iface.macAddress}</Marked></span> : <span className="inventory-muted">Not recorded</span>,
    ])} />
    {observedTable}
  </div>;
}

/** The BMC: each manager with the service root's Redfish facts, then its NICs. */
export function BmcComparison({ device }: { device: Device }) {
  const managers = device.status?.discovery?.inventory?.managers ?? [];
  const root = device.status?.discovery?.serviceRoot;
  if (!managers.length && !root) return null;
  const columns: [string, (manager?: typeof managers[number]) => string | undefined][] = [
    ['Manager', manager => manager?.id],
    ['Model', manager => manager?.model],
    ['Firmware', manager => manager?.firmwareVersion],
    ['Redfish', () => root?.redfishVersion],
    ['Vendor', () => root?.vendor],
    ['Product', () => root?.product],
  ];
  const rowsFor = managers.length ? managers : [undefined];
  const shown = columns.filter(([, value]) => rowsFor.some(manager => value(manager)));
  const nics = managers.flatMap(manager => (manager.networkInterfaces ?? []).map(nic => ({ ...nic, system: managers.length > 1 ? manager.id : undefined })));
  const nicTable = nicRows(nics);
  return <>
    {!!shown.length && <SmallTable label="BMC" head={shown.map(([head]) => head)} rows={rowsFor.map(manager => shown.map(([, value]) => value(manager)))} />}
    {!!nics.length && <SmallTable label="BMC NICs" head={nicTable.head.map(head => head === 'System' ? 'Manager' : head)} rows={nicTable.rows} />}
  </>;
}
