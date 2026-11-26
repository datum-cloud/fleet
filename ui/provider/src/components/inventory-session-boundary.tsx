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
 * host observes it. Loaded inventory and open forms are never discarded for
 * anything else, and nothing is shown while the user is being confirmed:
 *
 * - Operations are fenced while a navigation is pending.
 * - While the host re-runs its loaders, new reads and held operations wait
 *   (they are not cancelled); if the user is the same they go on.
 * - Staff-portal does not re-check the session when a tab returns, so this
 *   does: hiding the tab or leaving the page (pagehide) holds reads and
 *   operations, and showing it again (or a back-forward-cache pageshow) re-runs
 *   the host loaders. The same user releases the hold. A different user, no
 *   user, or a failed check invalidates the scopes: held writes are never
 *   sent, and the host's new principal remounts this cache.
 *
 * The API still authorizes every request and edits carry UID/resourceVersion
 * preconditions.
 */
const REVALIDATION = 'revalidation';
const RETURN = 'return';
type HostRouter = NonNullable<React.ContextType<typeof UNSAFE_DataRouterContext>>['router'];

/**
 * Confirms the signed-in user when a hidden tab, or a page restored from the
 * back-forward cache, comes back. Invisible: it holds reads and operations
 * (nothing is cancelled or re-rendered) and asks the host to re-run its
 * loaders. Only a completed check that names the same user releases the hold.
 */
function useReturnCheck(router: HostRouter | undefined, principal: string, scope: InventoryOperationScope, reads: InventoryReadScope, client: QueryClient, authority: boolean) {
  const ready = useRef(authority);
  ready.current = authority;
  useLayoutEffect(() => {
    if (!router || typeof document === 'undefined') return;
    let disposed = false;
    // Away: hidden or left since the last confirmed check. Checking: a return
    // has asked the host to re-run its loaders and waits for them to settle.
    let away = false;
    let checking = false;
    const hold = () => { scope.hold(RETURN); reads.hold(RETURN); };
    const release = () => { scope.release(RETURN); reads.release(RETURN); };
    const leave = () => { away = true; checking = false; hold(); };
    // After a failed check the scopes stay closed until a later check succeeds.
    let failed = false;
    const settle = (rejected = false) => {
      if (disposed || !checking) return;
      const state = router.state;
      if (!rejected && (state.revalidation !== 'idle' || state.navigation.state !== 'idle')) return;
      checking = false;
      if (rejected || principalOf(state.loaderData) !== principal || state.errors) {
        // Another user, no user, or no answer: fail closed. Held writes are
        // never sent; the host's new principal (if any) remounts the cache.
        failed = true;
        scope.invalidate();
        reads.suspend();
        void client.cancelQueries();
      } else if (failed) {
        // Operations held through the failure stay retired; new work may start.
        failed = false;
        reads.resume();
        if (ready.current) scope.startLifecycle();
      }
      release();
    };
    const back = () => {
      if (!away || document.visibilityState === 'hidden') return;
      away = false;
      checking = true;
      // A check that was already running may have been answered before the
      // tab came back, so always start a new one.
      const run = router.state.navigation.state === 'submitting' ? Promise.resolve() : router.revalidate();
      void run.then(() => settle(), () => settle(true));
    };
    const visibility = () => { if (document.visibilityState === 'hidden') leave(); else back(); };
    // A page restored from the back-forward cache may not have seen its own
    // pagehide in this realm's state, so a persisted pageshow always checks.
    const pageshow = (event: PageTransitionEvent) => { if (event.persisted) leave(); back(); };
    if (document.visibilityState === 'hidden') leave();
    const unsubscribe = router.subscribe(() => settle());
    document.addEventListener('visibilitychange', visibility);
    window.addEventListener('pagehide', leave);
    window.addEventListener('pageshow', pageshow);
    return () => {
      disposed = true;
      unsubscribe();
      document.removeEventListener('visibilitychange', visibility);
      window.removeEventListener('pagehide', leave);
      window.removeEventListener('pageshow', pageshow);
      release();
    };
  }, [router, principal, scope, reads, client]);
}

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
    // new reads and saves wait; what is on screen stays. Afterwards, if the
    // user is the same, they go on.
    const unsubscribe = host?.router.subscribe(state => {
      if (principalOf(state.loaderData) !== principal) { invalidate(); return; }
      if (state.revalidation !== 'idle') { scope.hold(REVALIDATION); reads.hold(REVALIDATION); }
      else { scope.release(REVALIDATION); reads.release(REVALIDATION); }
    });
    return () => { invalidate(); unsubscribe?.(); };
  }, [host, principal, scope, ready, generation, reads, client]);
  useReturnCheck(host?.router, principal, scope, reads, client, ready);
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
