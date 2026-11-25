# Boot network: boot addresses and DHCP reservations

A server that network-boots needs a provisioning address while Fleet installs
or wipes it, and none afterwards. Fleet claims that address from IPAM for each
installation and wipe, and carries it in the operation. The boot NIC comes from
Device inventory. At a site whose DHCP Fleet synchronizes, the site publishes a
reservation for that NIC and address only while the operation's network boot
is admitted, and DHCP reports what it applied. Those reports are the evidence
that the boot was reachable and that the reservation was later withdrawn.

This page describes the implemented contract. The design is the [server
networking enhancement][server-networking], which replaces the enrollment values
of the [boot network enhancement](../enhancements/boot-network/README.md). What
is still proposed is listed under [Implemented versus
proposed](#implemented-versus-proposed).

## Two kinds of address

| | Address | Belongs to | Lifetime | Owner |
| --- | --- | --- | --- | --- |
| Physical | BMC endpoint | Physical asset | The hardware's life | Networking/IPAM (out-of-band) |
| | Boot address | One installation or wipe | Until no DHCP instance can still serve it | IPAM assigns; Fleet claims and publishes |
| | Underlay (loopback, routing peers) | Physical host on the fabric | The host's life on the site | Galactic and IPAM |
| Consumer | Machine interfaces (`Machine.spec.networkInterfaces`) | Allocation | Allocation to release | Networking/IPAM, through the Machine |

A boot address must not be a consumer address. A site whose firmware boots on
the same untagged network and address as the running OS cannot withdraw one
without the other, and needs a dedicated provisioning network on the boot NIC.

## Boot address claims

When the allocator is configured with a provisioning IPAM class, each requester
claims a boot address before it creates its operation. The multi-plane
composition takes the class from its `BootAddressClass` setting; the lab leaves
it empty because its DHCP is static. Each requester is:

- the Metal provider, for a `DeviceInstallation`; and
- the release controller, for a `DeviceWipe`.

The claim is an IPAM `IPClaim` in the provider project named
`<operation>-boot`, labeled with the allocation, the operation and the
operation kind, and scoped to the site's location. It asks for one IPv4 host
address and `reclaimPolicy: Delete`.
IPAM binds it when it is created. The requester copies the claim's name, UID
and address into the operation's `spec.bootNetwork`:

```yaml
spec:
  bootInterface: primary
  bootNetwork:
    addressClaim: {name: alloc-3b9d61c2-install-1-boot, uid: 1d2f7a9e-0b3c-4e5f-8a6b-7c8d9e0f1a2b}
    address: 198.51.100.10
```

Admission refuses the operation with `BootAddressUnavailable` unless that claim
is the operation's own, by name and labels, and that incarnation exists, is
`Bound`, and holds that address. It refuses it with
`BootInterfaceUnknown` unless the Device records the boot interface with a MAC.
The admission snapshot records that MAC.

Each requester deletes only its own kind's claims, during release:

- a claim whose operation ended and that custody's recovery hold does not
  name, either as the held operation or as the original of a held retry; and
- every remaining claim once the allocation is released.

The allocator reads custody after it reads the operation, because the
coordinator writes a hold before it copies `OutcomeUnknown` to the operation.

A claim whose operation does not exist yet stays, so a requester that crashed
between the claim and the operation still finds it. The roles grant each
requester `create`, `get`, `list` and `delete` on `ipclaims`, and the
coordinator `get`.

## Enrollment

The site enrollment (`FLEET_REDFISH_ENROLLMENT`) is the explicit mapping of the
Devices a Redfish site serves. It sets the site's DHCP mode and, per target, the
BMC endpoint, the Redfish System, the disk size and the console capture:

```json
{
  "dhcp": "synced",
  "targets": [{
    "device": "default/server-example-001",
    "endpoint": "https://192.0.2.20:443",
    "systemID": "Self",
    "systemUUID": "00000000-0000-4000-8000-000000000001",
    "diskBytes": 960197124096,
    "console": "example-01.log"
  }]
}
```

- **`device`** is the Device's `<namespace>/<name>` in the provider project.
- **`endpoint`** is per target. Physical BMCs each expose one System, often with
  the same Id (`Self`), so a System is identified by its endpoint plus Id, and by
  its UUID.
- **`dhcp: static`** means the site's DHCP is configured independently of Fleet.
  The site refuses an operation with a boot address
  (`BootNetworkUnsupported`). The Redfish lab uses this mode: its dnsmasq hands
  out fixed leases. The simulator and local VM sites refuse a boot address too.
- **`dhcp: synced`** means Fleet publishes reservations. The site refuses a
  network boot without a boot address (`BootNetworkRequired`).

The enrollment names no MAC and no address. Before it sends anything, the site
worker checks the BMC endpoint and system UUID against the admitted snapshot.

## Reservation lifecycle

At a synchronized site, the site serves a reservation feed derived only from
the site store. A boot admission publishes the snapshot's boot MAC at the
operation's boot address while it:

- is at the Device's stored fence, so a superseded operation holds none;
- has a window that has not expired; and
- has not been closed.

The worker journals a `boot-reservation` step before it stores the admission,
so an operation that published a reservation can end `Succeeded` or
`OutcomeUnknown`, never `Failed`. That keeps its address claimed until
withdrawal is proven.

The worker gates its effects on reports:

1. **Before powering on for a network boot** it waits, with no effect, until
   some DHCP instance reports a feed that serves the boot NIC at the exact
   address, accepted after this operation's boot admission was created. If the
   boot window expires first, the outcome is unknown.
2. **Before reporting success** it waits, for up to ten minutes, until some
   instance served the reservation during this operation and no instance's
   latest report, however old, serves any MAC or address the site ever
   published for the Device. That covers an earlier, held operation's
   reservation, so a later success settles the hold safely.

An instance that went silent while serving keeps the outcome unknown. After
making sure its server no longer answers, an operator at the site retires it:

```sh
FLEET_BOOT_EVIDENCE=/var/lib/fleet-boot fleet-multiplane dhcp-retire -instance dhcp-a
```

A later report from a retired instance counts again. Report times are the boot
service's clock and admission times the worker's, so both roles must run on
synchronized clocks.

## Feed and reports

The site's `dhcp-feed` role (`fleet-multiplane dhcp-feed -sync-token-file …`)
listens on `:8081`, never on the firmware-facing boot listener. It reads the
site store read-only and writes only `dhcp-applied.log` in the boot-evidence
directory. Every request needs `Authorization: Bearer <token>`; the token is at
least 32 characters.

- `GET /dhcp/v1/reservations` returns the canonical feed:
  `{"revision": "<sha256>", "reservations": [{"mac", "address", "expires"}]}`.
  An address is one host address with no prefix. Reservations are sorted by MAC;
  the revision is the SHA-256 of their canonical JSON, so a feed is
  self-verifying.
- `POST /dhcp/v1/applied` takes `{"instance": "<name>", "feed": <feed>}`. The
  feed must be canonical with a matching revision; anything else is rejected and
  not retained. An accepted report is synced to disk before `204`.

The retained log holds MACs, addresses, revisions, instance names and
retirements. It holds no token, receipt or boot capability. An unreadable line
is treated as corruption, never as evidence.

## Kea synchronizer

`fleet-dhcp-sync` (in the manager image) runs beside one Kea DHCPv4 instance,
with a stable instance name such as a StatefulSet pod name. Each cycle it:

1. fetches and verifies the feed over HTTPS;
2. reads Kea's PID with `status-get`;
3. when the revision changed or Kea restarted, adds each reservation to the one
   base subnet that contains its address and sends `config-set` over Kea's
   control socket;
4. reports the feed only after Kea accepted it, once per revision.

The base configuration supplies each subnet's prefix, gateway (`routers`) and
boot options. A reservation that no base subnet contains, or that two contain,
fails the cycle; it is never dropped. A restarted Kea loses its reservations
and fails closed until the next cycle re-applies them. Before scaling down,
drain the instance being removed until it reports an empty feed.

## Implemented versus proposed

| Behavior | Status |
| --- | --- |
| Boot addresses claimed from IPAM per operation, checked at admission, and released only after the operation ended unheld | Implemented; tested against a real API server with a stand-in for IPAM's claim API. No run sets a class, and IPAM itself is not exercised |
| Boot NIC from Device inventory through the admission snapshot; enrollment without MACs or addresses | Implemented; unit tested |
| Reservation feed, authenticated applied reports, served and withdrawn gates over every reservation of the Device, instance retirement | Implemented; unit tested with a fake BMC and synchronizer, including a silent instance |
| Kea synchronizer: containing-subnet placement, restart re-apply, report only after Kea accepted | Implemented; unit tested against a fake Kea control socket |
| Deployment of the feed, TLS to the feed, Kea and the synchronizer as a site component | Proposed |
| Relay, provisioning VLAN, isolation and LACP fallback at a site | Network operations; see the [server networking enhancement][server-networking] |
| Telling firmware what to boot in synced mode (Redfish `HttpBootUri` for UEFI HTTP Boot, or a DHCP boot-file option) | Proposed; the Redfish worker still arms `Pxe`, which relies on site DHCP for the boot file |
| DHCPv6 reservations | Proposed; the feed accepts IPv6, the Kea DHCPv4 synchronizer and the claim reject it |
| Lab acceptance in synced mode | Not run; the lab uses static DHCP |

[server-networking]: https://github.com/datum-cloud/fleet/pull/186
