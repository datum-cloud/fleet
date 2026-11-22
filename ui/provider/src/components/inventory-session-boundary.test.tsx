import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, MemoryRouter, Outlet, RouterProvider, UNSAFE_DataRouterContext } from 'react-router';
import { useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query';
import { useContext } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { InventorySessionBoundary, SessionQueries } from './inventory-session-boundary';
import { InventoryOperationContext, InventoryOperationInvalidated, InventoryOperationScope } from '../lib/inventory-operation';

function mountHost(loader: () => unknown, content: React.ReactNode) {
  const router = createMemoryRouter([{
    id: 'layouts/milo.layout',
    path: '/',
    loader,
    element: <Outlet />,
    children: [{ index: true, element: <InventorySessionBoundary>{content}</InventorySessionBoundary> }],
  }]);
  render(<RouterProvider router={router} />);
  return router;
}

describe('host inventory session isolation', () => {
  afterEach(() => vi.restoreAllMocks());
  it('keeps visible inventory through browser-chrome blur/focus while fencing old and pending authority', async () => {
    let scope: InventoryOperationScope | undefined;
    let release: (() => void) | undefined;
    let reads = 0;
    const inventoryRead = vi.fn(async () => 'Visible inventory');
    function Records() {
      scope = useContext(InventoryOperationContext);
      const query = useQuery({ queryKey: ['inventory'], queryFn: inventoryRead, staleTime: Infinity });
      return <p>{query.data ?? 'Empty inventory cache'}</p>;
    }
    mountHost(async () => {
      if (++reads > 1) await new Promise<void>(resolve => { release = resolve; });
      return { user: { metadata: { uid: 'staff-a' } } };
    }, <Records />);
    await screen.findByText('Visible inventory');
    const before = scope!.begin();
    const dispatched = scope!.begin();
    dispatched.dispatch();
    fireEvent(window, new Event('blur'));
    expect(screen.getByText('Visible inventory')).toBeInTheDocument();
    expect(() => before.assertCurrent()).toThrow(InventoryOperationInvalidated);
    expect(() => dispatched.assertCurrent()).toThrow(/outcome is unknown/);
    fireEvent(window, new Event('focus'));
    await waitFor(() => expect(release).toBeDefined());
    expect(screen.getByText('Visible inventory')).toBeInTheDocument();
    const during = scope!.begin();
    expect(() => during.assertCurrent()).toThrow(InventoryOperationInvalidated);
    fireEvent(window, new Event('blur'));
    await act(async () => release!());
    expect(screen.getByText('Visible inventory')).toBeInTheDocument();
    expect(() => scope!.begin().assertCurrent()).toThrow(InventoryOperationInvalidated);
    expect(inventoryRead).toHaveBeenCalledTimes(1);
    release = undefined;
    fireEvent(window, new Event('focus'));
    await waitFor(() => expect(release).toBeDefined());
    await act(async () => release!());
    expect(screen.getByText('Visible inventory')).toBeInTheDocument();
    expect(inventoryRead).toHaveBeenCalledTimes(2);
    expect(() => scope!.begin().assertCurrent()).not.toThrow();
    expect(() => before.assertCurrent()).toThrow(InventoryOperationInvalidated);
    expect(() => during.assertCurrent()).toThrow(InventoryOperationInvalidated);
    expect(() => dispatched.assertCurrent()).toThrow(/outcome is unknown/);
  });

  it('replaces caches on visible focus principal changes including batched A to B to A', async () => {
    let client: QueryClient | undefined;
    let principal = 'staff-a';
    let revision = 1;
    const read = vi.fn(async () => `${principal} inventory ${revision}`);
    function Records() {
      client = useQueryClient();
      const query = useQuery({ queryKey: ['inventory'], queryFn: read, staleTime: Infinity });
      return <p>{query.data}</p>;
    }
    const router = mountHost(() => ({ user: { metadata: { uid: principal } } }), <Records />);
    await screen.findByText('staff-a inventory 1');
    const originalClient = client;
    await act(async () => {
      principal = 'staff-b';
      fireEvent(window, new Event('focus'));
      await router.revalidate();
      principal = 'staff-a';
      revision++;
      await router.revalidate();
    });
    await screen.findByText('staff-a inventory 2');
    expect(client).not.toBe(originalClient);
    expect(screen.queryByText('staff-a inventory 1')).not.toBeInTheDocument();
  });

  it('does not start operation authority for a principal different from the current host', async () => {
    const start = vi.spyOn(InventoryOperationScope.prototype, 'startLifecycle');
    const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'staff-b' } } }), element: null }]);
    await waitFor(() => expect(router.state.initialized).toBe(true));
    let scope: InventoryOperationScope | undefined;
    function Probe() {
      scope = useContext(InventoryOperationContext);
      return null;
    }
    render(<UNSAFE_DataRouterContext.Provider value={{ router, navigator: router, static: false, basename: '/' } as never}>
      <SessionQueries principal="staff-a" authority={true} generation={0}><Probe /></SessionQueries>
    </UNSAFE_DataRouterContext.Provider>);
    expect(() => scope!.begin().assertCurrent()).toThrow(InventoryOperationInvalidated);
    expect(start).not.toHaveBeenCalled();
    router.dispose();
  });

  it('replaces the cache when idle principal snapshots change A to B to A within one render batch', async () => {
    const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'staff-a' } } }), element: <Outlet />, children: [{ index: true, element: <InventorySessionBoundary><Records /></InventorySessionBoundary> }] }]);
    const subscribers: Parameters<typeof router.subscribe>[0][] = [];
    const subscribe = router.subscribe.bind(router);
    vi.spyOn(router, 'subscribe').mockImplementation(callback => {
      subscribers.push(callback);
      return subscribe(callback);
    });
    let client: QueryClient | undefined;
    function Records() {
      client = useQueryClient();
      return <p>Inventory cache mounted</p>;
    }
    render(<RouterProvider router={router} />);
    await screen.findByText('Inventory cache mounted');
    const original = client;
    act(() => {
      for (const principal of ['staff-b', 'staff-a']) {
        const state = { ...router.state, loaderData: { 'layouts/milo.layout': { user: { metadata: { uid: principal } } } } };
        for (const subscriber of subscribers.slice()) subscriber(state, { deletedFetchers: [], newErrors: null, flushSync: false });
      }
    });
    expect(client).not.toBe(original);
    router.dispose();
  });

  it('keeps failed focus verification masked until the next verification succeeds', async () => {
    const router = mountHost(() => ({ user: { metadata: { uid: 'staff-a' } } }), 'Private inventory');
    await screen.findByText('Private inventory');
    let release!: () => void;
    vi.spyOn(router, 'revalidate').mockRejectedValueOnce(new Error('verification failed')).mockImplementationOnce(() => new Promise<void>(resolve => { release = resolve; }));
    fireEvent(window, new Event('focus'));
    await waitFor(() => expect(screen.queryByText('Private inventory')).not.toBeInTheDocument());
    fireEvent(window, new Event('focus'));
    await waitFor(() => expect(release).toBeDefined());
    expect(screen.queryByText('Private inventory')).not.toBeInTheDocument();
    await act(async () => release());
    await screen.findByText('Private inventory');
    router.dispose();
  });

  it.each(['logout', 'failure'])('clears visible data when focus verification observes %s', async outcome => {
    let expired = false;
    mountHost(() => {
      if (expired && outcome === 'failure') throw new Response('Synthetic failure', { status: 503 });
      if (expired) return { user: null };
      return { user: { metadata: { uid: 'staff-a' } } };
    }, 'Visible inventory');
    await screen.findByText('Visible inventory');
    expired = true;
    fireEvent(window, new Event('blur'));
    fireEvent(window, new Event('focus'));
    await waitFor(() => expect(screen.queryByText('Visible inventory')).not.toBeInTheDocument());
  });

  it('discards genuine hidden data and waits for visible verification before a fresh read', async () => {
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
    let release: (() => void) | undefined;
    let reads = 0;
    const inventoryRead = vi.fn(async () => 'Private inventory');
    function Records() {
      const query = useQuery({ queryKey: ['inventory'], queryFn: inventoryRead, staleTime: Infinity });
      return <p>{query.data}</p>;
    }
    mountHost(async () => {
      if (++reads > 1) await new Promise<void>(resolve => { release = resolve; });
      return { user: { metadata: { uid: 'staff-a' } } };
    }, <Records />);
    await screen.findByText('Private inventory');
    visibility.mockReturnValue('hidden');
    fireEvent(document, new Event('visibilitychange'));
    expect(screen.queryByText('Private inventory')).not.toBeInTheDocument();
    visibility.mockReturnValue('visible');
    fireEvent(document, new Event('visibilitychange'));
    await waitFor(() => expect(release).toBeDefined());
    expect(screen.queryByText('Private inventory')).not.toBeInTheDocument();
    await act(async () => release!());
    await screen.findByText('Private inventory');
    expect(inventoryRead).toHaveBeenCalledTimes(2);
  });
  it('fails closed outside the authenticated host unless explicitly previewing', () => {
    render(<MemoryRouter><InventorySessionBoundary>Restricted inventory</InventorySessionBoundary></MemoryRouter>);
    expect(screen.queryByText('Restricted inventory')).not.toBeInTheDocument();
    expect(screen.getByText('Your session has expired')).toBeInTheDocument();
  });

  it('does not read inventory without an authenticated loader identity', async () => {
    mountHost(() => ({ user: null }), 'Restricted inventory');
    await screen.findByText('Your session has expired');
    expect(screen.queryByText('Restricted inventory')).not.toBeInTheDocument();
  });

  it('isolates identical query keys across principal changes and pending revalidation', async () => {
    let principal = 'staff-a';
    let release: (() => void) | undefined;
    const read = vi.fn(async () => `Inventory for ${principal}`);
    function Records() {
      const query = useQuery({ queryKey: ['inventory'], queryFn: read });
      return <p>{query.data}</p>;
    }
    const router = mountHost(async () => {
      if (principal === 'staff-b') await new Promise<void>(resolve => { release = resolve; });
      return { user: { metadata: { uid: principal } } };
    }, <Records />);
    await screen.findByText('Inventory for staff-a');
    principal = 'staff-b';
    let completion: Promise<void>;
    act(() => { completion = router.revalidate(); });
    await waitFor(() => expect(screen.queryByText('Inventory for staff-a')).not.toBeInTheDocument());
    await act(async () => { release?.(); await completion; });
    await screen.findByText('Inventory for staff-b');
    expect(read).toHaveBeenCalledTimes(2);
    expect(screen.queryByText('Inventory for staff-a')).not.toBeInTheDocument();
  });

  it('hides background data and revalidates the actual host loader before restoring it', async () => {
    const loader = vi.fn(() => ({ user: { metadata: { uid: 'staff-a' } } }));
    mountHost(loader, 'Restricted inventory');
    await screen.findByText('Restricted inventory');
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
    fireEvent(document, new Event('visibilitychange'));
    expect(screen.queryByText('Restricted inventory')).not.toBeInTheDocument();
    loader.mockReturnValue({ user: null } as never);
    visibility.mockReturnValue('visible');
    fireEvent(window, new Event('focus'));
    await screen.findByText('Your session has expired');
    expect(loader).toHaveBeenCalledTimes(2);
    expect(screen.queryByText('Restricted inventory')).not.toBeInTheDocument();
  });

  it('only restores the newest foreground generation after successive pagehide and focus events', async () => {
    const releases: (() => void)[] = [];
    let reads = 0;
    mountHost(async () => {
      if (++reads > 1) await new Promise<void>(resolve => releases.push(resolve));
      return { user: { metadata: { uid: 'staff-a' } } };
    }, 'Private inventory');
    await screen.findByText('Private inventory');
    fireEvent(window, new Event('pagehide'));
    fireEvent(window, new Event('focus'));
    await waitFor(() => expect(releases).toHaveLength(1));
    fireEvent(window, new Event('pagehide'));
    fireEvent(window, new Event('focus'));
    await waitFor(() => expect(releases).toHaveLength(2));
    await act(async () => releases[0]());
    expect(screen.queryByText('Private inventory')).not.toBeInTheDocument();
    await act(async () => releases[1]());
    await screen.findByText('Private inventory');
  });
});
