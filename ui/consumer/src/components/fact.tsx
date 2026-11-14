import { cn } from '@datum-cloud/datum-ui/utils';

export function Eyebrow({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn('fleet-eyebrow text-muted-foreground', className)}>{children}</span>;
}

/** A small uppercase label over its value, as on the NLB overview. */
export function Fact({
  label,
  mono,
  children,
}: {
  label: string;
  mono?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <dt className="fleet-fact-label text-muted-foreground">{label}</dt>
      <dd className={cn('min-w-0 text-sm wrap-break-word', mono && 'font-mono text-xs')}>{children}</dd>
    </div>
  );
}

export function Facts({ children }: { children: React.ReactNode }) {
  return <dl className="flex flex-col gap-3">{children}</dl>;
}
