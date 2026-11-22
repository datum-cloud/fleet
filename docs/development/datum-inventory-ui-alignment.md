# Inventory Datum UI alignment and stable navigation

## Scope and sources

This change originally started at PR104 commit
`4810fbf8b75fe50731a988326dd057f56e6b16cb` and now merges the exact stack base
`54ea22234decf841c01d17641b5686e558e4976d`, including settled asynchronous host
mount/test synchronization and the PR106 contract removal incorporated through
`5b60656759033b61674ee202a9ad94f4d49f4a64`. Provenance, Device sourceIdentity,
component sourceRef, placement method/snapshotDigest and NetBox importer tooling
remain removed. Visible blur/focus remains a complete no-op; actual hidden,
logout and stale-generation fences remain in force. PR107's unmerged catalog
work is not included. It is a stacked change targeting
`t3/preserve-inventory-controls-on-blur`; retarget to the default branch only
after PR104 merges and review the resulting diff.

Design inputs are the [Fleet architecture](../architecture/design.md), the
[pinned inventory proposal](../proposals/fleet-management/inventory.md), and the
[existing inventory UI contract](staff-inventory-ui.md). These distinguish physical
records from allocation, installation, verification, and authority.

Both frozen lockfiles resolve the declared `@datum-cloud/datum-ui: ^3.5.0` to
3.7.0. The implementation uses that installed package's README, exported type
declarations, emitted component source, and theme styles. No package manifest or
lockfile changes are needed. This is actual component adoption, not a root
package import plus custom lookalike controls.

Host routing was inspected at staff-portal commit
`a0c4cd3e4bd9e0343ffe3310caaa25b160073b41`:

- [`milo.layout.tsx`](https://github.com/datum-cloud/staff-portal/blob/a0c4cd3e4bd9e0343ffe3310caaa25b160073b41/app/layouts/milo.layout.tsx)
  calls `requireStaffUser` in its loader and supplies no query-only
  `shouldRevalidate` exemption.
- [`plugin-page-outlet.tsx`](https://github.com/datum-cloud/staff-portal/blob/a0c4cd3e4bd9e0343ffe3310caaa25b160073b41/app/modules/plugins/client/plugin-page-outlet.tsx)
  keys page routes and error resets by path, not inventory query parameters.
- [`federation-host.ts`](https://github.com/datum-cloud/staff-portal/blob/a0c4cd3e4bd9e0343ffe3310caaa25b160073b41/app/modules/plugins/client/federation-host.ts)
  shares the Button and Card modules, with the host's versions winning. Its
  package manifest currently declares Datum UI `^3.12.1` and React Router
  `^8.3.1`; that is source evidence, not verification of any deployed host.

No shallow-routing support is assumed. No host code, router loader, authentication
policy, or dependency version is changed here.

## Implemented component alignment

| Surface | Actual Datum UI use |
| --- | --- |
| Directory search, namespace, create/edit and reported-location inputs | `Input` and `Label`, preserving input types, bounds, required attributes, controlled values and accessible field names |
| Refresh, clear, component/lane add/remove, location actions, rack face controls | `Button`, explicitly using `htmlType` so auxiliary actions cannot submit a form |
| Inventory category navigation | `LinkButton as={Link}` with native router links and `aria-current`; no nested button/link or custom click/history implementation |
| Directory, physical components, cable lanes, equipment | `Table`, `TableHeader`, `TableBody`, `TableRow`, `TableHead`, `TableCell`, and `TableCaption` |
| Access guidance | `Card`, `CardContent`, `Title`, and `Text`, retaining named region, heading, retry and error semantics |
| Section headings | `Title`; the programmatically focused detail heading uses `titleVariants` on its existing ref-bearing `h2` |

The installed Button implementation renders a button even though its type surface
includes `asChild`; navigation therefore uses the implemented `LinkButton` API.
The pre-existing outer Fleet tabs, legacy claim table, and page-title components
already use Datum components and are not redesigned.

Removed custom input, button, heading, table decoration and category-link skin
rules so they no longer override the adopted components. Remaining specialized
geometry and responsive layout use Datum semantic color/radius tokens instead of
hardcoded fallback palettes. The existing focusable table region retains scroll
ownership; the library's inner table container does not add a second scroller.

## Stable tabs without bypassing host authority

The directory's Fleet header and category navigation now live outside the private
session boundary. They contain only fixed category labels, never record names,
namespace values, counts or management controls. Counts remain in authorized
content. Hosted category links therefore remain present during initial loading,
permission denial and session checking, and select access guidance when denied.
The default hosted category stays Devices rather than silently choosing a different
readable category while the static tab still says Devices.

For same-path navigation changing only `kind`, `record`, or `namespace`, without
a form submission or explicit revalidation:

1. Normal React Router links run the real host loader. Back/forward and direct
   namespaced links retain their URLs. No extra authority request is added.
2. Pending host checks still suspend read publication, invalidate operations,
   and unmount private content/forms. Only static tabs and localized
   “Checking inventory access…” text remain; the whole page is not replaced
   with skeletons.
3. A successfully returned **same principal** may reuse its isolated QueryClient's
   previously authorized inventory snapshot. The collection key no longer uses
   per-mount `useId`; it remains scoped to plugin, provider project and that
   private session client. It is retained for five minutes while inactive and is
   fresh until explicit refresh/invalidation within that session.
4. Operation lifetimes restart after every completed host transition, including
   batched transitions that never paint a pending frame. Reusing collection data
   never revives an old operation, immutable edit baseline or draft.

Explicit host revalidation, changed principal (including A–B–A), logout, hidden
visibility/pagehide and non-presentation navigation continue to discard the
session client or invalidate its generation. Read denials redact records; cached
data does not win over an observed error. Fresh form permission checks and guarded
submit-time exact reads/permission checks remain unchanged. Visible window
blur/focus still performs no request, remount, disabling or baseline reset.

This intentionally trades seamless private-content continuity for a short localized
access-check message while the host is pending. It does not promise zero host
loader requests. Same-session presentation changes avoid redundant seven-collection
inventory reads; explicit refresh and real authority changes fetch again. Snapshot
reuse is not a live freshness or newly granted permission claim.

## Deliberate remaining gaps

- Native selects remain for filters, record references, component types, endpoints
  and reported geometry. Datum 3.7.0 exports a Radix Select, not a drop-in native
  select. Replacing these safely needs a separate keyboard, required/empty-value,
  implicit-label, native fieldset-disabling and portal review. This change does
  not invent an adapter or claim these are Datum Selects.
- Native details/summary, fieldsets/legends, semantic definition lists, responsive
  form grids and bespoke rack geometry remain. Rack overlap and occupancy meaning
  are unchanged. The source-defined muted palette is used, not a new palette.
- PR107's catalog views are not integrated or covered. Audit them after that work
  merges; this is not catalog UX parity.
- No authenticated staff-portal/Safari acceptance, deployed Module Federation
  compatibility, production operation, or live API write is claimed. The parent
  must review and run its deployment/host gates separately.

## Validation

- Provider after exact-base merge: `pnpm --dir ui/provider test` — 35 files,
  539 tests; typecheck and
  production build pass. Coverage asserts actual library slots/variants, field
  and button semantics, visible blur/focus node identity, permissions, immutable
  baselines, unknown outcomes and existing read/write fences.
- New route integration tests use the actual inventory page and host loader:
  stable tab DOM during a held loader, seven total collection reads across
  category/detail/back/forward transitions, direct links, immediate/batched
  navigation management readiness, in-flight create fencing, and fresh reads on
  principal, logout, denial, hidden/pagehide, explicit revalidation and unknown
  query changes. These do not mock Datum components.
- Consumer: `pnpm --dir ui/consumer test` — 3 files, 44 tests; typecheck and
  production build pass. Consumer implementation is untouched.
- Seven focused session-boundary, tab-navigation, exact-staging, stable-control,
  visible-focus-save, navigation-safety and revalidation-safety files pass three
  consecutive runs (150 tests each). The inherited settled async `mountHost`
  synchronization remains unchanged. The cached-create navigation test now
  asserts the removed recorder/reference controls are absent rather than filling
  obsolete fields; it still checks draft clearing and zero stale writes.
- Documentation: `bash hack/validate-doc-links.sh`; whitespace: `git diff --check`.
- Merged API contract: `go test ./internal/api/inventory/v1alpha1` passes.
  Both UI builds retain the existing Module Federation warning about differing
  transitive `undici` versions; dependencies and lockfiles are not changed.
- Synthetic rendering: the existing `session-fixture.html` uses in-memory API
  responses and never writes a live API. The test Vite config now includes the
  existing Tailwind plugin so this harness compiles the same Datum tokens and
  component utilities instead of inspecting unprocessed CSS.
- Before the exact-base merge, local headless Brave/Chromium 146 checks at 1280px
  and 390px inspected directory,
  create, display-name edit, reported-location edit and rack rendering, light/dark
  tokens, horizontal overflow, keyboard rack-face activation, exact visible
  blur/focus markup/counters, stable tab nodes during held host loading and denied
  read redaction. Seven collection reads across loaded category/detail transitions;
  fourteen after an explicit denied refresh; zero writes and no page errors.
  Shared preview snapshots timed out, so these checks used a fresh local headless
  browser profile, not an authenticated browser session.
  These are prior rendering evidence, not a new browser acceptance of this merge.

To inspect the same synthetic harness locally:

```sh
pnpm --dir ui/provider exec vite --config vitest.config.ts --host 127.0.0.1 --port 7793
```

Open `/session-fixture.html`; the existing `inventorySessionFixture` exposes held
host responses, principal changes, denial and read/write counters. Local screenshots
are transient verification artifacts, not checked-in production evidence.
