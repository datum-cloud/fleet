import { Eyebrow } from './fact';
import { Dot } from './machine-status';
import type { CatalogOffering, CatalogProfile } from '../types';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { ArrowRightIcon, CpuIcon, WarehouseIcon } from 'lucide-react';

/** One profile a project can deploy, in the class that offers it. */
export interface DeployOption {
  className: string;
  profile: CatalogProfile;
}

export const optionKey = (o: Pick<DeployOption, 'className'> & { profile: { name: string } }) =>
  `${o.className}/${o.profile.name}`;

/** Every published profile of every available class, in catalog order. */
export function deployOptions(offerings: CatalogOffering[] | undefined): DeployOption[] {
  return (offerings ?? [])
    .filter((o) => o.available)
    .flatMap((o) =>
      o.profiles
        .filter((p) => p.published)
        .map((profile) => ({ className: o.className, profile }))
    );
}

// summarizeProfile renders CPU as "<cores> · <architecture>"; they read better
// as separate chips.
export function specChips(profile: CatalogProfile): string[] {
  const i = profile.cpu.lastIndexOf(' · ');
  const cpu = i < 0 ? [profile.cpu] : [profile.cpu.slice(0, i), profile.cpu.slice(i + 3)];
  return [cpu[0], profile.memory, ...cpu.slice(1), ...(profile.gpus ? [`${profile.gpus} GPU`] : [])];
}

function Chip({ children }: { children: React.ReactNode }) {
  return (
    <span className="bg-muted text-muted-foreground rounded px-1.5 py-0.5 font-mono text-xs">
      {children}
    </span>
  );
}

// The whole card is the button: it opens the deploy dialog with this class
// and profile chosen.
export function Chips({ chips }: { chips: string[] }) {
  return (
    <span className="flex flex-wrap gap-1.5">
      {chips.map((chip) => (
        <Chip key={chip}>{chip}</Chip>
      ))}
    </span>
  );
}

export function ProfileCard({
  option,
  onDeploy,
}: {
  option: DeployOption;
  onDeploy: (option: DeployOption) => void;
}) {
  const { className, profile } = option;
  return (
    <button
      type="button"
      data-testid="fleet-plugin-profile-card"
      aria-label={`Deploy ${profile.name} from ${className}`}
      onClick={() => onDeploy(option)}
      className="border-border bg-card hover:bg-muted flex h-full w-full flex-col gap-3 rounded-lg border p-4 text-left transition-colors">
      <span className="flex items-center gap-2">
        <Icon icon={CpuIcon} size={16} className="fleet-accent shrink-0" />
        <span className="min-w-0 flex-1 truncate font-mono text-xs font-semibold">{profile.name}</span>
      </span>
      <Chips chips={specChips(profile)} />
      <span className="flex items-center gap-2 text-xs">
        <span className="text-muted-foreground min-w-0 flex-1 truncate">{className}</span>
        <span className="fleet-accent flex shrink-0 items-center gap-1 font-medium">
          Deploy
          <Icon icon={ArrowRightIcon} size={13} />
        </span>
      </span>
    </button>
  );
}

// A class that exists but can't be deployed from yet: one muted, disabled card
// in the grid, so a missing publication isn't silent. The reason is in the
// tooltip and the accessible name; Fleet's condition message is never shown.
export function UnavailableCard({ offering }: { offering: CatalogOffering }) {
  const word = offering.failed ? 'Unavailable' : 'Setting up';
  const reason = offering.failed
    ? `${offering.className} can't be deployed from.`
    : `${offering.className} isn't ready to deploy from yet.`;
  return (
    <Tooltip message={reason}>
      <div
        role="group"
        aria-disabled="true"
        aria-label={reason}
        data-testid="fleet-plugin-offering-unavailable"
        className="border-border text-muted-foreground flex h-full w-full cursor-default flex-col gap-3 rounded-lg border border-dashed p-4 text-xs">
        <span className="flex items-center gap-2">
          <Icon icon={CpuIcon} size={16} className="shrink-0" />
          <span className="min-w-0 flex-1 truncate font-mono font-semibold">{offering.className}</span>
        </span>
        <span className="flex items-center gap-2">
          <Dot tone={offering.failed ? 'danger' : 'warning'} />
          {word}
        </span>
      </div>
    </Tooltip>
  );
}

/** Profile cards for every deployable profile, then a card per unavailable class. */
export function ProfileGrid({
  offerings,
  onDeploy,
}: {
  offerings: CatalogOffering[];
  onDeploy: (option: DeployOption) => void;
}) {
  const options = deployOptions(offerings);
  const blocked = offerings.filter((o) => !o.available);
  if (options.length === 0 && blocked.length === 0) return null;
  return (
    <div className="fleet-profile-grid">
      {options.map((option) => (
        <ProfileCard key={optionKey(option)} option={option} onDeploy={onDeploy} />
      ))}
      {blocked.map((o) => (
        <UnavailableCard key={o.className} offering={o} />
      ))}
    </div>
  );
}

// Under the machine list: what else this project can deploy. Takes the place
// of the old Catalog tab.
export function AvailableProfiles({
  offerings,
  onDeploy,
}: {
  offerings: CatalogOffering[];
  onDeploy: (option: DeployOption) => void;
}) {
  if (offerings.length === 0) return null;
  return (
    <section className="flex flex-col gap-3 pt-2" data-testid="fleet-plugin-available">
      <div className="flex items-center gap-2">
        <Icon icon={WarehouseIcon} size={13} className="text-muted-foreground" />
        <Eyebrow>Available to deploy</Eyebrow>
      </div>
      <ProfileGrid offerings={offerings} onDeploy={onDeploy} />
    </section>
  );
}
