import { PLUGIN_ID, PROVIDER_PROJECT, useMachineCapacities, useMachineClasses, useMachineProfiles } from '../lib/api';
import { Tabs, TabsLinkTrigger, TabsList } from '@datum-cloud/datum-ui/tabs';
import { QueryClientContext, skipToken, useQuery, type UseQueryResult } from '@tanstack/react-query';
import { useContext, type ReactNode } from 'react';
import { Link, useLocation, useParams } from 'react-router';
import './physical-inventory.css';

/** Lists shorter than this are scanned by eye, so they show no search box. */
export const SEARCH_MIN_ROWS = 10;

type Count = string | undefined;

// A tab count is the list's length, or "?" when the read failed; a list
// that has not loaded yet shows no count rather than a guess.
function count(query: Pick<UseQueryResult<unknown[] | undefined>, 'data' | 'error'>): Count {
  if (query.error) return '?';
  return query.data ? String(query.data.length) : undefined;
}

// Capacity, Profiles and Classes read all three lists, which React Query
// shares with the page itself.
function useLiveCounts(): Count[] {
  return [count(useMachineCapacities()), count(useMachineProfiles()), count(useMachineClasses())];
}

// Inventory pages only read what is already cached, so opening Inventory
// sends no Bare Metal requests of its own. They may render outside the
// plugin's QueryClient, and then show no counts.
function useCachedCounts(): Count[] {
  const capacities = useQuery<unknown[]>({ queryKey: [PLUGIN_ID, 'machinecapacities', PROVIDER_PROJECT], queryFn: skipToken });
  const profiles = useQuery<unknown[]>({ queryKey: [PLUGIN_ID, 'machineprofiles', PROVIDER_PROJECT], queryFn: skipToken });
  const classes = useQuery<unknown[]>({ queryKey: [PLUGIN_ID, 'machineclasses', PROVIDER_PROJECT], queryFn: skipToken });
  return [count(capacities), count(profiles), count(classes)];
}

function LiveTabs(props: TabProps) { return <FleetTabs {...props} counts={useLiveCounts()} />; }
function CachedTabs(props: TabProps) { return <FleetTabs {...props} counts={useCachedCounts()} />; }
function PassiveTabs(props: TabProps) { return useContext(QueryClientContext) ? <CachedTabs {...props} /> : <FleetTabs {...props} counts={[]} />; }

type TabProps = { tabs: { label: string; href: string }[]; active: string };

function FleetTabs({ tabs, active, counts }: TabProps & { counts: Count[] }) {
  return (
    <Tabs value={active} className="gap-0">
      <TabsList variant="line">
        {tabs.map((tab, index) => (
          <TabsLinkTrigger key={tab.href} value={tab.href} href={tab.href} linkComponent={Link} className="md:py-2">
            {tab.label}
            {counts[index] !== undefined && <span className="fleet-tab-count">{counts[index]}</span>}
          </TabsLinkTrigger>
        ))}
      </TabsList>
    </Tabs>
  );
}

// One "Bare Metal" nav item with tabs for each resource, so the Operations menu
// does not grow with every page. Every tab shares this header: the tab row
// sits first, with the page's action at its right end, so switching tabs
// never moves it. staff-portal adds no padding around plugin pages, so this
// pads itself like the host's pages (p-6).
export function FleetPage({
  testId,
  live = false,
  actions,
  children,
}: {
  testId: string;
  /** Capacity, Profiles and Classes: load every list so each tab shows its count. */
  live?: boolean;
  actions?: ReactNode;
  children: ReactNode;
}) {
  const { slug } = useParams();
  const { pathname } = useLocation();
  const base = `/plugins/${slug}`;
  // Capacity is the plugin root, where the nav item points, so staff-portal
  // keeps the Operations sidebar for every tab under it.
  const tabs = [
    { label: 'Capacity', href: base },
    { label: 'Profiles', href: `${base}/profiles` },
    { label: 'Classes', href: `${base}/classes` },
    { label: 'Inventory', href: `${base}/inventory` },
    { label: 'Catalog', href: `${base}/catalog` },
  ];
  const active = tabs.slice(1).find((t) => pathname.startsWith(t.href))?.href ?? base;

  return (
    <div data-testid={testId} className="flex min-w-0 flex-col gap-6 p-4 sm:p-6">
      {/* Inventory pages title themselves below the tabs. */}
      {live && <h1 className="sr-only">Bare Metal</h1>}
      <div className="fleet-tab-row">
        {live ? <LiveTabs tabs={tabs} active={active} /> : <PassiveTabs tabs={tabs} active={active} />}
        {actions && <div className="fleet-tab-actions">{actions}</div>}
      </div>
      {children}
    </div>
  );
}
