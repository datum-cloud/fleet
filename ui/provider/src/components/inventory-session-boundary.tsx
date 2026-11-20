import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { UNSAFE_DataRouterStateContext, useRevalidator, useRouteLoaderData } from 'react-router';
import { InventoryAccessState } from './inventory-access-state';
import { LoadingState } from './states';

export const InventoryPreviewContext = createContext(false);

function SessionQueries({ children }: { children: ReactNode }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } }));
  useEffect(() => () => client.clear(), [client]);
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function HostedInventory({ children }: { children: ReactNode }) {
  const identity = useRouteLoaderData('layouts/milo.layout') as { user?: { metadata?: { uid?: string; name?: string } } } | undefined;
  const principal = identity?.user?.metadata?.uid ?? identity?.user?.metadata?.name;
  const revalidator = useRevalidator();
  const revalidate = useRef(revalidator.revalidate);
  revalidate.current = revalidator.revalidate;
  const [checking, setChecking] = useState(() => typeof document !== 'undefined' && document.visibilityState === 'hidden');
  const [failed, setFailed] = useState(false);
  const checkingRef = useRef(false);
  useEffect(() => {
    let active = true;
    const hide = () => setChecking(true);
    const verify = () => {
      if (document.visibilityState === 'hidden' || checkingRef.current) return;
      checkingRef.current = true;
      setChecking(true);
      setFailed(false);
      void revalidate.current().catch(() => {
        if (active) setFailed(true);
      }).finally(() => {
        checkingRef.current = false;
        if (active) setChecking(false);
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
      window.removeEventListener('blur', hide);
      window.removeEventListener('focus', verify);
      window.removeEventListener('pagehide', hide);
      window.removeEventListener('pageshow', restore);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, []);
  if (checking || revalidator.state !== 'idle') return <LoadingState />;
  if (failed) return <InventoryAccessState status={503} />;
  if (!principal) return <InventoryAccessState status={401} />;
  return <SessionQueries key={principal}>{children}</SessionQueries>;
}

export function InventorySessionBoundary({ children }: { children: ReactNode }) {
  const router = useContext(UNSAFE_DataRouterStateContext);
  const preview = useContext(InventoryPreviewContext);
  if (preview) return <>{children}</>;
  if (!router) return <InventoryAccessState status={401} />;
  return <HostedInventory>{children}</HostedInventory>;
}
