import { MachineTable } from '../components/machine-table';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { MACHINE_NAMESPACE, PROVIDER_PROJECT, useMachines } from '../lib/api';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';

// staff-portal's platform mount renders its own breadcrumb but no padding, so
// this page starts at the title and pads itself like the host's pages (p-6).
export default function MachineList() {
  const { data: machines, isLoading, error, refetch } = useMachines();
  const isEmpty = !isLoading && !error && (machines?.length ?? 0) === 0;

  return (
    <div data-testid="fleet-plugin-machine-list" className="flex min-w-0 flex-col gap-6 p-4 sm:p-6">
      <PageTitle
        title="Fleet"
        description={`Provider capacity in ${PROVIDER_PROJECT} / ${MACHINE_NAMESPACE}`}
      />

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
    </div>
  );
}
