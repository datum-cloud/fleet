import { ConfirmDeleteDialog } from './confirm-delete-dialog';
import { ApiError, useReleaseMachine } from '../lib/api';
import { Button } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { toast } from '@datum-cloud/datum-ui/toast';
import { Trash2Icon } from 'lucide-react';
import { useParams } from 'react-router';

/** The row button that opens a ReleaseDialog. */
export function ReleaseButton({ name, onClick }: { name: string; onClick: () => void }) {
  return (
    <Button
      type="quaternary"
      theme="borderless"
      size="small"
      htmlType="button"
      aria-label={`Release ${name}`}
      onClick={onClick}>
      <Icon icon={Trash2Icon} className="size-4" />
    </Button>
  );
}

// Releasing deletes the machine after the user types its name. The machine stays,
// listed as Releasing, until Fleet frees its capacity. Errors go to a toast and
// the dialog stays open to retry.
export function ReleaseDialog({
  name,
  onOpenChange,
}: {
  /** The machine to release; the dialog is closed when absent. */
  name: string | undefined;
  onOpenChange: (open: boolean) => void;
}) {
  const { projectId } = useParams<{ projectId: string }>();
  const mutation = useReleaseMachine(projectId);

  const release = async () => {
    if (!name) return;
    try {
      await mutation.mutateAsync(name);
      toast.success(`Releasing machine ${name}`);
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) {
        toast.error("You don't have permission to release machines in this project.", {
          description: error.message,
        });
      } else {
        toast.error(error instanceof Error ? error.message : 'Release failed.');
      }
      throw error;
    }
  };

  return (
    <ConfirmDeleteDialog
      open={!!name}
      onOpenChange={onOpenChange}
      title={`Release ${name ?? ''}?`}
      description="The machine returns to the pool. This can't be undone."
      confirmLabel="Release"
      confirmValue={name ?? ''}
      onConfirm={release}
    />
  );
}
