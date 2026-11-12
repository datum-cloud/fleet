# Secure machine-management prototype

Status: implemented prototype; portable tests are runnable on macOS and Linux,
while the full sushy/libvirt path is Linux-only. This document records what the
repository implements. The [enhancement](../enhancements/secure-bmc-access.md)
remains the proposed product contract and is not weakened by the shortcuts here.

## Architecture and trust boundaries

The customer runs the real `datumctl` binary. Its plugin dispatcher finds the
first-party `datumctl-fleet` executable. The plugin receives project context from
datumctl, obtains its bearer credential only through `plugin.Token()`, generates
an ephemeral Ed25519 key pair, and creates a public
`MachineManagementSession`. It never reads `DATUM_TOKEN`.

The management-cluster controller authenticates through the Kubernetes delegated
request identity retained by the mutating admission webhook. Kubernetes RBAC
provides the prototype authorization decision: `machines` read and
`machinemanagementsessions` create are separate grants. The controller resolves
the customer `Machine` through the immutable, terminal `MachineAllocation` to a
provider `MachineCapacity` and creates a protected
`MachineManagementSessionBinding`. The latter pins the principal, consumer and
provider project identities, the Machine, allocation, MachineCapacity and
immutable `deviceRef`, authoritative location and Karmada member, client key,
deadlines, and correlation IDs. Local object UIDs are retained only in that
management-API record as recreation preconditions. None cross through Karmada
or enter the public session.

The controller writes a least-privileged session to the separate Karmada API and
creates an exact-cluster `PropagationPolicy` using the member identity retained
from protected allocation state. It has a Karmada-scoped credential and no cell
kubeconfig. Karmada creates a `Work` only for that member and reflects cell-written
status through a Fleet-specific `ResourceInterpreterCustomization`; the
interpreter rejects status from any other member and prevents a claimed route
from changing identity. The controller refreshes only the cached-authority
deadline while every pinned input still matches. The location gateway registers
an outbound opaque route with the relay. Public status
contains only its relay URL, random route, proof audience, protocol version, and
gateway certificate fingerprint. The central API and controllers are not in the
Redfish request/response path.

The client consumes the route once, pins the per-session gateway TLS certificate,
and signs every request with a timestamped, nonce-bearing, domain-separated
Ed25519 proof bound to that one-use route and gateway audience. The gateway
rejects changed projected inputs, audiences, expired timestamps, nonce replay,
and stale authority. Relay registration and gateway TLS/proof credentials are
separate. The relay only pairs an opaque route; it cannot select a BMC target and
does not implement unrestricted CONNECT.

The plugin exposes a TLS endpoint only on loopback. It writes a one-time local
username/password and CA bundle at mode `0600`, publishes a non-secret readiness
JSON file, deletes local login tokens on Redfish logout, and removes all temporary
files at shutdown. These credentials are unrelated to the gateway's private
sushy credential.

At the location, the gateway exposes one virtual system named `allocated`. The
projected session contains only the selected MachineCapacity name and session
authority. The gateway resolves a safe location-local MachineCapacity copy
through its immutable `deviceRef`, then selects exactly one service-owned
management-enrollment Secret carrying the adapter, endpoint, physical system and
credential inputs. Its adapter selects the physical sushy system only from that
resolved enrollment.
Customer paths, Hosts, OData links, and guessed identifiers never participate in
physical routing. The only supported mutations are one-time `Pxe` or `Hdd` boot
selection and `On`, `ForceOff`, `GracefulShutdown`, `GracefulRestart`, or
`ForceRestart`. Unsupported values fail rather than being substituted.

## Session and operation sequence

1. `datumctl fleet machine-management proxy MACHINE` generates a key and asks the
   management API for a session using only the customer Machine name.
2. Admission retains the authenticated principal. The controller resolves the
   current Machine, exact active MachineAllocation, provider MachineCapacity and
   immutable device relationship, enforces limits, and persists the protected
   binding before projection.
3. The projector checks the authoritative allocation on every refresh, creates a
   Karmada hub copy and exact-member policy, and waits for status from that member.
   The gateway reads only its cell-local copy, establishes an outbound relay
   route, and writes non-secret rendezvous status to that copy.
4. The plugin consumes the route, verifies the pinned TLS identity, proves its
   admitted private key, and opens the loopback Redfish service.
5. Before a boot or reset effect, the gateway fsyncs an operation containing the
   location-session, MachineCapacity and Device identities, normalized method
   and virtual URI, exact accepted body, internally selected physical target,
   idempotency key, operation epoch, conflict/freshness preconditions, and later
   execution evidence. Effects for a Device are serialized across all sessions
   in the gateway process. The protected central binding supplies durable
   correlation back to the MachineAllocation.
6. A lost adapter response becomes `Unknown`; replaying the exact request returns
   the existing task without a second adapter call. Failed or unknown effects are
   never rewritten as success.
7. Expiry, public deletion, allocation release, or changed binding inputs changes
   the hub copy's desired state to `Revoked`. Karmada carries the fence to the
   selected cell; the gateway closes the stream and writes `CleanupConfirmed`
   before the controller removes the policy and hub copy. Release also writes a
   terminal revocation annotation so later Machine-name reuse or a new allocation
   cannot resurrect the session. Deletion alone is never teardown proof; if the
   cell cannot acknowledge the fence, central cleanup is `Unknown`.

## API and deployment artifacts

Kubebuilder-style session types are under `internal/api/session/v1alpha1`; they
share `fleet.datumapis.com/v1alpha1` with the canonical lifecycle types under
`internal/api/v1alpha1`. Generated session CRDs are under `config/crd/bases`,
while the lifecycle CRD bundle remains under `config/base/crd`. CEL makes the
public spec immutable and constrains protocol, profile, key encoding, and TTL.
Conditions carry `observedGeneration`, explicit reasons, and true/false/unknown
outcomes.

RBAC is in `config/rbac`; controller and location workloads are in
`config/manager` and `config/location`. `config/federation` contains the Karmada
status interpreter and hub-only projector RBAC. Containers run as non-root with read-only
root filesystems, no added Linux capabilities, and `RuntimeDefault` seccomp.
NetworkPolicy permits the outbound relay, the one private BMC address, DNS, and
the Kubernetes API; it does not expose gateway ingress. Metrics cover admission,
active sessions, revocation latency, BMC reachability, operations, and unknown
outcomes. Protected bindings plus structured, correlation-bearing controller logs
provide the prototype's central audit record.

The declared DMTF interoperability profile is
`config/redfish/FleetProvisioning.v1_0_0.json`.

## Reproducible Linux environment

The qualified prerequisite is Linux with Docker Engine and Compose v2, Kind,
kubectl, Helm, Go 1.26.4, Task, jq, OpenSSL, and curl. KVM is used when `/dev/kvm` is available; otherwise
the same real libvirt domain runs with QEMU software emulation. There is no
in-memory or static-JSON BMC fallback. The scripts can also attempt the QEMU path
on Docker-backed macOS for development, but CI qualification remains Linux.

`task e2e:up` builds and starts:

- management and location Kind clusters;
- pinned Karmada 1.16.0 in the management cluster, with the location Kind
  cluster registered as a member;
- locally built controller, relay, and gateway images;
- a privileged test-only Ubuntu 24.04 container running libvirt/QEMU and pinned
  sushy-tools 2.2.0;
- allocated and unallocated libvirt domains on a PXE-enabled provisioning network;
- a minimal PXELINUX/initramfs fixture whose HTTP callback records the VM MAC;
- an internal Docker management network reachable only from the attached location
  Kind node; and
- pinned DMTF Python Redfish client and validator tools.

The harness is repository-local and does not currently consume
`datum-cloud/test-infra`. Production/environment installation is expected to be
owned there (or in `infra`) once hub identity, member registration, and location
registration contracts are selected. This is a prototype choice, not a proposal
change.

The sushy port accepts only the location node address. The customer process sees
only loopback ports for the relay and local Redfish connector. The default CI
profile relies on this isolated internal network. After `e2e:up`, the separate
`task e2e:openvpn:up` profile recreates the virtual BMC with a real OpenVPN `tun`
boundary, installs a privileged test client on the location node, moves the
gateway adapter to `10.88.0.1`, and proves the direct management address is
blocked. This profile is intentionally outside default hosted-runner CI.

Run the complete lifecycle with:

```console
task e2e:up
task e2e:test
task e2e:down
```

The test calls the actual `datumctl fleet ...` command. A repository-local test
Datum session stores synthetic service-account tokens through datumctl's file
keyring format, after which the plugin must still call datumctl's credential
helper. `e2e:up` explicitly trusts the locally built plugin by its path and SHA-256
using datumctl's unmanaged-plugin trust command. The test requires a real Karmada
`Work` in `karmada-es-fleet-location-e2e`, verifies the hub received the selected
cell's route status, and proves management has no direct location credential. It
exercises generic DMTF client login, profile narrowing, distinct RBAC,
per-Machine and per-project limits, one-shot rendezvous, real PXE and libvirt power
effects, DMTF validators, central API loss and freshness failure, release fencing,
Machine recreation, route isolation, response-loss recovery, and bounded BMC errors.

Validator versions are `redfish` 3.4.2,
`redfish-service-validator` 3.1.8, and
`redfish-interop-validator` 3.0.3. Reports and PXE correlation evidence are under
`.e2e/validators` and `.e2e/virtual-bmc/evidence`.

## Troubleshooting

Run `task e2e:diagnostics` before teardown. It captures both clusters' resources,
events and logs, Karmada clusters/Works/bindings and hub session state,
protected/public session state, libvirt domain state/XML, virtual
BMC logs, validator reports, acceleration selection, and PXE callbacks.

- A gateway stuck on `RelayUnavailable` usually indicates relay DNS/token or
  NetworkPolicy failure in the location cluster.
- A missing cell projection: inspect the hub `PropagationPolicy`, the Work in
  `karmada-es-<member>`, member `Ready`, and the Fleet status interpreter.
- `BMCUnavailable` is intentionally bounded; inspect the gateway and virtual-BMC
  diagnostics rather than expecting a private address in the customer response.
- No PXE callback: inspect `allocated-console.log`, libvirt network state, and the
  VM MAC in `pxe-callbacks.jsonl`.
- `AuthorityStale` after a controller outage is expected. The E2E deployment uses
  15 seconds so the test is fast; the binary default is two minutes.
- `task e2e:down` validates its repository-local state path and names every Docker
  and Kind target before removing it.

## Prototype choices and remaining production gaps

Implemented-and-tested code must not be confused with a production-ready
contract. Important gaps are:

- Machine, MachineAllocation, MachineCapacity, location registration, and local
  enrollment data are synthetic and pre-bound. There is no production allocator,
  quota transaction, installation controller, cleanup/quarantine workflow, or
  cross-project Milo policy integration.
- Delegated Kubernetes authentication/RBAC models the intended Compute/Milo
  pattern, but the production Datum API aggregation and IAM permission names are
  not selected.
- Karmada projection and status aggregation are implemented and exercised, but
  the prototype stores the member name directly in the protected synthetic
  allocation. Production needs an authoritative location-to-member registration
  controller, credential rotation, HA operations, failover policy, and
  environment packaging in `test-infra`/`infra`.
- The relay is a small Fleet-specific opaque TCP rendezvous with inner TLS, not
  Compute's maintained relay deployment. A shared public transport package and
  operational ownership decision are still required; Fleet does not import
  Compute internal packages.
- The gateway is intentionally single-replica with an RWO journal. Its fsynced
  files and in-process per-machine lock demonstrate order and recovery, but a
  production multi-replica lease/fencing store is not implemented.
- Exact request bytes form the prototype idempotency identity. Production needs a
  client-visible idempotency contract and adapter-specific recovery observation.
- The default test isolates the management network with Docker and iptables; a
  separate OpenVPN profile exists but is not yet a production-qualified VPN.
  Credential rotation, HSM-backed location identity, multi-location PKI, and
  relay denial-of-service controls remain.
- The virtual profile omits firmware, AccountService, Managers, UpdateService,
  virtual media, OEM actions, and BMC networking by design. Broader capability
  requires a separately reviewed profile and adapter qualification.
- No physical BMC vendor is qualified. sushy/libvirt demonstrates effects and
  ambiguity handling only.
