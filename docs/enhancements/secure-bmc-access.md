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
this document is an accepted production contract. The repository contains a
[working prototype](../implementation/machine-management-prototype.md) aligned
with the Machine, MachineAllocation and MachineCapacity identity model below.
Its synthetic allocation, location registration and enrollment records remain
prototype fixtures and must not be treated as accepted product APIs.

The architecture borrows the session and data-path separation used by Datum
Compute's provisional [instance shell sessions][compute-shell]: the platform
authenticates and authorizes a short-lived session, binds it to an authoritative
placement, and then keeps the central control plane out of the request data path.
The transport and resource names below remain design proposals; adopting this
pattern does not require Fleet to reuse Compute resources or deployment
components.

## Summary

Customers with an active consumer `Machine` can request a short-lived management
session. Datum authenticates the caller with the normal platform identity,
authorizes the requested capability against the Machine's current immutable
allocation, and resolves it to one provider `MachineCapacity` and location.
Kubernetes UIDs remain private admission evidence in the API server that issued
them; binding identifiers, binding epochs and cross-cluster UIDs are not part of
the session contract.

After admission, Redfish traffic travels through an encrypted relay path to a
location-local Redfish gateway. The gateway exposes a small, provider-neutral
Redfish profile, resolves the private BMC target for the retained allocation,
and reaches the BMC through the location's management network. For the initial
Supermicro integration, that network is expected to be reachable through an
operator-managed OpenVPN connection. Customers do not receive VPN access, a BMC
address, or BMC credentials.

The central API handles session admission, allocation resolution, status, audit
and revocation. It does not proxy each Redfish request. Relay capacity and
location gateways
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

- reuses the existing customer Machine allocation authority;
- preserves a standard Redfish API for customer provisioning software;
- keeps BMC endpoints, credentials and management networks private;
- routes once when a session is admitted instead of globally routing every
  Redfish request;
- fences stale sessions when a machine is released or reassigned; and
- retains exact authority and inputs before power or boot effects.

## Goals

- Let an authorized human or service identity operate its active customer
  `Machine` through a documented Redfish interoperability
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

- an active `Machine` allocated through Fleet's allocator;
- an IAM grant that permits creation of management sessions for that Machine;
- a machine offering that advertises the requested Redfish access profile; and
- a supported client connector, supplied as a CLI command, sidecar or SDK
  component.

The caller supplies only the customer Machine name. It never supplies the
provider `MachineCapacity`, `Device`, BMC address, location route or provider
credential. Fleet resolves those from protected allocation state.

### Manual access

The first supported connector is the first-party `datumctl-fleet` plugin and is
dispatched through the real `datumctl` executable:

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
Machine in its project. A connector sidecar owns the ephemeral transport
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

### Control-plane ownership

| State | Authoritative control plane |
| --- | --- |
| Customer `Machine` and `MachineManagementSession` | Customer Milo project API |
| Provider `Device` and `MachineCapacity` | Provider Milo project API |
| Immutable `MachineAllocation` and private admission evidence | Fleet management GKE cluster |
| Karmada policy and hub copies | Karmada control plane in the Fleet management cluster |
| Safe `Device`, `MachineCapacity` and session copies | Selected location member cluster |
| Owned-hardware enrollment, operation journal and materialized Secrets | Selected location member cluster |
| Authoritative BMC credential and trust material | Regional environment secret manager |

Milo supplies project identity, IAM, service enablement and quota. The Fleet
management GKE cluster runs the trusted controllers and Karmada hub; it is not a
customer Kubernetes API and has no BMC-network route. A location cluster has no
consumer-project credential and cannot make or change the global allocation
decision.

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

The initial `Provisioning` profile accepts one-time `Pxe` and `Hdd` boot targets.
It accepts `On`, `ForceOff`, `GracefulShutdown`, `GracefulRestart` and
`ForceRestart` reset types only after the owned-hardware adapter qualifies the
corresponding BMC behavior. An unsupported target or reset type returns a
Redfish extended error; the gateway never substitutes a more disruptive action.

Redfish supports HTTP Basic, Redfish login sessions, and optional delegated
OAuth 2.0 bearer authentication. This proposal does not depend on the physical
BMC implementing Datum OAuth. Platform identity authorizes creation of the Fleet
session; possession of its ephemeral private key authorizes the location data
path; the gateway authenticates to the BMC with operator-held credentials.

## Architecture

### Request and data paths

```text
                            control and status path
  client  ── OAuth ──> Fleet project API ──> session admission
    │                                                 │
    │                          Fleet management GKE    v
    │                               Karmada hub ──> exact location member
    │                                                 │
    │                 encrypted Redfish data path     v
    └─────────> Datum relay ──────────────> location Redfish gateway
                                                      │
                                                      │ owned-hardware adapter
                                                      v
                                              private management network
                                                      │
                                                      v
                                                     BMC
```

Karmada is the selected control-plane delivery mechanism. Fleet creates a
minimal location representation of the admitted session in the Karmada hub and
an exact-member propagation policy selected from protected Location
configuration. Karmada delivers the session and revocations without giving the
location credentials to the consumer project API. A Fleet resource interpreter
accepts status only from the selected member. The member cannot choose itself,
change centrally owned session input or redirect the session through status.
Karmada `Work`, `ResourceBinding` and propagation resources are federation
implementation details, not Fleet product resources.

### Responsibilities

- **Fleet project API:** authenticates the user or service identity, authorizes
  the requested session, validates immutable input and exposes lifecycle status.
- **Management-session admission:** resolves the active customer Machine through
  its immutable allocation to one provider `MachineCapacity`, checks current
  capability and conflicts, retains private admission evidence, and chooses the
  authoritative location from trusted allocation state.
- **Karmada session delivery:** carries the least-privileged resolved session and
  revocation state to exactly one location. It is not another authorization
  decision and is not the Redfish request path.
- **Relay endpoint:** maintains outbound connectivity from the location and
  forwards an end-to-end encrypted stream. It has no BMC credential and may
  reach only the location gateway.
- **Location Redfish gateway:** verifies possession of the session key, consumes
  or attaches the session, enforces its profile, virtualizes Redfish resources,
  retains mutating operations and calls the selected adapter.
- **Datum owned-hardware adapter:** resolves the selected `MachineCapacity`
  through its immutable `Device`, verifies the enrolled physical identity,
  translates the profile to a qualified BMC, obtains credentials from
  location-scoped secret custody, and normalizes observations and results.
- **Operation journal:** durably records admitted mutating requests, exact
  session and asset preconditions, normalized request body, idempotency
  identity, privately resolved target and evidence before effects.

The gateway and adapter may be one deployment initially, but their responsibilities
and credential boundaries remain distinct. The relay endpoint must not become an
unrestricted HTTP CONNECT service to the management network.

### End-to-end sequence

1. `datumctl-fleet` generates an ephemeral Ed25519 key pair and creates a public
   `MachineManagementSession` referring to the customer Machine by name.
2. Milo authenticates the project context and authorizes the distinct session
   create permission. Ordinary Machine read access is insufficient.
3. Fleet resolves the exact live customer Machine through its immutable
   `MachineAllocation` to one provider `MachineCapacity`, its immutable Device
   and authoritative Location. It retains the decision before reporting
   admission success.
4. Fleet creates a minimal `LocationMachineManagementSession` in the Karmada
   hub and targets the Location's exact member cluster through protected
   configuration.
5. The cell verifies that its safe MachineCapacity and Device copies match and
   that the owned-hardware adapter currently reports management availability for
   that physical identity.
6. The gateway registers a one-use outbound relay route and returns only
   non-secret rendezvous status through Karmada. Fleet reflects that status to
   the public session.
7. The connector pins the gateway certificate, presents a timestamped signed
   proof for the route and keeps the private key in memory. The first valid
   connector consumes the route.
8. The gateway exposes the virtual Redfish profile. Before each mutation, the
   location journals the exact accepted request and physical preconditions; the
   adapter then resolves local enrollment and reaches the BMC.
9. Expiry, public deletion or Machine release changes the location session to
   `Revoked`. The cell closes the stream and reports cleanup; missing confirmation
   remains `Unknown` and prevents unsafe capacity reuse.

### Authentication and authorization

Session admission uses Datum's existing human and service identity flows. IAM
defines a management-session create permission separately from ordinary
Machine read or update. Admission evaluates that permission against the
consumer project and target Machine and then resolves its private allocation;
possession of a Machine name or locally observed UID is not authorization.

The client generates an ephemeral Ed25519 key pair. The private key remains in
client memory and only the public key enters the session resource. On
connection, the gateway verifies a domain-separated signature covering at least
the one-use route ID, protocol version, timestamp, nonce and gateway audience.
The first accepted connection consumes the connection window. Replay, a changed
rendezvous or a second concurrent connector is rejected.

The admitted access profile is selected from provider-published capabilities and
the caller's grant. The caller cannot supply an arbitrary path allowlist or ask
the gateway to forward unknown Redfish resources. A narrower request can be
accepted; it cannot expand the offering.

The physical BMC sees only the gateway's location-scoped identity. BMC credentials
must never appear in the public session, its status, audit events, client errors
or relay configuration. Credential rotation does not change the customer
Machine, provider `MachineCapacity` or physical Device identity and does not
create a customer session.

### Allocation resolution and routing

Routing is a one-time admission decision, not a lookup on each Redfish request.
The customer Machine's allocation is an immutable, service-owned resource whose
generated Kubernetes name is its identity. Release is terminal; Fleet never
reactivates or retargets that allocation, so the session contract needs neither
a duplicated binding ID nor an epoch.

For Datum-owned hardware, the selected provider `MachineCapacity` has an
immutable `deviceRef`. Hardware replacement creates a new `Device` and
`MachineCapacity`; Fleet never redirects the old capacity name to another
physical asset. Credential, certificate, endpoint and private-route rotation may
change while the same enrolled Device identity remains. These implementation
changes do not require a management revision in the session.

Admission privately retains the authenticated principal, consumer and provider
project identities, the exact local source-object identities used during the
decision, the allocation resource name, selected `MachineCapacity` and
`Device`, client public key, deadlines and audit correlation. Local Kubernetes
UIDs may be used as same-API-server recreation preconditions, but they are not
authored references and never cross through Karmada.

The Karmada-delivered location session contains only the provider
`MachineCapacity` name, access profile, client public key, deadlines and desired
state. The owned-hardware adapter resolves the capacity through its immutable
Device relationship and private enrollment. Neither the public nor location
session contains a BMC endpoint, physical system identifier, Secret reference,
adapter route or Karmada member name.

If the allocation is released, the session is revoked. Hardware replacement
requires new capacity, allocation and session resources. The old endpoint cannot
be rebound to a successor, and provider discovery cannot silently redirect an
active session.

### Owned-hardware enrollment and secret custody

The Fleet management cluster intentionally has no route to location BMC
networks. Each location's Datum owned-hardware adapter maintains private
enrollment keyed by the provider `MachineCapacity` and its immutable `Device`.
Enrollment retains the BMC endpoint, physical Redfish system identifier,
qualified adapter configuration and verified physical identity. These values are
provider-backend state, not Fleet product resources.

Before a capacity can become eligible, Fleet mirrors a safe, read-only location
copy of its provider `MachineCapacity` and registered `Device` through Karmada
to the authoritative member. The provider-project objects remain authoritative;
the copies preserve names and immutable `deviceRef` but have their own local
Kubernetes metadata and no cross-cluster UID relationship. The location writes
only management observations, which Karmada returns through a constrained status
interpreter. This gives the adapter the MachineCapacity-to-Device association without
placing provider-project credentials in the cell.

BMC credentials and trust material are authoritative in the environment's
regional secret manager. External Secrets Operator may materialize them into a
protected namespace in the location cluster. Only the owned-hardware adapter or
another explicitly selected provider executor may read those Secrets. The
gateway submits typed operations to the adapter and cannot read physical BMC
credentials itself. Relay credentials, gateway transport identity and BMC
credentials are separate.

The materialized management Secret is associated with the local Device copy by
a service-owned label or adapter index, not by a customer-authored reference.
It may contain the private endpoint, physical system identifier, credential and
CA bundle. Secret names, contents and resource versions remain local provider
state and never enter MachineCapacity or session specs.

Enrollment is observational until explicitly enabled for management. The
adapter verifies that BMC-reported physical identity matches the registered
Device before reporting `ManagementAvailable`. Registration, credential
materialization or a successful observation does not authorize a power, boot,
cleaning or installation effect; the adapter executes only a retained admitted
operation.

Endpoint, certificate, credential and private-route rotation may update the
same enrollment while physical identity remains unchanged. If the enrolled BMC
reports a different Device identity, management becomes `Unknown` or `False`,
active sessions close, and mutations fail closed. The adapter never follows a
customer URL, Host header, returned BMC link or guessed Redfish identifier to
select a physical target.

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
propagates revocation to the location. Release of the customer Machine has
higher priority: it fences new operations, revokes all sessions for its
allocation and waits for active gateways to confirm teardown or records the
result as unknown. The allocator must not make the `MachineCapacity` available
to a later consumer until the release contract has accounted for those sessions.

The gateway fails closed when its allocation/session projection exceeds its
freshness limit. A partition does not turn cached authorization into an
indefinite management grant. An already admitted operation may continue only
under its retained authority and bounded expiry.

### Mutating operations

Read-only Redfish requests can be served from current BMC observations within
the session profile. Before forwarding a boot or power mutation, the gateway
retains a protected operation containing:

- the location session resource name;
- the selected `MachineCapacity` and immutable `Device` names;
- the normalized Redfish method, virtual target and exact accepted body;
- the physical target and enrolled identity resolved privately by the adapter;
- an idempotency key derived from the operation resource name;
- current session, asset and conflict preconditions; and
- execution evidence without secret material.

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
service identity. Lifecycle events record the session resource name, project,
customer Machine name, access profile, bound location, start and end times, and
terminal reason. Private same-API-server object identities may be retained in
protected admission evidence but are not customer audit fields. Audit must not
record private keys, session-local bearer values, BMC credentials, raw provider
endpoints or secret-bearing response bodies.

Every mutating request creates an operation with a generated Kubernetes name;
that name is also its audit correlation value rather than being duplicated in
spec. Audit records include its normalized operation type and outcome. Request
bodies are
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

Each resource represents one short-lived connection for one active customer
Machine. The spec is immutable; changing the Machine, key, profile or TTL
requires a new resource.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: MachineManagementSession
metadata:
  generateName: db-01-
  namespace: acme
spec:
  machineRef:
    name: db-01
  protocol: Redfish
  accessProfile: Provisioning
  clientPublicKey: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  ttl: 15m
status:
  connection:
    relayURL: https://relay.us-central-1.example.invalid
    routeID: abcdef0123456789abcdef0123456789
    gatewayAudience: fleet-redfish-iad1
    gatewayCertSHA256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    protocolVersion: redfish-v1alpha1
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

`machineRef.name` identifies the customer Machine in the authenticated project;
the consumer does not supply or manage allocation or provider identities. During
session admission, Fleet resolves the exact live Machine and its immutable
allocation before the session can become Ready or authorize an effect. If the
Machine or allocation changes before that commit, admission fails or restarts
resolution rather than following stale state. A later Machine that reuses the
same name cannot receive an already admitted session because local object
identity is checked during admission and the old allocation is terminal.

`protocol` is initially only `Redfish`. `accessProfile` refers to a versioned
platform-defined profile; arbitrary methods or paths are not accepted in the
spec. `ttl` begins no later than connection acceptance and is capped by policy.

`status.connection` is rendezvous information, not a bearer capability. Holding
the matching ephemeral private key is required to connect. The rendezvous
reaches a virtual gateway service and must not contain a BMC address. Generally
readable
status contains neither the private allocation nor credentials.

Deleting the resource requests revocation. Finalization or equivalent retained
service state keeps the private admission evidence until the location confirms
teardown or the platform records that confirmation as unknown. Deletion alone
is not teardown evidence.

### Private admission evidence and location representation

Admission durably retains private evidence outside the consumer project's
deletion domain. That persistence is a service implementation boundary, not a
customer API or a second access grant. It records the authenticated decision and
the exact immutable allocation and provider capacity selected at that time. Only
the admission service writes it.

Fleet then creates one internal `LocationMachineManagementSession` in the
Karmada hub. Its spec contains only `machineCapacityRef.name`, `accessProfile`,
`clientPublicKey`, `connectBefore`, `expiresAt`, a shorter refreshed
`freshUntil`, and monotonic `desiredState: Active|Revoked`. The authority fields
are immutable; only `freshUntil` may advance within `expiresAt`, and
`desiredState` may move from `Active` to `Revoked`. The selected location can
write conditions and non-secret rendezvous status but cannot alter spec.

```yaml
apiVersion: internal.fleet.datumapis.com/v1alpha1
kind: LocationMachineManagementSession
metadata:
  name: session-01jcc5
  namespace: machine-access-system
spec:
  machineCapacityRef:
    name: iad1-r12-srv01
  protocol: Redfish
  accessProfile: Provisioning
  clientPublicKey: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  connectBefore: "2026-11-11T19:01:00Z"
  expiresAt: "2026-11-11T19:15:00Z"
  freshUntil: "2026-11-11T19:02:00Z"
  desiredState: Active
status:
  connection:
    relayURL: https://relay.us-central-1.example.invalid
    routeID: abcdef0123456789abcdef0123456789
    gatewayAudience: fleet-redfish-iad1
    gatewayCertSHA256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    protocolVersion: redfish-v1alpha1
  conditions:
    - type: Ready
      status: "True"
      reason: RelayRegistered
      observedGeneration: 1
      lastTransitionTime: "2026-11-11T19:00:01Z"
```

The propagation policy, member-cluster name and private admission evidence are
not fields on this resource. Fleet owns those in its management-plane delivery
configuration. Fleet generates a globally unique Kubernetes name for the
location copy; it does not duplicate that name as a `sessionID` field. The
profile name is an immutable versioned contract within the API version;
incompatible profile behavior requires a new profile name or API version rather
than a mutable revision field.

Private evidence survives long enough to complete revocation, release and audit
even if the public session or customer project is deleted. Creating a session
cannot alter the Machine allocation or lifecycle.

### Permission model

The initial permission model should distinguish at least:

- reading session status;
- creating a provisioning-profile session;
- revoking one's own or project sessions;
- operator inspection of protected routing and operation evidence; and
- operator override or recovery actions.

Default viewer access does not imply session creation. Service identities receive
an explicit grant. The admission service rechecks current source capability,
Machine authority and allocation state at creation; an old IAM decision or copied
session object is not sufficient.

### API and Redfish errors

Session admission uses normal Datum API errors and conditions. Once connected,
the gateway returns Redfish status codes and extended error bodies for profile,
capability and operation failures. Errors must distinguish unsupported behavior,
conflict with another admitted operation, expired/revoked authority, unreachable
BMC and unknown execution outcome without disclosing the physical route.

## Security considerations

- **Compromised customer credential:** IAM limits session creation to authorized
  customer Machines; quotas, short connection windows and activity alerts bound
  abuse.
- **Stolen ephemeral private key:** keys live only in connector memory, have a
  short lifetime and are bound to a single one-use route, gateway audience and
  admitted location copy.
- **Compromised relay:** end-to-end encryption prevents the relay from reading or
  modifying Redfish traffic; the relay holds no allocation or BMC credential.
- **Replay or stale allocation:** the immutable session input, first-connector
  consumption, key proof, timestamps, nonce, gateway audience and current
  Karmada-delivered state are checked together. Release propagates revocation and
  blocks capacity reuse until sessions are accounted for.
- **Confused deputy:** the gateway resolves targets only from the trusted
  location session and owned-hardware enrollment, never from a customer URL,
  host header, Redfish link or BMC identifier.
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
4. **Allocation integration:** authorize sessions against real customer Machines
   and immutable MachineAllocations. Exercise concurrent allocation/release,
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
  grant and current customer Machine allocation are required for session
  creation.
- Two customer Machines, projects or overlapping classes cannot cause a session
  to bind the wrong `MachineCapacity`. Reuse of a customer Machine name cannot
  inherit a prior session or terminal allocation.
- Release and project deletion deny new operations, actively close sessions and
  prevent capacity reuse until teardown is confirmed or represented as unknown.
- A mutating request is retained before the BMC effect. Lost responses and gateway
  restarts recover the existing operation without blindly repeating it.
- Gateway, relay and BMC credential failure produce actionable but non-secret
  status and never become false readiness.
- The same public Redfish profile can be implemented by another qualified adapter
  without changing the consumer session contract.

## Open questions

- Which existing Redfish clients must be supported and what loopback TLS and
  session-login behavior they require beyond the qualified DMTF client and
  validators used by the prototype.
- Which Datum-owned BMC models and firmware revisions qualify each operation in
  the initial `Provisioning` profile.
- Whether an established Datum Connect deployment can satisfy browser, CLI and
  service automation requirements, or whether a dedicated relay profile is
  needed.
- The production Karmada topology, member-registration ownership, refresh
  interval and maximum `freshUntil` window during control-plane partitions.
- Durable storage and concurrency boundaries between Fleet admission, the
  location operation journal and provider adapters.
- Whether future regional HTTPS ingress is needed for customers that cannot run
  a connector, and how its bearer credentials would be scoped and revoked.
- Session quotas, TTLs, per-machine concurrency, request rate limits and activity
  alert thresholds for the initial pilot.
- Production ownership and deployment boundaries for the gateway, relay
  endpoint, owned-hardware adapter and optional management OpenVPN connection.

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
