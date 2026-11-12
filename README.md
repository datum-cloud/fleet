# Datum Fleet

Fleet is Datum's infrastructure management platform for owned and rented capacity.
It brings physical inventory, machine allocation, provisioning, operating system
configuration, managed clusters, and node services into a shared service with
explicit ownership and lifecycle boundaries.

This repository contains the detailed service proposal, design context,
implementation roadmap, and the first alpha public API encoding for the machine
lifecycle. The Go types, CRDs, synthetic persistent provider simulator,
standalone same-cluster allocation/installation lifecycle library, unwired NSO
network lifecycle adapter, unwired Milo tenancy/quota authority contracts, and
an event-only controller-manager wiring scaffold and MachineClaim orchestration
handler are implemented. A separate
[standalone synthetic smoke environment](docs/standalone-smoke.md) packages the
lifecycle library and persistent simulator for a two-consumer local demonstration.
Its namespaces are project stand-ins and do not establish Milo acceptance. The
handler deliberately accepts only injected authoritative inputs; the repository
has no production caller-preserving authorizer, trusted provider-observation
resolver, profile-publication provenance store, admission fence, protected quota
custodian, or replayable Milo event adapters, so its binary
remains unready and performs no lifecycle or provider effects. Production
manifest roots now package the manager security boundary and Milo API, IAM,
service-catalog, and quota registration, while environment identity, credentials,
and immutable image selection remain in `infra`. See
[`config/README.md`](config/README.md). There is still no real provider
integration; the standalone smoke manifests remain test fixtures.

The repository also contains a deliberately narrow working prototype for
allocation-scoped customer Redfish access. It is not a complete Fleet allocator,
installer, or production BMC service. Its synthetic allocation records preserve
the proposed authority and identity boundaries while exercising the access path
end to end.

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

- [Machine allocation and installation](docs/enhancements/machine-lifecycle/README.md): the
  smallest durable machine lifecycle, integrated with Milo project tenancy.
- [Service proposal](docs/proposals/fleet-management/README.md): detailed contracts,
  examples, and architecture diagrams imported from the infra proposals.
- [Design context](docs/design.md): subsystem boundaries, lifecycle contracts,
  source proposals, and unresolved design questions.
- [Implementation roadmap](docs/roadmap.md): proposed milestones and evidence
  required before enabling infrastructure effects.
- [Secure customer BMC access](docs/enhancements/secure-bmc-access.md): proposed
  product experience, allocation-scoped Redfish session architecture, security,
  routing, and provisional API design.
- [Implementation status](docs/implementation.md): implemented simulator
  guarantees, internal interfaces, and explicit gaps.
- [Milo acceptance harness](test/acceptance/milo/README.md): fail-closed live
  preflight and sanitized evidence contract; live acceptance remains blocked.
- [Contributor guidance](AGENTS.md): repository conventions and key invariants.
- [Machine-management prototype](docs/implementation/machine-management-prototype.md):
  implemented architecture, exact commands, trust boundaries, E2E environment,
  verified behavior, troubleshooting, and production gaps.

## Build and test

Go 1.26.4 is the selected repository toolchain. The prototype module path is
`go.datum.net/fleet`; it follows Datum's current Go import convention without
asserting that the provisional APIs are a stable external contract.

```console
task generate
task test
task build
```

The complete path requires Linux, Docker with Compose, Kind, kubectl, Helm, jq,
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

`e2e:up` installs pinned Karmada into the management Kind cluster and registers
the location Kind cluster as its member. The repository-local harness does not
yet consume `datum-cloud/test-infra`; that remains production-environment
packaging work. `e2e:up` and `e2e:down` are idempotent. The latter removes only the two named
Kind clusters, the `fleet-bmc-e2e` Compose project, its isolated network, and
the repository-local `.e2e` directory.

The foundation was developed in `datum-cloud/infra` PRs
[#5782](https://github.com/datum-cloud/infra/pull/5782) and
[#7705](https://github.com/datum-cloud/infra/pull/7705). Their API shapes and
implementation choices remain provisional. The proposal now lives here for
continued review; migration does not imply design acceptance or deployed behavior.

Fleet will integrate with Datum's existing identity, service catalog, location,
networking/IPAM, Galactic, and Compute services. Environment-specific rollout
configuration remains in `infra`; ownership of individual outputs must transfer
explicitly as Fleet capabilities are implemented.
