# Planning and registering physical devices

[Operations guide](README.md) · [Architecture](../architecture/README.md) ·
[Implementation status](../development/README.md)

Before Fleet can enroll or offer real hardware, operators need to record two
things: where a server is meant to go, and which physical server is actually
there. Recording either must not provision the server, publish capacity, or
take ownership of it.

This package implements a minimal subset of the
[inventory lifecycle and trust proposal (#63)](https://github.com/datum-cloud/fleet/pull/63).
It now has six operator-written kinds in `fleet.datumapis.com/v1alpha1`.
The [NetBox physical import and migration guide](netbox-physical-import.md)
describes the additive physical schema, typed components, partial placement,
same-namespace relationships, import reconciliation and current limits. The
manual examples below preserve the original long-lived-slot representation:

| Kind | What it is | Writer |
| --- | --- | --- |
| `DeviceClaim` | The installation plan for one slot: facility, rack, position and face, expected model, and expected ports with their expected cable peers. It is usually written before the hardware arrives, and the slot outlives any one Device. | The installation planner (an operator, for now) |
| `Device` | One actual asset, bound to exactly one DeviceClaim when detected hardware is registered. It carries the serial and SMBIOS system UUID, the model, an optional BMC endpoint, interfaces, and provenance. | The registrar (an operator, for now) |
| `Facility` / `Rack` | Explicit premises and rack definitions; membership is not free-capacity evidence. | Inventory operator through the environment's existing writer |
| `Cable` / `PowerFeed` | Typed termination/lane topology and physical supply dependencies. | Inventory operator through the environment's existing writer |

## Status

| Aspect | State |
| --- | --- |
| CRDs and Go types | Implemented in `config/components/inventory` and `internal/api/inventory/v1alpha1`. |
| Home | Milo. `config/milo` registers six CRDs, six ProtectedResources and two IAM roles. Management and synthetic-staging overlays install none of them. |
| Staff portal | Read-only **Inventory** tab in the Bare Metal provider plugin (`ui/provider`): a claim list and a per-claim detail page. |
| Tests | Unit tests run API-server schema, list-type and CEL validation offline for the six kinds and synthetic importer output. IAM tests check ProtectedResource and role shape and that no metal role grants inventory access. Plugin tests cover browse/detail, partial placement, reconciliation, restricted, pagination and empty states. |
| Controllers | None. Offline import and staff browsing read the records; no reconciler accepts plans, verifies hardware, or publishes eligibility. |
| Hardware qualification | None. Records hold operator-entered facts, not verified hardware state. |

## What registration does and does not do

Creating a Device is the explicit, manual binding step. It records that the
registrar saw this hardware in this planned slot. It does not:

- authorize power, boot, wipe, BMC credential, or network changes;
- create, change, or enable any `MachineCapacity`, or make anything
  allocatable; or
- verify identity, placement, cabling, or power.

Detection is manual too. The registrar records what the BMC (for example
Redfish) and the rack showed them. MachineCapacity is unchanged by this
package. Its `spec.deviceRef` already names a Device by name, and the
allocator forms the asset key
`<capacity namespace>/fleet.datumapis.com/Device/<name>`. Devices live in the
provider project next to its MachineCapacities, so a `deviceRef` in the same
namespace resolves by name. Nothing checks that reference yet.

## Where the records live

Milo is the single home. All six kinds are namespaced objects in the
**provider project's** control plane, in the same namespace as the
MachineCapacities they will back. No copy exists on the Fleet management
cluster, and no controller syncs them anywhere.

All six CRDs carry `discovery.miloapis.com/parent-contexts: Project`, so every
project control plane serves the kinds. Access is what keeps consumers out:

| Role | Grants |
| --- | --- |
| `fleet.datumapis.com-inventory-viewer` | `get`, `list`, `watch` on all six physical kinds |
| `fleet.datumapis.com-inventory-editor` | The viewer role plus `create`, `update`, `patch`, `delete` |

No `metal.datumapis.com` role includes or inherits either. The consumer,
claimant, and viewer roles that reach customer projects therefore grant no
inventory access, and the provider role does not either. Bind the inventory
roles explicitly, scoped to the provider project. No role grants status
writes, because status has no writer yet.

The ProtectedResources use `serviceRef: fleet.datumapis.com`. The authorization
provider looks resources up by `{apiGroup}/{plural}`, so the service name must
equal the API group. No `fleet.datumapis.com` Service or ServiceConfiguration
is registered; the kinds are provider-internal, not a catalog offering.

Staff see the records in the staff portal's Bare Metal plugin, under
**Inventory**. The plugin reads through the portal's Milo proxy with the staff
member's own token, so a staff member needs `fleet.datumapis.com-inventory-viewer`
in the provider project. The plugin never writes.

## Writers and validation

| Rule | Kind | Why |
| --- | --- | --- |
| `provenance` is required: `Manual` or `NetBoxImport`, `recordedBy`, `recordedAt`, optional opaque reference; imports also require structured source provenance | All six | Attribution is retained; imported claims are observations, not authenticated hardware evidence. |
| Any spec change must set a later `provenance.recordedAt` | All six | An edit cannot pass off new facts under old provenance. |
| `claimRef` is required and immutable | Device | A Device binds to exactly one claim, and moving hardware to another slot is not an edit. |
| `identity` is immutable, including adding missing fields; requires serial, system UUID, or an import source identity matching provenance | Device | Source identity does not prove chassis identity. Legacy bindings are preserved; hardware handover is not automated. |
| Serial is optional | DeviceClaim | It is usually unknown at plan time. |
| Port and interface names are unique; MACs are lowercase and colon-separated, appear at most once per Device, and are either mapped to an interface or listed in `unmappedMACAddresses`, not both | Both | Malformed or ambiguous facts are rejected. |
| The system UUID is in lowercase canonical form | Device | One spelling per identity. |
| `bmc.endpoint` is an `https` URL without credentials | Device | Credentials never live in inventory records. |
| `status` is a separate subresource that declares no fields | All six | Reserved for future collectors and verifiers to report drift and mismatch. Status never carries identity, and nothing writes it yet. |

The CRDs cannot check that `claimRef` names an existing DeviceClaim, because
CEL cannot read other objects. Until a registrar enforces it, the operator
creates the claim first.

Use `unmappedMACAddresses` for MACs detected on the asset that cannot yet be
attributed to a named port. Do not guess a mapping; #63 requires an
authenticated, complete NIC mapping before any network path depends on it.

## Example

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: DeviceClaim
metadata:
  name: rack-01-u12
  namespace: fleet-system
spec:
  provenance:
    method: Manual
    recordedBy: planner@example.com
    recordedAt: "2026-01-02T03:04:05Z"
    reference: rack plan 1234
  expectedIdentity:
    manufacturer: Example Vendor
    model: Example 1U
  placement:
    facility: example-facility
    rack: rack-01
    position: 12
    face: front
  ports:
    - name: eth0
      type: 25gbase-x-sfp28
      speedMbps: 25000
      expectedPeer: {device: switch-01, port: Ethernet1/1}
    - name: bmc0
      type: 1000base-t
      speedMbps: 1000
      expectedPeer: {device: mgmt-switch-01, port: Ethernet5}
---
apiVersion: fleet.datumapis.com/v1alpha1
kind: Device
metadata:
  name: rack-01-u12-synthetic-0001
  namespace: fleet-system
spec:
  claimRef:
    name: rack-01-u12
  provenance:
    method: Manual
    recordedBy: registrar@example.com
    recordedAt: "2026-01-09T10:00:00Z"
    reference: BMC inventory read during rack turn-up
  identity:
    manufacturer: Example Vendor
    model: Example 1U
    serial: SYNTHETIC-0001
    systemUUID: 00000000-0000-4000-8000-000000000001
  bmc:
    endpoint: https://192.0.2.100:443
  unmappedMACAddresses:
    - 00:00:5e:00:53:01
    - 00:00:5e:00:53:02
```

To replace the hardware, create a new Device with its own name and identity
that names the same claim. Leave the old Device in place until its retirement
is settled. This package does not model retirement.

## Relationship to the inventory proposal

The names and writers match #63:

- DeviceClaim is a physical installation plan written by the installation
  planner.
- Device is the canonical asset created only by the registrar.
- Collectors submit observations and never edit Device identity.
- Status is a protected projection.
- Serial and SMBIOS UUID are matching evidence, not authenticators.
- Logical LAGs and VLANs are not ports.

Still deferred to #63:

- Qualified geometry/reservations and automatic migration of legacy text
  placement/inline peers. The four additional physical record kinds now exist;
  typed records do not silently take over historical surfaces.
- Plan acceptance, revisions, and reservations; canonical-identity
  adjudication and duplicate detection; and the protected ledger.
- Trust roots, qualification profiles, enrollment challenges, technician and
  collector attestation, and evidence freshness.
- Component incarnations and lineage, maintenance, moves, retirement, and
  adopted custody.
- The `Accepted`, `Bound`, `IdentityVerified`, `PlacementVerified`,
  `NetworkPathVerified`, and `PowerPathVerified` predicates and any eligibility
  publication.
- Separation of planner, approver, registrar, and verifier roles. One operator
  can write both kinds today.

**Deliberate divergence: Milo-resident provider inventory.** #63 treats
private inventory, including serials, cable paths, and BMC endpoints, as
material that never reaches consumer catalogs or status, and keeps the
authoritative records in a protected management-side ledger. This package
stores the operator records in the provider project's Milo control plane
instead. Two things keep them away from consumers: they never enter a consumer
project or a catalog, and only the inventory roles can read them. This is a
deliberate change of home, not an oversight. It trades the ledger's isolation
for one authoritative record that MachineCapacity can name and that staff can
read. #63's ledger, trust, and enrollment protocol would still sit behind these
records if adopted.

**Compatibility decision:** legacy slots retain all historical bindings. New
imports use a distinct `Installation` lifecycle, but no automatic move or
replacement handover exists. Device identity and its original claimRef remain
immutable. The [migration decisions](netbox-physical-import.md#compatibility-and-migration-decisions)
explain why imports cannot delete/recreate or relabel legacy records to bypass this.

## Applying the records

`config/milo` installs the CRDs, ProtectedResources, and roles into Milo. Apply
claims and Devices into the provider project after those are established,
claims before the Devices that name them. Keep records with real serials,
UUIDs, MACs, endpoints, or cabling in the environment's private configuration,
not in this repository.

`config/inventory` still renders the inventory CRDs alone, for an environment that
applied them to the management cluster before Milo became their home. Once that
environment has moved its records into Milo and stopped reconciling the root, it
will be removed. New environments must not use it.
