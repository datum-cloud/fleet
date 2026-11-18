# Local Fleet PXE installation with KVM or explicit TCG

This opt-in development harness uses the existing [lifecycle library](../development/README.md)
and a real disposable Kubernetes API. It does not deploy Fleet or contact a
production cluster. The production manager remains unwired to this provider.

Run `hack/local-vm/run.sh` from a workstation with Go, Docker, kind, kubectl and
Python 3. The default KVM mode requires Linux amd64 Docker and working `/dev/kvm`. First build the
`fleet-vm-local` runtime described in infra PR 9332; the runner image supplies
libvirt, QEMU and `fleet-start-libvirt`. The harness builds its own guest installer
and Go executable. It never invokes the infrastructure smoke script.

For a local runtime without reliable x86 KVM, explicitly select software execution:

```sh
DOCKER_CONTEXT=colima-fleet-acceptance-tcg FLEET_LOCAL_VM_ACCEL=tcg bash hack/local-vm/run.sh
```

`task local-vm:acceptance` runs the same TCG command. For the multi-plane composition
of this provider with Milo and Karmada, use the one-command
[multi-plane acceptance](multiplane.md) harness instead.

The named Docker context must already identify your dedicated disposable runtime.
TCG builds a native amd64 or arm64 Debian libvirt/QEMU runner and boots the same
amd64 installer and blank disk, with no simulator or preinstalled guest image.
The installer build requires amd64 container execution (for example Rosetta on
an arm64 Colima runtime). TCG acceptance qualifies the real installation/control-plane
path, **not KVM acceleration**. There is no automatic fallback: invalid modes fail,
the runner checks `/dev/kvm` in KVM mode, and the provider rejects changing the
accelerator for a retained journal. Legacy journals without a mode mean KVM.
Evidence records the explicit accelerator, runtime architecture, and transferred
executable checksum. The executable is checksum-verified before being renamed
into its runnable path, preventing a truncated transfer from being executed.

The wrapper creates a fresh kind cluster and privileged libvirt container on the
disposable Docker kind network. It mounts no host directories or libvirt sockets.
Installer guests have one e1000 NIC on an isolated libvirt bridge inside the
container. Disk-verification guests have **no NIC**. Libvirt/dnsmasq is the sole
local DHCP/network authority; there is no forwarding, host LAN DHCP, external
bridge, or published HTTP port. NSO, Milo, IAM and quota boundaries remain synthetic. Privilege is restricted operationally to this disposable
Docker environment; this is not a sandbox for untrusted users or guest images.
Failures retain the named container, cluster, private kubeconfigs and executor
journal for recovery. Inspect the printed resource name before removing only
those resources with `docker rm -f NAME` and `kind delete cluster --name NAME`.
Do not share the raw failure directory: it contains local kubeconfig credentials
and private fencing tokens. Successful runs remove credentials and redact tokens.

## Implemented contract

The operator-owned capacity fixture references a distinct Device slot; this is
local virtual inventory, not physical discovery or a new Device API implementation.
The harness sets its actual configured CPU/memory bounds and uses the existing
Coordinator to reserve the slot exclusively for a consumer Machine. A second
consumer must receive InsufficientCapacity. IAM, image admission and quota are
explicit local test authorities; NSO, Milo and production network policy are not
claimed. Importing a record does not invoke the adapter.

The adapter implements `executor.Client`. It accepts one fixed asset, one
artifact-manifest-derived image reference, install and cleanup operations. All
resource names derive from hashes of retained operation IDs. Callers cannot
supply XML, commands, paths, URLs, boot arguments, or cleanup targets. One flock
serializes processes. Every exact request, request digest, authority and random
boot receipt is fsynced before effects. The journal survives process restart.
Changed retries conflict; unfinished operations block later operations. Cleanup
closes the allocation epoch; stale authority cannot reopen it.

A pinned Debian base supplies a Linux kernel, BusyBox and GRUB. Package resolution
currently uses Debian repositories at image-build time, so rebuilding is **not
bit-reproducible**. The build emits SHA256SUMS; the executor verifies both artifacts
and derives the admitted image digest from that exact manifest. Changing either
artifact invalidates the admitted image. Retain the built image ID and artifact
manifest when reproducing an acceptance run.

The guest firmware obtains DHCP configuration and an HTTP iPXE script, then
fetches the kernel and initrd from the guarded broker. Installer domain XML has
only network boot: no libvirt kernel/initrd or disk-boot shortcut. The guest
installer starts with an empty 2 GiB disk, partitions and formats it,
copies its Linux filesystem, installs GRUB, and writes the operation's random
receipt onto disk. A separate libvirt domain boot uses only that disk: no external
kernel, initrd or installer medium. Its one-shot init script checks that `/` is
`/dev/vda1` ext4, emits the retained receipt on serial, and powers off. Fleet
requires the exact serial line and domain disappearance. Installer exit alone,
QEMU running, an old receipt, or missing evidence never yields installed success.
There is no ongoing guest agent. The minimal guest deliberately powers off after
verification; this does not qualify a production distribution or workload.

Reinstall creates a new blank disk and a new receipt under the same allocation.
Prior incarnation disks remain private until cleanup; they are never booted or
exposed to another consumer. Cleanup deletes only retained disks for that exact
binding after verifying no matching domain exists, and verifies absence before
returning the full request-bound clean result. This is disposable-file deletion,
not a claim of physical media sanitization.

The harness crashes the process after verified provider completion, before the
controller receives its result, then starts a new process to recover the same
Installation. It repeats install and reinstall to verify retries, then releases
and allocates the slot to the waiting consumer. Pending effects without sufficient
evidence remain unknown and require inspection; the adapter does not blindly
restart an installer after an ambiguous libvirt start.

## Local PXE admission and trust boundary

The executable now selects `pxe` durably in the journal. Journals from the earlier
direct-kernel prototype cannot silently replay under PXE; there is no automatic
migration or runtime direct-boot option. The accelerator remains separately bound.
Tests retain private direct-mode fixtures for the existing lifecycle regressions.

A random 256-bit bearer capability is retained with the exact admitted operation,
request digest, asset, allocation, epoch, fence and ten-minute expiry before effects.
The HTTP broker reads the atomic journal on every request and permits only the
current `installer-started` operation with an open, unexpired admission. It serves
only its derived iPXE script and the two manifest-bound artifacts. Artifact bytes
and the manifest are rechecked before delivery. Unknown identity, wrong capability,
stale allocation/fence, closed/revoked/expired admission, unsupported paths and
unguarded artifacts are denied. No general file server is exposed.

The DHCP boot URL contains a short operation locator and scoped capability; the
locator is deliberately short enough for the legacy BOOTP filename field. MAC/IP
are routing information, not authentication. Delivery over HTTP and DHCP trusts the
single-guest isolated lab and container operator. A tenant or hostile peer on this
network could steal the capability: this is **not production attestation**, TLS
bootstrap, or qualified multi-tenant PXE. The listener is reachable in the disposable
container network but no host port is published. The capability cannot select an
image, asset, operation, URL or arbitrary file beyond its retained admission.

Expiry/revocation prevents new HTTP retrieval, not execution of a downloaded
installer. The provider first observes domain disappearance and the exact installer
receipt, then closes admission durably. It removes the isolated network before the
separate NIC-free disk boot. Missing observations remain Unknown with capacity held;
no timeout triggers a second installer start. Cleanup still requires every retained
domain absent before deleting disks. Network teardown is local test plumbing, not
NSO/IPAM or production fencing evidence.

Raw runtime state and failed-run directories are private: the journal and network
XML contain capabilities, and kubeconfigs contain Kubernetes credentials. Successful
exports remove these credentials, scan for retained boot capabilities, and retain
only artifact names, byte counts, DHCP leases and exact non-secret receipts.

## Evidence and limitations

Successful output includes DHCP leases, guarded HTTP delivery receipts, live
closed-admission HTTP denial probes, sanitized provider journal, both serial logs and
rendered domain definitions per installation, public Installation status, phase
log, image ID and the final domain inventory. The journal binds receipts to exact
requests including allocation, epoch, fence and image. Readiness assurance is
local trusted-executor evidence, not hardware attestation. The local operator and
container root remain trusted. No remote API or production authorization is added.

See the separately maintained [adversarial validation report](local-vm-adversarial-validation.md).
Actual execution results must be reported separately; a successful Go build or
synthetic simulator test is not evidence of an OS installation.

## Execution record (2026-11-14)

`go test -race ./internal/localvm ./cmd/fleet-local-vm`, shell syntax checks,
`hack/validate-doc-links.sh`, and `git diff --check` passed. The Linux executable
and installer image built. Two disposable runs reached real Kubernetes-backed
exclusive allocation and contention rejection. The first real KVM guest booted,
partitioned and formatted its blank disk, then failed during filesystem copying
because BusyBox selected its built-in tar. The installer now calls GNU tar by
absolute path, checks pipeline failures, enables ACPI, and disables automatic
libvirt restart on guest reboot/crash.

The corrected second run was interrupted by the dedicated Colima VM rebooting
and Docker being gracefully stopped. Subsequent serial-console inspection found
that this reboot followed a nested-KVM host kernel panic, not merely an external
Docker stop. Its journal retained `installer-started`
with an `Unknown` result. The runtime contained only the four containers from
these two runs. One authorized Docker restart restored the internal daemon;
the workstation socket remained unavailable. Restarting the provider daemon
showed no libvirt domains. An attempt to recover the existing installation
exited 139. Subsequent inspection traced exit 139 to an executable truncated at
4,194,304 bytes, compared with the original 37,455,636-byte binary. Restoring that
exact original binary to a separate recovery path, verifying its SHA256, and
retrying the same Installation correctly returned Unknown. No operation was reset
and no disk was reformatted during recovery.
Both runs' resources and private state were retained for inspection.

**The interrupted KVM attempts did not complete real OS disk boot, post-effect
crash recovery, reinstall, or cleanup/reuse.** The subsequent successful explicit
TCG acceptance is recorded below; it does not qualify this KVM environment.
The original local evidence directory is
`/tmp/fleet-local-evidence.ixHu29`; `summary.json`, `runtime-status.txt`,
`journal-sanitized.json`, `device.json`, and `SHA256SUMS` are safe diagnostic
artifacts. Its kubeconfig files and the raw container journals are private and
must not be published. The retained resource names are
`fleet-local-9c2ad625d67a` and `fleet-local-adcaca5a98e8`, each with a corresponding
`-control-plane` kind container. Inspect those exact resources before cleanup.

### Follow-up runtime diagnosis

The dedicated `fleet-e2e-x86` runtime is itself an x86 VM emulated by QEMU TCG on
an Apple Silicon host. A fresh diagnostic run, `fleet-local-9d76bd454637`, reproduced
the host Linux panic at 20:50 CST on November 14, 2026:
`Kernel panic - not syncing: corrupted stack end detected inside scheduler`,
with `Comm: CPU 0/KVM`. The earlier 20:35 CST reboot has the same signature.
The fresh operation also remains `installer-started`/Unknown; its container,
disk, kind cluster and private credentials are retained. No further KVM guest
boots should be attempted in this runtime until the nested virtualization issue
is resolved. The daemon was started only to collect retained diagnostics, without
restarting any provider container or replaying an installer.

Sanitized panic excerpts and sanitized journals are under
`/tmp/fleet-acceptance-recovery` (`nested-kvm-panic.txt`,
`prior-nested-kvm-panic.txt`, `journal-sanitized.json`,
`first-journal-sanitized.json`, and `diagnostic-journal-sanitized.json`). This
directory also contains private recovery files: share only the named sanitized
files, not the directory. A separate task-owned native arm64 Colima VZ runtime,
`fleet-acceptance-tcg`, was created with no host mounts and without switching the
active Docker context, for explicit TCG acceptance using the updated provider.

### Successful explicit TCG acceptance (2026-11-14)

The following command exited zero on November 14, 2026 at approximately 21:03 CST
(November 15, 03:03 UTC), using the parent-updated provider with durable directory
sync, accelerator binding, and lifecycle regression tests:

```sh
DOCKER_CONTEXT=colima-fleet-acceptance-tcg FLEET_LOCAL_VM_ACCEL=tcg bash hack/local-vm/run.sh
```

The runtime has no `/dev/kvm`. Its native arm64 QEMU executed the real amd64 Linux
guest from the built installer, not a simulator or a preinstalled cloud disk.
The run verified Kubernetes-backed exclusive allocation and contention rejection;
partitioning, ext4 formatting, payload copying and GRUB installation onto a blank
2 GiB disk; a separate disk-only boot with the exact installation receipt; exit 75
after the provider's durable success and recovery by a new process; exact retries;
a second real installation with a distinct receipt and blank disk; and verified
cleanup followed by exclusive allocation to the waiting consumer.

The fresh resource `fleet-local-00cf3dc540bb` and its kind cluster were removed
after successful cleanup/reuse. Its credentials were removed from the evidence
directory. The new task-owned Colima runtime remains available, with no containers;
none of the three unknown KVM operations was reset, cleaned, or declared ready.

Sanitized acceptance evidence is in `/tmp/fleet-local-evidence.gz8QaY`:

- `summary.json`, `phases.log`, `accelerator`, and `runtime-architecture` record
  the result and execution mode.
- `provider/journal.json` contains two succeeded installation operations and one
  succeeded cleanup operation, with fences redacted and the allocation closed.
- The operation directories contain both installer and disk serial logs and XML;
  the two disk-only boot receipts match their retained operations and differ from
  each other. Neither disk domain XML contains a kernel, initrd, or network NIC.
- `provider/installations.json` records both public Installation statuses as Ready.
- `domains-after.txt` is empty, and no `disk.raw` remains in the copied provider state.
- `SHA256SUMS`, `binary-SHA256SUMS`, `source-SHA256SUMS`, and `image-id` identify
  the exact tested artifacts. The runner image ID is
  `sha256:5047968ca9404706a83199f3808eaf23f94ca7d2fb165dda1f1cd3dfa97ea24b`.

An independent readback checked these journal, receipt, XML, retry, cleanup and
credential-removal assertions after the harness passed.
`go test -race ./internal/localvm ./cmd/fleet-local-vm`, shell syntax checks, ShellCheck on
`run.sh` and `start-libvirt`, documentation links, and `git diff --check` also passed.
The installer has an informational ShellCheck SC2013 diagnostic for intentionally
splitting `/proc/cmdline` into words. Docker reports an intentional fixed-platform
build warning because the installer must remain amd64 on an arm64 runner.

**This is real OS-install/control-plane acceptance under explicit TCG, not KVM
qualification, production hardware attestation, or a production distribution
qualification.** KVM remains the default and must be qualified separately on a
reliable x86 virtualization host.

### Successful firmware PXE acceptance (2026-11-14 CST / 2026-11-15 UTC)

The same explicit TCG command above exited zero with the PXE implementation.
Sanitized evidence is `/tmp/fleet-local-evidence.A5KTID`; its `summary.json` was
produced by a separate readback of the exported artifacts. The runner image ID is
`sha256:2b2c1109b2e11068c03ef25239adec425108898a4299e06a50bce2414ed6e6c3`.
The native arm64 runtime executed amd64 SeaBIOS/e1000 iPXE firmware and Linux with
TCG. This is legacy BIOS PXE, not UEFI or KVM qualification.

Both fresh blank-disk installations have DHCP leases and guarded requests for
`boot.ipxe`, the 8,234,944-byte kernel and 215,654,737-byte initrd. Readback verified
network-only installer XML with no direct kernel/initrd, real partition/format/
payload-copy/GRUB logs, distinct exact receipts from separate NIC-free disk boots,
closed admissions, two succeeded installs, succeeded cleanup, no remaining disks
or domains, and allocation to the waiting consumer. The harness also exercised
exclusive contention, exit 75 after durable provider completion, new-process
recovery, repeated exact requests, and reinstall. Twelve live HTTP denials after
reinstall/cleanup cover closed predecessor scripts/artifacts, unknown identity,
unguarded paths and unsupported artifacts; the earlier post-install denial probe
also passed. Expired/revoked admission and cross-operation capabilities were tested
through HTTP against durable synthetic journals in `boot_test.go`, not by expiring
a real destructive guest mid-install.

`go test -race ./internal/localvm ./cmd/fleet-local-vm`, shell syntax, ShellCheck on
`run.sh` and `start-libvirt`, documentation links, and whitespace validation passed.
The successful harness checks exported credentials, XML boot paths, DHCP evidence,
artifact receipts and disk deletion. Its resource `fleet-local-7ad959c9bb50` and
corresponding kind cluster were removed. No production changes or KVM starts were
made. Base image and artifact checksums are retained; apt package resolution still
is not reproducible.

Two diagnostic attempts remain in the task-owned TCG runtime, each with its
corresponding `-control-plane` kind container and private credentials/state:

- `fleet-local-d4e060978d40`: the first DHCP URL exceeded the legacy BOOTP filename
  capacity. DHCP succeeded but no HTTP request followed. The guest was explicitly
  destroyed and absence observed; the operation remains `installer-started`/Unknown.
  Private failure directory: `/tmp/fleet-local-evidence.Hs5F4y`.
- `fleet-local-482c67269d19`: after shortening the locator, real PXE install,
  disk-only boot, post-effect recovery and exact retries passed. The harness then
  stopped because a newly added probe was unsupported by its already-built binary.
  Its single installation is Succeeded, no guest domain remains, and its allocated
  disk is retained. Private failure directory: `/tmp/fleet-local-evidence.3W7DIL`.

Only sanitized diagnostic journals and empty domain inventories are exported in
`/tmp/fleet-pxe-diagnostics`. The earlier three unknown KVM resource sets remain
untouched. Neither those nor the failed PXE operation was inferred clean or ready.
The final complete acceptance used a freshly built executable and a fresh resource.
