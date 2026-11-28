# Boot network: physical addresses and DHCP reservations

A physical host has addresses that belong to the asset, separate from the
addresses a consumer's Machine receives. Fleet records the physical ones in the
site enrollment and, for network boot, publishes a DHCP reservation only while an
operation's boot is admitted at the Device's stored fence. The network owner's DHCP applies it and reports
back; those reports are the evidence that the boot was reachable and that
provisioning networking was later revoked.

This page describes the implemented contract. The design and its alternatives
are in the [boot network enhancement](../enhancements/boot-network/README.md).
What is still proposed is listed under
[Implemented versus proposed](#implemented-versus-proposed).

## Two kinds of address

| | Address | Belongs to | Lifetime | Owner |
| --- | --- | --- | --- | --- |
| Physical | BMC endpoint | Physical asset | The hardware's life | Networking/IPAM (out-of-band) |
| | Provisioning attachment (address and gateway) | Physical asset, during an admitted network boot | The boot window | Networking/IPAM allocates; Fleet publishes |
| | Underlay (loopback, routing peers) | Physical host on the fabric | The host's life on the site | Galactic and IPAM |
| Consumer | Machine interfaces (`Machine.spec.networkInterfaces`) | Allocation | Allocation to release | Networking/IPAM, through the Machine |

A provisioning address must not be a consumer address. Release fences
provisioning networking, so Fleet must be able to withdraw the provisioning
address without touching anything a tenant uses. A site whose firmware boots on
the same untagged network and address as the running OS cannot meet that, and
needs a dedicated provisioning network on the install NIC.

## Enrollment

The site enrollment (`FLEET_REDFISH_ENROLLMENT`) is the explicit mapping of the
Devices a Redfish site serves. It sets a site DHCP mode and, per target, the BMC
endpoint, the Redfish System, the boot NIC and, in synchronized mode, the
provisioning attachment:

```json
{
  "dhcp": "synced",
  "targets": [{
    "device": "default/server-example-001",
    "endpoint": "https://192.0.2.20:443",
    "systemID": "Self",
    "systemUUID": "00000000-0000-4000-8000-000000000001",
    "mac": "00:00:5e:00:53:01",
    "provisioning": {"address": "198.51.100.10/24", "gateway": "198.51.100.1"},
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
  Targets must not carry a provisioning attachment. The Redfish lab uses this
  mode: its dnsmasq hands out fixed leases.
- **`dhcp: synced`** requires a provisioning attachment on every target: a host
  address in prefix form and a different host in the same prefix as gateway.
  Provisioning addresses are unique within the enrollment.

Before it sends anything, the site worker checks the enrollment against the
operation's admitted snapshot: the BMC endpoint, the system UUID and the boot
MAC. A mismatch ends the operation without an effect.

## Reservation lifecycle

In synchronized mode the site serves a reservation feed derived only from the
site store. A target's boot NIC holds its provisioning address while its
operation's boot admission:

- is at the Device's stored fence, so a superseded operation holds none;
- has a window that has not expired; and
- has not been closed.

That window opens at admission, before power-on, so DHCP is ready when firmware
asks. The boot handler's narrower rule still decides which scripts and artifacts
are served.

The worker gates its effects on reports:

1. **Before arming a network boot** it waits, with no effect, until some DHCP
   instance reports a feed that serves the boot NIC at its exact provisioning
   address, accepted after this operation's boot admission was created. A report
   from an earlier operation is not evidence. If the boot window expires first,
   the worker stops before it arms the boot, and nothing is sent.
2. **Before reporting success** it waits until every DHCP instance that served
   the reservation during this operation reports a later feed without it, for up to ten
   minutes. A silent instance may still be serving, so the worker does not
   report success. Completion therefore includes verified withdrawal.

Report times are the boot service's clock and admission times the worker's, so
both roles must run on synchronized clocks.

A reservation carries an address and gateway only. It does not tell firmware
what to boot; see the boot-program row below.

## Feed and reports

The site's `dhcp-feed` role (`fleet-multiplane dhcp-feed -sync-token-file …`)
listens on `:8081`, never on the firmware-facing boot listener. It reads the
site store read-only and writes only `dhcp-applied.log` in the boot-evidence
directory. Every request needs `Authorization: Bearer <token>`; the token is at
least 32 characters.

- `GET /dhcp/v1/reservations` returns the canonical feed:
  `{"revision": "<sha256>", "reservations": [{"mac", "address", "gateway", "expires"}]}`.
  Reservations are sorted by MAC; the revision is the SHA-256 of their canonical
  JSON, so a feed is self-verifying.
- `POST /dhcp/v1/applied` takes `{"instance": "<name>", "feed": <feed>}`. The
  feed must be canonical with a matching revision; anything else is rejected and
  not retained. An accepted report is synced to disk before `204`.

The retained log holds MACs, addresses, revisions and instance names. It holds
no token, receipt or boot capability. An unreadable line is treated as
corruption, never as evidence.

## Kea synchronizer

`fleet-dhcp-sync` (in the manager image) runs beside one Kea DHCPv4 instance,
with a stable instance name such as a StatefulSet pod name. Each cycle it:

1. fetches and verifies the feed over HTTPS;
2. reads Kea's PID with `status-get`;
3. when the revision changed or Kea restarted, adds each reservation to the base
   configuration's subnet with exactly its prefix, sets the gateway as the
   host's `routers` option, and sends `config-set` over Kea's control socket;
4. reports the feed only after Kea accepted it, once per revision.

A reservation with no matching base subnet fails the cycle; it is never
dropped. A restarted Kea loses its reservations and fails closed until the next
cycle re-applies them. An instance that disappears after serving never reports
withdrawal, which keeps that operation Unknown until an operator resolves it,
so instance names must survive restarts. Before scaling down, drain the
instance being removed until it reports an empty feed.

## Implemented versus proposed

| Behavior | Status |
| --- | --- |
| Per-target BMC endpoints, endpoint-scoped System identity, static and synced DHCP modes, validated provisioning attachments | Implemented; unit tested |
| Reservation feed, authenticated applied reports, served and withdrawn gates, multi-instance withdrawal | Implemented; unit tested with a fake BMC and synchronizer, including a silent instance |
| Kea synchronizer: exact-subnet placement, restart re-apply, report only after Kea accepted | Implemented; unit tested against a fake Kea control socket |
| Provisioning attachments allocated by Networking/IPAM instead of enrollment values | Proposed |
| NIC MACs and BMC endpoints recorded as Fleet inventory observations | Proposed; the enrollment is the only record |
| Deployment of `dhcp-feed` and the synchronizer, TLS to the feed, relay and provisioning VLAN at a site | Proposed; not exercised end to end |
| Telling firmware what to boot in synced mode (Redfish `HttpBootUri` for UEFI HTTP Boot, or a DHCP boot-file option) | Proposed; the Redfish worker still arms `Pxe`, which relies on site DHCP for the boot file |
| DHCPv6 reservations | Proposed; the feed accepts IPv6, the Kea DHCPv4 synchronizer rejects it |
| Lab acceptance in synced mode | Not run; the lab uses static DHCP |
