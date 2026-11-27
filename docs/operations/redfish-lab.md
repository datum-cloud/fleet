# Redfish-mode acceptance with the DMTF Redfish Interface Emulator

This opt-in mode of the [multi-plane acceptance](multiplane.md) replaces the
cell worker's libvirt path with the Redfish site workers
(`internal/site/redfishwork`). Workers reach two persistent lab VMs only through
the DMTF Redfish Interface Emulator and a small lab-only adapter. Everything else
is the same pipeline:

- native Milo project APIs;
- the allocator and [Device custody](../development/device-custody.md#cutover-increment-c),
  with the coordinator running on the workstation;
- Karmada delivery and reflection;
- kro-created per-operation Jobs;
- Flux-owned shared services.

Status: the Redfish workers are **implemented and verified** by unit tests,
including against TLS BMC stubs. The multi-plane Redfish run on custody is
**implemented and verified**: it passes every phase. The execution records at the
end of this page describe runs on the path before the cutover.

It is a **local MVP proof**, not hardware qualification. No physical server, BMC,
vendor Redfish implementation, UEFI firmware or production secret store was
involved. The lab BMC serves TLS with a key generated for each run, but that key
is not a vendor certificate. See [what remains
unqualified](#limitations-and-physical-hardware-readiness).

```sh
task multiplane:redfish-acceptance
# equivalent:
FLEET_MULTIPLANE_INOTIFY=1024 hack/multiplane/run.sh --redfish
```

Prerequisites, exit codes, the state and evidence layout, `--keep`,
`--leave-running`, `stop.sh` and `teardown.sh` are those of the
[multi-plane harness](multiplane.md#run). Redfish mode also needs network access
to GitHub release archives, Docker Hub and PyPI to build the lab BMC image.

Redfish mode also runs in [CI mode](multiplane.md#ci-mode), as the `redfish`
job of the `multiplane-acceptance` workflow, with the lab targets under KVM.
Status: implemented, not yet verified under KVM.
The lab adapter defines and starts a target only while the site runtime
publishes the accelerator the adapter was given, and evidence requires every
QEMU command line of target A to show it. KVM on a cloud runner is its own
qualification target, separate from the Colima runs below and from physical
hardware.

## Upstream emulator and provenance

| Item | Pin |
| --- | --- |
| Project | [DMTF Redfish-Interface-Emulator](https://github.com/DMTF/Redfish-Interface-Emulator), BSD-3-Clause |
| Version | Release `1.2.2` (2025-09-12), commit `5f51d8ac48e46665a2a85242434f125aa7b25172` |
| Source archive | `https://github.com/DMTF/Redfish-Interface-Emulator/archive/refs/tags/1.2.2.tar.gz`, SHA-256 `acee609abaedaf123b56e7ca5b2534da94bdee6fc1183dde72699c30d63de79f`, verified by `hack/redfish-lab/upstream.sh` before extraction |
| Python | `python:3.12.12-slim-bookworm@sha256:593bd06efe90efa80dc4eee3948be7c0fde4134606dd40d8dd8dbcade98e669c` |
| Python packages | Exact versions with every PyPI file hash in `hack/redfish-lab/requirements.lock`, installed with `pip --require-hashes --no-deps` (Flask 2.3.3, Werkzeug 2.3.8, Flask-RESTful 0.3.10 and their dependencies). Upstream's `requirements.txt` lists names only. |
| libvirt client | Debian `libvirt-clients` from apt, used only by the adapter. This apt resolution is not reproducible, the same caveat as the site image. |

The 1.2.2 release was 14 months old at integration and is the newest release.
The emulator tree is copied into the image **unmodified**. Configuration comes
from Fleet files: `emulator-config.json`, an infragen `populate-config.json`
with one chassis and two Systems, and `targets.json`. The image ID, its registry
digest and the archive provenance are recorded in `evidence/image-id` and
`evidence/redfish-upstream.json`.

**No OpenStack component is used.** The image contains no Sushy, sushy-tools,
Ironic or VirtualBMC. Fleet's Redfish client is built on gofish
([BMC client library](#bmc-client-library)). `hack/multiplane/test-redfish.py`
fails if any of these names appears in the lock file, adapter or image
definition.

### What upstream provides and what the adapter adds

Inspection of the 1.2.2 source found these limits:

- `ComputerSystem.Reset` only rewrites an in-memory `PowerState`.
- A System PATCH accepts only `Name`, `HostName`, `AssetTag`, `SerialNumber`
  and `UUID`, so it rejects `Boot`.
- `flask_httpauth` is listed but never used, and `AccountService` hard-codes
  accounts. **No request is authenticated.**
- `DELETE /redfish/v1/reset` resets the whole emulator.

The adapter is `hack/redfish-lab/fleet_lab_adapter.py`. It imports the upstream
`emulator` module into the same Flask process and registers request hooks; it
does not fork or patch upstream files.

| Concern | Served by |
| --- | --- |
| Service root, Systems collection and members, Chassis, Managers, ActionInfo, routing and JSON serialization | Upstream, unmodified |
| TLS on port 8443 with a certificate and key from a mounted Secret, generated for each run | Adapter |
| HTTP Basic authentication against a mounted credential (the service root stays anonymous, per the Redfish specification) | Adapter |
| `ComputerSystem.Reset` with `On`, `ForceOff` and `ForceRestart` applied to one exact persistent libvirt domain per System | Adapter (replaces upstream's in-memory handler for the two lab Systems) |
| `PowerState` read from libvirt on every System read | Adapter |
| `Boot` source override (`Pxe` or `Hdd`, `Once`), persisted in NVRAM state on the site volume and consumed at power-on | Adapter |
| `ResetType` and boot `AllowableValues` narrowed to what the adapter implements; System `UUID` set from the lab target | Adapter |
| Definition of the lab domains and the provisioning network, re-created with the same identity if the runtime loses them | Adapter |
| Append-only request audit: method, path, status, principal class, client User-Agent, ResetType, boot override, effect and fault. It never records bodies or credentials. | Adapter |
| `DELETE /redfish/v1/reset` and non-Redfish paths | Refused by the adapter |
| Bounded, one-shot lab faults (`reject-auth`, `hide-reset`, `drop-response`) from a fault file | Adapter, lab only |

`hack/redfish-lab/test_adapter.py` runs inside the built image, with the real
upstream emulator and a fake `virsh`, in the `bmc-self-test` phase.

## Topology on the selected cell

```text
             cell Flux layers (Redfish overlays of config/site)
 fleet-site-runtime (privileged)     fleet-site-bmc (unprivileged, all caps dropped)
   libvirtd + QEMU      ◀── libvirt socket ── DMTF emulator 1.2.2 + lab adapter
   lab-target-a  lab-target-b                 ▲  Redfish over TLS (pinned key), Basic auth
   (persistent disks, serial capture)         │  Service 10.96.0.81:8443
        │ firmware DHCP/iPXE (static)         │
        ▼                                     │
 fleet-site-boot (site store read-only) kro worker Job (per handoff, Redfish only)
   per-MAC chain, artifacts            site store rw, boot evidence ro,
                                       console capture ro, per-Device credentials
```

| Component | Owner | Holds |
| --- | --- | --- |
| `fleet-site-runtime` Deployment | Flux `fleet-site-services` (Redfish overlay) | libvirtd and QEMU; creates the two 2 GiB target disks once on the retained volume (`lab-disks`) and the console capture directory (`lab-console`) |
| `fleet-site-bmc` Deployment and Service (fixed ClusterIP `10.96.0.81`, port 8443) | Flux `fleet-site-services` | The emulator and adapter; the libvirt socket, NVRAM and audit (`bmc-state`), the credential Secret and the TLS Secret |
| `fleet-site-bmc` ServiceAccount | Flux `fleet-site-base` (Redfish overlay) | No API token |
| `fleet-site-bmc-credentials` and `fleet-site-bmc-tls` Secrets | Created by the harness in the `redfish-enroll` phase; in no source revision or Flux inventory | A synthetic username, a random 256-bit password and a TLS key and certificate generated per run |
| One credential Secret per Device in the `fleet-site-credentials` namespace, named `bmc-` and 20 hex digits of the Device's SHA-256, labelled `fleet.datumapis.com/bmc-credential` | The harness, in `redfish-enroll` | The same username and password, and `tlsPublicKeySHA256`, the digest of the run's BMC key: each Device's approved pin. This follows the [BMC discovery](bmc-discovery.md) contract. Each operation's worker may get only the Secret of the Device its assignment names. kro derives that name, no handoff field names it, and the worker cannot list, watch or get another's. The `worker-rbac` and `delivery-faults` phases check this. |
| Boot service and observer | Flux, as before | They read the site store; no credential |
| `w-<handoff>` Job, ServiceAccount, Role, RoleBinding | kro, as before | The Redfish worker; **no libvirt socket**, no lab disks |

Flux selects the overlays with the same source revision: in Redfish mode the
staged cell environment points `fleet-site-base`, `fleet-site-graph` and
`fleet-site-services` at `config/site/redfish/*`. Shared objects are still owned
by exactly one Flux layer, and kro still writes only per-operation objects. The
ownership audit covers the BMC Deployment, Service and ServiceAccount.

### Lab targets: identity before allocation

| Target | Redfish System | UUID | NIC MAC / lease | Device | Role |
| --- | --- | --- | --- | --- | --- |
| A | `System-1` | `7f3d0c2e-…-0a1b2c3d4e0a` | `52:54:00:77:01:0a` / `.10` | `fleet-provider/synthetic-device-central-01` | The only published capacity |
| B | `System-2` | `7f3d0c2e-…-0a1b2c3d4e0b` | `52:54:00:77:01:0b` / `.11` | `fleet-provider/synthetic-device-central-02` | Enrolled at the site only, never a Device: it shares the lab BMC's key; proves target isolation |

The disks, domain UUIDs and MACs exist before any allocation. Each installation
or cleanup reuses target A's single persistent disk. The site enrollment is an
explicit Device-to-System mapping in the worker template, with each target's BMC
endpoint, System Id, UUID, MAC, disk size and console capture name. The lab sets
`"dhcp": "static"` because its dnsmasq leases are fixed; see
[Boot network](boot-network.md) for synchronized DHCP. Before any request, the
worker checks the enrollment against the admitted snapshot.

## Redfish worker contract

The Redfish workers (`internal/site/redfishwork`) run on the [site framework for
Device custody](../development/device-custody.md#site-execution-increment-b).
The framework owns the per-Device fence, the journal, the credential lookup, the
TLS pin and identity checks, the deadline and result delivery. A worker has no
libvirt client or lab dependency. A Redfish site refuses a libvirt URI and a
mounted libvirt socket, so a Redfish worker has no hypervisor fallback.

**Credential, pin and identity (GETs only).** Before any effect, the framework
reads the Device's own credential Secret and connects only to the key its
approved pin names. It walks the service root to the Systems collection and
reads every member. Exactly one member must carry the enrolled UUID, and it must
have the enrolled Id. A member with the enrolled Id and another UUID is a
renumbered or swapped target and is refused. The reset target and ActionInfo must
belong to that System. `On`, `ForceOff`, `Pxe` and `Hdd` must be advertised, and
`Once` too when the System advertises its enabled values. It never follows
redirects or `@odata.id` paths outside `/redfish/v1`. A rejected credential, a
missing capability or an identity mismatch sends nothing.

**BMC client library.** See [BMC client library](#bmc-client-library) for
which requests go through gofish and which helpers are deliberately not used.

**Exact inputs before effects.** The assignment carries the operation's inputs
exactly as admitted, with their digest, the fence and the snapshot. The framework
checks the digest, the fence and the enrollment, and journals each step before it
sends it. The worker records a boot admission with a random receipt and
capability and a 20-minute window, at the operation's fence.

**Phases.** An install is a network-boot installer phase (`Pxe`) followed by a
disk-boot phase (`Hdd`). A wipe is one network-boot phase. Each phase proceeds in
order:

1. Power off if needed. This is safe to repeat, because nothing of the phase has
   booted.
2. Arm a one-time override. This is safe to repeat.
3. In synchronized DHCP mode, wait until DHCP applied the reservation. See [Boot
   network](boot-network.md).
4. Journal `<phase>-power-on`, then `POST Reset On`.
5. Wait for exactly one console receipt, and for a network boot, the boot
   server's delivery evidence, and power off.

**Lost responses and recovery.** HTTP success is never evidence. Power-on is the
only step that is not sent again: after a crash or a lost response, a resumed
worker observes whether the boot began. Any sign of a boot counts: power not
`Off`, a consumed one-time override, a boot delivery for the operation, or a
receipt. With no sign, the outcome is unknown and nothing is sent again. The old
executor sent `On` again, up to a small budget, when the server was still off and
armed after a settle window; the workers do not.

**Completion.** A phase completes only with exactly one console line
`FLEET-INSTALLED:<receipt>`, `FLEET-DISK-BOOT:<receipt>` or
`FLEET-WIPED:<receipt>:<disk bytes>`, and power `Off`. A second identical receipt
means a duplicate execution and fails rather than succeeding. The installer
phase also requires completed boot deliveries of the script, kernel and initrd;
boot admission then closes. Each verified phase is journaled, so a resumed
worker continues from the next phase. Worker exit codes and the
Job/observer/coordinator split are unchanged:
[exit 0 never implies readiness](site-execution.md#authority-and-failure-contract).

**Cleanup uses a booted environment.** The wipe environment is the same pinned
kernel and initrd with `fleet.mode=wipe`. It zeroes the whole disk with `dd`,
drops caches, compares every byte with `/dev/zero` and only then prints its
receipt. The harness independently hashes target A's disk file as all zeroes
after the final cleanup. This is **logical test cleanup of a virtual disk, not
media sanitization**: it does not address SSD remapping, over-provisioning, HPA
or DCO areas, firmware caches or crypto-erase.

## BMC client library

`internal/redfish/client.go` uses
[gofish](https://github.com/stmcginnis/gofish) **v0.27.0** (2026-10-05, the
newest release at adoption, BSD-3-Clause, no transitive module dependencies). It
replaced the handwritten `net/http` request helper and the `map[string]any`
decoding of the service root, Systems collection and ComputerSystem. The
Redfish workers call its `Discover`, `Identify`, `Observe`, `SetBootOnce` and
`Reset` methods. The site framework owns validation, the journal, sequencing,
recovery and results.

**What gofish provides here:**

- the HTTP client: basic authentication, JSON requests and Redfish error
  decoding (`schemas.Error` status codes);
- typed `ComputerSystem` decoding: `Id`, `UUID`, `@odata.id`, `PowerState`,
  the boot override and its target `AllowableValues`, and the ETag;
- Systems collection walking, including `Members@odata.nextLink` pages. The
  previous client read only the first page, so a duplicate UUID on a later
  page went unseen; discovery now reads every page, and a partially read
  collection fails discovery.

**What Fleet still does itself, and why:**

- **Transport guard.** Every request passes a Fleet `RoundTripper`: the
  configured scheme and host only, paths under `/redfish/v1` without `..`, no
  query on a mutation, `OData-Version: 4.0`, and a 1 MiB response bound. A
  link returned by the BMC cannot send the credential elsewhere. Redirects are
  never followed. The environment proxy is not used, so the credential goes
  only to the enrolled endpoint. `NewPinnedClient` verifies a BMC by the SHA-256 of
  its leaf public key instead of the system trust store, through the same guard.
  [BMC discovery](bmc-discovery.md) and the site workers both use it, with the
  Device's approved pin. The endpoint must still be a literal `http(s)://IPv4:port`.
- **Identity and capability checks.** These are unchanged: one UUID match
  across all members with the enrolled Id, a reset target and ActionInfo on
  that System, and `On`, `ForceOff`, `Pxe`, `Hdd` and `Once` checked against
  advertised values.
- **ActionInfo and Reset annotations.** gofish keeps the reset target, the
  ActionInfo link and the `BootSourceOverrideEnabled` allowable values in
  unexported fields, so Fleet reads them from the System's raw JSON.
  ActionInfo is decoded leniently: gofish's typed `ActionInfo` rejects the
  DMTF emulator 1.2.2 document, whose `"Required": "true"` is a string.
- **Error classification.** Errors collapse to credential rejected,
  unsupported, identity, precondition failed, HTTP status, or
  transport/unreadable response. They never carry a URL, header or response
  body.
- **Mutations.** Each is exactly one request, issued through gofish's raw
  `PatchWithHeaders` and `PostWithHeaders`. Audited gofish helpers that are
  **not** used:
  - `ComputerSystem.SetBoot` sends a second PATCH to the `@Redfish.Settings`
    resource when the first fails, which could be a lost response.
  - `ComputerSystem.Reset` (via `PostWithTask`) resends without `If-Match`
    after a 412.
  - Sessions (`CreateSession`) would be a BMC mutation with its own lifetime
    and cleanup. Basic authentication is used instead, as before.

  The boot override PATCH now carries `If-Match` with the ETag observed
  immediately before it, when the BMC returns one. A 412 is reported as
  `ErrPrecondition` and not retried. The operation stays Unknown before the
  boot is requested, and the next attempt re-observes and re-arms; this is
  pre-boot and idempotent, as before. Reset POSTs carry no `If-Match`. A 202
  task monitor is not followed: power and boot state are observed, as before.

**bmclib was evaluated and not adopted.** bmclib v2.3.5 (2026-03-22, the
newest release) was rejected for these reasons:

- It pins gofish v0.20.0, whose API is incompatible with v0.27.0, so both
  libraries cannot be used together.
- Its Redfish provider selects a System by index or name, not by UUID.
- Its `SystemBootDeviceSet` retries a failed PATCH with fewer fields.
- `SystemPowerOn` skips the request when the System already reports `On`.
  This is incompatible with Fleet's recorded-request recovery.
- Its `bmc` power facade tries the next provider after an error.

Its vendor providers (Dell, Supermicro, ASRockRack, OpenBMC, IPMI) target BIOS,
firmware and virtual-media work that the workers do not perform. **No
vendor-specific compatibility is added by this change.** It reuses a
maintained Redfish client and schema model. It also fixes collection paging
and adds `If-Match` on the boot override.

**Known compatibility risks to qualify:**

- **Strict decoding.** gofish decodes the whole ComputerSystem strictly. A
  vendor property with an off-schema type fails discovery and observation as
  "unreadable response", with no effect issued. The emulator's System decodes
  cleanly.
- **`If-Match` rejection.** gofish documents that some BMCs reject `If-Match`
  on some endpoints, for example Dell iDRAC10. On such a BMC arming fails
  visibly as Unknown instead of sending an unconditional PATCH. Changing that
  is a per-vendor decision for qualification.

## Synthetic credentials and networking

These are documented lab seams, not production designs:

- **Credential and transport.** HTTP Basic over TLS. The harness generates one
  synthetic username, password and TLS key for the run. The adapter serves that
  key. Each Device has its own credential Secret, which holds the shared username
  and password and the key's digest as its approved pin. A worker connects only to
  that key, so a different key aborts the handshake before any credential is sent.
  The lab seam is that one key and one account serve both targets. There is no
  rotation, vault or per-BMC account. The Secrets are never in Git, a source
  revision or evidence. The harness keeps the password in a `.token` file so the
  evidence secret scan covers it. Worker and harness logs print outcome classes
  only.
- **Provisioning network.** The adapter defines one static libvirt network.
  dnsmasq gives each enrolled MAC a fixed address and the same boot file. That
  iPXE script chains to `/pxe/${net0/mac:hexhyp}/boot.ipxe`. The boot service
  serves a script only to the MAC of a retained operation with open admission.
  The MAC is routing information, not authentication: a peer on this network
  could impersonate it during the window, and the capability URLs travel in
  clear.
- **Receipts.** The worker reads the target's serial-console capture read-only,
  as a stand-in for serial-over-LAN or an installer callback. It is observation,
  not control.
- **Admission.** The Redfish overlay's admission policy limits kro worker Jobs
  to the site state claim, `emptyDir` and the per-Device credentials. It denies a
  `libvirt*` subPath, `/run/fleet-libvirt` or lab-disk mount and a
  `FLEET_LIBVIRT_URI`, and requires `FLEET_SITE_PROVIDER=redfish`. The `rbac`
  phase proves each denial as kro's identity, and evidence re-checks every real
  worker Job spec.

## Acceptance phases

`hack/multiplane/acceptance-redfish.sh` runs every scenario of the
[libvirt-mode sequence](multiplane.md#acceptance-phases) through the same
federation and kro path, with these differences. The phase descriptions follow the
harness at this change, and the run on custody passes every phase.

**Before topology:**

- `bmc-upstream` fetches and verifies the pinned archive;
- `bmc-image` builds the lab BMC image;
- `bmc-self-test` runs the adapter self-test with no network.

**Before Flux installs the shared services:** `redfish-enroll` creates the
synthetic credential and TLS Secrets, one credential Secret per Device, and the
fixture enrollment. The coordinator runs on the workstation
(`hack/multiplane/coordinator.sh`), and a separate discovery identity writes
fixture discovery for the lab Devices. Real BMC discovery is not part of this run.

**Redfish phases:**

| Phase | What it proves |
| --- | --- |
| `redfish-baseline` | Both target disks exist and neither target was ever started; records target B's disk digest |
| `redfish-discovery-faults` | Uses the real emulator through `fleet-multiplane redfish-probe` and the production discovery code. Each enrolled System is discovered exactly. A wrong credential exits 10. Target A's Id with target B's UUID, or an unknown UUID, exits 12. An injected unadvertised `On` exits 11. A TLS key other than the pin exits 13 and reaches no request. Every probe carries one run-scoped User-Agent, and the checks read only audit entries with it, so discovery or a worker reading the same BMC with the same credential cannot change the result. Those entries are GETs only. |
| `redfish-bmc-interrupt` | As soon as the installer is running, the BMC Pod is force-deleted and replaced. The guest keeps running; the worker tolerates a bounded BMC outage. |
| `interrupt-pod`, `interrupt-job` | As in libvirt mode, now on the Redfish worker, still inside the installer phase |
| `redfish-arm-lost-response`, `redfish-lost-response` | Armed before dispatch: the second effective `On` for System-1 (the first install's disk boot) is answered only after 45 seconds. The worker times out and exits 75 (Unknown). Its replacement observes the running or completed target and the consumed override. Exactly one `On` per phase was sent: `powerOnRequests` is one installer and one disk request. |
| `resume` | The recreated Job completes the exact operation with one succeeded Pod |
| `deadline-admit`, `machine-delete`, `deadline-hold` | After the wipe and reuse, consumer B's installation runs under the lab installation deadline with the lab boot hook armed. Its `On` is sent and its installer is never served. The Machine is deleted mid-install, the installation ends `OutcomeUnknown` at its deadline, and a mismatched staff recovery is refused with no BMC effect. The release wipe powers the target off and settles the hold. |
| `boot-denials` | Closed per-MAC scripts and capabilities of all four operations, target B's MAC and unknown identities are denied over the live boot Service |
| `redfish-isolation` | Target B: unchanged disk digest, no QEMU start, no console line, no mutating Redfish request. Target A: disk hashes as all zeroes. No domain running. Exports the BMC audit. |

The final `evidence` phase adds these independent checks in Redfish mode:

- **Console.** Target A's console holds exactly one receipt line per boot, in
  operation order: install, disk boot, wipe, wipe. B's held installation
  prints none.
- **QEMU.** libvirt's append-only log shows exactly five starts of target A, in
  the boot order network, disk, network, network, network (read from
  `bootindex`), and none of target B.
- **BMC audit.** Exactly five effective `On` requests, all to System-1, of which
  exactly one carried the lost-response fault.
- **Kubernetes and admission.** Every worker Job selects the Redfish provider
  with no hypervisor mount or URI, and the admission probes denied each
  hypervisor path.

Redfish-specific evidence files are `redfish-upstream.json`,
`redfish-enrollment.json` (no password), `redfish-discovery-faults.json`,
`redfish-lost-response.json`, `redfish-bmc-interrupt.json`,
`redfish-isolation.json` and `redfish-audit.jsonl`. `summary.json` records the
`physicalHardware`, `physicalBMC`, `vendorRedfishQualified`, `bmcTLS` and
`mediaSanitization` flags as `false`.

## Offline checks

```sh
go test -race -count=1 ./internal/redfish ./internal/site/... ./cmd/fleet-multiplane ./internal/multiplane
task multiplane:test-harness       # includes hack/multiplane/test-redfish.py
```

The Go tests in `internal/site/redfishwork` use a fake BMC whose fake guest
fetches its script and artifacts from the real boot handler and writes console
receipts. The site framework's tests in `internal/site` run against TLS stubs.
Together they cover:

- an install with both phases and one power-on each, a wipe that checks the
  enrolled disk, and a bootstrap reference refused at the site
  (`TestInstallationRunsBothPhasesWithOneOnEach`, `TestWipeChecksTheEnrolledDisk`,
  `TestBootstrapIsRefusedAtTheSiteToo`);
- a worker interrupted in either phase that resumes without a second power-on
  (`TestAnInterruptedWorkerResumesWithoutASecondOn`,
  `TestAWorkerInterruptedInTheDiskPhaseResumes`);
- a server that never powers off ending the step
  (`TestAServerThatNeverPowersOffEndsTheStep`);
- a boot admission open only at the current fence
  (`TestOnlyTheCurrentFenceHasAnOpenBootAdmission`);
- synchronized DHCP following the open admission (`TestSyncedDHCPFollowsTheOpenAdmission`);
- a pin mismatch and an identity mismatch sending nothing
  (`TestPinMismatchSendsNothing`, `TestIdentityMismatchSendsNothing`), and a
  rejected credential, unsupported reset and renumbered or swapped UUIDs, each
  with no effect.

`client_test.go` covers the gofish-backed client against a recording stub:

- discovery across a paged Systems collection;
- a duplicate UUID on a later page refused;
- an unreadable, rejected, foreign or cross-host member refused, with no
  mutation;
- a reset target or ActionInfo off the System, or no `Once`, refused;
- an oversized response refused;
- the boot override sent as one PATCH with the observed `If-Match`;
- a 412 or 500 not retried and never redirected to the Settings resource;
- a Reset that gets 412, 503 or no response before the timeout sent exactly
  once, without `If-Match` or a session;
- an untrusted TLS certificate refused;
- no environment proxy;
- no credential, response body or endpoint in any error.

The workers' fake BMC serves the emulator's string `"Required"` in
ActionInfo.

`test-redfish.py` renders the overlays and a staged Redfish revision with
kustomize and checks the following:

- worker mounts and environment;
- privilege and socket holders;
- enrollment matching `targets.json`;
- variable substitution;
- phase coverage;
- that no fault hook appears in `internal/redfish`;
- source pins, and no OpenStack dependency.

## Execution record

The records below describe runs on the path before the cutover to Device custody,
with the old executor, a site-wide journal and a shared credential over plain
HTTP. They show what the lab and the harness proved then. They are not evidence
for the Redfish workers. The multi-plane Redfish run on custody is implemented
and passes every phase.

### Attempts before the first pass (2026-11-16)

Every attempt below was a fresh topology owned by this task. Each failure was
inspected from its retained private state, including the stopped cell node's
volume read with `docker cp` and no restart. Each was then discarded with
`teardown.sh --discard-unknown`, which is **not release evidence**. No Unknown
operation was reset or declared ready.

1. **`fleet-mp-8a37c720`** failed in `milo-bootstrap`, before any allocation or
   effect. The new `$state/provider` mode marker collided with the Milo provider
   project's credential directory. The marker is now `$state/site-provider`, and
   `test-redfish.py` guards it.
2. **`fleet-mp-24371388`** failed in `interrupt-pod` after 37 phases had passed,
   including discovery faults and lost-response recovery on the installer `On`.
   The real PXE install (one network start, one `On`, an exact receipt) took
   about 47 seconds, which left no window for the worker interruptions. The BMC
   interruption now runs as soon as the installer starts, and the lost-response
   fault targets the disk-boot `On`.
3. **`fleet-mp-052586a6`** failed in `release` after 50 phases. The wipe
   environment zeroed the disk, but `cmp -n 2147483648` was rejected (`not in
   0..2147483647`). The guest therefore printed no receipt and powered off, and
   the worker kept the cleanup Unknown and retried without inferring success.
4. **`fleet-mp-90c15839`** failed the same way. The chunked `cmp -i` fix had been
   tested with GNU `cmp`, but the guest's static BusyBox shell runs its own
   applets first, and BusyBox `cmp` has no `-i`. The verifier now calls
   `/bin/busybox cmp -n 1GiB … SKIP1 0` per chunk, tested against Debian's
   BusyBox 1.35 for a clean disk and for a dirty byte in either GiB. Again no
   receipt was printed and the cleanup stayed Unknown.
5. A launch exited 2 in preflight, with 11.6 GiB free against the 12 GiB gate,
   and created nothing. Teardown had not removed the new BMC image tags. It now
   does, and `test-redfish.py` checks it. This task's leftover BMC tags and
   unused build cache were removed.
6. **`fleet-mp-836a521e`** failed in `delivery-faults` after 54 phases. The
   booted wipe cleanup, release projection and reuse had passed. Three fault
   Jobs started together, and one worker's `redfish.Open` lost the non-blocking
   journal lock while recording the enrollment, exiting 1 before validating its
   delivered authority. Its retry then failed by policy with 65. Enrollment is
   now retained by an atomic create-by-link and an unlocked compare, and a Go
   test opens eight workers concurrently while another holds the journal lock.
   No effect was issued for any fault handoff.

7. **`fleet-mp-f7008e93`** failed in `flux-bootstrap-hub`, before any fixture or
   effect. The hub node's pulls of the digest-pinned Flux controller images from
   `ghcr.io` hit `DeadlineExceeded`, a transient external network failure. The
   stopped hub node was started only to read its Pod events.

8. **`fleet-mp-17555913`** failed in `redfish-bmc-interrupt` after 33 phases. Run
   right after dispatch, the step read the journal before the first worker had
   created it, and treated the absent file as fatal. The wait now treats an
   absent journal as not yet started. The install was in flight when the run
   stopped; its custody was Unknown.

9. **`fleet-mp-5c4ce7c2`** passed 62 of 63 phases, including every Redfish,
   federation, Flux, release, reuse, reinstall and isolation phase. Target B
   never started and its disk digest was unchanged, and target A's disk hashed
   as all zeroes after six boots and twelve Redfish effects. It failed in the
   final `evidence` phase: a reworded `redfish-baseline` message no longer
   matched its evidence marker. `test-redfish.py` now checks that every
   Redfish evidence marker is printed by a phase. This run is not counted as a
   pass.

Attempts 1 to 4 used the original `net/http` client; attempts 6 to 9 used the
gofish client.

### Fresh pass (2026-11-16)

`FLEET_MULTIPLANE_INOTIFY=1024 hack/multiplane/run.sh --redfish` (the command
behind `task multiplane:redfish-acceptance`) exited 0, with the readiness test
gate enabled, which includes `./internal/redfish`. Run details:

- state `/tmp/fleet-multiplane.NryZRD`, prefix `fleet-mp-34dd4bcc`;
- all 65 bounded phases passed in 34 minutes (22:20 to 22:54 UTC);
- SHA-256 of `source-SHA256SUMS` (177 entries):
  `6409311d0af13cdffeb09333b3ef7822c25b296b20ed4c06214349c5e9ee0963`;
- site binary `f0ddaf41…`, host binary `b43045e0…`; both use the gofish client;
- site image `sha256:d8e874680f7ee6f047a0cf03f85b25270ec13df1f381448b8c2f7dd5e1e3c24b`;
- lab BMC image `sha256:e4cd737b991570b8f2e6ac373ada523b18bafbd560bebca466851017fdbc0f55`;
- both pulled by the cell by registry digest;
- seven Flux source revisions from the dirty checkout at HEAD `d7ff793b`.

After the run, `teardown.sh` was changed to also delete the run's synthetic BMC
credential file after a pass, and `test-redfish.py` gained checks for that.
This run's teardown had removed every container, cluster and image tag but left
that file, which was deleted by hand. No other fingerprinted file changed.

Only `evidence/` is shareable. It holds `summary.json`, `result.json`,
`phases.jsonl`, the `redfish-*.json` files, `redfish-audit.jsonl` and the files
listed for the multi-plane harness. The facts below come from that evidence.

- **Upstream and authentication.** The pinned emulator archive was verified,
  and the image self-test passed. Live discovery through the production client
  found `System-1` and `System-2` exactly. A wrong credential exited 10, the
  swapped and unknown UUIDs exited 12, and an injected unadvertised `On` exited
  11. Those 38 requests were GETs only, one of them rejected.
- **Install with interruptions.** The BMC Pod was force-deleted at
  `installer-started` and replaced while the guest kept running. The worker Pod
  was then force-deleted, and the Job controller replaced it. The Job was
  deleted mid-installer, and kro recreated it.
- **Lost response.** The disk-boot `On` took effect, but its response was
  withheld for 45 seconds. The worker exited 75. Its replacement observed the
  consumed override and powered target and did not repeat the `On`
  (`powerOnRequests` = one installer and one disk request).
- **Partition and federation.** As in the libvirt mode, partition kept public
  Ready False with the Job Complete. Heal, idempotent install and lifecycle
  recreation passed. Graph lifecycle, drift repair, controller restart,
  fail-closed and retention passed with the BMC included in the ownership
  audit. Nine reflection faults were rejected.
- **Cleanup and reuse.** Consumer A's cleanup PXE-booted the wipe environment.
  Consumer B reused the target in epoch 2, installed on it with a new receipt,
  and was cleaned the same way. Release projection was verified for both.
- **Delivery faults.** The duplicate, wrong-cell and stale-epoch deliveries each
  failed once with exit 65 by `podFailurePolicy`, with the journal unchanged,
  and kro pruned them. Wrong location was denied with free capacity.
- **Closed boot.** 24 live HTTP denials covered the closed per-MAC scripts and
  capabilities of all four operations, target B's MAC and unknown identities.
- **Independent checks.**
  - Target A's console held exactly one receipt per boot in operation order.
  - libvirt logged six target A starts: network, disk, network, network, disk,
    network.
  - The BMC audit shows exactly twelve effects, all to System-1: six one-time
    overrides and six effective `On` requests, one of them faulted.
  - Target B had no start, no console line, no effect and an unchanged disk
    digest. Target A's disk hashed as all zeroes.
  - All four worker Jobs, created by kro, selected the Redfish provider with no
    libvirt mount or URI.
  - Admission denied the libvirt socket, lab disk, libvirt URI, libvirt-provider
    and foreign-Secret worker shapes, and admitted the Redfish worker shape.
- **Memory, secrets and teardown.** There were zero OOM kills. 40 evidence files
  were clean against 27 retained secrets, including the BMC password. Teardown
  removed every `fleet-mp-34dd4bcc` container, cluster and image tag, and
  restored inotify to 128.

This proves the composed pipeline against an emulator and lab VMs. It is not
an independent review, and it does not qualify any physical BMC or server.

### Repeat pass (2026-11-17)

The same command was rerun on a fresh topology and exited 0. Run details:

- state `/tmp/fleet-multiplane.MF4gq9`, prefix `fleet-mp-717e7e96`;
- all 65 bounded phases passed in 34 minutes (23:53 to 00:27 UTC);
- SHA-256 of `source-SHA256SUMS` (177 entries):
  `c560df28646c1fe2f9b5315886ffe0f9a12fca20c6703c8af10be849ac850023`;
- site binary `f0ddaf41…` and host binary `b43045e0…`, identical to the first
  pass, so the executor and its gofish client are unchanged;
- site image `sha256:b484df32c64072896d019e38ed31d065daeb3d9a1d775fc47a5a6681111a6dc4`;
- lab BMC image `sha256:10b8bbb739a4288e734b3a515adb2a92c8fbd1c3170b9ebeab5fc0d74568e935`.

The evidence shows the following:

- **Discovery probes.** They matched the first pass: both Systems were found,
  with ResetTypes read from the emulator's ActionInfo. The probes exited 10,
  12, 12 and 11 for the injected faults, with no effect.
- **Lost response.** The lost disk-boot `On` was recovered without a repeat
  (`powerOnRequests` = one installer and one disk request).
- **Isolation.** Target B received no effect, and target A's disk hashed as
  all zeroes.
- **BMC audit.** It recorded 589 GET 200, 6 PATCH 200, 6 POST 204 and one GET
  401. There were no 412 or 5xx responses and no session creation. The
  emulator sends no ETag, so `If-Match` was not exercised.
- **Teardown.** This time teardown itself removed the synthetic BMC
  credential file and left no containers.

Physical hardware, physical BMC, vendor Redfish, BMC TLS and media
sanitization all remain `false` in `summary.json`.

## Limitations and physical-hardware readiness

These differences from physical hardware are known and **not** addressed by this
MVP. They are listed as an explicit qualification checklist:

1. **Real BMC.** Qualify the Redfish workers against at least one vendor Redfish
   implementation. Check ActionInfo and inline `AllowableValues`, the actual
   reset semantics (some BMCs reject `On` while on, or report `PoweringOn`), and
   whether `Once` overrides are consumed at POST. Check ETag/`If-Match`
   behavior on the boot override PATCH (a 412 is never retried
   unconditionally), strict gofish decoding of the vendor's ComputerSystem,
   session (`X-Auth-Token`) authentication and rate limits.
2. **UEFI.** Only legacy BIOS (SeaBIOS) PXE is exercised. Verify
   `BootSourceOverrideMode`, UEFI PXE or HTTP boot, Secure Boot policy and boot
   order persistence.
3. **Receipts.** Replace the read-only console capture with serial-over-LAN or
   an authenticated installer callback, and decide its trust model.
4. **Credentials and transport.** Workers read a per-Device credential Secret
   and connect only to the approved pin, but the lab uses one account and one key
   for both targets. Still open: per-BMC accounts, a credential broker so
   workers never hold a long-lived password, and rotation and revocation of both
   the credential and the pinned key.
5. **Provisioning network.** Synchronized DHCP reservations with served and
   withdrawn evidence are implemented and unit tested
   ([Boot network](boot-network.md)) but not exercised at a site. Still open:
   attachments allocated by Networking/IPAM, a dedicated provisioning VLAN,
   and authentication beyond MAC.
6. **Cleanup assurance.** Use vendor or NIST SP 800-88 sanitization (secure
   erase, crypto-erase, sanitize commands) with verifiable device evidence.
   Distinguish it from this logical overwrite.
7. **Power semantics.** Physical power transitions take longer, may fail and may
   report intermediate states. Revisit the settle window, the outage tolerance
   and the five-minute power-off bound. A worker never sends `On` a second time;
   it observes. Revisit installer and disk-boot power-off as the completion
   signal; a real OS reboots and keeps running.
8. **Fencing and the site store.** Each Device has its own fence and journal on
   one retained volume. Store durability across volume loss and HA are not
   designed. Never reset the store of a retained target
   ([known limitations](multiplane.md#known-limitations-of-the-local-mvp)).
9. **Enrollment.** The Device-to-System mapping is a static source value.
   Enrollment trust and attestation of the System identity (beyond Id and
   UUID) are open. Read-only [BMC discovery](bmc-discovery.md) reports
   observed identity into Device status but does not enroll.
10. **Operator recovery.** An unknown operation, for example a power-on with no
    sign that the boot began, holds the Device. A person settles it with a
    `DeviceRecovery`. Nothing retries it automatically, and the multi-plane run
    does not exercise recovery. Its only resolution there is to discard the
    disposable topology after inspection.

The existing local-MVP limitations also apply: single node, trusted graph and
service authors, and no image upgrade lifecycle.
