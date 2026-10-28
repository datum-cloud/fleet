# Toolchain and API choices for the Omni adapter

Status: proposed, 2026-10-27. Both threads must agree before this is accepted.
Tracked by [#4](https://github.com/datum-cloud/fleet/issues/4).

Fleet has two threads of work. The bare-metal thread builds inventory,
allocation and provisioning. The Omni edge thread
([infra#2490](https://github.com/datum-cloud/infra/issues/2490)) first imports
the existing Omni clusters and rented servers without changing them
([#5](https://github.com/datum-cloud/fleet/issues/5),
[#6](https://github.com/datum-cloud/fleet/issues/6),
[#7](https://github.com/datum-cloud/fleet/issues/7)), then manages cluster
lifecycle and upgrades ([#8](https://github.com/datum-cloud/fleet/issues/8),
[#9](https://github.com/datum-cloud/fleet/issues/9)).

The read-only import needs a language, a module path, an API group and a place
to run before any code lands. This record makes those choices for the Omni
thread. It decides nothing that only the bare-metal thread needs.

## Decisions

### Go, kubebuilder and controller-runtime

Fleet's controllers are written in Go, laid out by kubebuilder (layout
`go.kubebuilder.io/v4`) and built on controller-runtime.

Why: the Datum operators that Fleet sits beside already use this stack.
[compute](https://github.com/datum-cloud/compute) and
[network-services-operator](https://github.com/datum-cloud/network-services-operator)
use the kubebuilder layout, and
[galactic](https://github.com/datum-cloud/galactic) uses controller-runtime.
Engineers move between them without learning a new build, and the Omni and
Talos client libraries are Go. kubebuilder generates CRDs, RBAC and deep-copy
code from markers, so later pull requests add types with
`kubebuilder create api` instead of hand-written manifests.

### Module path `go.datum.net/fleet`

The Go module is `go.datum.net/fleet`, hosted at `datum-cloud/fleet`.

Why: compute, network-services-operator and galactic all use
`go.datum.net/<repo>`. The vanity path lets the repository move without
breaking imports. The proposals' `milo-os/fleet` was illustrative, and this
repository lives in `datum-cloud`.

### API group `fleet.datumapis.com`, version `v1alpha1`

Cluster-lifecycle resources (Site, Cluster, MachinePool, NodeClaim) belong to
`fleet.datumapis.com` at `v1alpha1`.

Why: [design.md](../design.md) already names this group for cluster lifecycle,
and `datumapis.com` is the domain other Datum operators register under.
`v1alpha1` says the schema may change without a migration path while the import
proves which fields matter.

This record does not create those types. The importer pull requests add them
with the observed fields they need. The `inventory.fleet.datumapis.com` and
`baremetal.fleet.datumapis.com` groups named in design.md are not created here.

### Imported objects are observed, not managed

Every object an importer creates carries a required spec field,
`spec.managementMode`, set to `ObserveOnly`. Admission rejects any other value
until the lifecycle work in [#8](https://github.com/datum-cloud/fleet/issues/8)
defines the transition. Controllers that would act on an object skip any object
in `ObserveOnly`, and importers only update status and observed fields.

```yaml
spec:
  managementMode: ObserveOnly
```

The name and value come from the Fleet proposal, which already puts
`managementMode: ObserveOnly` on ServersComMachine and BareMetalMachine. One
field name across provider machines and cluster-lifecycle resources lets one
admission rule and one controller check cover every kind.

Why a spec field and not a label or annotation:

- The CRD schema validates it. An object without it is rejected, so an
  importer cannot create an object that a later controller mistakes for
  managed.
- Changing it is a spec change. It bumps `generation`, shows in audit logs,
  and admission policy can guard it. A label can be changed by anyone who can
  patch metadata, and nothing validates its value.

Accepting `Managed` is the explicit ownership transfer that
[AGENTS.md](../../AGENTS.md) requires. #8 needs its own record for it: who may
make the change, what parity must be shown first, and whether it can be
reversed.

### Runs on the management cluster, deployed from infra

The controller runs on Datum's GKE management cluster. Flux applies it from
`datum-cloud/infra`
([infra#7859](https://github.com/datum-cloud/infra/issues/7859)). Credentials
for Omni and the providers come from GCP Secret Manager through ExternalSecrets.
None are committed here.

Why: the management cluster already runs Flux and can reach Omni and the
providers. Edge sites keep serving while the controller or that cluster is down,
because the importer only reads. Environment wiring stays in infra, as
[README.md](../../README.md) says.

## Still open

The bare-metal thread owns these. This record does not decide them:

- Allocation, quota and reservation storage, and their commit and recovery
  protocol.
- Milo project scope and IAM for Fleet resources, including whether Fleet
  controllers run against project control planes.
- Provisioning backends and execution adapters.
- Versions and scope of the inventory and baremetal groups.

Also open:

- Field shapes of Site, Cluster, MachinePool and NodeClaim, and whether each is
  namespaced or cluster-scoped. The importer pull requests propose them.
- The `Managed` transition described above.
- Where the controller image is published and how releases are tagged.
- The repository license. Generated files carry no license header until one
  is chosen.
- Registering `go.datum.net/fleet` with Datum's Go vanity import service. Only
  another module importing Fleet needs it; building this repository does not.
