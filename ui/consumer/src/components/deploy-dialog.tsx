import { optionKey, type DeployOption } from './deploy-options';
import { usePluginBase } from './machines-page';
import { ApiError, DEFAULT_LOCATION, useCreateMachine } from '../lib/api';
import { Form } from '@datum-cloud/datum-ui/form';
import { ConformAdapter } from '@datum-cloud/datum-ui/form/adapters/conform';
import { toast } from '@datum-cloud/datum-ui/toast';
import { Text } from '@datum-cloud/datum-ui/typography';
import { useNavigate, useParams } from 'react-router';
import { z } from 'zod';

const NAME_MESSAGE =
  'Lowercase letters, numbers, and hyphens only. Must start and end with a letter or number.';

export const deployFormSchema = z.object({
  option: z.string({ error: 'Choose a profile.' }).min(1, 'Choose a profile.'),
  name: z
    .string({ error: 'Name is required.' })
    .max(63, 'At most 63 characters.')
    .regex(/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/, NAME_MESSAGE),
});

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4 text-sm">
      <Text textColor="muted">{label}</Text>
      <Text className="text-right font-mono break-all">{value}</Text>
    </div>
  );
}

function Summary({ options }: { options: DeployOption[] }) {
  const key = Form.useWatch<string>('option');
  const option = options.find((o) => optionKey(o) === key);
  if (!option) return null;
  return (
    <div className="flex flex-col gap-1" data-testid="fleet-plugin-deploy-summary">
      <Row label="Class" value={option.className} />
      <Row label="CPU" value={option.profile.cpu} />
      <Row label="Memory" value={option.profile.memory} />
      <Row label="GPUs" value={option.profile.gpus ?? 'None'} />
      <Row label="Location" value={DEFAULT_LOCATION} />
    </div>
  );
}

// Deploying creates a Machine. It doesn't reserve capacity: the Machine
// starts Pending and Fleet binds capacity when some is free.
export function DeployDialog({
  open,
  options,
  preset,
  onOpenChange,
}: {
  open: boolean;
  options: DeployOption[];
  /** optionKey of the profile card that opened the dialog. */
  preset?: string;
  onOpenChange: (open: boolean) => void;
}) {
  const { projectId } = useParams<{ projectId: string }>();
  const base = usePluginBase();
  const navigate = useNavigate();
  const mutation = useCreateMachine(projectId);
  // One profile needs no choice.
  const initial = preset ?? (options.length === 1 ? optionKey(options[0]) : undefined);

  const handleSubmit = ({ name, option: key }: z.infer<typeof deployFormSchema>) => {
    const option = options.find((o) => optionKey(o) === key);
    if (!option) return;
    mutation.mutate(
      { name, machineClass: option.className, machineProfile: option.profile.name },
      {
        onSuccess: () => {
          toast.success(`Requested machine ${name}`);
          onOpenChange(false);
          navigate(`${base}/${encodeURIComponent(name)}`);
        },
        // A 403 is the common case (no consumer role); keep the API's
        // wording as the detail rather than the headline.
        onError: (error) => {
          if (error instanceof ApiError && error.status === 403) {
            toast.error("You don't have permission to deploy machines in this project.", {
              description: error.message,
            });
          } else {
            toast.error(error.message);
          }
        },
      }
    );
  };

  return (
    <ConformAdapter>
      <Form.Dialog
        key={initial ?? 'none'}
        open={open}
        onOpenChange={onOpenChange}
        title="Deploy a machine"
        description="Fleet binds a server matching the profile and hands it over powered off."
        schema={deployFormSchema}
        defaultValues={{ option: initial }}
        onSubmit={handleSubmit}
        loading={mutation.isPending}
        submitText="Deploy"
        submitTextLoading="Deploying..."
        formClassName="flex min-h-0 flex-col"
        className="w-full focus:ring-0 focus:outline-none sm:max-w-xl">
        <div className="space-y-5 px-5" data-testid="fleet-plugin-deploy-form">
          <Form.Field name="option" label="Profile" required>
            {options.length > 0 ? (
              <Form.Select placeholder="Choose a profile">
                {options.map((o) => (
                  <Form.SelectItem key={optionKey(o)} value={optionKey(o)}>
                    {o.profile.name} · {o.className}
                  </Form.SelectItem>
                ))}
              </Form.Select>
            ) : (
              <Text as="p" textColor="muted">
                No machine classes are available to this project yet.
              </Text>
            )}
          </Form.Field>
          <Summary options={options} />
          <Form.Field
            name="name"
            label="Name"
            description="Identifies the machine in this project. It can't be changed later."
            required>
            <Form.Input autoFocus placeholder="e.g. build-runner" />
          </Form.Field>
        </div>
      </Form.Dialog>
    </ConformAdapter>
  );
}
