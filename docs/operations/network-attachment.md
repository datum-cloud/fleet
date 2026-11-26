# Network attachment: Machine interfaces on customer networks

A Machine names its interfaces in `spec.networkInterfaces`, each a
MachineProfile slot on a customer Network. Fleet asks NSO for one interface per
entry and names where the server's NIC meets the network. The network owner
connects the switch port. Fleet reports the result on the Machine and never
configures a switch.

This page describes the implemented contract. The design is the [server
networking enhancement][server-networking]. What is still proposed is listed
under [Implemented versus proposed](#implemented-versus-proposed).

## Before allocation

The Metal provider checks each interface before it reserves capacity:

- the name is unique;
- the Network is in the Machine's namespace, exists, is `Ready` at its
  generation, and carries each requested address family; and
- the principal the Machine was admitted for may use the Network.

A failed check leaves the Machine `Pending`, with `Bound` and `Ready` false for
one of these reasons:

| Reason | Meaning |
| --- | --- |
| `NetworkAttachmentUnsupported` | No network owner is configured, or NSO has no physical attachment mode |
| `NetworkUseNotAuthorized` | The principal may not use a Network, or no network-use check is configured |
| `NetworkNotReady` | A Network is missing, not ready, or does not carry a requested family |

## Attachment

After allocation, the Metal provider records each interface on the
MachineAllocation's `status.network` before it creates anything:

- the interface name, which is the profile slot and the Device inventory
  interface;
- the Network;
- the attachment point: the interface's MAC from Device inventory, and the
  switch and port at the other end of the one cable record that names the
  interface as the Device's component; and
- the claim's namespace and name, `fleet-` and 40 hex characters of the
  SHA-256 of `<allocation>/<interface>`.

It then creates a `NetworkInterfaceClaim` in the Network's namespace, labeled
`fleet.datumapis.com/allocation` and `fleet.datumapis.com/machine-uid`, with
the configured physical attachment mode, and records the claim's UID. A crash
between the create and the UID write is recovered by name. Fleet adopts a claim
only if its labels and spec match exactly, never recreates a claim whose UID it
recorded, and records every proven UID even when a later interface fails.

The Network's namespace is the consumer's, so a consumer who learns the
allocation name could create a matching claim first, and Fleet would adopt it.
That claim asks for nothing Fleet would not, and the realizer checks the
attachment point against the allocation's record. Claims move to a namespace
the consumer cannot write once NSO defines one.

A claim's status wakes its Machine through the `machine-uid` label. The Machine
reports, per interface, the Network, the MAC, the MTU of the bound
`NetworkInterface`, its addresses, and the claim's `Allocated`, `Prepared` and
`Programmed` conditions. `NetworkReady` is `True` only when every interface
reports all three at the claim's generation. Until then the Machine is
`Pending` with reason `NetworkPending`. A recorded claim that changed or
disappeared reports `NetworkConfigurationChanged`, and an interface with no
single cable record reports `AttachmentPointUnknown`. When NSO cannot be read,
`NetworkReady` is `Unknown`, `Ready` is false, and the last network status
stays.

## Release

Release deletes every recorded claim with its UID as a precondition. The
`Network` and `Address` gates close only when every claim is gone, because NSO
keeps a claim until the realizer detached the port. A claim under the same name
with another UID is not Fleet's, so Fleet's own counts as gone and the other is
left alone.

An interface that asked to retain its addresses keeps them in the consumer's
project after release, as NSO's `Retain` policy defines. Fleet's obligation
ends when its claim is gone. If the NSO API stops serving claims after Fleet
recorded some, release waits with both gates open until an operator restores
it.

## Configuration

The allocator's network owner is an NSO adapter with:

- a client for the NSO API that holds the consumer's Networks and the claims;
- the physical attachment mode; and
- a network-use authorizer that checks the admitted principal.

Without all three, every Machine that requests interfaces is refused. The
synthetic staging provider uses the allocator's network owner when one is set;
no mode in this repository sets one.

## Implemented versus proposed

| Behavior | Status |
| --- | --- |
| Checks before allocation, attachment points from inventory, claims named and recorded on the allocation, Machine network status, release gated on claim absence | Implemented; tested against a real API server with NSO's CRDs, a stand-in `Physical` mode and a test realizer |
| NSO `Physical` attachment mode and attachment point fields | Proposed to NSO; the pinned NSO refuses `Physical`, and a test records that |
| Where a provider that is not compute creates claims, in a namespace the consumer cannot write | Proposed to NSO; Fleet creates claims in the Network's namespace |
| Network-use authorization of the admitted principal through Milo | Not implemented; no authorizer is configured, so interfaces are refused |
| The physical realizer that programs switch ports | Proposed to Networking |
| Host network configuration in installation inputs | Proposed |
| Fleet's IAM grants on interface claims in consumer projects | Not implemented |

[server-networking]: https://github.com/datum-cloud/fleet/pull/186
