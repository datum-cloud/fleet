import { ClaimTable } from '../components/claim-table';
import { MachinesPage } from '../components/machines-page';
import { ReleaseDialog } from '../components/release-dialog';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { useMachineClaims } from '../lib/api';
import type { MachineClaim } from '../types';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { useCallback, useState } from 'react';
import { useParams } from 'react-router';

export default function ClaimList() {
  const { projectId } = useParams<{ projectId: string; serviceSlug: string }>();
  const { data: claims, isLoading, error, refetch } = useMachineClaims(projectId);
  const isEmpty = !isLoading && !error && (claims?.length ?? 0) === 0;
  const [releasing, setReleasing] = useState<string>();
  const startRelease = useCallback((claim: MachineClaim) => setReleasing(claim.name), []);

  return (
    <MachinesPage testId="fleet-plugin-claim-list" description="Machines allocated to this project">
      {isLoading && <LoadingState />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this project's machines."
          onRetry={() => void refetch()}
        />
      )}

      {isEmpty && (
        <div data-testid="fleet-plugin-claim-empty">
          <EmptyContent
            title="No machines yet"
            subtitle="Machines you claim in this project will appear here."
            className="w-full"
          />
        </div>
      )}

      {!isLoading && !error && claims && claims.length > 0 && (
        <ClaimTable claims={claims} onRelease={startRelease} />
      )}

      <ReleaseDialog name={releasing} onOpenChange={(open) => !open && setReleasing(undefined)} />
    </MachinesPage>
  );
}
