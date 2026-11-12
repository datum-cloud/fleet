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
provides the prototype authorization decision: `machineclaims` read and
`machinemanagementsessions` create are separate grants. The controller resolves
the claim against a service-owned `MachineAllocationBinding` and creates a
protected `MachineManagementSessionBinding`. The latter pins the principal,
consumer and provider Project UIDs, claim and machine name/UID pairs, binding ID
and epoch, location UID/revision, private BareMetalMachine UID, profile revision,
client key, deadlines, adapter route, and correlation IDs. None of the private
target fields are copied to the public session.

The projector writes a least-privileged session to the location API and refreshes
only its cached-authority deadline while every pinned input still matches. The
location gateway registers an outbound opaque route with the relay. Public status
contains only its relay URL, random route, proof audience, protocol version, and
gateway certificate fingerprint. The central API and controllers are not in the
Redfish request/response path.

The client consumes the route once, pins the per-session gateway TLS certificate,
and signs every request with a timestamped, nonce-bearing, domain-separated
Ed25519 proof. The gateway rejects changed inputs, audiences, expired timestamps,
nonce replay, stale authority, and adapter-route mismatch. Relay registration and
gateway TLS/proof credentials are separate. The relay only pairs an opaque route;
it cannot select a BMC target and does not implement unrestricted CONNECT.

The plugin exposes a TLS endpoint only on loopback. It writes a one-time local
username/password and CA bundle at mode `0600`, publishes a non-secret readiness
JSON file, deletes local login tokens on Redfish logout, and removes all temporary
files at shutdown. These credentials are unrelated to the gateway's private
sushy credential.

At the location, the gateway exposes one virtual system named `allocated`. Its
adapter selects the physical sushy system solely from protected binding state.
Customer paths, Hosts, OData links, and guessed identifiers never participate in
physical routing. The only supported mutations are one-time `Pxe` or `Hdd` boot
selection and `On`, `ForceOff`, `GracefulShutdown`, `GracefulRestart`, or
`ForceRestart`. Unsupported values fail rather than being substituted.

## Session and operation sequence

1. `datumctl fleet machine-management proxy CLAIM` generates a key and asks the
   management API for a session using only the claim name.
2. Admission retains the authenticated principal. The controller resolves the
   current claim UID and exact active allocation, enforces limits, and persists
   the protected binding before projection.
3. The projector checks the authoritative allocation on every refresh. The
   location gateway establishes an outbound relay route and publishes non-secret
   rendezvous status back through the projector.
4. The plugin consumes the route, verifies the pinned TLS identity, proves its
   admitted private key, and opens the loopback Redfish service.
5. Before a boot or reset effect, the gateway fsyncs an operation containing the
   session/allocation/correlation identities, normalized method and virtual URI,
   exact accepted body, internally selected physical target, idempotency key,
   operation and binding epochs, conflict/freshness preconditions, adapter route,
   and later execution evidence. Effects for a machine are serialized across all
   sessions in the gateway process.
6. A lost adapter response becomes `Unknown`; replaying the exact request returns
   the existing task without a second adapter call. Failed or unknown effects are
   never rewritten as success.
7. Expiry, public deletion, allocation release, or changed binding inputs closes
   the location stream. Release writes a terminal revocation annotation so later
   claim-name reuse or record reactivation cannot resurrect the session. If the
   location cannot confirm deletion, the central cleanup condition is `Unknown`.

## API and deployment artifacts

Kubebuilder-style Go types and generated CRDs are under `api/v1alpha1` and
`config/crd/bases`. CEL makes the public spec immutable and constrains protocol,
profile, key encoding, and TTL. Conditions carry `observedGeneration`, explicit
reasons, and true/false/unknown outcomes.

RBAC is in `config/rbac`; controller and location workloads are in
`config/manager` and `config/location`. Containers run as non-root with read-only
root filesystems, no added Linux capabilities, and `RuntimeDefault` seccomp.
NetworkPolicy permits the outbound relay, the one private BMC address, DNS, and
the Kubernetes API; it does not expose gateway ingress. Metrics cover admission,
active sessions, revocation latency, BMC reachability, operations, and unknown
outcomes. Protected bindings plus structured, correlation-bearing controller logs
provide the prototype's central audit record.

The declared DMTF interoperability profile is
`config/redfish/FleetProvisioning.v1_0_0.json`.

## Reproducible Linux environment

The qualified prerequisite is Linux with Docker Engine and Compose v2, Kind, kubectl, Go 1.26.1,
Task, jq, OpenSSL, and curl. KVM is used when `/dev/kvm` is available; otherwise
the same real libvirt domain runs with QEMU software emulation. There is no
in-memory or static-JSON BMC fallback. The scripts can also attempt the QEMU path
on Docker-backed macOS for development, but CI qualification remains Linux.

`task e2e:up` builds and starts:

- management and location Kind clusters;
- locally built controller, relay, and gateway images;
- a privileged test-only Ubuntu 24.04 container running libvirt/QEMU and pinned
  sushy-tools 2.2.0;
- allocated and unallocated libvirt domains on a PXE-enabled provisioning network;
- a minimal PXELINUX/initramfs fixture whose HTTP callback records the VM MAC;
- an internal Docker management network reachable only from the attached location
  Kind node; and
- pinned DMTF Python Redfish client and validator tools.

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
using datumctl's unmanaged-plugin trust command. It exercises generic DMTF client login, profile narrowing, distinct RBAC,
per-claim and per-project limits, one-shot rendezvous, real PXE and libvirt power
effects, DMTF validators, central API loss and freshness failure, release fencing,
claim recreation, route isolation, response-loss recovery, and bounded BMC errors.

Validator versions are `redfish` 3.4.2,
`redfish-service-validator` 3.1.8, and
`redfish-interop-validator` 3.0.3. Reports and PXE correlation evidence are under
`.e2e/validators` and `.e2e/virtual-bmc/evidence`.

## Troubleshooting

Run `task e2e:diagnostics` before teardown. It captures both clusters' resources,
events and logs, protected/public session state, libvirt domain state/XML, virtual
BMC logs, validator reports, acceleration selection, and PXE callbacks.

- A gateway stuck on `RelayUnavailable` usually indicates relay DNS/token or
  NetworkPolicy failure in the location cluster.
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

- Allocation and claim data are synthetic and pre-bound. There is no allocator,
  quota transaction, installation controller, cleanup/quarantine workflow, or
  cross-project Milo policy integration.
- Delegated Kubernetes authentication/RBAC models the intended Compute/Milo
  pattern, but the production Datum API aggregation and IAM permission names are
  not selected.
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
