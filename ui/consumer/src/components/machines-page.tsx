import { FleetStyles } from './fleet-styles';
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
import { HomeIcon } from 'lucide-react';
import { Fragment, type ReactNode } from 'react';
import { Link, useParams } from 'react-router';

/** The plugin's mount root: /project/:projectId/services/:serviceSlug. */
export function usePluginBase(): string {
  const { projectId, serviceSlug } = useParams<{ projectId: string; serviceSlug: string }>();
  return `/project/${projectId}/services/${serviceSlug}`;
}

export interface Crumb {
  label: string;
  /** Omitted on the current page. */
  href?: string;
}

// Breadcrumb, title and actions, laid out like the other service plugins'
// pages (NLB in particular). The list has no tabs: deploying starts from the
// list itself.
export function MachinesPage({
  testId,
  title,
  description,
  actions,
  crumbs,
  children,
}: {
  testId: string;
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  /** Crumbs after the project home icon. */
  crumbs: Crumb[];
  children: ReactNode;
}) {
  const { projectId } = useParams<{ projectId: string }>();

  return (
    <div data-testid={testId} className="flex min-w-0 flex-col gap-6">
      <FleetStyles />
      <Breadcrumb className="min-w-0 overflow-x-auto">
        <BreadcrumbList className="flex-nowrap">
          <BreadcrumbItem>
            <BreadcrumbLink asChild>
              <Link to={projectId ? `/project/${projectId}` : '/'}>
                <Icon icon={HomeIcon} size={16} />
              </Link>
            </BreadcrumbLink>
          </BreadcrumbItem>
          {crumbs.map((crumb) => (
            <Fragment key={crumb.label}>
              <BreadcrumbSeparator />
              <BreadcrumbItem className="min-w-0">
                {crumb.href ? (
                  <BreadcrumbLink asChild>
                    <Link to={crumb.href}>{crumb.label}</Link>
                  </BreadcrumbLink>
                ) : (
                  <BreadcrumbPage className="truncate">{crumb.label}</BreadcrumbPage>
                )}
              </BreadcrumbItem>
            </Fragment>
          ))}
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle
        title={title}
        description={description}
        descriptionClassName="max-w-none"
        actions={actions}
      />

      {children}
    </div>
  );
}
