# Datum Metal and Fleet

Bare Metal is Datum's customer-facing machine service at `metal.datumapis.com`.
Fleet manages physical infrastructure at `fleet.datumapis.com`; the intended
internal handoff API uses `coordination.fleet.datumapis.com`.

Fleet is Datum's infrastructure management platform for owned and rented capacity.
It brings physical inventory, machine allocation, provisioning, operating system
configuration, managed clusters, and node services into a shared service with
explicit ownership and lifecycle boundaries.

Fleet is under development. This repository contains the service design, alpha
APIs, lifecycle libraries, and synthetic validation environments. Production
integration and real provider support are not complete; the production manager
remains fail-closed and performs no lifecycle or provider effects. See the
[implementation status](docs/implementation.md) for the implemented subset and
remaining gaps.

## What Fleet is for

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

## Learn more

Start with the [product overview and documentation guide](docs/README.md) to
understand who Fleet serves, how its lifecycle fits together, and what exists
today. Then choose a topic:

| Topic | What you will find |
| --- | --- |
| [Functionality](docs/functionality.md) | Capabilities, customer journeys, and API contracts |
| [Architecture](docs/architecture.md) | Subsystems, ownership boundaries, and design rationale |
| [Operations](docs/operations.md) | Local validation, deployment packaging, and acceptance |
| [Troubleshooting](docs/troubleshooting.md) | Expected limitations, diagnostic entry points, and safe recovery boundaries |

Contributors should also read the [repository guidance](AGENTS.md) and
[implementation roadmap](docs/roadmap.md).
