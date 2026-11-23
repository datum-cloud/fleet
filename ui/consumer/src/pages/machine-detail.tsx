import { Fact, Facts } from '../components/fact';
import { Dot, MachineStatusChip } from '../components/machine-status';
import { MachinesPage, usePluginBase } from '../components/machines-page';
import { RELEASE_ICON, ReleaseDialog } from '../components/release-dialog';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { ApiError, useMachine } from '../lib/api';
import { machineSteps, type MachineDetail as MachineDetailModel, type MachineStep } from '../types';
import { Button } from '@datum-cloud/datum-ui/button';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { DateTime } from '@datum-cloud/datum-ui/date-time';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import {
  ActivityIcon,
  ArrowRightIcon,
  CheckIcon,
  CpuIcon,
  FileTextIcon,
  HourglassIcon,
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

// A short muted word, with the explanation in its tooltip and accessible name.
function Word({ word, reason, tone }: { word: string; reason?: string; tone?: 'warning' }) {
  return (
    <Tooltip message={reason} hidden={!reason}>
      <span
        className={tone === 'warning' ? 'fleet-warn cursor-default' : 'text-muted-foreground cursor-default'}
        aria-label={reason ? `${word}: ${reason}` : undefined}>
        {word}
      </span>
    </Tooltip>
  );
}

function RequestCard({ machine }: { machine: MachineDetailModel }) {
  return (
    <FlowCard icon={FileTextIcon} title="You asked for" testId="fleet-plugin-machine-request">
      <Facts>
        <Fact label="Class" mono>{machine.machineClass}</Fact>
        <Fact label="Profile" mono>{machine.machineProfile}</Fact>
        <Fact label="Location">{machine.location}</Fact>
      </Facts>
    </FlowCard>
  );
}

function unassigned(machine: MachineDetailModel): { word: string; reason: string } {
  if (machine.phase === 'Rejected') return { word: 'None', reason: "The request wasn't accepted." };
  if (machine.waitingForCapacity) {
    return { word: 'Waiting', reason: 'No free server matches this profile yet. One is assigned as soon as capacity frees up.' };
  }
  return { word: 'Pending', reason: 'A server is assigned when one matching the profile is free.' };
}

// Until a server is assigned there's no hardware to show: skeleton lines and
// one word, never zeros.
function HardwareCard({ machine }: { machine: MachineDetailModel }) {
  if (!machine.hardware) {
    const { word, reason } = unassigned(machine);
    return (
      <FlowCard icon={CpuIcon} title="Hardware" testId="fleet-plugin-machine-hardware">
        <div className="flex flex-col gap-2.5" aria-hidden>
          <Skeleton className="h-2.5 w-44 rounded" />
          <Skeleton className="h-2.5 w-60 rounded" />
          <Skeleton className="h-2.5 w-36 rounded" />
        </div>
        <span className="text-sm">
          <Word word={word} reason={reason} />
        </span>
      </FlowCard>
    );
  }
  const profile = machine.acceptedProfile;
  return (
    <FlowCard icon={CpuIcon} title="Hardware" testId="fleet-plugin-machine-hardware">
      <Facts>
        {profile && profile !== machine.machineProfile && (
          <Fact label="Profile" mono>
            <Word word={profile} reason={`Differs from the requested ${machine.machineProfile}`} tone="warning" />
          </Fact>
        )}
        {machine.hardware.map(({ label, value }) => (
          <Fact key={label} label={label} mono>{value}</Fact>
        ))}
      </Facts>
    </FlowCard>
  );
}

// The address is the main fact; the network's name follows it.
function NetworkCard({ machine }: { machine: MachineDetailModel }) {
  return (
    <FlowCard icon={NetworkIcon} title="Network" testId="fleet-plugin-machine-network">
      <Facts>
        {machine.interfaces.length === 0 ? (
          <Fact label="Interfaces">None requested</Fact>
        ) : (
          machine.interfaces.map((i) => (
            <div key={i.name} className="flex min-w-0 flex-col gap-0.5" data-testid={`fleet-plugin-interface-${i.name}`}>
              <dt className="fleet-fact-label text-muted-foreground flex items-center gap-1.5">
                {i.ready !== 'True' && (
                  <Tooltip message="Not ready">
                    <span className="inline-flex cursor-default" role="img" aria-label="Not ready">
                      <Dot tone="warning" />
                    </span>
                  </Tooltip>
                )}
                {i.name}
              </dt>
              <dd className="flex min-w-0 flex-col gap-0.5 text-sm">
                {i.addresses.length > 0 ? (
                  i.addresses.map((a) => (
                    <span key={a} className="font-mono text-xs break-all select-all">{a}</span>
                  ))
                ) : (
                  <span className="text-muted-foreground text-xs">No address</span>
                )}
                <span className="text-muted-foreground text-xs wrap-break-word">{i.network}</span>
              </dd>
            </div>
          ))
        )}
      </Facts>
    </FlowCard>
  );
}

function Overview({ machine }: { machine: MachineDetailModel }) {
  const reported = machine.powerObserved && machine.hardwareObservedAt && (
    <span>
      Power reported <When date={machine.hardwareObservedAt} />
    </span>
  );
  return (
    <section
      className="fleet-band flex flex-col gap-4 rounded-xl px-5 pt-5 pb-6 md:px-7"
      data-testid="fleet-plugin-machine-overview">
      <div className="flex justify-end">
        <MachineStatusChip machine={machine} power tooltip={reported || undefined} />
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
  return (
    <Card size="sm" data-testid="fleet-plugin-machine-progress">
      <CardContent className="flex flex-col gap-5">
        <span className="flex items-center gap-2">
          <Icon icon={ActivityIcon} size={14} className="text-muted-foreground" />
          <span className="text-sm font-semibold">Progress</span>
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
                <span className="text-sm font-medium wrap-break-word">{step.label}</span>
                {step.detail && (
                  <Tooltip message={step.detailTooltip} hidden={!step.detailTooltip}>
                    <span className="text-muted-foreground min-w-0 cursor-default text-xs wrap-break-word">
                      {step.detail}
                    </span>
                  </Tooltip>
                )}
                {step.at && (
                  <span className="text-muted-foreground font-mono text-xs">
                    <When date={step.at} />
                  </span>
                )}
              </li>
            );
          })}
        </ol>
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
        machine &&
        (machine.deletingSince || machine.createdAt) && (
          <span className="text-muted-foreground text-sm">
            {machine.deletingSince ? (
              <>
                Releasing since <When date={machine.deletingSince} />
              </>
            ) : (
              <>
                Created <When date={machine.createdAt} />
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
            icon={<Icon icon={RELEASE_ICON} size={12} />}
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
          errorTitle="Couldn't load this machine"
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
