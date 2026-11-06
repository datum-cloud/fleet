import { DeployDialog, type DeployTarget } from '../components/deploy-dialog';
import { MachinesPage } from '../components/machines-page';
import { OfferingSection } from '../components/offering-section';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { useCatalog } from '../lib/api';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { useState } from 'react';
import { useParams } from 'react-router';

export default function Catalog() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: offerings, isLoading, error, refetch } = useCatalog(projectId);
  const isEmpty = !isLoading && !error && (offerings?.length ?? 0) === 0;
  const [deployTarget, setDeployTarget] = useState<DeployTarget | null>(null);

  return (
    <MachinesPage testId="fleet-plugin-catalog" description="Machine profiles this project can claim">
      {isLoading && <LoadingState />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this project's machine catalog."
          onRetry={() => void refetch()}
        />
      )}

      {isEmpty && (
        <div data-testid="fleet-plugin-catalog-empty">
          <EmptyContent
            title="Nothing to claim yet"
            subtitle="Machine classes published to this project will appear here."
            className="w-full"
          />
        </div>
      )}

      {!isLoading &&
        !error &&
        offerings?.map((offering) => (
          <OfferingSection
            key={offering.className}
            offering={offering}
            onDeploy={(className, profile) => setDeployTarget({ className, profile })}
          />
        ))}

      <DeployDialog target={deployTarget} onOpenChange={(open) => !open && setDeployTarget(null)} />
    </MachinesPage>
  );
}
