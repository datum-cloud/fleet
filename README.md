# Datum Fleet

Fleet is Datum's infrastructure management platform for owned and rented capacity.
It brings physical inventory, machine allocation, provisioning, operating system
configuration, managed clusters, and node services into a shared service with
explicit ownership and lifecycle boundaries.

This repository is the starting point for building that platform. It currently
contains the detailed service proposal, design context, and an implementation roadmap. There are no implemented
APIs, controllers, deployment manifests, or build/test commands yet.

## Scope

- Plan, register, and verify physical infrastructure and its connections.
- Publish machine classes into eligible projects and allocate provider-owned
  capacity through one shared allocator.
- Install supported operating systems and provide allocation-scoped networking,
  power, console access, and safe release for standalone customers.
- Manage cluster membership, logical node identity, configuration, and replacement
  using the same machine allocation and installation services.
- Place platform node services, such as Private DNS resolvers, on managed nodes
  and incorporate their readiness into consumer placement gates.

The first bare-metal release must exercise a standalone customer workload on a
qualified OS other than Talos, without requiring a managed cluster or an ongoing
guest agent. Provisioning engines and provider integrations remain pluggable.

## Start here

- [Service proposal](docs/proposals/fleet-management/README.md): detailed contracts,
  examples, and architecture diagrams imported from the infra proposals.
- [Design context](docs/design.md): subsystem boundaries, lifecycle contracts,
  source proposals, and unresolved design questions.
- [Implementation roadmap](docs/roadmap.md): proposed milestones and evidence
  required before enabling infrastructure effects.
- [Contributor guidance](AGENTS.md): repository conventions and key invariants.

The foundation was developed in `datum-cloud/infra` PRs
[#5782](https://github.com/datum-cloud/infra/pull/5782) and
[#7705](https://github.com/datum-cloud/infra/pull/7705). Their API shapes and
implementation choices remain provisional. The proposal now lives here for
continued review; migration does not imply design acceptance or deployed behavior.

Fleet will integrate with Datum's existing identity, service catalog, location,
networking/IPAM, Galactic, and Compute services. Environment-specific rollout
configuration remains in `infra`; ownership of individual outputs must transfer
explicitly as Fleet capabilities are implemented.
