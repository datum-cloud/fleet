import { ClaimTable } from '../components/claim-table';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { useMachineClaims } from '../lib/api';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@datum-cloud/datum-ui/breadcrumb';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { HomeIcon } from 'lucide-react';
import { Link, useParams } from 'react-router';

export default function ClaimList() {
  const { projectId } = useParams<{ projectId: string; serviceSlug: string }>();
  const { data: claims, isLoading, error, refetch } = useMachineClaims(projectId);
  const isEmpty = !isLoading && !error && (claims?.length ?? 0) === 0;

  return (
    <div data-testid="fleet-plugin-claim-list" className="flex min-w-0 flex-col gap-6">
      <Breadcrumb>
        <BreadcrumbList>
          <BreadcrumbItem>
            <BreadcrumbLink asChild>
              <Link to={projectId ? `/project/${projectId}` : '/'}>
                <Icon icon={HomeIcon} size={16} />
              </Link>
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbPage>Machines</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle title="Machines" description="Machines allocated to this project" />

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

      {!isLoading && !error && claims && claims.length > 0 && <ClaimTable claims={claims} />}
    </div>
  );
}
