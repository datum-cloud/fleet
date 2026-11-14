import { DeployDialog } from '../components/deploy-dialog';
import { AvailableProfiles, deployOptions, optionKey, type DeployOption } from '../components/deploy-options';
import { MachineTable } from '../components/machine-table';
import { MachinesEmptyState } from '../components/machines-empty-state';
import { MachinesPage } from '../components/machines-page';
import { ReleaseDialog } from '../components/release-dialog';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { useCatalog, useMachines } from '../lib/api';
import type { Machine } from '../types';
import { Button } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { PlusIcon } from 'lucide-react';
import { useCallback, useState } from 'react';
import { useParams } from 'react-router';

// The dialog is closed, open with no profile chosen, or open on a profile card's.
type DeployState = { preset?: string } | undefined;

export default function MachineList() {
  const { projectId } = useParams<{ projectId: string; serviceSlug: string }>();
  const { data: machines, isLoading, error, refetch } = useMachines(projectId);
  // Only deploying needs the catalog, so a failed read leaves the list alone.
  const { data: offerings } = useCatalog(projectId);
  const options = deployOptions(offerings);
  const isEmpty = !isLoading && !error && (machines?.length ?? 0) === 0;
  const [releasing, setReleasing] = useState<string>();
  const [deploy, setDeploy] = useState<DeployState>();
  const startRelease = useCallback((machine: Machine) => setReleasing(machine.name), []);
  const startDeploy = (option?: DeployOption) => setDeploy({ preset: option && optionKey(option) });

  return (
    <MachinesPage
      testId="fleet-plugin-machine-list"
      title="Machines"
      description="Dedicated bare-metal servers, allocated to this project."
      crumbs={[{ label: 'Machines' }]}
      actions={
        machines &&
        machines.length > 0 && (
          <Button
            type="primary"
            theme="solid"
            size="small"
            htmlType="button"
            className="w-full sm:w-auto"
            disabled={options.length === 0}
            onClick={() => startDeploy()}>
            <Icon icon={PlusIcon} className="size-4" />
            Deploy machine
          </Button>
        )
      }>
      {isLoading && <LoadingState />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this project's machines."
          onRetry={() => void refetch()}
        />
      )}

      {isEmpty && <MachinesEmptyState offerings={offerings} onDeploy={startDeploy} />}

      {!isLoading && !error && machines && machines.length > 0 && (
        <>
          <MachineTable machines={machines} onRelease={startRelease} />
          {offerings && <AvailableProfiles offerings={offerings} onDeploy={startDeploy} />}
        </>
      )}

      <DeployDialog
        open={!!deploy}
        options={options}
        preset={deploy?.preset}
        onOpenChange={(open) => !open && setDeploy(undefined)}
      />
      <ReleaseDialog name={releasing} onOpenChange={(open) => !open && setReleasing(undefined)} />
    </MachinesPage>
  );
}
