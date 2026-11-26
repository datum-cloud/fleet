# Boot network: boot addresses and DHCP reservations

A server that network-boots needs a provisioning address while Fleet installs
or wipes it, and none afterwards. Fleet claims that address from IPAM for each
installation and wipe, and carries it in the operation. The boot NIC comes from
Device inventory. At a site whose DHCP Fleet synchronizes, the site publishes a
reservation for that NIC and address only while the operation's network boot
is admitted, and DHCP reports what it applied. Those reports are the evidence
that the boot was reachable and that the reservation was later withdrawn.

DHCP does not run at the site. Each site's Kea instances and their
synchronizers run in a central cluster, reached by the site's relays over the
site's WAN link, and firmware boots over UEFI HTTP boot, never TFTP.

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
IPAM binds it when it is created. The requester copies the claim's name and
address into the operation's `spec.bootNetwork`. The spec holds no UID:

```yaml
spec:
  bootInterface: primary
  bootNetwork:
    addressClaim: {name: alloc-3b9d61c2-install-1-boot}
    address: 198.51.100.10
```

Admission refuses the operation with `BootAddressUnavailable` unless that claim
is the operation's own, by name and labels, exists, is `Bound`, holds that
address, and was created no later than the operation. The requester always
creates the claim first, so a claim deleted and recreated under the name after
the operation is refused even if it holds the same address. It refuses it with
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
Devices a Redfish site serves. It sets the site's DHCP mode, the DHCP
instances a synced site expects and, per target, the BMC endpoint, the Redfish
System, the disk size and the console capture:

```json
{
  "dhcp": "synced",
  "dhcpInstances": ["fleet-site-dhcp-0", "fleet-site-dhcp-1"],
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
- **`dhcpInstances`** is the expected set: every DHCP instance that may serve
  the site's reservations, by the stable name it reports under (the
  StatefulSet's Pod names). It is required, non-empty and without duplicates
  when `dhcp` is `synced`, and must be absent when it is `static`. The feed
  serves only these instances and takes reports only from them, and
  withdrawal is proven only when every one of them reported it. The workers
  and the feed read the same enrollment.

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

Only reports from expected instances count.

1. **Before powering on for a network boot** it waits, with no effect, until
   some expected instance reports a feed that serves the boot NIC at the exact
   address, accepted after this operation's boot admission was created. If the
   boot window expires first, the outcome is unknown.
2. **Before reporting success** it waits, for up to ten minutes, until some
   expected instance served the reservation during this operation, and every
   expected instance's latest report is of a feed issued after the admission
   was stored that serves no MAC or address the site ever published for the
   Device. That covers an earlier, held operation's reservation, so a later
   success settles the hold safely. Past ten minutes the outcome is unknown.

Every feed carries the time the feed server issued it. While the admission is
open, every feed issued after it was stored carries the reservation, so a
report of a later feed without it proves the instance dropped it, and every
later feed lacks it too. An instance that fetched a feed with the reservation
and stopped before reporting still has a latest report that is older than the
admission or serves the reservation, so it keeps the outcome unknown. So does
an expected instance that never reported. Issue times are the feed's clock and
admission times the worker's, so both roles must run on the same clock; they
share the site's state volume.

### Retiring a DHCP instance

Removing an instance from the expected set is the only way to stop waiting for
it. To retire one on purpose:

1. make sure it no longer answers, then scale the StatefulSet down. A
   StatefulSet removes its highest ordinals first, so the instances left are
   the lowest ones;
2. shrink `dhcpInstances` to match the Pods that remain; and
3. restart the site's workers and its feed, so both read the new set.

The feed then refuses the removed instance: a later report from it is
rejected and not retained as a report. Removing a name does not stop its
replica, and one still running behind the load balancer could keep serving
its last reservations. So the feed records each refused contact from a
synchronizer holding the site's token, at most once per instance name every
30 seconds, and the withdrawal proof refuses to pass while any contact was
recorded after the operation's admission was stored. It also waits at least
45 seconds after that, so the rate limit cannot hide a replica that is still
polling. Stopping an instance before removing it is therefore enforced for any
instance whose synchronizer still runs: its operations stay unknown until it
stops, and later operations pass.

**Known limit:** the feed cannot see Pods, so a replica whose synchronizer
stopped while its Kea keeps serving is not detected. Making sure Kea no longer
answers before removing its name still rests on the operator.

These gates do not depend on where Kea runs. The feed comes only from the
site's admitted operations at the Device's stored fence, and every report
names its instance and the exact feed it applied. A WAN outage between the
site and the central cluster makes the instances silent: an operation waiting
to power on does not, and one waiting for withdrawal stays unknown until every
expected instance reports it withdrawn, or the set shrinks.
An instance that cannot reach the feed keeps its last applied configuration,
which its last report shows.

## Feed and reports

The site's `dhcp-feed` role (`fleet-multiplane dhcp-feed -sync-token-file …
-tls-cert … -tls-key …`) listens with TLS on `:8081`, never on the
firmware-facing boot listener. It reads the
site store read-only and writes only `dhcp-applied.log` in the boot-evidence
directory. It reads the expected set from the site's synced enrollment, and
refuses to start without one. Every request needs `Authorization: Bearer
<token>`; the token is at least 32 characters.

- `GET /dhcp/v1/reservations` needs `X-Fleet-DHCP-Instance: <name>` naming an
  expected instance; otherwise it is refused with `403`, nothing is served and
  the contact is recorded (see [Retiring a DHCP instance](#retiring-a-dhcp-instance)).
  It returns the canonical feed:
  `{"revision": "<sha256>", "reservations": [{"mac", "address", "expires"}],
  "issued": "<UTC time>"}`. An address is one host address with no prefix.
  Reservations are sorted by MAC; the revision is the SHA-256 of their
  canonical JSON, so a feed is self-verifying. `issued` is the feed server's
  clock, to the second, read before it read the reservations. It is not part
  of the revision.
- `POST /dhcp/v1/applied` takes `{"instance": "<name>", "feed": <feed>}`, with
  the feed exactly as it was served. The feed must be canonical with a
  matching revision, and the instance must be expected; anything else is
  rejected and not retained as a report. A report from an instance outside
  the set is recorded as a refused contact. An accepted report is synced to
  disk before `204`.

The retained log holds MACs, addresses, revisions, issue times, instance
names and refused contacts: `{"at", "instance", "rejected": true}`, synced
before the `403`, with an empty instance when the request named none. A
refused contact is never evidence that a reservation was served or withdrawn;
it only blocks the proof. A request without the site's token is never
recorded, and a contact that cannot be recorded is still refused. It holds no token, receipt or boot capability. An unreadable line is
treated as corruption, never as evidence. A report retained before feeds
carried an issue time counts as issued at the zero time, so it can never prove
a withdrawal. A record left by the removed retirement command is ignored.

## Kea synchronizer

`fleet-dhcp-sync` (in the manager image) runs beside one Kea DHCPv4 instance,
with a stable instance name such as a StatefulSet pod name, which it sends on
every feed request and report. `-ca-file` names
the CA bundle the feed's certificate must chain to. Kea 2.6 accepts a control
socket only under `/var/run/kea`, which is the synchronizer's default. Each
cycle it:

1. fetches and verifies the feed over HTTPS;
2. reads Kea's PID with `status-get`;
3. when the revision changed or Kea restarted, adds each reservation to the one
   base subnet that contains its address and sends `config-set` over Kea's
   control socket;
4. reports the feed, unchanged, only after Kea accepted it, once per revision.

The base configuration supplies each subnet's prefix, gateway (`routers`) and
boot options. A reservation that no base subnet contains, or that two contain,
fails the cycle; it is never dropped. A restarted Kea loses its reservations
and fails closed until the next cycle re-applies them. To remove an instance,
see [Retiring a DHCP instance](#retiring-a-dhcp-instance).

## DHCP service

Two Kustomize components deploy a synchronized site's DHCP service, to two
places:

| Component | Object | What it runs |
| --- | --- | --- |
| `config/site/dhcp/feed`, beside the site's boot service | `dhcp-feed` container in `fleet-site-boot` | The feed, so it reads the site store read-only and appends reports to the boot evidence on the same volume. It reads the enrollment from the `fleet-site-redfish-enrollment` ConfigMap, which the environment supplies with the same enrollment its workers get |
| | `fleet-site-dhcp-feed` Service | The feed on `:8081`, over TLS only. The environment exposes it to the central cluster |
| `config/site/dhcp/component`, in a central cluster, once per site | `fleet-site-dhcp` StatefulSet | At least two Pods without host networking, with stable names, so a replaced Pod keeps its instance name and its last report. Each runs ISC's Kea DHCPv4 image, pinned by digest, answering relayed requests on a UDP socket, and the synchronizer, sharing Kea's control socket on an `emptyDir` that Kea's command makes `0750`, as Kea 2.6 requires. The Pod name is the instance name |
| | `fleet-site-dhcp` Service | One load-balancer address on UDP 67 in front of every instance, with local traffic policy and client-IP affinity. The site's relays forward to it |
| | `fleet-site-dhcp` PodDisruptionBudget | One instance down at a time |

Any instance may answer a request. Each applies the same feed through its own
synchronizer and reports under its own name, so the power-on gate needs one
expected instance serving and the withdrawal gate needs every expected
instance. An instance that has not applied a reservation yet stays silent,
because Kea is not authoritative, and the firmware retries; client-IP affinity
keeps one relay's exchange on one instance.

Kea answers each relayed request at the relay address the request carries, on
UDP 67. Its reply leaves through the cluster's egress, which is not always the
load-balancer address the relay forwarded to, so the cluster must route to
every relay address and each relay must accept replies from the cluster's
egress address. Every boot at the site then depends on the central cluster and
the site's WAN link.

The environment supplies, never in Git:

- the `fleet-site-dhcp-sync` Secret in both places, with the synchronizer
  token under `token`;
- the `fleet-site-dhcp-feed-tls` Secret, with `tls.crt` and `tls.key` for the
  feed's name beside the boot service, and the issuing `ca.crt` in both places;
- the `fleet-site-dhcp-base` ConfigMap from network operations, with
  `kea-dhcp4.conf`; and
- the `FLEET_SITE_IMAGE`, `FLEET_DHCP_SYNC_IMAGE` and `FLEET_DHCP_FEED_URL`
  substitutions.

The base configuration names the site's provisioning subnet and gateway, the
relay addresses Kea accepts, and the boot options. Kea answers only relayed
requests, so the subnet's relay filter is what limits it to the site's relays.
It has no pools, so Kea answers only reserved MACs, and its control socket is
the synchronizer's. It sets no TFTP server: firmware in the HTTP boot vendor
class gets the boot service's loader URL.

Both Secrets are required: without them the boot Pod, and so the boot service,
does not start. The feed loads its certificate and the synchronizer its CA
once, so rotating either Secret takes a restart of the boot Pod or the Kea
Pods.
`config/site/dhcp/example` renders the component with a synthetic base
configuration, and `hack/validate-site-dhcp-render.sh` checks that render in
CI. ISC builds the Kea image for `linux/amd64` only.

## Network operations requirements

Fleet configures no switch, router or relay. A synchronized site needs:

1. A provisioning VLAN, untagged on each server's boot NIC port.
2. A private VLAN or equivalent on the provisioning VLAN only, so a server
   reaches only the gateway there. Customer VLANs on the same port keep normal
   forwarding.
3. Spanning tree edge mode on server ports.
4. LACP fallback to an individual port where the boot NIC is a bond member,
   with a fallback timeout below the DHCP timeout of every qualified firmware.
5. On the provisioning subnet's gateway, a DHCP relay to the instances'
   load-balancer address in the central cluster, with the gateway as the relay
   address, accepting replies from the cluster's egress address.
6. Routes and filters over the site's WAN link so the provisioning subnet
   reaches only the central DHCP and boot service addresses, and no customer
   network, BMC network or other internet address, and so the central cluster
   reaches every relay address.
7. DHCP snooping and source address binding on the provisioning VLAN.
8. LLDP on server ports.
9. Switch and port names in Fleet's cable records that match the network
   owner's.
10. The base configuration above.
11. Firmware that boots over UEFI HTTP boot, with its DHCP timeout measured
    against the round trip over the WAN link during qualification.

## Boot network lab

`task boot-network:kea-lab` (`hack/boot-network/kea-lab.sh`) runs ISC's Kea
image, the real synchronizer, and the site's feed listener over TLS on one
container bridge, in the `colima` Docker context by default. Kea runs with the
component's capabilities and socket directory. A lab program
opens and closes boot admissions in a real site store, and a `udhcpc` client
with a chosen MAC asks for an address. It passes only if:

1. a boot NIC with no open admission gets no answer;
2. the reserved boot NIC gets exactly its boot address while its admission is
   open, and another NIC gets none; and
3. after the admission closes and Kea reports the withdrawal, the boot NIC gets
   no answer.

The lab proves the feed, the synchronizer and reservation-gated answers in a
real DHCP exchange. With no relay, its Kea answers the client directly on a raw
socket, where the central Kea answers relays on a UDP socket. It has no VLAN, no
switch and no firmware, so it proves none of the network operations
requirements.

## Implemented versus proposed

| Behavior | Status |
| --- | --- |
| Boot addresses claimed from IPAM per operation, checked at admission, and released only after the operation ended unheld | Implemented; tested against a real API server with a stand-in for IPAM's claim API. No run sets a class, and IPAM itself is not exercised |
| Boot NIC from Device inventory through the admission snapshot; enrollment without MACs or addresses | Implemented; unit tested |
| Reservation feed, authenticated applied reports, served and withdrawn gates over every reservation of the Device | Implemented; unit tested with a fake BMC and synchronizer |
| Expected DHCP instance set in the enrollment: the feed refuses other instances, withdrawal needs every expected instance's report of a feed issued after the admission, and retirement only by shrinking the set | Implemented; unit tested, including an instance that never reported and one that stopped after fetching the reservation. Restarting the workers and the feed after a change is an operator step |
| Refused contacts recorded and blocking withdrawal, so a removed instance whose synchronizer still polls keeps its operations unknown | Implemented; unit tested, including the rate limit, an unauthenticated request and a log that cannot be written. A removed replica whose synchronizer stopped while Kea serves is not detected |
| Kea synchronizer: containing-subnet placement, restart re-apply, report only after Kea accepted | Implemented; unit tested against a fake Kea control socket |
| Feed over TLS beside the boot service; Kea and the synchronizer as a central component, several instances behind one UDP 67 load balancer | Implemented; render checked in CI, and reservation-gated answers proved by the boot network lab with real Kea. Not deployed, and relayed requests across a WAN are not exercised |
| Boot service and boot files in the central cluster, serving a UEFI HTTP boot loader | Proposed; the boot service runs in the site's boot Pod beside the site store, and serves the iPXE script but no EFI loader yet |
| Relay, provisioning VLAN, isolation and LACP fallback at a site | Network operations; see the [server networking enhancement][server-networking] |
| Telling firmware what to boot in synced mode (Redfish `HttpBootUri` for UEFI HTTP boot, or the DHCP boot-file option above) | Proposed; the Redfish worker still arms `Pxe`, and the base configuration's HTTP boot class names a loader the boot service does not serve yet |
| DHCPv6 reservations | Proposed; the feed accepts IPv6, the Kea DHCPv4 synchronizer and the claim reject it |
| Lab acceptance in synced mode | Implemented as `run.sh --redfish --synced-dhcp`, with a synthetic IPAM stand-in; not yet run. See the [Redfish lab](redfish-lab.md#synchronized-dhcp-mode) |

[server-networking]: https://github.com/datum-cloud/fleet/pull/186
