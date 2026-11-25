import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { UNSAFE_DataRouterContext, UNSAFE_DataRouterStateContext, useLocation, useNavigation, useRouteLoaderData, type Location } from 'react-router';
import { InventoryAccessState } from './inventory-access-state';
import { LoadingState } from './states';
import { InventoryOperationContext, InventoryOperationGenerationContext, InventoryOperationReadyContext, InventoryOperationScope } from '../lib/inventory-operation';
import { InventoryReadContext, InventoryReadScope } from '../lib/inventory-read';

export const InventoryPreviewContext = createContext(false);

type MiloLayout = { user?: { metadata?: { uid?: string; name?: string } } } | undefined;
const principalOf = (loaderData: Record<string, unknown>) => {
  const identity = loaderData['layouts/milo.layout'] as MiloLayout;
  return identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name;
};

/**
 * A private query cache and operation/read scopes for one signed-in user.
 *
 * The session boundary is a change of signed-in user (or sign-out), as the
 * host observes it, and nothing else. Like the rest of staff-portal, hiding the
 * tab, focus changes and host loader revalidation (for example a theme change)
 * leave loaded inventory and open forms alone; the API authorizes every request
 * and edits carry UID/resourceVersion preconditions. Operations are fenced while
 * a navigation is pending; operations and new reads pause while the host
 * re-runs its loaders; neither changes what is on screen.
 */
export function SessionQueries({ children, principal, authority, generation }: { children: ReactNode; principal: string; authority: boolean; generation: number }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, refetchOnWindowFocus: false } } }));
  const [scope] = useState(() => new InventoryOperationScope());
  const [reads] = useState(() => new InventoryReadScope());
  const ready = authority;
  const host = useContext(UNSAFE_DataRouterContext);
  useLayoutEffect(() => {
    const invalidate = () => {
      scope.invalidate();
      reads.suspend();
      void client.cancelQueries();
    };
    const current = !host || principalOf(host.router.state.loaderData) === principal;
    if (current) reads.resume(); else invalidate();
    if (ready && current) scope.startLifecycle(); else scope.invalidate();
    // While the host re-runs its loaders the signed-in user may be changing, so
    // saves and new reads are paused; what is on screen stays. Afterwards, if
    // the user is the same, reads resume and new operations may start.
    let revalidating = false;
    const unsubscribe = host?.router.subscribe(state => {
      if (principalOf(state.loaderData) !== principal) { invalidate(); return; }
      if (state.revalidation !== 'idle') { revalidating = true; scope.invalidate(); reads.suspend(); }
      else if (revalidating) { revalidating = false; reads.resume(); if (ready) scope.startLifecycle(); }
    });
    return () => { invalidate(); unsubscribe?.(); };
  }, [host, principal, scope, ready, generation, reads, client]);
  useEffect(() => () => client.clear(), [client]);
  return <InventoryReadContext.Provider value={reads}><InventoryOperationContext.Provider value={scope}><InventoryOperationGenerationContext.Provider value={generation}><InventoryOperationReadyContext.Provider value={ready}><QueryClientProvider client={client}>{children}</QueryClientProvider></InventoryOperationReadyContext.Provider></InventoryOperationGenerationContext.Provider></InventoryOperationContext.Provider></InventoryReadContext.Provider>;
}

/** URL parameters that only change what is shown, never whose data or which scope. */
const PRESENTATION_PARAMS = ['kind', 'record', 'namespace', 'q', 'location', 'model', 'tab', 'view', 'selectedKind', 'selected'];

/** The page and any non-presentation parameters: a change of these starts a fresh cache. */
function dataScope(location: Pick<Location, 'pathname' | 'search'>) {
  const params = new URLSearchParams(location.search);
  for (const key of PRESENTATION_PARAMS) params.delete(key);
  return `${location.pathname}?${params.toString()}`;
}

function HostedInventory({ children, retainPresentationData = false, pending: pendingContent = <LoadingState />, unavailable = status => <InventoryAccessState status={status} /> }: { children: ReactNode; retainPresentationData?: boolean; pending?: ReactNode; unavailable?: (status: number) => ReactNode }) {
  const host = useContext(UNSAFE_DataRouterContext);
  const identity = useRouteLoaderData('layouts/milo.layout') as MiloLayout;
  const principal = identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name;
  // The private cache is keyed by the signed-in user, so a change of user
  // remounts it. A fast A→B→A change can reach render as "still A"; the
  // subscription catches that case, and only that case, so an ordinary change
  // of user remounts once.
  const rendered = useRef(principal);
  rendered.current = principal;
  const [sessionGeneration, setSessionGeneration] = useState(0);
  useLayoutEffect(() => {
    if (!host) return;
    let observed = principalOf(host.router.state.loaderData);
    return host.router.subscribe(state => {
      const next = principalOf(state.loaderData);
      if (next === observed) return;
      observed = next;
      if (next === rendered.current) setSessionGeneration(current => current + 1);
    });
  }, [host]);
  const location = useLocation();
  const navigation = useNavigation();
  // Like host pages, the current view stays on screen while the host loads the
  // next one. Without a signed-in user nothing private is rendered.
  if (!principal) return navigation.state !== 'idle' ? <>{pendingContent}</> : unavailable(401);
  return <SessionQueries key={JSON.stringify([principal, retainPresentationData ? dataScope(location) : location.key, sessionGeneration])} principal={principal} authority={navigation.state === 'idle'} generation={sessionGeneration}>{children}</SessionQueries>;
}

export function InventorySessionBoundary({ children, retainPresentationData, pending, unavailable }: { children: ReactNode; retainPresentationData?: boolean; pending?: ReactNode; unavailable?: (status: number) => ReactNode }) {
  const router = useContext(UNSAFE_DataRouterStateContext);
  const preview = useContext(InventoryPreviewContext);
  if (preview) return <>{children}</>;
  if (!router) return unavailable ? unavailable(401) : <InventoryAccessState status={401} />;
  return <HostedInventory retainPresentationData={retainPresentationData} pending={pending} unavailable={unavailable}>{children}</HostedInventory>;
}
