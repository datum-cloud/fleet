import { Badge } from '@datum-cloud/datum-ui/badge';

export type BadgeType = 'success' | 'warning' | 'danger' | 'muted';

// Matches staff-portal's own status badges (STATUS_BADGE_CLASS and the
// BadgeState / BadgeCondition colors in app/components/badge). A plugin has no
// CSS build, so it may only use classes the host already ships; these are.
const STATUS_BADGE_CLASS = 'h-4 px-[5px] text-4xs leading-4 font-medium tracking-[0.3px] uppercase';

const TONE_CLASS: Record<BadgeType, string> = {
  success:
    'bg-green-100 text-green-800 border-green-200 dark:bg-green-900/20 dark:text-green-400 dark:border-green-800',
  danger:
    'bg-red-100 text-red-800 border-red-200 dark:bg-red-900/20 dark:text-red-400 dark:border-red-800',
  warning:
    'bg-amber-100 text-amber-800 border-amber-200 dark:bg-amber-900/20 dark:text-amber-400 dark:border-amber-800',
  muted:
    'bg-gray-100 text-gray-800 border-gray-200 dark:bg-gray-900/20 dark:text-gray-400 dark:border-gray-800',
};

export function StatusBadge({ type, children }: { type: BadgeType; children: React.ReactNode }) {
  return (
    <Badge className={`inline-flex w-fit items-center ${STATUS_BADGE_CLASS} ${TONE_CLASS[type]}`}>
      {children}
    </Badge>
  );
}
