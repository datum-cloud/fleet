import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { MemoryRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { toInventory } from '../inventory';
import { depthClaims, depthDevices, depthRecords } from '../lib/depth-fixtures';
import { PhysicalInventory } from './physical-inventory';
import { DeviceConnections } from './device-connections';
import { inventoryHref } from './inventory-location';

// Synthetic records only.
const inventory = toInventory(depthClaims, depthDevices, depthRecords);
const device = (name: string) => depthDevices.find(record => record.metadata.name === name)!;

function showRack() {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={['/?kind=Rack&namespace=default&record=rack-02']}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
}
const face = (side: 'front' | 'rear') => screen.getByRole('list', { name: `${side} recorded placements` });
const blocks = (side: 'front' | 'rear') => within(face(side)).getAllByRole('link').map(link => ({ name: link.querySelector('.inventory-rack-plan-name')?.textContent, far: link.parentElement!.hasAttribute('data-far'), depth: link.parentElement!.getAttribute('data-depth'), overlap: link.getAttribute('data-attention') === 'conflict' }));

describe('rack elevation by face', () => {
  it('draws full-depth devices on both faces, muted and labelled on the far side', () => {
    showRack();
    expect(blocks('front')).toContainEqual({ name: 'Example compute 07', far: false, depth: 'full', overlap: false });
    expect(blocks('rear')).toContainEqual({ name: 'rear of Example compute 07', far: true, depth: 'full', overlap: false });
    const far = within(face('rear')).getByRole('link', { name: /^Review Rear of Example compute 07/ });
    expect(far).toHaveAccessibleName('Review Rear of Example compute 07 · U10 · 1U · Reported placement · mounted front · Full depth (catalog), rear');
    expect(far.parentElement).toHaveClass('inventory-rack-plan-far');
    expect(far.parentElement).toHaveStyle({ gridRow: '33 / span 1' });
  });

  it('draws a reversed switch primarily on the rear and its chassis rear on the front', () => {
    showRack();
    expect(blocks('rear')).toContainEqual({ name: 'Example leaf 01', far: false, depth: 'full', overlap: false });
    expect(blocks('front')).toContainEqual({ name: 'rear of Example leaf 01', far: true, depth: 'full', overlap: false });
  });

  it('draws half-depth devices only on their face, so two can share a unit', () => {
    showRack();
    expect(blocks('front').filter(block => /patch panel/.test(block.name ?? ''))).toEqual([{ name: 'Example patch panel 01', far: false, depth: 'half', overlap: false }]);
    expect(blocks('rear').filter(block => /patch panel/.test(block.name ?? ''))).toEqual([{ name: 'Example patch panel 02', far: false, depth: 'half', overlap: false }]);
    // The other side of a half-depth device shows unverified units, not free space.
    const u42 = within(face('front')).getByText('U42').parentElement!;
    expect(u42).not.toHaveTextContent('—');
    const u41 = within(face('front')).getByText('U41').parentElement!;
    expect(within(u41).getByLabelText('Occupancy unverified')).toBeInTheDocument();
    // Nothing may claim free, open or available space.
    expect(within(face('front')).queryByText(/(?<!not )free|open|available/i)).not.toBeInTheDocument();
  });

  it('labels unknown depth, draws it on both faces and flags same-face overlap as records disagree', () => {
    showRack();
    expect(blocks('front')).toContainEqual({ name: 'Example appliance 03', far: false, depth: 'unknown', overlap: false });
    expect(blocks('rear')).toContainEqual({ name: 'rear of Example appliance 03', far: true, depth: 'unknown', overlap: true });
    expect(blocks('rear')).toContainEqual({ name: 'Example console server 01', far: false, depth: 'half', overlap: true });
    const unknown = within(face('rear')).getByRole('link', { name: /^Review Rear of Example appliance 03/ });
    expect(unknown).toHaveAccessibleName(/Depth unknown · records disagree: overlapping placements on this face/);
    // Range, height and depth are the tooltip, not text in the slot.
    expect(unknown.getAttribute('title')).toMatch(/U20–U21 · 2U · .*Depth unknown/);
    expect(unknown.querySelector('small')).toBeNull();
  });

  it('keeps the far-side block when only one face is shown', () => {
    showRack();
    fireEvent.click(screen.getByRole('button', { name: 'Rear' }));
    expect(screen.queryByRole('list', { name: 'front recorded placements' })).not.toBeInTheDocument();
    expect(blocks('rear').map(block => block.name)).toContain('rear of Example compute 07');
  });
});

describe('connection view by chassis face', () => {
  it('groups ports by chassis face with the derived rack face, and notes an opposite-face cable', () => {
    render(<MemoryRouter><DeviceConnections device={device('compute-07')} inventory={inventory} href={inventoryHref} /></MemoryRouter>);
    // The port tiles lay each chassis face out as its own faceplate, stating the rack face once.
    const tiles = screen.getByRole('group', { name: /Port tiles/ });
    const rearPlate = within(tiles).getByRole('group', { name: 'Rear · rack rear' });
    expect(within(rearPlate).getByRole('list', { name: 'Network ports' })).toBeInTheDocument();
    expect(within(tiles).getByRole('group', { name: 'Front · rack front' })).toBeInTheDocument();
    fireEvent.click(within(rearPlate).getByRole('button', { name: /^Ethernet 1:/ }));
    expect(screen.getByRole('dialog')).toHaveTextContent(/Check.*this end is on the rack rear and the far end on the rack front/);
    // In the table the soft check is a short marker, its sentence the tooltip and accessible text.
    const tableRows = within(screen.getByRole('table', { name: 'Components and Cables' })).getAllByRole('row').slice(1);
    const eth1 = tableRows.find(row => within(row).getAllByRole('cell')[0].textContent === 'Ethernet 1')!;
    expect(eth1.querySelector('.inventory-connection-check')).toHaveTextContent(/^· Check this end is on the rack rear and the far end on the rack front/);
    expect(within(eth1).getAllByRole('cell')[3].querySelector('[title]')!.getAttribute('title')).toMatch(/^Check: this end is on the rack rear/);
    // The face is short: the rack face matches the chassis face, so it goes without saying.
    expect(within(eth1).getAllByRole('cell').slice(1, 3).map(cell => cell.textContent)).toEqual(['Network SFP28', 'Rear']);
  });

  it('draws one unlabelled faceplate when no face is recorded, and the table says the face is unknown', () => {
    render(<MemoryRouter><DeviceConnections device={device('appliance-03')} inventory={inventory} href={inventoryHref} /></MemoryRouter>);
    // No face is recorded, so the tiles draw one unlabelled faceplate.
    expect(within(screen.getByRole('group', { name: /Port tiles/ })).queryByText('Face unknown')).not.toBeInTheDocument();
    const rows = within(screen.getByRole('table', { name: 'Components and Cables' })).getAllByRole('row').slice(1);
    expect(rows.map(row => within(row).getAllByRole('cell')[2].textContent)).toEqual(rows.map(() => 'Face unknown'));
  });
});
