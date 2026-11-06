import { ClassTable } from '../components/class-table';
import { CreateClassDialog } from '../components/create-class-dialog';
import { DeleteDialog } from '../components/delete-dialog';
import { FleetPage } from '../components/fleet-page';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { PROVIDER_PROJECT, useDeleteMachineClass, useMachineClasses } from '../lib/api';
import type { MachineClass } from '../types';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { useCallback, useState } from 'react';

export default function MachineClassList() {
  const { data: classes, isLoading, error, refetch } = useMachineClasses();
  const isEmpty = !isLoading && !error && (classes?.length ?? 0) === 0;
  const [deleting, setDeleting] = useState<MachineClass>();
  const deletion = useDeleteMachineClass();
  const startDelete = useCallback(
    (machineClass: MachineClass) => {
      deletion.reset();
      setDeleting(machineClass);
    },
    [deletion.reset]
  );

  return (
    <FleetPage testId="fleet-plugin-class-list" actions={<CreateClassDialog />}>
      {isLoading && <LoadingState />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage={`You don't have permission to view MachineClasses in ${PROVIDER_PROJECT}.`}
          onRetry={() => void refetch()}
        />
      )}

      {isEmpty && (
        <div data-testid="fleet-plugin-class-empty">
          <EmptyContent
            title="No classes yet"
            subtitle="A class offers profiles to consumers and names the Machines that serve them."
            className="w-full"
          />
        </div>
      )}

      {!isLoading && !error && classes && classes.length > 0 && (
        <ClassTable classes={classes} onDelete={startDelete} />
      )}

      <DeleteDialog
        name={deleting?.name}
        resource="class"
        warning="Consumers can no longer deploy from it. Claims that already use it keep pointing at a class that no longer exists, and its Machines stay as they are."
        pending={deletion.isPending}
        error={deletion.error}
        onConfirm={() =>
          deleting &&
          deletion.mutate(deleting.name, { onSuccess: () => setDeleting(undefined) })
        }
        onOpenChange={(open) => !open && setDeleting(undefined)}
      />
    </FleetPage>
  );
}
