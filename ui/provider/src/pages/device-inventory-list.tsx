import { FleetPage } from '../components/fleet-page';
import { PhysicalInventory } from '../components/physical-inventory';
import { LoadingState } from '../components/states';
import { InventoryAccessState } from '../components/inventory-access-state';
import { useQueryClient } from '@tanstack/react-query';
import { useInventory } from '../lib/api';
import { recheckInventoryPermission } from '../lib/inventory-management';
import { InventorySessionBoundary } from '../components/inventory-session-boundary';
import { InventoryCountsProvider, InventoryNavigation } from '../components/inventory-navigation';
import { Text } from '@datum-cloud/datum-ui/typography';
import { InventoryPendingShell } from '../components/inventory-directory-heading';
import { TelemetryProvider } from '../components/power-telemetry';
import { Navigate, useLocation, useSearchParams } from 'react-router';
import { isCatalogKind } from '../components/inventory-groups';
import { inventoryDirectoryPath } from '../components/inventory-page-title';

type Scope = 'inventory' | 'catalog';

/**
 * The Inventory page, and (with scope "catalog") the Catalog page of
 * Manufacturers and Device types. A kind that belongs to the other page is sent
 * there, which also keeps old links and relative record links working.
 */
export function InventoryPage({ scope }: { scope: Scope }) {
  const [params] = useSearchParams();
  const { pathname, search } = useLocation();
  const kind = params.get('kind');
  if (kind !== null && isCatalogKind(kind) !== (scope === 'catalog')) return <Navigate replace to={`${inventoryDirectoryPath(pathname, kind)}${search}`} />;
  // The frame is always rendered so the navigation is never remounted; only
  // its class changes between list (navigation over the toolbar row) and detail
  // (navigation hidden; the header's back link is the way back).
  return <FleetPage testId={scope === 'catalog' ? 'fleet-plugin-catalog' : 'fleet-plugin-inventory-list'}><InventoryCountsProvider>
    <div className={params.get('record') === null ? 'inventory-list-frame' : 'inventory-detail-frame'}>
      <InventoryNavigation scope={scope} />
      <InventorySessionBoundary retainPresentationData pending={<InventoryPendingShell scope={scope}><Text as="p" role="status" aria-busy="true">Checking inventory access…</Text></InventoryPendingShell>} unavailable={status => <InventoryPendingShell scope={scope}><InventoryAccessState status={status} /></InventoryPendingShell>}>
        <InventoryListContent scope={scope} />
      </InventorySessionBoundary>
    </div>
  </InventoryCountsProvider></FleetPage>;
}

export default function DeviceInventoryList() {
  return <InventoryPage scope="inventory" />;
}

function InventoryListContent({ scope }: { scope: Scope }) {
  const { data, isLoading, error, refetch: refetchInventory } = useInventory();
  const client = useQueryClient();
  // Refresh is the explicit way to check management access again before its cache expires.
  const refetch = () => Promise.all([refetchInventory(), recheckInventoryPermission(client)]);
  return <>
    {isLoading && <InventoryPendingShell scope={scope}><LoadingState /></InventoryPendingShell>}
    {!isLoading && error && <InventoryPendingShell scope={scope}><InventoryAccessState status={error.status} onRetry={() => void refetch()} /></InventoryPendingShell>}
    {!isLoading && !error && data && <TelemetryProvider pending={<InventoryPendingShell scope={scope}><LoadingState /></InventoryPendingShell>}><PhysicalInventory inventory={data} scope={scope} showNavigation={false} onRetry={() => void refetch()} /></TelemetryProvider>}
  </>;
}
