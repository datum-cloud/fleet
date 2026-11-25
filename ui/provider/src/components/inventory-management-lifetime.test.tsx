import { findEnabledButton } from '../test/enabled-button';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { inventoryPayload, INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';

const values = { name: 'synthetic-only', namespace: 'default', displayName: 'Original', facility: '', height: '' };
const original = { ...inventoryPayload('Facility', values), metadata: { name: values.name, namespace: 'default', uid: 'synthetic', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const response = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ data }) });
afterEach(() => vi.unstubAllGlobals());

it.each(['create-unmount', 'edit-unmount'])('revokes pending authority on %s', async mode => {
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
  await findEnabledButton({ name: editing ? 'Edit display name' : 'Create facility' });
  fireEvent.click(screen.getByRole('button', { name: editing ? 'Edit display name' : 'Create facility' }));
  if (!editing) fireEvent.change(screen.getByLabelText('Record name'), { target: { value: values.name } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Retained safe input' } });
  fireEvent.click(screen.getByRole('button', { name: editing ? 'Save changes' : 'Create record' }));
  await waitFor(() => expect(release).toBeDefined());
  if (mode.endsWith('unmount')) mounted.unmount();
  else fireEvent(window, new Event('pagehide'));
  await act(async () => { release!(response(mode.includes('dispatched') ? original : { status: { allowed: true } })); });
  expect(writes).toEqual(mode.includes('dispatched') ? [editing ? 'PATCH' : 'POST'] : []);
  if (mode.endsWith('pagehide')) {
    expect(screen.getByRole('status')).toHaveTextContent(mode.includes('dispatched') ? 'outcome is unknown' : 'No write was dispatched');
    expect(screen.queryByText(/Display name saved\.|Facility created\./)).not.toBeInTheDocument();
    expect(screen.getByLabelText('Display name')).toHaveValue('Retained safe input');
    expect(screen.getByRole('button', { name: editing ? 'Save changes' : 'Create record' })).toBeDisabled();
  }
});

it.each(['create', 'edit'])('%s: hiding the tab mid-save leaves the form and the save alone', async mode => {
  let release: ((value: unknown) => void) | undefined;
  let reviews = 0;
  const writes: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) {
      if (++reviews === 2) return new Promise(resolve => { release = resolve; });
      return response({ status: { allowed: true } });
    }
    if (init.method !== 'GET') writes.push(init.method!);
    return response(original);
  }));
  const editing = mode === 'edit';
  render(<QueryClientProvider client={new QueryClient()}><InventoryManagement kind="Facility" record={editing ? original : undefined} facilities={[]} /></QueryClientProvider>);
  fireEvent.click(await findEnabledButton({ name: editing ? 'Edit display name' : 'Create facility' }));
  if (!editing) fireEvent.change(screen.getByLabelText('Record name'), { target: { value: values.name } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Kept across tab switch' } });
  fireEvent.click(screen.getByRole('button', { name: editing ? 'Save changes' : 'Create record' }));
  await waitFor(() => expect(release).toBeDefined());
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
  fireEvent(document, new Event('visibilitychange'));
  fireEvent(window, new Event('pagehide'));
  visibility.mockReturnValue('visible');
  fireEvent(document, new Event('visibilitychange'));
  fireEvent(window, new Event('pageshow'));
  await act(async () => { release!(response({ status: { allowed: true } })); });
  await screen.findByText(editing ? 'Display name saved.' : 'Facility created.');
  expect(writes).toEqual([editing ? 'PATCH' : 'POST']);
  visibility.mockRestore();
});

it('shows no "checking access" text while the management permission is being checked', async () => {
  let allow: ((value: unknown) => void) | undefined;
  vi.stubGlobal('fetch', vi.fn(() => new Promise(resolve => { allow = resolve; })));
  render(<QueryClientProvider client={new QueryClient()}><InventoryManagement kind="Facility" facilities={[]} /></QueryClientProvider>);
  await waitFor(() => expect(allow).toBeDefined());
  expect(screen.getByRole('button', { name: 'Create facility' })).toBeDisabled();
  expect(screen.getByRole('status')).toBeEmptyDOMElement();
  await act(async () => allow!(response({ status: { allowed: true } })));
  await findEnabledButton({ name: 'Create facility' });
});
