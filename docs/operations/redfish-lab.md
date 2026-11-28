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
**implemented**, and its verification is pending. The execution records at the
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
| Append-only request audit: method, path, status, principal class, ResetType, boot override, effect and fault. It never records bodies or credentials. | Adapter |
| `DELETE /redfish/v1/reset` and non-Redfish paths | Refused by the adapter |
| Bounded, one-shot lab faults (`reject-auth`, `hide-reset`, `drop-response`) from a fault file | Adapter, lab only |

`hack/redfish-lab/test_adapter.py` runs inside the built image, with the real
upstream emulator and a fake `virsh`, in the `bmc-self-test` phase.

## Topology on the selected cell

```text
             cell Flux layers (Redfish overlays of config/site)
 fleet-site-runtime (privileged)     fleet-site-bmc (unprivileged, all caps dropped)
   libvirtd + QEMU TCG  ◀── libvirt socket ── DMTF emulator 1.2.2 + lab adapter
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
| One credential Secret per Device in the `fleet-site-credentials` namespace, named `bmc-` and 20 hex digits of the Device's SHA-256, labelled `fleet.datumapis.com/bmc-credential` | The harness, in `redfish-enroll` | The same username and password, and `tlsPublicKeySHA256`, the digest of the run's BMC key: each Device's approved pin. This follows the [BMC discovery](bmc-discovery.md) contract. Each operation's worker may get only its own Device's Secret, which its handoff names; it cannot list, watch or get another's. The `worker-rbac` phase checks this. |
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
endpoint, System Id, UUID, disk size and console capture name. Target A's boot
NIC MAC is a Device inventory fact, seeded from the lab's VM definitions. The
lab sets `"dhcp": "static"` because its dnsmasq leases are fixed; see
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

## Synchronized DHCP mode

`hack/multiplane/run.sh --redfish --synced-dhcp` runs the same Redfish
acceptance with the site's synchronized DHCP instead of the static leases. It
exercises boot addresses, reservations and Kea end to end in the lab:

- **No lab DHCP.** The lab BMC defines the provisioning network with
  `FLEET_LAB_DHCP=synced`: the same bridge and NAT, with no `<dhcp>` element, so
  libvirt starts no dnsmasq.
- **Kea on the bridge.** The bridge lives in the runtime Pod's network
  namespace, so Kea and `fleet-dhcp-sync` run as two more containers in that
  Pod (`config/site/redfish/synced/services`). Kea is Debian's
  `kea-dhcp4-server` from the site image, which is native to the arm64 lab;
  ISC's image is amd64 only. Kea is not privileged: it adds only `NET_RAW` and
  `NET_BIND_SERVICE`. It listens on `fleetpxe0` with a raw socket, has no pool,
  and keeps its leases on the site volume. The production component differs:
  its Kea answers relayed requests on a UDP socket and adds only
  `NET_BIND_SERVICE`. The synchronizer reports as the one
  fixed instance `lab-kea`, so a restarted runtime Pod never strands a
  withdrawal. In production Kea runs in a central cluster behind the site's
  relays; the lab has no WAN link or relay, so its Kea sits on the emulated
  site's bridge instead. The feed, the reports and the gates are the same.
- **Feed.** The `dhcp-feed` container from `config/site/dhcp/feed` runs beside
  the boot service over TLS. Redfish enrollment generates the synchronizer
  token and a self-signed feed key per run, as Secrets in `fleet-site`, never
  in Git or evidence.
- **Worker mode.** `config/site/redfish/synced/graph` sets the worker
  enrollment's `dhcp` to `synced`, with `lab-kea` as its one expected DHCP
  instance, so every network boot needs a boot address and waits for Kea to
  apply and withdraw its reservation. The feed reads the same enrollment from
  a ConfigMap in `config/site/redfish/synced/services`.
- **IPAM.** Boot addresses come from the real IPAM service, described below.
  Fleet's own code claims, checks at admission and releases the addresses.
  The static leases (.10 and .11) are outside the range IPAM allocates.

### IPAM in the lab

Synced mode runs IPAM (`milo-os/ipam` v0.6.0) on the platform cluster, beside
the provider project's Milo API. It is set up the way the platform runs it:

- **Published manifests.** The `ipam-apiserver` Flux layer reconciles IPAM's
  published Kustomize bundle (`oci://ghcr.io/milo-os/ipam-kustomize`, its
  `base`). The bundle and the IPAM image are pinned by digest. The lab changes
  only four things: the serving key is a Secret instead of a cert-manager CSI
  volume, authentication and authorization go to the provider Milo API, quota
  is off, and it runs one smaller replica.
- **Its own PostgreSQL.** The `ipam-system` layer runs a single PostgreSQL
  17.10 StatefulSet, pinned by digest, in IPAM's namespace. IPAM owns this
  storage. Fleet adds no database and never connects to it. IPAM's own
  PostgreSQL component is not used because its chart version and image float.
- **Aggregated through Milo.** The provider Milo API runs its aggregation layer
  with a front-proxy client key and a CA that bootstrap generates per run.
  Bootstrap registers the `v1alpha1.ipam.miloapis.com` APIService in that API.
  Milo proxies project requests
  (`/apis/resourcemanager.miloapis.com/v1alpha1/projects/provider/control-plane/apis/ipam.miloapis.com/...`)
  to IPAM, forwarding the caller and the project extras
  (`iam.miloapis.com/parent-{api-group,type,name}`) as `X-Remote-*` headers.
  IPAM trusts that CA for the headers. It sends TokenReview and
  SubjectAccessReview to the same Milo API with its own static token, which is
  in `system:masters` like the platform's IPAM client identity. The static-token
  RBAC that bootstrap writes therefore decides what each Fleet identity may do
  in IPAM.
- **Availability seam.** On the platform, Milo's controller-manager marks a
  remote APIService `Available`. The lab runs no controller-manager, so
  bootstrap sets the condition once Flux reports IPAM's apiserver Ready.
- **Quota off.** The lab Milo API serves no quota API, so IPAM runs with
  `ENABLE_QUOTA=false`. Claims are not counted against any quota, which the
  platform does count.
- **Fixtures.** In the provider project, bootstrap creates the `lab-provisioning`
  IPClass (IPv4, Private, /32 only, reclaim Delete). It also creates an IPPool
  for 192.168.77.0/24 scoped to the lab Location, which reserves the first 100
  and last 56 addresses, so IPAM allocates only .100–.199. Kea's base subnet is
  the same /24 with the bridge at .1.
- **Credentials.** Bootstrap generates IPAM's Milo token, serving key, database
  password and kubeconfig per run and enrolls them as Secrets in `ipam-system`.
  None is in the source or the evidence. `evidence/ipam.json` records the
  running image digest, the APIService condition, the class and the pool.

The `redfish-dhcp-synced` phase runs after `boot-denials` and writes
`evidence/redfish-dhcp.json`. For the whole acceptance run, a background
sampler (`redfish_steps.py ipam-watch`) reads every IPClaim and IPAllocation in
`fleet-provider` every two seconds. Those records outlive the claims, which
are gone by the time the phase runs. A failed read is recorded as such, never
fatal, and the phase requires the sampler's last state to be under a minute
old. The phase fails unless:

1. every installation and wipe carried a boot address from its own claim
   (`<operation>-boot`), within .100–.199;
2. for each operation that ran at the site, while its boot was open, IPAM
   recorded that claim Bound to the operation's address and naming an
   IPAllocation, and that allocation names the claim back and holds exactly
   the address as a /32. If IPAM could not be read for the whole window,
   samples just before and just after it must show the same allocation, with
   only failed reads between;
3. each operation that ran at the site published the snapshot's boot MAC at its
   address, and Kea reported serving it after that boot opened and reported it
   withdrawn later;
4. for every operation that powered on for a network boot, Kea leased the boot
   MAC that operation's address during that operation's own boot window, and
   at least two installations and two wipes did. Every lease Kea granted falls
   in the boot window of an operation whose claim IPAM held that address for,
   and no lease is a static or out-of-range address;
5. after both releases, no IPClaim and no IPAllocation remain in
   `fleet-provider`, and the pool reports 156 of 256 addresses allocated: its
   reservations and nothing else;
6. the lab network has no `<dhcp>` element and no dnsmasq runs; and
7. Kea's last report serves nothing.

The second installation is consumer B's, on the same server after consumer
A's release. It gets its own claim and reservation, and its firmware leases
that address from Kea, but the failure scenarios withhold its boot script and
hold it at its deadline, so it never installs. Both release wipes boot and
complete. IPAM's first-fit allocation hands a released address out again, so
freshness is judged by claim, allocation, lease and report times, not by
address.

What the lab proves: Fleet's requesters, admission check and release
verification work against the real IPAM API through Milo's project routing and
aggregation, and Kea serves only addresses IPAM allocated to each operation's
own claim. What it does not prove: quota, Milo's OpenFGA-backed IAM (the lab
uses static-token RBAC), IPAM's high-availability deployment, pool exhaustion
through IPAM (unit tests cover how Fleet handles a 507 `Exhausted`), and
anything about a physical site's IPAM pools.

The mode is implemented and its offline checks pass. No synced run has passed
yet.

## Acceptance phases

`hack/multiplane/acceptance-redfish.sh` runs every scenario of the
[libvirt-mode sequence](multiplane.md#acceptance-phases) through the same
federation and kro path, with these differences. The phase descriptions follow the
harness at this change; verification of the run on custody is pending.

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
| `redfish-discovery-faults` | Uses the real emulator through `fleet-multiplane redfish-probe` and the production discovery code. Each enrolled System is discovered exactly. A wrong credential exits 10. Target A's Id with target B's UUID, or an unknown UUID, exits 12. An injected unadvertised `On` exits 11. A TLS key other than the pin exits 13 and reaches no request. The BMC audit shows only GETs in the window. |
| `redfish-bmc-interrupt` | As soon as the installer is running, the BMC Pod is force-deleted and replaced. The guest keeps running; the worker tolerates a bounded BMC outage. |
| `interrupt-pod`, `interrupt-job` | As in libvirt mode, now on the Redfish worker, still inside the installer phase |
| `redfish-arm-lost-response`, `redfish-lost-response` | Armed before dispatch: the second effective `On` for System-1 (the first install's disk boot) is answered only after 45 seconds. The worker times out and exits 75 (Unknown). Its replacement observes the running or completed target and the consumed override. Exactly one `On` per phase was sent: `powerOnRequests` is one installer and one disk request. |
| `resume` | The recreated Job completes the exact operation with one succeeded Pod |
| `deadline-admit`, `machine-delete`, `deadline-hold` | After the wipe and reuse, consumer B's installation runs under a six-minute deadline with the lab boot hook armed. Its `On` is sent and its installer is never served. The Machine is deleted mid-install, the installation ends `OutcomeUnknown` at its deadline, and a mismatched staff recovery is refused with no BMC effect. The release wipe powers the target off and settles the hold. |
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

## Virtual media suite (prototype)

A prototype of virtual media, the second boot delivery method proposed in
[#227](https://github.com/datum-cloud/fleet/pull/227), built to the
product owner's revision of that contract. It runs as its own Redfish-mode
suite:

```sh
task multiplane:media-acceptance
# equivalent:
FLEET_MULTIPLANE_INOTIFY=1024 hack/multiplane/run.sh --redfish --virtual-media
```

Status: **implemented and verified by unit tests**. The end-to-end record is
in the pull request that adds it. Network boot is unchanged behind the same
seam; the regular Redfish suite above covers it.

### How an install works

1. The Metal provider selects the method: the held installation's method
   when it settles a hold, or else the first method in the site's order the
   server is qualified for. The installation records the method, the central
   media service, the address mode and the two phase windows. Admission
   checks them again under the custody update.
2. The worker renders a config medium with a fresh report token and salt,
   and opens the operation's window at the central media service. The
   service opens it only while custody names that operation active at that
   fence.
3. The worker ejects any leftover media, inserts the boot and config media,
   reads both URLs back by digest, sets a one-time boot from CD and powers
   the server on. The BMC fetches both media over HTTPS it verifies.
4. The installer installs the image, reads the bootstrap payload back from
   disk, and powers off. It reports nothing.
5. The worker boots the installed disk once. At first boot the installed OS
   brings up its data network and posts its report over HTTPS, verifying the
   media service: the report token, the boot medium's content digest and the
   salted bootstrap digest.
6. The worker takes exactly one report with the token and the expected
   digests before the report deadline. It then closes every window of the
   Device, ejects every device, and proves release from the BMC (every
   device empty, no override) and the media service (no open window, no
   transfer in progress, no reused token).

Any step that was sent and cannot be proven ends `OutcomeUnknown` with a
hold, after an attempt to undo delivery that is never proof. A window the
media service refuses outright ends the operation `Failed` when the journal
holds no BMC step, and `OutcomeUnknown` otherwise.

A site may stop offering a method at any time: new operations no longer
select it, and a running one finishes with it. A hold prepared with it waits,
because only that method can prove its release. Admission refuses a settling
operation while no serving site offers the method, and the coordinator
records `HeldMethodNotOffered` on the hold for staff until a site offers it
again. The serial
console is diagnostics only and never carries a token.

The deadline is the sum of the fetch window, 15 minutes by default, and the
report window, 30 minutes by default. A site may set shorter ones; the lab
uses 5 and 10 minutes. `FLEET_MULTIPLANE_MEDIA_FETCH` and
`FLEET_MULTIPLANE_MEDIA_REPORT` shorten them for a faster runtime, such as
a CI runner's KVM guests. Each held installation waits out both windows.

### What is real and what is stubbed

| Part | In the prototype |
| --- | --- |
| Delivery inputs | Real. Each installation records its method and the method's inputs before admission; the input digest pins them. |
| Site declaration | Real. The site enrollment's `fleet-site-delivery` ConfigMap declares the site's methods in preference order. The observer publishes it on each report handoff, and the coordinator and the Metal provider read it there. A change is a source revision, with no coordinator restart. |
| Admission | Real for the site and qualification checks, the settling-hold rule, input consistency and digest-pinned downloads. A machine class cannot restrict methods yet. |
| Qualification | Stubbed. Staff set the `fleet.datumapis.com/qualified-delivery-methods` annotation on the Device. A Device without it is qualified for network boot only. Commissioning will replace it. |
| Central media service | Real windows, fences from custody, media, report endpoint, transfer records and a background sweep that drops config media no window can serve. The sweep skips and counts a window file it cannot read, and goes on. In the lab it runs on the workstation beside the coordinator, on loopback, which lab Pods reach through the runtime's host gateway. Guests reach it through the report relay. |
| Worker authentication | Real. Each site worker presents a client certificate from the site workers' CA. The certificate is its own Secret, `fleet-site-worker-media`, one per site worker identity, in the `fleet-site-worker-identity` namespace. Each worker gets exactly that Secret through a per-operation Role, and BMC credential Secrets hold only BMC credentials. Only virtual media reads it: network boot installs and wipes neither read nor need it. The service refuses every window operation from a client with no certificate, or one that is not the worker for that Device's site. |
| BMC | Real Redfish VirtualMedia on the lab emulator: two CD devices, insert over verified HTTPS, eject, readback, one-time boot from CD. |
| First-boot report | Real. The installed lab OS posts it over HTTPS. Bootstrap data is always empty because the sealed store is not on this branch; admission still refuses bootstrap data. |
| Installer downloads | Admission refuses any download that is not pinned by digest. The config medium lists the pinned downloads, and the first-boot report names each download by the digest the installer fetched. The worker refuses a report whose downloads are not exactly the recorded ones. The lab installer fetches nothing; it reports what its config medium lists. |
| Report source | Real. Each virtual media installation records the Machine's address, which IPAM bound to the operation's own claim. The media service takes the report only from that address, and only inside the site's report ranges; a report from another address in the ranges is refused and recorded, token or not. The path from lab guests to the workstation rewrites their addresses, so guests post to a relay on the lab bridge, which names each guest's address to the service's relay listener in a PROXY protocol header. The service trusts that header only from its configured relays. The installed OS adds its IPAM address beside its DHCP lease and reports from it. The lab proves the service's check, not the network's binding: its relay set is all of loopback, because every lab Pod arrives there, so any lab Pod could name any source and only the report token stops it. Guests cannot reach the relay listener, but on the shared lab bridge a guest can take any lab address, as the stray-source fault does. A real site network, where only the relay or load balancer reaches the relay listener and the data network serves only the Machine its address, still needs verifying. |
| BMCs that do not verify HTTPS | Stubbed. Staff record `fleet.datumapis.com/virtual-media-tls: Unverified`, and admission refuses virtual media for that server. The fallback that mounts only the public medium and fetches bootstrap data with a single-use token is not implemented. |
| Lab data network | The lab provisioning network stands in for the customer's data network. Its firewall lets guests reach the boot Service and the central media service's report endpoint, and nothing else. |
| Addresses | `DataNetwork` only: the config medium carries no address and Fleet assigns none. `Static` needs NSO interface claims, which are not on this branch. |
| Media service client scoping | Weaker than proposed. In the lab every request reaches it from loopback, so it cannot tell the Device's BMC from any other client. |

Talos installs from its ISO by pulling the installer container image that
`machine.install.image` names from a registry, so a Talos image offered on
virtual media must list that image, pinned by digest, as a download. Talos
v1.9.5 installs from a digest-only reference: booted from its metal ISO under
QEMU with `machine.install.image` set to
`ghcr.io/siderolabs/installer@sha256:9e6c6564b870813b0f88032dabf1140fd1157f9ecf8736dfbf8d3df25cb891dd`,
it pulled that reference, installed, and booted the installed disk.

### Suite phases

| Phase | What it proves |
| --- | --- |
| `media-not-offered` | While the cell declares network boot only, a virtual media installation is refused with `DeliveryMethodNotOffered`. The fence does not change and nothing is delivered. |
| `media-central`, `media-offer`, `media-offered` | The central media service starts with the image's pinned artifacts. Revision 3 adds virtual media after network boot to the site enrollment, and the coordinator reads it from the cell's report without a restart. Staff record target A as qualified for virtual media only. |
| `media-unpinned` | An installation whose installer downloads an image by tag is refused with `InstallDownloadUnpinned`. |
| `media-arm-corrupt-token`, `media-install-held`, `media-held-report` | The lab BMC's config medium carries another report token. The installed OS's report, from its IPAM address, is refused for its token. Reports posted straight to the service, over IPv4 and IPv6 loopback, come from outside the site's report ranges and are refused for their source. No report is taken, and the installation ends `OutcomeUnknown` at its report deadline with a hold that records virtual media. |
| `media-arm-add-download`, `media-install-download`, `media-held-download` | The next installation settles that hold with the held method. The lab BMC adds a download the operation never pinned to its config medium; the first-boot report names it, so the report is never verified and the installation is held. |
| `media-arm-lost-eject`, `media-install-lost`, `media-held-eject` | The next installation settles that hold with the held method. Its report is taken, then the BMC withholds an eject response past the client timeout: `OutcomeUnknown`, release never journaled. |
| `media-arm-stray-source`, `media-install`, `media-stray-address` | A fourth installation settles the third hold, succeeds, and clears it. Its config medium makes the installed OS post its report, with the right token, from another address in the site's ranges first. The media service refuses and records that report, then takes the one from the address IPAM bound to the installation's claim. |
| `media-release-proof` | The BMC reads back both devices empty and no override. The media service reports four windows, none open, no transfer in progress and no reused token, and denies each one live. No config medium is kept anywhere. |
| `media-stale-fence` | For each of the four operations, the media service refuses a window open with no client certificate (401), with another site's worker certificate (403), and as the cell's worker once custody no longer names the operation (409). The site store refuses a media window and step below the stored fence. |
| `media-evidence` | Each taken report carries its operation's digests; target A booted media then disk four times; the console carries no token; target B was never touched. Writes `virtual-media.json`. |

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
- source pins, and no OpenStack dependency;
- in synced mode, the IPAM layer's exact inventory, the IPAM bundle and image
  digests, the provider Milo API's aggregation flags, and the IPAM evidence
  matching (a claim's record inside the boot window, an allocation naming it
  back at exactly /32, and each Kea lease belonging to one boot window).

The `ipam-apiserver` layer renders from IPAM's remote bundle, so CI does not
render it. Its inventory in `flux.py` was taken from the v0.6.0 bundle
rendered with the layer's patches. `flux-verify` checks the inventory, the
pinned source and the running image on every live run.

## Execution record

The records below describe runs on the path before the cutover to Device custody,
with the old executor, a site-wide journal and a shared credential over plain
HTTP. They show what the lab and the harness proved then. They are not evidence
for the Redfish workers. The multi-plane Redfish run on custody is implemented,
and its verification is pending.

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
