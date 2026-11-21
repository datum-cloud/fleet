import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import DeviceInventoryList from '../pages/device-inventory-list';
import { inventoryPayload } from '../lib/inventory-management';
import { StrictMode } from 'react';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
const response = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ data }) });
it.each([false, true])('does not publish new-principal reads while focus identity is unresolved strict=%s', async strict => {
  let principal = 'staff-a';
  let release: (() => void) | undefined;
  const requests = vi.fn(async (url: string) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    const record = inventoryPayload('Facility', { name: 'review-facility', namespace: 'default', displayName: `Private ${principal}`, recordedBy: 'synthetic', reference: '', facility: '', height: '' }, '2026-01-01T00:00:00Z');
    return response({ items: url.includes('/facilities?') ? [record] : [] });
  });
  vi.stubGlobal('fetch', requests);
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: async () => {
    if (principal !== 'staff-a') await new Promise<void>(resolve => { release = resolve; });
    return { user: { metadata: { uid: principal } } };
  }, element: <Outlet />, children: [{ index: true, element: <DeviceInventoryList /> }] }], { initialEntries: ['/?kind=Facility'] });
  render(strict ? <StrictMode><RouterProvider router={router} /></StrictMode> : <RouterProvider router={router} />);
  await screen.findByRole('link', { name: /Private staff-a/ });
  principal = 'staff-b';
  fireEvent(window, new Event('blur'));
  fireEvent(window, new Event('focus'));
  await waitFor(() => expect(release).toBeDefined());
  fireEvent.click(screen.getByRole('button', { name: 'Refresh inventory' }));
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 30)); });
  expect(router.state.loaderData['layouts/milo.layout'].user.metadata.uid).toBe('staff-a');
  expect(screen.queryByRole('link', { name: /Private staff-b/ })).not.toBeInTheDocument();
  router.dispose();
});

it.each(['visible', 'overlap', 'hidden', 'denied'])('renews management only after a fresh successful read: %s', async scenario => {
  let holding = false;
  let denied = false;
  const releases: (() => void)[] = [];
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  const record = inventoryPayload('Facility', { name: 'fresh', namespace: 'default', displayName: 'Synthetic fresh facility', recordedBy: 'synthetic', reference: '', facility: '', height: '' }, '2026-01-01T00:00:00Z');
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.includes('selfsubjectaccessaccessreviews')) throw new Error('invalid endpoint');
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    if (denied) return { ok: false, status: 403, json: async () => ({ error: 'synthetic' }) };
    return response({ items: url.includes('/facilities?') ? [record] : [] });
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: async () => {
    if (holding) await new Promise<void>(resolve => releases.push(resolve));
    return { user: { metadata: { uid: 'staff-a' } } };
  }, element: <Outlet />, children: [{ index: true, element: <DeviceInventoryList /> }] }], { initialEntries: ['/?kind=Facility'] });
  render(<RouterProvider router={router} />);
  await screen.findByRole('button', { name: 'Create facility' });
  const table = screen.getByRole('table');
  fireEvent.click(screen.getByRole('button', { name: 'Create facility' }));
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Abandoned old form' } });
  holding = true;
  if (scenario === 'hidden') {
    visibility.mockReturnValue('hidden');
    fireEvent(document, new Event('visibilitychange'));
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    visibility.mockReturnValue('visible');
  } else fireEvent(window, new Event('blur'));
  fireEvent(window, new Event('focus'));
  await waitFor(() => expect(releases).toHaveLength(1));
  expect(screen.queryByLabelText('Display name')).not.toBeInTheDocument();
  if (scenario === 'overlap') {
    fireEvent(window, new Event('focus'));
    await waitFor(() => expect(releases).toHaveLength(2));
    await act(async () => releases[0]());
    expect(screen.queryByRole('button', { name: 'Create facility' })).not.toBeInTheDocument();
  }
  denied = scenario === 'denied';
  await act(async () => releases.at(-1)!());
  if (denied) {
    await screen.findByRole('heading', { name: 'Inventory access needed' });
    expect(screen.queryByRole('button', { name: 'Create facility' })).not.toBeInTheDocument();
    denied = false;
    holding = false;
    fireEvent(window, new Event('focus'));
  }
  await screen.findByRole('button', { name: 'Create facility' });
  if (scenario === 'visible' || scenario === 'overlap') expect(screen.getByRole('table')).toBe(table);
  fireEvent.click(screen.getByRole('button', { name: 'Create facility' }));
  expect(screen.getByLabelText('Display name')).toHaveValue('');
  router.dispose();
  visibility.mockRestore();
});
