import { fireEvent, render, screen, within } from '@testing-library/react';
import { expect, it } from 'vitest';
import { MemoryRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { PhysicalInventory } from './physical-inventory';
import { toInventory } from '../inventory';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';

const rack = fixturePhysicalRecords[1];
function show(placement = fixturePhysicalClaims[0].spec.placement, issues = {}, height: number | null = 42) {
  const claim = { ...fixturePhysicalClaims[0], spec: { ...fixturePhysicalClaims[0].spec, placement } };
  return render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/?kind=Rack&namespace=default&record=rack-01']}><PhysicalInventory inventory={{ ...toInventory([claim], fixturePhysicalDevices, [{ ...rack, spec: { ...rack.spec, heightUnits: height ?? undefined } }]), issues }} /></MemoryRouter></QueryClientProvider>);
}
it.each([
  { position: 1, height: 2, face: 'front' as const, row: '41 / span 2' },
  { position: 39, height: 4, face: 'rear' as const, row: '1 / span 4' },
  { position: 12, height: 4, face: 'front' as const, row: '28 / span 4' },
])('renders one continuous block for $height units at $position on $face', ({ position, height, face, row }) => {
  show({ scope: 'RackUnits', rackRef: { name: 'rack-01' }, position, height, face });
  const elevation = screen.getByRole('list', { name: `${face} recorded plans` });
  const links = within(elevation).getAllByRole('link');
  expect(links).toHaveLength(1);
  expect(links[0]).toHaveAccessibleName(new RegExp(`U${position}–U${position + height - 1}.*${height}U`));
  expect(links[0].parentElement).toHaveStyle({ gridRow: row, gridColumn: '2' });
  expect(within(elevation).getAllByLabelText('Occupancy unverified')).toHaveLength(42 - height);
  expect(within(screen.getByRole('list', { name: `${face === 'front' ? 'rear' : 'front'} recorded plans` })).queryByRole('link')).not.toBeInTheDocument();
});
it.each([1, 2])('keeps overlapping %iU plans identifiable with height-aware labels', height => {
  const claims = [10, 10 + height - 1].map((position, index) => ({ ...fixturePhysicalClaims[0], metadata: { ...fixturePhysicalClaims[0].metadata, name: `overlap-${index}` }, spec: { ...fixturePhysicalClaims[0].spec, displayName: `Plan ${index}`, placement: { scope: 'RackUnits' as const, rackRef: { name: 'rack-01' }, position, height, face: 'front' as const } } }));
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/?kind=Rack&namespace=default&record=rack-01']}><PhysicalInventory inventory={toInventory(claims, [], [rack])} /></MemoryRouter></QueryClientProvider>);
  const elevation = screen.getByRole('list', { name: 'front recorded plans' });
  const links = within(elevation).getAllByRole('link', { name: /overlapping plans/ });
  expect(links).toHaveLength(2);
  expect(links[0].parentElement).toHaveStyle({ gridColumn: '2', gridRow: `${34 - height} / span ${height}` });
  expect(links[1].parentElement).toHaveStyle({ gridColumn: '3', gridRow: `${35 - 2 * height} / span ${height}` });
  for (const link of links) {
    expect(within(link).getByText('!')).toHaveClass('inventory-rack-overlap');
    expect(link.querySelectorAll('small')).toHaveLength(height > 1 ? 1 : 0);
    expect(link.querySelector('.inventory-rack-plan-name')).toHaveTextContent(/Plan/);
  }
  expect(screen.getByText(/! indicates overlapping plans requiring review/)).toBeInTheDocument();
});
it('renders recorded front/rear plans, never free units or occupancy percentages', () => {
  show();
  const front = screen.getByRole('list', { name: 'front recorded plans' });
  expect(within(front).getByRole('link', { name: /Review Server installation/ })).toBeInTheDocument();
  expect(screen.queryByText(/%|available units|Online/)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Rear' }));
  expect(screen.queryByRole('list', { name: 'front recorded plans' })).not.toBeInTheDocument();
  expect(screen.getByRole('list', { name: 'rear recorded plans' })).toBeInTheDocument();
});
it.each([
  { scope: 'RackMembership' as const, rackRef: { name: 'rack-01' } },
  { scope: 'RackUnits' as const, rackRef: { name: 'rack-01' }, position: 45, height: 1, face: 'front' as const },
  { scope: 'RackUnits' as const, rackRef: { name: 'rack-01' }, position: 12, height: 1 },
  { scope: 'RackUnits' as const, rackRef: { name: 'rack-01' }, position: 41, height: 4, face: 'front' as const },
  { scope: 'RackUnits' as const, rackRef: { name: 'rack-01' }, position: 12, face: 'front' as const },
])('keeps incomplete or out-of-range geometry out of the elevation: %j', placement => {
  show(placement);
  expect(within(screen.getByRole('list', { name: 'front recorded plans' })).queryByRole('link')).not.toBeInTheDocument();
  const section = screen.getByRole('heading', { name: /Unpositioned equipment/ }).parentElement!;
  expect(within(section).getByRole('link', { name: 'Example compute server' })).toBeInTheDocument();
});
it('separates non-unit mounts rather than consuming rack units', () => {
  show({ scope: 'NonRackUnitMount', rackRef: { name: 'rack-01' }, mount: 'left-rail' });
  const section = screen.getByRole('heading', { name: /Non-racked mounts/ }).parentElement!;
  expect(within(section).getByText('left-rail')).toBeInTheDocument();
  expect(within(screen.getByRole('list', { name: 'front recorded plans' })).queryByRole('link')).not.toBeInTheDocument();
});
it('does not disguise denied installation collections as empty geometry', () => {
  show(undefined, { DeviceClaim: { status: 403 } });
  expect(screen.getByText(/This is not an empty rack/)).toBeInTheDocument();
  expect(screen.queryByRole('list', { name: 'front recorded plans' })).not.toBeInTheDocument();
  expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
});
it('does not invent rack units when the rack height is unknown', () => {
  show(undefined, {}, null);
  expect(screen.queryByRole('list', { name: 'front recorded plans' })).not.toBeInTheDocument();
});
it('filters by recorded model and facility without manufacturing a hardware type', () => {
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><PhysicalInventory inventory={toInventory(fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords)} /></MemoryRouter></QueryClientProvider>);
  fireEvent.change(screen.getByRole('combobox', { name: 'Filter by model' }), { target: { value: 'Example 1U' } });
  expect(screen.queryByRole('link', { name: 'orphan-synthetic-0009' })).not.toBeInTheDocument();
  fireEvent.change(screen.getByRole('combobox', { name: 'Filter by facility' }), { target: { value: 'default/example-facility' } });
  expect(screen.getByRole('link', { name: 'Example compute server' })).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }));
  expect(screen.getByRole('link', { name: 'orphan-synthetic-0009' })).toBeInTheDocument();
});
