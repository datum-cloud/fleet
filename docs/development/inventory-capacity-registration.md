# Inventory-backed Metal capacity registration: blocked stage1

[Implementation status](README.md) · [Physical inventory](../operations/physical-inventory.md)

Inspected on 2026-11-22 against staging commit
`dfb9253919946edd4804b98fee7916f013b9c257` on
`feat/manual-inventory-records`. **Real registration is not implemented.** This
change removes the obsolete staff-portal synthetic creation form and its direct
MachineCapacity POST helper. It replaces them with a Datum UI alert explaining
the blocker. It is a UI guardrail, not server-side enforcement; authorized direct
API writes remain possible. Do not use them to bypass this restriction.

## Evidence at the actual boundaries

- `config/base/crd/metal.datumapis.com.yaml` validates one MachineCapacity at a
  time. `deviceRef` is name-only and immutable; `allocationPolicy` defaults to
  Disabled but accepts Enabled. It cannot establish that another capacity does
  not reference the same physical asset, resolve a Device, or retain its UID.
- `config/milo/kustomization.yaml` installs the project APIs and IAM. The
  management-plane policies in `config/components/admission/policies.yaml`
  protect Machine finalizers and private Secrets, not MachineCapacity creation
  in provider project control planes. Installing an additional policy only in
  the management plane would not gate the staff proxy's project API POSTs.
- `ui/provider/src/lib/api.ts` uses the staff principal's project-scoped proxy.
  There is no registration service endpoint behind the removed form. A UI list
  followed by a POST would race another writer and could not authenticate an
  internal incarnation binding. A caller-authored UID annotation would not fix
  that boundary.
- `internal/api/inventory/v1alpha1/capacity.go` provides the issue131
  `CapacityBackingError` check. It rejects nil, explicitly unidentified, and
  identity-less Devices. Passing it proves only recorded matching evidence,
  not hardware verification, uniqueness, occupancy, or allocator eligibility.
  No production registration resolver invokes it.
  The inventory schema already makes recorded identity immutable; that does
  not prevent deleting/recreating the name or recording duplicate hardware
  under another Device name.
- `internal/controller/lifecycle.go` has durable allocation custody, but
  `machineAssetID` derives the Device identity from its namespace/name and
  provider project identity, not its Device UID. Its retained `MachineUID` is
  the MachineCapacity UID. Allocation contention protection is not a
  registration uniqueness gate or a Device delete/recreate fence. Reusing it
  for registration would conflate capacity registration with allocation custody.
- `cmd/fleet-manager/main.go` selects `DefaultHandlers` in production. The
  explicit synthetic-staging branch supplies synthetic authority and provider
  integration. It does not supply real registration admission. Production
  allocation remains blocked; this change does not enable that branch.

## Cleaning policy is a contract blocker

The CRD requires `cleaningPolicyRef.name`, but the implemented API has no
CleaningPolicy resource, resolver, policy-use authorization, or real-provider
qualification contract. `synthetic-tenant-reuse-v1` is a fixture value, not a
deployable real cleaning policy. Accepting arbitrary free text or inventing a
second real-looking default would conceal the same missing integration.

The [provisioning proposal](../proposals/fleet-management/bare-metal-provisioning.md)
requires owner-selected cleaning/restoration policy, distinct from observation
and enrollment. Registration must perform neither cleaning nor preparation.
Before enabling a real form, define who owns this policy and how a registration
can validate its reference without effects, or approve an explicit API change
allowing an unconfigured policy while Disabled. Neither option is implemented
here. No additional credentials or permissions resolve the missing contract.

## Required server design before enabling registration

The following are requirements for a follow-up design, **not implemented guarantees**:

1. Establish an authenticated, caller-preserving registration command boundary
   in the provider project. Retain operation identity, exact submitted inputs,
   actor authority, project/namespace lifetime and selected Device UID/resource
   version before any create. Authored resource references remain name-only.
2. Read the same-namespace Device authoritatively and call
   `CapacityBackingError`. Missing, unreadable, deleting, identity-less or stale
   devices must not create capacity. Define how repeated records of the same
   physical hardware are recognized; a lock on one Device UID alone does not
   deduplicate two records with the same physical identity. Do not infer hardware
   from DeviceType templates or treat identity as occupancy clearance.
3. Choose a durable unique physical-asset reservation and exact incarnation
   binding with crash recovery. All project API writers must pass the same
   enforcement, including direct API clients. A list-then-create webhook and
   a process mutex are insufficient. Admission side effects alone cannot prove
   the subsequent API write committed. Device identity updates, deletion and
   name reuse must participate in the fence; a separate GET followed by a
   capacity POST is not atomic across these objects.
4. Retain ambiguous dispatched outcomes and reconcile the same operation before
   retrying. A timeout, denial after dispatch, session change, or missing response
   must never release a reservation or authorize another capacity. After restart,
   recover using retained exact inputs, not the latest same-named Device. Define
   deletion/tombstone retention without losing occupied capacity or custody.
5. Create only Disabled/Retain capacity with no positive health, preparation or
   availability status. Do not provide an enabling control. Decide the admission
   rollout for legacy/synthetic records separately, without bulk migration,
   mutation of existing allocations, or takeover of inventory ownership.
6. Resolve the cleaning-policy contract and deploy the enforcement in the actual
   provider project API path with narrowly scoped authority. Only then add the
   default-namespace Device picker and operation/session fencing using existing
   inventory UI patterns. Inventory reads and Metal create permission are
   separate grants; neither should silently grant the other.

## Implemented behavior and limits

The provider Capacity page no longer offers synthetic creation or "Offer for
allocation". It has no Device picker because no safe registration command exists.
The old same-name Device reference and automatic synthetic cleaning-policy
payload are removed rather than reused for real assets. Existing list/delete
behavior and capacity records are unchanged, including existing Enabled values.
The pre-existing delete path still lacks custody checking and is not an approved
recovery mechanism for a future registration. No production API, RBAC, schema,
controller, hardware, network, power or provisioning behavior changes.

Synthetic fixture data and standalone/local harnesses remain available. The
staff form is blocked in fixture builds too, so demonstrations do not imply
real registration support. Default-only inventory scope is unchanged; this
guardrail makes no inventory reads and discloses no Device identifiers.

The tests cover the production UI/API path with mocked HTTP transport: parallel
page mounts, empty and existing capacity, loading, 401/403/503 failure and read
recovery perform no registration dispatch. They do not prove server concurrency,
incarnation binding, session fencing for existing capacity reads, or live host
integration. Before stage1 can be called complete, envtest and project-routing
integration tests must demonstrate direct-write bypass rejection, concurrent
same-asset creates, stale UID/RV, identity updates and delete/recreate, durable
unknown-outcome recovery, repeated requests, permission/session changes, and
legacy/occupied-record preservation at the deployed authoritative boundary.

## Local validation

On 2026-11-22, the provider suite passed 683 tests in 42 files and the consumer
suite passed 44 tests in 3 files. Both typechecks and production builds passed.
`go test -race ./...` passed with Kubernetes 1.37.0 envtest assets selected using
the repository's `setup-envtest@v0.25.2` command; `go vet ./...` passed. Rendering
`config/milo` and `config/overlays/management-plane`,
`hack/validate-overlay-isolation.sh`, `hack/validate-doc-links.sh`, and
`git diff --check` passed. These are regression checks, not qualification of an
unimplemented registration server. No browser acceptance, live mutations,
deployment, hardware execution, or independent adversarial review was performed.
