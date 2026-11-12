# Metal and Fleet product overview

[Project introduction](../README.md)

Fleet is Datum's infrastructure management service for owned and rented capacity.
Its intended product spans physical inventory, machine allocation and
installation, managed clusters, and platform node services. It replaces repeated
infrastructure facts and disconnected workflows with explicit ownership and
recoverable lifecycle transitions.

The customer-facing machine service is **Bare Metal**, with alpha APIs at
`metal.datumapis.com`. Fleet's physical infrastructure APIs use
`fleet.datumapis.com`; the intended internal execution handoff uses
`coordination.fleet.datumapis.com`. See the
[API boundaries](architecture.md#api-boundaries) for resource ownership and the
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

These are intended experiences, not a statement that every capability is
available today.

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
service policy from their owners. See [architecture](architecture.md) for these
boundaries and their rationale.

## What exists today

The repository implements alpha API types and CRDs, a local-scope machine
lifecycle library, a persistent synthetic provider simulator, and manager wiring
with injected lifecycle handling. Standalone smoke and Milo-routed synthetic
staging environments exercise subsets of the lifecycle. They are test fixtures,
not production service or hardware qualification.

Production authority and event integrations remain incomplete, and no real
provider integration exists. The default production manager remains unready and
performs no lifecycle or provider effects. Deployment packaging does not change
that boundary. The [implementation status](implementation.md) is the detailed
record of implemented behavior and gaps; the [roadmap](roadmap.md) describes
future milestones and required evidence.

## Explore the documentation

| If you want to… | Start here |
| --- | --- |
| Understand capabilities and user journeys | [Functionality](functionality.md) |
| Understand components, authority, and design decisions | [Architecture](architecture.md) |
| Build, validate, or prepare a deployment | [Operations](operations.md) |
| Investigate a blocked run or unexpected state | [Troubleshooting](troubleshooting.md) |
| Review the current machine API contract | [Machine allocation and installation](enhancements/machine-lifecycle/README.md) |
| Review the broader proposed service | [Service proposal](proposals/fleet-management/README.md) |

## How to read these docs

The overview and topic guides organize the product by reader task. Detailed
contracts stay in their existing proposal and enhancement documents so their
links, examples, and review history remain intact. Commands and environment
requirements stay with the relevant implementation or operational guide rather
than being duplicated across entry points.

- **Implementation status and operational guides** describe the implemented
  subset and its limits; passing a synthetic test does not prove live acceptance.
- **Enhancements and proposals** describe intended contracts. API examples and
  rollout plans are provisional unless implementation evidence says otherwise.
- **Design context and migration history** explain source revisions and open
  questions. Start with the [design context](design.md) and
  [proposal migration record](proposals/fleet-management/MIGRATION.md) for provenance.
