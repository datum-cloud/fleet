import { FleetPage } from '../components/fleet-page';
import { MachineTable } from '../components/machine-table';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { PROVIDER_PROJECT, useMachines } from '../lib/api';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';

export default function MachineList() {
  const { data: machines, isLoading, error, refetch } = useMachines();
  const isEmpty = !isLoading && !error && (machines?.length ?? 0) === 0;

  return (
    <FleetPage testId="fleet-plugin-machine-list">
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
            subtitle="Machines enrolled in the provider project will appear here."
            className="w-full"
          />
        </div>
      )}

      {!isLoading && !error && machines && machines.length > 0 && (
        <MachineTable machines={machines} />
      )}
    </FleetPage>
  );
}
