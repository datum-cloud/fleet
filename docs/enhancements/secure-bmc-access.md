---
status: provisional
stage: alpha
latest-milestone: "unplanned"
---

# Secure customer BMC access

This enhancement proposes allocation-scoped customer access to a narrow Redfish
API without exposing a BMC, its management network, or its credentials directly.
It extends the provisional [Fleet design context](../architecture/design.md) and
the pinned [bare-metal provisioning proposal][fleet-provisioning]. The secure BMC
session, authorization, and execution contracts below are proposed, not implemented
or verified. Existing lifecycle APIs and synthetic validation do not establish
support for this enhancement; see [implementation status](../development/README.md).

This revision aligns with the shared decisions for [Fleet management][fleet-management],
[provider offerings][provider-offerings], and [PXE installation][pxe-installation].
Those links pin the companion heads reviewed on 2026-11-14. Their older wording
about provider MachineClass policy or direct cell-to-hub status writes is superseded
here by the agreed offering ownership and local-result reflection contract. No
migration, compatibility aliases, or runtime changes are proposed by this update.

The architecture borrows the session and data-path separation used by Datum
Compute's provisional [instance shell sessions][compute-shell]: the platform
authenticates and authorizes a short-lived session, binds it to an authoritative
placement, and then keeps the central control plane out of the request data path.
The transport and resource names below remain design proposals; adopting this
pattern does not require Fleet to reuse Compute resources or deployment
components.

## Summary

Customers with an allocated `Machine` can request a short-lived management
session for that consumer resource. Datum authenticates the caller
with the normal platform identity, authorizes the requested capability against
the current allocation, and binds the session to the consumer project, principal,
Machine identity, private MachineAllocation identity and binding epoch.

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

- reuses the shared allocator's private MachineAllocation authority;
- preserves a standard Redfish API for customer provisioning software;
- keeps BMC endpoints, credentials and management networks private;
- routes once when a session is admitted instead of globally routing every
  Redfish request;
- fences stale sessions when a machine is released or reassigned; and
- retains exact authority and inputs before power or boot effects.

## Goals

- Let an authorized human or service identity operate the machine currently
  allocated to its `Machine` through a documented Redfish interoperability
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

### Resource and capability ownership

`Device` identifies physical equipment; `MachineCapacity` represents provider
supply; `Machine` is the consumer request; `MachineAllocation` is the private,
immutable binding; `Installation` identifies an immutable operation and OS
incarnation. A managed `NodeClaim` sits above allocation and uses the same
contracts as a standalone consumer. A management session replaces none of them.

`MachineOffering` in `provider.metal.datumapis.com` owns provider capabilities,
including supported installation modes and management operations.
`MachineOfferingAssignment` controls access to new allocations. The consumer
`MachineClass` is a publisher-owned, one-hop view of the offering, not another
capability authority. `MachineProfile` contains immutable hardware requirements,
not boot, power, or session policy.

Allocation retains the accepted offering identity, revision, capabilities, and
profile requirements. Offering or assignment withdrawal blocks new allocations;
it does not retroactively remove accepted capabilities, evict an allocated
Machine, or authorize reinstall. Each session and each subsequent action still
checks current action permissions and exact input-use permissions. Explicit
safety restrictions or suspension are enforced separately from catalog access.
Neither withdrawal nor suspension may prevent service-authorized revocation,
fencing, settlement, or cleanup. Cleanup does not inherit a customer's expired
grant and must not authorize new customer work.

### Prerequisites

The consumer has:

- an allocated `Machine` with a retained `MachineAllocation`;
- current IAM permission for the session and its actions on that Machine;
- accepted allocation capabilities supporting the requested Redfish profile; and
- a supported client connector, supplied as a CLI command, sidecar or SDK
  component.

The caller supplies only its consumer `machineRef`, and an `installationRef` for
provisioning. It never supplies a physical target, BMC address, location route or
provider credential. Fleet resolves those from the private retained allocation.
Provisioning requires an admitted Installation; observation and ordinary runtime
power do not, but cannot bypass conflicts with installation or release.

### Manual access

The exact command is illustrative:

```console
$ datumctl metal machine-management proxy db-01
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

An automated provisioner first submits an immutable Installation for its Machine,
selecting an authorized source and the customer-driven Redfish operation driver.
After admission, it creates a provisioning session naming that Installation. A
connector sidecar owns the ephemeral transport
key and presents the provisioner with the same Redfish base URL for the session's
lifetime. The provisioner can then:

1. read the Redfish service root and computer-system state;
2. set a one-time PXE boot override;
3. request a supported reset or power transition;
4. follow a returned Redfish task when the operation is asynchronous; and
5. report completion and cooperate with mandatory local-disk boot and settlement.

The customer owns its installer and, in delegated mode, its PXE endpoint. The
platform remains the first DHCP/iPXE decision point; this is not permission to
answer DHCP on shared networks. Networking and IPAM own attachments, routing and
isolation. The Installation pins endpoint, trust, credentials, artifacts and
completion requirements before effects. A session cannot supply a different
endpoint or initiate an untracked install. See [shared operation ownership](#shared-operation-ownership-and-conflicts).

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
    │                              Karmada hub session projection
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

Karmada carries the admitted session from the federation hub to exactly one
member cluster at the authoritative location. The edge gateway reads and writes
only its member-cluster copy; it has no credentials for, or direct egress to,
the consumer project API or federation hub. Karmada reflects edge status onto
the hub copy, and the Fleet session controller copies only safe status fields to
the public project session. Karmada is a control and status path only: Redfish
traffic continues over the relay and never traverses Karmada or a Kubernetes API
server.

### Responsibilities

- **Fleet project API:** authenticates the user or service identity, authorizes
  the requested session, validates immutable input and exposes lifecycle status.
- **Management-session admission:** resolves the retained allocation,
  checks accepted capabilities, current permissions and conflicts, retains the
  exact binding and chooses
  the authoritative location from trusted allocation state.
- **Karmada federation:** propagates a protected edge-session copy to exactly
  one retained member cluster and reflects that cluster's status. It is not the
  Redfish request path and does not choose a different location.
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
Machine read or update. Admission evaluates that permission against the
consumer project and target Machine and then resolves the private binding;
possession of a Machine name or identity is not authorization.

The client generates an ephemeral Ed25519 key pair. Only the public key enters
the proposed session resource. On connection, the gateway verifies a signature
covering at least the session UID, protocol version, connection timestamp and
gateway audience. The first accepted connection consumes the connection window.
Replay, a changed route or a second concurrent connector is rejected.

The admitted access profile is selected from the allocation's accepted offering
capabilities and current caller permissions, subject to explicit safety policy.
Each request rechecks current action and input-use authorization through bounded,
fresh projections; missing or stale authorization fails closed. The caller cannot
supply an arbitrary path allowlist or ask
the gateway to forward unknown Redfish resources. A narrower request can be
accepted; it cannot expand the offering.

The physical BMC sees only the gateway's location-scoped identity. BMC credentials
must never appear in the public session, its status, audit events, client errors
or relay configuration. Credential rotation does not change Machine identity or
create a customer session.

### Binding and routing

Routing is a one-time admission decision, not a lookup on each Redfish request.
The protected session binding retains:

- authoritative consumer and provider Project identities;
- requesting principal identity;
- consumer Machine name and authoritative identity;
- private MachineAllocation identity and accepted offering revision;
- MachineCapacity and Device identities, and Installation identity when required;
- allocation binding identifier and epoch;
- selected location identity and location-service revision;
- selected provider target and adapter identity;
- admitted access-profile revision;
- client public key, creation time, connect deadline and expiry; and
- session and operation correlation identifiers.

The consumer-visible object names the consumer Machine, but does not reveal the
provider project, private capacity or Device, BMC endpoint or adapter route. The
location gateway resolves
the retained target through its private provider records and verifies that every
request still matches the projected binding epoch.

Project, hub and cell Kubernetes UIDs are local evidence, not portable authored
references. Protected correlation records preserve the authoritative identities
and each projected object's local identity without confusing those lifetimes.

If an allocation moves, is released or is replaced, a new session is required.
The old endpoint cannot be rebound to the successor. A location-revision change
requires controlled handover or session replacement; mutable discovery must not
silently redirect an active session.

### Karmada federation

Fleet follows Compute's hub-and-cell session pattern while keeping Fleet's
allocation authority separate from Karmada placement:

1. The consumer creates a `MachineManagementSession` in its project control
   plane. The Fleet controller resolves the current allocation and commits the
   protected session binding before federation.
2. The controller renders an internal edge-session resource in
   `coordination.fleet.datumapis.com`, in a service-owned federation-hub namespace.
   This copy contains the exact retained binding and
   adapter route needed by the selected edge, but no BMC credential. It also
   carries a control-plane-issued `validUntil` authority deadline that the edge
   cannot extend. It is not a consumer API object.
3. Fleet resolves the retained location route to one Karmada member-cluster
   identity. A generated or service-owned `PropagationPolicy` names that exact
   cluster; Karmada is not allowed to schedule the session among candidate
   locations. Before publishing connection details, Fleet verifies that the
   resulting `ResourceBinding` contains exactly that one member cluster.
4. Karmada propagates the edge-session copy to the selected member. The gateway
   validates the binding epoch and private target, claims the session, and writes
   connection and lifecycle status only to the member-cluster copy.
5. A `ResourceInterpreterCustomization` reflects member status and aggregates it
   onto the hub copy. Aggregation accepts status only from the cluster recorded
   by Fleet on the hub copy, ignores claims from every other cluster, and never
   replaces an established endpoint with a different endpoint identity.
6. The Fleet controller watches the hub copy, verifies the member cluster and
   binding, operation revision, fencing token and evidence freshness, then projects
   the permitted connection and lifecycle fields
   to the public project session.

The aggregation rule is monotonic. A default or empty member copy cannot erase a
claim; an older status cannot replace a terminal result; and status from another
member cannot claim, refuse, complete or revoke the session. Interpreter fixtures
must cover hostile-first ordering, a hostile-only status, refusal from the wrong
member, endpoint replacement, no reporting member, terminal status and cleanup
confirmation.

Revocation updates the protected hub copy before deleting it. Karmada propagates
the revoke marker to the edge; the gateway closes the relay session, stops
accepting operations, and records terminal status plus teardown confirmation on
its local copy. Karmada reflects that confirmation, after which Fleet may delete
the hub copy and release the public finalizer. Deleting the hub copy first is
unsafe because Karmada would remove the edge copy before its final status could
be observed. If confirmation does not arrive by the cleanup deadline, Fleet
records `CleanupUnconfirmed` and retains recoverable session, operation and cleanup
custody outside the consumer deletion domain. It must not discard the last
recoverable projection or journal merely to meet a deletion deadline. Hub-copy
deletion requires confirmed teardown or durable transfer of unresolved obligations
to a recovery record with a fenced recovery path; it never proves settlement.

Fleet also checks that the selected member continues reporting in the
`ResourceBinding` aggregated status. A deregistered edge, removed propagated
copy, or route that no longer reports ends the session as location/agent lost;
the last status retained on the hub is not proof that the edge is still serving.
The edge rejects new requests after `validUntil` unless a newer hub-authorized
projection renews it, which bounds authority during a Karmada or control-plane
partition. Machine release still fences new central admission immediately and is
not complete until session teardown, installer settlement and required cleanup
are verified. Expired authority blocks new effects but cannot stop a running
installer or prove that a previously accepted BMC effect ended. Unknown outcomes
retain custody and block reuse.

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
propagates revocation to the location. Release of the Machine has higher
priority: it fences new operations, revokes all sessions for that binding and
waits for active gateways to confirm teardown or records the result as unknown.
The allocator must not return MachineCapacity to supply until the release
contract verifies session teardown, operation settlement and required cleanup.

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

### Shared operation ownership and conflicts

An Installation is mandatory whenever Fleet enables provisioning PXE, including
customer-driven Redfish boot overrides. Session admission resolves
`installationRef` to the exact admitted operation for the same Machine and
allocation. Session creation does not admit an Installation, consume another
predecessor, or create another operation. Ordinary observation and runtime power
may omit that reference, but every effect uses the same asset-level journal,
conflict admission and effect-boundary fencing.

The immutable Installation admission selects exactly one operation driver:
Fleet automatic execution or customer-driven Redfish. Source mode (`ManagedImage`
or `DelegatedPXE`) describes the installer, not an independent power/boot writer;
only qualified source/driver combinations may be offered. In automatic mode the
Fleet executor owns step progression and a customer session is observational.
In customer-driven mode the gateway admits bounded customer steps into the same
Installation journal; an automatic controller must not concurrently set boot or
power state. Persistent Machine power intent also waits behind the operation.
Driver takeover requires explicit recovery authority, retained inputs, and proof
that the previous writer is fenced at the BMC, boot, network and installer effect
boundaries. A lease, session expiry or changed fencing number alone is not proof.

| Requested action | Shared admission rule |
| --- | --- |
| Observation | May coexist when currently authorized; cannot mutate boot or infer readiness from session health. |
| PXE override or provisioning power | Requires the associated admitted Installation, selected driver, exact allowed step and input permissions; another Installation or session cannot race it. |
| Ordinary runtime power/reset | No Installation required when no conflicting operation holds the asset; reject conflicts rather than interrupt installation or settlement. |
| Another install, rescue, or boot change | Conflicts with active or uncertain installer custody, even after its connection closes. A boot override cannot be used as an installation escape hatch. |
| Release, safety stop or recovery | Fence new consumer steps first; retain an ordered revocation/settlement/cleanup operation, never race a second effect writer. |

#### Initial installation and predecessor

Use the [shared predecessor contract](machine-lifecycle/README.md#installation):
`expectedPredecessor: {type: None}` is valid only for a verified blank allocation
that has never admitted an install. Otherwise name the exact current Installation;
Fleet resolves and pins its incarnation. Admission atomically consumes the
precondition and retains authority before effects. Competing requests lose with
`InstallationPredecessorConflict`; reconnects and duplicate requests recover the
same retained operation, not a fresh predecessor.

Where a Machine requires initial installation, only that retained initial request
may execute until the initial gate succeeds. An allocation-only Machine has no
initial gate, but a later PXE operation still requires an Installation. Session
readiness, power observations, deletion or recreation cannot complete or reset the
initial gate. Active or unknown work withholds a usable successor. Failure or
cancellation allows a successor only after fencing and verified no-effect/prior-
state-intact evidence; partial or uncertain effects require recovery or release.

#### Completion, connection closure and release

The connection lifetime is not the boot or installer authority lifetime. Closing,
expiring or revoking a session denies new customer steps; it does not mark its
Installation complete, free its conflict reservation, or prove the installer
stopped. The retained operation and cleanup authority survive until verified
settlement, including after consumer project deletion.

Completion follows the PXE contract: validate the exact operation and callback,
revoke boot/delegation and temporary credentials, fence provisioning access
through the networking owner, settle the installer and outstanding BMC effects,
and verify required local-disk boot and handoff evidence. Managed-image success
requires the retained image/recipe evidence. Delegated success reports the
customer's result and verified delegation/boot settlement, not platform validation
of an opaque OS. A customer callback, successful Redfish task, disk override or
disconnected stream alone proves none of this.

The location writes evidence only to its local coordination object. Karmada
reflects selected-cell results; management validates target, revision, fencing,
freshness and all completion requirements before projecting Installation readiness
and durable current-incarnation/initial-gate state. There are no direct location
writes to the hub or project APIs.

Release fences all new consumer effects, revokes management/PXE/console sessions,
settles in-flight installation and power work, verifies network fencing and
provider sanitization/restoration, and only then returns capacity to supply.
Failed, missing or unknown settlement retains allocation and cleanup custody or
quarantines capacity. Neither session TTL nor recording `Unknown` authorizes reuse.

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
service identity. Lifecycle events record the session UID, project, Machine identity,
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

Each resource represents one short-lived connection to one immutable allocation
binding. The spec is immutable; changing the Machine, Installation, key, profile
or TTL requires a new resource. The session is namespaced and shares its Machine
and Installation namespace. Project scope comes from authenticated API context;
a namespace is not a project identity. Admission retains the Namespace lifetime
alongside the resolved object identities.

```yaml
apiVersion: metal.datumapis.com/v1alpha1
kind: MachineManagementSession
metadata:
  generateName: db-01-
  namespace: default
spec:
  machineRef:
    name: db-01
  installationRef:
    name: db-01-os-1
  protocol: Redfish
  accessProfile: Provisioning
  clientPublicKey: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123
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

`machineRef.name` resolves in the session namespace of the authenticated project;
the consumer does not supply or manage allocation identities. During session
admission, Fleet resolves the name and retains the exact Machine and
MachineAllocation identities, binding identifier and epoch before the
session can become Ready or authorize an effect. Admission requires an allocated
Machine in a lifecycle state that permits the requested action, including the
retained initial Installation while activation is gated. If the Machine or binding
changes before that commit,
admission fails or restarts resolution rather than following stale state. A later
Machine that reuses the same name cannot receive an already admitted session.

`installationRef` is mandatory for the Provisioning profile and resolves to an
already admitted Installation in the same namespace on this allocation with the selected customer-driven
Redfish driver. Its exact incarnation and operation identity enter the protected
binding. Observation and ordinary runtime power profiles omit it; omission cannot
authorize PXE or override conflicts. These are proposed fields, not supported CRDs.

`protocol` is initially only `Redfish`. `accessProfile` refers to a versioned
platform-defined profile; arbitrary methods or paths are not accepted in the
spec. `ttl` begins no later than connection acceptance and is capped by policy.

`status.connection` is rendezvous information, not a bearer capability. Holding
the matching ephemeral private key is required to connect. The target is a
virtual gateway service and must not contain a BMC address. Generally readable
status contains neither the private allocation binding nor credentials.

Deleting the resource requests revocation. Finalization or equivalent retained
service state keeps the private session binding until the location confirms
teardown. If confirmation is unknown, durable recovery custody retains the binding
and unresolved obligations. Deletion alone is not teardown evidence.

### Protected session binding

Admission creates a private service-owned binding record outside the consumer
project's deletion domain. It contains the routing and fencing fields listed in
[Binding and routing](#binding-and-routing). Only the session admission service
can create it; the location gateway can observe only the sessions routed to its
location and can update only their location-local status.

This record is intentionally separate from `Machine.status`. A public
session may be short-lived or deleted while its audit, revocation and release
obligations remain. The allocator-owned MachineAllocation remains the binding
authority; creating a session cannot alter allocation or installation lifecycle.

### Permission model

The initial permission model should distinguish at least:

- reading session status;
- creating a provisioning-profile session;
- revoking one's own or project sessions;
- operator inspection of protected routing and operation evidence; and
- operator override or recovery actions.

Default viewer access does not imply session creation. Service identities receive
an explicit grant. The admission service rechecks accepted allocation capabilities,
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
  Machines; quotas, short connection windows and activity alerts bound abuse.
- **Stolen ephemeral private key:** keys live only in connector memory, have a
  short lifetime and are bound to one session, gateway audience and allocation.
- **Compromised relay:** end-to-end encryption prevents the relay from reading or
  modifying Redfish traffic; the relay holds no allocation or BMC credential.
- **Replay or stale allocation:** session UID, Machine identity, MachineAllocation identity, binding
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
2. **Location transport prototype:** establish a Karmada hub and member cluster,
   propagate a synthetic bound session to exactly that member, reflect its status,
   establish outbound relay connectivity, and prove direct client-to-location
   traffic without placing Karmada or the central API in the Redfish data path.
3. **Physical lab qualification:** connect the gateway to disposable Supermicro
   hardware over the management OpenVPN path. Verify correct-host identity,
   one-time PXE boot, supported power actions, task recovery, credential rotation,
   rate limits and network isolation.
4. **Allocation integration:** authorize sessions against real Machines and
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
- Karmada propagates each protected session to exactly the retained member
  cluster; another member cannot claim, refuse, complete or replace its endpoint.
- The edge gateway has no project-control-plane or federation-hub credentials,
  and an expired projected authority is not usable during a partition.
- The customer cannot discover or reach the BMC address, management VPN, another
  machine, an OEM endpoint or an unlisted Redfish field/action through the gateway.
- Human and service identities use normal Datum authentication; an explicit IAM
  grant and current Machine binding are required for session creation.
- Two Machines, projects or overlapping classes cannot cause a session to bind the
  wrong allocation. A reused Machine name or stale binding epoch is rejected.
- Release and project deletion deny new operations, actively close sessions and
  prevent capacity reuse until teardown, installer settlement and cleanup are
  verified. Unknown outcomes retain custody, never readiness or free supply.
- Offering withdrawal blocks new allocation but leaves accepted capabilities
  intact. Current action/input permission revocation and explicit suspension block
  affected new effects without blocking cleanup.
- Provisioning without an admitted Installation, a mismatched operation, or a
  competing automatic/customer driver causes no effects. Concurrent boot, power,
  initial install, reinstall and release exercise one conflict contract.
- Disconnect, expired sessions, lost callbacks, stale predecessor, and failed or
  unknown installation cannot clear the initial gate or admit a successor.
- Customer completion without verified boot revocation, installer settlement and
  required local-disk evidence cannot become Installation readiness.
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
- The exact Karmada policy ownership, interpreter packaging, freshness limit and
  behavior during federation-hub or member-cluster partitions.
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
- [Datum Compute session federation controller][compute-session-federation]
- [Datum Compute Karmada session status interpreter][compute-session-interpreter]
- [Pinned Fleet infrastructure management proposal][fleet-proposal]
- [Pinned Fleet bare-metal provisioning proposal][fleet-provisioning]

[compute-session-protocol]: https://github.com/datum-cloud/compute/blob/fe85e3c619d2a774fc9ad45b2d362ced32a43c28/internal/consolesession/protocol.go
[compute-session-federation]: https://github.com/datum-cloud/compute/blob/fe85e3c619d2a774fc9ad45b2d362ced32a43c28/internal/controller/instanceconsolesession_controller.go
[compute-session-interpreter]: https://github.com/datum-cloud/compute/blob/fe85e3c619d2a774fc9ad45b2d362ced32a43c28/config/components/federation/instanceconsolesession-interpreter.yaml
[compute-shell]: https://github.com/datum-cloud/compute/blob/fe85e3c619d2a774fc9ad45b2d362ced32a43c28/docs/enhancements/instance-shell-sessions/README.md
[compute-tunnel]: https://github.com/datum-cloud/compute/blob/fe85e3c619d2a774fc9ad45b2d362ced32a43c28/internal/consoleclient/tunnel.go
[fleet-proposal]: https://github.com/datum-cloud/infra/tree/51781e2456367be82281e1443ca56d0686311133/docs/enhancements/fleet-management
[fleet-provisioning]: https://github.com/datum-cloud/infra/blob/51781e2456367be82281e1443ca56d0686311133/docs/enhancements/fleet-management/bare-metal-provisioning.md
[redfish-resources]: https://redfish.dmtf.org/essentials
[redfish-spec]: https://www.dmtf.org/sites/default/files/standards/documents/DSP0266_1.22.1.pdf
[fleet-management]: https://github.com/datum-cloud/fleet/blob/16e7cb2a268cf20bc58d490493b5a3fef9237027/docs/enhancements/fleet-management/README.md
[provider-offerings]: https://github.com/datum-cloud/fleet/blob/031477d011090ea6ec378d526e975bbfae07a060/docs/enhancements/profile-access/README.md
[pxe-installation]: https://github.com/datum-cloud/fleet/blob/f1ccc24ac66d5d930e3264c7ee856a7e1cd67254/docs/enhancements/pxe-installation/README.md
