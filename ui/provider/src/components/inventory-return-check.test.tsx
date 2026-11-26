import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import DeviceInventoryList from '../pages/device-inventory-list';
import { findEnabledButton } from '../test/enabled-button';
import { fixturePhysicalRecords } from '../lib/fixtures';

// Staff-portal does not re-check the signed-in user when a tab returns (its
// Milo layout loader runs on navigations and explicit revalidation only), so
// the inventory session boundary does: see "Session boundary" in
// docs/development/staff-inventory-ui.md. These tests drive the real page.

beforeEach(() => vi.stubGlobal('confirm', vi.fn(() => true)));
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

function host() {
  let principal = 'staff-a';
  let holdLoader = false;
  let confirmLoader: (() => void) | undefined;
  let holdReview = false;
  let releaseReview: (() => void) | undefined;
  const collections = vi.fn();
  const reviews = vi.fn();
  const writes: { method: string; principal: string }[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) {
      reviews();
      if (holdReview) { holdReview = false; await new Promise<void>(resolve => { releaseReview = resolve; }); }
      return Response.json({ data: { status: { allowed: true } } });
    }
    if (init.method !== 'GET') {
      writes.push({ method: init.method!, principal });
      return Response.json({ data: { kind: 'Facility', metadata: { name: 'synthetic-new', namespace: 'default', uid: 'synthetic-new-uid', resourceVersion: '1' }, spec: { displayName: 'Synthetic new' } } });
    }
    collections();
    // Each synthetic user sees different records, so a stale view is detectable.
    if (principal !== 'staff-a') return Response.json({ data: { items: [] } });
    const kind = url.includes('/manufacturers') ? 'Manufacturer' : url.includes('/devicetypes') ? 'DeviceType' : url.includes('/facilities') ? 'Facility' : url.includes('/racks') ? 'Rack' : url.includes('/cables') ? 'Cable' : url.includes('/powerfeeds') ? 'PowerFeed' : url.includes('/devices') || url.includes('/deviceclaims') ? 'none' : 'Site';
    return Response.json({ data: { items: fixturePhysicalRecords.filter(record => record.kind === kind) } });
  }));
  const loader = vi.fn(async () => {
    if (holdLoader) { holdLoader = false; await new Promise<void>(resolve => { confirmLoader = resolve; }); }
    return { user: { metadata: { uid: principal } } };
  });
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader, element: <Outlet />, children: [{ path: 'plugins/:slug/inventory', element: <DeviceInventoryList /> }] }], { initialEntries: ['/plugins/metal/inventory?kind=Facility'] });
  render(<RouterProvider router={router} />);
  // Every text the page shows during the test, to prove nothing flashed.
  const shown: string[] = [];
  const observer = new MutationObserver(records => { for (const record of records) {
    if (record.type === 'characterData') shown.push(record.target.textContent ?? '');
    for (const node of record.addedNodes) shown.push(node.textContent ?? '');
  } });
  observer.observe(document.body, { subtree: true, childList: true, characterData: true });
  return { router, loader, collections, reviews, writes, shown,
    signIn: (next: string) => { principal = next; },
    holdLoader: () => { holdLoader = true; confirmLoader = undefined; },
    confirmLoader: async () => { await waitFor(() => expect(confirmLoader).toBeDefined()); await act(async () => confirmLoader!()); },
    holdReview: () => { holdReview = true; releaseReview = undefined; },
    releaseReview: async () => { await waitFor(() => expect(releaseReview).toBeDefined()); await act(async () => releaseReview!()); },
    dispose: () => { observer.disconnect(); router.dispose(); },
  };
}

const visibility = () => vi.spyOn(document, 'visibilityState', 'get');
function hide(state: ReturnType<typeof visibility>) { state.mockReturnValue('hidden'); fireEvent(document, new Event('visibilitychange')); }
function show(state: ReturnType<typeof visibility>) { state.mockReturnValue('visible'); fireEvent(document, new Event('visibilitychange')); }

async function startHeldCreate(view: ReturnType<typeof host>) {
  fireEvent.click(await findEnabledButton({ name: 'Create facility' }));
  fireEvent.change(screen.getByLabelText('Record name'), { target: { value: 'synthetic-new' } });
  fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Synthetic new' } });
  // The save's own permission review is held, so the write has not left when the tab is hidden.
  view.holdReview();
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  await screen.findByRole('button', { name: 'Saving…' });
}

it('same user on return: no flash, one host check and no other request, and a held save resumes', async () => {
  const view = host();
  await findEnabledButton({ name: 'Create facility' });
  view.shown.length = 0;
  const table = screen.getByRole('table');
  const row = screen.getByRole('link', { name: 'Example facility' });
  const collections = view.collections.mock.calls.length;
  const reviews = view.reviews.mock.calls.length;
  expect(view.loader).toHaveBeenCalledTimes(1);
  await startHeldCreate(view);
  const state = visibility();
  hide(state);
  view.holdLoader();
  show(state);
  await waitFor(() => expect(view.loader).toHaveBeenCalledTimes(2));
  // The review answers while the host is still confirming the user: the write waits.
  await view.releaseReview();
  expect(view.writes).toEqual([]);
  expect(screen.getByRole('button', { name: 'Saving…' })).toBeInTheDocument();
  await view.confirmLoader();
  await screen.findByText('Facility created.');
  expect(view.writes).toEqual([{ method: 'POST', principal: 'staff-a' }]);
  // The save's own review was the only review; the created record is then re-read as after any save.
  expect(view.reviews).toHaveBeenCalledTimes(reviews + 1);
  expect(view.collections.mock.calls.length).toBeGreaterThanOrEqual(collections);
  expect(screen.getByRole('table')).toBe(table);
  // The save refetches inventory as any save does, so the row is re-rendered; it never left the page.
  expect(screen.getByRole('link', { name: 'Example facility' })).toBeInTheDocument();
  expect(row).toHaveTextContent('Example facility');
  expect(view.loader).toHaveBeenCalledTimes(2);
  expect(view.shown.filter(text => /Checking|Loading/i.test(text))).toEqual([]);
  view.dispose();
});

it('same user on return with nothing pending reads nothing but the host check, and holds reads until it resolves', async () => {
  const view = host();
  await findEnabledButton({ name: 'Create facility' });
  view.shown.length = 0;
  const table = screen.getByRole('table');
  const requests = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls.length;
  const state = visibility();
  hide(state);
  show(state);
  await waitFor(() => expect(view.loader).toHaveBeenCalledTimes(2));
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  expect(fetch).toHaveBeenCalledTimes(requests);
  expect(screen.getByRole('table')).toBe(table);
  // Reads wait during a check: Refresh sends nothing until the host confirms the user.
  hide(state);
  view.holdLoader();
  show(state);
  await waitFor(() => expect(view.loader).toHaveBeenCalledTimes(3));
  expect(view.shown.filter(text => /Checking|Loading/i.test(text))).toEqual([]);
  view.shown.length = 0;
  fireEvent.click(screen.getByRole('button', { name: 'Refresh inventory' }));
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  expect(fetch).toHaveBeenCalledTimes(requests);
  await view.confirmLoader();
  await waitFor(() => expect(view.collections.mock.calls.length).toBeGreaterThan(0));
  await waitFor(() => expect((fetch as unknown as ReturnType<typeof vi.fn>).mock.calls.length).toBeGreaterThan(requests));
  expect(screen.getByRole('table')).toBe(table);
  expect(await findEnabledButton({ name: 'Create facility' })).toBeEnabled();
  // (Refresh itself re-checks management access, which may describe the Create button as checking; that is Refresh, not the return check.)
  expect(view.shown.filter(text => /Loading/i.test(text))).toEqual([]);
  view.dispose();
});

it('a different user on return: the old data is gone and the held save is never sent', async () => {
  const view = host();
  await findEnabledButton({ name: 'Create facility' });
  expect(screen.getByRole('link', { name: 'Example facility' })).toBeInTheDocument();
  await startHeldCreate(view);
  const state = visibility();
  hide(state);
  // Another user signs in elsewhere while this tab is hidden.
  view.signIn('staff-b');
  view.holdLoader();
  show(state);
  await waitFor(() => expect(view.loader).toHaveBeenCalledTimes(2));
  await view.releaseReview();
  expect(view.writes).toEqual([]);
  await view.confirmLoader();
  await waitFor(() => expect(screen.queryByRole('link', { name: 'Example facility' })).not.toBeInTheDocument());
  expect(screen.queryByLabelText('Display name')).not.toBeInTheDocument();
  expect(screen.queryByText('Facility created.')).not.toBeInTheDocument();
  await findEnabledButton({ name: 'Create facility' });
  expect(view.writes).toEqual([]);
  view.dispose();
});

it.each(['staff-a', 'staff-b'])('a back-forward-cache pageshow checks the user again (%s on return)', async next => {
  const view = host();
  await findEnabledButton({ name: 'Create facility' });
  view.shown.length = 0;
  const table = screen.getByRole('table');
  await startHeldCreate(view);
  fireEvent(window, new PageTransitionEvent('pagehide', { persisted: true }));
  view.signIn(next);
  view.holdLoader();
  fireEvent(window, new PageTransitionEvent('pageshow', { persisted: true }));
  await waitFor(() => expect(view.loader).toHaveBeenCalledTimes(2));
  await view.releaseReview();
  expect(view.writes).toEqual([]);
  await view.confirmLoader();
  if (next === 'staff-a') {
    await screen.findByText('Facility created.');
    expect(view.writes).toEqual([{ method: 'POST', principal: 'staff-a' }]);
    expect(screen.getByRole('table')).toBe(table);
    expect(view.shown.filter(text => /Checking|Loading/i.test(text))).toEqual([]);
  } else {
    await waitFor(() => expect(screen.queryByRole('link', { name: 'Example facility' })).not.toBeInTheDocument());
    expect(screen.queryByText('Facility created.')).not.toBeInTheDocument();
    expect(view.writes).toEqual([]);
  }
  view.dispose();
});

it('a persisted pageshow without a pagehide in this realm still checks', async () => {
  const view = host();
  await findEnabledButton({ name: 'Create facility' });
  view.signIn('staff-b');
  fireEvent(window, new PageTransitionEvent('pageshow', { persisted: true }));
  await waitFor(() => expect(view.loader).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(screen.queryByRole('link', { name: 'Example facility' })).not.toBeInTheDocument());
  view.dispose();
});

it('a failed check fails closed: the held save is not sent', async () => {
  const view = host();
  await findEnabledButton({ name: 'Create facility' });
  await startHeldCreate(view);
  const state = visibility();
  hide(state);
  view.loader.mockImplementationOnce(async () => { throw new Response('Unavailable', { status: 503 }); });
  show(state);
  await waitFor(() => expect(view.loader).toHaveBeenCalledTimes(2));
  await view.releaseReview();
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  expect(view.writes).toEqual([]);
  view.dispose();
});
