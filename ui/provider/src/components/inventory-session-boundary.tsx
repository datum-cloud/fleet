import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { UNSAFE_DataRouterContext, UNSAFE_DataRouterStateContext, useLocation, useNavigation, useRevalidator, useRouteLoaderData } from 'react-router';
import { InventoryAccessState } from './inventory-access-state';
import { LoadingState } from './states';
import { InventoryOperationContext, InventoryOperationGenerationContext, InventoryOperationReadyContext, InventoryOperationScope } from '../lib/inventory-operation';
import { inventoryResources, type Inventory, type InventoryKind } from '../inventory';
import { InventoryReadContext, InventoryReadScope } from '../lib/inventory-read';

export const InventoryPreviewContext = createContext(false);

export function SessionQueries({ children, principal, authority, generation }: { children: ReactNode; principal: string; authority: boolean; generation: number }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } }));
  const [scope] = useState(() => new InventoryOperationScope());
  const [reads] = useState(() => new InventoryReadScope());
  const readGeneration = useRef(generation);
  const [readyGeneration, setReadyGeneration] = useState(generation);
  const ready = authority && readyGeneration === generation;
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
    window.addEventListener('blur', invalidate);
    window.addEventListener('pagehide', invalidate);
    document.addEventListener('visibilitychange', visibility);
    const unsubscribe = host?.router.subscribe(state => {
      const identity = state.loaderData['layouts/milo.layout'] as { user?: { metadata?: { uid?: string; name?: string } } } | undefined;
      if (state.revalidation !== 'idle' || state.navigation.state !== 'idle' || (identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name) !== principal) invalidate();
    });
    return () => {
      invalidate(); unsubscribe?.();
      window.removeEventListener('blur', invalidate);
      window.removeEventListener('pagehide', invalidate);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, [host, principal, scope, ready, generation, authority, reads, client]);
  useEffect(() => {
    if (readGeneration.current === generation) return;
    readGeneration.current = generation;
    let active = true;
    void client.cancelQueries().then(async () => {
      if (!active) return;
      await client.invalidateQueries({ predicate: query => query.queryKey[1] !== 'inventory-permission' });
      const readable = client.getQueryCache().getAll().every(query => {
        if (query.queryKey[1] !== 'inventory') return true;
        const inventory = query.state.data as Inventory | undefined;
        return query.state.status === 'success' && query.state.fetchStatus === 'idle' && !Object.keys(inventoryResources).every(kind => inventory?.issues?.[kind as InventoryKind]);
      });
      if (active && readable) setReadyGeneration(generation);
    });
    return () => { active = false; };
  }, [client, generation]);
  useEffect(() => () => client.clear(), [client]);
  return <InventoryReadContext.Provider value={reads}><InventoryOperationContext.Provider value={scope}><InventoryOperationGenerationContext.Provider value={generation}><InventoryOperationReadyContext.Provider value={ready}><QueryClientProvider client={client}>{children}</QueryClientProvider></InventoryOperationReadyContext.Provider></InventoryOperationGenerationContext.Provider></InventoryOperationContext.Provider></InventoryReadContext.Provider>;
}

function HostedInventory({ children }: { children: ReactNode }) {
  const host = useContext(UNSAFE_DataRouterContext);
  const [hostGeneration, setHostGeneration] = useState(0);
  const [sessionGeneration, setSessionGeneration] = useState(0);
  const softRequested = useRef(false);
  useLayoutEffect(() => {
    if (!host) return;
    const readPrincipal = (state: typeof host.router.state) => {
      const identity = state.loaderData['layouts/milo.layout'] as { user?: { metadata?: { uid?: string; name?: string } } } | undefined;
      return identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name;
    };
    let observedPrincipal = readPrincipal(host.router.state);
    let pending = host.router.state.revalidation !== 'idle' || host.router.state.navigation.state !== 'idle';
    let preserveCache = false;
    return host.router.subscribe(state => {
      const nextPrincipal = readPrincipal(state);
      if (nextPrincipal !== observedPrincipal) {
        observedPrincipal = nextPrincipal;
        setSessionGeneration(current => current + 1);
      }
      if (state.revalidation !== 'idle' || state.navigation.state !== 'idle') {
        if (!pending) {
          preserveCache = softRequested.current;
          setSoftVerification(preserveCache);
        }
        pending = true;
      } else if (pending) {
        pending = false;
        if (!preserveCache) setSessionGeneration(current => current + 1);
        setHostGeneration(current => current + 1);
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
  const [verifying, setVerifying] = useState(false);
  const [focused, setFocused] = useState(true);
  const [softVerification, setSoftVerification] = useState(false);
  const masked = useRef(checking);
  const generation = useRef(0);
  const foreground = useRef(true);
  useEffect(() => {
    let active = true;
    const hide = () => {
      generation.current++;
      foreground.current = false;
      masked.current = true;
      softRequested.current = false;
      setFocused(false);
      setSoftVerification(false);
      setChecking(true);
    };
    const blur = () => {
      generation.current++;
      foreground.current = false;
      setFocused(false);
      if (document.visibilityState === 'hidden') hide();
    };
    const verify = () => {
      if (document.visibilityState === 'hidden') return;
      foreground.current = true;
      setFocused(true);
      const attempt = ++generation.current;
      setVerifying(true);
      softRequested.current = !masked.current;
      setSoftVerification(!masked.current);
      setFailed(false);
      let succeeded = true;
      void revalidate.current().catch(() => {
        succeeded = false;
        if (active && attempt === generation.current && foreground.current) {
          masked.current = true;
          setChecking(true);
          setFailed(true);
        }
      }).finally(() => {
        if (active && attempt === generation.current && foreground.current && document.visibilityState !== 'hidden') {
          setVerifying(false);
          softRequested.current = false;
          setChecking(!succeeded);
          masked.current = !succeeded;
        }
      });
    };
    const visibility = () => document.visibilityState === 'hidden' ? hide() : verify();
    const restore = (event: PageTransitionEvent) => { if (event.persisted) verify(); };
    window.addEventListener('blur', blur);
    window.addEventListener('focus', verify);
    window.addEventListener('pagehide', hide);
    window.addEventListener('pageshow', restore);
    document.addEventListener('visibilitychange', visibility);
    return () => {
      active = false;
      generation.current++;
      window.removeEventListener('blur', blur);
      window.removeEventListener('focus', verify);
      window.removeEventListener('pagehide', hide);
      window.removeEventListener('pageshow', restore);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, []);
  if (failed) return <InventoryAccessState status={503} />;
  if (checking || (revalidator.state !== 'idle' && !softVerification) || navigation.state !== 'idle') return <LoadingState />;
  if (!principal) return <InventoryAccessState status={401} />;
  return <SessionQueries key={JSON.stringify([principal, location.key, sessionGeneration])} principal={principal} authority={focused && !verifying && revalidator.state === 'idle'} generation={hostGeneration}>{children}</SessionQueries>;
}

export function InventorySessionBoundary({ children }: { children: ReactNode }) {
  const router = useContext(UNSAFE_DataRouterStateContext);
  const preview = useContext(InventoryPreviewContext);
  if (preview) return <>{children}</>;
  if (!router) return <InventoryAccessState status={401} />;
  return <HostedInventory>{children}</HostedInventory>;
}
