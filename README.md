# Datum Fleet

Fleet is Datum's infrastructure management platform for owned and rented capacity.
It brings physical inventory, machine allocation, provisioning, operating system
configuration, managed clusters, and node services into a shared service with
explicit ownership and lifecycle boundaries.

This repository contains design context, an implementation roadmap, and one
deliberately narrow working prototype: allocation-scoped customer Redfish access.
The prototype is not a complete Fleet allocator, installer, or production BMC
service. Its synthetic allocation records preserve those proposed authority and
identity boundaries while exercising the access path end to end.

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

- [Design context](docs/design.md): subsystem boundaries, lifecycle contracts,
  source proposals, and unresolved design questions.
- [Implementation roadmap](docs/roadmap.md): proposed milestones and evidence
  required before enabling infrastructure effects.
- [Secure customer BMC access](docs/enhancements/secure-bmc-access.md): proposed
  product experience, allocation-scoped Redfish session architecture, security,
  routing, and provisional API design.
- [Contributor guidance](AGENTS.md): repository conventions and key invariants.
- [Machine-management prototype](docs/implementation/machine-management-prototype.md):
  implemented architecture, exact commands, trust boundaries, E2E environment,
  verified behavior, troubleshooting, and production gaps.

## Build and test

Go 1.26.1 is the selected prototype toolchain. The module path is
`go.datum.net/fleet`; it follows Datum's current Go import convention without
asserting that the provisional APIs are a stable external contract.

```console
task generate
task test
task build
```

The complete path requires Linux, Docker with Compose, Kind, kubectl, jq,
OpenSSL, curl, and Task. It builds all images locally and loads them into Kind;
it does not require a registry.

```console
task e2e:up
task e2e:test
task e2e:test:path DIR=machine-management
task e2e:diagnostics
task e2e:openvpn:up
task e2e:down
```

`e2e:up` and `e2e:down` are idempotent. The latter removes only the two named
Kind clusters, the `fleet-bmc-e2e` Compose project, its isolated network, and
the repository-local `.e2e` directory.

The foundation comes from open `datum-cloud/infra` PRs
[#5782](https://github.com/datum-cloud/infra/pull/5782) and
[#7705](https://github.com/datum-cloud/infra/pull/7705). Their API shapes and
implementation choices remain provisional. This repository's summaries do not
turn those proposals into accepted schemas or deployed behavior.

Fleet will integrate with Datum's existing identity, service catalog, location,
networking/IPAM, Galactic, and Compute services. Environment-specific rollout
configuration remains in `infra`; ownership of individual outputs must transfer
explicitly as Fleet capabilities are implemented.
