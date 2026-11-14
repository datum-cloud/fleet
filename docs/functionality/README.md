# Fleet functionality

[Product overview](../README.md) · [Architecture](../architecture/README.md) ·
[Operations](../operations/README.md) · [Troubleshooting](../troubleshooting/README.md)

Fleet's design covers infrastructure from planning to reuse. These capabilities
are not all available. Check [implementation status](../development/README.md)
before relying on one.

Customer machine allocation and installation are exposed through **Bare Metal**
at `metal.datumapis.com`. Fleet manages the underlying physical infrastructure;
see [API boundaries](../architecture/README.md#api-boundaries) for the distinction between
the customer service, inventory, and intended coordination contracts.

## Capabilities and contracts

| Capability | Intended experience | Learn more |
| --- | --- | --- |
| Physical inventory | Plan installations and verify actual equipment, cables, and power connections | [Inventory](../proposals/fleet-management/inventory.md), [bare-metal hardware](../proposals/fleet-management/bare-metal-hardware.md) |
| Capacity discovery | Discover eligible provider offerings without exposing private inventory or reserving supply | [Capacity publication](../proposals/fleet-management/capacity-publication.md) |
| Machine allocation | Request hardware in a project through the shared allocator | [Machine allocation and installation](../enhancements/machine-lifecycle/README.md) |
| Provisioning and access | Perform authorized preparation, installation, power, console, and cleanup work | [Bare-metal provisioning](../proposals/fleet-management/bare-metal-provisioning.md) |
| OS installation and configuration | Select approved artifacts and retain exact inputs with one configuration writer | [OS configuration](../proposals/fleet-management/os-configuration.md) |
| Managed clusters | Coordinate membership, logical node identity, replacement, and bounded rollouts | [Cluster lifecycle](../proposals/fleet-management/cluster-lifecycle.md) |
| Platform node services | Place and update services on managed nodes and report their readiness | [Node services](../proposals/fleet-management/node-services.md) |

## Follow a user journey

- **Standalone machine consumer:** Read the
  [allocation and installation experience](../enhancements/machine-lifecycle/README.md#managing-allocated-machines),
  then the [API examples](../enhancements/machine-lifecycle/api-examples.md).
  Allocation-only and allocation-with-installation are separate handoffs;
  installation is not continuous guest OS management.
- **Owned infrastructure operator:** Follow the
  [owned bare-metal journey](../proposals/fleet-management/examples/owned-bare-metal/README.md)
  from planning and physical verification through customer release.
- **Managed platform operator:** Read the
  [rented-capacity adoption example](../proposals/fleet-management/examples/us-east-1-alice.md)
  alongside the cluster lifecycle contract. Adoption starts with observation,
  not reprovisioning or exposing occupied machines as available.

These examples illustrate proposed contracts. For an executable local scenario,
use the [standalone synthetic smoke guide](../operations/standalone-smoke.md); for broader
validation requirements, read the
[machine lifecycle acceptance tests](../enhancements/machine-lifecycle/acceptance-tests.md).
