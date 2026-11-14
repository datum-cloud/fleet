import { ApiError } from '../lib/api';
import { Button } from '@datum-cloud/datum-ui/button';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import { Text } from '@datum-cloud/datum-ui/typography';
import { LockIcon, ServerCrashIcon } from 'lucide-react';

export function LoadingState() {
  return (
    <div
      className="flex flex-col gap-3"
      aria-busy="true"
      aria-label="Loading"
      data-testid="fleet-plugin-loading">
      <Skeleton className="h-9 w-full sm:max-w-xs" />
      {Array.from({ length: 4 }, (_, i) => (
        <Skeleton key={i} className="h-10 w-full" />
      ))}
    </div>
  );
}

export function RestrictedState({ message }: { message: string }) {
  return (
    <Card className="max-w-md" data-testid="fleet-plugin-restricted">
      <CardContent className="flex flex-col items-start gap-3">
        <Icon icon={LockIcon} size={24} className="text-muted-foreground" />
        <div>
          <Text as="p" weight="semibold">Access restricted</Text>
          <Text as="p" textColor="muted" className="mt-1">{message}</Text>
        </div>
      </CardContent>
    </Card>
  );
}

export function ErrorState({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const message = error instanceof Error ? error.message : 'An unknown error occurred';
  return (
    <Card className="max-w-md" data-testid="fleet-plugin-error">
      <CardContent className="flex flex-col items-start gap-3">
        <Icon icon={ServerCrashIcon} size={24} className="text-destructive" />
        <div>
          <Text as="p" weight="semibold">Failed to load</Text>
          <Text as="p" textColor="muted" className="mt-1">{message}</Text>
        </div>
        <Button type="secondary" theme="outline" size="small" htmlType="button" onClick={onRetry}>
          Retry
        </Button>
      </CardContent>
    </Card>
  );
}

export function ErrorOrRestrictedState({
  error,
  restrictedMessage,
  onRetry,
}: {
  error: unknown;
  restrictedMessage: string;
  onRetry: () => void;
}) {
  if (error instanceof ApiError && error.status === 403) {
    return <RestrictedState message={restrictedMessage} />;
  }
  return <ErrorState error={error} onRetry={onRetry} />;
}
