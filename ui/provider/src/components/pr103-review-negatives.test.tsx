import { findEnabledButton } from '../test/enabled-button';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import DeviceInventoryList from '../pages/device-inventory-list';
import { inventoryPayload } from '../lib/inventory-management';
import { StrictMode } from 'react';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
const response = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ data }) });
it.each([false, true])('does not publish new-principal reads while explicit host identity is unresolved strict=%s', async strict => {
  let principal = 'staff-a';
  let release: (() => void) | undefined;
  const requests = vi.fn(async (url: string) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    const record = inventoryPayload('Facility', { name: 'review-facility', namespace: 'default', displayName: `Private ${principal}`, facility: '', height: '' });
    return response({ items: url.includes('/facilities?') ? [record] : [] });
  });
  vi.stubGlobal('fetch', requests);
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: async () => {
    if (principal !== 'staff-a') await new Promise<void>(resolve => { release = resolve; });
    return { user: { metadata: { uid: principal } } };
  }, element: <Outlet />, children: [{ index: true, element: <DeviceInventoryList /> }] }], { initialEntries: ['/?kind=Facility'] });
  render(strict ? <StrictMode><RouterProvider router={router} /></StrictMode> : <RouterProvider router={router} />);
  await screen.findByRole('link', { name: /Private staff-a/ });
  const refresh = screen.getByRole('button', { name: 'Refresh inventory' });
  const before = requests.mock.calls.length;
  principal = 'staff-b';
  act(() => { void router.revalidate(); });
  await waitFor(() => expect(release).toBeDefined());
  fireEvent.click(refresh);
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 30)); });
  expect(router.state.loaderData['layouts/milo.layout'].user.metadata.uid).toBe('staff-a');
  expect(screen.queryByRole('link', { name: /Private staff-b/ })).not.toBeInTheDocument();
  expect(requests).toHaveBeenCalledTimes(before);
  router.dispose();
});
