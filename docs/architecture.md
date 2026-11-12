# Fleet architecture guide

[Product overview](README.md) · [Functionality](functionality.md) ·
[Operations](operations.md) · [Troubleshooting](troubleshooting.md)

Fleet groups related infrastructure capabilities without making one controller
the owner of every resource. Physical inventory, allocation, installation, and
managed node identity remain separate. Standalone and managed consumers use the
same allocation and installation contracts.

## Start with the boundaries

Read the [design context](design.md) for the subsystem map, authority contracts,
and unresolved questions. Then read the
[detailed service architecture proposal](proposals/fleet-management/architecture.md)
and [architecture diagrams](proposals/fleet-management/diagrams/README.md).
These describe the intended service, not the deployed topology.

Key principles to carry into the detailed design:

- Each configuration surface has one authoritative writer. Networking/IPAM,
  Galactic routing, DNS, and consumer service policy stay with their owners.
- Publication and discovery are not authorization. Allocation needs retained
  consumer authority and exact source identity before effects.
- Hardware, allocation, installation, and logical node lifetimes are distinct.
  Replacement must not silently reuse an old incarnation's authority.
- Execution and cleanup can be uncertain. Unknown outcomes do not establish
  readiness or permit capacity reuse.

## Choose a deeper view

| Design question | Reference |
| --- | --- |
| What are the machine resources and project boundaries? | [Machine lifecycle API design](enhancements/machine-lifecycle/README.md#api-design) |
| How are reservations and concurrent operations coordinated? | [Durable coordination](enhancements/machine-lifecycle/README.md#durable-coordination) |
| How are provider effects recovered safely? | [Provider execution and recovery](enhancements/machine-lifecycle/README.md#provider-execution-and-recovery) |
| What happens on release or project deletion? | [Release and project lifecycle](enhancements/machine-lifecycle/README.md#release-and-project-lifecycle) |
| Which internal interfaces and guarantees exist? | [Implementation status](implementation.md) |
| Which milestones must precede rollout? | [Implementation roadmap](roadmap.md) |
| Where did the design originate? | [Pinned source proposals](design.md#source-proposals), [migration record](proposals/fleet-management/MIGRATION.md) |

For subsystem-specific contracts, use the [functionality map](functionality.md).
For manifests and environment responsibilities rather than proposed topology,
use the [operations guide](operations.md).
