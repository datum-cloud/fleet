import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { MemoryRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import { PhysicalInventory } from './physical-inventory';
import { toInventory, type Inventory } from '../inventory';

const facility: PhysicalRecord = { kind: 'Facility', metadata: { name: 'premises-stable', namespace: 'default', uid: 'premises-uid' }, spec: { displayName: 'Physical premises', identifiers: [{ provider: 'example-provider', type: 'PremisesCode', value: 'EX1' }] } };
const site: PhysicalRecord = { kind: 'Site', metadata: { name: 'site-stable', namespace: 'default', uid: 'site-uid' }, spec: { displayName: 'Operational identity', code: 'EX3', facilityRefs: [{ name: facility.metadata.name }] } };
const inventory = toInventory([], [], [facility, site]);

function view(query: string, data: Inventory) {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={[`/inventory${query}`]}><PhysicalInventory inventory={data} /></MemoryRouter></QueryClientProvider>;
}

describe('Site and premises directory', () => {
  it('shows internal identity separately from provider identity', () => {
    render(view('?kind=Site&record=site-stable&namespace=default', inventory));
    expect(screen.getByText('Internal Site code')).toBeInTheDocument();
    expect(screen.getByText('EX3')).toBeInTheDocument();
    expect(screen.getByText(/example-provider \/ EX1/)).toBeInTheDocument();
    expect(screen.getByText(/Membership grants no placement eligibility/)).toBeInTheDocument();
    expect(screen.getByText(/Recreating a name resolves to the replacement/)).toBeInTheDocument();
  });

  it('keeps a legacy Facility browsable without inventing membership', () => {
    render(view('?kind=Facility&record=premises-stable&namespace=default', toInventory([], [], [facility])));
    expect(screen.getByText(/Unmapped · no operational Site recorded/)).toBeInTheDocument();
  });

  it('redacts denied Site identity from Facility search', () => {
    render(view('?kind=Facility', { ...inventory, issues: { Site: { status: 403 } } }));
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'EX3' } });
    fireEvent.click(screen.getByRole('button', { name: 'Apply search' }));
    expect(screen.queryByText('Physical premises')).not.toBeInTheDocument();
    expect(screen.queryByText(/EX3 · Operational identity/)).not.toBeInTheDocument();
  });

  it('redacts denied Facility identifiers and links from Site details', () => {
    render(view('?kind=Site&record=site-stable&namespace=default', { ...inventory, issues: { Facility: { status: 403 } } }));
    expect(screen.getByText(/Facility details unavailable/)).toBeInTheDocument();
    expect(screen.queryByText(/EX1/)).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Physical premises' })).not.toBeInTheDocument();
  });
});
