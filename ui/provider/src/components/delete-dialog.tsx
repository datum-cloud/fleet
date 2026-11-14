import { Alert, AlertDescription } from '@datum-cloud/datum-ui/alert';
import { Button } from '@datum-cloud/datum-ui/button';
import { Form } from '@datum-cloud/datum-ui/form';
import { ConformAdapter } from '@datum-cloud/datum-ui/form/adapters/conform';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Trash2Icon } from 'lucide-react';
import type { ReactNode } from 'react';
import { z } from 'zod';

export function confirmSchema(name: string) {
  return z.object({
    confirm: z
      .string({ error: `Type ${name} to confirm.` })
      .refine((value) => value === name, `Type ${name} to confirm.`),
  });
}

/** The row button that opens a DeleteDialog. */
export function DeleteButton({ name, onClick }: { name: string; onClick: () => void }) {
  return (
    <Button
      type="quaternary"
      theme="borderless"
      size="small"
      htmlType="button"
      aria-label={`Delete ${name}`}
      onClick={onClick}>
      <Icon icon={Trash2Icon} className="size-4" />
    </Button>
  );
}

// Deleting is permanent and Fleet doesn't yet guard what still references the
// record, so the user types its name and the dialog says what breaks. Errors
// render in the dialog because staff-portal does not share its toast module.
export function DeleteDialog({
  name,
  resource,
  warning,
  pending,
  error,
  onConfirm,
  onOpenChange,
}: {
  /** The record to delete; the dialog is closed when absent. */
  name: string | undefined;
  /** e.g. "machine capacity", used in the title. */
  resource: string;
  warning: ReactNode;
  pending: boolean;
  error: Error | null;
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <ConformAdapter>
      <Form.Dialog
        // Remount per record so the typed name doesn't carry over.
        key={name}
        open={!!name}
        onOpenChange={onOpenChange}
        title={`Delete ${resource} ${name ?? ''}?`}
        description="This can't be undone."
        schema={confirmSchema(name ?? '')}
        onSubmit={onConfirm}
        loading={pending}
        submitType="danger"
        submitText="Delete"
        submitTextLoading="Deleting..."
        className="w-full focus:ring-0 focus:outline-none sm:max-w-lg">
        <div className="space-y-4 px-5" data-testid="fleet-plugin-delete-form">
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error.message}</AlertDescription>
            </Alert>
          )}
          <div className="text-muted-foreground text-sm">{warning}</div>
          <Form.Field name="confirm" label={`Type ${name ?? ''} to confirm`} required>
            <Form.Input autoComplete="off" />
          </Form.Field>
        </div>
      </Form.Dialog>
    </ConformAdapter>
  );
}
