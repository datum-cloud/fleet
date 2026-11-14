import { PROVIDER_PROJECT } from '../lib/api';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { Tabs, TabsLinkTrigger, TabsList } from '@datum-cloud/datum-ui/tabs';
import type { ReactNode } from 'react';
import { Link, useLocation, useParams } from 'react-router';

// One "Bare Metal" nav item with tabs for each resource, so the Operations menu
// does not grow with every page. staff-portal adds no padding around plugin
// pages, so this pads itself like the host's pages (p-6).
export function FleetPage({
  testId,
  actions,
  children,
}: {
  testId: string;
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
  ];
  const active = tabs.slice(1).find((t) => pathname.startsWith(t.href))?.href ?? base;

  return (
    <div data-testid={testId} className="flex min-w-0 flex-col gap-6 p-4 sm:p-6">
      <PageTitle
        title="Bare Metal"
        description={`Provider resources in ${PROVIDER_PROJECT}`}
        actions={actions}
      />
      <div className="border-border border-b">
        <Tabs value={active} className="gap-0">
          <TabsList variant="line">
            {tabs.map((tab) => (
              <TabsLinkTrigger
                key={tab.href}
                value={tab.href}
                href={tab.href}
                linkComponent={Link}
                className="text-xs md:py-2">
                {tab.label}
              </TabsLinkTrigger>
            ))}
          </TabsList>
        </Tabs>
      </div>
      {children}
    </div>
  );
}
