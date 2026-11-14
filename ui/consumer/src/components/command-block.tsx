import { useCopyToClipboard } from '@datum-cloud/datum-ui/hooks';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { toast } from '@datum-cloud/datum-ui/toast';
import { cn } from '@datum-cloud/datum-ui/utils';
import { CheckIcon, CopyIcon } from 'lucide-react';
import { useState } from 'react';

// Copied from network-services-operator's consumer plugin (cli-section.tsx).
export function CommandBlock({ value, className }: { value: string; className?: string }) {
  const [, copy] = useCopyToClipboard();
  const [copied, setCopied] = useState(false);

  const handleCopy = () => {
    copy(value).then(() => {
      toast.success('Copied to clipboard');
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  return (
    <div className={cn('bg-background flex items-center gap-3 rounded-lg border px-3 sm:px-4', className)}>
      <span className="text-foreground min-w-0 flex-1 font-mono text-xs leading-relaxed break-all sm:text-sm">
        <span className="text-muted-foreground mr-2">$</span>
        {value}
      </span>
      <button
        type="button"
        onClick={handleCopy}
        className="text-muted-foreground hover:text-foreground shrink-0 transition-colors"
        aria-label="Copy command">
        <Icon icon={copied ? CheckIcon : CopyIcon} size={16} />
      </button>
    </div>
  );
}
