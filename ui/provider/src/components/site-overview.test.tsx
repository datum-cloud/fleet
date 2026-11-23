import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { MemoryRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { PhysicalInventory } from './physical-inventory';
import { toInventory, type Inventory, type RawDevice, type RawDeviceClaim } from '../inventory';
import type { PhysicalRecord } from '../../../shared/fleet-api';

const meta = (name: string) => ({ name, namespace: 'default', uid: `${name}-uid` });
const facility: PhysicalRecord = { kind: 'Facility', metadata: meta('building'), spec: { displayName: 'Premises', locationRef: { name: 'geo-ref' } } };
const site: PhysicalRecord = { kind: 'Site', metadata: meta('site'), spec: { displayName: 'Operational site', code: 'EX3', facilityRefs: [{ name: 'building' }, { name: 'building' }] } };
const rack: PhysicalRecord = { kind: 'Rack', metadata: meta('rack'), spec: { displayName: 'Rack A', facilityRef: { name: 'building' } } };
const plan: RawDeviceClaim = { metadata: meta('plan'), spec: { displayName: 'Install A', lifecycle: 'Installation', expectedIdentity: { model: 'Example' }, placement: { scope: 'RackMembership', rackRef: { name: 'rack' } } } };
const device: RawDevice = { metadata: meta('device'), spec: { displayName: 'Device A', claimRef: { name: 'plan' }, identity: { model: 'Example' } } };
const data = () => toInventory([plan], [device], [site, facility, rack]);
function show(inventory = data()) {
  return render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/inventory?kind=Site&record=site']}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
}

// Secondary lists live in URL-held tabs. Inactive panels stay in the DOM but
// hidden, so absence checks use `hidden: true` to catch leaks in any panel.
const openTab = (name: string) => fireEvent.click(tab(name));
const tab = (name: string) => within(screen.getByRole('tablist', { name: 'Site details' })).getByRole('tab', { name: new RegExp(`^${name}`) });
/** The tab's visible count and the reason it carries, if any. */
const count = (name: string) => { const element = tab(name).querySelector('.inventory-count')!; return { text: [...element.childNodes].filter(node => node.nodeType === Node.TEXT_NODE).map(node => node.textContent).join('').trim(), note: element.getAttribute('title'), amber: element.getAttribute('data-tone') === 'warning' }; };

describe('operational Site overview', () => {
  it('follows typed facility/rack/claim paths, deduplicates shared premises and uses semantic detail links', () => {
    const inventory = data();
    inventory.physical.push({ ...site, metadata: meta('other-site') }, rack);
    inventory.slots[0].devices.push(device);
    show(inventory);
    // One facility: it is a header fact, not a tab.
    expect(screen.getAllByRole('link', { name: 'Premises', hidden: true })).toHaveLength(1);
    expect(screen.getByRole('link', { name: 'Premises' }).closest('.inventory-record-summary')).not.toBeNull();
    expect(within(screen.getByRole('tablist', { name: 'Site details' })).queryByRole('tab', { name: /^Facilities/ })).toBeNull();
    expect(screen.queryByText(/Operational membership only|Geographic location reference/)).toBeNull();
    openTab('Racks');
    expect(screen.getByRole('link', { name: 'Rack A' })).toHaveAttribute('href', '/inventory?kind=Rack&record=rack&namespace=default');
    expect(screen.queryByRole('heading', { name: /Racks ·/ })).toBeNull();
    openTab('Devices');
    expect(count('Devices')).toEqual({ text: '1', note: null, amber: false });
    const devices = screen.getByRole('table', { name: 'Devices at linked facilities' });
    expect(within(devices).getAllByRole('link', { name: 'Device A' })).toHaveLength(1);
    // Planned-only location: an amber edge with the reason as the tooltip, not a column.
    const row = within(devices).getByRole('link', { name: 'Device A' }).closest('tr')!;
    expect(row).toHaveAttribute('data-attention', 'unavailable');
    expect(within(row).getByTitle(/Planned: no placement report/)).toBeInTheDocument();
    expect(screen.queryByText('Location basis')).toBeNull();
    openTab('Installation plans');
    expect(screen.getByRole('link', { name: 'Install A' })).toHaveAttribute('href', '/inventory?kind=DeviceClaim&record=plan&namespace=default');
    expect(screen.queryByText(/execution status is not recorded/)).toBeNull();
    expect(screen.queryByText('Related records')).not.toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Record' })).toHaveTextContent(/^Record site/);
  });

  it('prefers device reports and counts each registration', () => {
    const moved = { ...device, metadata: meta('moved'), spec: { ...device.spec, displayName: 'Moved device', reportedPlacement: { revision: 1, reason: 'Observation', reference: 'Synthetic', observedAt: '2026-11-22', facilityRef: { name: 'elsewhere' } } } };
    const direct = { ...device, metadata: meta('direct'), spec: { ...moved.spec, displayName: 'Direct device', claimRef: { name: 'missing' }, reportedPlacement: { ...moved.spec.reportedPlacement, facilityRef: { name: 'building' } } } };
    show(toInventory([plan], [device, moved, direct], [site, facility, rack, { ...facility, metadata: meta('elsewhere') }]));
    openTab('Devices');
    expect(count('Devices').text).toBe('2');
    expect(screen.getByRole('link', { name: 'Direct device' }).closest('tr')).not.toHaveAttribute('data-attention');
    // Reported elsewhere: not at this Site, though its installation plan still lists the registration.
    expect(within(screen.getByRole('table', { name: 'Devices at linked facilities' })).queryByRole('link', { name: 'Moved device' })).not.toBeInTheDocument();
  });

  it.each(['Facility', 'Rack', 'Device', 'DeviceClaim'] as const)('redacts denied %s collections without claiming zero', kind => {
    show({ ...data(), issues: { [kind]: { status: 403 } } });
    const protectedName = { Facility: 'Premises', Rack: 'Rack A', Device: 'Device A', DeviceClaim: 'Install A' }[kind];
    expect(screen.queryByRole('link', { name: protectedName, hidden: true })).not.toBeInTheDocument();
    expect(screen.getAllByText(/Access denied/).length).toBeGreaterThan(0);
    if (kind === 'Facility') expect(count('Facilities')).toMatchObject({ text: '?', amber: true });
    if (kind === 'Rack' || kind === 'DeviceClaim') expect(count('Devices')).toMatchObject({ text: '0+', amber: true });
    if (kind !== 'Facility') expect(count({ Rack: 'Racks', Device: 'Devices', DeviceClaim: 'Installation plans' }[kind])).toEqual({ text: '?', note: 'Access denied', amber: true });
  });

  it('keeps readable direct device reports with denied claims and marks the result incomplete', () => {
    const inventory = data();
    inventory.slots[0].devices = [{ ...device, spec: { ...device.spec, reportedPlacement: { revision: 1, reason: 'Observed', reference: 'Synthetic', observedAt: '2026-11-22', facilityRef: { name: 'building' } } } }];
    show({ ...inventory, issues: { DeviceClaim: { status: 403 } } });
    openTab('Devices');
    expect(screen.getByRole('link', { name: 'Device A' })).toBeInTheDocument();
    // Incomplete: an amber "1+", never a plain count, with the reason in the tooltip and the tab's accessible name.
    expect(count('Devices')).toEqual({ text: '1+', note: expect.stringMatching(/unresolved or unavailable/), amber: true });
    expect(tab('Devices')).toHaveAccessibleName(/^Devices 1\+ · Some locations/);
    expect(screen.queryByText(/this is not a complete device count/)).toBeNull();
  });

  it('does not match display names, foreign namespaces, or dangling rack references', () => {
    show(toInventory([plan], [device], [site, facility, { ...rack, metadata: { ...meta('rack'), namespace: 'foreign' } }, { ...rack, metadata: meta('different') }]));
    openTab('Devices');
    expect(screen.queryByRole('link', { name: 'Device A', hidden: true })).not.toBeInTheDocument();
    expect(count('Devices').text).toBe('0+');
    expect(count('Installation plans').text).toBe('0+');
  });

  it.each([undefined, '2026-11-22'])('keeps missing/deleting facility membership unresolved (%s)', deletionTimestamp => {
    show(toInventory([plan], [device], [site, rack, ...(deletionTimestamp ? [{ ...facility, metadata: { ...facility.metadata, deletionTimestamp } }] : [])]));
    expect(within(screen.getByRole('region', { name: 'Facilities' })).getByText(/membership unresolved/)).toHaveAttribute('data-tone', 'warning');
    expect(count('Facilities')).toMatchObject({ text: '0+', amber: true });
    expect(screen.queryByRole('link', { name: 'Device A', hidden: true })).not.toBeInTheDocument();
    expect(count('Devices')).toEqual({ text: '?', note: 'Unknown until linked facilities are readable', amber: true });
  });

  it('retains partial results when one of the linked facilities is missing', () => {
    const inventory = data();
    inventory.physical[0] = { ...site, spec: { ...site.spec, facilityRefs: [{ name: 'building' }, { name: 'missing' }] } };
    show(inventory);
    // The resolved facility is listed in its tab, and the unresolved one makes its count incomplete.
    expect(count('Facilities').text).toBe('1+');
    expect(within(screen.getByRole('table', { name: 'Site facilities' })).getByRole('link', { name: 'Premises' })).toBeInTheDocument();
    openTab('Racks');
    expect(count('Racks').text).toBe('1+');
    openTab('Devices');
    expect(screen.getByRole('link', { name: 'Device A' })).toBeInTheDocument();
    expect(count('Devices').text).toBe('1+');
    expect(count('Installation plans').text).toBe('1+');
    expect(screen.queryByText(/Counts do not establish capacity or readiness/)).toBeNull();
  });

  it('does not reuse a deleting claim for device location fallback', () => {
    show(toInventory([{ ...plan, metadata: { ...plan.metadata, deletionTimestamp: '2026-11-22' } }], [device], [site, facility, rack]));
    openTab('Devices');
    expect(screen.queryByRole('link', { name: 'Device A', hidden: true })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Install A', hidden: true })).not.toBeInTheDocument();
    expect(count('Devices').text).toBe('0+');
  });

  it('distinguishes unavailable membership from an unmapped Site', () => {
    const view = show({ ...data(), issues: { Facility: { status: 503 } } });
    expect(within(screen.getByRole('region', { name: 'Facilities' })).getByText(/Facility details unavailable/)).toBeInTheDocument();
    expect(count('Facilities')).toEqual({ text: '?', note: 'Facility details unavailable', amber: true });
    view.unmount();
    show(toInventory([], [], [{ ...site, spec: { ...site.spec, facilityRefs: [] } }]));
    expect(screen.getByText('No facilities linked.')).toBeInTheDocument();
    expect(count('Facilities')).toEqual({ text: '0', note: null, amber: false });
  });

  it('lists several facilities in a tab and sorts plans by rack, then unit', () => {
    const second: PhysicalRecord = { ...facility, metadata: meta('annex'), spec: { displayName: 'Annex', identifiers: [{ provider: 'example-colo', type: 'PremisesCode', value: 'EXA2' }] } };
    const rackB: PhysicalRecord = { ...rack, metadata: meta('rack-b'), spec: { displayName: 'Rack B', facilityRef: { name: 'annex' } } };
    const at = (name: string, rackName: string, position: number): RawDeviceClaim => ({ ...plan, metadata: meta(name), spec: { ...plan.spec, displayName: name, placement: { scope: 'RackUnits', rackRef: { name: rackName }, position, height: 1, face: 'front' } } });
    show(toInventory([at('b-u2', 'rack-b', 2), at('a-u40', 'rack', 40), at('a-u2', 'rack', 2), at('a-u20', 'rack', 20)], [], [{ ...site, spec: { ...site.spec, facilityRefs: [{ name: 'building' }, { name: 'annex' }] } }, facility, second, rack, rackB]));
    expect(within(screen.getByRole('tablist', { name: 'Site details' })).getAllByRole('tab')[0]).toHaveAccessibleName('Facilities 2');
    const facilities = screen.getByRole('table', { name: 'Site facilities' });
    expect(within(facilities).getByRole('cell', { name: 'EXA2' })).toHaveAttribute('title', 'example-colo · PremisesCode');
    openTab('Installation plans');
    const rows = within(screen.getByRole('table', { name: 'Site installation plans' })).getAllByRole('row').slice(1);
    expect(rows.map(row => within(row).getAllByRole('cell')[0].textContent)).toEqual(['a-u2', 'a-u20', 'a-u40', 'b-u2']);
    // With several facilities the planned location names the facility.
    expect(within(rows[3]).getByText('Annex · Rack B · U2 front')).toBeInTheDocument();
  });
});
