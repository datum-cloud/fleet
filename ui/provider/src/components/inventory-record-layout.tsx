// Building blocks for inventory detail pages. See "Page design rules" in
// docs/development/staff-inventory-ui.md: a compact header, one primary view,
// secondary detail in tabs held in the URL, issues first, quiet when normal.
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { Tabs, TabsLinkTrigger, TabsList } from '@datum-cloud/datum-ui/tabs';
import type { ReactNode } from 'react';
import { Link, useLocation, useSearchParams } from 'react-router';
import type { ObjectMeta } from '../../../shared/fleet-api';
import { StatusBadge, type BadgeType } from './status-badge';

/** The URL search parameter holding a detail page's selected tab. */
export const RECORD_TAB_PARAM = 'tab';

export interface RecordTab {
  /** Stable URL value. */
  id: string;
  label: string;
  /** Shown after the label. Undefined hides it; use a string for "unknown". */
  count?: number | string;
  /** Why the count needs checking ("17+", "?"): shows the count in amber, with this as its tooltip and accessible text. */
  countNote?: string;
  content: ReactNode;
}

/** The selected tab id: the URL value when it names a tab, else the first. */
export function useRecordTab(tabs: Pick<RecordTab, 'id'>[]): string | undefined {
  const [params] = useSearchParams();
  const requested = params.get(RECORD_TAB_PARAM);
  return tabs.find(tab => tab.id === requested)?.id ?? tabs[0]?.id;
}

/** The current URL with only the tab changed. */
export function useTabHref() {
  const [params] = useSearchParams();
  const { pathname } = useLocation();
  return (id: string) => {
    const next = new URLSearchParams(params);
    next.set(RECORD_TAB_PARAM, id);
    return `${pathname}?${next}`;
  };
}

/** A link that selects a tab on this page. */
export function TabLink({ tab, children }: { tab: string; children: ReactNode }) {
  const tabHref = useTabHref();
  return <Link to={tabHref(tab)}>{children}</Link>;
}

export function RecordTabs({ tabs, label }: { tabs: RecordTab[]; label: string }) {
  const active = useRecordTab(tabs);
  const tabHref = useTabHref();
  const { pathname, search } = useLocation();
  const current = tabs.find(tab => tab.id === active);
  if (!current) return null;
  return <div className="inventory-record-tabs">
    <Tabs value={current.id} activationMode="manual" className="inventory-tab-row">
      <TabsList aria-label={label}>
        {tabs.map(tab => <TabsLinkTrigger key={tab.id} value={tab.id} href={tabHref(tab.id)} linkComponent={Link}
          onClick={event => { if (tabHref(tab.id) === `${pathname}${search}`) event.preventDefault(); }}
          aria-current={tab.id === current.id ? 'page' : undefined} aria-controls={undefined}
          onKeyDown={event => { if (event.key === ' ') { event.preventDefault(); event.currentTarget.click(); } }}>
          {tab.label}{tab.count !== undefined && <span className="inventory-count" data-tone={tab.countNote ? 'warning' : undefined} title={tab.countNote}> {tab.count}{tab.countNote && <span className="inventory-visually-hidden"> · {tab.countNote}</span>}</span>}
        </TabsLinkTrigger>)}
      </TabsList>
    </Tabs>
    {/* Every panel stays mounted so open dialogs and permission checks keep
        their state across tab changes; inactive panels are hidden. */}
    {tabs.map(tab => <section key={tab.id} className="inventory-record-panel" aria-label={tab.label} data-tab={tab.id} hidden={tab.id !== current.id}>{tab.content}</section>)}
  </div>;
}

export interface RecordIssue {
  id: string;
  text: ReactNode;
  /** Where the problem can be reviewed: a tab id on this page or an href. */
  tab?: string;
  href?: string;
  tone?: 'danger' | 'warning';
}

/** Short banners for disagreements, unknowns and unreadable records. */
export function IssueBanners({ issues }: { issues: RecordIssue[] }) {
  const tabHref = useTabHref();
  if (!issues.length) return null;
  return <ul className="inventory-issues" aria-label="Record issues">
    {issues.map(issue => {
      const target = issue.tab ? tabHref(issue.tab) : issue.href;
      return <li key={issue.id} data-tone={issue.tone ?? 'warning'}>
        <span className="inventory-issue-glyph" aria-hidden="true">!</span>
        <span>{issue.text}</span>
        {target && (target.startsWith('#') ? <a href={target}>Review</a> : <Link to={target}>Review</Link>)}
      </li>;
    })}
  </ul>;
}

export interface SummaryBadge { label: string; tone: BadgeType }

/** The one-line summary under a record's title. */
export function RecordSummary({ badges = [], children }: { badges?: SummaryBadge[]; children?: ReactNode }) {
  return <span className="inventory-record-summary">
    {badges.map(badge => <StatusBadge key={badge.label} type={badge.tone}>{badge.label}</StatusBadge>)}
    {children !== undefined && <span className="inventory-record-summary-text">{children}</span>}
  </span>;
}

/** Joins summary parts with a middle dot, skipping empty ones. */
export function SummaryParts({ parts }: { parts: ReactNode[] }) {
  const shown = parts.filter(part => part !== undefined && part !== null && part !== false && part !== '');
  return <>{shown.map((part, index) => <span key={index}>{index > 0 && <span aria-hidden="true"> · </span>}{part}</span>)}</>;
}

export interface FactItem {
  label: string;
  value: ReactNode;
  /** Keep this fact even when it is empty, because the absence matters. */
  always?: boolean;
  /** Shown instead of an empty value when `always` is set. */
  empty?: string;
}

const isEmpty = (value: ReactNode) => value === undefined || value === null || value === false || value === '' || (Array.isArray(value) && !value.length);

/** Label/value facts. Empty facts are hidden unless marked `always`. */
export function Facts({ items, label }: { items: FactItem[]; label?: string }) {
  const shown = items.filter(item => item.always || !isEmpty(item.value));
  if (!shown.length) return null;
  return <dl className="inventory-fields" aria-label={label}>
    {shown.map(item => <div key={item.label}><dt>{item.label}</dt><dd>{isEmpty(item.value) ? item.empty ?? 'Not recorded' : item.value}</dd></div>)}
  </dl>;
}

/** A titled group of facts inside a tab. */
export function FactSection({ title, children }: { title: string; children: ReactNode }) {
  return <section className="inventory-fact-section" aria-label={title}><h4>{title}</h4>{children}</section>;
}

/** A compact read-only table for short lists inside a tab. */
export function SmallTable({ label, head, rows }: { label: string; head: string[]; rows: ReactNode[][] }) {
  return <div className="inventory-scroll inventory-small-table"><Table aria-label={label} className="inventory-dense-table">
    <TableHeader><TableRow>{head.map(cell => <TableHead key={cell}>{cell}</TableHead>)}</TableRow></TableHeader>
    <TableBody>{rows.map((row, index) => <TableRow key={index}>{row.map((cell, column) => <TableCell key={column}>{cell}</TableCell>)}</TableRow>)}</TableBody>
  </Table></div>;
}

/**
 * The record's resource name, creation date and any fact the page does not
 * show elsewhere, as one quiet line at the end of the page. Namespace, UID
 * and verification are left out: the UI reads only `default`, and nothing
 * verifies inventory yet.
 */
export function RecordFooter({ metadata, extra = [] }: { metadata: ObjectMeta; extra?: FactItem[] }) {
  const parts = [
    <>Record <code>{metadata.name}</code></>,
    metadata.creationTimestamp && <>created {metadata.creationTimestamp.slice(0, 10)}</>,
    ...extra.filter(item => !isEmpty(item.value)).map(item => <>{item.label} {item.value}</>),
  ].filter(Boolean);
  return <section className="inventory-record-footer" aria-label="Record"><p>{parts.map((part, index) => <span key={index}>{index > 0 && ' · '}{part}</span>)}</p></section>;
}
