# Device power operations

[Operations guide](README.md) · [BMC discovery](bmc-discovery.md) ·
[Redfish lab](redfish-lab.md)

Staff can power an unallocated Device on or off, or restart it, by creating a
`DevicePowerOperation` in the provider project. A dispatcher in the site
checks the request and starts one short-lived worker. The worker sends at most
one Redfish `ComputerSystem.Reset` to the exact server named. The operation
then ends as `Succeeded`, `Failed` or `Unknown`, and nothing retries it.

This is a prototype of the
[device power proposal](https://github.com/datum-cloud/fleet/pull/173), which
is not yet merged into this branch. It has run end to
end against emulated BMCs only. No real server or vendor BMC has received a
command.

## Implementation status

| Proposal part | State |
| --- | --- |
| Phase 1: discovery records offered actions | **Implemented and verified** against the lab BMCs. `status.discovery.inventory.systems[].resetTypes`, read with `GET`s only. |
| Phase 2: API and roles | **Implemented.** Opt-in component `config/components/device-power`; no root includes it. The CRD rules and role split are verified in a Milo v0.32.5 project API. The requester check is verified only on a standard API server: [Milo does not enforce it](#the-requester-check-is-not-enforced-in-milo). |
| Phase 3: dispatcher and worker | **Implemented and verified** against the lab BMCs ([run record](#end-to-end-run-record)). |
| Phase 4: staff UI | **Not built.** Staff use the API. |
| Phase 4: one real server | **Not done.** Restart and power cycle stay unqualified on real hardware. |
| Phase 5: allocator gate | **Not built.** The window between the dispatcher's last capacity check and the command is still open; see [known gaps](#known-gaps). |
| Production readiness: monitoring | **Not built.** There are no metrics or alerts yet. |

## Who writes what

| Surface | Writer | Content |
| --- | --- | --- |
| `DevicePowerOperation.spec` | Staff with the power operator role | The request. Fixed at creation. |
| `DevicePowerOperation.status` | The site dispatcher only | Conditions, the result, the time the one command was sent, power before and after. |
| Stage record, ConfigMap `pwr-<operation UID>` in the site | Inputs: the dispatcher, once. Progress: the operation's worker. | The exact inputs and how far the worker got. |
| Device lock, Lease `pwr-lock-<hash>` in the site | The dispatcher | Which operation holds the Device. |
| `Device.status` | BMC discovery only, unchanged | What the BMC reports, now including offered reset types. |

The worker holds no Milo credential. The dispatcher holds no BMC credential.

## How one operation runs

1. Staff create the operation. The CRD fixes the spec. An admission policy
   requires `spec.requester` to be the creating user, but Milo does not yet
   enforce it; see [known gaps](#the-requester-check-is-not-enforced-in-milo).
2. The dispatcher runs the [acceptance checks](#acceptance-checks). A refusal
   ends the operation as `Failed`, and nothing reaches the site or the BMC.
3. The dispatcher takes the Device's site Lease and marks the operation
   `Accepted`. A second operation on the Device is refused while the first
   holds the Lease.
4. The dispatcher re-reads capacity from Milo, bypassing its cache. It writes
   the stage record with the exact inputs and the time of that check, then
   creates the worker's ServiceAccount, Role, RoleBinding and Job, and marks
   the operation `Executing`.
5. The worker claims the record: it moves the stage from `Pending` to
   `Claimed` with an optimistic update. A second worker for the same
   operation loses that update and exits without contacting the BMC.
6. The worker refuses with `Stale` if it started more than two minutes after
   the capacity check. It then checks its mounted credential, connects with
   the approved TLS key, finds the one System with the expected UUID, checks
   the action is offered, and reads the power state.
7. The worker records stage `Sending`, then sends one `ComputerSystem.Reset`.
   If it cannot record `Sending`, it sends nothing.
8. The worker observes the System and records the result. The dispatcher
   copies it to the operation's status and releases the Lease.

If the worker Job ends without a result, the dispatcher fences the record:
it moves the stage to `Abandoned` with the same optimistic lock. A lost worker
process can then no longer record `Sending`, so it cannot send. A record that
reached `Sending` or `Sent` ends as `Unknown`; an earlier stage ends as
`Failed`.

### Acceptance checks

The dispatcher refuses with `result: Failed` and sends nothing when any of
these holds, checked in this order:

| Reason | When |
| --- | --- |
| `Expired` | The operation is more than five minutes old and was never accepted. |
| `DeviceNotFound` | The Device does not exist in the namespace. |
| `DeviceRetired` | `spec.lifecycle` is `Retired`. |
| `CapacityBacked` | A MachineCapacity in the namespace names the Device. Checked again just before the worker starts. |
| `OperationInProgress` | Another accepted operation on the Device has no result, or holds the Device's Lease. |
| `SystemUUIDNotRecorded` | The Device has no `spec.identity.systemUUID`. |
| `SystemUUIDMismatch` | `spec.expectedSystemUUID` differs from the Device's system UUID. |
| `DiscoveryNotCurrent` | Discovery has no TLS and inventory observation of the current `spec.bmc.endpoint`, the inventory is older than `discoveryMaxAge`, `Discovered` or `IdentityMatched` is not `True` for the current generation, or `PublicKeyUnique` is not yet known. |
| `SystemNotObserved` | Discovery did not see exactly one System with the expected UUID. |
| `SharedPublicKey` | `PublicKeyUnique` is `False`. |
| `ResetTypeNotOffered` | The observed System does not list the action in `resetTypes`. |

### Worker reasons

| Result | Reason | Meaning |
| --- | --- | --- |
| `Succeeded` | `RequestedStateObserved` | `On` or `Off` was observed within `observeTimeout`. The message says when the BMC's response was lost first. |
| `Succeeded` | `ResetObserved` | The System is `On` and its `LastResetTime` is later than before the command. |
| `Succeeded` | `AlreadyInState` | The System was already in the requested state. Nothing was sent. |
| `Failed` | `Stale`, `CredentialNotFound`, `CredentialInvalid`, `PinNotApproved`, `CredentialMismatch`, `PinMismatch`, `CredentialRejected`, `BMCUnreachable`, `SystemUUIDMismatch`, `ResetTypeNotOffered`, `PowerStateChanging`, `SystemOff` | Refused before any command. Nothing was sent. |
| `Failed` | `CommandRefused` | The BMC answered the command with a 4xx and the power did not change. |
| `Unknown` | `PowerChangedAfterRefusal` | The BMC refused the command, but the power changed. |
| `Unknown` | `OutcomeNotObserved` | A command may have been sent, and the expected state or reset was not observed in time. |
| `Unknown` | `IdentityChanged` | The BMC reported another server after the command. |
| `Failed` | `WorkerNotStarted`, `WorkerLost` | The worker ended before it recorded `Sending`. Nothing was sent. |
| `Unknown` | `WorkerLost` | The worker ended after it recorded `Sending`. |
| `Unknown` | `RecordLost` | The stage record of a started operation is missing or unreadable. |

A 5xx, a timeout or a broken connection on the command counts as a lost
response, because the BMC may have applied it. A command is never resent.

## Authorization

### Milo

`config/components/device-power` adds two IAM roles:

| Role | Grants |
| --- | --- |
| `fleet.datumapis.com-device-power-operator` | Create, get, list and watch operations; get, list and watch Devices. No update, patch, delete or status write. |
| `fleet.datumapis.com-device-power-dispatcher` | Get, list and watch operations, Devices and MachineCapacities; write operation status. No create, update or delete. |

The `fleet-device-power-requester` ValidatingAdmissionPolicy denies a create
whose `spec.requester` differs from the request's username. A standard API
server enforces it. Milo v0.32.5 does not; see
[known gaps](#the-requester-check-is-not-enforced-in-milo).

### Site

`config/base/device-power` runs the dispatcher with this Role in its
namespace:

| Resource | Verbs |
| --- | --- |
| ConfigMaps, Leases | get, list, watch, create, update, delete |
| Jobs | get, list, watch, create |
| ServiceAccounts, Roles, RoleBindings | get, create |

It has no Secret or Pod permission. RBAC still lets it create a Job that
mounts any Secret in the namespace, so two ValidatingAdmissionPolicies pin its
writes. Their bindings select namespaces labelled
`fleet.datumapis.com/device-power-site=true`. Without the label the
dispatcher's writes are unchecked. The selector is required: the
dispatcher policy's parameter is looked up before its match conditions, so an
unscoped binding denies every write in a namespace without the parameter,
including system leader election.

- `fleet-device-power-dispatcher-writes` matches only the dispatcher's
  ServiceAccount. Every object is named `pwr-` and labelled
  `app.kubernetes.io/part-of=fleet-device-power`. A worker Job runs one Pod with
  `backoffLimit: 0`, as its own ServiceAccount, without host namespaces, as
  non-root and unprivileged, with the image in the
  `fleet-device-power-worker-policy` ConfigMap and the command
  `/fleet-device-power worker`. It mounts exactly one volume: the Secret
  `bmc.<device namespace>.<device name>` named by its own annotations. A worker
  Role grants only get and update on its own stage record. A RoleBinding binds
  only its own Role to its own ServiceAccount. A stage record's inputs cannot
  change. Deletes are limited to stage records and Leases.
- `fleet-device-power-worker-writes` matches the per-operation worker
  ServiceAccounts. A worker may only update its own stage record, and only its
  progress.

The worker reads its credential from the mounted volume, not the API, so its
token cannot read any Secret. Deleting a stage record removes the worker
objects it owns.

## Credentials

A worker mounts one Secret, named after the Device:
`bmc.<device namespace>.<device name>`, in the namespace where discovery and
the dispatcher run. It is the same Secret discovery reads, with the same
labels, annotations and keys; see [credential Secrets](bmc-discovery.md#credential-secrets).
Discovery still selects Secrets by annotation, so this name is required for
power operations only. A name longer than 253 characters cannot be used.

The worker requires the Secret's approved `tlsPublicKeySHA256` to equal the
key discovery last observed for the Device. A missing Secret ends the
operation as `Failed` with `CredentialNotFound`.

## Deployment

Apply `config/components/device-power` to the Milo control plane that serves
the provider project, next to `config/milo`. Without a dispatcher, operations
are never accepted and have no effect.

Apply `config/base/device-power` in the site namespace that holds discovery's
credential Secrets. The deploying repository supplies:

| Input | Where |
| --- | --- |
| Namespace | Kustomize `namespace:`. Label it `fleet.datumapis.com/device-power-site=true`; the site policies apply only in labelled namespaces. |
| Image digest | Kustomize `images:` for `ghcr.io/datum-cloud/fleet`, `workerImage` in `config.yaml`, and `workerImage` in the `fleet-device-power-worker-policy` ConfigMap. All three must match. |
| Milo kubeconfig | Secret `fleet-device-power-milo-kubeconfig`, key `kubeconfig`, bound to the dispatcher role. |
| Configuration | A patch of `config.yaml`. |

`config.yaml`, read with strict decoding:

| Field | Default | Meaning |
| --- | --- | --- |
| `project` | required | The Milo project the kubeconfig addresses. |
| `miloKubeconfig` | required | Path of the Milo project kubeconfig. |
| `deviceNamespaces` | required | Project namespaces whose operations are served. |
| `workerImage` | required | The image worker Jobs run. |
| `workerNamespace` | `$POD_NAMESPACE` | Site namespace of records, workers and credential Secrets. |
| `localKubeconfig` | in-cluster | Kubeconfig of the site cluster. |
| `discoveryMaxAge` | `2h` | Oldest discovery inventory read that acceptance allows. Keep it above discovery's `interval`. |
| `observeTimeout` | `3m` | How long a worker watches for the result after its command (10s to 10m). |
| `requestTimeout` | `20s` | Each HTTP request a worker makes to the BMC. |
| `retention` | `24h` | How long a finished operation's site objects are kept. |
| `healthProbeAddress` | off | Serves `/healthz` and `/readyz`; the base sets `:8081`. |

The dispatcher runs as one replica without leader election. A second replica
is safe: acceptance, the Lease, the stage record and the Job are each written
once or with an optimistic lock. The run record shows two dispatchers starting
one worker.

## End-to-end acceptance

```sh
task device-power:acceptance
# equivalent:
hack/device-power/run.sh
```

The harness needs Go, Python 3, Docker, kind, kubectl, kustomize, OpenSSL and
curl, the running task-owned Colima profile `fleet-acceptance-tcg`, and
network access to GitHub, PyPI, Docker Hub, `ghcr.io` and `registry.k8s.io`.
It refuses to start while another `fleet-mp-`, `fleet-local-` or `fleet-dp-`
container runs. `--keep` leaves the cluster running after a pass, and a failed
run always keeps it. `--skip-unit-tests` skips the test gate, for diagnosis
only.

Exit 0 means every phase passed. The harness then deletes its kind cluster,
image tags, tokens, passwords, keys and kubeconfigs. Exit 1 means a phase
failed; the cluster and the private state are kept. Only the state
directory's `evidence/` subdirectory is shareable.

### What runs

One kind cluster (node v1.35.0) holds:

- A Milo v0.32.5 project API with its own etcd. The harness installs the
  inventory, metal and `DevicePowerOperation` CRDs and the requester policy
  in project `provider`. Static tokens identify staff, a viewer, the
  dispatcher and discovery. Kubernetes RBAC with the same grants stands in
  for the Milo IAM roles.
- Site namespace `fleet-power-site`, which enforces the `restricted` Pod
  Security profile. It runs discovery from `config/base/bmc-discovery`, the
  dispatcher and its admission policies from `config/base/device-power`, and
  holds one credential Secret per Device.
- Two lab BMCs: the unmodified DMTF Redfish Interface Emulator 1.2.2 with the
  [lab adapter](redfish-lab.md), each serving one System over TLS with its own
  key. Power is the adapter's simulated libvirt (`fake_virsh.py`). No guest
  boots.

Device A backs no capacity. Device B backs a MachineCapacity.

### Phases

| Phase | What it proves |
| --- | --- |
| `preflight` | Tools present; no other Fleet runtime in the profile. |
| `unit-tests` | `go test -race` of the power, Redfish, discovery and inventory API packages, including the envtest API-server tests. |
| `images` | Binaries built from the checkout; the lab BMC image built from the verified DMTF archive; the adapter self-test passes. |
| `cluster`, `milo`, `site` | The topology above, applied from the shipped manifests with only namespace, image and config overlays. |
| `inventory` | Discovery reads both BMCs over pinned TLS, matches identity, and records `resetTypes` `[On, ForceOff, ForceRestart]`, with `GET`s only. |
| `admission` | In Milo: a user without the operator role is forbidden; a spec change is rejected; staff cannot write status or delete. A forged requester is **admitted** and recorded as the known gap. The probe targets the capacity-backed Device, so the dispatcher refuses it. |
| `power` | The scenarios in the run record, each with the result staff see and the number of `ComputerSystem.Reset` requests in the BMC's own audit log. |
| `security` | `kubectl auth can-i` as each site identity: the dispatcher cannot read Secrets or create Pods; a worker cannot get or list Secrets and can update its own record. |
| `evidence-scan` | No token, password or private key appears in `evidence/`. |

The harness sets `observeTimeout: 45s` and `requestTimeout: 10s` to keep the
run short. Lab faults withhold a response for 25 seconds, past the request
timeout.

### End-to-end run record

`hack/device-power/run.sh` (the command behind `task device-power:acceptance`)
exited 0 on 2026-11-24, on a fresh cluster in the `fleet-acceptance-tcg`
profile. All 11 phases passed in 541 seconds; the `power` phase took 368. The
Fleet image held `fleet-device-power` with SHA-256 `bc042c1f…` and
`fleet-bmc-discovery` with `0a1eea91…`, built from this branch.

Discovery read both lab BMCs over pinned TLS, matched their identity, and
recorded `[On, ForceOff, ForceRestart]`, with `GET`s only.

| Scenario | Result | Reason | Resets at the BMC | Worker Jobs |
| --- | --- | --- | --- | --- |
| `On`, with the dispatcher Pod deleted right after the request | `Succeeded` | `RequestedStateObserved` | 1 | 1 |
| `ForceRestart` | `Succeeded` | `ResetObserved` | 1 | 1 |
| `ForceOff` | `Succeeded` | `RequestedStateObserved` | 1 | 1 |
| `PowerCycle`, which the BMC does not offer | `Failed` | `ResetTypeNotOffered` | 0 | 0 |
| `On` with another Device's system UUID | `Failed` | `SystemUUIDMismatch` | 0 | 0 |
| `ForceRestart` of the capacity-backed Device | `Failed` | `CapacityBacked` | 0 | 0 |
| `On` after the BMC starts reporting another server (`swap-identity`) | `Failed` | `SystemUUIDMismatch` | 0 | 1 |
| `On`, response withheld 25s after the effect | `Succeeded` | `RequestedStateObserved`, "after the BMC's response was lost; the command was not resent" | 1 | 1 |
| `ForceOff`, request withheld 25s and never applied; a cloned worker Job started mid-observation | `Unknown` | `OutcomeNotObserved` | 1 | 1, and the clone exited without claiming |
| `On` while the next operation on the Device runs | `Failed` | `OperationInProgress` | 0 | 0 |
| `ForceOff`, request withheld; the worker Pod force-deleted after it recorded `Sending` | `Unknown` | `WorkerLost` | 1 | 1, never replaced |
| `ForceRestart` with a second dispatcher Deployment running | `Succeeded` | `ResetObserved` | 1 | 1 |
| `ForceRestart` on a BMC that hides `LastResetTime` | `Unknown` | `OutcomeNotObserved` | 1 | 1 |

The capacity-backed Device's BMC received no reset in the whole run. In
`admission`, the viewer was forbidden, the spec change was rejected and staff
could neither write status nor delete. The forged requester was admitted,
which is the [known gap](#the-requester-check-is-not-enforced-in-milo). In
`security`, the dispatcher could not get Secrets, create Pods or delete Jobs,
and in Milo could neither create operations nor patch Devices. No worker could
get or list Secrets through the API, and each could update its own record.
Nine evidence files held no token, password or private key. Teardown removed
the cluster, both image tags and every private credential file.

Earlier attempts on the same day found these, each fixed before the pass:

- The site policy binding denied every Lease update in namespaces without its
  parameter, including kube-controller-manager's leader election. The
  bindings now select the labelled site namespace, and an envtest covers it.
- Milo does not enforce the requester policy, as described in known gaps.
- Harness defects: client-side validation against Milo's OpenAPI document,
  binaries copied without the execute bit, counting resets before the lab
  wrote a withheld request's audit line, and reading a terminating lab Pod.

## Decisions where the proposal left a choice

| Open question | Prototype choice |
| --- | --- |
| Override for capacity-backed Devices | None. `CapacityBacked` has no exception. |
| Lost response, then the right state | `Succeeded`, as the proposal proposes. The message records that the response was lost. The stricter `Unknown` remains a one-line change. |
| Restart evidence | Restart and power cycle are built and run only against the emulator. `ResetObserved` needs a `LastResetTime` later than before the command, on the BMC's own clock. A BMC that reports none ends as `Unknown`. Real hardware stays unqualified until the phase 4 test. |
| Graceful actions | Excluded. The CRD admits only `On`, `ForceOff`, `ForceRestart` and `PowerCycle`. |
| Retention | Operations in Milo are kept; nothing deletes them, and staff cannot. Site records and worker objects are deleted `retention` after the result. |
| Unserved operations | Not expired. An operation no dispatcher serves stays without status. A dispatcher that starts later refuses it as `Expired` if it is more than five minutes old. |
| Credential naming | Derived from the Device: `bmc.<namespace>.<device>`, mounted as a volume. No dispatcher credential read and no broker. |
| Installation server list | Unchanged. The prototype checks MachineCapacity only. |
| Requester source | `spec.requester`, checked by a ValidatingAdmissionPolicy. Milo does not enforce that policy, so this choice is open again; see [known gaps](#the-requester-check-is-not-enforced-in-milo). |

## Where the prototype differs from the proposal

- **Executing comes after the record.** The proposal's sequence marks the
  operation `Executing` before the capacity re-check. The prototype marks it
  after it writes the stage record. A started operation whose record then
  disappears ends as `Unknown` (`RecordLost`).
- **The acceptance order** starts with `Expired`, `DeviceNotFound`,
  `DeviceRetired` and `CapacityBacked`. The proposal's table lists reasons
  without an order.
- **Extra refusal reasons.** The worker has reasons the proposal does not
  name, listed in [worker reasons](#worker-reasons). `DiscoveryNotCurrent` also
  covers a TLS key that discovery has not yet compared with other BMCs.
- **The worker also checks the credential's pin** against the key discovery
  observed (`CredentialMismatch`), before it connects.
- **The Device lock is a site Lease**, in addition to the proposal's check of
  other running operations. It stops two dispatchers from accepting two
  operations on one Device at once.
- **Worker deadline.** The Job's `activeDeadlineSeconds` is the two-minute
  start window plus four request timeouts, `observeTimeout` and one minute.

## Known gaps

### The requester check is not enforced in Milo

The proposal says admission requires `spec.requester` to equal the creating
user. The prototype ships that check as a ValidatingAdmissionPolicy. In the
end-to-end run, Milo v0.32.5 loads the policy plugin but does not evaluate a
policy stored in a project: a staff user created an operation naming another
requester. A copy of the policy at Milo's root is evaluated, but it resolves
the request's namespace in root storage and rejects every create in a project
namespace with `namespaces "inventory" not found`. Do not apply the policy at
the root.

Until this is decided, `spec.requester` is what the creator wrote, and the
audit record of who asked comes only from Milo's own audit log. The choices
are a validating webhook at Milo's root, as capacity registration already
uses, or taking the requester from Milo's audit record, as the proposal's
open question suggests. Milo versions after v0.32.5 have not been tested.

### Other gaps

- **Capacity window.** Capacity defined after the dispatcher's last check and
  before the command is not caught. The allocator gate in phase 5 is not built.
- **No staff UI.**
- **No real hardware.** The command path has run only against the emulator
  and its simulated power. Vendor `LastResetTime`, `PoweringOn` and
  `PoweringOff`, and reset semantics are unqualified.
- **Milo IAM.** The roles are checked by tests of their shape. The e2e run
  uses Kubernetes RBAC with the same grants, not Milo's IAM authorizer.
- **Monitoring.** No metrics for unaccepted operations, `Unknown` results,
  duplicate workers or dispatcher restarts.
- **Deployed Milo.** The component has not been applied to any deployed Milo,
  and it is in no root.

## Tests

`go test -race ./internal/devicepower ./internal/redfish ./internal/bmcdiscovery`
covers, with `KUBEBUILDER_ASSETS` set for the API-server tests:

- every acceptance refusal, and that refusals create no site object;
- the capacity re-check after acceptance;
- each worker outcome, with the number of resets the BMC received: success,
  already in state, refused, refused but changed, lost response with and
  without effect, restart without a reset time, identity change after the
  command, and every refusal before the command;
- a second worker and eight concurrent workers sending once;
- a fenced worker and a failed write-ahead sending nothing;
- a lost worker at each stage, a deleted Job and a missing record;
- two dispatchers and six concurrent reconcilers starting one worker;
- the Device Lease and its takeover after the holder finishes;
- retention;
- on a real API server: the requester policy, spec immutability, the
  result set once, and the staff and dispatcher permissions;
- on a real API server: the site policies admit the worker shape and deny
  another image, another Device's credential, a second volume, a Secret in
  the environment, a privileged or host-network Pod, retries, the
  dispatcher's identity, another command, a Role reading Secrets or another
  record, a binding to another identity, a non-worker object, an input change
  and a delete of the policy parameter;
- the Redfish client finding one System by UUID, refusing a replaced or
  duplicated server and a foreign reset target, re-checking identity, and
  sending a lost reset once;
- discovery recording reset types with `GET`s only.
