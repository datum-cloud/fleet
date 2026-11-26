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

1. **In-service Devices are not deleted.** A reusable ValidatingAdmissionPolicy
   component denies a Device `DELETE` unless the Device is `Retired`, its
   namespace is being deleted, or the caller is a cleanup identity that the
   deploying repository lists. No root in this repository includes it;
   deploying repositories opt in. See
   [Device lifecycle](#device-lifecycle) and
   [Device deletion protection](#device-deletion-protection).
2. **No signed activation.** The bootstrap/activation gate, `/activationz`,
   `/status`, the operator control key and ConfigMap,
   `cmd/fleet-registration-control` and signed controls are gone. The
   registration webhook is an ordinary fail-closed webhook (`failurePolicy:
   Fail`) with mutual TLS, scoped credentials and least privilege.
3. **Registration checks live objects.** The webhook keeps no state. It
   checks the live Device and capacities through the API server on every
   request.

Rationale: when in-service Devices are never deleted, the live Device list
already holds every identity the ledger used to remember, so a second store
adds failure modes without adding information. A Device leaves the list only
after someone has retired it on purpose, deleted its namespace, or deleted its
project through a configured cleanup identity. Environments
that do not include the component, such as test and synthetic environments, do
not hold real machines and do not need identity retention.

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
permits it only while Disabled. The webhook rejects every update and delete
of [inventory-backed capacity](#scope-inventory-backed-capacity-only). An
update that would move capacity out of
`Disabled` while its Device is `Decommissioning` or `Retired` is denied for
that reason; the webhook first checks that the caller can read the Device. Real policy resolution and policy-use authorization
are future prerequisites for a separately designed transition; there is no
stage-1 preparation endpoint.

`cmd/fleet-registration` serves the validating webhook. For each create of
inventory-backed capacity it:

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
   resourceVersion must equal the annotations, it must not be deleting, and
   its [lifecycle](#device-lifecycle) must be `Active`;
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

### Provider-project scope

Step 1 is the only check that decides which project a request is judged in.
Nothing after it is limited to the provider projects on its own: `Resolve`
resolves whatever project name it is given, the webhook match conditions do
not look at the project, and the SubjectAccessReviews carry the request's own
user and extras. If step 1 let a project through, every later check would run
against that project. So a request whose project is outside
`--provider-projects` is denied before any project is resolved, any Device or
capacity is read, or any SubjectAccessReview is sent. The same holds for an
update that would leave `Disabled`. Separately, if `Resolve` returns a project
whose route name differs from the one requested, the request is denied before
that project's client is used. `internal/registration/tenancy_test.go` and
`tenancy_envtest_test.go` pin both checks with project clients and
SubjectAccessReview stubs that fail the test if they are called.

**The scope check is currently the only limit on what a certificate holder can
learn.** The webhook does not yet check who is calling it
([#149](https://github.com/datum-cloud/fleet/issues/149)). Any holder of a
client certificate from the webhook's client CA can post an AdmissionReview
directly and read the verdict, and a verdict can name Devices and their
lifecycle. Such a caller writes the review's user and extras itself, so the
SubjectAccessReviews do not limit it: it can claim any identity. Until #149 is
fixed, the provider-project scope check is the only thing that bounds what it
learns, and it bounds it to the configured provider projects. Treat every
certificate from that CA as read access to Device names and lifecycle across
the provider scope.

## Scope: inventory-backed capacity only

The webhook governs only **inventory-backed** capacity, meaning capacity that
registration creates from a Device. Other capacity, such as synthetic
capacity that a GitOps controller applies, capacity whose status a synthetic
provider writes, and hand-made capacity, is not routed to it at all.

The marker is registration's own metadata:

- any annotation whose key starts with `fleet.datumapis.com/registration-`
  (the three annotations above), on the new or the old object; or
- on create only, a name starting with `device-`, the derived name.

The marker is reliable because the webhook admits no create of
inventory-backed capacity without all three annotations and the derived
name, and admits no later update or delete of it, so the annotations cannot
be removed or changed after creation. A label was not used: registration
sets none, adding one would change the portal's wire contract, and a match
condition can read annotations directly. The name is part of the marker only
on create, so that the derived name space is reserved for registration and
no unannotated create can occupy a Device's derived name. Capacity is not
renamed, so the annotations alone identify it afterwards.

The packaged webhook routes requests with one rule and one match condition:

```yaml
rules:
  - apiGroups: [metal.datumapis.com]
    apiVersions: [v1alpha1]
    operations: [CREATE, UPDATE, DELETE]
    resources: [machinecapacities]
    scope: Namespaced
matchConditions:
  - name: inventory-backed-capacity
    expression: >-
      (request.operation == 'CREATE' && object != null
      && has(object.metadata.name) && object.metadata.name.startsWith('device-'))
      || (object != null && has(object.metadata.annotations)
      && object.metadata.annotations.exists(key, key.startsWith('fleet.datumapis.com/registration-')))
      || (oldObject != null && has(oldObject.metadata.annotations)
      && oldObject.metadata.annotations.exists(key, key.startsWith('fleet.datumapis.com/registration-')))
```

`object` is null for a delete and `oldObject` is null for a create. Checking
both objects routes, and so denies, an update that adds a registration
annotation to unmarked capacity, which would make it look inventory-backed,
and an update or delete that removes or changes the annotation on marked
capacity, which would let it escape the gate. `failurePolicy: Fail` applies
to a match condition that fails to evaluate.

The handler applies the same test as defence in depth: a request for capacity
without the marker that reaches it anyway is allowed without reading Devices
or the provider project. A marked request is handled as before: creates are
gated in full, and updates, deletes and any routed subresource are denied.

**Status writes are not routed.** `machinecapacities/status` is not in the
rule. Stage 1 has no status writer for inventory-backed capacity, but a
provider may need one later, and the webhook would then have to be changed
first. Status writes are governed by RBAC alone; in the packaged IAM roles
only the controller role holds `machinecapacities.updateStatus`. This is
acceptable because a status write cannot widen what capacity offers:

- the CRD's status subresource ignores changes to `metadata` and `spec`, so a
  status write cannot strip the marker or move capacity out of `Disabled`;
- allocation requires `spec.allocationPolicy: Enabled`, which status cannot
  set.

The envtest suite confirms the first point on Kubernetes 1.37.0. A future
stage that lets status influence availability must revisit this.

**Unmarked capacity is not gated.** Capacity without the marker, including
legacy capacity created before registration admission, can again be created,
updated and deleted by any caller RBAC allows, with no Device check and no
Disabled requirement. It is not inventory-backed and makes no claim about a
Device. Registration still reads it: a create of inventory-backed capacity is
denied while any capacity in the provider scope references the Device, or has
unresolved physical identity.

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
- A's capacity is `Disabled`, and the webhook denies every update of
  inventory-backed capacity, so it can never become available in stage 1.
- The staff registration form lists every Device and marks each one that
  shares an alias with another as **Conflicting**, so both A and B show the
  conflict until a person resolves it.
- Any future transition that enables capacity must recheck duplicates against
  live Devices at that time.

**This race is open, and it blocks stage 2.** The bounds above hold only while
inventory-backed capacity stays `Disabled`. Allocation must not be enabled for
inventory-backed capacity, by the webhook allowing an update out of
`Disabled` or by any other path, until this race is closed. A recheck at
enable time narrows the window but does not close it, because a duplicate
Device can still appear after the recheck. See
[Stage 2 blockers](#stage-2-blockers).

Two registrations racing for two already-existing Devices that share an alias
both fail, because each sees the other Device. Device status has no writer
today, so there is no server-side eligibility condition to set; reporting
conflicts through Device status is a follow-up.

## Device lifecycle

`Device.spec.lifecycle` records whether the asset is in service. It is part of
spec, so the inventory editor role, which can update Devices, can set it; no
extra RBAC exists.

| Value | Meaning |
| --- | --- |
| `Active` | In service. The CRD default when the field is unset. |
| `Decommissioning` | Leaving service. Can return to `Active`. |
| `Retired` | Out of service. Terminal. |

The CRD enforces transitions with transition rules (`oldSelf`) on `spec`:

- `Active` to `Decommissioning`, `Decommissioning` to `Active`, and
  `Decommissioning` to `Retired` are allowed. Leaving the value unchanged is
  always allowed.
- `Active` to `Retired` is rejected: move the Device to `Decommissioning`
  first.
- Nothing leaves `Retired`. Removing the field defaults it to `Active`, so
  that is rejected too.

The rules apply to updates only; a Device may be created in any lifecycle, for
example to record hardware that is already out of service. Other spec edits to
a Retired Device are still subject only to the existing rules.

Registration semantics, enforced by the registration webhook:

- Only an `Active` Device can get new capacity.
- Capacity whose Device is `Decommissioning` or `Retired` cannot leave
  `Disabled`. Stage 1 already denies every update of inventory-backed
  capacity, so this states the rule a future enabling transition must keep.
- A Retired Device still takes part in the duplicate-identity check for as
  long as it exists. Deleting it removes its identity from that check.

Lifecycle is a record, not an effect. Moving a Device changes no capacity,
allocation, provider or network state.

## Device deletion protection

`config/components/device-deletion-protection` is a reusable Kustomize
component with a ValidatingAdmissionPolicy and binding named
`fleet-device-deletion-protection`. It matches every `DELETE` on
`devices.fleet.datumapis.com` (all versions, all namespaces) and admits it only
when:

1. the Device's namespace is being deleted: the policy's `namespaceObject`
   has `metadata.deletionTimestamp` set. A missing namespace object does not
   match;
2. the Device being deleted (`oldObject`) has `spec.lifecycle: Retired`; or
3. the request's `userInfo.username` is listed exactly in the
   `cleanupUsernames` key of the binding's parameter ConfigMap. The policy's
   `paramKind` is core `v1` `ConfigMap`. This exception is off as shipped; see
   [Cleanup identity](#cleanup-identity).

Every other `DELETE` is denied with reason `Forbidden` (HTTP 403). The message
lists the three ways a delete is allowed and says to move the Device to
`Decommissioning`, then `Retired`, first. The policy uses
`failurePolicy: Fail` and the binding `validationActions: [Deny]`, so an
evaluation error also denies.

- **Who includes it.** No root in this repository renders the policy.
  Production-specific configuration lives in the deploying repository, which
  adds the component to the root it applies to the Milo control plane. Devices
  are stored and admitted there: `config/components/inventory` registers the
  CRD in Milo, and records live in provider projects. Management-cluster host
  overlays have no Device CRD. Like the registration webhook, the policy relies
  on Milo running its root admission policies for project-routed requests
  (pinned Milo `v0.36.9` enables the `ValidatingAdmissionPolicy` plugin).
  Confirm this on the deployed Milo before relying on it.
- **Validation.** `hack/validate-overlay-isolation.sh` renders the component
  on its own and fails unless it is exactly this policy: the same name,
  expression, a `paramKind` of exactly core `v1` `ConfigMap`,
  `failurePolicy: Fail`, `Deny` only, no `paramRef` on the binding, and no
  match conditions, selectors, exclusions or variables. Because the expression
  is compared exactly, the validator rejects wildcard or prefix username
  matching, a group-based exemption, a null `params` treated as allow-all, a
  dropped empty-username guard and an always-true clause. It also rejects a
  `paramRef` in the component, including one with
  `parameterNotFoundAction: Allow`: deploying repositories add the `paramRef`.
  It fails if any root in this repository renders the policy, under its own
  name or another, and if the inventory component exists without this
  component.
- **The cleanup identity is the only subject exemption.** Apart from exact
  usernames in the deploying repository's ConfigMap, the policy does not look
  at the request's user, group or service account. Cluster administrators are
  held to the same rule. Anyone able to delete or edit the binding, or to write
  the parameter ConfigMap, is outside the guarantee.
- **deletecollection, garbage collection and finalizers.** The API server
  admits each object of a collection delete individually as a `DELETE`, so a
  collection delete that reaches a Device that is not Retired is denied and
  leaves that Device in place. Retired Devices and other objects it reached
  first may already be gone; it may also stop before reaching every Retired
  Device, so repeat the delete per Device. Owner-reference cascades issue
  the same `DELETE`. A denied Device gets no deletion timestamp, so its
  finalizers never run.
- **Namespace deletion is the escape hatch.** Deleting a namespace is a
  privileged act. Once the namespace has a deletion timestamp, the namespace
  controller's Device deletions are admitted, so the namespace finishes
  terminating and takes its Devices with it, whatever their lifecycle. Their
  identities leave the duplicate check. The policy reads the namespace from the
  API server's namespace cache, which observes the deletion mark shortly after
  it is set.
- **Project deletion.** In pinned Milo `v0.36.9`, project purge
  (`internal/controllers/projectpurge`) first issues a collection delete for
  every namespaced resource in every namespace, `default` included, and only
  then deletes the namespaces. It never deletes `default`, which it drains in
  place. In that first phase no namespace is marked yet, so the
  deleting-namespace exception never applies, and a denied collection delete
  aborts the purge before it deletes any namespace. The purge runs as the Milo
  controller-manager's own identity against the project's control plane.
  - With the [cleanup identity](#cleanup-identity) configured to list that
    username, the purge's collection deletes are admitted whatever the
    Devices' lifecycle, and project teardown completes.
  - Without it, a project that holds a Device that is not Retired does not
    finish purging while the component is installed. Retire every Device
    first.

  This comes from reading the pinned Milo source and from the envtest
  simulation below, not from a live Milo test.
- **Capacity is not deleted with its Device.** The registration webhook denies
  deletes of inventory-backed capacity, so a namespace or project teardown that removes Devices
  still stops at their capacity; see the next section.

### Cleanup identity

The cleanup identity is a stopgap. Milo's project cleanup deletes namespaced
resources before it marks any namespace for deletion, and it never deletes
`default`, so the namespace exception cannot cover project teardown. Milo
should mark every namespace it purges, `default` included, before deleting
its contents. The policy can then rely on that namespace signal and drop the
subject exemption.

The binding ships without a `paramRef`. The policy then evaluates with
`params == null`, the third exception is off, and only the namespace and
Retired exceptions apply. A policy that declares `paramKind` does not need a
`paramRef` on its binding: Kubernetes evaluates such a binding with a null
`params` and no error. The envtest suite confirms this on Kubernetes 1.37.0.

A deploying repository turns the exception on in the root it applies to Milo:

1. Add a ConfigMap with a `cleanupUsernames` key that lists the usernames, one
   per line or separated by commas. Entries are trimmed and empty entries are
   ignored. Matching is exact and case-sensitive. Groups, prefixes and
   wildcards never match, and an empty username never matches. List only the
   identity that runs project cleanup: in pinned Milo, the controller-manager's
   base identity.
2. Patch the binding with a `paramRef` that names that ConfigMap, sets its
   `namespace` explicitly, and sets `parameterNotFoundAction: Deny`. Without
   an explicit namespace, the API server looks for the ConfigMap in the
   Device's own namespace.

A synthetic example, rendered with `kustomize build`:

```yaml
# kustomization.yaml in the deploying repository's Milo root. The component
# path stands in for however that repository vendors or references Fleet.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
components:
  - ../fleet/config/components/device-deletion-protection
resources:
  - device-cleanup-identity.yaml
patches:
  - target:
      group: admissionregistration.k8s.io
      kind: ValidatingAdmissionPolicyBinding
      name: fleet-device-deletion-protection
    patch: |-
      - op: add
        path: /spec/paramRef
        value:
          name: fleet-device-cleanup
          namespace: example-system
          parameterNotFoundAction: Deny
---
# device-cleanup-identity.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: fleet-device-cleanup
  namespace: example-system
data:
  cleanupUsernames: |
    system:serviceaccount:example-system:example-controller-manager
```

Consequences to plan for:

- **A missing ConfigMap denies every Device delete.** With
  `parameterNotFoundAction: Deny`, a `paramRef` whose ConfigMap does not exist
  denies every Device `DELETE`. That includes a Retired Device, a Device in a
  deleting namespace and the listed identity's deletes, so namespace and
  project teardown stop until the ConfigMap exists. This is the fail-closed
  behaviour: an absent or deleted ConfigMap never widens who may delete. The
  API server reports it as `422 Invalid` with the message ``no params found
  for policy binding with `Deny` parameterNotFoundAction``, not as the
  policy's 403. Apply the ConfigMap with the patch, or before it. Never use
  `parameterNotFoundAction: Allow`: a missing ConfigMap would then skip the
  policy and admit every Device `DELETE`.
- **Impersonation grants the exemption.** Anyone allowed to impersonate a
  listed username is admitted as that username and can delete any Device.
  Ordinary RBAC on `impersonate` applies. List only the cleanup controller's
  identity, and restrict impersonation of it, and writes to the ConfigMap and
  the binding, as tightly as the controller itself.
- **Where the ConfigMap lives in Milo.** The admission plugin reads the
  ConfigMap through its informers. In pinned Milo `v0.36.9` those informers
  run against the root control plane, not a project, so the ConfigMap belongs
  in the root. This is inferred from the Milo source, as is the policy's use of
  Milo's root admission for project-routed requests. Confirm on the deployed
  Milo that the cleanup identity admits a project purge before relying on it.

### Deleting a Device

Deleting a Device that backs capacity, whether it is Retired, its namespace is
being deleted, or the component is not installed, leaves the capacity in
place, still `Disabled`. The webhook denies every update and delete of it, so
it cannot become available. No owner reference links the two: the webhook
rejects owner references on registration, and garbage collection would be
denied anyway because deletes of inventory-backed capacity are fenced.

The deletion policy narrows when this happens but does not prevent it: a
Retired Device, a Device in a deleting namespace, and a delete by a cleanup
identity are all still admitted. No root in this repository installs the
policy, so in an environment built from these roots alone any authorized
caller can delete a Device, including one that a registration checked just
before its capacity was admitted.

The resulting orphaned capacity is in this state:

- It stays `Disabled` and is immutable. The webhook denies every update and
  delete of inventory-backed capacity for every caller, administrators
  included, and an update out of `Disabled` is denied with
  `its Device does not exist`.
- It keeps its Device UID annotation and its `spec.deviceRef` name. A
  replacement Device created under the same name cannot register, because
  the capacity already references that name.
- Its Device reference no longer resolves, so it has unresolved physical
  identity. Registration denies **every** create in the provider scope while
  such capacity exists, not only the same-name replacement, with
  `capacity default/<name> has unresolved physical identity`.
  `TestOrphanedCapacityBlocksUnrelatedRegistration` pins this.

Remediation, in stage 1:

1. Re-record the deleted machine as a Device under its **old name**, with its
   original serial and system UUID, created directly in `Retired` (a Device
   may be created in any lifecycle). The capacity's reference resolves again,
   so other Devices can register. The Retired Device cannot register, the
   capacity stays `Disabled`, and the machine's identity is back in the
   duplicate check. The same test pins this.
2. Register the real replacement machine under a **new** Device name. The old
   name stays bound to the old capacity.
3. Where the deletion policy is installed, do not delete the re-recorded
   Retired Device: the policy admits that delete, and the capacity would be
   orphaned again.

There is no stage-1 path that removes the orphaned capacity itself in an
environment where the registration webhook is installed. Removing it would
mean taking the webhook out of the admission path, which unfences every
inventory-backed capacity write while it is out, so it needs a separately
reviewed change, not an operator action. Clean up orphaned capacity directly
only in environments where the registration webhook is not installed, such as
synthetic ones.

## Production packaging and rollout

- `config/milo` includes the mandatory fail-closed registration webhook
  (`config/components/registration-admission`). Its one entry matches
  capacity create, update and delete in every namespace, narrowed by the
  [inventory-backed match condition](#scope-inventory-backed-capacity-only)
  and with no selectors. Status writes are not matched. An unreachable
  service blocks writes to inventory-backed capacity and nothing else:
  unmarked capacity, capacity status, Device, namespace and Project writes do
  not pass through the registration webhook.
- `config/milo` also includes the fail-closed
  [Machine power webhook](device-custody.md#machine-power) on the same
  service. It matches only Machine status writes that change `status.power`,
  so an unreachable service also blocks power changes, and no other Machine
  write.
- No root in this repository includes Device deletion protection. A
  deploying repository adds `config/components/device-deletion-protection` to
  the root it applies to Milo when it must keep in-service Devices.
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
- Writes to inventory-backed capacity fail closed. Legacy and other unmarked
  capacity is not governed by the webhook; its writes are subject to RBAC
  alone.

Environment-owned inputs, none of which this repository supplies:

1. Keep the existing `fleet-manager-config` authority and discovery paths.
   Supply `fleet-registration-milo-kubeconfig` (Project reads) and
   `fleet-registration-project-kubeconfig` (Device and capacity get/list
   across the complete provider scope, and SubjectAccessReview create). It
   needs no public writes, allocation permissions or provider credentials.
2. Supply `fleet-registration-tls` with `tls.crt`, `tls.key` and a dedicated
   `client-ca.crt`. Configure Milo's admission webhook client with an
   API-server client certificate signed by that CA, patch the webhook URL for
   reachability, and set `clientConfig.caBundle` to the serving CA. Do the
   same for the Machine power webhook, or every power change fails closed.
3. Keep `--provider-projects` equal to the complete provider scope. Before a
   power projector runs against the root, set `--machine-power-writer` to the
   username Milo reports for the manager's identity in a routed project
   request. Confirm it with one of the manager's own status writes: a
   mismatch refuses every power change.
4. Decide whether the Milo root you apply includes
   `config/components/device-deletion-protection`. With it, the only Device
   deletes admitted are of Retired Devices, of Devices in a deleting namespace,
   and by a configured [cleanup identity](#cleanup-identity); without it, any
   authorized caller can delete a Device. If you include it, configure the
   cleanup identity, or project deletion stays blocked until every Device in
   the project is Retired.
5. Before rolling out the UI, verify caller-preserving project routing, named
   Device read denial, registration denial, capacity mutation denial, TLS
   rejection, Device deletion denial where installed, lifecycle transitions, and duplicate handling
   in the target environment. This change performs **no live writes or
   deployment**, so it does not claim those checks.

### Leftover ledger objects

Removing the ledger deletes nothing that the ledger service or its operator
created at runtime. GitOps prune removes only what the old manifests applied.
The ledger revisions created these objects on the host cluster:

| Object | Namespace | Created by | Removed by prune |
| --- | --- | --- | --- |
| Secret `fleet-capacity-registration-v1` (the ledger root) | `fleet-system-private` | the operator, once, at bootstrap | no |
| Immutable Secrets `fleet-registration-segment-<digest>-<NN>` (sealed segments) | `fleet-system-private` | the registration service, on expansion | no |
| ConfigMap `fleet-registration-control` (`public.pem`, `request.json`) | `fleet-system` | the environment owner | only if the owner applied it with GitOps and has stopped |
| ValidatingAdmissionPolicies and bindings `fleet-registration-ledger-protection` and `fleet-registration-namespace-protection` | cluster | `config/base/registration` | yes |
| Role and RoleBinding `fleet-registration-ledger` | `fleet-system-private` | `config/base/registration` | yes |

Remove the Secrets and the ConfigMap by hand. The current service never reads
them, and they hold Device identity digests, project names and signed control
requests that no longer protect anything. If you want a record of the
identities the ledger retained, export the Secrets to private storage outside
Git first. After the Device custody cutover nothing keeps records in the
`fleet-system-private` namespace, so delete it as the last step of [moving
from the Secret-backed staging](../../config/README.md#moving-from-the-secret-backed-staging).

Delete in this order, on the host cluster, after the current registration
release is running and no older registration pod remains:

```sh
# 1. The retention policies must be gone, or they deny the Secret delete.
kubectl get validatingadmissionpolicybindings \
  fleet-registration-ledger-protection fleet-registration-namespace-protection
# Expect NotFound for both. If either exists, prune did not run; fix that
# first rather than deleting the policies by hand.

# 2. The ledger root and its segments.
kubectl -n fleet-system-private delete secret fleet-capacity-registration-v1 --ignore-not-found
kubectl -n fleet-system-private get secrets -o name \
  | grep '^secret/fleet-registration-segment-' \
  | xargs -r kubectl -n fleet-system-private delete

# 3. The control ConfigMap, after removing it from the environment overlay.
kubectl -n fleet-system delete configmap fleet-registration-control --ignore-not-found
```

Environments that never deployed the registration service have none of these
objects and nothing to clean.

## Stage 2 blockers

Stage 1 is safe because inventory-backed capacity never leaves `Disabled`.
These must be resolved before any stage lets it:

1. **Close the duplicate race.** Allocation must not be enabled for
   inventory-backed capacity until the
   [same-serial race](#the-documented-duplicate-race) is closed. A recheck at
   enable time is not enough on its own.
2. **Give orphaned capacity a way out.** Capacity whose Device was deleted
   [cannot be removed](#deleting-a-device) while the webhook is installed, and
   it blocks every registration in the provider scope until its Device is
   re-recorded.
3. **Authenticate the webhook's caller**
   ([#149](https://github.com/datum-cloud/fleet/issues/149)). Until then the
   [provider-project scope check](#provider-project-scope) is the only limit
   on what a certificate holder can learn.

## UI

The Datum dialog uses the existing host-session boundary and isolated read
cache. Without a verified session or registration permission, the Register
capacity button is disabled and its tooltip gives the reason. The dialog shows
the derived capacity name instead of a name field. A confirmed registration
closes the dialog without a message; the new row in the Capacity table is the
confirmation. Missing
host authority, denied registration, unavailable Devices, Devices that are not
Active, partial identity and conflicting identities, including a conflict with
a Retired Device, have explicit explanations. The inventory detail panel shows
a Device's lifecycle; the UI does not edit it. Selecting a Device
snapshots its baseline; neither refetch nor browser blur/focus changes it. A
named current read immediately precedes dispatch. Session changes fence
pending work. After dispatch, an uncertain result keeps the selection, disables
retry in that form, and asks the operator to check whether the derived capacity
exists. A repeat cannot create a second record. The unfenced legacy deletion
helper is never used for rollback.

Registration sets no `fleet.datumapis.com/capacity-pool` label, so no
MachineClass selects registered capacity. The webhook does not inspect labels
on create, but it denies every later update, so a label set at registration
could never be corrected, and registered capacity stays `Disabled` in this
stage anyway. Which writer sets the pool label, at registration or when a
future stage enables capacity, is undecided.

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
- a Decommissioning or Retired Device cannot be registered, a Retired Device
  still blocks a duplicate, and capacity cannot leave `Disabled` once its
  Device is Decommissioning or Retired;
- a webhook outage blocks capacity registration but not Device writes.

`internal/registration/scope_envtest_test.go` installs the same packaged
webhook and checks that:

- updates and deletes of registered capacity are denied, including by the
  admin;
- removing all or one registration annotation, or changing one, is denied by
  update and by merge patch, and adding one to unmarked capacity is denied;
- an unannotated create under a `device-` name is denied;
- with the webhook backend closed, a synthetic capacity's server-side-apply
  create and spec update, its status write, and the update and delete of
  hand-made capacity in `default` all succeed, so they are not routed;
- with the backend closed, a status write on registered capacity succeeds but
  leaves its annotations and spec unchanged, while its update and delete, and
  adding the marker to unmarked capacity, fail closed.

`internal/registration/tenancy_envtest_test.go` installs the same packaged
webhook with a handler whose project client and SubjectAccessReview fail the
test if they are reached, and checks that:

- a cluster-admin caller routed through a project outside the provider scope
  cannot register, and cannot learn why registered capacity stays `Disabled`;
- when `Resolve` returns a different route name than the one requested, a
  registration and an update that would leave `Disabled` are both denied.

`internal/registration/tenancy_test.go` checks the same two cases on the
handler directly. Disabling the scope check, or either route-name check, fails
these tests.

`internal/registration/deletion_envtest_test.go` applies the component from
its manifest and checks that:

- the CRD transition rules accept `Active`/`Decommissioning` moves and
  `Decommissioning` to `Retired`, and reject `Active` to `Retired` and every
  move out of `Retired`;
- deleting an Active or Decommissioning Device is denied for every caller,
  and deleting a Retired Device is allowed;
- with the shipped binding, which has no `paramRef`, the policy evaluates with
  a null `params` and no error, and a would-be cleanup username is denied;
- `deletecollection` over mixed Devices denies those that are not Retired;
- after the namespace is deleted, its Devices can be deleted. envtest has no
  namespace controller, so the test issues the controller's deletions itself
  and they are admitted because `namespaceObject` has a deletion timestamp;
- Devices in other namespaces stay protected;
- with the ConfigMap and a `paramRef` set as a deploying repository sets them,
  a listed username deletes Active and Decommissioning Devices and a mixed
  collection. The admin, an unlisted user, and usernames that are a prefix,
  superset, embedding, suffix or different case of a listed name are denied,
  as is a user who holds a listed name only as a group. The Retired and
  deleting-namespace exceptions still admit unlisted callers;
- a Milo-style purge, the listed user's collection delete in `default` and in
  another namespace with mixed lifecycles, removes every Device;
- a `paramRef` to a missing ConfigMap under `parameterNotFoundAction: Deny`
  denies every delete with `422 Invalid`, including a Retired Device, the
  listed user, a Device in a deleting namespace and the purge.

`internal/registration/deletion_policy_test.go` evaluates the component's
expression offline with the API server's admission CEL compiler, for cases
envtest cannot produce, such as an empty username: it never matches empty
entries, and padded, CRLF-separated entries match only exactly.

Unit tests cover the remaining negative cases, the handler's own scope test
(unmarked create, update, status and delete are allowed without reading
Devices; marker additions, removals and changes are denied), normalization, cross-project
duplicates, project lifetime and the packaged webhook shape.
`cmd/fleet-registration` tests reject unauthenticated and untrusted webhook
transport. `internal/api/inventory/v1alpha1/lifecycle_test.go` runs the
lifecycle default and transition rules through the CRD schema offline. The
overlay isolation tests fail when a root includes the deletion component and
when the component's policy is renamed, made audit-only, fail-open, scoped,
exempted or given a weaker expression. The weaker expressions include
wildcard, prefix, group-based, null-params-allows-all, empty-username and
always-true cleanup clauses. The tests also fail on a different or missing
`paramKind`, and on a `paramRef` in the component with either
`parameterNotFoundAction`. Frontend tests cover the derived name,
conflict marking including Retired Devices, and refusal of Devices that are not
Active, with mocked transport. None of this is a live Milo deployment or an independent
security review.

The publishing workflow applies the server build tag to
`config/base/registration`. A regression applies the workflow's image
transforms to a temporary config tree and asserts the management-plane bundle
uses the published tag and synthetic staging has no registration workload.
