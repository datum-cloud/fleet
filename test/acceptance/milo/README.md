# Milo acceptance harness

This directory defines the evidence boundary for the proposed live Milo machine
lifecycle acceptance scenario. It does not currently run that scenario. The
implemented manager has no production caller-preserving source authorizer,
admission fence, protected quota custodian, project purge guard, or live Milo
event adapters. Treating controller credentials or test fakes as substitutes
would invalidate the acceptance result.

## Commands

The offline commands are safe for pull-request CI:

```sh
task milo:validate
task milo:test:fail-closed
```

`milo:validate` checks packaging and tests the evidence encoder. It is not live
acceptance. `milo:test:fail-closed` proves an incomplete environment cannot
produce `Passed` evidence.

The live entry point requires an explicit JSON config and evidence directory:

```sh
task milo:test:live \
  MILO_ACCEPTANCE_CONFIG=/absolute/path/to/config.json \
  MILO_ACCEPTANCE_OUTPUT=/absolute/path/to/evidence
```

Copy `config.example.json` outside the repository and replace every synthetic
value. Credential material belongs only in the referenced kubeconfig files.
Never add that completed config or its kubeconfigs to Git.

The current live entry point validates exact Project lifetimes, revisions, image
digests, and explicit credential paths, then writes `Blocked` evidence and
returns nonzero. A compiled runner uses status 3; the `go run` wrapper reports
that child status as its own generic failure. The runner does not load a
kubeconfig or contact a cluster. Invalid config writes `NotRun` evidence when
possible and returns nonzero. Exit status 0 is reserved for a future
implementation that verifies and completes every phase.

Credential paths are cleaned before comparison. Before any future live code may
open them, `OpenCredentialFiles` requires five distinct, owner-only real regular
files, rejects symlinks in every path component, opens final paths with
`O_NOFOLLOW`, rechecks the opened descriptors, and uses operating-system file
identity to reject hardlink aliases. Future consumers must use those already-open
descriptors rather than reopening paths. The current blocked runner never opens
the files.

## Required production capabilities

Live preflight will remain closed until owning services provide verifiable:

- caller-preserving source-use and publication provenance;
- an atomic invalidation-versus-binding admission fence;
- protected quota custody and release acknowledgment;
- consumer deletion recovery and a provider-project purge guard;
- durably replayable Milo project, IAM, catalog, and quota events;
- caller-preserving network and image authorization;
- real NSO/IPAM preparation and programming evidence; and
- wired lifecycle handlers plus a live scenario executor.

When those contracts exist, the executor must use API watches with bounded
deadlines rather than a polling resync. It must never write public status,
private authority records, NSO/IPAM status, or remove finalizers. Scenario
cleanup may delete only exact UIDs created by that run.

## Evidence

The output directory is mode `0700`; files are written atomically with mode
`0600`:

- `summary.json` contains the outcome, exact non-secret environment identity,
  phases, and blockers;
- `environment.json` contains only the allow-listed revisions, Project
  identities, and image digests; and
- `junit.xml` reports blocked and not-run phases as skipped and adds a suite
  error for every non-`Passed` overall outcome, preventing a blocked run from
  appearing green.

The schema has no field for kubeconfig paths, tokens, Secret bodies, or raw
controller logs. Invalid configuration produces identity-empty `NotRun`
evidence; no caller-controlled config field is copied into it.

The destination must be absent or an empty real directory. Evidence is first
written and verified in a fresh sibling staging directory, then renamed into
place. Existing files, nested directories, symlinks, or any entry outside the
three-file allowlist reject the write. The manual workflow uploads those three
files individually and pins every action to a reviewed commit SHA.

The current harness is non-concurrent. It rejects symlinks in every existing
evidence-parent component before staging. A future concurrent live writer must
replace that component check with held directory descriptors and no-follow
`openat`-style traversal; reusing a checked path would not be race-safe.

`Passed` is structurally unavailable unless the result contains the exact known
mandatory phase sequence, every phase is `Passed`, exact environment identity
and UTC times are valid, and no blocker remains. The CLI and evidence writer
both enforce the same invariant validator. They also bind status to execution
error: `Passed` requires nil, `Blocked` requires `ErrBlocked`, `NotRun` requires
`ErrNotRun`, and `Failed` requires a non-sentinel error. A mismatch cannot be
serialized or select an exit code.

The authoritative acceptance inventory remains
[`docs/enhancements/machine-lifecycle/acceptance-tests.md`](../../../docs/enhancements/machine-lifecycle/acceptance-tests.md).
