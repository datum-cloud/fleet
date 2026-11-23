import { fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { PhysicalInventory } from './physical-inventory';
import { fixtureInventory } from '../lib/inventory-fixtures';

// Synthetic fixture records only. Rack A01 (42U): devices at U2–U8, U20–U21
// and U40 on both faces, and half-depth rear equipment at U41–U42, so the
// blank runs on both faces are U9–U19 and U22–U39.
const RACK = (extra = '') => `/plugins/metal-datumapis-com/inventory?kind=Rack&record=example-rack-a01&namespace=default${extra}`;

function open(path = RACK()) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={[path]}><PhysicalInventory inventory={fixtureInventory()} /></MemoryRouter></QueryClientProvider>);
}
const face = (side: 'front' | 'rear') => screen.getByRole('list', { name: `${side} recorded placements` });
const run = (side: 'front' | 'rear', range = 'U22–U39') => within(face(side)).getByRole('button', { name: new RegExp(`^${range} · `) });
const unitLabels = (side: 'front' | 'rear') => within(face(side)).queryAllByText(/^U\d+$/).map(item => item.textContent);

afterEach(() => vi.restoreAllMocks());

describe('collapsed empty runs in the elevation', () => {
  it('collapses each run of three or more blank units into one row, never calling it free', () => {
    open();
    for (const side of ['front', 'rear'] as const) {
      const row = run(side);
      expect(row).toHaveTextContent(/^U22–U39 · 18 units · no placement recorded \(not free\)$/);
      expect(row).toHaveAttribute('aria-expanded', 'false');
      expect(run(side, 'U9–U19')).toHaveTextContent('U9–U19 · 11 units · no placement recorded (not free)');
      // Collapsed units are not drawn one by one.
      expect(unitLabels(side)).not.toContain('U30');
      expect(unitLabels(side)).toContain('U1');
    }
    // U1 is a single blank unit, and U41–U42 on the front are two: both stay as units.
    expect(within(face('front')).queryByRole('button', { name: /^U41–U42/ })).not.toBeInTheDocument();
    expect(within(within(face('front')).getByText('U41').parentElement!).getByLabelText('Occupancy unverified')).toBeInTheDocument();
    expect(screen.getByText(/runs of three or more blank units collapse into one row where both faces are blank/)).toBeInTheDocument();
  });

  it('gives collapsed rows accessible names that start with the visible words and say which face', () => {
    open();
    expect(run('front')).toHaveAccessibleName('U22–U39 · 18 units · no placement recorded (not free), on either face. Show each unit');
    fireEvent.click(screen.getByRole('button', { name: 'Front' }));
    expect(run('front')).toHaveAccessibleName('U22–U39 · 18 units · no placement recorded (not free), on the front face. Show each unit');
    fireEvent.click(screen.getByRole('button', { name: 'Rear' }));
    expect(run('rear')).toHaveAccessibleName('U22–U39 · 18 units · no placement recorded (not free), on the rear face. Show each unit');
  });

  it('keeps front and rear rows aligned: both faces share one row template and every placement keeps its row', () => {
    open();
    expect(face('front').style.gridTemplateRows).toBe(face('rear').style.gridTemplateRows);
    expect(face('front').style.gridTemplateRows).toContain('var(--inventory-run-row, 24px)');
    // Rows stay one per unit (row = 43 − unit), so the run spans its units and the leaf at U40 keeps row 3.
    expect(run('front').parentElement).toHaveStyle({ gridRow: '4 / span 18' });
    expect(run('rear').parentElement).toHaveStyle({ gridRow: '4 / span 18' });
    expect(within(face('rear')).getByRole('link', { name: /^Review Leaf a01/ }).parentElement).toHaveStyle({ gridRow: '3 / span 1' });
  });

  it('expands a run in place by click, on both faces, and collapses it again', () => {
    open();
    fireEvent.click(run('front'));
    for (const side of ['front', 'rear'] as const) {
      expect(within(face(side)).queryByRole('button', { name: /^U22–U39 · / })).not.toBeInTheDocument();
      expect(unitLabels(side)).toEqual(expect.arrayContaining(Array.from({ length: 18 }, (_, index) => `U${22 + index}`)));
      // Each unit is unverified, never free.
      expect(within(within(face(side)).getByText('U30').parentElement!).getByLabelText('Occupancy unverified')).toBeInTheDocument();
    }
    expect(face('front').style.gridTemplateRows).toBe(face('rear').style.gridTemplateRows);
    const collapse = within(face('front')).getByRole('button', { name: /^Collapse U22–U39/ });
    expect(collapse).toHaveFocus();
    expect(collapse).toHaveAttribute('aria-expanded', 'true');
    fireEvent.click(collapse);
    expect(run('front')).toHaveFocus();
    expect(unitLabels('front')).not.toContain('U30');
  });

  it('expands and collapses with Enter', () => {
    open();
    fireEvent.keyDown(run('rear'), { key: 'Enter' });
    expect(unitLabels('rear')).toContain('U30');
    const collapse = within(face('rear')).getByRole('button', { name: /^Collapse U22–U39/ });
    expect(collapse).toHaveFocus();
    fireEvent.keyDown(collapse, { key: 'Enter' });
    expect(unitLabels('rear')).not.toContain('U30');
    expect(run('rear')).toHaveFocus();
  });

  it('moves across collapsed rows with the arrow keys, in drawn order, and Enter expands the focused row', () => {
    vi.spyOn(window, 'matchMedia').mockImplementation(query => ({ matches: false, media: query, onchange: null, addListener: () => {}, removeListener: () => {}, addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => false }) as MediaQueryList);
    open();
    const leaf = within(face('front')).getByRole('link', { name: /^Review Rear of Leaf a01/ });
    leaf.focus();
    fireEvent.keyDown(leaf, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(run('front'));
    fireEvent.keyDown(document.activeElement!, { key: 'ArrowDown' });
    expect(document.activeElement).toHaveAccessibleName(/^Review Appliance a01/);
    fireEvent.keyDown(document.activeElement!, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(run('front', 'U9–U19'));
    fireEvent.keyDown(document.activeElement!, { key: 'ArrowUp' });
    fireEvent.keyDown(document.activeElement!, { key: 'ArrowUp' });
    expect(document.activeElement).toBe(run('front'));
    fireEvent.keyDown(document.activeElement!, { key: 'Enter' });
    // Enter expands rather than selecting anything; the rack page selection is unchanged.
    expect(screen.queryByTestId('rack-device-panel')).not.toBeInTheDocument();
    const collapse = within(face('front')).getByRole('button', { name: /^Collapse U22–U39/ });
    expect(collapse).toHaveFocus();
    fireEvent.keyDown(collapse, { key: 'ArrowDown' });
    expect(document.activeElement).toHaveAccessibleName(/^Review Appliance a01/);
  });

  it.each([['silhouette', '12px'], ['full', '44px'], [undefined, '16px']])('collapses at every level of detail (?detail=%s)', (detail, size) => {
    open(RACK(detail ? `&detail=${detail}` : ''));
    const rows = face('front').style.gridTemplateRows;
    // U42–U40 at the unit size, then the run: one compact row and seventeen of zero height.
    expect(rows).toContain('var(--inventory-run-row, 24px) repeat(17, 0px)');
    expect(rows.startsWith(`repeat(3, ${size}) var(`)).toBe(true);
    expect(run('front')).toBeInTheDocument();
    expect(within(face('rear')).getByRole('link', { name: /^Review Leaf a01/ })).toHaveAttribute('data-level', detail ?? 'reduced');
    fireEvent.click(run('front'));
    // U42–U20 are now 23 unit rows; U9–U19 stays collapsed.
    expect(face('front').style.gridTemplateRows).toBe(`repeat(23, ${size}) var(--inventory-run-row, 24px) repeat(10, 0px) repeat(8, ${size})`);
  });
});
