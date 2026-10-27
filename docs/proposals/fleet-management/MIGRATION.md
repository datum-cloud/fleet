# Proposal migration record

Imported on 2026-10-27 into `datum-cloud/fleet` for continued design review and
implementation. The proposal remains provisional. Moving it does not accept the
design, implement its APIs, or authorize infrastructure changes.

## Provenance

| Original proposal | Exact source revision | Role |
| --- | --- | --- |
| [infra #5782](https://github.com/datum-cloud/infra/pull/5782) | `51781e2456367be82281e1443ca56d0686311133` | Core Fleet service proposal. |
| [infra #7705](https://github.com/datum-cloud/infra/pull/7705) | `a712d2d3aaddcdc4d81c09d7f4cdd139bef06631` | Node services and consumer pool gates; based on the core proposal. |

All 27 files under `docs/enhancements/fleet-management/` at the second revision
were imported into this directory. That revision contains both proposals. Original
commits and contributor/reviewer history remain in the infra repository; the
import does not copy unrelated infra Git history into Fleet.

Migration-only edits are limited to:

- Adding the provisional-status notice and this migration record.
- Converting three links to infra application manifests into URLs pinned to the
  source revision, since those manifests remain owned by infra.
- Updating three illustrative `milo-os/fleet` implementation references to
  `datum-cloud/fleet`, the chosen repository home.

The examples, PlantUML sources, and rendered PNGs are otherwise preserved. The
repository overview and design summary now point to this local proposal.
Environment-specific deployment and migration configuration remains in infra.

## Review points carried forward

### Unresolved provider network fact contract

The [comment on #5782](https://github.com/datum-cloud/infra/pull/5782#issuecomment-5838307998)
asks for provider-assigned uplinks, gateways, peer ASNs, and peer addresses to be
modeled as normalized observations. Datum-assigned addresses remain IPAM intent;
routing policy remains networking intent. Define the observation shape, separate
facts from policy in the adoption example, and resolve how IPAM discrepancies are
reported without pretending Datum can change provider assignments. This migration
preserves the original text and does not silently resolve that review request.

### Node service follow-ups reflected in the imported text

The [review of #7705](https://github.com/datum-cloud/infra/pull/7705#pullrequestreview-5646192072)
requested four clarifications. The imported head adds:

- All gated slots ready to open the readiness gate after join/reboot/reinstall/
  replacement; at least one ready slot per service to keep it open afterward.
- A regional broker/storage and writer prerequisite before expansion to another
  region. The regional control-plane writer still needs tracked implementation work.
- Predecessor fencing evidence and a `FencingStuck` path.
- Operator ownership of procurement policy and proposed pilot guardrail values.

These textual responses are preserved, not treated as proof of a completed
implementation or a new reviewer approval. In particular, clarify replacement
joining versus activation of shared identity, and how a stuck occupied logical
slot interacts with demand-based pool sizing. These are implementation questions
identified in the [design context](../../design.md), separate from the original
reviewer's requests.

### Historical CI discussion

The [latest source review](https://github.com/datum-cloud/infra/pull/7705#pullrequestreview-5646279860)
and [CI discussion](https://github.com/datum-cloud/infra/pull/7705#issuecomment-6401060628)
report failures in infra's Flux-render fixtures. Keep those reports with the source
history; importing documentation neither repairs those checks nor establishes
runtime readiness. Fleet validation covers the imported documentation and assets.

## Continuing review

Review and amend the [service proposal](README.md) in this repository. Open design
questions in each companion document and the [design context](../../design.md)
remain unresolved unless a subsequent change explicitly records a decision.
The two original infra PRs are superseded by the combined Fleet proposal PR;
their discussions remain accessible through the links above.
