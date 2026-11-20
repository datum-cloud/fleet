import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { UNSAFE_DataRouterContext, UNSAFE_DataRouterStateContext, useLocation, useNavigation, useRevalidator, useRouteLoaderData } from 'react-router';
import { InventoryAccessState } from './inventory-access-state';
import { LoadingState } from './states';
import { InventoryOperationContext, InventoryOperationScope } from '../lib/inventory-operation';

export const InventoryPreviewContext = createContext(false);

function SessionQueries({ children, principal }: { children: ReactNode; principal: string }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } }));
  const [scope] = useState(() => new InventoryOperationScope());
  const host = useContext(UNSAFE_DataRouterContext);
  useLayoutEffect(() => {
    scope.startLifecycle();
    const unsubscribe = host?.router.subscribe(state => {
      const identity = state.loaderData['layouts/milo.layout'] as { user?: { metadata?: { uid?: string; name?: string } } } | undefined;
      if (state.revalidation !== 'idle' || state.navigation.state !== 'idle' || (identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name) !== principal) scope.invalidate();
    });
    return () => { scope.invalidate(); unsubscribe?.(); };
  }, [host, principal, scope]);
  useEffect(() => () => client.clear(), [client]);
  return <InventoryOperationContext.Provider value={scope}><QueryClientProvider client={client}>{children}</QueryClientProvider></InventoryOperationContext.Provider>;
}

function HostedInventory({ children }: { children: ReactNode }) {
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
  const foreground = useRef(true);
  useEffect(() => {
    let active = true;
    const hide = () => {
      generation.current++;
      foreground.current = false;
      setChecking(true);
    };
    const verify = () => {
      if (document.visibilityState === 'hidden') return;
      foreground.current = true;
      const attempt = ++generation.current;
      setChecking(true);
      setFailed(false);
      void revalidate.current().catch(() => {
        if (active && attempt === generation.current && foreground.current) setFailed(true);
      }).finally(() => {
        if (active && attempt === generation.current && foreground.current && document.visibilityState !== 'hidden') setChecking(false);
      });
    };
    const visibility = () => document.visibilityState === 'hidden' ? hide() : verify();
    const restore = (event: PageTransitionEvent) => { if (event.persisted) verify(); };
    window.addEventListener('blur', hide);
    window.addEventListener('focus', verify);
    window.addEventListener('pagehide', hide);
    window.addEventListener('pageshow', restore);
    document.addEventListener('visibilitychange', visibility);
    return () => {
      active = false;
      generation.current++;
      window.removeEventListener('blur', hide);
      window.removeEventListener('focus', verify);
      window.removeEventListener('pagehide', hide);
      window.removeEventListener('pageshow', restore);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, []);
  if (checking || revalidator.state !== 'idle' || navigation.state !== 'idle') return <LoadingState />;
  if (failed) return <InventoryAccessState status={503} />;
  if (!principal) return <InventoryAccessState status={401} />;
  return <SessionQueries key={JSON.stringify([principal, location.key])} principal={principal}>{children}</SessionQueries>;
}

export function InventorySessionBoundary({ children }: { children: ReactNode }) {
  const router = useContext(UNSAFE_DataRouterStateContext);
  const preview = useContext(InventoryPreviewContext);
  if (preview) return <>{children}</>;
  if (!router) return <InventoryAccessState status={401} />;
  return <HostedInventory>{children}</HostedInventory>;
}
