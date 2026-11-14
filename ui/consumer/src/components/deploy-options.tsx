import { Eyebrow } from './fact';
import { Dot } from './machine-status';
import type { CatalogOffering, CatalogProfile } from '../types';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Text } from '@datum-cloud/datum-ui/typography';
import { ArrowRightIcon, CpuIcon, WarehouseIcon } from 'lucide-react';

/** One profile a project can deploy, in the class that offers it. */
export interface DeployOption {
  className: string;
  /** "datum-cloud/central-metal" for a class published from another project. */
  source?: string;
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
        .map((profile) => ({ className: o.className, source: o.source, profile }))
    );
}

// summarizeProfile renders CPU as "<cores> · <architecture>"; they read better
// as separate chips.
function specChips(profile: CatalogProfile): string[] {
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
export function ProfileCard({
  option,
  onDeploy,
}: {
  option: DeployOption;
  onDeploy: (option: DeployOption) => void;
}) {
  const { className, source, profile } = option;
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
      <span className="flex flex-wrap gap-1.5">
        {specChips(profile).map((chip) => (
          <Chip key={chip}>{chip}</Chip>
        ))}
      </span>
      <span className="flex items-center gap-2 text-xs">
        <span className="text-muted-foreground min-w-0 flex-1 truncate">
          {className}
          {source && ` · from ${source.split('/')[0]}`}
        </span>
        <span className="fleet-accent flex shrink-0 items-center gap-1 font-medium">
          Deploy
          <Icon icon={ArrowRightIcon} size={13} />
        </span>
      </span>
    </button>
  );
}

// Classes that exist but can't be deployed from yet: say why, so a missing
// publication isn't silent.
export function UnavailableNotes({ offerings }: { offerings: CatalogOffering[] }) {
  const blocked = offerings.filter((o) => !o.available);
  if (blocked.length === 0) return null;
  return (
    <ul className="flex flex-col gap-1.5">
      {blocked.map((o) => (
        <li
          key={o.className}
          className="text-muted-foreground flex items-center gap-2 text-sm"
          data-testid="fleet-plugin-offering-unavailable">
          <Dot tone={o.failed ? 'danger' : 'warning'} />
          <span className="text-foreground font-medium">{o.className}</span>
          {o.failed ? 'is unavailable' : 'is still being set up'}
          {o.unavailableReason && `: ${o.unavailableReason}`}
        </li>
      ))}
    </ul>
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
  const options = deployOptions(offerings);
  if (offerings.length === 0) return null;
  return (
    <section className="flex flex-col gap-3 pt-2" data-testid="fleet-plugin-available">
      <div className="flex flex-wrap items-center gap-2">
        <Icon icon={WarehouseIcon} size={13} className="text-muted-foreground" />
        <Eyebrow>Available to deploy</Eyebrow>
        <Text size="xs" textColor="muted">
          Profiles from the machine classes this project can use
        </Text>
      </div>
      {options.length > 0 && (
        <div className="fleet-profile-grid">
          {options.map((option) => (
            <ProfileCard key={optionKey(option)} option={option} onDeploy={onDeploy} />
          ))}
        </div>
      )}
      <UnavailableNotes offerings={offerings} />
    </section>
  );
}
