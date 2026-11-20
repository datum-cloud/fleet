import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { inventoryPayload, INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';

const values = { name: 'synthetic-only', namespace: 'default', displayName: 'Original', recordedBy: 'operator', reference: '', facility: '', height: '' };
const original = { ...inventoryPayload('Facility', values, '2026-01-01T00:00:00Z'), metadata: { name: values.name, namespace: 'default', uid: 'synthetic', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const response = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ data }) });
afterEach(() => vi.unstubAllGlobals());

it.each(['create-blur', 'create-unmount', 'edit-blur', 'edit-unmount', 'create-dispatched-blur', 'edit-dispatched-blur'])('revokes pending authority on %s', async mode => {
  let release: ((value: unknown) => void) | undefined;
  let reviews = 0;
  const writes: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) {
      if (++reviews === 2 && !mode.includes('dispatched')) return new Promise(resolve => { release = resolve; });
      return response({ status: { allowed: true } });
    }
    if (init.method !== 'GET') {
      writes.push(init.method!);
      if (mode.includes('dispatched')) return new Promise(resolve => { release = resolve; });
    }
    return response(original);
  }));
  const editing = mode.startsWith('edit');
  const mounted = render(<QueryClientProvider client={new QueryClient()}><InventoryManagement kind="Facility" record={editing ? original : undefined} facilities={[]} /></QueryClientProvider>);
  await screen.findByRole('button', { name: editing ? 'Edit display name' : 'Create facility' });
  fireEvent.click(screen.getByText(editing ? 'Manage this record' : 'Manage facilities'));
  fireEvent.click(screen.getByRole('button', { name: editing ? 'Edit display name' : 'Create facility' }));
  if (!editing) fireEvent.change(screen.getByLabelText('Record name'), { target: { value: values.name } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Retained safe input' } });
  fireEvent.change(screen.getByLabelText('Recorded by'), { target: { value: values.recordedBy } });
  fireEvent.click(screen.getByRole('button', { name: editing ? 'Save display name' : 'Create facility record' }));
  await waitFor(() => expect(release).toBeDefined());
  if (mode.endsWith('unmount')) mounted.unmount();
  else fireEvent(window, new Event('blur'));
  await act(async () => { release!(response(mode.includes('dispatched') ? original : { status: { allowed: true } })); });
  expect(writes).toEqual(mode.includes('dispatched') ? [editing ? 'PATCH' : 'POST'] : []);
  if (mode.endsWith('blur')) {
    expect(screen.getByRole('status')).toHaveTextContent(mode.includes('dispatched') ? 'outcome is unknown' : 'No write was dispatched');
    expect(screen.queryByText(/Display name saved\.|Facility record created\./)).not.toBeInTheDocument();
    expect(screen.getByLabelText('Display name')).toHaveValue('Retained safe input');
    expect(screen.getByRole('button', { name: editing ? 'Save display name' : 'Create facility record' })).toBeDisabled();
  }
});
