import { chooseValue, openFilters } from '../test/field-select';
import { fireEvent, render, screen, within } from '@testing-library/react';
import { expect, it } from 'vitest';
import { MemoryRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { PhysicalInventory } from './physical-inventory';
import { toInventory } from '../inventory';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';

const rack = fixturePhysicalRecords[1];
/** Units drawn as unverified: every unit without a placement. */
const unverifiedUnits = (elevation: HTMLElement) => within(elevation).queryAllByLabelText('Occupancy unverified').length;
it.each([true, false, undefined])('draws reported Device geometry with explicit depth %s', fullDepth => {
  const device = { ...fixturePhysicalDevices[0], spec: { ...fixturePhysicalDevices[0].spec, reportedPlacement: { revision: 1 as const, observedAt: '2026-11-21T00:00:00Z', reason: 'Synthetic survey', reference: 'ticket/synthetic', rackRef: { name: rack.metadata.name }, position: 12, height: 4, face: 'rear' as const, fullDepth } } };
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/?kind=Rack&namespace=default&record=rack-01']}><PhysicalInventory inventory={toInventory([fixturePhysicalClaims[0]], [device], [rack])} /></MemoryRouter></QueryClientProvider>);
  const rear = within(screen.getByRole('list', { name: 'rear recorded placements' })).getByRole('link', { name: /Reported placement/ });
  // The elevation selects on the rack page; the Device page is one step on, from the panel.
  expect(rear).toHaveAttribute('href', expect.stringContaining(`selectedKind=Device&selected=${device.metadata.name}`));
  fireEvent.click(rear);
  expect(within(screen.getByTestId('rack-device-panel')).getByRole('link', { name: 'Open device page' })).toHaveAttribute('href', expect.stringContaining(`kind=Device&record=${device.metadata.name}`));
  expect(rear.parentElement).toHaveStyle({ gridRow: '28 / span 4' });
  const front = within(screen.getByRole('list', { name: 'front recorded placements' })).queryAllByRole('link');
  // Unset reported depth falls back to the Device's recorded full depth.
  expect(front).toHaveLength(fullDepth === false ? 0 : 1);
  if (front.length) {
    expect(front[0]).toHaveAccessibleName(/^Review Rear of Example compute server · U12–U15 · 4U · Reported placement · mounted rear · Full depth/);
    expect(front[0].parentElement).toHaveAttribute('data-far');
  }
  expect(rear).toHaveAccessibleName(new RegExp(fullDepth === false ? 'Half depth \\(reported\\)' : fullDepth ? 'Full depth \\(reported\\)' : 'Full depth \\(recorded on Device\\)'));
  expect(screen.queryByRole('link', { name: /Review Server installation/ })).not.toBeInTheDocument();
});
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
  const elevation = screen.getByRole('list', { name: `${face} recorded placements` });
  const links = within(elevation).getAllByRole('link');
  expect(links).toHaveLength(1);
  expect(links[0]).toHaveAccessibleName(new RegExp(`U${position}–U${position + height - 1}.*${height}U`));
  expect(links[0].parentElement).toHaveStyle({ gridRow: row, gridColumn: '2' });
  // Every unit without a placement is drawn, and unverified.
  expect(unverifiedUnits(elevation)).toBe(42 - height);
  // The registered Device records full depth, so the far face shows the
  // chassis rear, muted, over the same units.
  const far = within(screen.getByRole('list', { name: `${face === 'front' ? 'rear' : 'front'} recorded placements` })).getAllByRole('link');
  expect(far).toHaveLength(1);
  expect(far[0]).toHaveAccessibleName(new RegExp(`^Review Rear of Server installation · U12 · U${position}–U${position + height - 1} · ${height}U · Claim-derived fallback · mounted ${face} · Full depth \\(recorded on Device\\)`));
  expect(far[0].parentElement).toHaveClass('inventory-rack-plan-far');
  expect(far[0].parentElement).toHaveStyle({ gridRow: row });
});
it.each([1, 2])('keeps overlapping %iU plans identifiable with height-aware labels', height => {
  const claims = [10, 10 + height - 1].map((position, index) => ({ ...fixturePhysicalClaims[0], metadata: { ...fixturePhysicalClaims[0].metadata, name: `overlap-${index}` }, spec: { ...fixturePhysicalClaims[0].spec, displayName: `Plan ${index}`, placement: { scope: 'RackUnits' as const, rackRef: { name: 'rack-01' }, position, height, face: 'front' as const } } }));
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/?kind=Rack&namespace=default&record=rack-01']}><PhysicalInventory inventory={toInventory(claims, [], [rack])} /></MemoryRouter></QueryClientProvider>);
  const elevation = screen.getByRole('list', { name: 'front recorded placements' });
  // Entries are in visual order (top first), so pick each plan by name.
  const all = within(elevation).getAllByRole('link', { name: /overlapping placements/ });
  expect(all).toHaveLength(2);
  const links = [/Plan 0/, /Plan 1/].map(name => all.find(link => name.test(link.getAttribute('aria-label') ?? ''))!);
  expect(links[0].parentElement).toHaveStyle({ gridColumn: '2', gridRow: `${34 - height} / span ${height}` });
  expect(links[1].parentElement).toHaveStyle({ gridColumn: '3', gridRow: `${35 - 2 * height} / span ${height}` });
  for (const link of links) {
    // Overlap is colour coded, not marked; the reason is the tooltip and accessible name.
    expect(link).toHaveAttribute('data-attention', 'conflict');
    expect(link.getAttribute('title')).toMatch(/records disagree/);
    expect(within(link).queryByText('!')).toBeNull();
    // No Device is registered, so depth is unknown and labelled as such.
    expect(link.querySelector('small')).toBeNull();
    expect(link.getAttribute('title')).toMatch(height > 1 ? /U\d+–U\d+ · 2U · .*Depth unknown/ : /Depth unknown/);
    expect(link).toHaveAccessibleName(/Depth unknown · records disagree/);
    expect(link.querySelector('.inventory-rack-plan-name')).toHaveTextContent(/Plan/);
  }
});
it('renders recorded front/rear plans, never free units or occupancy percentages', () => {
  show();
  const front = screen.getByRole('list', { name: 'front recorded placements' });
  expect(within(front).getByRole('link', { name: /Review Server installation/ })).toBeInTheDocument();
  expect(screen.queryByText(/%|available units|Online/)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Rear' }));
  expect(screen.queryByRole('list', { name: 'front recorded placements' })).not.toBeInTheDocument();
  expect(screen.getByRole('list', { name: 'rear recorded placements' })).toBeInTheDocument();
});
it.each([
  { scope: 'RackMembership' as const, rackRef: { name: 'rack-01' } },
  { scope: 'RackUnits' as const, rackRef: { name: 'rack-01' }, position: 45, height: 1, face: 'front' as const },
  { scope: 'RackUnits' as const, rackRef: { name: 'rack-01' }, position: 12, height: 1 },
  { scope: 'RackUnits' as const, rackRef: { name: 'rack-01' }, position: 41, height: 4, face: 'front' as const },
  { scope: 'RackUnits' as const, rackRef: { name: 'rack-01' }, position: 12, face: 'front' as const },
])('keeps incomplete or out-of-range geometry out of the elevation: %j', placement => {
  show(placement);
  expect(within(screen.getByRole('list', { name: 'front recorded placements' })).queryByRole('link')).not.toBeInTheDocument();
  expect(within(screen.getByRole('region', { name: 'Rack issues' })).getByRole('link', { name: 'Server installation · U12' })).toHaveAttribute('href', expect.stringContaining('selectedKind=DeviceClaim&selected=rack-01-u12'));
  fireEvent.click(within(screen.getByRole('tablist', { name: 'Rack views' })).getByRole('tab', { name: /^Equipment/ }));
  // One table: equipment without a usable position reads "No position" and sorts after positioned rows.
  const row = within(screen.getByRole('table', { name: 'Rack equipment' })).getByRole('link', { name: 'Example compute server' }).closest('tr')!;
  expect(within(row).getAllByRole('cell')[0]).toHaveTextContent(/^No position$/);
});
it('separates non-unit mounts rather than consuming rack units', () => {
  show({ scope: 'NonRackUnitMount', rackRef: { name: 'rack-01' }, mount: 'left-rail' });
  expect(within(screen.getByRole('list', { name: 'front recorded placements' })).queryByRole('link')).not.toBeInTheDocument();
  fireEvent.click(within(screen.getByRole('tablist', { name: 'Rack views' })).getByRole('tab', { name: /^Equipment/ }));
  const table = screen.getByRole('table', { name: 'Rack equipment' });
  expect(within(table).getAllByRole('row')[1].querySelector('td')).toHaveTextContent(/^left-rail$/);
});
it('does not disguise denied installation collections as empty geometry', () => {
  show(undefined, { DeviceClaim: { status: 403 } });
  expect(screen.getByText(/Installation details unavailable. Rack positions cannot be shown/)).toBeInTheDocument();
  expect(screen.queryByRole('list', { name: 'front recorded placements' })).not.toBeInTheDocument();
  expect(within(screen.getByRole('tablist', { name: 'Rack views' })).getByRole('tab', { name: 'Equipment ?' })).toBeInTheDocument();
  expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
  fireEvent.click(within(screen.getByRole('tablist', { name: 'Rack views' })).getByRole('tab', { name: 'Equipment ?' }));
  expect(screen.getByText(/This is not an empty rack/)).toBeInTheDocument();
  expect(screen.queryByText('Example compute server')).not.toBeInTheDocument();
});
it('does not invent rack units when the rack height is unknown', () => {
  show(undefined, {}, null);
  expect(screen.queryByRole('list', { name: 'front recorded placements' })).not.toBeInTheDocument();
});
it('filters by recorded model and facility without manufacturing a hardware type', () => {
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><PhysicalInventory inventory={toInventory(fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords)} /></MemoryRouter></QueryClientProvider>);
  openFilters();
  chooseValue(screen.getByRole('combobox', { name: 'Filter by model' }), 'Example 1U');
  expect(screen.queryByRole('link', { name: 'orphan-synthetic-0009' })).not.toBeInTheDocument();
  chooseValue(screen.getByRole('combobox', { name: 'Filter by facility' }), 'default/example-facility');
  expect(screen.getByRole('link', { name: 'Example compute server' })).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }));
  expect(screen.getByRole('link', { name: 'orphan-synthetic-0009' })).toBeInTheDocument();
});
