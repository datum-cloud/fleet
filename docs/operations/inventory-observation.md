# Observational inventory import from NetBox

[Operations guide](README.md) · [Architecture](../architecture/README.md) ·
[Implementation status](../development/README.md)

Operators already record racks, chassis, serials, ports, cabling, and management
addresses in NetBox. Fleet needs to read those records about real hardware
before it can plan, enroll, or offer that hardware. The importer must not
provision anything, publish capacity, or take ownership in the process.

The `InventoryObservation` kind in `fleet.datumapis.com/v1alpha1` and the
`fleet-inventory-observer` command do this. An operator names one NetBox device.
The observer copies an allowlisted set of that device's recorded facts into the
observation's status. The status carries the source revision, the time it was
read, a content digest, and the `ObservedOnly-v1` trust profile.

## Status

| Aspect | State |
| --- | --- |
| CRD, Go types, observer command, NetBox client, CronJob and RBAC root | Implemented in `config/inventory-observer` and `cmd/fleet-inventory-observer`. |
| Tests | Unit tests cover NetBox decoding, auth schemes, pagination, redirects, failure mapping, identity guards, retained facts, write conflicts, write scope, CRD validation, and RBAC scope. An envtest test runs the observer as its packaged service account against a real API server. It checks that the packaged Role allows status writes only, that `spec.source` is immutable, that stale writes conflict, and that the trust profile cannot be raised. No test runs against a live NetBox. |
| Production roots | Not included. `config/milo`, `config/overlays/management-plane`, and `config/overlays/synthetic-staging` do not reference it. An environment opts in by reconciling `config/inventory-observer`. |
| Hardware qualification | None. Facts are operator-entered records, not observed hardware. |

## What an observation is and is not

An observation is attributable evidence about one source object. It is **not**
a `Device`, a `DeviceClaim`, a `MachineCapacity`, a plan, or an ownership
transition. This follows the inventory principle that discovery creates
observed-only evidence. No Fleet controller reads observations. Creating,
refreshing, or deleting one does not:

- publish supply or make capacity allocatable;
- reserve rack space, ports, or power;
- authorize power, boot, wipe, credential, or network changes; or
- change any record in NetBox.

`MachineCapacity.spec.deviceRef` still names a Device record. The proposed
`Device` kind is not implemented. A later, explicit registration step may link an
observation to a Device. Until then the two stay separate identities.

## Writers

| Surface | Sole writer |
| --- | --- |
| `spec.source` (system, kind, NetBox ID, optional expected name) | Operator, usually through GitOps. Immutable after creation. |
| `status` | `fleet-inventory-observer`. It needs `get`/`list` on observations and `update` on their status, and nothing else. |
| NetBox | Unchanged. The observer sends only `GET` requests. |

The observer never creates or deletes observations, and it never edits their
spec, labels, or annotations. To retarget an observation, create a new one; the
CRD rejects changes to `spec.source`.

## Example

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: InventoryObservation
metadata:
  name: netbox-device-1234
  namespace: fleet-system
spec:
  source:
    system: NetBox
    kind: Device
    id: 1234
    expectedName: server-01.rack-01.example
```

After a refresh, `kubectl get inventoryobservations -n fleet-system` shows the
recorded name, whether the last refresh succeeded and why, and when the facts
were read. `status.facts` holds:

- the name, manufacturer, model, serial, asset tag, role, and NetBox status;
- the site slug, rack, position, and face;
- each interface's name, type, recorded speed, enabled and management flags,
  LAG, normalized MAC addresses, and recorded cable peers; and
- the primary IPv4, primary IPv6, and out-of-band addresses as recorded.

## Refresh semantics

| Situation | `Observed` condition | Facts |
| --- | --- | --- |
| Device read and identity matches | `True`, `Observed` | Replaced; `observedAt`, `sourceUpdatedAt`, and `factsDigest` updated |
| NetBox unreachable, timed out, malformed, or inconsistent during pagination | `False`, `SourceUnavailable` | Previous facts retained, unchanged |
| Credential rejected (HTTP 401/403) | `False`, `SourceUnauthorized` | Retained |
| Device ID not found | `False`, `SourceObjectMissing` | Retained |
| Name differs from `expectedName`, or the serial differs from the previously observed serial | `False`, `IdentityMismatch` | Not updated |

A failed refresh never reports success, and it never clears or overwrites the
facts from the last successful read. Consumers must compare `observedAt` with
their own freshness requirement; an old `True` condition is still old evidence.
A changed serial on the same NetBox ID is reported as a mismatch rather than
accepted as an update, because replaced hardware is a new asset identity. To
record it, create a new observation. The observer updates status with the
listed `resourceVersion`, so a concurrent change makes its write fail. The next
run retries.

## Source handling

- **Allowlist only.** Custom fields, comments, config contexts, local context
  data, tags, and tenant data are never decoded or stored. NetBox custom fields
  can hold credentials.
- **Identity.** The NetBox numeric ID is the source key. Device names can
  change, so `expectedName` is a guard, not the key.
- **Units.** `speedKbps` is the source value unchanged. NetBox defines interface
  speed in kbps, but values are operator-entered and not checked against links.
- **MAC addresses.** The pre-4.2 `mac_address`, `primary_mac_address`, and
  `mac_addresses` fields are merged, parsed, lowercased, and deduplicated. Values
  that do not parse are dropped.
- **Transport.** The origin must be HTTPS; plain HTTP is allowed only on
  loopback, for tests. Redirects are not followed. The client computes its own
  pagination offsets and never follows server-supplied `next` URLs, so the token
  goes only to the configured origin. Response bodies are capped at 4 MiB and
  interface lists at 2,000 entries.
- **Credentials.** Tokens prefixed `nbt_` (NetBox 4.5+ v2 tokens) use the
  `Bearer` scheme; others use `Token`. The token file is re-read on every
  request, so a rotated Secret applies without a restart.

## Deploying the observer root

`config/inventory-observer` contains the cluster-scoped CRD and the following in
`fleet-system`: a ServiceAccount, a Role and RoleBinding, and a CronJob that
runs every 15 minutes with `concurrencyPolicy: Forbid`. The publish workflow
pins the CronJob image to the same build as the manager. The environment
supplies:

| Input | Key | Content |
| --- | --- | --- |
| ConfigMap `fleet-inventory-observer` | `netboxURL` | NetBox origin, for example `https://netbox.example` |
| Secret `fleet-inventory-observer-netbox` | `token` | A read-only NetBox API token, delivered by the environment's secret manager |

Apply observations after the CRD is established, for example from a separate
reconciliation that depends on this root. A Job exits non-zero when any
observation was not refreshed, so failed Jobs show stale or mismatched
evidence. The CronJob needs egress to the NetBox origin and the Kubernetes API.

Run one refresh locally against a disposable cluster:

```sh
go run ./cmd/fleet-inventory-observer \
  --namespace fleet-system \
  --netbox-url https://netbox.example \
  --netbox-token-file /path/outside/the/repository/token
```

It uses the current kubeconfig context. Keep the token file outside the
repository.

## Deferred decisions

- Where operator-only inventory finally lives: this root keeps observations on
  the management cluster. The inventory proposal places physical inventory in an
  operator project, which requires Milo registration and IAM.
- Registration from an observation into a `Device`, and any link to
  `MachineCapacity`. Both need the explicit, separately authorized transition
  described by the inventory proposal.
- Hardware facts (CPU, memory, disks). NetBox records for compute nodes often
  lack them; they belong to a qualified collector such as Redfish inventory,
  not to NetBox import.
- Bulk selection, for example every device in a rack. Each observation is
  explicit today so that importing cannot widen silently.
