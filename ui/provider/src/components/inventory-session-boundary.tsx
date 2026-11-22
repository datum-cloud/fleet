import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { UNSAFE_DataRouterContext, UNSAFE_DataRouterStateContext, useLocation, useNavigation, useRevalidator, useRouteLoaderData } from 'react-router';
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

function HostedInventory({ children }: { children: ReactNode }) {
  const host = useContext(UNSAFE_DataRouterContext);
  const [sessionGeneration, setSessionGeneration] = useState(0);
  useLayoutEffect(() => {
    if (!host) return;
    const readPrincipal = (state: typeof host.router.state) => {
      const identity = state.loaderData['layouts/milo.layout'] as { user?: { metadata?: { uid?: string; name?: string } } } | undefined;
      return identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name;
    };
    let observedPrincipal = readPrincipal(host.router.state);
    let pending = host.router.state.revalidation !== 'idle' || host.router.state.navigation.state !== 'idle';
    return host.router.subscribe(state => {
      const nextPrincipal = readPrincipal(state);
      if (nextPrincipal !== observedPrincipal) {
        observedPrincipal = nextPrincipal;
        setSessionGeneration(current => current + 1);
      }
      if (state.revalidation !== 'idle' || state.navigation.state !== 'idle') {
        pending = true;
      } else if (pending) {
        pending = false;
        setSessionGeneration(current => current + 1);
      }
    });
  }, [host]);
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
  if (failed) return <InventoryAccessState status={503} />;
  if (checking || revalidator.state !== 'idle' || navigation.state !== 'idle') return <LoadingState />;
  if (!principal) return <InventoryAccessState status={401} />;
  return <SessionQueries key={JSON.stringify([principal, location.key, sessionGeneration])} principal={principal} authority={true} generation={sessionGeneration}>{children}</SessionQueries>;
}

export function InventorySessionBoundary({ children }: { children: ReactNode }) {
  const router = useContext(UNSAFE_DataRouterStateContext);
  const preview = useContext(InventoryPreviewContext);
  if (preview) return <>{children}</>;
  if (!router) return <InventoryAccessState status={401} />;
  return <HostedInventory>{children}</HostedInventory>;
}
