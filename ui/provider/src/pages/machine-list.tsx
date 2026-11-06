import { CreateMachineDialog } from '../components/create-machine-dialog';
import { DeleteDialog } from '../components/delete-dialog';
import { FleetPage } from '../components/fleet-page';
import { MachineTable } from '../components/machine-table';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { PROVIDER_PROJECT, useDeleteMachine, useMachines } from '../lib/api';
import type { Machine } from '../types';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { useCallback, useState } from 'react';

export default function MachineList() {
  const { data: machines, isLoading, error, refetch } = useMachines();
  const isEmpty = !isLoading && !error && (machines?.length ?? 0) === 0;
  const [deleting, setDeleting] = useState<Machine>();
  const deletion = useDeleteMachine();
  const startDelete = useCallback(
    (machine: Machine) => {
      deletion.reset();
      setDeleting(machine);
    },
    [deletion.reset]
  );

  return (
    <FleetPage testId="fleet-plugin-machine-list" actions={<CreateMachineDialog />}>
      {isLoading && <LoadingState />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage={`You don't have permission to view Machines in ${PROVIDER_PROJECT}.`}
          onRetry={() => void refetch()}
        />
      )}

      {isEmpty && (
        <div data-testid="fleet-plugin-machine-empty">
          <EmptyContent
            title="No machines yet"
            subtitle="Create a machine to give a class capacity to draw from."
            className="w-full"
          />
        </div>
      )}

      {!isLoading && !error && machines && machines.length > 0 && (
        <MachineTable machines={machines} onDelete={startDelete} />
      )}

      <DeleteDialog
        name={deleting?.name}
        resource="machine"
        warning={`The record is removed from ${deleting?.namespace ?? 'its namespace'} straight away. Fleet doesn't yet check whether a claim is using this machine.`}
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
