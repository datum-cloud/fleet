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
checks the installed root webhook configuration at startup and before admission:
required write rules, explicit serving CA, Fail policy and no selectors or match
conditions that allow a caller to skip the fence.

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

`fleet-capacity-registration-v1` is a separate host-cluster Secret in
`fleet-system-private`; it is **not** allocation custody. The service has only
get/update permission on that single Secret. Its JSON retains the actor, exact
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
delete race. Device updates retain their old resourceVersion before admission
completes. Registration rejects a selected version with such an update fence;
only an explicitly selected newer persisted version can pass it. This serializes
pending metadata/spec updates with registration without silently refreshing the
selection. An update whose completion is unknown can leave its old version
ineligible even if storage made no change. Failed Device creates can likewise retain an alias reservation.
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
The bounded ledger refuses growth past 750,000 encoded bytes. Lost or corrupt
ledger state, scope changes, uncertain operations and full ledgers require
operator-led reconciliation/migration; deleting or recreating the ledger is not
a repair. Ledger update protection and private-namespace deletion protection
are installed in the host cluster. Cluster administrators who can remove those
fences or restore arbitrary storage remain outside the guarantee.

## Production packaging and rollout

- `config/milo` includes the mandatory Fail-closed registration webhook on the
  **actual project resource path**, not just a manager-side helper. It matches
  capacity create/update/delete/status, Device create/update/delete/status,
  namespace deletion, and root Project deletion. No object-label opt-in exists.
- `config/overlays/management-plane` includes a separate two-replica registration
  Deployment, TLS Service, narrowly scoped host RBAC, and private retention
  policies. It does not enable production lifecycle handlers.
- The webhook consumes parent context from Milo's standard admission extras.
  The repository-pinned Milo module enables validating admission by default and
  documents these extras in its cluster-aware webhook implementation.
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
   actual cross-cluster reachability and set `clientConfig.caBundle` to the
   serving CA. No empty or guessed trust defaults are accepted.
3. Install the host retention policies. Bootstrap the ledger **once**, using
   create, never apply/upsert, with environment-matching authority and sorted
   projects. Its initial JSON has `authority`, `projects`, `entries: []` and
   `deviceFences: []`. It is intentionally absent from GitOps resources so a
   later reconcile cannot overwrite accumulated reservations. Do not label it
   as legacy allocation custody (`fleet.datumapis.com/private-record`).
4. Quiesce provider inventory/capacity writes while installing the root webhook
   and service. Validate the complete provider scope and legacy inventory before
   reopening writes. Unavailable TLS, credentials, ledger or service leave
   creates blocked. Readiness is process reachability, not asset eligibility.
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
Envtest exercises the production authorization function against real SARs,
the packaged validating webhook, and the host retention policies against a
disposable API server. TLS tests reject unauthenticated webhook transport. It is not a live Milo
deployment or independent security review. Frontend tests use real session
components and mocked transport. Run the existing Go/envtest, race, vet,
provider/consumer test/typecheck/build, manifest-isolation and documentation
checks described in the [development guide](README.md#build-test-and-run).
