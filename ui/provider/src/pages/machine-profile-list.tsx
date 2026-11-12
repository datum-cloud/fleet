import { CreateProfileDialog } from '../components/create-profile-dialog';
import { DeleteDialog } from '../components/delete-dialog';
import { FleetPage } from '../components/fleet-page';
import { ProfileTable } from '../components/profile-table';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import {
  PROVIDER_PROJECT,
  useDeleteMachineProfile,
  useMachineClasses,
  useMachineProfiles,
} from '../lib/api';
import type { MachineProfile } from '../types';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { useCallback, useState } from 'react';

export default function MachineProfileList() {
  const { data: profiles, isLoading, error, refetch } = useMachineProfiles();
  const isEmpty = !isLoading && !error && (profiles?.length ?? 0) === 0;
  const { data: classes } = useMachineClasses();
  const [deleting, setDeleting] = useState<MachineProfile>();
  const deletion = useDeleteMachineProfile();
  const startDelete = useCallback(
    (profile: MachineProfile) => {
      deletion.reset();
      setDeleting(profile);
    },
    [deletion.reset]
  );
  // Fleet doesn't stop a profile being deleted while classes allow it.
  const allowedBy = (classes ?? [])
    .filter((c) => deleting && c.allowedProfiles.includes(deleting.name))
    .map((c) => c.name);

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
        <ProfileTable profiles={profiles} onDelete={startDelete} />
      )}

      <DeleteDialog
        name={deleting?.name}
        resource="profile"
        warning={
          allowedBy.length > 0
            ? `Allowed by ${allowedBy.join(', ')}. Those classes keep listing it, but consumers can't deploy it.`
            : 'No class allows this profile.'
        }
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
