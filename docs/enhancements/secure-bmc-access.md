---
status: provisional
stage: alpha
latest-milestone: "unplanned"
---

# Secure customer BMC access

This enhancement proposes allocation-scoped customer access to a narrow Redfish
API without exposing a BMC, its management network, or its credentials directly.
It extends the provisional [Fleet design context](../design.md) and the pinned
[bare-metal provisioning proposal][fleet-provisioning]. No API or behavior in
this document is implemented or verified in Fleet today.

The architecture borrows the session and data-path separation used by Datum
Compute's provisional [instance shell sessions][compute-shell]: the platform
authenticates and authorizes a short-lived session, binds it to an authoritative
placement, and then keeps the central control plane out of the request data path.
The transport and resource names below remain design proposals; adopting this
pattern does not require Fleet to reuse Compute resources or deployment
components.

## Summary

Customers with an active `MachineClaim` can request a short-lived management
session for the machine allocated to that claim. Datum authenticates the caller
with the normal platform identity, authorizes the requested capability against
the current allocation, and binds the session to the consumer project, principal,
Claim UID, Machine UID, binding identifier and binding epoch.

After admission, Redfish traffic travels through an encrypted relay path to a
location-local Redfish gateway. The gateway exposes a small, provider-neutral
Redfish profile, resolves the private BMC target for the retained allocation,
and reaches the BMC through the location's management network. For the initial
Supermicro integration, that network is expected to be reachable through an
operator-managed OpenVPN connection. Customers do not receive VPN access, a BMC
address, or BMC credentials.

The central API handles session admission, binding, status, audit and revocation.
It does not proxy each Redfish request. Relay capacity and location gateways
scale independently, and a location can continue serving already admitted,
still-valid sessions during a bounded control-plane interruption.

## Motivation

Some customers already operate bare-metal provisioning systems and need to keep
using them while Fleet's installation and cluster automation is developed. PXE
boot, power control and machine inspection are enough to integrate many of these
systems, but direct BMC or management-VPN access would expose a large and
vendor-specific administrative surface.

A central Redfish reverse proxy would hide BMC credentials, but it would also
place every hardware request and response on a global service. That creates an
unnecessary availability, latency and scaling dependency and requires the global
service to maintain routes into every management network.

Fleet instead needs an access contract that:

- reuses the existing MachineClaim allocation authority;
- preserves a standard Redfish API for customer provisioning software;
- keeps BMC endpoints, credentials and management networks private;
- routes once when a session is admitted instead of globally routing every
  Redfish request;
- fences stale sessions when a machine is released or reassigned; and
- retains exact authority and inputs before power or boot effects.

## Goals

- Let an authorized human or service identity operate the machine currently
  allocated to its `MachineClaim` through a documented Redfish interoperability
  profile.
- Support customer-controlled PXE provisioning through one-time boot selection
  and bounded power actions before full Fleet installation automation exists.
- Use Datum identity and IAM for session admission without requiring customer
  accounts to be created on each BMC.
- Keep the central Fleet API out of the Redfish data path.
- Keep location management connections outbound-only where the selected relay
  transport permits it.
- Prevent a session, delayed request or recovered worker from affecting a later
  allocation of the same machine.
- Record mutating operation authority before effects and report uncertain
  outcomes as unknown.
- Provide the same public contract across qualified BMC vendors while allowing
  vendor-specific adapters behind the location gateway.

## Non-goals

- General BMC administration or unrestricted pass-through to a vendor API.
- Customer access to the BMC web UI, SSH service, IPMI, management VPN, OEM
  endpoints, account management, firmware update or BMC network configuration.
- Selecting a final relay implementation, state store, API group version,
  provisioning engine or Supermicro model matrix.
- Replacing brokered serial or graphical console access. Console remains a
  separately authorized capability with its own transport and recording policy.
- Making Redfish the internal contract for every Fleet hardware provider.
- Treating BMC command acceptance as proof that a requested hardware state was
  reached.

## Consumer experience

### Prerequisites

The consumer has:

- an active `MachineClaim` bound through Fleet's allocator;
- an IAM grant that permits creation of management sessions for that claim;
- a machine offering that advertises the requested Redfish access profile; and
- a supported client connector, supplied as a CLI command, sidecar or SDK
  component.

The caller never supplies a Machine, BMC address, location route or provider
credential. Those are resolved from the claim's private retained binding.

### Manual access

The exact command is illustrative:

```console
$ datumctl fleet machine-management proxy db-01
Session db-01-k7m2p is ready until 2026-11-11T19:15:00Z
Redfish endpoint: https://127.0.0.1:9443/redfish/v1
```

The connector creates an ephemeral key pair in memory, creates a management
session containing the public key, waits for the location endpoint, and opens an
encrypted tunnel. It exposes a loopback Redfish endpoint for ordinary Redfish
tools. Closing the connector deletes or closes the session and clears the private
key.

The loopback TLS and Redfish login-session behavior must be qualified with the
initial client tools. The connector may issue a one-time local login credential
and a per-session CA bundle, then implement Redfish `SessionService` locally and
return an `X-Auth-Token`. Those credentials authorize only the local connector;
they are never forwarded to or created on the BMC. The connector must not write
the transport private key or a reusable BMC credential to disk by default.

### Customer provisioning automation

An automated provisioner uses a Datum service identity to create a session for a
MachineClaim in its project. A connector sidecar owns the ephemeral transport
key and presents the provisioner with the same Redfish base URL for the session's
lifetime. The provisioner can then:

1. read the Redfish service root and computer-system state;
2. set a one-time PXE boot override;
3. request a supported reset or power transition;
4. follow a returned Redfish task when the operation is asynchronous; and
5. return boot selection to local disk if its workflow requires it.

The customer continues to own its PXE, DHCP, image and installation workflow.
This enhancement supplies only the admitted hardware-management operations.
Networking and IPAM remain responsible for customer and provisioning network
attachments.

### Compatibility boundary

The HTTP resources and payloads exposed after connection follow Redfish. Session
admission, location rendezvous and the encrypted relay are Datum-specific. A
generic Redfish tool therefore works through the connector, not by connecting
directly to a BMC or to a global Fleet proxy.

If requiring a connector proves incompatible with important customer tooling, a
future deployment may expose the same gateway through regional HTTPS ingress and
short-lived scoped credentials. That is a transport alternative, not a reason to
change the allocation, authorization, operation or Redfish contracts.

## Proposed Redfish profile

The gateway presents a virtual Redfish service rather than forwarding the BMC's
entire resource tree. Resource identifiers are stable within the session and do
not reveal private device identifiers. The initial profile is intentionally
small:

| Method and resource | Initial behavior |
| --- | --- |
| `GET /redfish/v1/` | Service discovery for the exposed profile. |
| `GET /redfish/v1/SessionService` and its session collection | Redfish login-session discovery. |
| `POST /redfish/v1/SessionService/Sessions` and `DELETE` of the resulting member | A session local to the connector or virtual gateway; no account or session is created on the BMC. |
| `GET /redfish/v1/Systems` | A collection containing the one allocated system. |
| `GET /redfish/v1/Systems/{id}` | Permitted system, power and boot observations. |
| `PATCH /redfish/v1/Systems/{id}` | One-time boot override for qualified targets such as PXE or local disk. Other fields are rejected. |
| `POST /redfish/v1/Systems/{id}/Actions/ComputerSystem.Reset` | Qualified reset and power actions. Unsupported `ResetType` values are rejected rather than substituted. |
| `GET /redfish/v1/TaskService` and task members | Progress and outcome for asynchronous gateway operations. |

The gateway does not expose `AccountService`, `Managers`, `UpdateService`, BMC
network configuration, virtual media, log deletion, OEM actions, arbitrary
`Actions` targets or links outside the allowlisted virtual resource tree. Later
profiles require explicit review, capability publication and authorization.

Redfish supports HTTP Basic, Redfish login sessions, and optional delegated
OAuth 2.0 bearer authentication. This proposal does not depend on the physical
BMC implementing Datum OAuth. Platform identity authorizes creation of the Fleet
session; possession of its ephemeral private key authorizes the location data
path; the gateway authenticates to the BMC with operator-held credentials.

## Architecture

### Request and data paths

```text
                         control and status path
  client  ── OAuth ──> Fleet project API ──> session admission and binding
    │                                              │
    │                                              v
    │                                  location-local session projection
    │                                              │
    │              encrypted Redfish data path    v
    └────────> Datum relay ───────────────> location Redfish gateway
                                                   │
                                                   │ private target and credential
                                                   v
                                           OpenVPN / management network
                                                   │
                                                   v
                                                  BMC
```

The exact projection mechanism is not selected here. It must deliver the bound
session and revocations to the authoritative location without giving the
location agent broad credentials to the consumer project API. Status returns
through the same controlled synchronization contract. A location cannot choose
itself or replace an already bound endpoint through status.

### Responsibilities

- **Fleet project API:** authenticates the user or service identity, authorizes
  the requested session, validates immutable input and exposes lifecycle status.
- **Management-session admission:** resolves the private active allocation,
  checks current capability and conflicts, retains the exact binding and chooses
  the authoritative location from trusted allocation state.
- **Session projection:** carries the least-privileged location-local session
  copy and revocation state. It is not the Redfish request path.
- **Relay endpoint:** maintains outbound connectivity from the location and
  forwards an end-to-end encrypted stream. It has no BMC credential and may
  reach only the location gateway.
- **Location Redfish gateway:** verifies possession of the session key, consumes
  or attaches the session, enforces its profile, virtualizes Redfish resources,
  retains mutating operations and calls the selected adapter.
- **BMC adapter:** translates the profile to a qualified physical BMC, holds or
  obtains its credentials from location-scoped secret custody, and normalizes
  supported observations and results.
- **Operation journal:** durably records admitted mutating requests, exact
  binding, target, normalized request body, idempotency identity, execution route
  and evidence before effects.

The gateway and adapter may be one deployment initially, but their responsibilities
and credential boundaries remain distinct. The relay endpoint must not become an
unrestricted HTTP CONNECT service to the management network.

### Authentication and authorization

Session admission uses Datum's existing human and service identity flows. IAM
defines a management-session create permission separately from ordinary
MachineClaim read or update. Admission evaluates that permission against the
consumer project and target MachineClaim and then resolves the private binding;
possession of a claim name or UID is not authorization.

The client generates an ephemeral Ed25519 key pair. Only the public key enters
the proposed session resource. On connection, the gateway verifies a signature
covering at least the session UID, protocol version, connection timestamp and
gateway audience. The first accepted connection consumes the connection window.
Replay, a changed route or a second concurrent connector is rejected.

The admitted access profile is selected from provider-published capabilities and
the caller's grant. The caller cannot supply an arbitrary path allowlist or ask
the gateway to forward unknown Redfish resources. A narrower request can be
accepted; it cannot expand the offering.

The physical BMC sees only the gateway's location-scoped identity. BMC credentials
must never appear in the public session, its status, audit events, client errors
or relay configuration. Credential rotation does not change Machine identity or
create a customer session.

### Binding and routing

Routing is a one-time admission decision, not a lookup on each Redfish request.
The protected session binding retains:

- consumer and provider Project UIDs;
- requesting principal identity;
- MachineClaim name and UID;
- Machine name and UID;
- allocation binding identifier and epoch;
- selected location UID and location-service revision;
- selected BareMetalMachine and adapter identity;
- admitted access-profile revision;
- client public key, creation time, connect deadline and expiry; and
- session and operation correlation identifiers.

The consumer-visible object does not reveal the provider project, Machine,
BareMetalMachine, BMC endpoint or adapter route. The location gateway resolves
the retained target through its private provider records and verifies that every
request still matches the projected binding epoch.

If an allocation moves, is released or is replaced, a new session is required.
The old endpoint cannot be rebound to the successor. A location-revision change
requires controlled handover or session replacement; mutable discovery must not
silently redirect an active session.

### Session lifecycle

A session progresses through `Pending`, `Ready` and `Connected`, then reaches one
terminal reason. Proposed terminal reasons include `Completed`, `Expired`,
`Revoked`, `NotConnected`, `AllocationEnded`, `LocationUnavailable`, `Rejected`,
`Disconnected` and `CleanupUnconfirmed`.

`Ready` means a gateway accepted the projected session and published a usable
endpoint before the connection deadline. It does not mean the BMC is healthy or
that any requested hardware state was reached. `Connected` means the ephemeral
client key was accepted. Session expiry and command outcome remain separate.

Deleting or closing a session immediately denies new central admission and
propagates revocation to the location. Release of the MachineClaim has higher
priority: it fences new operations, revokes all sessions for that binding and
waits for active gateways to confirm teardown or records the result as unknown.
The allocator must not make the Machine available to a later consumer until the
release contract has accounted for those sessions.

The gateway fails closed when its allocation/session projection exceeds its
freshness limit. A partition does not turn cached authorization into an
indefinite management grant. An already admitted operation may continue only
under its retained authority and bounded expiry.

### Mutating operations

Read-only Redfish requests can be served from current BMC observations within
the session profile. Before forwarding a boot or power mutation, the gateway
retains a protected operation containing:

- the session and allocation identities;
- the normalized Redfish method, virtual target and exact accepted body;
- the physical target resolved by the adapter;
- an idempotency identity and operation epoch;
- current binding, session and conflict preconditions; and
- the selected adapter route and credential revision without secret material.

The operation journal is the authority for the effect. A gateway process lease
or an open relay stream is not sufficient. Installation, release, power intent
and other hardware operations serialize through the same asset-level conflict
contract.

If the BMC response is lost, the result is `Unknown`. Recovery queries the
existing operation and physical state before deciding whether another request
is safe. The gateway must not translate an uncertain failure into a successful
Redfish response or blindly submit the same reset again. A Redfish task can
remain incomplete or finish with an explicit exception while recovery continues.

### Availability and scaling

Central load is proportional to session creation, status transitions, revocation
and audit events, not to Redfish request volume or response size. Relays provide
generic transport capacity and can scale independently. Each location gateway
handles only sessions for machines managed in that location.

The location gateway and its operation state must be recoverable independently
of the workload machines it manages. The management path cannot depend on a
guest OS, customer cluster or the server being powered on. A location that
cannot establish current allocation authority stops accepting new connections
and mutations rather than falling back to raw BMC access.

### Audit and observability

The project activity log records session creation with the requesting human or
service identity. Lifecycle events record the session UID, project, claim UID,
access profile, bound location, start and end times, and terminal reason. It must
not record private keys, session-local bearer values, BMC credentials, raw
provider endpoints or secret-bearing response bodies.

Every mutating request receives a Fleet operation correlation identifier. Audit
records include its normalized operation type and outcome. Request bodies are
retained in protected operation state when needed for recovery; customer-facing
activity uses a safe summary. Session byte streams and Redfish response bodies
are not recorded by default.

Metrics cover admission latency and denial reasons, session readiness, active
connections, revocation latency, gateway/BMC reachability, operation outcome and
age of unknown results. Metrics must not label by customer-controlled high-cardinality
values or expose machine-management addresses.

## Proposed API design

All names, groups and fields in this section are illustrative. The proposal uses
`MachineManagementSession` to distinguish the customer contract from a BMC login
session. It is a consumer-project resource and does not expose a raw BMC handle.

### MachineManagementSession

Each resource represents one short-lived connection to one immutable claim
binding. The spec is immutable; changing the claim, key, profile or TTL requires
a new resource.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineManagementSession
metadata:
  generateName: db-01-
  namespace: acme
spec:
  machineClaimRef:
    name: db-01
  protocol: Redfish
  accessProfile: Provisioning
  clientPublicKey: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  ttl: 15m
status:
  connection:
    endpointID: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
    relayURLs:
      - https://relay.us-central-1.example.invalid
    target: redfish-gateway-0.machine-access-system.svc:8443
  connectBefore: "2026-11-11T19:01:00Z"
  expiresAt: "2026-11-11T19:15:00Z"
  conditions:
    - type: Ready
      status: "True"
      reason: SessionReady
      message: Connect before 2026-11-11T19:01:00Z
      observedGeneration: 1
      lastTransitionTime: "2026-11-11T19:00:01Z"
```

`machineClaimRef.name` is a consumer lookup handle in the authenticated project;
the consumer does not supply or manage allocation identities. During session
admission, Fleet resolves the name and retains the current Claim UID, Machine UID,
binding identifier and binding epoch in the protected session binding before the
session can become Ready or authorize an effect. Admission requires the resolved
claim to be bound and active. If the claim or binding changes before that commit,
admission fails or restarts resolution rather than following stale state. A later
claim that reuses the same name cannot receive an already admitted session.

`protocol` is initially only `Redfish`. `accessProfile` refers to a versioned
platform-defined profile; arbitrary methods or paths are not accepted in the
spec. `ttl` begins no later than connection acceptance and is capped by policy.

`status.connection` is rendezvous information, not a bearer capability. Holding
the matching ephemeral private key is required to connect. The target is a
virtual gateway service and must not contain a BMC address. Generally readable
status contains neither the private allocation binding nor credentials.

Deleting the resource requests revocation. Finalization or equivalent retained
service state keeps the private session binding until the location confirms
teardown or the platform records that confirmation as unknown. Deletion alone
is not teardown evidence.

### Protected session binding

Admission creates a private service-owned binding record outside the consumer
project's deletion domain. It contains the routing and fencing fields listed in
[Binding and routing](#binding-and-routing). Only the session admission service
can create it; the location gateway can observe only the sessions routed to its
location and can update only their location-local status.

This record is intentionally separate from `MachineClaim.status`. A public
session may be short-lived or deleted while its audit, revocation and release
obligations remain. The claim remains the allocation authority; creating a
session cannot alter the binding or machine lifecycle.

### Permission model

The initial permission model should distinguish at least:

- reading session status;
- creating a provisioning-profile session;
- revoking one's own or project sessions;
- operator inspection of protected routing and operation evidence; and
- operator override or recovery actions.

Default viewer access does not imply session creation. Service identities receive
an explicit grant. The admission service rechecks current source capability,
claim authority and allocation state at creation; an old IAM decision or copied
session object is not sufficient.

### API and Redfish errors

Session admission uses normal Datum API errors and conditions. Once connected,
the gateway returns Redfish status codes and extended error bodies for profile,
capability and operation failures. Errors must distinguish unsupported behavior,
conflict with another admitted operation, expired/revoked authority, unreachable
BMC and unknown execution outcome without disclosing the physical route.

## Security considerations

- **Compromised customer credential:** IAM limits session creation to authorized
  claims; quotas, short connection windows and activity alerts bound abuse.
- **Stolen ephemeral private key:** keys live only in connector memory, have a
  short lifetime and are bound to one session, gateway audience and allocation.
- **Compromised relay:** end-to-end encryption prevents the relay from reading or
  modifying Redfish traffic; the relay holds no allocation or BMC credential.
- **Replay or stale allocation:** session UID, Claim UID, Machine UID, binding
  epoch, key proof, timestamps and gateway audience are checked together. Release
  propagates revocation and blocks reuse until sessions are accounted for.
- **Confused deputy:** the gateway resolves targets only from retained bindings,
  never from a customer URL, host header, Redfish link or BMC identifier.
- **Compromised location gateway:** network policy and credentials limit it to
  machines in its location. Credential isolation and one authoritative adapter
  reduce blast radius; a compromised gateway remains a high-severity management
  incident requiring credential rotation and allocation review.
- **BMC response injection:** the adapter validates TLS trust and the enrolled
  physical identity. Redirects or links outside the virtual allowlist are not
  followed or returned as customer routes.
- **Persistent firmware state:** the narrow profile excludes account, network,
  firmware and OEM mutation. Release still evaluates applicable BMC and firmware
  restoration obligations before reuse.

## Rollout plan

1. **Contract prototype:** implement the virtual Redfish profile against a
   virtualized or simulated Supermicro-compatible BMC. Exercise common Redfish
   clients through a local connector and verify that unlisted resources and
   fields cannot be reached.
2. **Location transport prototype:** establish outbound relay connectivity from
   a lab location, project a synthetic bound session, and prove direct client-to-location
   traffic without placing the central API in the Redfish data path.
3. **Physical lab qualification:** connect the gateway to disposable Supermicro
   hardware over the management OpenVPN path. Verify correct-host identity,
   one-time PXE boot, supported power actions, task recovery, credential rotation,
   rate limits and network isolation.
4. **Allocation integration:** authorize sessions against real MachineClaims and
   retain the private binding epoch. Exercise concurrent allocation/release,
   project deletion, location loss, stale session replay and uncertain BMC
   responses.
5. **Customer pilot:** enable an explicit IAM grant and session quota for selected
   projects. Integrate one existing customer provisioner without giving it BMC
   or VPN credentials. Review audit records and operational load before expansion.

## Acceptance gates

- A standard Redfish client, using the supported connector, can inspect the
  allocated system, select one-time PXE boot and perform each offered power action.
- The central API is absent from the Redfish request and response data path.
- The customer cannot discover or reach the BMC address, management VPN, another
  machine, an OEM endpoint or an unlisted Redfish field/action through the gateway.
- Human and service identities use normal Datum authentication; an explicit IAM
  grant and current MachineClaim binding are required for session creation.
- Two claims, projects or overlapping classes cannot cause a session to bind the
  wrong Machine. A reused claim name or stale binding epoch is rejected.
- Release and project deletion deny new operations, actively close sessions and
  prevent capacity reuse until teardown is confirmed or represented as unknown.
- A mutating request is retained before the BMC effect. Lost responses and gateway
  restarts recover the existing operation without blindly repeating it.
- Gateway, relay and BMC credential failure produce actionable but non-secret
  status and never become false readiness.
- The same public Redfish profile can be implemented by another qualified adapter
  without changing the consumer session contract.

## Open questions

- Whether the first connector is embedded in `datumctl`, shipped as a sidecar,
  exposed through an SDK, or provided in more than one form.
- Which existing Redfish clients must be supported and what loopback TLS and
  session-login behavior they require.
- The exact first-version access profile, allowed `ResetType` values and boot
  targets for each qualified Supermicro model and firmware revision.
- Whether an established Datum Connect deployment can satisfy browser, CLI and
  service automation requirements, or whether a dedicated relay profile is
  needed.
- The session projection and revocation transport, freshness limit and behavior
  during control-plane or location partitions.
- Durable storage and concurrency boundaries between Fleet admission, the
  location operation journal and provider adapters.
- Whether future regional HTTPS ingress is needed for customers that cannot run
  a connector, and how its bearer credentials would be scoped and revoked.
- Session quotas, TTLs, per-machine concurrency, request rate limits and activity
  alert thresholds for the initial pilot.
- Ownership and deployment boundaries for the gateway, relay endpoint, BMC
  adapter and management OpenVPN connection.

## References

- [DMTF Redfish Specification DSP0266 v1.22.1][redfish-spec]
- [DMTF Redfish schema and developer resources][redfish-resources]
- [Datum Compute instance shell session proposal][compute-shell]
- [Datum Compute session protocol][compute-session-protocol]
- [Datum Compute client-to-location tunnel][compute-tunnel]
- [Pinned Fleet infrastructure management proposal][fleet-proposal]
- [Pinned Fleet bare-metal provisioning proposal][fleet-provisioning]

[compute-session-protocol]: https://github.com/datum-cloud/compute/blob/fe85e3c619d2a774fc9ad45b2d362ced32a43c28/internal/consolesession/protocol.go
[compute-shell]: https://github.com/datum-cloud/compute/blob/fe85e3c619d2a774fc9ad45b2d362ced32a43c28/docs/enhancements/instance-shell-sessions/README.md
[compute-tunnel]: https://github.com/datum-cloud/compute/blob/fe85e3c619d2a774fc9ad45b2d362ced32a43c28/internal/consoleclient/tunnel.go
[fleet-proposal]: https://github.com/datum-cloud/infra/tree/51781e2456367be82281e1443ca56d0686311133/docs/enhancements/fleet-management
[fleet-provisioning]: https://github.com/datum-cloud/infra/blob/51781e2456367be82281e1443ca56d0686311133/docs/enhancements/fleet-management/bare-metal-provisioning.md
[redfish-resources]: https://redfish.dmtf.org/essentials
[redfish-spec]: https://www.dmtf.org/sites/default/files/standards/documents/DSP0266_1.22.1.pdf
