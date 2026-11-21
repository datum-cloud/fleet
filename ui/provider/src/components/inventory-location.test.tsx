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
