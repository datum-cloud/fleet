import { ClassTable } from '../components/class-table';
import { CreateClassDialog } from '../components/create-class-dialog';
import { FleetPage } from '../components/fleet-page';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { PROVIDER_PROJECT, useMachineClasses } from '../lib/api';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';

export default function MachineClassList() {
  const { data: classes, isLoading, error, refetch } = useMachineClasses();
  const isEmpty = !isLoading && !error && (classes?.length ?? 0) === 0;

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

      {!isLoading && !error && classes && classes.length > 0 && <ClassTable classes={classes} />}
    </FleetPage>
  );
}
