import { Link, useParams } from 'react-router';
import { Button } from '@datum-cloud/datum-ui/button';

export function InventoryAccessState({ status, onRetry }: { status: number; onRetry?: () => void }) {
  const { slug } = useParams();
  const title = status === 401 ? 'Your session has expired' : status === 403 ? 'Inventory access needed' : status === 404 ? 'Inventory section unavailable' : 'Inventory is temporarily unavailable';
  const message = status === 401
    ? 'Sign in again through the staff portal, then return to inventory.'
    : status === 403
      ? 'Ask your administrator for inventory viewing access in the provider project. Staff portal access alone does not grant inventory access.'
      : status === 404
        ? 'This inventory API is not available here. Your administrator can check the provider project configuration.'
        : 'We could not load this section. Other inventory sections may still be available. Please try again shortly.';
  return <section className="flex flex-col gap-4 rounded-lg border p-6" aria-label={title} data-testid={`inventory-access-${status}`}>
    <h2 className="text-lg font-semibold">{title}</h2>
    <p className="text-muted-foreground">{message}</p>
    <div className="flex flex-wrap items-center gap-4">
      <Link className="underline underline-offset-4" to={slug ? `/plugins/${slug}` : '/'}>{slug ? 'Back to Bare Metal' : 'Back to staff portal'}</Link>
      {status === 401 && slug && <Link className="underline underline-offset-4" to="/">Return to staff portal</Link>}
      {![401, 403, 404].includes(status) && onRetry && <Button type="secondary" theme="outline" size="small" htmlType="button" onClick={onRetry}>Try again</Button>}
    </div>
  </section>;
}
