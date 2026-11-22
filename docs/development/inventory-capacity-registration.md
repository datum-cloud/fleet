# Inventory-backed capacity registration, stage 1

[Implementation status](README.md) · [Inventory proposal](../proposals/fleet-management/inventory.md) · [Enrollment contract](../proposals/fleet-management/bare-metal-provisioning.md#enrollment-and-explicit-preparation)

This implements an inert registration boundary, not hardware enrollment,
preparation, allocation, or verification. It supersedes the blocker-only UI
guardrail in PR140 (`12439e1fdf0d229352b4e32c27d9985993ca117d`). The synthetic
name-derived Device reference, invented cleaning default, allocation checkbox,
and unrestricted frontend create helper remain removed. Existing capacity
listing and legacy values remain visible; no existing object is migrated.

## Public contract and authority

The staff form selects an existing `Device` in `default`, retains its UID and
resourceVersion as an immutable selection baseline, and authors an independent
capacity name. It POSTs to the existing provider project's `MachineCapacity`
API using the staff portal's caller-preserving proxy. `spec.deviceRef` remains
a name-only reference. It must not be a generated capacity name masquerading as
a Device name.

The only admitted new capacity contract is:

```yaml
spec:
  deviceRef: {name: selected-device}
  allocationPolicy: Disabled
  reclaimPolicy: Retain
  cleaningPolicyRef: {name: ""}
```

An empty cleaning-policy name explicitly means **unconfigured**, not a policy
named `default`, synthetic cleaning, or authorization to prepare. The CRD permits
it only while Disabled. The production admission handler rejects *all* capacity
updates, status updates and deletes, including setting a nonempty policy and
enabling in the same request. Real policy resolution and policy-use authorization
are future prerequisites for a separately designed transition; there is no
stage-1 preparation endpoint. Legacy nonempty cleaning references remain valid
in the schema and synthetic lifecycle environments remain separate.

Three protected request-precondition annotations carry the selected UID,
selected resourceVersion and stable client operation ID:
`fleet.datumapis.com/registration-device-uid`,
`fleet.datumapis.com/registration-device-version`, and
`fleet.datumapis.com/registration-operation`. They are not authentication or
ownership proofs. A create with copied annotations still goes through the full
admission protocol; updates and deletion cannot remove or replace them. The
private ledger, not these public annotations, binds the exact capacity UID.

`cmd/fleet-registration` serves a validating admission webhook. Its production
implementation uses Milo's authenticated `iam.miloapis.com/parent-*` extras,
requires a single Project context and an actor UID, and resolves the current
Project UID through the root Milo client. It does not use a project, actor or
permission bypass from request annotations. Separate SubjectAccessReviews pass
through the caller's username, UID, groups and extras for current
`machinecapacities.create` and named `devices.get` in `default`. Permission errors,
denials, missing identity and incomplete evaluations fail closed. Both checks
are repeated before reservation, alongside uncached Device and namespace
lifetime/version reads. Controller credentials supply reads and SAR evaluation,
not the caller's registration authority.

The webhook accepts only mutually authenticated TLS from a dedicated API-server
client CA. Do not use a broad tenant/client CA: an AdmissionReview received from
an untrusted client is not evidence of an authenticated actor. The server also
checks the installed root webhook configuration at signed activation and before admission:
the exact write rules, explicit serving CA, Fail policy, common authority
endpoint, and the restricted match conditions described below. Label selectors,
missing fences, expanded rules, and conditions differing from the configured
provider scope are rejected. Scope changes require an operator-coordinated
manifest and ledger migration, not caller-selected labels or annotations.

## Physical identity and bounded deduplication

The complete operator-configured provider-project list and Milo authority are
fixed in one private ledger. All replicas, names and projects in that scope use
the same resourceVersion CAS. Configuration must match the retained scope
exactly; changing the list requires an explicit ledger migration, not a restart
with a smaller list. The packaged UI and deployment use `datum-cloud`.

Eligible Devices must have **both** a recorded non-placeholder serial and a
canonical non-placeholder system UUID. The canonical asset key is the digest of
`uuid:` plus that UUID. Known reservation aliases include the digests of the
normalized serial, UUID, and, when present, provider host system/type/ID tuple.
Normalization trims and lowercases values; conservative false-positive
collisions require operator investigation rather than choosing a winner.
MAC addresses, BMC endpoints, installation claims, rack placement and asset tags
are not canonical chassis identifiers in this stage.

Every known alias is claimed in the same CAS transaction. A second Device with
any shared alias, including inconsistent serial/UUID pairs or a shared provider
host reference, is ambiguous and is rejected rather than merged or adopted.
Before reservation the handler reads Devices and capacities in **all namespaces
of every configured provider project**. Existing unresolved legacy capacity
blocks new registration because non-conflict cannot be established. Existing
capacity bound to a conflicting asset blocks it regardless of allocation
policy or reported readiness. None of these records or allocations is changed.

This is **not universal physical deduplication** or proof of real hardware
identity. Serial-only and UUID-only records cannot prove the same hardware in
the absence of shared evidence; neither can be selected for registration.
Different fabricated or incomplete identifiers, assets outside the configured
provider scope, and physical equipment absent from inventory cannot be compared
reliably. Recording identity does not establish physical verification or
ownership. Production allocation remains disabled independently.

## Serialization and recovery

`fleet-capacity-registration-v1` is a separate host-cluster Secret in the
dedicated `fleet-registration-ledger` namespace; it is **not** allocation custody
and does not share `fleet-system-private` with the manager's private records.
The service updates only that root and can create/read immutable retained
segments under the host policy.
The [storage and bootstrap protocol](registration-storage-rollout.md) adds signed
lossless expansion and deny-only transport startup. Its JSON retains the actor, exact
API-normalized create object, admission request UID, client operation ID,
Project/namespace lifetimes, Device UID/resourceVersion, aliases, canonical key,
and API-assigned capacity UID **before allowing the public create**. Kubernetes
assigns that UID before validating admission; envtest verifies a caller-supplied
UID is replaced and the retained UID matches the actual result.

CAS conflicts repeat authorization and observation before another reservation
attempt. No public object, allocation, status, provider, networking, or cleaning
write is performed by the registration service. Capacity persistence is the
original caller's API-server operation. All new entries start `Unknown` because
another admission plugin, API storage failure, timeout or crash can still prevent
the create. A failed CAS response may itself have committed: it is not released.

Device creation also reserves its known aliases before admission completes.
Device deletion retains a permanent UID tombstone before admission completes.
Both use the same ledger CAS, closing the registration-versus-pending-create or
delete race. Potentially changing Device updates retain their old resourceVersion
before admission completes. Registration rejects a selected version with such an update fence;
only an explicitly selected newer persisted version can pass it. This serializes
pending metadata/spec updates with registration without silently refreshing the
selection. The sole exception is a provably unchanged update: the complete old
and new admission JSON objects are equal, including metadata, managed fields,
status and any fields unknown to Fleet's typed Device. JSON key order and
whitespace are immaterial; numbers are compared without floating-point rounding.
An unchanged Kubernetes update can keep its existing resourceVersion, and must
not permanently fence that still-current version. This exception adds no new
reservation and **never removes an existing pending-update fence**. It does not
infer success from a later read, refresh a selected version, or relax identity
and registered-spec checks. Any differing field still takes the conservative
fencing path. An uncertain potentially changing update can therefore leave its
old version ineligible even if storage ultimately made no change. Failed Device
creates can likewise retain an alias reservation.
Update fences are coalesced to one record per provider Project and Device UID,
not one per observation. Before replacing a retained version, admission reads
the Device directly through the uncached project client and requires the current
UID and resourceVersion to equal both incoming update incarnations/baselines.
It rejects stale admissions rather than replacing a newer pending fence. The
ledger snapshot precedes that authoritative read; a competing ledger write
forces a full CAS retry and another Device read. All concurrent updates at the
same baseline share its fence. Once the API server has persisted a newer version,
optimistic concurrency prevents an older-baseline write from subsequently
committing; an API-server storage retry must re-enter admission at its new
baseline. Thus superseded fences can be compacted without allowing an unfinished
current-baseline update to race registration. ResourceVersions are compared only
for equality, never ordered numerically.

Coalescing unions creation aliases and never clears a deletion tombstone.
A deletion replaces update history with a permanent all-version tombstone;
later changing updates for that UID are rejected. Older-build multiple-version
records are compacted only when this same authoritative update check succeeds,
or when deletion installs a stronger permanent tombstone. Unrelated incarnations
are untouched. An unknown/rejected write at the still-current version stays
fenced, including after restart and no-op updates. Recovery still requires a
genuine persisted change and explicit new selection, not automatic ledger cleanup.
The selected Device's own successful creation reservation is not a conflict.
Identity/provider-alias changes are forbidden; registered Device specs are
frozen, and its deletion and same-name replacement are forbidden. The default
provider namespace and configured provider Project deletions are denied.

Recovery observes the exact retained capacity UID, namespace and Project
lifetimes, Device incarnation, aliases and authored input. An exact observed
object becomes `Recorded`, which means only that the inert record exists.
Absence remains `Unknown`; replacement or lifetime loss is a conflict/tombstone,
never adoption. Recovery never creates, deletes, retries under a different name,
or publishes positive readiness. It runs every 30 seconds and after restart;
CAS conflicts leave evidence retained for a later observation. Repeating POST
is not the recovery mechanism: inspect the original name and retained operation.

Reservations and tombstones have no automatic expiry or release path in stage 1.
This deliberately includes create attempts that never persisted and deleted
Devices that were never registered: their known hardware aliases remain reserved,
and recreating that hardware under a new UID fails closed. Stage 1 has no safe
operator release command. Future reconciliation must prove the original outcome,
all competing operations and lifetime ownership before designing a fenced release;
neither manual Secret editing nor speculative expiry is an accepted workaround.

Routine updates to an incarnation use one record with at most one pending
resourceVersion, retained aliases and a deletion bit. This bounds growth by
distinct incarnations/aliases and registration operations, **not** by elapsed
collector observations. It is not an unlimited-inventory or universal deduplication
guarantee: retained incarnations and reservations can still fill the pilot ledger.
The mutable tail refuses growth past 650,000 encoded bytes (the boundary fits),
leaving sealing headroom. Signed expansion seals up to sixteen immutable segments;
existing v1 tails up to 750,000 bytes can migrate losslessly. This is bounded growth,
not unlimited capacity, alias expiry, or permission to increase caps.
Admission makes at most six CAS attempts with complete reevaluation after conflicts;
exhaustion emits a structured error with project, operation identity and attempt
count, without actor credentials or ledger contents. Lost or corrupt
ledger state, scope changes, uncertain operations and full ledgers require
operator-led reconciliation/migration; deleting or recreating the ledger is not
a repair. Ledger update protection and private-namespace deletion protection
are installed in the host cluster. Cluster administrators who can remove those
fences or restore arbitrary storage remain outside the guarantee.

## Production packaging and rollout

- `config/milo` includes the mandatory Fail-closed registration webhook on the
  **actual project resource path**, not just a manager-side helper. Four separate
  admission entries use the same endpoint. Capacity create/update/delete/status
  remains globally fenced. Device create/update/delete/status excludes only
  proven unrelated Project contexts (in **all namespaces**, because alias scans
  cover all of them). Namespace deletion matches only `default` in protected or
  ambiguous contexts. Project deletion matches only exact configured provider Project
  names, including root requests without parent extras. Unnamed collection
  deletion cannot prove exclusion of protected lifetimes and is explicitly
  denied (including when transport is healthy); delete unrelated lifetimes by
  exact name instead. No object-label opt-in or opt-out exists.
- `config/overlays/management-plane` includes a separate two-replica registration
  Deployment, TLS Service, narrowly scoped host RBAC, and private retention
  policies. It does not enable production lifecycle handlers.
- The webhook consumes parent context from Milo's standard admission extras.
  The API server evaluates the inventory/lifetime CEL match conditions **before
  webhook transport**. An unreachable service or incomplete certificate rollout
  therefore cannot block unrelated consumers' Device writes, other namespaces,
  or unrelated Project deletions through these entries. In-scope writes remain
  Fail-closed during transport failure; capacity writes remain globally blocked.
  Handler-side exemptions alone are not an outage-isolation boundary.
- These conditions rely on **server-established routing context**, not arbitrary
  identity-provider claims. In pinned Milo `v0.36.9`,
  `ProjectRouterWithRequestInfo` establishes the project from the routed path;
  `ProjectContextAuthorizationDecorator` overwrites all three parent extras
  after authentication/impersonation and before authorization/admission
  (`cmd/milo/apiserver/config.go`). The pinned filter regression verifies that
  conflicting caller extras are overwritten while actor identity is preserved.
  Only a singleton `Project` / `resourcemanager.miloapis.com` / nonempty name
  outside the protected list proves that inventory is unrelated and skips the
  webhook. Missing, malformed, root, Organization and User inventory contexts
  remain fail-closed. This conservative fallback matters: pinned Milo's later
  Organization/User decorators can obscure the Project extras for nested paths;
  a non-Project context must not be mistaken for proof of unrelated storage.
  Regression tests exercise those actual pinned decorators and the resulting
  admission matching. Root Project deletion still uses only exact protected
  names; other namespace names remain outside the namespace fence.
  This scope is **not safe on a server that lets caller-supplied extras replace
  its actual routing context**, or exposes an alternate provider-storage path
  appearing as a different valid Project. Confirm these properties on the
  deployed Milo path before enabling use. Unscoped/root Device writes are not
  supported by this pilot; that is an explicit fail-closed limit, not universal
  outage isolation for every possible inventory route.
- Legacy capacity writes in configured production scope fail closed; legacy
  records remain readable and unchanged. Outside configured provider scope,
  ordinary inventory writes are unaffected, but new capacity writes are denied.
  The synthetic/standalone test APIs do not install this production webhook.
  Pair the synthetic staging overlay with `config/milo-synthetic` on a separate
  non-production API server; that overlay excludes registration workloads and
  retention policies. Never use these synthetic roots in production or share
  the registration authority's ledger. This is deployment isolation, not a
  caller-selected bypass.

Environment-owned inputs are required, just as for the existing manager. No
real credentials or certificates are supplied by this repository:

1. Preserve the existing `fleet-manager-config` authority and discovery paths.
   Supply separate `fleet-registration-milo-kubeconfig` and
   `fleet-registration-project-kubeconfig` Secrets at the mounted paths. The root
   credential needs Project reads and registration webhook-configuration reads;
   the project credential needs Device and capacity get/list across the complete
   scope, namespace get, and SubjectAccessReview create. It needs **no public
   capacity or Device writes**, allocation permissions or provider credentials.
2. Supply `fleet-registration-tls` with `tls.crt`, `tls.key`, and a dedicated
   `client-ca.crt`. Configure Milo's admission webhook client kubeconfig with an
   API-server client certificate signed by that CA. Patch the webhook URL for
   actual cross-cluster reachability and set every entry's
   `clientConfig.caBundle` to the serving CA. All four entries must have the same
   client configuration. No empty or guessed trust defaults are accepted.
3. Install the host retention policies. Bootstrap the ledger **once**, using
   create, never apply/upsert, with environment-matching authority and sorted
   projects. Its initial JSON has `authority`, `projects`, `entries: []` and
   `deviceFences: []`. It is intentionally absent from GitOps resources so a
   later reconcile cannot overwrite accumulated reservations. Create it in
   `fleet-registration-ledger`, not `fleet-system-private`. Do not label it
   as legacy allocation custody (`fleet.datumapis.com/private-record`).
4. Quiesce provider inventory/capacity writes while installing the root webhook
   and service. Validate the complete provider scope and legacy inventory before
   reopening writes. Unavailable TLS, credentials, ledger or service leave
   in-scope writes blocked. The API server must support webhook CEL match
   conditions; preserve the packaged conditions, including the exact sorted
   provider-name lists matching `--provider-projects` and the private ledger.
   Do not replace them with namespace/object label selectors. A missing or
   mismatched fence also prevents registration startup/admission; this does not
   authorize unquiesced partial installation or deletion of admission policy.
   Readiness is process reachability, not asset eligibility.
5. Before rolling out the UI, verify caller-preserving project routing, named
   Device read denial, registration denial, direct write/status/delete fences,
   TLS rejection, and duplicate concurrency in the target environment. This
   change performs **no live writes or deployment**, so it does not claim those
   environment-specific checks have been performed.

## UI and verification

The Datum dialog uses the existing real host-session boundary and isolated read
cache. Missing host authority, denied registration, unavailable Devices and
partial identity have explicit explanations. Selecting a Device snapshots its
baseline; neither refetch nor visible browser-chrome blur/focus changes it. A
named current read immediately precedes dispatch. Session changes fence pending
work. After dispatch, uncertain results retain the authored name and disable
retry; the existing unfenced legacy deletion helper is never used for rollback.

Tests cover concurrent CAS, serial/UUID/provider aliases and provider projects,
stale UID/RV, Device deletion fences, namespace/Project recreation, permission
revocation, unknown commit and restart, copied-annotation incarnation conflicts,
real API-assigned UID binding, direct mutation fences and no public effects.
Regression coverage also includes real successful unchanged updates retaining
their RV followed by `Recorded` registration, genuine-edit/stale-selection
controls, preservation of pending real-update fences, and transport-outage
isolation for Device writes and namespace/Project deletion. The original reviewer
reproduction is retained byte-for-byte in
`internal/registration/testdata/review_adversarial_test.go.txt`; it is evidence,
not an executable regression because it asserts the former outage failure and
assumes the no-op registration failed before its same-asset control. Derived
regressions use separate assets and assert the corrected behavior.
Envtest exercises the production authorization function against real SARs,
the packaged validating webhook, and the host retention policies against a
disposable API server. The outage regression uses a minimal Project CRD solely
to exercise root deletion admission and disables envtest's built-in
NamespaceLifecycle plugin so its independent ban on deleting `default` cannot
mask the webhook test. It uses dry-run deletion for shared `default`, and actual
deletion for unrelated namespaces/Projects. This does not alter production
admission plugins. The trust-boundary test imports the pinned Milo routing
filters (their test-build transitive dependencies are now recorded in `go.mod`).
TLS tests reject unauthenticated webhook transport. It is not a live Milo
deployment or independent security review. Frontend tests use real session
components and mocked transport. Run the existing Go/envtest, race, vet,
provider/consumer test/typecheck/build, manifest-isolation and documentation
checks described in the [development guide](README.md#build-test-and-run).

Boundedness regressions perform 120 successive persisted collector observations
and 120 metadata updates through the packaged webhook, assert one incarnation
fence and stable ledger size, then register the current selection and recover it
to `Recorded`. The disposable Device CRD adds a test-only integer status observation
because the current production `DeviceStatus` is empty; this exercises real
`/status` storage and admission without inventing a production collector API.
Deterministic interleavings retain a newer pending fence while stale admission
retries, and keep unknown/rejected baselines fenced across restart/no-op requests.
An envtest race pauses two admitted updates at successive resourceVersions: after
coalescing, the older in-flight update conflicts at actual API-server storage,
the newer pending version remains unregistrable, and only its persisted successor
can be explicitly selected and recorded.
Real API-server Secret conflicts exercise success after five conflicts and six-conflict
exhaustion/logging. Separate tests assert the exact size boundary and effect-free
dry-run denial.

The publishing workflow applies the server build tag to `config/base/registration`,
as it does to the manager. A regression reads the workflow's image transforms,
applies them to a temporary config tree, and renders the management-plane bundle
to assert that `/fleet-registration` uses the published server image tag rather
than `latest`. It also verifies registration remains absent from synthetic staging.
Raw source manifests still contain template `latest` references: direct deployment
must supply equivalent immutable build-tag or digest transforms, plus the required
trust/credential configuration. No published bundle or live deployment was tested.

### Review-fix validation, November 22, 2026

- Full `go test -race ./...` with Kubernetes 1.37.0 envtest assets, `go vet ./...`,
  and `go build ./cmd/...` pass locally.
- Provider tests: 693 pass; consumer tests: 44 pass. Both typechecks and builds
  pass. One initial provider timing failure under concurrent validation load
  passed on a full rerun without modifying UI code or reviewer tests.
- Production and synthetic manifest renders, overlay isolation tests,
  standalone/synthetic render checks, documentation links and whitespace pass.
- The bounded-fencing rework adds real delayed-update and Secret-contention races,
  exact-size/dry-run guards, repeated status-write bounds, and rendered publishing
  image-transform checks. The original reviewer reproduction remains byte-identical.
- These are local checks, not live Milo acceptance or independent re-review.
  No deployment, merge, live resource mutation, or ledger migration was performed.
