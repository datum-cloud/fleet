import { Eyebrow, Fact, Facts } from '../components/fact';
import { MachineStatusChip } from '../components/machine-status';
import { MachinesPage, usePluginBase } from '../components/machines-page';
import { ReleaseDialog } from '../components/release-dialog';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { ApiError, useMachine } from '../lib/api';
import { machineSteps, type MachineDetail as MachineDetailModel, type MachineStep } from '../types';
import { Button } from '@datum-cloud/datum-ui/button';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { DateTime } from '@datum-cloud/datum-ui/date-time';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import {
  ActivityIcon,
  ArrowRightIcon,
  CheckIcon,
  CpuIcon,
  FileTextIcon,
  HourglassIcon,
  LogOutIcon,
  NetworkIcon,
  XIcon,
  type LucideIcon,
} from 'lucide-react';
import { useState } from 'react';
import { useParams } from 'react-router';

function When({ date }: { date?: string }) {
  if (!date) return <>—</>;
  return <DateTime date={date} variant="relative" addSuffix />;
}

function FlowCard({
  icon,
  title,
  testId,
  children,
}: {
  icon: LucideIcon;
  title: string;
  testId: string;
  children: React.ReactNode;
}) {
  return (
    <Card size="sm" className="h-full w-full" data-testid={testId}>
      <CardContent className="flex flex-col gap-4">
        <span className="text-muted-foreground flex items-center gap-2 text-xs font-medium">
          <Icon icon={icon} size={14} />
          {title}
        </span>
        {children}
      </CardContent>
    </Card>
  );
}

function FlowArrow() {
  return (
    <div className="fleet-arrow" aria-hidden>
      <Icon icon={ArrowRightIcon} size={18} className="text-muted-foreground" />
    </div>
  );
}

function RequestCard({ machine }: { machine: MachineDetailModel }) {
  return (
    <FlowCard icon={FileTextIcon} title="You asked for" testId="fleet-plugin-machine-request">
      <Facts>
        <Fact label="Class" mono>{machine.machineClass}</Fact>
        <Fact label="Profile" mono>{machine.machineProfile}</Fact>
        <Fact label="Location">{machine.location}</Fact>
        <Fact label="Power">{machine.powerIntent}</Fact>
      </Facts>
    </FlowCard>
  );
}

// Until Fleet binds a server there's no hardware to show: skeleton lines and
// Fleet's own explanation instead.
function HardwareCard({ machine }: { machine: MachineDetailModel }) {
  if (!machine.hardware) {
    const tone = machine.phase === 'Rejected' ? 'danger' : machine.waitingForCapacity ? 'warning' : undefined;
    return (
      <FlowCard icon={CpuIcon} title="Fleet allocates" testId="fleet-plugin-machine-hardware">
        <div className="flex flex-col gap-2.5" aria-hidden>
          <Skeleton className="h-2.5 w-44 rounded" />
          <Skeleton className="h-2.5 w-60 rounded" />
          <Skeleton className="h-2.5 w-36 rounded" />
        </div>
        <p className="fleet-note flex items-start gap-2 text-sm" data-tone={tone}>
          <Icon icon={HourglassIcon} size={14} className="mt-0.5 shrink-0" />
          <span>
            {machine.waitingForCapacity
              ? `No free server matches ${machine.machineProfile} in ${machine.machineClass} yet. Fleet binds one as soon as capacity frees up; you don't need to do anything.`
              : 'No hardware is allocated yet. Fleet binds a server when capacity matching the profile is available.'}
          </span>
        </p>
      </FlowCard>
    );
  }
  return (
    <FlowCard icon={CpuIcon} title="Fleet allocated" testId="fleet-plugin-machine-hardware">
      <Facts>
        {machine.acceptedProfile && machine.acceptedProfile !== machine.machineProfile && (
          <Fact label="Accepted profile" mono>{machine.acceptedProfile}</Fact>
        )}
        {machine.hardware.map(({ label, value }) => (
          <Fact key={label} label={label} mono>{value}</Fact>
        ))}
        <Fact label="Power">
          {machine.powerObserved ?? 'Not reported'}
          {machine.hardwareObservedAt && (
            <span className="text-muted-foreground">
              {' '}· reported <When date={machine.hardwareObservedAt} />
            </span>
          )}
        </Fact>
        <Fact label="Installation">{machine.installation ?? 'None yet'}</Fact>
      </Facts>
    </FlowCard>
  );
}

function NetworkCard({ machine }: { machine: MachineDetailModel }) {
  return (
    <FlowCard icon={NetworkIcon} title="Network" testId="fleet-plugin-machine-network">
      <Facts>
        {machine.interfaces.length === 0 ? (
          <Fact label="Interfaces">None requested</Fact>
        ) : (
          machine.interfaces.map((i) => (
            <Fact key={i.name} label={i.name} mono>
              {i.network}
              {i.addresses.length > 0 && (
                <span className="text-muted-foreground"> · {i.addresses.join(', ')}</span>
              )}
            </Fact>
          ))
        )}
      </Facts>
    </FlowCard>
  );
}

function Overview({ machine }: { machine: MachineDetailModel }) {
  const power = machine.phase === 'Ready' && machine.powerObserved ? `powered ${machine.powerObserved.toLowerCase()}` : undefined;
  return (
    <section
      className="fleet-band flex flex-col gap-4 rounded-xl px-5 pt-5 pb-6 md:px-7"
      data-testid="fleet-plugin-machine-overview">
      <div className="flex flex-wrap items-center gap-2.5">
        <Eyebrow>Request → allocation</Eyebrow>
        <span className="flex-1" />
        <MachineStatusChip machine={machine} suffix={power} />
      </div>
      <div className="fleet-flow">
        <RequestCard machine={machine} />
        <FlowArrow />
        <HardwareCard machine={machine} />
        <FlowArrow />
        <NetworkCard machine={machine} />
      </div>
    </section>
  );
}

const STEP_ICON: Record<MachineStep['state'], LucideIcon | undefined> = {
  done: CheckIcon,
  current: HourglassIcon,
  failed: XIcon,
  upcoming: undefined,
};

function Progress({ machine }: { machine: MachineDetailModel }) {
  const steps = machineSteps(machine.conditions);
  // Conditions beyond the four lifecycle ones, listed as written.
  const extra = machine.conditions.filter((c) => !steps.some((s) => s.type === c.type));
  return (
    <Card size="sm" data-testid="fleet-plugin-machine-progress">
      <CardContent className="flex flex-col gap-5">
        <span className="flex items-center gap-2">
          <Icon icon={ActivityIcon} size={14} className="text-muted-foreground" />
          <span className="text-sm font-semibold">Progress</span>
          <span className="text-muted-foreground text-xs">from Fleet's conditions</span>
        </span>
        <ol className="fleet-steps">
          {steps.map((step, i) => {
            const icon = STEP_ICON[step.state];
            return (
              <li
                key={step.type}
                className="flex min-w-0 flex-col gap-2"
                data-testid={`fleet-plugin-step-${step.type}`}
                data-state={step.state}>
                <span className="flex items-center gap-2">
                  <span className="fleet-step-mark" data-state={step.state}>
                    {icon && <Icon icon={icon} size={12} />}
                  </span>
                  {i < steps.length - 1 && (
                    <span className="fleet-step-line" data-state={step.state === 'done' ? 'done' : undefined} />
                  )}
                </span>
                <span className="text-sm font-medium">{step.label}</span>
                <span
                  className={
                    step.state === 'upcoming' ? 'text-muted-foreground/70 text-xs' : 'text-muted-foreground text-xs'
                  }>
                  {step.detail}
                </span>
                {step.at && (
                  <span className="text-muted-foreground font-mono text-xs">
                    <When date={step.at} />
                  </span>
                )}
              </li>
            );
          })}
        </ol>
        {extra.length > 0 && (
          <dl className="border-border flex flex-col gap-1 border-t pt-4 text-xs">
            {extra.map((c) => (
              <div key={c.type} className="flex gap-2">
                <dt className="font-medium">{c.type}</dt>
                <dd className="text-muted-foreground">
                  {c.status}
                  {c.reason && ` · ${c.reason}`}
                </dd>
              </div>
            ))}
          </dl>
        )}
      </CardContent>
    </Card>
  );
}

export default function MachineDetail() {
  const { projectId, machineName } = useParams<{ projectId: string; machineName: string }>();
  const base = usePluginBase();
  const { data: machine, isLoading, error, refetch } = useMachine(projectId, machineName);
  const [releasing, setReleasing] = useState(false);
  const notFound = error instanceof ApiError && error.status === 404;

  return (
    <MachinesPage
      testId="fleet-plugin-machine-detail"
      title={machineName ?? 'Machine'}
      crumbs={[{ label: 'Machines', href: base }, { label: machineName ?? '' }]}
      description={
        machine && (
          <span className="text-muted-foreground text-sm">
            Bare-metal machine · created <When date={machine.createdAt} />
            {machine.deletingSince && (
              <>
                {' '}· releasing since <When date={machine.deletingSince} />
              </>
            )}
          </span>
        )
      }
      actions={
        machine &&
        !machine.deletingSince && (
          <Button
            type="danger"
            theme="outline"
            size="xs"
            htmlType="button"
            icon={<Icon icon={LogOutIcon} size={12} />}
            onClick={() => setReleasing(true)}>
            Release
          </Button>
        )
      }>
      {isLoading && <LoadingState />}

      {notFound && (
        <div data-testid="fleet-plugin-machine-not-found">
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

      {machine && (
        <>
          <Overview machine={machine} />
          <Progress machine={machine} />
        </>
      )}

      <ReleaseDialog name={releasing ? machine?.name : undefined} onOpenChange={setReleasing} />
    </MachinesPage>
  );
}
