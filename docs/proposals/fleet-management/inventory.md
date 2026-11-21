---
status: provisional
stage: alpha
latest-milestone: "v0.1"
---

<!-- omit from toc -->
# Fleet inventory

Part of the [Fleet infrastructure management service](README.md). Fleet inventory
should give infrastructure operators a reliable answer to four questions: what
equipment do we have, where is it, how is it connected, and is it ready for use?

**Status:** This is a proposal. The existing manual records cover only part of
this experience; the verification and safety requirements below have not yet
been implemented or tested on hardware. Implementation details belong in the
[physical inventory contracts, examples, and migration notes](inventory-reference.md).

- [Summary](#summary)
- [Goals and boundaries](#goals-and-boundaries)
- [Proposal](#proposal)
  - [Resources and responsibilities](#resources-and-responsibilities)
  - [From installation plan to ready equipment](#from-installation-plan-to-ready-equipment)
  - [Location and equipment identity](#location-and-equipment-identity)
  - [Connections and power](#connections-and-power)
  - [Moves, replacements, and retirement](#moves-replacements-and-retirement)
- [Bringing existing records into Fleet](#bringing-existing-records-into-fleet)
- [First release and success criteria](#first-release-and-success-criteria)
- [Decisions still needed](#decisions-still-needed)

## Summary

Operators need to plan new installations without double-booking rack space,
confirm that delivered equipment matches the plan, and identify missing or
incorrect connections before making it available. They also need to move, repair
and replace equipment without losing its history or disrupting customers by
accident.

Fleet should keep the installation plan separate from what technicians and
trusted tools actually find. Operators should be able to see what has been
checked, what is wrong, and what is still unknown, with supporting records for
each result. A missing measurement must not look like a successful check.

Inventory readiness is one input to [machine allocation](bare-metal-hardware.md),
not permission to use or change equipment. Recording a device or marking it ready
must never start it, erase it, install software, assign it to a customer, or end
an existing customer reservation. Those actions require separate approval through
[hardware provisioning and operations](bare-metal-provisioning.md).

## Goals and boundaries

- Give operators one reliable record of equipment, placement, cables and power.
- Prevent conflicting installation plans and show what remains to be completed.
- Preserve equipment history through imports, moves, repairs and retirement.
- Support servers, network and power equipment, and passive devices such as patch
  panels, including equipment that cannot report its own state.

Fleet inventory does not replace purchasing, billing, work scheduling, network
address management, routing, or cluster management. It links to those services
rather than keeping competing records. Passwords and other credentials never
belong in public inventory or repository examples.

## Proposal

### Resources and responsibilities

The proposed experience uses six records:

| Record | What it tells an operator |
| --- | --- |
| Facility | The physical premises where equipment is installed. |
| Rack | The rack's location, available mounting space and physical limits. |
| DeviceClaim | What equipment is expected for one installation and which resources are reserved for it. |
| Device | The actual piece of equipment, its identity and its components. |
| Cable | Which physical ports should be connected. |
| PowerFeed | Where electrical supply comes from and how much load it can support. |

Geographic Location and physical Facility are separate from
[Site rules for choosing where machines may run](cluster-lifecycle.md#site-and-cluster-resources).
A provider's metro name may cover several buildings; it must not be treated as
one physical facility without checking.

Operators approve installation plans. Authorized registration establishes which
physical equipment matches each plan. Technicians and trusted tools provide
observations; Fleet checks those observations against the approved plan rather
than silently changing the plan to match them.

Each record has one responsible writer. Providers remain responsible for hardware
controls, networking services for addresses and switch configuration, and Galactic
for routing. Management addresses, customer information and supporting documents
remain visible only to authorized users.

### From installation plan to ready equipment

1. **Plan the installation.** Record the expected equipment, location, cables,
   power needs and required checks. Reserve the relevant space, ports and power
   so another installation cannot claim them, even if requests arrive together.
2. **Record what arrived.** Check the actual equipment against the approved plan.
   One installation plan identifies one device, and a device has only one active
   placement. Retrying a registration must not create a duplicate asset.
3. **Check the installation.** Verify equipment identity, placement and required
   network and power paths. Show missing work and mismatches separately from
   completed checks. Connected devices can be checked together; neither must
   already be ready before its cable endpoints can be identified.
4. **Report what is ready.** State which checks passed and what they cover.
   For example, confirming a facility does not confirm a rack position. Services
   using inventory must require the checks appropriate to their task.

Keep the approved plan, supporting evidence and change history behind each result.
A cable change or component replacement requires new checks where it affects the
installation. Old results must not carry over merely because a name was reused.
If collecting evidence requires starting or changing equipment, that work needs
separate authorization.

### Location and equipment identity

Operators should be able to record only what is known: a facility, a rack without
an exact position, an exact rack position, or a side-mounted location. Unknown
space must not appear available. Invented positions cannot justify reservations
or verification; new reservations need enough evidence to avoid existing equipment.

Keep manufacturer serial numbers, operator asset tags and provider host IDs
separate, including where each value came from. None alone proves that equipment
is trusted. Virtual machines must not become physical Fleet devices just because
an imported system records them alongside hardware.

Track network, power, console and patch-panel ports separately, along with
replaceable parts. A software interface name is not a physical port identity.
After a repair, preserve the device history but record the replacement part and
recheck affected connections. Reusable model descriptions can reduce repeated
entry, but must be checked against the actual equipment.

### Connections and power

Cable records are the single source for planned physical connections. Device
plans reference them rather than keeping another list of expected neighbors.
Checks must follow patch panels and split cables, not assume that a working
end-to-end link proves every connection is correct.

Power checks must follow the supply through distribution equipment to the device.
Two power supplies do not prove redundancy if they share an upstream failure
point. Reserve enough power for the agreed failure scenario, not just normal
operation. Unknown supply details must remain unverified.

Operators also need circuit identifiers and the physical handoff points for
internet, transport and out-of-band management connections. The proposal links
these to the service owner's records and local cables. Service delivery, prices,
addresses and routing remain with their owners. Whether Fleet needs a separate
Circuit record is still an open decision, not an approved addition.

### Moves, replacements, and retirement

- **Move equipment:** create a new installation plan for the same device, reserve
  its destination and check its new connections. Keep the device and provider
  capacity identities. Release old space and ports only after confirming they
  are free.
- **Replace equipment:** create a new installation plan, device record and provider
  capacity record. Do not transfer customer assignments or successful checks just
  because the replacement uses the same rack position or management address.
- **Repair equipment:** keep the device record, record changed parts and repeat
  the affected checks.
- **Retire equipment or cancel a plan:** keep the history and outstanding customer
  obligations. Deleting a record does not prove removal, data erasure or release.

A replacement cannot double-book an occupied slot. When work is interrupted or
its outcome is unclear, retain the reservation until it is safe to release it.
Moves also require explicit cable-plan changes; cables do not automatically
follow a device to its new location.

## Bringing existing records into Fleet

The [existing manual inventory guide][manual-guide] describes a smaller model:
installation plans represent long-lived slots, locations are text fields, and
expected connections are stored on ports. This proposal instead uses a new plan
for each installation and separate Cable records. That difference must be settled
before automating moves and replacements.

The current implementation uses manual entry and operator-controlled migration,
not automated import. NetBox and optional offline candidate review are not required.
Migration must preserve existing equipment identities, customer reservations and
history. Change the responsible writer only through an explicit approval. Do not delete
and recreate assets, assume occupied space is free, or mark old records ready
without new checks.

Source data needs interpretation: NetBox sites may group several premises,
serial fields may contain provider host IDs, device lists may include virtual
machines, and rack positions may have been invented for display. Preserve these
limitations rather than turning them into verified facts.

See the [comparison with existing records and migration steps](inventory-reference.md#existing-records-and-migration)
and the [source review and its limits](inventory-reference.md#source-evidence-and-limits).
No live NetBox audit has been performed, and complete coverage of existing data
has not been established.

## First release and success criteria

Start with a small set of equipment in one facility. Operators should be able to
plan an installation, register equipment, find incorrect or incomplete work, and
see which checks make it suitable for use.

Before expanding, demonstrate that Fleet prevents conflicting reservations,
keeps unknown facts visible, and preserves occupied resources after interrupted
work. Moves, replacements, repairs and retirement must preserve history without
accidentally releasing customer equipment. Imported records must not trigger
hardware actions or become available capacity on their own.

The [detailed validation and hardware acceptance checks](inventory-reference.md#validation-and-qualification-matrix)
describe work still required; they are not a report of tests already passed.

## Decisions still needed

- Agree how to move from long-lived slot plans to one plan per installation
  without losing existing bindings or history.
- Decide who can view and change shared facilities, racks, cables and feeds,
  and where their authoritative records live.
- Choose how reservations survive failures, which hardware and connection layouts
  are supported first, and what evidence is sufficient and recent enough.
- Assign ownership of circuit references and physical handoff points, and decide
  whether a separate Circuit record is needed.

[manual-guide]: https://github.com/datum-cloud/fleet/blob/08d6dde8563b7c999ecf839565db2bd37b4dda7e/docs/operations/physical-inventory.md
