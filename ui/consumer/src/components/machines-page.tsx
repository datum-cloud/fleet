import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@datum-cloud/datum-ui/breadcrumb';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { Tabs, TabsLinkTrigger, TabsList } from '@datum-cloud/datum-ui/tabs';
import { HomeIcon } from 'lucide-react';
import type { ReactNode } from 'react';
import { Link, useLocation, useParams } from 'react-router';

/** The plugin's mount root: /project/:projectId/services/:serviceSlug. */
export function usePluginBase(): string {
  const { projectId, serviceSlug } = useParams<{ projectId: string; serviceSlug: string }>();
  return `/project/${projectId}/services/${serviceSlug}`;
}

// The Machines nav item opens on the claims list; the catalog sits beside it
// as a tab, so the project nav keeps a single entry.
export function MachinesPage({
  testId,
  description,
  children,
}: {
  testId: string;
  description: string;
  children: ReactNode;
}) {
  const { projectId } = useParams<{ projectId: string }>();
  const { pathname } = useLocation();
  const base = usePluginBase();
  const tabs = [
    { label: 'Machines', href: base },
    { label: 'Catalog', href: `${base}/catalog` },
  ];
  // Longest matching href wins, since the Machines tab is the mount root.
  const active =
    tabs
      .filter((t) => pathname === t.href || pathname.startsWith(`${t.href}/`))
      .sort((a, b) => b.href.length - a.href.length)[0]?.href ?? base;

  return (
    <div data-testid={testId} className="flex min-w-0 flex-col gap-6">
      <Breadcrumb>
        <BreadcrumbList>
          <BreadcrumbItem>
            <BreadcrumbLink asChild>
              <Link to={projectId ? `/project/${projectId}` : '/'}>
                <Icon icon={HomeIcon} size={16} />
              </Link>
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbPage>Machines</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle title="Machines" description={description} />

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
