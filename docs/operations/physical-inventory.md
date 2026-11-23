# Planning and registering physical devices

For reported locations and authorized, concurrency-safe location record corrections, see
[Device-reported placement](../development/device-reported-placement.md).

[Operations guide](README.md) · [Architecture](../architecture/README.md) ·
[Implementation status](../development/README.md)

Before Fleet can enroll or offer real hardware, operators need to record two
things: where a server is meant to go, and which physical server is actually
there. Recording either must not provision the server, publish capacity, or
take ownership of it.

This package implements a minimal subset of the
[inventory lifecycle and trust proposal (#63)](https://github.com/datum-cloud/fleet/pull/63).
It has six physical kinds plus the observational operational `Site` kind in
`fleet.datumapis.com/v1alpha1`. See the implemented
[Site–Facility relationship](../development/site-facility-relationship.md) for
stable membership references, provider premises identifiers and rollout gates.
The primary workflow is **manual entry, operator review, and explicit record
ownership**. Authorized direct API creation is supported for new records;
legacy Flux-owned records require a reviewed ownership transition before edits
by another writer. Fleet does not read or reconcile any external inventory
source. A separate writer may copy records in from another system; it writes
ordinary records through the same API and validation, and keeps any record of
where facts came from on its own labels and annotations.

| Kind | What it is | Writer |
| --- | --- | --- |
| `DeviceClaim` | An observational installation plan with expected model, typed placement and Cable references. | Installation planner, through the record's explicitly assigned writer |
| `Device` | One actual asset, bound to exactly one DeviceClaim when detected hardware is registered. It carries the serial and SMBIOS system UUID, or an explicit marker that neither is on record, the model, an optional BMC endpoint, and interfaces. | The registrar (an operator, for now) |
| `Facility` / `Rack` | Explicit premises and rack definitions; membership is not free-capacity evidence. | Inventory operator through the environment's existing writer |
| `Cable` / `PowerFeed` | Typed termination/lane topology and physical supply dependencies. | Inventory operator through the environment's existing writer |

## Status

| Aspect | State |
| --- | --- |
| CRDs and Go types | Implemented in `config/components/inventory` and `internal/api/inventory/v1alpha1`. |
| Home | Milo. `config/milo` registers six CRDs, six ProtectedResources and three IAM roles. Management and synthetic-staging overlays install none of them. |
| Staff portal | Six-kind hardware directory in the Bare Metal provider plugin (`ui/provider`), with permission-aware browsing and limited authorized Facility/Rack creation and portal-owned name edits. See [implementation scope](../development/staff-inventory-ui.md). |
| Tests | Unit tests run API-server schema, list-type and CEL validation offline for the six kinds, including pruning of retired fields. IAM tests check ProtectedResource and role shape and that no metal role grants inventory access. Plugin tests cover browse/detail, partial placement, reconciliation, restricted, pagination and empty states. |
| Controllers | None for inventory. Staff browsing is read-only; no reconciler accepts plans, verifies hardware, discovers or synchronizes external inventory, or publishes eligibility. Flux applies only operator-reviewed Git records. |
| Hardware qualification | None. Records hold operator-entered facts, not verified hardware state. |

## What registration does and does not do

Creating a Device is the explicit, manual binding step. It records that the
registrar saw this hardware at this planned installation. It does not:

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
| `fleet.datumapis.com-inventory-manager` | The viewer role plus `create`, `update`, `patch`; no deletion or status writes |
| `fleet.datumapis.com-inventory-importer` | The viewer role plus `create` and `patch`, for server-side apply; no `update`, deletion or status writes |

No `metal.datumapis.com` role includes or inherits any inventory role. The consumer,
claimant, and viewer roles that reach customer projects therefore grant no
inventory access, and the provider role does not either. Bind the inventory
roles explicitly, scoped to the provider project. No role grants status
writes, because status has no writer yet.

The manager is a separate least-privilege role for explicitly authorized inventory
staff; it does not change the existing editor or give staff viewers write access.
It grants only the six physical kinds, never hardware operations, allocation,
installation, Metal, networking, billing, or IAM administration. Fleet owns this
role and its tests; environment-specific groups and project bindings belong in
the infrastructure repository. The role ships through `config/milo`; shipping a
role does not bind it or add members in any environment.

The importer is the role for an external importer's identity. It can read
every physical kind, to detect existing records and identity collisions, and
write records with server-side apply, which needs `create` and `patch`. It
cannot delete any record, replace one with `update`, or write status.

The API server can't restrict `patch` to server-side apply, so the role alone
does not stop an importer from changing records another writer owns. Importers
must apply under their own field manager, never force ownership, and leave
records they did not create alone. That way the managed fields show which
writer owns each field. Which kinds an importer writes is the importer's
decision; Fleet does not encode any source system in the role. Binding it,
scoped to the provider project, is the explicit opt-in for that identity to
write.

A Project resource selector is project-wide, not a namespace filter. These
ProtectedResources have Project parents, and a binding's `metadata.namespace`
locates the IAM object, not the inventory it authorizes. Choosing `default` in a
portal request is a convention, not isolation from other namespaces in that
project. Require a separately supported and tested authorization boundary before
promising namespace-only access.

The manager contract is validated offline with
`go test ./internal/api/inventory/v1alpha1`. That checks manifests, not deployed
authorization. Before adding named members, an environment owner must validate
the deployed role and binding, and explicitly decide record-writer ownership.
IAM capability does not transfer ownership away from Flux or permit two writers
for the same records. Existing GitOps-owned records remain GitOps-owned until a
separate reviewed transition; this role does not perform that transition.

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
| Records carry no attribution fields | All six | Resource audit logs record who changed a record and `metadata.creationTimestamp` records when it was created. A writer that needs to remember where facts came from uses its own labels and annotations. |
| `claimRef` is required and immutable | Device | A Device binds to exactly one claim, and moving hardware to another installation is not an edit. |
| `identity` is immutable, including adding missing fields; requires a serial or system UUID, or `unidentified: true` with a reason and neither | Device | Neither proves chassis identity. Hardware handover is not automated. See [Devices without hardware identity](#devices-without-hardware-identity). |
| An unidentified Device cannot record a `bmc` endpoint | Device | Equipment that cannot be matched to a chassis must not be enrolled as machine capacity. |
| Serial is optional | DeviceClaim | It is usually unknown at plan time. |
| `lifecycle` is required and is `Installation` | DeviceClaim | Every claim describes one installation. |
| `placement.scope` is required and names exactly its reference: `Facility` takes `facilityRef`; `RackMembership`, `RackUnits` and `NonRackUnitMount` take `rackRef`. Only `RackUnits` takes `position`, `height` and `face`, all three required; only `NonRackUnitMount` takes `mount`, and requires it | DeviceClaim | Partial placement cannot assert rack units. |
| `placement` is immutable | DeviceClaim | Moving an installation is a new claim, not an edit. |
| Interface names are unique; MACs are lowercase and colon-separated, appear at most once per Device, and are either mapped to an interface or listed in `unmappedMACAddresses`, not both | Both | Malformed or ambiguous facts are rejected. |
| The system UUID is in lowercase canonical form | Device | One spelling per identity. |
| `bmc.endpoint` is an `https` URL without credentials | Device | Credentials never live in inventory records. |
| `lifecycle` is `Active` (default), `Decommissioning` or `Retired`; `Active` and `Decommissioning` move between each other, only `Decommissioning` moves to `Retired`, and `Retired` is terminal | Device | Retirement is deliberate and one-way. Only an Active Device can be registered as capacity. See [Device lifecycle](../development/inventory-capacity-registration.md#device-lifecycle). |
| `status` is a separate subresource that declares no fields | All six | Reserved for future collectors and verifiers to report drift and mismatch. Status never carries identity, and nothing writes it yet. |

The CRDs cannot check that `claimRef` names an existing DeviceClaim, because
CEL cannot read other objects. Until a registrar enforces it, the operator
creates the claim first.

## Devices without hardware identity

Some equipment has no serial or system UUID on record: passive gear such as
patch panels and cable managers, and active gear such as PDUs, switches,
routers or console servers whose serial was never recorded or does not exist.
Cables reference Devices, so this equipment still needs a Device record.
Record it with an explicit marker instead of inventing a serial:

```yaml
spec:
  claimRef:
    name: rack-01-u40
  identity:
    manufacturer: Example Vendor
    model: Example 24-port panel
    unidentified: true
    unidentifiedReason: passive patch panel; no serial on the chassis
```

The rules:

- `identity.unidentified` may only be `true`. Omit it for a Device with a
  serial or system UUID; there is no `false` spelling.
- `identity.unidentifiedReason` is required with the marker and allowed only
  with it: 1 to 1024 characters, not all whitespace. Say why no identity is
  recorded, not where the record came from.
- A marked Device carries no `serial` and no `systemUUID`. Every Device has
  exactly one of the two forms.
- The marker is part of `identity` and is immutable. If a serial is found or
  recorded later, create a new Device with that identity against the same
  claim, as for a hardware swap. Cables that name the old Device keep naming
  it until they are updated.
- A marked Device cannot record `bmc`.

Use the marker only when no serial or system UUID is known. Do not use it to
skip reading a serial that the equipment reports, and do not put an asset tag
or provider host ID in `serial` to avoid it.

**What it prevents.** A Device without hardware identity cannot back a
MachineCapacity. Nothing can match it to a chassis, so enrollment, wiping and
allocation could reach the wrong machine. Two things enforce this today:

- Admission rejects a `bmc` endpoint on a marked Device, so it has no
  management endpoint to enroll through.
- `CapacityBackingError` in `internal/api/inventory/v1alpha1` is the
  resolution contract for `MachineCapacity.spec.deviceRef`. It returns
  `ErrUnidentifiedDevice` for a marked Device and `ErrDeviceIdentityMissing`
  for one with no identity at all. Any resolver of `deviceRef` must call it
  and treat a non-nil result as ineligible.

Nothing resolves `deviceRef` yet: the allocator uses the name only to form the
asset key and never reads the Device. Admission cannot check the reference
either, because MachineCapacity is a separate API group and CEL cannot read
other objects. Until a resolver exists, operators must not create a
MachineCapacity whose `deviceRef` names a marked Device, just as they must not
name a Device that does not exist.

A marked Device still binds to a DeviceClaim through `claimRef`. Do not bind it
to a claim whose `expectedIdentity.serial` is set; that claim expects
identified hardware. Admission cannot check this across objects.

Use `unmappedMACAddresses` for MACs detected on the asset that cannot yet be
attributed to a named port. Do not guess a mapping; #63 requires an
authenticated, complete NIC mapping before any network path depends on it.

## Manually record a small installation

1. Choose the provider project and namespace, and inspect existing records and
   their writers. The legacy staging `fleet-provider-inventory` reconciles
   `apps/fleet-system/staging/provider-inventory` with `prune: false` and
   `deletionPolicy: Orphan`. Flux remains the sole writer of those legacy records
   until an explicit ownership transition. Do not run a competing apply
   against them. New and separately created inventory can be direct
   API/operator-managed; GitOps is not required. Establish the authorized writer
   per record before making changes.
2. Enter the Facility and Rack facts you actually know. Leave unknown
   dimensions absent. Rack
   membership is not a reserved footprint or evidence of available space.
3. For each installation, set `DeviceClaim.spec.lifecycle: Installation` and
   typed placement. Use `Facility` when only the premises are known,
   `RackMembership` when the rack is known, or explicitly reviewed `RackUnits`
   with position, height and face. Never guess a rack position. Use a new record
   only for a genuinely new installation, not to evade an existing immutable binding.
4. Register an actual Device against that claim only when its identity is known.
   A manual Device requires a real serial or system UUID. If neither exists or
   was recorded, use the explicit
   [no-hardware-identity marker](#devices-without-hardware-identity); otherwise
   keep the plan and defer registration. Provider host IDs and asset tags are
   not serials. Use typed `components` and `topologyAuthority: Cable`; put expected
   connections in Cable records, the only topology authority.
   Add PowerFeed facts without inferring usable power or independent redundancy.
5. Review every reference in the same namespace, component type and position,
   cable endpoint, and potential duplicate against existing records. API validation
   cannot enforce cross-object existence or exclusive endpoints. Preserve existing
   names, Device identities, claim bindings and MachineCapacity references.
6. Review the intended record changes with another operator. They must contain
   no status, capacity publication, hardware action, deletion or implicit
   ownership transfer. Submit individually approved new records through the
   authorized API/operator workflow; use the established Flux source for records
   still owned by Flux. Compare acknowledged writes with read-only API and staff
   views. An interrupted or uncertain request is unknown, not failed or ready;
   inspect the record before retrying. Portal forms expose only the subset
   documented in the [staff UI scope](../development/staff-inventory-ui.md).

The [complete synthetic six-record example](../../hack/testdata/physical/manual.yaml)
shows this workflow without external sources or credentials. It includes a partial rack
placement, typed network and power components, and one Cable from a PowerFeed to
the Device. It is a test fixture, not an automatically installed overlay. Copy
its shape into private configuration and replace synthetic facts only after
review. No physical status is asserted.

Validate the synthetic example locally:

```sh
go test ./internal/api/inventory/v1alpha1 -run TestManualInventoryRecordsPassAdmission -v
```

The same offline check accepts a private multi-document YAML file of existing
records. It validates creation and unchanged updates against the proposed schemas;
it does not read a cluster, check cross-object relationships, or write records.
Keep the file outside Git. Diagnostics report only kind, ordinal and error count.

```sh
go test ./internal/api/inventory/v1alpha1 -run TestManualInventoryRecordsPassAdmission \
  -count=1 -v -args -inventory-records /private/tmp/reviewed-inventory.yaml
```

## Rack faces, depth and component faces

Three fields together describe where equipment sits in a rack and which way it
points. None is measured or verified; each is a record.

**Mounted rack face.** `face` (`front` or `rear`) on
`Device.spec.reportedPlacement` and on `DeviceClaim.spec.placement` is the rack
face that the chassis front points toward. This is the meaning NetBox gives its
`face` field. It also fixes the rails:

| Depth | Mounted face | Rails and occupancy |
| --- | --- | --- |
| Half depth | `front` | Front rails. Occupies its units on the front face only. |
| Half depth | `rear` | Rear rails. Occupies its units on the rear face only. |
| Full depth | either | Both rails. Occupies its units on both faces. `face` says which way the chassis points. |

A switch mounted reversed, with its ports toward the rack rear, has
`face: rear` when its DeviceType puts the ports on the chassis front. A
half-depth device mounted on one face's rails but pointing into the rack is
not representable. That mount is rare, and adding a second field for it would
make every other record ambiguous until both fields were set.

**Depth.** Depth uses the existing optional `modelShape.fullDepth` boolean on
both Device and DeviceType. It has no default on either kind: unset means
unknown. A Device value overrides its DeviceType. Readers resolve a Device's
depth in this order:

1. `Device.spec.reportedPlacement.fullDepth`, the depth reported for this
   installation;
2. `Device.spec.modelShape.fullDepth`;
3. the DeviceType's `modelShape.fullDepth`, read through `deviceTypeRef`;
4. otherwise unknown. This includes a Device with no DeviceType, and a
   DeviceType or Manufacturer that is missing or unreadable.

Unknown depth counts as full depth only when computing occupancy and
conflicts, so it never makes the far face look clear, and it is labelled
"Depth unknown" wherever it is shown. A default would hide that and make
conflict checks overconfident. NetBox's `is_full_depth` defaults to true; an
importer, of which none exists today, should copy an explicit NetBox value
directly but should not write a value NetBox only defaulted. A claim-derived
fallback uses the depth of its registered Devices only when all of them
agree.

**Component faces.** A `DeviceType.spec.componentTemplates[]` entry may set
`face` to `Front` or `Rear`. This face is relative to the chassis, never to the
rack, and unset means unknown. A Device component inherits the face of the
template whose `id` equals the component `id`; the Device has no face field of
its own and no override. A component's rack face is derived:

| Chassis face | Mounted `front` | Mounted `rear` |
| --- | --- | --- |
| `Front` | rack front | rack rear |
| `Rear` | rack rear | rack front |

If either face is unknown, the rack face is unknown.

**Faceplate layout.** A DeviceType may also record where each template sits on
its face, as cells of named port banks (`spec.faceplate.banks[]` and
`componentTemplates[].layout`), plus an optional `modelShape.width`. Layout
needs the template's `face`, is catalog data only, and has no Device override.
See [faceplate layout](../development/hardware-catalog.md#faceplate-layout-implemented).

**Conflicts.** Two placements whose units overlap on the same rack face are
shown as "records disagree". Two half-depth devices on opposite faces of the
same units do not disagree. Admission never blocks an overlap, because
placement is reported and not verified. A Cable whose two ends land on
opposite rack faces gets a soft "Check" note in the connection view, never an
error: a pass-through panel or a reversed peer can make that correct.

## Replacing hardware

To replace the hardware, create a new Device with its own name and identity
that names the same claim. Leave the old Device in place and move its
`spec.lifecycle` from `Active` to `Decommissioning`, then to `Retired`; see
[Device lifecycle](../development/inventory-capacity-registration.md#device-lifecycle).
Retired is terminal and still counts in the duplicate-identity check while the
Device exists. Environments with
[Device deletion protection](../development/inventory-capacity-registration.md#device-deletion-protection)
delete a Device only once it is Retired, or with its namespace. The one other
path is project teardown: when the deploying repository configures the
[cleanup identity](../development/inventory-capacity-registration.md#cleanup-identity),
that identity, and anyone allowed to impersonate it, can delete Devices
whatever their lifecycle, so a project purge completes. Without it, a project
that holds a Device that is not Retired cannot finish deleting.

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

- Qualified geometry and reservations.
- Plan acceptance, revisions, and reservations; canonical-identity
  adjudication and duplicate detection; and the protected ledger.
- Trust roots, qualification profiles, enrollment challenges, technician and
  collector attestation, and evidence freshness.
- Component incarnations and lineage, maintenance, moves, a retirement
  workflow beyond the recorded Device lifecycle, and adopted custody.
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

No automatic move or replacement handover exists. Device identity, its
claimRef and the claim's placement remain immutable.

## Applying the records

`config/milo` installs the CRDs, ProtectedResources, and roles into Milo. Apply
operator-reviewed records through their approved writer after those are established,
claims before the Devices that name them. New records may be created
directly through the provider API with authorization and ownership safeguards.
Existing Flux-owned records remain with Flux until a reviewed transition stops
reconciliation for those exact records. Do not overwrite or adopt them implicitly.
Keep records with real serials,
UUIDs, MACs, endpoints, or cabling in the environment's private configuration,
not in this repository.

`config/inventory` still renders the inventory CRDs alone, for an environment that
applied them to the management cluster before Milo became their home. Once that
environment has moved its records into Milo and stopped reconciling the root, it
will be removed. New environments must not use it.

## Compatibility

Earlier `v1alpha1` schemas required `spec.provenance` (method, recorder,
recording time, optional reference and import source) on every kind, required
each spec change to advance `provenance.recordedAt`, and accepted Device
`identity.sourceIdentity`, component `sourceRef`, and
`reportedPlacement.method`/`snapshotDigest`. These fields are gone. The served
and stored version remains `v1alpha1`; this is an alpha breaking change.

Records that still validate without the removed fields need no migration. Each
schema is structural and does not preserve unknown fields, so the API server
prunes the removed fields from stored objects when it reads them and drops them
from requests; they leave storage on the next write.

Two kinds of stored record no longer validate once pruned:

- A Device whose only identity was `identity.sourceIdentity` is left with no
  serial or system UUID. Because `identity` is immutable, delete it and create
  a new Device with a serial or system UUID. Where
  [Device deletion protection](../development/inventory-capacity-registration.md#device-deletion-protection)
  is installed, retire it (`Decommissioning`, then `Retired`) before deleting
  it, or leave it in place and create the new Device alongside it.
- A Device whose `reportedPlacement` lacks `reason` or `reference` must gain
  both the next time its report changes.

Until then, such a record can still be read and deleted, and it accepts
metadata or status updates that leave the invalid fields unchanged, through
CRD validation ratcheting. Ratcheting is always on from Kubernetes 1.33, and
Milo builds on 1.37. Any other spec change is rejected until the record is
valid.
A client that still sends them is not rejected unless it asks for strict field
validation: by default the server drops the fields and returns an unknown-field
warning, while strict validation (the `kubectl` default) returns an error.
Remove the fields from GitOps sources and other writers.

Retirement happened in two steps so that GitOps-applied records could drop the
fields first: provenance became optional and stopped gating edits, then the
fields were removed.

### Rack face and depth semantics

The mounted-face meaning above is a clarification of the existing `face`
field, not a new field or value. Stored values stay `front` and `rear` and are
not rewritten. The meaning changes nothing for half-depth equipment, which sits
on the rails of the face it is recorded on either way, or for occupancy of
full-depth equipment, which covers both faces either way. The only new reading
is the rack face of a component, which needs a component template face, and
no stored template has one yet. Before adding template faces to a model,
review the recorded `face` of each reversed full-depth device, usually a
switch, of that model: if it was recorded as the face its rails are reached
from, rather than the face its chassis front points toward, correct it with
an ordinary report revision.

`modelShape.fullDepth` is unchanged in the schema and still has no default,
so no stored record is reinterpreted. Readers now also consult the Device's
and the DeviceType's model shape when the report omits depth. Before this
change the elevation used only `reportedPlacement.fullDepth` and drew any
Device without it on its mounted face only, with a note that unknown depth
did not establish clearance. It now draws unknown depth on both faces, labelled
"depth unknown", and draws a Device whose model shape says half depth on its
mounted face only. Set `fullDepth: false` on half-depth models so that two
half-depth devices on opposite faces stop disagreeing.
