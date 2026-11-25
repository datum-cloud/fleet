import { describe, expect, it } from 'vitest';
import type { Device, ObservedSystem } from '../../../shared/fleet-api';
import { bmcChip, comparedSystem, conditionChips, identityRows, keyApproval, linkState, powerWord, shortFingerprint, specNamesFor, staleReason, tlsExpiry, unobservedMacs } from './device-discovery';

const NOW = Date.parse('2026-06-01T00:00:00Z');
const DAY = 86_400_000;

function device(status?: Device['status'], spec: Partial<Device['spec']> = {}): Device {
  return {
    metadata: { name: 'example-host' },
    spec: { claimRef: { name: 'example-claim' }, identity: { model: 'X1', serial: 'SYNTH-1', systemUUID: '00000000-0000-4000-8000-000000000001' }, bmc: { endpoint: 'https://192.0.2.1' }, ...spec },
    status,
  };
}
const system: ObservedSystem = { id: 'System.1', uuid: '00000000-0000-4000-8000-000000000001', model: 'Vendor X1', serialNumber: 'SYNTH-1', powerState: 'On' };
const inventory = (systems = [system]) => ({ discovery: { inventory: { observedAt: '2026-06-01T00:00:00Z', endpoint: 'https://192.0.2.1', systems } } });
const cond = (type: string, status: 'True' | 'False' | 'Unknown', reason: string, message = `${reason} message`) => ({ type, status, reason, message });

describe('condition chips', () => {
  it('is red for False, amber for Unknown or absent, plain for True, with type, status, reason and message in the tooltip', () => {
    const chips = conditionChips(device({ conditions: [cond('Reachable', 'True', 'ServiceRootRead'), cond('Authenticated', 'False', 'CredentialRejected'), cond('Discovered', 'Unknown', 'ReadFailed')] }));
    expect(chips.map(chip => [chip.word, chip.tone])).toEqual([['Reachable', undefined], ['Auth', 'danger'], ['Inventory', 'warning'], ['Identity', 'warning'], ['Key unique', 'warning']]);
    expect(chips[1].reason).toBe('Authenticated: False · CredentialRejected · CredentialRejected message');
    expect(chips[3].reason).toBe('IdentityMatched: Unknown · not reported');
  });

  it('summarises as one header chip, naming the first failure', () => {
    expect(bmcChip(device())).toBeUndefined();
    expect(bmcChip(device({ conditions: [cond('Reachable', 'False', 'ConnectFailed')] }))).toMatchObject({ label: 'BMC unreachable', tone: 'danger' });
    expect(bmcChip(device({ conditions: [cond('Reachable', 'True', 'ServiceRootRead'), cond('Authenticated', 'False', 'PinNotApproved')] }))).toMatchObject({ label: 'BMC key not approved', tone: 'danger' });
    expect(bmcChip(device({ conditions: [cond('Reachable', 'True', 'ServiceRootRead')] }))).toMatchObject({ label: 'BMC ?', tone: 'warning' });
    const ok = ['Reachable', 'Authenticated', 'Discovered', 'IdentityMatched', 'PublicKeyUnique'].map(type => cond(type, 'True', 'Fine'));
    expect(bmcChip(device({ conditions: ok }))).toMatchObject({ label: 'BMC ✓', tone: undefined });
  });
});

describe('key approval', () => {
  it('reads PinNotApproved as not approved and CredentialAccepted as approved', () => {
    expect(keyApproval(device({ conditions: [cond('Authenticated', 'False', 'PinNotApproved', 'verify it out of band')] }))).toEqual({ label: 'Not approved', tone: 'warning', reason: 'PinNotApproved · verify it out of band' });
    expect(keyApproval(device({ conditions: [cond('Authenticated', 'True', 'CredentialAccepted')] }))).toMatchObject({ label: 'Approved', tone: undefined });
    expect(keyApproval(device({ conditions: [cond('Authenticated', 'False', 'PinMismatch')] }))).toMatchObject({ tone: 'danger' });
    // Other reasons say nothing about the pin.
    expect(keyApproval(device({ conditions: [cond('Authenticated', 'Unknown', 'NotAttempted')] }))).toBeUndefined();
  });
});

describe('TLS expiry', () => {
  it('is red once expired, amber within 30 days and plain after', () => {
    expect(tlsExpiry(new Date(NOW - DAY).toISOString(), NOW)).toMatchObject({ tone: 'danger', label: 'Expired 2026-05-31' });
    expect(tlsExpiry(new Date(NOW + 12 * DAY).toISOString(), NOW)).toMatchObject({ tone: 'warning', label: '2026-06-13 · 12 d' });
    expect(tlsExpiry(new Date(NOW + 30 * DAY).toISOString(), NOW)?.tone).toBe('warning');
    expect(tlsExpiry(new Date(NOW + 31 * DAY).toISOString(), NOW)).toMatchObject({ tone: undefined, label: '2026-07-02' });
    expect(tlsExpiry(undefined, NOW)).toBeUndefined();
  });

  it('shortens the fingerprint', () => expect(shortFingerprint('ab'.repeat(32))).toBe('abababab…abab'));
});

describe('identity rows', () => {
  it('compares serial, model and system UUID by the rules discovery uses', () => {
    const rows = identityRows(device({ ...inventory([{ ...system, sku: 'SKU-9' }]), conditions: [cond('IdentityMatched', 'True', 'Matched')] }));
    expect(rows.map(row => [row.field, row.expected, row.observed, row.mismatch])).toEqual([
      ['Serial', 'SYNTH-1', 'SYNTH-1', undefined],
      // A vendor family prefix is not a mismatch.
      ['Model', 'X1', 'Vendor X1', undefined],
      ['System UUID', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000001', undefined],
      ['SKU', undefined, 'SKU-9', undefined],
    ]);
  });

  it('marks the field IdentityMatched names, with its message', () => {
    const rows = identityRows(device({ ...inventory([{ ...system, serialNumber: 'SYNTH-OTHER' }]), conditions: [cond('IdentityMatched', 'False', 'SerialMismatch', 'reports another serial')] }));
    expect(rows.find(row => row.field === 'Serial')?.mismatch).toBe('reports another serial');
    expect(rows.find(row => row.field === 'Model')?.mismatch).toBeUndefined();
  });

  it('accepts the spec serial as the SKU, and marks a different model without a condition', () => {
    const rows = identityRows(device(inventory([{ ...system, serialNumber: 'BOARD-7', sku: 'synth-1', model: 'Y2' }])));
    expect(rows.find(row => row.field === 'Serial')?.mismatch).toBeUndefined();
    expect(rows.find(row => row.field === 'Model')?.mismatch).toBe('Discovery reports Y2; spec records X1');
  });

  it('leaves the expected cell blank where spec does not record the field', () => {
    const rows = identityRows(device(inventory(), { identity: { model: 'X1', serial: 'SYNTH-1' } }));
    expect(rows.find(row => row.field === 'System UUID')).toMatchObject({ expected: undefined, mismatch: undefined });
  });

  it('compares no System when more than one could be this Device', () => {
    const many = device(inventory([{ ...system, uuid: undefined }, { ...system, id: 'System.2', uuid: undefined }]), { identity: { model: 'X1' } });
    expect(comparedSystem(many)).toBeUndefined();
    expect(identityRows(many)).toEqual([]);
  });

  it('shows power state as a short word', () => {
    expect(powerWord('PoweringOn')).toBe('Starting');
    expect(powerWord('Off')).toBe('Off');
  });
});

describe('NICs and MAC addresses', () => {
  it('is amber unless the link is up', () => {
    expect(linkState({ id: 'n', linkStatus: 'LinkUp' })).toEqual({ label: 'Up', tone: undefined });
    expect(linkState({ id: 'n', linkStatus: 'LinkDown' })).toEqual({ label: 'Down', tone: 'warning' });
    expect(linkState({ id: 'n', linkStatus: 'NoLink' })).toEqual({ label: 'No link', tone: 'warning' });
    expect(linkState({ id: 'n' })).toEqual({ label: 'Not reported', tone: 'warning' });
  });

  it('matches spec interfaces to host NICs by MAC address only, never by name', () => {
    const nics = [{ id: 'EthernetInterface0', macAddress: '00:00:5E:00:53:01' }, { id: 'EthernetInterface1', macAddress: '00:00:5e:00:53:09' }];
    const host = device(inventory([{ ...system, networkInterfaces: nics }]), { interfaces: [{ name: 'ETH0', macAddress: '00:00:5e:00:53:01' }, { name: 'ETH1', macAddress: '00:00:5e:00:53:02' }, { name: 'ETH2' }] });
    expect(specNamesFor(host, nics[0].macAddress)).toEqual(['ETH0']);
    expect(specNamesFor(host, nics[1].macAddress)).toEqual([]);
    expect([...unobservedMacs(host).keys()]).toEqual(['ETH1']);
  });

  it('marks nothing unobserved before an inventory read', () => {
    expect(unobservedMacs(device({ conditions: [] }, { interfaces: [{ name: 'ETH0', macAddress: '00:00:5e:00:53:01' }] })).size).toBe(0);
  });
});

describe('observation from another endpoint', () => {
  // Discovery reports IdentityMatched Unknown (StaleObservation) when the retained inventory came from another endpoint.
  const moved = (systems = [{ ...system, serialNumber: 'SYNTH-OTHER' }]) => device({ ...inventory(systems), conditions: [cond('IdentityMatched', 'Unknown', 'StaleObservation')] }, { bmc: { endpoint: 'https://192.0.2.9' }, interfaces: [{ name: 'ETH0', macAddress: '00:00:5e:00:53:99' }] });

  it('compares nothing and marks every observed value stale instead of red', () => {
    const rows = identityRows(moved());
    expect(rows.every(row => !row.mismatch)).toBe(true);
    expect(rows.find(row => row.field === 'Serial')).toMatchObject({ observed: 'SYNTH-OTHER', stale: 'Read from https://192.0.2.1; spec.bmc.endpoint is https://192.0.2.9' });
  });

  it('marks no spec MAC as unobserved, since the NICs are another BMC\'s', () => {
    expect(unobservedMacs(moved()).size).toBe(0);
  });

  it('names the endpoint only when it differs from spec', () => {
    expect(staleReason({ endpoint: 'https://192.0.2.1' }, device())).toBeUndefined();
    expect(staleReason({ endpoint: 'https://192.0.2.7' }, device())).toBe('Read from https://192.0.2.7; spec.bmc.endpoint is https://192.0.2.1');
    expect(staleReason(undefined, device())).toBeUndefined();
    // Discovery treats a Device with no BMC endpoint in spec as stale too.
    expect(staleReason({ endpoint: 'https://192.0.2.1' }, device(undefined, { bmc: undefined }))).toBe('Read from https://192.0.2.1; spec.bmc.endpoint is not set');
  });
});
