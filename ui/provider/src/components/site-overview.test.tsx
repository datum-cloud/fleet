import { render, screen, within } from '@testing-library/react';
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
const plan: RawDeviceClaim = { metadata: meta('plan'), spec: { displayName: 'Install A', lifecycle: 'Installation', expectedIdentity: { model: 'Example' }, placement: { rackRef: { name: 'rack' } } } };
const device: RawDevice = { metadata: meta('device'), spec: { displayName: 'Device A', claimRef: { name: 'plan' }, identity: { model: 'Example' } } };
const data = () => toInventory([plan], [device], [site, facility, rack]);
function show(inventory = data()) {
  return render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/inventory?kind=Site&record=site']}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
}

describe('operational Site overview', () => {
  it('follows typed facility/rack/claim paths, deduplicates shared premises and uses semantic detail links', () => {
    const inventory = data();
    inventory.physical.push({ ...site, metadata: meta('other-site') }, rack);
    inventory.slots[0].devices.push(device);
    show(inventory);
    expect(screen.getAllByRole('link', { name: 'Premises' })).toHaveLength(1);
    expect(screen.getByRole('link', { name: 'Rack A' })).toHaveAttribute('href', '/inventory?kind=Rack&record=rack&namespace=default');
    expect(screen.getAllByRole('link', { name: 'Device A' })).toHaveLength(1);
    expect(screen.getByRole('link', { name: 'Install A' })).toHaveAttribute('href', '/inventory?kind=DeviceClaim&record=plan&namespace=default');
    expect(screen.getByRole('heading', { name: 'Devices · 1 recorded' })).toBeInTheDocument();
    expect(screen.getByText('Claim-derived fallback')).toBeInTheDocument();
    expect(screen.getByText(/execution status is not recorded/)).toBeInTheDocument();
    expect(screen.getByText(/not equipment assigned to this Site/)).toBeInTheDocument();
    expect(screen.queryByText('Related records')).not.toBeInTheDocument();
    expect(screen.queryByText('Namespace')).not.toBeInTheDocument();
    expect(screen.getByText('Record name').closest('details')).not.toHaveAttribute('open');
  });

  it('prefers device reports, counts each registration, and excludes legacy claims from plans', () => {
    const moved = { ...device, metadata: meta('moved'), spec: { ...device.spec, displayName: 'Moved device', reportedPlacement: { revision: 1, reason: 'Observation', reference: 'Synthetic', observedAt: '2026-11-22', facilityRef: { name: 'elsewhere' } } } };
    const direct = { ...device, metadata: meta('direct'), spec: { ...moved.spec, displayName: 'Direct device', claimRef: { name: 'missing' }, reportedPlacement: { ...moved.spec.reportedPlacement, facilityRef: { name: 'building' } } } };
    const legacy = { ...plan, metadata: meta('legacy'), spec: { ...plan.spec, lifecycle: undefined, displayName: 'Legacy slot' } };
    show(toInventory([plan, legacy], [device, moved, direct], [site, facility, rack, { ...facility, metadata: meta('elsewhere') }]));
    expect(screen.getByRole('heading', { name: 'Devices · 2 recorded' })).toBeInTheDocument();
    expect(screen.getByText('Reported placement')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Moved device' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Legacy slot' })).not.toBeInTheDocument();
  });

  it.each(['Facility', 'Rack', 'Device', 'DeviceClaim'] as const)('redacts denied %s collections without claiming zero', kind => {
    show({ ...data(), issues: { [kind]: { status: 403 } } });
    const protectedName = { Facility: 'Premises', Rack: 'Rack A', Device: 'Device A', DeviceClaim: 'Install A' }[kind];
    expect(screen.queryByRole('link', { name: protectedName })).not.toBeInTheDocument();
    expect(screen.getAllByText(/read access|Access denied/).length).toBeGreaterThan(0);
    if (kind === 'Rack' || kind === 'DeviceClaim') expect(screen.getByRole('heading', { name: /Devices · 0 found · incomplete/ })).toBeInTheDocument();
    if (kind === 'Device') expect(screen.getByRole('heading', { name: 'Devices' })).toBeInTheDocument();
  });

  it('keeps readable direct device reports with denied claims and marks the result incomplete', () => {
    const inventory = data();
    inventory.slots[0].devices = [{ ...device, spec: { ...device.spec, reportedPlacement: { revision: 1, reason: 'Observed', reference: 'Synthetic', observedAt: '2026-11-22', facilityRef: { name: 'building' } } } }];
    show({ ...inventory, issues: { DeviceClaim: { status: 403 } } });
    expect(screen.getByRole('link', { name: 'Device A' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Devices · 1 found · incomplete' })).toBeInTheDocument();
  });

  it('does not match display names, foreign namespaces, or dangling rack references', () => {
    show(toInventory([plan], [device], [site, facility, { ...rack, metadata: { ...meta('rack'), namespace: 'foreign' } }, { ...rack, metadata: meta('different') }]));
    expect(screen.queryByRole('link', { name: 'Device A' })).not.toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Devices · 0 found · incomplete' })).toBeInTheDocument();
    expect(screen.getByText(/0 found · incomplete · execution/)).toBeInTheDocument();
  });

  it.each([undefined, '2026-11-22'])('keeps missing/deleting facility membership unresolved (%s)', deletionTimestamp => {
    show(toInventory([plan], [device], [site, rack, ...(deletionTimestamp ? [{ ...facility, metadata: { ...facility.metadata, deletionTimestamp } }] : [])]));
    expect(screen.getByText(/membership unresolved/)).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Device A' })).not.toBeInTheDocument();
    expect(screen.getByText(/Equipment cannot be summarized/)).toBeInTheDocument();
  });

  it('retains partial results when one of the linked facilities is missing', () => {
    const inventory = data();
    inventory.physical[0] = { ...site, spec: { ...site.spec, facilityRefs: [{ name: 'building' }, { name: 'missing' }] } };
    show(inventory);
    expect(screen.getByRole('link', { name: 'Device A' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Racks · 1 found · incomplete' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Devices · 1 found · incomplete' })).toBeInTheDocument();
    expect(screen.getByText(/1 found · incomplete · execution/)).toBeInTheDocument();
    expect(screen.getByText(/Counts do not establish placement eligibility, capacity, or readiness/)).toBeInTheDocument();
  });

  it('does not reuse a deleting claim for device location fallback', () => {
    show(toInventory([{ ...plan, metadata: { ...plan.metadata, deletionTimestamp: '2026-11-22' } }], [device], [site, facility, rack]));
    expect(screen.queryByRole('link', { name: 'Device A' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Install A' })).not.toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Devices · 0 found · incomplete' })).toBeInTheDocument();
  });

  it('distinguishes unavailable membership from an unmapped Site and gives a next step', () => {
    const view = show({ ...data(), issues: { Facility: { status: 503 } } });
    expect(screen.getByText(/Facility details unavailable/)).toBeInTheDocument();
    view.unmount();
    show(toInventory([], [], [{ ...site, spec: { ...site.spec, facilityRefs: [] } }]));
    expect(within(screen.getByRole('region', { name: 'Site location' })).getByText(/Ask an inventory manager/)).toBeInTheDocument();
  });
});
