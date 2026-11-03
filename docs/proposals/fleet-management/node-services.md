---
status: provisional
stage: alpha
latest-milestone: "v0.1"
---

<!-- omit from toc -->
# Node services

Part of the [Fleet infrastructure management service](README.md). A node
service is a platform service that runs once per managed node slot, such as
the Private DNS resolvers, and follows that node through drain, reboot,
reinstall, and hardware replacement. Fleet places it, rolls it, and reports
it as part of node readiness. Consumers such as Compute inherit it on every
node they receive; their customers never see it.

Node services build on [cluster orchestration](cluster-lifecycle.md): they
select NodeClaims and report through them. Standalone machines from the
[machine allocation API](bare-metal-hardware.md) are never selected. Resource
shapes and field names below are proposed contracts, not implemented schemas.

- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
  - [Node service resource](#node-service-resource)
  - [Slots and identity](#slots-and-identity)
  - [Readiness](#readiness)
  - [Rollout and disruption](#rollout-and-disruption)
  - [Lifecycle](#lifecycle)
  - [Fencing](#fencing)
  - [Ownership and authority](#ownership-and-authority)
  - [Standalone machines](#standalone-machines)
- [Implementation](#implementation)
- [Rollout plan](#rollout-plan)
- [Acceptance gates](#acceptance-gates)
- [Open questions](#open-questions)

## Summary

The owning service declares a `NodeService` that selects managed nodes by
Fleet role. Fleet runs one instance per selected NodeClaim and declared slot,
gives each slot a stable network attachment, and reports per-slot readiness in
`NodeClaim.status.nodeServices`. The `fleet.datumapis.com/node-services-ready`
condition lets a consumer, such as Compute, hold tenant placement until the
node's platform services are serving.

Node services live at the cluster and node layer, below consumers and above
machines. A consumer does not deploy per-host platform agents, and a customer
API does not grow a per-host placement mode to carry them.

## Motivation

Private DNS shows the problem. Its node resolvers run today as hand-wired
Deployments per host, with:

- host pins in `nodeSelector`;
- `Recreate` strategy;
- one Deployment per resolver slot;
- a dedicated runtime class installed through a Talos machine patch;
- an annotation that asks for the producer network interface.

A new compute host needs each of these edited by hand. A replacement host
needs them again under a new name. None of them gates the host's readiness,
so Compute can schedule tenants before the host can resolve private names.

The resolver's lifecycle is the node's lifecycle. It must exist before the
first tenant, keep serving while the node drains, return on the same address
after a reboot, and follow the logical node to replacement hardware. Fleet
already owns those transitions through NodeClaim. Placing the resolver in a
consumer's tenant API instead would make that consumer re-derive node identity,
drain order, and replacement from outside Fleet.

## Proposal

### Node service resource

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: NodeService
metadata:
  name: private-dns-resolver
spec:
  nodeSelector:
    matchExpressions:
      - key: fleet.datumapis.com/role
        operator: In
        values:
          - compute
          - edge
  slots:
    - name: a
    - name: b
  template:
    runtimeClassName: platform-service
    containers:
      - name: agent
        image: ghcr.io/datum-cloud/dns-operator:<version>
        args:
          - agent
        readinessProbe:
          exec:
            command:
              - /internal-dns
              - ready
      - name: dnsdist
        image: powerdns/dnsdist-19:<version>
    resources:
      requests:
        cpu: 200m
        memory: 256Mi
    sysctls:
      net.ipv4.ip_local_port_range: "10000 65535"
    networkAttachments:
      - name: producer
        networkRef:
          project: internal-dns-service
          name: internal-dns-producer
        reclaimPolicy: Retain
  readinessGate: true
  updateStrategy:
    slotOrder:
      - a
      - b
    maxUnavailablePerSite: 1
    pauseOnFailure: true
status:
  matched: 12
  ready: 24
  updated: 24
  conditions:
    - type: Ready
      status: "True"
```

- `nodeSelector` selects NodeClaims by Fleet labels. It cannot select by
  Kubernetes node name or hardware identity.
- `slots` declares how many instances run on each node and names them. Slot
  names are stable and short. They identify the instance on the node, not the
  node.
- `template` is the instance definition. The runtime class must be installed
  on the selected nodes through an [OSConfigPolicy](os-configuration.md#policy-resource);
  a NodeService cannot install host software.
- `networkAttachments` requests logical attachments from the networking
  authority. Fleet does not program routes, VIPs, or Galactic frontends.
- `readinessGate` makes the service count toward
  `fleet.datumapis.com/node-services-ready`.
- `updateStrategy` bounds a change of the template. See
  [Rollout and disruption](#rollout-and-disruption).

### Slots and identity

Each pair of NodeClaim and slot is one instance with a stable identity. Its
attachment reservation belongs to the NodeClaim's identity scope, so the
instance keeps its address:

- across container restarts and node reboots;
- across reinstall on the same hardware;
- across hardware replacement, once the predecessor is fenced.

`reclaimPolicy: Retain` keeps the reservation until the NodeClaim is removed or
the slot is removed from the spec. Two slots let one instance roll while the
other serves. The networking authority decides how clients reach the slots, for
example two Galactic frontends that select one producer per node and slot.

### Readiness

The node service controller reports each instance on its NodeClaim:

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: NodeClaim
metadata:
  name: us-central-1-alice-compute-0
status:
  nodeServices:
    - name: private-dns-resolver
      slot: a
      revision: private-dns-resolver-7
      ready: true
      addresses:
        - fd20:0:0:1::a
    - name: private-dns-resolver
      slot: b
      revision: private-dns-resolver-7
      ready: true
      addresses:
        - fd20:0:0:1::b
  conditions:
    - type: fleet.datumapis.com/node-services-ready
      status: "True"
      reason: AllSlotsReady
```

The condition has two rules, one to open it and one to keep it open:

- **To become `True`**, every slot of every gated service that selects the node
  must be ready. This applies when the node joins and again after each reboot,
  reinstall, or hardware replacement. A node with one dead resolver slot does
  not admit its first tenants.
- **To stay `True`**, each gated service needs at least one ready slot. A
  template roll or one failed slot does not evict tenants, because the other
  slot still serves. A failed slot sets the reason `SlotDegraded` and alerts
  the owning service.

The condition turns `False` with reason `NoReadySlot` when a gated service has
no ready slot. Unknown or stale probe evidence is `Unknown`, not `True`.

A consumer can require it before admitting tenants. A MachinePool declares this
through consumer readiness gates (see
[Capacity resource](cluster-lifecycle.md#capacity-resource)). For example,
Compute sets `compute.datumapis.com/tenant-ready` only after
`node-services-ready` is `True`.

### Rollout and disruption

A template change creates a new retained revision. Fleet rolls it slot by slot
in `slotOrder`: slot `a` across every selected node, then slot `b`. At most
`maxUnavailablePerSite` instances of the service are unavailable in a Site at
once. A failed readiness check pauses the roll when `pauseOnFailure` is set.
The node keeps serving from the other slot throughout, so a template roll
never closes `node-services-ready`.

A template roll is not an OS change and does not need a cluster
[Rollout](cluster-lifecycle.md#rollout-resource). An OS or runtime class change
that a node service depends on follows the node's Rollout, and the node service
must be ready again before the next node in that wave proceeds.

Node services are not tenants. A consumer's drain evacuates its tenants first.
Node services keep running until the node is cordoned for the last time or the
NodeClaim is released, so tenants never lose a local resolver while they still
run.

### Lifecycle

| Event | Node service behavior |
| --- | --- |
| Node added | Instances start once the node joins. The node is not tenant-ready until they are ready. |
| Template change | Slot `a` everywhere, then slot `b`, within the per-Site budget. |
| Consumer drain | Keeps running until the consumer's tenants have left. |
| Reboot | Instances return on the same slots and addresses. |
| Reinstall | Same as reboot; the attachment reservation is kept. |
| Hardware replacement | Instances start on the replacement with the same slots and addresses, after the predecessor is fenced. See [Fencing](#fencing). |
| Node removed | Attachments are released after the instances stop. |
| Standalone machine | Never selected. |

### Fencing

A replacement reuses the predecessor's addresses, so the predecessor must not
answer on them. The NodeClaim controller fences it through the infrastructure
provider and records the evidence on the NodeClaim:

- power off, confirmed by an out-of-band power-state reading, not by the
  command's acknowledgment;
- the predecessor's switch ports or fabric session withdrawn;
- the predecessor's node and service credentials revoked.

If fencing does not complete within the provider's fencing timeout, the
NodeClaim reports `Fenced=False` with reason `FencingStuck` and alerts
operators. The replacement is installed and joined, but its node service slots
do not take the shared addresses, so `node-services-ready` stays `False` and
the consumer admits no tenants there. The pool counts the NodeClaim as
unavailable, so a consumer that sizes its pool from demand gets capacity on
another node instead of waiting. Tenants are not affected: they had already
left the failed predecessor, and the replacement has none yet. An operator
clears a stuck fence by recording physical removal or a confirmed power-off,
which is the only manual override.

### Ownership and authority

The owning service writes its NodeService in its own service project. For
Private DNS, that is the DNS control plane, not Flux. Fleet IAM decides which
Fleet roles a service may select and which runtime classes it may use. Customer
projects cannot create NodeServices.

Each region must keep working when another region is down. A NodeService is
written per region, by the owning service's writer in that region, and selects
only that region's nodes. For Private DNS this means regional independence
needs more than local resolvers: each region keeps publishing records, not
only serving them, so it needs its own broker storage and eventually its own
control-plane writer. Today one control plane and one broker in us-central-1
feed every region, which a region outage in us-central-1 would stop.

Fleet owns placement, slot identity, roll order, and readiness reporting. The
owning service owns the template, the probe's meaning, and its own
configuration. Networking owns the attachments and how clients reach them.

### Standalone machines

A standalone machine handed to a customer runs the customer's OS. Fleet does
not run anything on it after handoff, so no NodeService selects it.

Private DNS for such a machine comes from the platform node where its VPC
attachment lands. The networking authority attaches the machine's VLAN to the
customer VPC at a platform node, such as a Galactic edge host. The resolver
NodeService selects that node's role, and Galactic routes `fd53::53` and
`fd53::54` to the resolvers on it. The customer sees the same resolver
addresses as on a Compute instance.

Two alternatives remain open:

- **A resolver on a DPU.** If the machine attaches through a DPU, Fleet
  can treat the DPU as a managed node and select it. Isolation from the
  customer's host OS then depends on the DPU.
- **The nearest healthy resolver in the location.** Galactic selects a producer
  on another node when no local one is ready. Galactic does not do this today:
  it picks only the producer on the guest's own node.

## Implementation

The node service controller in `datum-cloud/fleet` watches NodeService and
NodeClaim. For each selected pair of node and slot, it delivers one instance to
the managed cluster through the admitted delivery path, pinned to the Kubernetes
Node of that NodeClaim. It requests attachments from the networking authority
and reports instance readiness on the NodeClaim. It does not edit OS
configuration, MachineClaims, or attachments it did not request.

## Rollout plan

1. Add the NodeService and NodeClaim status schemas. Observe the existing Private
   DNS resolver Deployments without changing them.
2. Run the resolver NodeService alongside the existing resolvers, as a third
   producer behind a new frontend address, on one staging cell.
3. Gate tenant readiness on `node-services-ready` for that cell.
4. Move the existing frontend addresses to the NodeService and retire the
   hand-wired Deployments, their host pins, and the runtime class patch.
5. Before a second region relies on the resolver NodeService, make Private DNS
   publishing regional: each region gets its own broker storage and its own
   writer for its NodeService. Per-region publication storage is in progress
   in datum-cloud/dns-operator#339. A per-region control-plane writer has no
   issue yet and must be filed before this step starts.

## Acceptance gates

- A new compute node becomes tenant-ready only after every slot of every gated
  node service on it is ready, with no edits to per-host manifests.
- A node with one failed slot keeps its tenants and reports `SlotDegraded`.
- A template roll keeps at least one slot ready on every node.
- A reboot, reinstall, or hardware replacement returns each slot on its
  previous address, and only after the predecessor is fenced.
- A stuck fence reports `FencingStuck` and never blocks consumer capacity on
  other nodes.
- In a region outage, every other region keeps publishing and serving Private
  DNS records.
- A consumer drain never stops a node service while that consumer's tenants
  still run on the node.
- A standalone machine is never selected.

## Open questions

- Whether node services also run on control-plane and fabric nodes, or only on
  roles that host tenants.
- Whether the DPU or the location fallback is needed for Private DNS on
  standalone machines, beyond the attachment-node resolver.
- How much of the template a service may set, for example host networking or
  privileged containers, and who approves it.
