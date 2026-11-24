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

export default function DeviceInventoryList() {
  return <FleetPage testId="fleet-plugin-inventory-list"><InventoryCountsProvider>
    <InventoryNavigation />
    <InventorySessionBoundary retainPresentationData pending={<InventoryPendingShell><Text as="p" role="status" aria-busy="true">Checking inventory access…</Text></InventoryPendingShell>} unavailable={status => <InventoryPendingShell><InventoryAccessState status={status} /></InventoryPendingShell>}>
      <InventoryListContent />
    </InventorySessionBoundary>
  </InventoryCountsProvider></FleetPage>;
}

function InventoryListContent() {
  const { data, isLoading, error, refetch: refetchInventory } = useInventory();
  const client = useQueryClient();
  // Refresh is the explicit way to check management access again before its cache expires.
  const refetch = () => Promise.all([refetchInventory(), recheckInventoryPermission(client)]);
  return <>
    {isLoading && <InventoryPendingShell><LoadingState /></InventoryPendingShell>}
    {!isLoading && error && <InventoryPendingShell><InventoryAccessState status={error.status} onRetry={() => void refetch()} /></InventoryPendingShell>}
    {!isLoading && !error && data && <TelemetryProvider pending={<InventoryPendingShell><LoadingState /></InventoryPendingShell>}><PhysicalInventory inventory={data} showNavigation={false} onRetry={() => void refetch()} /></TelemetryProvider>}
  </>;
}
