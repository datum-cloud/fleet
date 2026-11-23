import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { UNSAFE_DataRouterContext, UNSAFE_DataRouterStateContext, useLocation, useNavigation, useRevalidator, useRouteLoaderData, type Location } from 'react-router';
import { InventoryAccessState } from './inventory-access-state';
import { LoadingState } from './states';
import { InventoryOperationContext, InventoryOperationGenerationContext, InventoryOperationReadyContext, InventoryOperationScope } from '../lib/inventory-operation';
import { InventoryReadContext, InventoryReadScope } from '../lib/inventory-read';

export const InventoryPreviewContext = createContext(false);

export function SessionQueries({ children, principal, authority, generation }: { children: ReactNode; principal: string; authority: boolean; generation: number }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, refetchOnWindowFocus: false } } }));
  const [scope] = useState(() => new InventoryOperationScope());
  const [reads] = useState(() => new InventoryReadScope());
  const ready = authority;
  const host = useContext(UNSAFE_DataRouterContext);
  useLayoutEffect(() => {
    const identity = host?.router.state.loaderData['layouts/milo.layout'] as { user?: { metadata?: { uid?: string; name?: string } } } | undefined;
    if (ready && host?.router.state.revalidation === 'idle' && host.router.state.navigation.state === 'idle' && (identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name) === principal) scope.startLifecycle();
    else scope.invalidate();
    const invalidate = () => {
      scope.invalidate();
      reads.suspend();
      void client.cancelQueries();
    };
    if (authority && host?.router.state.revalidation === 'idle' && host.router.state.navigation.state === 'idle' && (identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name) === principal) reads.resume();
    else invalidate();
    const visibility = () => { if (document.visibilityState === 'hidden') invalidate(); };
    window.addEventListener('pagehide', invalidate);
    document.addEventListener('visibilitychange', visibility);
    const unsubscribe = host?.router.subscribe(state => {
      const identity = state.loaderData['layouts/milo.layout'] as { user?: { metadata?: { uid?: string; name?: string } } } | undefined;
      if (state.revalidation !== 'idle' || state.navigation.state !== 'idle' || (identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name) !== principal) invalidate();
    });
    return () => {
      invalidate(); unsubscribe?.();
      window.removeEventListener('pagehide', invalidate);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, [host, principal, scope, ready, generation, authority, reads, client]);
  useEffect(() => () => client.clear(), [client]);
  return <InventoryReadContext.Provider value={reads}><InventoryOperationContext.Provider value={scope}><InventoryOperationGenerationContext.Provider value={generation}><InventoryOperationReadyContext.Provider value={ready}><QueryClientProvider client={client}>{children}</QueryClientProvider></InventoryOperationReadyContext.Provider></InventoryOperationGenerationContext.Provider></InventoryOperationContext.Provider></InventoryReadContext.Provider>;
}

function isPresentationNavigation(current: Location, next: Location | undefined) {
  if (!next || current.pathname !== next.pathname || current.search === next.search) return false;
  const currentParams = new URLSearchParams(current.search);
  const nextParams = new URLSearchParams(next.search);
  for (const key of ['kind', 'record', 'namespace', 'q', 'location', 'model', 'tab', 'selectedKind', 'selected']) {
    currentParams.delete(key);
    nextParams.delete(key);
  }
  return currentParams.toString() === nextParams.toString();
}

function HostedInventory({ children, retainPresentationData = false, pending: pendingContent = <LoadingState />, unavailable = status => <InventoryAccessState status={status} /> }: { children: ReactNode; retainPresentationData?: boolean; pending?: ReactNode; unavailable?: (status: number) => ReactNode }) {
  const host = useContext(UNSAFE_DataRouterContext);
  const [sessionGeneration, setSessionGeneration] = useState(0);
  const [operationGeneration, setOperationGeneration] = useState(0);
  useLayoutEffect(() => {
    if (!host) return;
    const readPrincipal = (state: typeof host.router.state) => {
      const identity = state.loaderData['layouts/milo.layout'] as { user?: { metadata?: { uid?: string; name?: string } } } | undefined;
      return identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name;
    };
    let observedPrincipal = readPrincipal(host.router.state);
    let pending = host.router.state.revalidation !== 'idle' || host.router.state.navigation.state !== 'idle';
    let resetAfterPending = pending;
    return host.router.subscribe(state => {
      const nextPrincipal = readPrincipal(state);
      if (nextPrincipal !== observedPrincipal) {
        observedPrincipal = nextPrincipal;
        setSessionGeneration(current => current + 1);
      }
      if (state.revalidation !== 'idle' || state.navigation.state !== 'idle') {
        pending = true;
        if (!retainPresentationData || state.revalidation !== 'idle' || state.navigation.formMethod || !isPresentationNavigation(state.location, state.navigation.location)) resetAfterPending = true;
      } else if (pending) {
        pending = false;
        setOperationGeneration(current => current + 1);
        if (resetAfterPending) setSessionGeneration(current => current + 1);
        resetAfterPending = false;
      }
    });
  }, [host, retainPresentationData]);
  const identity = useRouteLoaderData('layouts/milo.layout') as { user?: { metadata?: { uid?: string; name?: string } } } | undefined;
  const principal = identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name;
  const location = useLocation();
  const navigation = useNavigation();
  const revalidator = useRevalidator();
  const revalidate = useRef(revalidator.revalidate);
  revalidate.current = revalidator.revalidate;
  const [checking, setChecking] = useState(() => typeof document !== 'undefined' && document.visibilityState === 'hidden');
  const [failed, setFailed] = useState(false);
  const generation = useRef(0);
  useEffect(() => {
    let active = true;
    let awaitingReturn = document.visibilityState === 'hidden';
    const hide = () => {
      generation.current++;
      awaitingReturn = true;
      setChecking(true);
    };
    const verify = () => {
      if (!awaitingReturn || document.visibilityState === 'hidden') return;
      awaitingReturn = false;
      const attempt = ++generation.current;
      setFailed(false);
      let succeeded = true;
      void revalidate.current().catch(() => {
        succeeded = false;
        if (active && attempt === generation.current) {
          setChecking(true);
          setFailed(true);
        }
      }).finally(() => {
        if (active && attempt === generation.current && document.visibilityState !== 'hidden') {
          setChecking(!succeeded);
        }
      });
    };
    const visibility = () => document.visibilityState === 'hidden' ? hide() : verify();
    window.addEventListener('pagehide', hide);
    window.addEventListener('pageshow', verify);
    document.addEventListener('visibilitychange', visibility);
    return () => {
      active = false;
      generation.current++;
      window.removeEventListener('pagehide', hide);
      window.removeEventListener('pageshow', verify);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, []);
  if (failed) return unavailable(503);
  const presentationPending = retainPresentationData && !navigation.formMethod && isPresentationNavigation(location, navigation.location);
  if (checking || revalidator.state !== 'idle' || navigation.state !== 'idle' && !presentationPending) return <>{pendingContent}</>;
  if (!principal) return unavailable(401);
  return <SessionQueries key={JSON.stringify([principal, retainPresentationData ? null : location.key, sessionGeneration])} principal={principal} authority={navigation.state === 'idle'} generation={operationGeneration}>{navigation.state !== 'idle' ? pendingContent : children}</SessionQueries>;
}

export function InventorySessionBoundary({ children, retainPresentationData, pending, unavailable }: { children: ReactNode; retainPresentationData?: boolean; pending?: ReactNode; unavailable?: (status: number) => ReactNode }) {
  const router = useContext(UNSAFE_DataRouterStateContext);
  const preview = useContext(InventoryPreviewContext);
  if (preview) return <>{children}</>;
  if (!router) return unavailable ? unavailable(401) : <InventoryAccessState status={401} />;
  return <HostedInventory retainPresentationData={retainPresentationData} pending={pending} unavailable={unavailable}>{children}</HostedInventory>;
}
