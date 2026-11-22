# Inventory-backed capacity registration, stage 1

[Implementation status](README.md) · [Inventory proposal](../proposals/fleet-management/inventory.md) · [Enrollment contract](../proposals/fleet-management/bare-metal-provisioning.md#enrollment-and-explicit-preparation)

This implements an inert registration boundary, not hardware enrollment,
preparation, allocation, or verification. It supersedes the blocker-only UI
guardrail in PR140 (`12439e1fdf0d229352b4e32c27d9985993ca117d`). The synthetic
name-derived Device reference, invented cleaning default, allocation checkbox,
and unrestricted frontend create helper remain removed. Existing capacity
listing and legacy values remain visible; no existing object is migrated.

## Decision: no registration ledger

An earlier revision kept a private ledger in host-cluster Secrets. Its job was
to remember Device identities after a Device was deleted, and it grew into
tombstones, incarnation fences, permanent alias retention, `Unknown` outcomes,
a mutable tail with sealed segments, signed expansion and activation, epochs
and growth limits. The user decided this was overengineering and replaced it
with the following:

1. **Devices are not deleted.** An optional ValidatingAdmissionPolicy denies
   every Device `DELETE`. An environment decides whether to install it; see
   [Device deletion protection](#device-deletion-protection).
2. **No signed activation.** The bootstrap/activation gate, `/activationz`,
   `/status`, the operator control key and ConfigMap,
   `cmd/fleet-registration-control` and signed controls are gone. The
   registration webhook is an ordinary fail-closed webhook (`failurePolicy:
   Fail`) with mutual TLS, scoped credentials and least privilege.
3. **Registration checks live objects.** The webhook keeps no state. It
   checks the live Device and capacities through the API server on every
   request.

Rationale: when Devices are never deleted, the live Device list already holds
every identity the ledger used to remember, so a second store adds failure
modes without adding information. Environments that need to delete Devices,
such as test and synthetic environments, do not hold real machines and do not
need permanent identity retention.

## Public contract and authority

The staff form selects an existing `Device` in `default` and keeps its UID and
resourceVersion as an immutable selection baseline. It POSTs to the provider
project's `MachineCapacity` API through the staff portal's caller-preserving
proxy. The admitted create is:

```yaml
metadata:
  name: device-<Device UID>
  namespace: default
  annotations:
    fleet.datumapis.com/registration-device-uid: <Device UID>
    fleet.datumapis.com/registration-device-version: <Device resourceVersion>
    fleet.datumapis.com/registration-operation: <client operation ID>
spec:
  deviceRef: {name: selected-device}
  allocationPolicy: Disabled
  reclaimPolicy: Retain
  cleaningPolicyRef: {name: ""}
```

`spec.deviceRef` remains a name-only reference. The capacity name is no longer
authored: it is `device-` followed by the Device UID (43 characters). This
changes the earlier contract, where staff typed an independent name. All other
fields and the three annotations are unchanged. The annotations are request
preconditions and traceability, not authentication or ownership proofs. The
operation ID is no longer used for deduplication.

An empty cleaning-policy name explicitly means **unconfigured**, not a policy
named `default`, synthetic cleaning, or authorization to prepare. The CRD
permits it only while Disabled. The webhook rejects *all* capacity updates,
status updates and deletes. Real policy resolution and policy-use authorization
are future prerequisites for a separately designed transition; there is no
stage-1 preparation endpoint.

`cmd/fleet-registration` serves the validating webhook. For each create it:

1. requires Milo's authenticated `iam.miloapis.com/parent-*` extras naming a
   single Project in the configured provider scope, and an actor UID;
2. requires the `default` namespace, the exact shape above, and a name equal
   to `device-<annotated Device UID>`;
3. resolves the current Project lifetime through the root Milo client;
4. runs SubjectAccessReviews with the caller's username, UID, groups and
   extras for `machinecapacities.create` and named `devices.get` in
   `default`. Denials, errors and incomplete evaluations fail closed. This
   happens before the Device is read, so an unauthorized caller cannot learn
   whether a Device exists;
5. reads the Device uncached. A missing Device is denied. Its UID and
   resourceVersion must equal the annotations, and it must not be deleting;
6. requires both a non-placeholder serial and a canonical system UUID, and a
   complete provider host reference when one is present;
7. lists Devices and capacities in **all namespaces of every configured
   provider project** and denies when:
   - another Device shares a normalized alias (below);
   - any capacity already references this Device by name or by its UID
     annotation;
   - any capacity has unresolved physical identity, so non-conflict cannot be
     established; or
   - any capacity is backed by a Device that shares an alias.

The webhook writes nothing. Dry-run requests are evaluated like real ones
(`sideEffects: None`). Controller credentials supply reads and SAR evaluation,
not the caller's registration authority. Errors about records in another
provider project do not name them.

The webhook accepts only mutually authenticated TLS from a dedicated API-server
client CA. Do not use a broad tenant/client CA: an AdmissionReview received from
an untrusted client is not evidence of an authenticated actor.

## Physical identity and duplicates

Aliases are the digests of the normalized serial, system UUID and, when
present, provider host system/type/ID tuple. Normalization trims and lowercases
values. A shared alias between two Devices, including inconsistent
serial/UUID pairs, is ambiguous: registration is denied for both until a person
resolves the duplicate. Neither is merged or adopted. MAC addresses, BMC
endpoints, installation claims, rack placement and asset tags are not chassis
identifiers in this stage.

This is **not universal physical deduplication** or proof of real hardware
identity. Hardware outside the provider scope or absent from inventory cannot
be compared. Recording identity does not establish physical verification or
ownership.

### One capacity per Device

Two creates for the same Device carry the same derived name, so the API server
rejects the second with `AlreadyExists` even when both pass admission
concurrently. The webhook also refuses a Device that a legacy capacity already
references under another name. A replacement Device created under a deleted
Device's name is refused too, because the old capacity still references that
name.

### The documented duplicate race

The duplicate check is a point-in-time list. It does not close this race:

- Device A is being registered while Device B, which shares A's serial, is
  being created. If B is not yet visible when A's registration lists Devices,
  A's capacity is admitted.

No reservation store closes this, by decision. The consequences are bounded:

- B can never be registered, because A now exists and shares its alias.
- A's capacity is `Disabled`, and the webhook denies every capacity update, so
  it can never become available in stage 1.
- The staff registration form lists every Device and marks each one that
  shares an alias with another as **Conflicting**, so both A and B show the
  conflict until a person resolves it.
- Any future transition that enables capacity must recheck duplicates against
  live Devices at that time.

Two registrations racing for two already-existing Devices that share an alias
both fail, because each sees the other Device. Device status has no writer
today, so there is no server-side eligibility condition to set; reporting
conflicts through Device status is a follow-up.

## Device deletion protection

`config/components/device-deletion-protection` is a Kustomize component with a
ValidatingAdmissionPolicy and binding named `fleet-device-deletion-protection`.
It denies every `DELETE` on `devices.fleet.datumapis.com` (all versions, all
namespaces) with `failurePolicy: Fail` and reason `Forbidden`.

- **Where it is installed.** Devices are stored and admitted by the Milo
  control plane: `config/components/inventory` registers the CRD there, and
  records live in provider projects. The component therefore belongs with the
  Milo root, not the management-cluster host overlays, which have no Device
  CRD. Like the registration webhook, it is installed at the Milo root and
  relies on Milo running its root admission policies for project-routed
  requests (pinned Milo `v0.36.9` enables the `ValidatingAdmissionPolicy`
  plugin). Confirm this on the deployed Milo before relying on it.
- **Which roots include it.** `config/milo-production` is `config/milo` plus
  the component, and is the production-style root. `config/milo` (consumed by
  staging and test control planes that need Device deletion),
  `config/milo-synthetic`, `config/inventory`, `config/standalone` and every
  host overlay leave it out. `hack/validate-overlay-isolation.sh` fails if a
  production-style root lacks it, or if any other root renders it.
- **No exemptions.** No user, group or service account is exempt, including
  cluster administrators. The escape hatch is an environment not installing
  the component. Anyone able to delete the binding is outside the guarantee.
- **deletecollection.** The API server admits each object of a collection
  delete individually as a `DELETE`, so a collection delete that includes a
  Device fails and leaves Devices in place. Other objects in the same call may
  already be gone.
- **Garbage collection and finalizers.** Owner-reference cascades issue the
  same `DELETE` and are denied. Because the `DELETE` is denied, a Device never
  gets a deletion timestamp, so finalizers never run.
- **Namespace and project deletion (fail-closed: stays stuck).** A policy
  cannot see what a namespace contains, so it does not block the namespace
  `DELETE`. The namespace becomes `Terminating`, the namespace controller
  deletes the other contents, and its Device deletions are denied. The
  namespace stays `Terminating` with its Devices intact and readable; it
  accepts no new objects. Milo keeps `default` undeletable, and Milo's project
  purge drains `default` in place and reports remaining Devices as blockers on
  the Project. So deleting a provider project stops with its Devices in place.
  Other inventory in that namespace or project (for example DeviceClaims) is
  still deleted before the cascade stops. Capacity is not, because the
  registration webhook denies capacity deletes. Do not delete namespaces or
  projects that hold Devices.
- **Remediation for a stuck namespace or project.** Kubernetes cannot cancel
  a namespace deletion. Either leave it `Terminating`, which keeps every
  Device, or have the environment owner deliberately decide to give up those
  identities: remove the binding, let the cascade finish, and reinstall the
  component. The second option loses the Device records and their duplicate
  protection, so it is an explicit ownership decision, not routine repair.
- **Retirement.** A machine leaving service keeps its Device. Its capacity is
  already `Disabled` and cannot be changed, which is the only retirement state
  stage 1 has. Devices have no lifecycle or retirement field (see
  [Planning and registering physical devices](../operations/physical-inventory.md)).
  A Device retirement state is a follow-up.

### Deleting a Device where deletion is allowed

Without the component, deleting a Device that backs capacity leaves the
capacity in place, still `Disabled`. The webhook denies every update and
delete of it, so it cannot become available. No owner reference links the two:
the webhook rejects owner references on registration, and garbage collection
would be denied anyway because capacity deletes are fenced. The orphaned
capacity keeps its Device UID annotation and name, which also prevents a
replacement Device with the same name from registering. Clean it up only in
environments where capacity writes are not fenced, such as synthetic ones.

## Production packaging and rollout

- `config/milo` includes the mandatory fail-closed registration webhook
  (`config/components/registration-admission`). Its one entry matches
  capacity create/update/delete and status in every namespace, with no
  selectors or match conditions. An unreachable service blocks capacity
  writes globally and nothing else: Device, namespace and Project writes do
  not pass through the registration webhook.
- `config/milo-production` adds Device deletion protection.
- `config/overlays/management-plane` includes a separate two-replica
  registration Deployment and TLS Service. The pod does not mount a service
  account token and has no host RBAC. It does not enable production lifecycle
  handlers.
- The synthetic staging overlay excludes the registration workload; pair it
  with `config/milo-synthetic`, which installs neither the webhook nor the
  deletion policy. Never use synthetic roots in production.
- The handler trusts parent context from Milo's admission extras. In pinned
  Milo `v0.36.9`, `ProjectRouterWithRequestInfo` establishes the project from
  the routed path and `ProjectContextAuthorizationDecorator` overwrites the
  parent extras before authorization and admission. The pinned filter
  regression verifies that conflicting caller extras are overwritten while
  actor identity is preserved, and that nested Organization/User contexts do
  not present as a Project. Only a singleton
  `Project`/`resourcemanager.miloapis.com` context is accepted.
- Legacy capacity writes fail closed; legacy records remain readable and
  unchanged.

Environment-owned inputs, none of which this repository supplies:

1. Keep the existing `fleet-manager-config` authority and discovery paths.
   Supply `fleet-registration-milo-kubeconfig` (Project reads) and
   `fleet-registration-project-kubeconfig` (Device and capacity get/list
   across the complete provider scope, and SubjectAccessReview create). It
   needs no public writes, allocation permissions or provider credentials.
2. Supply `fleet-registration-tls` with `tls.crt`, `tls.key` and a dedicated
   `client-ca.crt`. Configure Milo's admission webhook client with an
   API-server client certificate signed by that CA, patch the webhook URL for
   reachability, and set `clientConfig.caBundle` to the serving CA.
3. Keep `--provider-projects` equal to the complete provider scope.
4. Choose the Milo root: `config/milo-production` to forbid Device deletion,
   or `config/milo` to allow it.
5. Before rolling out the UI, verify caller-preserving project routing, named
   Device read denial, registration denial, capacity mutation denial, TLS
   rejection, Device deletion denial where installed, and duplicate handling
   in the target environment. This change performs **no live writes or
   deployment**, so it does not claim those checks.

Removing the ledger deletes nothing live. An environment that bootstrapped the
old `fleet-capacity-registration-v1` Secret, its segments, the
`fleet-registration-control` ConfigMap, or the old retention policies keeps
those objects until its owner removes them; the new service ignores them.

## UI

The Datum dialog uses the existing host-session boundary and isolated read
cache. It shows the derived capacity name instead of a name field. Missing
host authority, denied registration, unavailable Devices, partial identity and
conflicting identities have explicit explanations. Selecting a Device
snapshots its baseline; neither refetch nor browser blur/focus changes it. A
named current read immediately precedes dispatch. Session changes fence
pending work. After dispatch, an uncertain result keeps the selection, disables
retry in that form, and asks the operator to check whether the derived capacity
exists. A repeat cannot create a second record. The unfenced legacy deletion
helper is never used for rollback.

## Verification

Run with Kubernetes 1.37.0 envtest assets:

```sh
KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.25.2 use 1.37.0 -p path)" go test -race -count=1 ./...
./hack/validate-overlay-isolation.sh </dev/null
./hack/test-overlay-isolation.sh </dev/null
```

`internal/registration/envtest_test.go` runs the production handler behind the
packaged webhook against a real API server and checks that:

- a duplicate alias across two live Devices is denied for both;
- a missing Device is denied;
- a caller without capacity permission, or without Device read permission, is
  denied;
- an authored name is denied, and concurrent creates for one Device persist
  once;
- a second capacity for the same Device is refused, including against legacy
  capacity;
- without the component, Device `DELETE` succeeds and the backing capacity
  stays `Disabled` and cannot be updated, deleted or re-registered by a
  same-name replacement;
- with the component applied from its manifest, Device `DELETE` is denied for
  every caller, `deletecollection` is denied, and namespace deletion stays
  `Terminating` with its Devices while other content drains. envtest has no
  namespace controller, so the test issues the controller's content deletions
  itself;
- a webhook outage blocks capacity writes but not Device writes.

Unit tests cover the remaining negative cases, normalization, cross-project
duplicates, project lifetime and the packaged webhook shape.
`cmd/fleet-registration` tests reject unauthenticated and untrusted webhook
transport. The overlay isolation tests cover where the deletion component may
and must appear. Frontend tests cover the derived name and conflict marking
with mocked transport. None of this is a live Milo deployment or an independent
security review.

The publishing workflow applies the server build tag to
`config/base/registration`. A regression applies the workflow's image
transforms to a temporary config tree and asserts the management-plane bundle
uses the published tag and synthetic staging has no registration workload.
