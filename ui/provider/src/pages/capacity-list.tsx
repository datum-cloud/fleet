import { CreateCapacityDialog } from '../components/create-capacity-dialog';
import { DeleteDialog } from '../components/delete-dialog';
import { FleetPage } from '../components/fleet-page';
import { CapacityTable } from '../components/capacity-table';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { PROVIDER_PROJECT, useDeleteMachineCapacity, useMachineCapacities } from '../lib/api';
import type { MachineCapacity } from '../types';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { useCallback, useState } from 'react';
import { Link, useSearchParams } from 'react-router';

export default function CapacityList() {
  const { data: all, isLoading, error, refetch } = useMachineCapacities();
  // Classes link here with the namespace and pool their selector names.
  const [params] = useSearchParams();
  const pool = params.get('pool') ?? undefined;
  const namespace = params.get('namespace') ?? undefined;
  const capacities = pool || namespace ? all?.filter((c) => (!pool || c.pool === pool) && (!namespace || c.namespace === namespace)) : all;
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
    <FleetPage live testId="fleet-plugin-capacity-list" actions={<CreateCapacityDialog onRegistered={() => void refetch()} />}>
      {(pool || namespace) && (
        <p className="fleet-filter" data-testid="fleet-plugin-capacity-filter">
          <span title={namespace && `MachineCapacities in ${namespace}`}>Pool {pool ?? 'any'}</span>
          <Link className="fleet-link" to="?">Show all</Link>
        </p>
      )}

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
            title={pool || namespace ? 'No machine capacity in this pool' : 'No machine capacity yet'}
            subtitle={pool || namespace ? undefined : 'Add machine capacity for a class to draw from.'}
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
        warning={`Deletion in ${deleting?.namespace ?? 'its namespace'} is subject to server lifecycle fences. The production registration authority rejects deletion. Never use deletion to roll back an uncertain registration.`}
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
