import { ConfirmDeleteDialog } from './confirm-delete-dialog';
import { ApiError, useReleaseMachine } from '../lib/api';
import { Button } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { toast } from '@datum-cloud/datum-ui/toast';
import { Trash2Icon } from 'lucide-react';

/** The one icon Release uses, in the list row and on the machine's page. */
export const RELEASE_ICON = Trash2Icon;
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
      <Icon icon={RELEASE_ICON} className="size-4" />
    </Button>
  );
}

// Releasing deletes the machine after the user types its name. The machine stays,
// listed as Releasing, until Fleet finishes. Errors go to a toast, without the
// API's text, and the dialog stays open to retry.
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
        toast.error("You don't have permission to release machines in this project.");
      } else {
        toast.error("Couldn't release the machine.");
      }
      throw error;
    }
  };

  return (
    <ConfirmDeleteDialog
      open={!!name}
      onOpenChange={onOpenChange}
      title={`Release ${name ?? ''}?`}
      description="You lose the server and the data on it. This can't be undone."
      confirmLabel="Release"
      confirmValue={name ?? ''}
      onConfirm={release}
    />
  );
}
