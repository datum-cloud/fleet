# Metal and Fleet product overview

[Project introduction](../README.md)

Fleet manages owned and rented infrastructure. Its design covers equipment
inventory, machine allocation, OS installation, managed clusters, and node
services. It gives each resource one owner and records work so controllers can
recover after failures.

The customer-facing machine service is **Bare Metal**, with alpha APIs at
`metal.datumapis.com`. Fleet's physical infrastructure APIs use
`fleet.datumapis.com`; the intended internal execution handoff uses
`coordination.fleet.datumapis.com`. See the
[API boundaries](architecture/README.md#api-boundaries) for resource ownership and the
implementation status of each group.

## Who Fleet serves

- **Infrastructure operators** plan and verify physical installations, prepare
  equipment, publish eligible capacity, and maintain it without losing asset or
  cleanup history.
- **Standalone customers** request machines in their projects and use permitted
  installation, networking, power, and console capabilities without operating a
  managed cluster or requiring an ongoing guest agent.
- **Platform teams** build managed clusters and node services on the same
  allocation and installation contracts, adding membership, logical node identity,
  replacement, and service readiness above the shared machine lifecycle.

These capabilities are planned. Not all are implemented.

## How the product fits together

1. **Plan and observe.** Record physical installation intent and actual assets,
   then verify their identity and connections. Registration alone does not
   authorize provisioning.
2. **Prepare and offer.** Prepare eligible equipment and publish machine offerings
   to permitted projects. Discovery neither reserves capacity nor grants access.
3. **Allocate and install.** Reserve capacity exclusively for a consumer. Hand off
   a prepared, powered-off machine or install an approved OS as requested.
4. **Operate.** Scope actions to the allocation. Managed consumers additionally
   coordinate node membership, configuration changes, and platform services.
5. **Release and reuse.** Revoke access, resolve in-flight work, isolate networking,
   and verify cleanup before making capacity available again. Unknown outcomes
   keep capacity reserved or quarantined.

Physical assets, allocations, OS installations, and managed logical nodes have
separate identities. Fleet does not take over networking, IPAM, routing, DNS, or
service policy from their owners. See [architecture](architecture/README.md) for these
boundaries and their rationale.

## What exists today

The repository includes alpha APIs, Kubernetes custom resource definitions
(CRDs), machine lifecycle code, and a provider simulator. Test environments
exercise allocation, installation, cleanup, and reuse. They do not prove that
Fleet can operate production hardware.

Production authorization and event handling remain incomplete. No real provider
integration exists. The production manager remains unready and makes no provider
changes. See [implementation status](development/README.md) for details and the
[roadmap](development/roadmap.md) for planned work.

## Explore the documentation

Each topic folder has a `README.md` entry point:

| Folder | Contents |
| --- | --- |
| [Functionality](functionality/README.md) | Capabilities and user journeys |
| [Architecture](architecture/README.md) | API boundaries, ownership, and design context |
| [Operations](operations/README.md) | Deployment, validation, and local smoke tests |
| [Troubleshooting](troubleshooting/README.md) | Common problems and safe recovery |
| [Development](development/README.md) | Implementation status, local commands, and roadmap |
| [Enhancements](enhancements/machine-lifecycle/README.md) | Focused machine lifecycle contracts and acceptance requirements |
| [Proposals](proposals/fleet-management/README.md) | Broader service proposals, examples, diagrams, and provenance |

## Read implementation and design separately

Topic guides link to detailed instructions rather than repeat them. Proposals
and enhancements retain their examples and review history.

- **Implementation status and operational guides** describe the implemented
  subset and its limits; passing a synthetic test does not prove live acceptance.
- **Enhancements and proposals** describe intended contracts. API examples and
  rollout plans are provisional unless implementation evidence says otherwise.
- **Design context and migration history** explain source revisions and open
  questions. Start with the [design context](architecture/design.md) and
  [proposal migration record](proposals/fleet-management/MIGRATION.md) for provenance.
