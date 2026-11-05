import { StatusBadge } from './status-badge';
import type { CatalogOffering, CatalogProfile } from '../types';
import { Button } from '@datum-cloud/datum-ui/button';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';

function Spec({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span>{value}</span>
    </div>
  );
}

function ProfileCard({
  profile,
  onDeploy,
}: {
  profile: CatalogProfile;
  onDeploy: (profile: CatalogProfile) => void;
}) {
  return (
    <Card data-testid="fleet-plugin-profile-card">
      <CardContent className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-2">
          <p className="text-sm font-semibold break-all">{profile.name}</p>
          {!profile.published && <StatusBadge type="muted">Not published</StatusBadge>}
        </div>
        {profile.published && (
          <div className="flex flex-col gap-1">
            <Spec label="CPU" value={profile.cpu} />
            <Spec label="Memory" value={profile.memory} />
            <Spec label="GPUs" value={profile.gpus ?? 'None'} />
          </div>
        )}
        {profile.published && (
          <Button
            type="primary"
            theme="solid"
            size="small"
            htmlType="button"
            className="w-fit"
            onClick={() => onDeploy(profile)}>
            Deploy
          </Button>
        )}
      </CardContent>
    </Card>
  );
}

// One class: its name, where it comes from, and the profiles it allows.
export function OfferingSection({
  offering,
  onDeploy,
}: {
  offering: CatalogOffering;
  onDeploy: (className: string, profile: CatalogProfile) => void;
}) {
  return (
    <section data-testid="fleet-plugin-offering" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-base font-semibold">{offering.className}</h2>
        {offering.available ? (
          <StatusBadge type="success">Available</StatusBadge>
        ) : (
          <StatusBadge type="warning" pending>
            Pending
          </StatusBadge>
        )}
        {offering.source && (
          <span className="text-muted-foreground text-sm">from {offering.source}</span>
        )}
      </div>

      {!offering.available && (
        <p className="text-muted-foreground text-sm">{offering.unavailableReason}</p>
      )}

      {offering.available && offering.profiles.length === 0 && (
        <p className="text-muted-foreground text-sm">This class allows no profiles yet.</p>
      )}

      {offering.available && offering.profiles.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2">
          {offering.profiles.map((profile) => (
            <ProfileCard
              key={profile.name}
              profile={profile}
              onDeploy={(p) => onDeploy(offering.className, p)}
            />
          ))}
        </div>
      )}
    </section>
  );
}
