import { CreateCapacityDialog } from '../components/create-capacity-dialog';
import { DeleteDialog } from '../components/delete-dialog';
import { FleetPage } from '../components/fleet-page';
import { CapacityTable } from '../components/capacity-table';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { PROVIDER_PROJECT, useDeleteMachineCapacity, useMachineCapacities } from '../lib/api';
import type { MachineCapacity } from '../types';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { useCallback, useState } from 'react';

export default function CapacityList() {
  const { data: capacities, isLoading, error, refetch } = useMachineCapacities();
  const isEmpty = !isLoading && !error && (capacities?.length ?? 0) === 0;
  const [deleting, setDeleting] = useState<MachineCapacity>();
  const deletion = useDeleteMachineCapacity();
  const startDelete = useCallback(
    (capacity: MachineCapacity) => {
      deletion.reset();
      setDeleting(capacity);
    },
    [deletion.reset]
  );

  return (
    <FleetPage testId="fleet-plugin-capacity-list" actions={<CreateCapacityDialog />}>
      {isLoading && <LoadingState />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage={`You don't have permission to view machine capacity in ${PROVIDER_PROJECT}.`}
          onRetry={() => void refetch()}
        />
      )}

      {isEmpty && (
        <div data-testid="fleet-plugin-capacity-empty">
          <EmptyContent
            title="No machine capacity yet"
            subtitle="Add machine capacity for a class to draw from."
            className="w-full"
          />
        </div>
      )}

      {!isLoading && !error && capacities && capacities.length > 0 && (
        <CapacityTable capacities={capacities} onDelete={startDelete} />
      )}

      <DeleteDialog
        name={deleting?.name}
        resource="machine capacity"
        warning={`The record is removed from ${deleting?.namespace ?? 'its namespace'} straight away. Fleet doesn't yet check whether a customer machine is using it.`}
        pending={deletion.isPending}
        error={deletion.error}
        onConfirm={() =>
          deleting && deletion.mutate(deleting, { onSuccess: () => setDeleting(undefined) })
        }
        onOpenChange={(open) => !open && setDeleting(undefined)}
      />
    </FleetPage>
  );
}
