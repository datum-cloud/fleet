import { CreateProfileDialog } from '../components/create-profile-dialog';
import { FleetPage } from '../components/fleet-page';
import { ProfileTable } from '../components/profile-table';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { PROVIDER_PROJECT, useMachineProfiles } from '../lib/api';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';

export default function MachineProfileList() {
  const { data: profiles, isLoading, error, refetch } = useMachineProfiles();
  const isEmpty = !isLoading && !error && (profiles?.length ?? 0) === 0;

  return (
    <FleetPage testId="fleet-plugin-profile-list" actions={<CreateProfileDialog />}>
      {isLoading && <LoadingState />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage={`You don't have permission to view MachineProfiles in ${PROVIDER_PROJECT}.`}
          onRetry={() => void refetch()}
        />
      )}

      {isEmpty && (
        <div data-testid="fleet-plugin-profile-empty">
          <EmptyContent
            title="No profiles yet"
            subtitle="Profiles describe the hardware a machine class can offer."
            className="w-full"
          />
        </div>
      )}

      {!isLoading && !error && profiles && profiles.length > 0 && (
        <ProfileTable profiles={profiles} />
      )}
    </FleetPage>
  );
}
