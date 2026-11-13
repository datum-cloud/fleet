---
status: provisional
stage: alpha
---

# Bare Metal CLI experience

## Summary

Introduce `datumctl metal`, a CLI plugin for discovering bare-metal offerings,
allocating machines, installing an OS, inspecting progress, controlling power,
and safely releasing capacity. A customer manages a `Machine` in their project,
not a provider's physical asset or provisioning backend.

This document proposes the user experience, not an implemented CLI or a promise
that every backing API exists. All commands, flags, output, and exit codes below
are proposed contracts. Plugin distribution, host CLI integration, and runtime
access APIs must be settled before shipping the corresponding commands.

The [machine lifecycle enhancement](../machine-lifecycle/README.md) defines the
current allocation and installation contract. The [API architecture](../../architecture.md)
defines service boundaries; [implementation status](../../implementation.md)
distinguishes implemented synthetic behavior from missing production authority
and provider integrations. Historical foundations are the pinned
[allocation proposal][allocation-source] and [installation proposal][installation-source].
Their older resource names do not override the current lifecycle contract.

## Motivation

Customers should not need to understand BMCs, provider projects, allocation
records, or provisioning engines to obtain and operate a bare-metal machine.
They do need to understand what they requested, what has actually happened, what
is blocking progress, and which actions may interrupt a workload or erase data.

The CLI must be equally useful at a terminal and in automation. A disconnected
terminal must not lose ownership of a machine, start a second installation, or
make uncertain cleanup appear complete.

### Goals

- Provide a discoverable path from offering selection to a usable machine.
- Support allocation without installation, with no managed cluster or guest agent.
- Make asynchronous progress, failure, and unknown outcomes distinguishable.
- Keep project scope and destructive consequences visible before mutation.
- Provide structured output and safe recovery after interrupted requests.
- Use the same service admission, allocator, and installation contracts as other
  clients, including managed consumers.

### Non-goals

- Physical inventory, cabling, BMC enrollment, provider capacity preparation,
  procurement, or publication administration in the initial plugin.
- Managing clusters, NodeClaims, OS drift, or applications inside the guest.
- Owning network creation, IPAM, routing, DNS, or Internet-access policy.
- Exposing protected allocations, provider workflow IDs, or management credentials.
- Force release, finalizer removal, or bypassing managed-controller authority.

Provider and infrastructure operator workflows require a separate experience
design. Importing inventory must remain observational until an explicit ownership
transition; this plugin will not provide a shortcut from import to provisioning.

## User model and conventions

### Context and authentication

The plugin should reuse `datumctl` authentication, endpoint selection, project
context, namespace context, and output conventions rather than create a second
credential store. The proposed examples use explicit `--project` and `--namespace`
flags; their exact spelling and precedence must be aligned with the host CLI.

Explicit flags override the selected context. Missing or ambiguous scope is an
error for mutations; the CLI never guesses a project from the first matching
machine. It must not translate a Milo project into an arbitrary Kubernetes
namespace. Classes and profiles are project-scoped discovery resources; Machines
and Installations also have a namespace within that project.

Interactive previews show endpoint/context, project, namespace, target, and
consequences. Automation should pass scope explicitly. Authentication failures
point to the host CLI's login flow without printing credentials. A service that
is not enabled produces an actionable diagnostic, not an empty machine list or
an automatic service-enablement request.

### Names and identity

`Machine` is the durable customer handle for one allocation. `Installation` names
an immutable request for one OS incarnation. Reinstallation keeps the Machine;
hardware replacement requires a new Machine and a new Installation. Nothing
silently transfers addresses, names, or managed-node identity to replacement
hardware.

Users author local resource names, not physical Device IDs, MachineCapacity IDs,
private allocation IDs, or fencing tokens. The service pins exact incarnations
and validates authority. CLI names and confirmation prompts are not authority.

### Command map

Use plural resource nouns consistently. Singular aliases, if adopted by the host
CLI, are convenience aliases rather than a second documented vocabulary.

| Command | Intended experience | Delivery dependency |
| --- | --- | --- |
| `datumctl metal classes list/get` | Discover published offerings, locations, profile policy, and supported operations. | Consumer-safe class discovery. |
| `datumctl metal profiles list/get` | Inspect hardware requirements separately from offering policy. | Published profile discovery. |
| `datumctl metal machines list/get/describe` | Inspect allocations, requested and observed state, conditions, and safe diagnostics. | Customer Machine API. |
| `datumctl metal machines create` | Allocate only, or allocate with an initial installation. | Admission and optional installation support. |
| `datumctl metal machines wait` | Observe a selected lifecycle condition without submitting work. | Public status with explicit freshness limitations. |
| `datumctl metal installations list/get/describe/wait` | Inspect installation requests and retained outcomes. | Installation API. |
| `datumctl metal machines install` | Create the first Installation for an allocation-only Machine. | Verified blank-allocation admission. |
| `datumctl metal machines reinstall` | Create a successor Installation with a predecessor precondition. | Successor admission and fencing. |
| `datumctl metal machines power on/off` | Change persistent power intent. | Authorized power reconciliation. |
| `datumctl metal machines delete` | Request release and optionally wait for verified completion. | Cleanup and quota-release evidence. |
| `datumctl metal machines console` | Open a short-lived, allocation-scoped brokered session. | Future session API and qualified provider. |
| `datumctl metal machines reboot` | Submit one bounded reboot, not an off/on toggle. | Future idempotent operation API. |

Help labels unavailable features as unsupported or experimental. A command must
not advertise functional console or reboot support based solely on a future
design. Image discovery remains with the eventual image-catalog owner; until
that API exists, examples use an approved image reference supplied by the service
operator. Do not invent a working `images list` endpoint.

## Primary journeys

All names below are synthetic. Examples describe the intended experience and are
not commands users can run against the current implementation.

### Discover an offering

```sh
datumctl metal classes list --project example-project
datumctl metal classes get standard-metal --project example-project
datumctl metal profiles get general-purpose --project example-project
```

Class details show eligible locations, allowed profiles, whether custom profiles
are permitted, and advertised power/console capabilities. Profile details show
CPU architecture and cores, memory, disk requirements, and interface slots.
Requested minimums are not displayed as allocated hardware.

Discovery is not a capacity reservation, price quote, or grant to use a source
offering. Only expose authorized availability summaries, with observation times
when supplied; do not enumerate provider assets. Allocation can still be blocked
by source-use permission, entitlement, quota, profile compatibility, or exhausted
capacity. Unsupported combinations should be identified early where possible,
but server-side admission remains authoritative.

### Allocate without an OS

```sh
datumctl metal machines create worker-01 \
  --project example-project --namespace default \
  --class standard-metal --profile general-purpose --location lab \
  --allocate-only
datumctl metal machines wait worker-01 \
  --project example-project --namespace default --for=ready --timeout=20m
```

Require exactly one of `--allocate-only` or `--image` so the handoff is deliberate.
Allocation-only defaults to power off and no network attachments. Its ready
message explicitly says: “Allocation handoff complete; no OS installation was
requested; desired power is Off.” It does not claim a bootable machine, guest
connectivity, or application health.

`create` returns after the API acknowledges the request by default. Its response
identifies the Machine and gives a scoped follow-up command; it says “request
created,” not “machine provisioned.” `--wait --timeout=20m` is an optional
convenience with the same semantics as a separate wait command.

### Allocate and install

```sh
datumctl metal machines create worker-02 \
  --project example-project --namespace default \
  --class standard-metal --profile general-purpose --location lab \
  --image qualified-linux --installation worker-02-os-1 \
  --bootstrap-secret worker-02-bootstrap \
  --network primary=production --power-state=On --wait --timeout=30m
```

The user chooses a named installation request and an existing same-namespace
Secret for bootstrap configuration. The CLI does not accept secret bytes in
command-line arguments or print bootstrap content. Creating and securing that
Secret uses the owning API and explicit caller permissions.

The convenience command creates a Machine with `initialInstallationRef` and a
reciprocal Installation with `expectedPredecessor.type=None`. It creates the
Machine first, then the Installation. These are two API writes, not a transaction.
The initial gate prevents activation before the named installation succeeds.
The CLI does not replace the gate with client-side sequencing or an early power-on.

If only the Machine write succeeds, report partial submission and the exact
missing Installation name. Keep the Machine and reservation: automatic deletion
would begin cleanup and could destroy legitimate work. A proposed `--resume`
mode on the same command reconciles the same names and intended immutable inputs,
creating only a missing request after server validation. A mismatch is a conflict,
not permission to patch immutable fields, choose another name, or overwrite work.
Recovery must never infer a blank machine merely from a missing Installation
object; service-retained history decides whether the request is admissible.

`--network primary=production` binds a profile slot to an existing Network in the
Machine's namespace. The initial shorthand uses the API's IPv6 default; advanced
families, addresses, namespace references, and reclaim policies use a manifest.
It does not create a Network or grant Internet access. Network bindings are
immutable placement inputs, not a promise of an attach/detach command later.
Display assigned addresses separately from verified network readiness and guest
connectivity. Networking remains the sole authority for those assignments.

### Inspect and troubleshoot

```sh
datumctl metal machines list --project example-project --namespace default
datumctl metal machines describe worker-02 --project example-project --namespace default
datumctl metal installations describe worker-02-os-1 \
  --project example-project --namespace default
```

A default table emphasizes `NAME`, `CLASS`, `PROFILE`, `LOCATION`, `READY`,
`POWER`, and `REASON`. Desired and observed power are separate in details. Missing
facts display as unknown, not zero capacity, powered off, or a successful state.
Hardware details distinguish the retained allocation snapshot and its observation
time from current health observations.

`describe` presents requested inputs, accepted placement, current Installation
when known, network assignments, conditions, and sanitized reasons. For example:

```text
Machine:       example-project/default/worker-02
Ready:         Unknown
Reason:        OutcomeUnknown
Installation:  worker-02-os-1 (outcome not yet verified)
Next step:     Inspect installation worker-02-os-1; do not submit another install.
```

An Installation listed as an outstanding request is not presented as the current
verified OS. Detail output distinguishes the requested image, completed historical
installation, and current installation reference; absent current state is unknown.

Diagnostics explain the failing boundary without leaking provider internals:

| Reason | User-facing guidance |
| --- | --- |
| `InsufficientCapacity` | Request remains pending; inspect eligible offerings before deciding to release and create another request. |
| `QuotaDenied` | Review project quota with its owner; changing names does not bypass quota. |
| `AuthorizationUnavailable` | Authority could not be verified; do not treat this as a permanent denial or retry through provider credentials. |
| `ProfileUnsupported` | Inspect offering/profile compatibility; immutable placement changes require a different Machine request. |
| `NetworkPending` | Inspect the referenced network through its owning service. |
| `InstallationPredecessorConflict` | Re-read installation history; do not automatically advance the predecessor and retry. |
| `OutcomeUnknown` / `EvidenceStale` | Keep observing the retained request; uncertainty is not successful execution. |
| `CleanupBlocked` | Release is incomplete; contact the service operator rather than force-removing lifecycle protection. |

### Install later or reinstall

```sh
datumctl metal machines install worker-01 \
  --project example-project --namespace default \
  --image qualified-linux --installation worker-01-os-1
datumctl metal machines reinstall worker-02 \
  --project example-project --namespace default \
  --image qualified-linux --installation worker-02-os-2 \
  --expected-installation worker-02-os-1
```

`install` requests `None` as the predecessor only for a service-verified blank
allocation. `reinstall` requires the expected predecessor explicitly in automation;
an interactive flow may suggest the observed current Installation but must show
it before confirmation. Admission pins the exact predecessor incarnation and
rejects competing successors. The CLI never retries by selecting a newer
predecessor without a new user decision.

Installation and reinstallation may overwrite disks. Both show the scoped Machine,
image, request name, predecessor, and warning that machine data may be destroyed.
Reinstallation does not replace failed hardware, alter placement, or transfer
network identity. An unknown incarnation requires service recovery or release,
not another installation with a guessed predecessor.

Installation history is read-only in the initial plugin. Deleting a completed
Installation through another client does not uninstall the OS or release the
Machine. Cancellation of unfinished installations is deferred until its safe
boundary and recovery semantics have a dedicated UX; Ctrl-C is not cancellation.

### Power and console access

```sh
datumctl metal machines power off worker-02 --project example-project --namespace default
datumctl metal machines power on worker-02 --project example-project --namespace default
```

Power commands set persistent `spec.powerState` intent, then optionally wait for
matching observed power. Repeating identical intent does not create a new reboot.
Power-off warns about interruption and does not promise a graceful guest shutdown.
Power-on still passes activation, authorization, and installation gates. The CLI
does not cycle power to emulate reboot.

When brokered console support exists, `machines console` should attach directly,
identify the scoped target, and explain the detach key before opening a stream.
Session credentials remain ephemeral and are excluded from logs and normal
output. Console access is not raw BMC access, and network reachability is not
permission. Release revokes new access and terminates existing sessions.
Reconnection requires fresh authorization; the client cannot keep a session
alive across release or reallocation. Guest SSH remains outside the initial
plugin; no SSH keys or guest credentials are inferred from allocation ownership.

Commands against coordinator-managed machines must respect that authority.
Displaying a target or holding project membership does not authorize an override.
The plugin directs users to the managing service rather than bypassing its gates.

### Release a machine

```sh
datumctl metal machines delete worker-02 \
  --project example-project --namespace default --wait --timeout=30m
```

The confirmation states that deleting the Machine ends access, releases its
allocation, and initiates the provider's cleaning policy. It does not delete the
physical asset. There is no `--force` or “skip cleaning” option.

Without `--wait`, success means the release request was accepted. With `--wait`,
success requires the service's authoritative release-completion contract,
including cleanup and quota release; disappearance alone is insufficient when
project removal or access loss could hide the resource. The final implementation
must define how this evidence remains observable after deletion. Until then,
release waiting must report an unverifiable outcome rather than claim completion.
Already absent targets likewise cannot be reported as verified clean capacity.

Repeated release requests are reconciled against the same target incarnation.
Deletion uses an observed object-identity precondition; a recreated name must not
be deleted by a stale retry. Timeout does not free capacity or roll back release.

## Automation and safety contract

### Input and confirmation

- Require explicit names for mutating examples; generating a fresh name is a new
  request, never a recovery strategy.
- Offer `machines create --file machine.yaml` for advanced existing API fields.
  Do not combine a manifest with conflicting shorthand spec flags. Initial
  installation pairs must be validated for reciprocal references before writes.
- Offer `--dry-run=client -o yaml` to render requests without writes, allocation,
  secret retrieval, or effects. State that rendering does not check authorization,
  live compatibility, quota, or availability. A server validation mode requires a
  separately verified side-effect-free API contract.
- Interactive mutations show a concise plan. Disk-destructive operations,
  power-off, reboot, and release require target-name confirmation. In non-TTY
  mode these commands fail before mutation unless `--yes` is supplied. `--yes`
  bypasses only the prompt, never admission or predecessor checks.
- A create-and-install plan carries the same disk warning; new allocation does
  not justify hiding its installation effects. A preview is not a reservation.
- Do not persist bootstrap bytes, rendered secret-bearing configuration,
  kubeconfigs, or provider tokens in local recovery files or Git. Recovery uses
  named resources and service-retained authority, not client-held capabilities.

### Output, waiting, and failures

Provide human-readable tables by default and `-o json` / `-o yaml` for automation.
Resource reads preserve API fields rather than invent a second resource schema.
Mutation receipts distinguish submitted resources, partial submission, and
observed completion; their versioned shape must be finalized before scripting is
declared stable. Structured stdout contains only the selected result; progress,
prompts, and diagnostics go to stderr. Do not require scripts to parse prose.

`wait` is read-only and reconnects watches by re-reading the same resource
incarnation. `--for=ready` checks the relevant reported condition and generation;
it never treats absent, False, or Unknown as success. A known terminal failure
returns immediately; recoverable pending conditions continue until timeout.
Generation and transition time do not prove live evidence freshness. A wait result
is explicitly a report of service state, not independent verification or authority
to perform effects. Surface `EvidenceStale` and disclose when freshness is not
available rather than promising real-time health. Completed installation success
is a durable historical result, not ongoing guest health.

Proposed exit codes, subject to host CLI alignment:

| Code | Meaning |
| --- | --- |
| `0` | Requested API submission acknowledged, or selected wait condition satisfied; the receipt identifies which. |
| `1` | Definite API rejection or verified operation failure. |
| `2` | Invalid input, unsupported feature, or missing noninteractive confirmation; no mutation submitted. |
| `3` | Wait deadline exceeded, partial submission, lost mutation response, or otherwise unknown completion; inspect the named resources. |
| `130` | User interrupted the client; previously submitted work may continue. |

A network timeout after submission is not a definite rejection. Recover by reading
the original names and checking retained inputs. Do not blindly repeat destructive
requests, roll back partial work, or substitute a new name. Transport retries for
reads are safe; effect-bearing retries require the service's idempotency and exact
target contract. Future reboot/session APIs must establish those contracts before
the plugin implements automatic retries. Concurrent changes produce actionable
conflicts rather than last-writer-wins overwrites of unrelated fields.

## Delivery and acceptance

1. Align plugin loading, packaging, authentication, project context, help, and
   output with `datumctl`. Start with discovery and read-only inspection.
2. Add create, install, power intent, and release against the synthetic lifecycle
   harness. Label simulator evidence explicitly; it does not qualify hardware.
3. Qualify a standalone non-Talos installation on a real provider with networking,
   cleanup, authorization, and interruption/recovery tests before general use.
4. Add console and bounded reboot only after their public APIs and providers are
   qualified. Keep provider administration out of this customer-facing milestone.

The CLI implementation location and distribution channel are not selected here.
Do not add a module, framework, dependency, or executable merely to accompany this
UX proposal. Implementation must document actual build and test commands.

Acceptance coverage must include:

- A new user discovers an offering, allocates powered off, installs a qualified
  OS later, observes progress, and releases without a Cluster or guest agent.
- Create-and-install interruption between writes resumes the original pair;
  lost responses, same-name recreation, and mismatched inputs cannot duplicate
  effects or act on another incarnation.
- Two competing reinstall commands cannot both consume one predecessor; an
  unknown installation outcome never prompts automatic replacement work.
- Project/context mistakes, permission revocation, suspended/deleting projects,
  unavailable authorization, quota denial, and managed authority all fail safely.
- Desired power differs visibly from observed power; stale and absent conditions
  never appear successful; installation success is not reported as guest health.
- Release blocked by cleanup retains occupancy; project disappearance and access
  loss do not satisfy release waiting; no force path clears protection.
- Non-TTY destructive commands require confirmation flags; structured stdout is
  parseable during progress, errors, partial submission, and interruption.
- Help, dry runs, debug logs, receipts, and diagnostics expose no bootstrap data,
  private allocation/operation IDs, BMC endpoints, or bearer credentials.
- Unsupported console/reboot fail without effects; later console tests prove
  session expiry and active revocation on release.

These are proposed tests, not claims of verified CLI behavior.

## Alternatives and open questions

Generic resource CRUD alone remains useful for advanced users but leaves safe
installation pairing, consequence previews, and recovery to each caller. A
guided wizard alone is easy to start but hard to automate and recover. Prefer
explicit commands with optional interactive guidance over either extreme.

A single synchronous “provision” command would obscure admission versus effects
and fail poorly on terminal disconnect. Prefer durable asynchronous requests with
optional waits. Do not introduce a second CLI-side allocator or workflow engine.

Resolve before implementation:

- What host plugin protocol, installation/update mechanism, and compatibility
  policy does `datumctl` require? Which context, error, and output conventions
  must this proposal adopt rather than introduce?
- How will approved image references and image/profile compatibility be discovered
  before a full image catalog exists?
- What consumer-safe evidence establishes release completion after object or
  project deletion? How is projection freshness exposed to waiting clients?
- What receipt schema and resume contract safely correlate partially submitted
  pairs without exposing protected operation identities?
- How should advanced manifests represent multiple requests, and should custom
  profile authoring join the initial plugin or remain generic resource management?
- Which operations support graceful shutdown, bounded reboot, and console modes
  on the first real provider? How will capability/version skew be communicated?

[allocation-source]: https://github.com/datum-cloud/fleet/blob/24568e13bdc23b4eb360c8f1f38a16d8c59f9a86/docs/proposals/fleet-management/bare-metal-hardware.md
[installation-source]: https://github.com/datum-cloud/fleet/blob/24568e13bdc23b4eb360c8f1f38a16d8c59f9a86/docs/proposals/fleet-management/os-configuration.md
