# Datum Metal and Fleet

Bare Metal is Datum's customer-facing machine service at `metal.datumapis.com`.
Fleet manages physical infrastructure at `fleet.datumapis.com`; the intended
internal handoff API uses `coordination.fleet.datumapis.com`.

Fleet manages owned and rented infrastructure. It tracks equipment, allocates
machines, installs operating systems, and supports managed clusters and node
services. Each subsystem owns its resources and controls changes to them.

Fleet is under development. This repository contains the service design, alpha
APIs, lifecycle libraries, and synthetic validation environments. Production
integration and real provider support are not complete; the production manager
rejects work that requires those integrations and makes no provider changes. See the
[implementation status](docs/development/README.md) for the implemented subset and
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
| [Functionality](docs/functionality/README.md) | Capabilities, customer journeys, and API contracts |
| [Architecture](docs/architecture/README.md) | Subsystems, ownership boundaries, and design rationale |
| [Operations](docs/operations/README.md) | Local validation, deployment packaging, and acceptance |
| [Troubleshooting](docs/troubleshooting/README.md) | Expected limitations, diagnostic entry points, and safe recovery boundaries |

The [Bare Metal CLI enhancement](docs/enhancements/metal-cli/README.md) proposes
`datumctl metal` discovery, machine management, and safe recovery workflows.

Contributors should also read the [repository guidance](AGENTS.md) and
[implementation roadmap](docs/development/roadmap.md).
