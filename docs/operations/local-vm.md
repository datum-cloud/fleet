# Local VM site

[Operations guide](README.md) · [Multi-plane acceptance](multiplane.md)

The local VM site installs a real operating system into a disposable libvirt
domain over firmware PXE. It is one of the sites that run
[Device custody](../development/device-custody.md#cutover-increment-c) work. It
is a disposable acceptance executor, not production hardware control, and it is
not KVM or hardware qualification.

Status: **implemented**. The standalone smoke does not use it. The
[multi-plane acceptance](multiplane.md) (`task multiplane:acceptance`) runs it,
and verification of that run after the cutover is pending.

## The single-plane harness is retired

Before the cutover, `hack/local-vm/run.sh` (also `task local-vm:acceptance`)
ran the local VM executor against a disposable kind cluster on its own. The
cutover deleted that harness, its `cmd/fleet-local-vm` command, and the old
executor and its journal. The multi-plane local VM mode runs the same workers,
so no separate harness remains.

This is a decision for a person. If a single-plane run is still needed, it must
be rebuilt on the site framework.

The earlier execution records, which describe the retired executor, are not
kept. The guest installer and the site image build are still in `hack/local-vm`.

## What the site does

The workers are in `internal/site/vmwork`. They run on the
[site framework](../development/device-custody.md#site-execution-increment-b),
so each Device has its own fence and journal, and a newer fence stops an older
worker. The site reaches a Device through enrollment, with no BMC. The
enrollment is checked against the admission snapshot before anything is sent.

| Operation | What the worker does |
| --- | --- |
| `DeviceInstallation` | Creates an exclusive blank disk for the installation. Boots the installer once and waits for exactly one console receipt. In PXE mode it also requires completed boot deliveries and a DHCP lease. Then boots from the disk alone and waits for the disk-boot receipt. |
| `DeviceWipe` | Deletes every installation disk of the Device. It refuses while any local domain exists, because it could not prove the disks are unused. |

A boot is started once. After a crash or a lost response, the worker observes
the domain and does not start it again. If it cannot show what happened, the
outcome is unknown and custody stays held. A reinstall creates a new blank disk
and a new receipt for the same allocation. A wipe deletes the disks of every
installation of the Device, not only the latest.

## Installer and artifacts

A pinned Debian base supplies a Linux kernel, BusyBox and GRUB. Package
resolution uses Debian repositories at image-build time, so rebuilding is **not
bit-reproducible**. The build emits `SHA256SUMS`. The site derives the admitted
image identity from that manifest, and the boot server rechecks the artifact
bytes and the manifest before each delivery. Changing either artifact
invalidates the admitted image. `hack/local-vm/Containerfile` builds the site
image from the `site` target, with the checksum-verified `fleet-multiplane`
binary.

The guest firmware obtains DHCP configuration and an HTTP iPXE script, then
fetches the kernel and initrd from the boot server. The installer domain has only
network boot, with no libvirt kernel, initrd or disk-boot shortcut in PXE mode.
The installer partitions and formats an empty 2 GiB disk, copies its Linux
filesystem, installs GRUB, and writes the operation's random receipt onto the
disk. A separate boot uses only that disk: no external kernel, initrd or
installer medium. Its one-shot init script checks that `/` is the disk's first partition, ext4,
prints the receipt on the serial console, and powers off. Installer exit,
QEMU running, an old receipt, or missing evidence never counts as installed.
There is no ongoing guest agent. The installer also accepts `fleet.mode=wipe`,
which zeroes and verifies the disk. Only the [Redfish mode](redfish-lab.md)
boots it.

## Boot admission and trust boundary

A random 256-bit capability is stored with the operation's admission, at the
operation's fence, with a ten-minute expiry, before any boot. The boot server
reads the site store on every request. It serves a boot only while the
admission is open and unexpired, the token matches, and the fence equals the
Device's stored fence. A superseded operation can never boot the server. It
serves only its derived iPXE script and the two manifest-bound artifacts, and
no general files. Each delivery is logged as an attempt, and as `write-complete`
only when the response writer wrote the whole body without error.

The DHCP boot URL carries a short operation locator and the capability. The
locator is short enough for the legacy BOOTP filename field. The MAC address and
IP address are routing information, not authentication. Delivery over HTTP and
DHCP trusts the single-guest isolated lab and its container operator. A hostile
peer on the network could take the capability. This is **not production
attestation**, TLS bootstrap, or qualified multi-tenant PXE.

Expiry or revocation stops new retrievals. It does not stop an installer that has
already downloaded. The worker closes the admission only after it observes the
domain gone and the exact receipt. It then removes the isolated network before the
disk boot. Missing observations stay unknown, and no timeout starts a second
installer.

## Limits

- Guests use one e1000 NIC on an isolated libvirt bridge inside the runtime
  container. Disk-boot guests have no NIC. Libvirt and dnsmasq are the only DHCP
  and network authority.
- The accelerator is KVM or an explicit TCG. The multi-plane run uses TCG, with
  an amd64 guest on a native arm64 runtime. TCG qualifies the installation and
  control-plane path, not KVM.
- Boot is legacy BIOS PXE, not UEFI.
- Privilege is limited to the disposable container environment. It is not a
  sandbox for untrusted users or guest images.
- NSO, Milo IAM and quota are synthetic. The guest has no managed network.

Validation of earlier prototypes is described in the [adversarial validation
notes](local-vm-adversarial-validation.md).
