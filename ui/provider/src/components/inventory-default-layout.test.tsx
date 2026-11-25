import { fireEvent, render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { beforeEach, expect, it, vi } from 'vitest';
import { PhysicalInventory } from './physical-inventory';
import { InventoryNavigation, sections } from './inventory-navigation';
import { isCatalogKind } from './inventory-groups';
import { chooseCategory } from '../test/inventory-category';
import { InventoryPendingShell } from './inventory-directory-heading';
import { toInventory, type Inventory } from '../inventory';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';
import { useInventoryPermission } from '../lib/inventory-management';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import { readFileSync } from 'node:fs';

const styles = readFileSync('src/components/physical-inventory.css', 'utf8');

vi.mock('../lib/inventory-management', async () => ({ ...await vi.importActual('../lib/inventory-management'), useInventoryPermission: vi.fn() }));
const permission = vi.mocked(useInventoryPermission);
const catalog: PhysicalRecord[] = [
  { kind: 'Manufacturer', metadata: { name: 'vendor', namespace: 'default' }, spec: { displayName: 'Private vendor' } },
  { kind: 'DeviceType', metadata: { name: 'model', namespace: 'default' }, spec: { displayName: 'Private model', model: 'Private model', manufacturerRef: { name: 'vendor' } } },
];
const inventory = toInventory(fixturePhysicalClaims, fixturePhysicalDevices, [...fixturePhysicalRecords, ...catalog]);
beforeEach(() => permission.mockReturnValue({ data: true, isFetching: false, error: null } as never));

function shape(container: HTMLElement) {
  return [...container.querySelectorAll('.inventory-list-heading, .inventory-management-controls, .inventory-management-status, .inventory-toolbar')].map(element => [element.tagName, element.className]);
}

it.each(sections)('keeps $kind category, heading, action and filter structure across pending/allowed/denied', section => {
  const client = new QueryClient();
  const catalogKind = isCatalogKind(section.kind);
  const view = (data?: Inventory) => <QueryClientProvider client={client}><MemoryRouter initialEntries={[`/?kind=${section.kind}`]}><InventoryNavigation scope={catalogKind ? 'catalog' : 'inventory'} />{data ? <PhysicalInventory inventory={data} showNavigation={false} /> : <InventoryPendingShell><p>Checking inventory access…</p></InventoryPendingShell>}</MemoryRouter></QueryClientProvider>;
  const result = render(view());
  const navName = catalogKind ? 'Catalog categories' : 'Inventory categories';
  const nav = screen.getByRole('navigation', { name: navName });
  const navMarkup = nav.innerHTML;
  const pendingShape = shape(result.container);
  const toolbarLabels = [...result.container.querySelectorAll('.inventory-toolbar [aria-label]')].map(element => element.getAttribute('aria-label'));
  if (catalogKind) expect(within(nav).getByRole('link', { name: section.title })).toHaveAttribute('aria-current', 'page');
  else expect(within(nav).getByRole('button', { name: `Inventory category: ${section.title}` })).toBeInTheDocument();
  expect(screen.getByRole('heading', { name: section.title })).toBeInTheDocument();
  expect(screen.getByRole('searchbox')).toBeDisabled();
  expect(result.container.textContent).not.toMatch(/Private vendor|Private model|Example compute server|\d+ of \d+ records/);
  result.rerender(view(inventory));
  expect(shape(result.container)).toEqual(pendingShape);
  expect(screen.getByRole('searchbox')).toBeEnabled();
  expect(screen.getByRole('navigation', { name: navName })).toBe(nav);
  expect(nav.innerHTML).toBe(navMarkup);
  result.rerender(view({ ...inventory, issues: { [section.kind]: { status: 403 } } }));
  expect(shape(result.container)).toEqual(pendingShape);
  expect(nav.innerHTML).toBe(navMarkup);
  expect(screen.getByRole('heading', { name: section.title })).toBeInTheDocument();
  expect(screen.getByRole('searchbox')).toBeDisabled();
  expect([...result.container.querySelectorAll('.inventory-toolbar [aria-label]')].map(element => element.getAttribute('aria-label'))).toEqual(toolbarLabels);
  expect(screen.queryByRole('table')).not.toBeInTheDocument();
  expect(result.container.textContent).not.toMatch(/Private vendor|Private model|Example compute server|\d+ of \d+ records/);
  expect(screen.queryByText('Namespace & access')).not.toBeInTheDocument();
  expect(screen.queryByLabelText('Management namespace')).not.toBeInTheDocument();
  result.rerender(view(inventory));
  expect(shape(result.container)).toEqual(pendingShape);
  expect(nav.innerHTML).toBe(navMarkup);
});

it('retains the same action DOM while permission changes and reserves CSS geometry', () => {
  const client = new QueryClient();
  const view = () => <QueryClientProvider client={client}><MemoryRouter initialEntries={['/?kind=Rack']}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>;
  permission.mockReturnValue({ data: undefined, isFetching: true, error: null } as never);
  const result = render(view());
  const button = screen.getByRole('button', { name: 'Create rack' });
  const heading = screen.getByRole('heading', { name: 'Racks' });
  expect(button).toBeDisabled();
  for (const allowed of [true, false, true]) {
    permission.mockReturnValue({ data: allowed, isFetching: false, error: null } as never);
    result.rerender(view());
    expect(screen.getByRole('button', { name: 'Create rack' })).toBe(button);
    expect(screen.getByRole('heading', { name: 'Racks' })).toBe(heading);
    expect(button.hasAttribute('disabled')).toBe(!allowed);
  }
  expect(styles).toContain('.fleet-inventory .inventory-management-controls > button { min-width:180px; }');
  expect(styles).toContain('min-height:3em;');
});

it.each(sections)('rejects a forced foreign $kind detail even when a same-named record is supplied', section => {
  const records = [...inventory.physical, ...fixturePhysicalDevices.map(record => ({ ...record, kind: 'Device' as const })), ...fixturePhysicalClaims.map(record => ({ ...record, kind: 'DeviceClaim' as const }))];
  const selected = records.find(record => record.kind === section.kind)!;
  const foreign = { ...selected, metadata: { ...selected.metadata, namespace: 'other' }, spec: { ...selected.spec, displayName: 'Foreign private record' } };
  const source = toInventory([...fixturePhysicalClaims, ...(section.kind === 'DeviceClaim' ? [foreign as typeof fixturePhysicalClaims[number]] : [])], [...fixturePhysicalDevices, ...(section.kind === 'Device' ? [foreign as typeof fixturePhysicalDevices[number]] : [])], [...inventory.physical, ...(!['Device', 'DeviceClaim'].includes(section.kind) ? [foreign as PhysicalRecord] : [])]);
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={[`/?kind=${section.kind}&record=${selected.metadata.name}&namespace=other`]}><PhysicalInventory inventory={source} /></MemoryRouter></QueryClientProvider>);
  expect(screen.getByRole('heading', { name: 'Record not found' })).toBeInTheDocument();
  expect(screen.getByText(/only supports the default namespace/)).toBeInTheDocument();
  expect(screen.queryByText('Foreign private record')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /Edit display name|Correct location/ })).not.toBeInTheDocument();
  if (isCatalogKind(section.kind)) fireEvent.click(within(screen.getByRole('navigation', { name: 'Catalog categories' })).getByRole('link', { name: section.title }));
  else chooseCategory(section.title);
  expect(screen.queryByText('Foreign private record')).not.toBeInTheDocument();
  expect(screen.queryByRole('columnheader', { name: 'Namespace' })).not.toBeInTheDocument();
});
