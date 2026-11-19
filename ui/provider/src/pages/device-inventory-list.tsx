import { FleetPage } from '../components/fleet-page';
import { InventoryTable } from '../components/inventory-table';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { PROVIDER_PROJECT, useInventory } from '../lib/api';
import { modelLabel } from '../inventory';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { Text } from '@datum-cloud/datum-ui/typography';
import { useParams } from 'react-router';

// Read-only. Planners and registrars write DeviceClaims and Devices through
// GitOps; nothing here creates capacity or acts on hardware.
export default function DeviceInventoryList() {
  const { slug } = useParams();
  const base = `/plugins/${slug}`;
  const { data, isLoading, error, refetch } = useInventory();
  const isEmpty = !isLoading && !error && data && data.slots.length === 0 && data.unboundDevices.length === 0;

  return (
    <FleetPage testId="fleet-plugin-inventory-list">
      {isLoading && <LoadingState />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage={`You don't have permission to view DeviceClaims and Devices in ${PROVIDER_PROJECT}.`}
          onRetry={() => void refetch()}
        />
      )}

      {isEmpty && (
        <div data-testid="fleet-plugin-inventory-empty">
          <EmptyContent
            title="No planned slots yet"
            subtitle="DeviceClaims plan where servers go; Devices record the hardware bound to them."
            className="w-full"
          />
        </div>
      )}

      {!isLoading && !error && data && data.slots.length > 0 && <InventoryTable slots={data.slots} base={base} />}

      {!isLoading && !error && data && data.unboundDevices.length > 0 && (
        <Card data-testid="fleet-plugin-unbound-devices">
          <CardContent className="flex flex-col gap-2">
            <Text as="p" weight="semibold">Devices without a matching claim</Text>
            <Text as="p" textColor="muted">
              These Devices name a DeviceClaim that does not exist in their namespace.
            </Text>
            <ul className="flex flex-col gap-1">
              {data.unboundDevices.map((device) => (
                <li key={`${device.metadata.namespace}/${device.metadata.name}`}>
                  <Text>
                    {device.metadata.name} → {device.spec.claimRef.name} ({modelLabel(device.spec.identity)})
                  </Text>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
    </FleetPage>
  );
}
