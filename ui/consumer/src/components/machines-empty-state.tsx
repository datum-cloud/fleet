import { ProfileGrid, deployOptions, type DeployOption } from './deploy-options';
import { Eyebrow } from './fact';
import type { CatalogOffering } from '../types';
import { Button } from '@datum-cloud/datum-ui/button';
import { Card } from '@datum-cloud/datum-ui/card';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { PlusIcon, ServerIcon } from 'lucide-react';
import { useId } from 'react';

// Shown while the project has no Machines: the hero line, the Deploy button
// and the profiles on offer.
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

  return (
    <Card className="flex flex-col items-center gap-6 px-6 py-10 text-center sm:px-10" data-testid="fleet-plugin-machine-empty">
      <span className="bg-muted fleet-accent flex size-14 items-center justify-center rounded-xl">
        <Icon icon={ServerIcon} size={24} />
      </span>

      <h2 className="font-title text-3xl leading-tight">Run on dedicated bare metal</h2>

      <Button
        type="primary"
        theme="solid"
        htmlType="button"
        disabled={options.length === 0}
        onClick={() => onDeploy()}>
        <Icon icon={PlusIcon} className="size-4" />
        Deploy machine
      </Button>

      {offerings && (
        <section
          aria-labelledby={startersId}
          className="border-border flex w-full flex-col gap-3 border-t pt-6 text-left">
          <Eyebrow className="text-foreground">
            <span id={startersId}>Start from a profile</span>
          </Eyebrow>
          {offerings.length > 0 ? (
            <ProfileGrid offerings={offerings} onDeploy={onDeploy} />
          ) : (
            <p className="text-muted-foreground text-sm" data-testid="fleet-plugin-no-offerings">
              No machine classes are available to this project yet.
            </p>
          )}
        </section>
      )}
    </Card>
  );
}
