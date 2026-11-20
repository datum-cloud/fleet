import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, MemoryRouter, Outlet, RouterProvider } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { describe, expect, it, vi } from 'vitest';
import { InventorySessionBoundary } from './inventory-session-boundary';

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
    fireEvent(window, new Event('blur'));
    expect(screen.queryByText('Restricted inventory')).not.toBeInTheDocument();
    loader.mockReturnValue({ user: null } as never);
    fireEvent(window, new Event('focus'));
    await screen.findByText('Your session has expired');
    expect(loader).toHaveBeenCalledTimes(2);
    expect(screen.queryByText('Restricted inventory')).not.toBeInTheDocument();
  });

  it('only restores the newest focused generation after successive blur and focus events', async () => {
    const releases: (() => void)[] = [];
    let reads = 0;
    mountHost(async () => {
      if (++reads > 1) await new Promise<void>(resolve => releases.push(resolve));
      return { user: { metadata: { uid: 'staff-a' } } };
    }, 'Private inventory');
    await screen.findByText('Private inventory');
    fireEvent(window, new Event('blur'));
    fireEvent(window, new Event('focus'));
    await waitFor(() => expect(releases).toHaveLength(1));
    fireEvent(window, new Event('blur'));
    fireEvent(window, new Event('focus'));
    await waitFor(() => expect(releases).toHaveLength(2));
    await act(async () => releases[0]());
    expect(screen.queryByText('Private inventory')).not.toBeInTheDocument();
    await act(async () => releases[1]());
    await screen.findByText('Private inventory');
  });
});
