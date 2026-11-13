import { CommandBlock } from './command-block';
import { ProfileCard, UnavailableNotes, deployOptions, optionKey, type DeployOption } from './deploy-options';
import { Eyebrow } from './fact';
import { Dot } from './machine-status';
import type { CatalogOffering } from '../types';
import { DEFAULT_LOCATION } from '../lib/api';
import { Button } from '@datum-cloud/datum-ui/button';
import { Card } from '@datum-cloud/datum-ui/card';
import { Icon } from '@datum-cloud/datum-ui/icons';
import {
  ArrowRightIcon,
  BoxIcon,
  CheckIcon,
  CircleCheckIcon,
  LayersIcon,
  LinkIcon,
  PlusIcon,
  ServerIcon,
  type LucideIcon,
} from 'lucide-react';
import { useId } from 'react';

const MACHINE_FITS = [
  'You need the whole server: no neighbours, no hypervisor',
  'You run your own OS install or PXE tooling',
  'The hardware matters: cores, memory, GPUs',
];

const COMPUTE_FITS = [
  "You want to ship a container, not look after a server",
  'Scaling up and down should happen on its own',
  "You'd rather Datum handle the operating system",
];

function Lifecycle() {
  const steps = [
    { icon: LayersIcon, title: 'Profile', detail: '24 cores · 128Gi' },
    { icon: LinkIcon, title: 'Bound', detail: 'a matching server' },
    { icon: CircleCheckIcon, title: 'Ready', detail: 'powered off, yours' },
  ];
  return (
    <ol aria-label="How a machine is deployed" className="flex flex-col items-center gap-2 sm:flex-row">
      {steps.map((step, i) => (
        <li key={step.title} className="flex flex-col items-center gap-2 sm:flex-row">
          {i > 0 && (
            <Icon icon={ArrowRightIcon} size={16} className="text-muted-foreground hidden sm:block" />
          )}
          <span className="border-border bg-background flex items-center gap-3 rounded-lg border px-4 py-2 text-left">
            <Icon icon={step.icon} size={18} className="text-muted-foreground" />
            <span>
              <span className="block text-sm font-medium">{step.title}</span>
              <span className="text-muted-foreground block font-mono text-xs">{step.detail}</span>
            </span>
          </span>
        </li>
      ))}
    </ol>
  );
}

function ComparisonCard({ icon, title, tag, fits }: { icon: LucideIcon; title: string; tag: string; fits: string[] }) {
  return (
    <Card className="flex flex-col gap-3 p-6">
      <div className="flex items-center gap-2">
        <Icon icon={icon} size={16} className="text-muted-foreground" />
        <h3 className="text-sm font-semibold">{title}</h3>
        <span className="text-muted-foreground font-mono text-xs">{tag}</span>
      </div>
      <ul className="flex flex-col gap-2">
        {fits.map((fit) => (
          <li key={fit} className="text-muted-foreground flex items-start gap-2 text-sm">
            <Icon icon={CheckIcon} size={14} className="mt-0.5 shrink-0 text-green-600" />
            {fit}
          </li>
        ))}
      </ul>
    </Card>
  );
}

// Shown while the project has no Machines, after the NLB plugin's empty
// state: what a Machine is, how to get one, and the profiles on offer.
export function MachinesEmptyState({
  offerings,
  onDeploy,
}: {
  /** Undefined while the catalog loads or if it failed; the hero still renders. */
  offerings: CatalogOffering[] | undefined;
  onDeploy: (option?: DeployOption) => void;
}) {
  const startersId = useId();
  const options = deployOptions(offerings);
  const classes = [...new Set(options.map((o) => o.className))];

  return (
    <div className="flex flex-col gap-6" data-testid="fleet-plugin-machine-empty">
      <Card className="flex flex-col items-center gap-6 px-6 py-10 text-center sm:px-10">
        <span className="bg-muted fleet-accent flex size-14 items-center justify-center rounded-xl">
          <Icon icon={ServerIcon} size={24} />
        </span>

        <div className="flex max-w-2xl flex-col gap-3">
          <h2 className="font-title text-3xl leading-tight">Run on dedicated bare metal</h2>
          <p className="text-muted-foreground text-sm leading-relaxed">
            A machine is a whole physical server, reserved for this project until you release it.
            Pick a profile and we will bind a server that matches it. Nothing is shared with other
            customers.
          </p>
        </div>

        <Lifecycle />

        <div className="flex w-full max-w-xl flex-col items-stretch gap-3 sm:flex-row">
          <Button
            type="primary"
            theme="solid"
            htmlType="button"
            className="shrink-0"
            disabled={options.length === 0}
            onClick={() => onDeploy()}>
            <Icon icon={PlusIcon} className="size-4" />
            Deploy machine
          </Button>
          <div className="flex min-w-0 flex-1 text-left">
            <CommandBlock value="datumctl apply -f machine.yaml" className="w-full py-2" />
          </div>
        </div>

        {classes.length > 0 && (
          <p className="text-muted-foreground flex items-center gap-2 text-sm">
            <Dot tone="success" />
            {classes.join(', ')} {classes.length === 1 ? 'is' : 'are'} available in {DEFAULT_LOCATION}
            <span className="mx-1">·</span>OS install coming soon
          </p>
        )}

        <section
          aria-labelledby={startersId}
          className="border-border flex w-full flex-col gap-3 border-t pt-6 text-left">
          <p className="text-xs">
            <Eyebrow className="text-foreground">
              <span id={startersId}>Start from a profile</span>
            </Eyebrow>
            <span className="text-muted-foreground ml-2">Choose one and give your machine a name.</span>
          </p>
          {options.length > 0 ? (
            <div className="fleet-profile-grid">
              {options.map((option) => (
                <ProfileCard key={optionKey(option)} option={option} onDeploy={onDeploy} />
              ))}
            </div>
          ) : (
            offerings && (
              <p className="text-muted-foreground text-sm" data-testid="fleet-plugin-no-offerings">
                No machine classes are available to this project yet.
              </p>
            )
          )}
          {offerings && <UnavailableNotes offerings={offerings} />}
        </section>
      </Card>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <ComparisonCard icon={ServerIcon} title="Use a machine when" tag="bare metal" fits={MACHINE_FITS} />
        <ComparisonCard icon={BoxIcon} title="Use Compute when" tag="containers" fits={COMPUTE_FITS} />
      </div>
    </div>
  );
}
