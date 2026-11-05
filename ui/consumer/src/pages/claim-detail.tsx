import { PhaseBadge } from '../components/claim-table';
import { DetailCard, DetailList } from '../components/detail-card';
import { usePluginBase } from '../components/machines-page';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { StatusBadge, type BadgeType } from '../components/status-badge';
import { ApiError, useMachineClaim } from '../lib/api';
import type { ClaimCondition, ClaimDetail as ClaimDetailModel } from '../types';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@datum-cloud/datum-ui/breadcrumb';
import { DateTime } from '@datum-cloud/datum-ui/date-time';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { ActivityIcon, CpuIcon, FileTextIcon, HomeIcon, NetworkIcon } from 'lucide-react';
import { Link, useParams } from 'react-router';

function Muted({ children }: { children: React.ReactNode }) {
  return <span className="text-muted-foreground">{children}</span>;
}

function When({ date }: { date?: string }) {
  if (!date) return <Muted>—</Muted>;
  return <DateTime date={date} variant="relative" className="text-muted-foreground text-sm" />;
}

// A rejected request is a failure; any other False is a step still waiting.
function conditionBadge(condition: ClaimCondition): BadgeType {
  if (condition.status === 'True') return 'success';
  if (condition.status === 'Unknown') return 'muted';
  return condition.type === 'Accepted' ? 'danger' : 'warning';
}

function RequestCard({ claim }: { claim: ClaimDetailModel }) {
  return (
    <DetailCard icon={FileTextIcon} title="Request" testId="fleet-plugin-claim-request">
      <DetailList
        items={[
          { label: 'Class', content: claim.machineClass },
          { label: 'Profile', content: claim.machineProfile },
          { label: 'Location', content: claim.location },
          { label: 'Requested power', content: claim.powerIntent },
          { label: 'Created', content: <When date={claim.createdAt} /> },
          {
            label: 'Releasing since',
            content: <When date={claim.deletingSince} />,
            hidden: !claim.deletingSince,
          },
        ]}
      />
    </DetailCard>
  );
}

function MachineCard({ claim }: { claim: ClaimDetailModel }) {
  return (
    <DetailCard icon={CpuIcon} title="Machine" testId="fleet-plugin-claim-machine">
      {claim.hardware ? (
        <DetailList
          items={[
            { label: 'Location', content: claim.placement ?? '—' },
            {
              label: 'Accepted profile',
              content: claim.acceptedProfile ?? '—',
              hidden: !claim.acceptedProfile,
            },
            { label: 'Power', content: claim.powerObserved ?? 'Not reported' },
            ...claim.hardware.map(({ label, value }) => ({ label, content: value })),
            { label: 'Installation', content: claim.installation ?? 'None', hidden: !claim.installation },
            { label: 'Observed', content: <When date={claim.hardwareObservedAt} /> },
          ]}
        />
      ) : (
        <p className="text-muted-foreground p-4 text-sm">
          No machine is allocated yet. Fleet binds one when capacity matching the profile is
          available.
        </p>
      )}
    </DetailCard>
  );
}

function NetworkCard({ claim }: { claim: ClaimDetailModel }) {
  return (
    <DetailCard icon={NetworkIcon} title="Network" testId="fleet-plugin-claim-network">
      <DetailList
        items={claim.interfaces.map((i) => ({
          label: i.name,
          content: (
            <span>
              {i.network}
              {i.addresses.length > 0 && <Muted> · {i.addresses.join(', ')}</Muted>}
            </span>
          ),
        }))}
      />
    </DetailCard>
  );
}

function ConditionsCard({ claim }: { claim: ClaimDetailModel }) {
  return (
    <DetailCard icon={ActivityIcon} title="Conditions" testId="fleet-plugin-claim-conditions">
      <DetailList
        items={claim.conditions.map((c) => ({
          label: c.type,
          content: (
            <div className="flex flex-col gap-1" data-testid={`fleet-plugin-condition-${c.type}`}>
              <div className="flex flex-wrap items-center gap-2">
                <StatusBadge type={conditionBadge(c)}>{c.status}</StatusBadge>
                {c.reason && <span className="text-sm">{c.reason}</span>}
                {c.lastTransitionTime && <When date={c.lastTransitionTime} />}
              </div>
              {c.message && <Muted>{c.message}</Muted>}
            </div>
          ),
        }))}
      />
    </DetailCard>
  );
}

export default function ClaimDetail() {
  const { projectId, claimName } = useParams<{ projectId: string; claimName: string }>();
  const base = usePluginBase();
  const { data: claim, isLoading, error, refetch } = useMachineClaim(projectId, claimName);
  const notFound = error instanceof ApiError && error.status === 404;

  return (
    <div data-testid="fleet-plugin-claim-detail" className="flex min-w-0 flex-col gap-6">
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
            <BreadcrumbLink asChild>
              <Link to={base}>Machines</Link>
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem className="min-w-0">
            <BreadcrumbPage className="truncate">{claimName}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle
        title={claimName ?? 'Machine'}
        description={claim ? <PhaseBadge claim={claim} /> : undefined}
      />

      {isLoading && <LoadingState />}

      {notFound && (
        <div data-testid="fleet-plugin-claim-not-found">
          <EmptyContent
            title="Machine not found"
            subtitle="It may have been released, or the name is wrong."
            className="w-full"
          />
        </div>
      )}

      {!isLoading && error && !notFound && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this machine."
          onRetry={() => void refetch()}
        />
      )}

      {claim && (
        <div className="grid gap-4 sm:grid-cols-2">
          <RequestCard claim={claim} />
          <MachineCard claim={claim} />
          {claim.interfaces.length > 0 && <NetworkCard claim={claim} />}
          <ConditionsCard claim={claim} />
        </div>
      )}
    </div>
  );
}
