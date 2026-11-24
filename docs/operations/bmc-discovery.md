# BMC discovery

[Operations guide](README.md) · [Physical devices](physical-inventory.md) ·
[Redfish client](redfish-lab.md#bmc-client-library)

`fleet-bmc-discovery` reads what each Device's BMC reports and writes it into
the Device's `status`. It is read-only towards the hardware: it issues Redfish
`GET` requests only, never changes power, boot or any BMC setting, and never
writes a Device's `spec`.

## Implementation status

| Aspect | State |
| --- | --- |
| Code | `cmd/fleet-bmc-discovery`, `internal/bmcdiscovery`, pinning and the read-only inventory in `internal/redfish` (`pin.go`, `inventory.go`). |
| API | `Device.status` schema in `config/components/inventory/crds`, Go types in `internal/api/inventory/v1alpha1`. |
| IAM | `fleet.datumapis.com-inventory-discoverer` role; `updateStatus` and the `status` subresource on the Device ProtectedResource. |
| Packaging | `config/base/bmc-discovery`, published in the `fleet-kustomize` bundle. The binary is in the `fleet` image. |
| Tests | Unit tests and an end-to-end test against an `httptest` TLS Redfish service (see [Tests](#tests)). |
| Hardware qualification | **None.** Not run against a vendor BMC. See [Not yet qualified](#not-yet-qualified). |

## Who writes what

| Surface | Writer | Content |
| --- | --- | --- |
| `Device.spec` | The inventory source (an operator or an importer) | Expected facts: identity, components, `bmc.endpoint`. |
| `Device.status` | BMC discovery only, through the status subresource | What the BMC reported, and how it compares with spec. |

Observed values in status are evidence. `spec.identity` stays the
authoritative, immutable record. A mismatch is reported as a condition and is
never corrected in either direction. Discovery does not fill in MACs or a
system UUID that spec lacks; an importer that wants them reads status and
decides for itself.

## Deployment

Discovery runs where the BMC network is reachable, as **one replica with no
leader election**. Two replicas would contact every BMC twice and race on
status. The base uses `replicas: 1` and the `Recreate` strategy, so a rollout
never runs two.

It needs:

- a kubeconfig for **one Milo project**, mounted from a Secret;
- read access to credential Secrets in the namespace where it runs;
- network reachability to each `spec.bmc.endpoint`. It needs no privilege,
  capability or host networking of its own.

### Standalone Deployment

`config/base/bmc-discovery` contains a ServiceAccount, a namespaced Role and
RoleBinding for Secrets, a ConfigMap generated from `config.yaml`, and the
Deployment. The container runs as UID 65532, non-root, with a read-only root
filesystem, no privilege escalation, all capabilities dropped and the
`RuntimeDefault` seccomp profile. The deploying repository supplies:

| Input | Where |
| --- | --- |
| Namespace | Kustomize `namespace:`. Credential Secrets live here. |
| Image digest | Kustomize `images:`, for `ghcr.io/datum-cloud/fleet`. |
| Milo kubeconfig | Secret `fleet-bmc-discovery-milo-kubeconfig`, key `kubeconfig`. |
| Configuration | A patch of `config.yaml` or the generated ConfigMap. |

The ServiceAccount token is mounted because discovery reads Secrets through
the Kubernetes API. Kubernetes RBAC cannot narrow a Role to labelled Secrets,
so run discovery in a namespace that holds nothing else it should not read.
To avoid the token, set `localKubeconfig` and mount a kubeconfig instead.

### Sidecar in another pod

When only one pod can reach the BMC network, for example a VPN gateway pod
that owns the tunnel device, run discovery as an extra container in that pod.
It shares the pod's network namespace and depends on no other container.
`config/base/bmc-discovery/examples/sidecar-container.yaml` is the container
and volume snippet to merge into that pod's template. In that case:

- create the ConfigMap and Milo kubeconfig Secret yourself; the snippet uses
  fixed names;
- bind the Role in `rbac.yaml` to that pod's ServiceAccount, or set
  `localKubeconfig`;
- set `healthProbeAddress: ""` if port 8081 is already in use in the pod.
  The snippet defines no probes.

### Configuration

`config.yaml`, read with strict decoding:

| Field | Default | Meaning |
| --- | --- | --- |
| `project` | required | The Milo project the kubeconfig addresses. Secrets must name it. |
| `miloKubeconfig` | required | Path of the Milo project kubeconfig. |
| `deviceNamespaces` | required | Project namespaces whose Devices are read. |
| `credentialNamespace` | `$POD_NAMESPACE` | Local namespace of the credential Secrets. |
| `localKubeconfig` | in-cluster | Kubeconfig for the local cluster. |
| `interval` | `1h` | Re-discovery period after a completed attempt. At least `1m`. |
| `retryBase` | `30s` | First retry after an uncertain attempt; doubles up to `interval`. |
| `minContactInterval` | `1m` | Least time between two attempts on one BMC endpoint. |
| `requestTimeout` | `20s` | Timeout of each HTTP request to a BMC. |
| `concurrency` | `4` | BMCs contacted at once (1–64). |
| `healthProbeAddress` | off | Serves `/healthz` and `/readyz`; the base sets `:8081`. |

### Milo access

Bind the kubeconfig's identity to `fleet.datumapis.com-inventory-discoverer`
in the project. It grants exactly:

| Permission | Used for |
| --- | --- |
| `fleet.datumapis.com/devices.get`, `.list`, `.watch` | Reading Devices. |
| `fleet.datumapis.com/devices.updateStatus` | Status writes when the authorizer maps status to this verb, as Fleet's metal roles assume. |
| `fleet.datumapis.com/devices/status.update`, `/status.patch` | Status writes when Milo's subresource authorization is enabled. |

It grants no `devices.update`, `devices.patch` or any other kind, and inherits
no role. Discovery writes with a JSON merge patch on `devices/status` and an
optimistic lock. If an authorizer were to check `devices.patch` for a status
patch, the write would be denied, so a misconfiguration fails closed.

### First run with `--once`

`--once` contacts each eligible BMC once, prints the statuses it would write
as JSON on stdout, and exits. It reads Devices and Secrets and writes nothing
to Milo. Against the running Deployment:

```sh
kubectl -n <namespace> exec deploy/fleet-bmc-discovery -- \
  /fleet-bmc-discovery --config=/etc/fleet/bmc-discovery/config.yaml --once
```

## Credential Secrets

One Secret per Device, in the credential namespace, never in Git, Milo or the
Device. `examples/credential-secret.yaml` is a synthetic example.

| Field | Value |
| --- | --- |
| Label `fleet.datumapis.com/bmc-credential` | `"true"`; only labelled Secrets are cached or read. |
| Annotation `fleet.datumapis.com/device-project` | Must equal `project` in the config. |
| Annotation `fleet.datumapis.com/device-namespace` | The Device's namespace in the project. |
| Annotation `fleet.datumapis.com/device-name` | The Device's name. |
| `data.username`, `data.password` | Single-line BMC credential. |
| `data.tlsPublicKeySHA256` | Optional. The approved pin, 64 hex digits, colons and case ignored. |

A Device is discovered only when it has `spec.bmc` and exactly one Secret
names it. A Device with neither is skipped and its status is not written. Two
Secrets naming one Device report `CredentialConflict` and send nothing.

## TLS pinning

BMC certificates are commonly self-signed and share one subject across a
fleet, so chain and hostname verification cannot identify a BMC. Discovery
identifies it by the SHA-256 of its leaf certificate's SubjectPublicKeyInfo
instead. TLS 1.2 or later is required.

Each attempt runs in this order:

1. **Anonymous probe.** A TLS handshake that accepts any certificate, then one
   `GET /redfish/v1/` without a credential. The certificate's key hash,
   `notAfter` and whether it is self-signed are recorded in
   `status.discovery.tls`. The probe client holds no credential, so it cannot
   send one.
2. **Pin check.** Discovery stops here without sending the credential when no
   pin is approved (`PinNotApproved`) or the observed key differs from the
   approved pin (`PinMismatch`). It never accepts a new key by itself.
3. **Authenticated read.** Only when the observed key equals the approved pin.
   This client's handshake fails unless the key matches the pin, and it opens
   a new connection for every request. A BMC that changes keys after the probe
   gets no request at all and is reported as `PinMismatch`.

All requests go through the shared Redfish transport guard: the enrolled
`https://IPv4:port` only, paths under `/redfish/v1`, no redirects, no
environment proxy, and a 1 MiB bound on each response.
`redfish.NewPinnedClient` and `redfish.PinnedTLSConfig` are available to the
provisioning executor too; its behavior is unchanged.

### Approving a pin

1. Create the credential Secret without `tlsPublicKeySHA256`.
2. Read the observed key:

   ```sh
   kubectl get device <name> -n <namespace> \
     -o jsonpath='{.status.discovery.tls.publicKeySHA256}'
   ```

3. Verify it out of band, from a host you trust on the BMC network or from
   the BMC's own console or certificate page:

   ```sh
   openssl s_client -connect <bmc-ip>:443 </dev/null 2>/dev/null \
     | openssl x509 -pubkey -noout \
     | openssl pkey -pubin -outform der | sha256sum
   ```

4. Check that `PublicKeyUnique` is `True`. If it is `False`, see
   [Shared keys](#shared-keys) before approving.
5. Set `tlsPublicKeySHA256` in the Secret. The Secret change triggers a new
   attempt at once, still subject to `minContactInterval`.

A BMC that regenerates its key, for example after a firmware reset, reports
`PinMismatch` until an operator verifies and approves the new key.

### Shared keys

Some firmware ships one default key on every unit. Then the pin cannot tell
those BMCs apart. Discovery compares the observed key of every Device it sees
and sets `PublicKeyUnique=False` with reason `SharedPublicKey` on each Device
that shares it, naming up to three of the others. This does not block the
credential: identity comparison still runs after the read. Replace the key
on those BMCs where the firmware allows it.

## Status

```yaml
status:
  observedGeneration: 3
  discovery:
    lastAttemptAt: "2026-01-02T03:04:05Z"
    lastAttemptEndpoint: https://192.0.2.10:443
    tls:
      observedAt: "2026-01-02T03:04:05Z"
      endpoint: https://192.0.2.10:443
      publicKeySHA256: <64 hex digits>
      notAfter: "2036-01-01T00:00:00Z"
      selfSigned: true
    serviceRoot:
      observedAt: "2026-01-02T03:04:05Z"
      endpoint: https://192.0.2.10:443
      redfishVersion: 1.15.0
      uuid: <service UUID>
      vendor: Example
      product: Example BMC
    inventory:
      observedAt: "2026-01-02T03:04:05Z"
      endpoint: https://192.0.2.10:443
      managers:
        - id: BMC
          model: Example BMC
          firmwareVersion: 1.2.3
          networkInterfaces:
            - {id: "1", macAddress: "00:00:5e:00:53:10", linkStatus: LinkUp, speedMbps: 1000}
      systems:
        - id: "1"
          uuid: 00000000-0000-4000-8000-000000000001
          manufacturer: Example Vendor
          model: Example 1U
          serialNumber: SYNTHETIC-0001
          sku: TAG0001
          powerState: "On"
          networkInterfaces:
            - {id: NIC.1, macAddress: "00:00:5e:00:53:01", linkStatus: LinkUp, speedMbps: 25000}
  conditions: [...]
```

Each observation (`tls`, `serviceRoot`, `inventory`) carries its own time and
endpoint and is replaced only by a newer observation of the same kind. A
failed or refused attempt updates `lastAttemptAt` and the conditions and
keeps every earlier observation. Inventory is all or nothing: a partially read
BMC keeps the previous inventory. MACs are lowercase colon form and UUIDs
lowercase canonical. Power state is an observation; discovery never changes it.

### Conditions

| Type | Reasons |
| --- | --- |
| `Reachable` | `ServiceRootRead`; `ServiceRootUnreadable` (handshake completed, anonymous read failed); False `ConnectFailed`, `EndpointInvalid` |
| `Authenticated` | `CredentialAccepted`; False `PinNotApproved`, `PinMismatch`, `CredentialRejected`, `CredentialConflict`, `CredentialInvalid`; Unknown `NotAttempted`, `ReadFailed` |
| `Discovered` | `InventoryRead`; False for a definite refusal (the reason above); Unknown `Unreachable`, `ReadFailed` |
| `IdentityMatched` | `Matched`; False `SystemUUIDMismatch`, `SerialMismatch`, `ModelMismatch`; Unknown `NotObserved`, `StaleObservation`, `AmbiguousSystem` |
| `PublicKeyUnique` | `Unique`; False `SharedPublicKey`; Unknown `NotObserved` |

Messages never carry a URL, credential, header or response body.

Identity comparison selects the System by `spec.identity.systemUUID` when set,
else by serial, else the only System. The serial matches the System's
`SerialNumber` or `SKU`, because some vendors report the service tag as the
SKU. The model matches when the normalized values are equal or one ends with
the other as whole words, so `Example 1U` matches `Family Example 1U`.
Manufacturer is recorded but not compared, since vendor names vary in form.
An inventory read from an endpoint other than the current `spec.bmc.endpoint`
is `StaleObservation`.

### Scheduling

- A completed attempt, including a definite refusal, is repeated after
  `interval`. A rejected credential is not retried sooner, so discovery cannot
  lock out the BMC account.
- An uncertain attempt (no handshake, an interrupted read) is retried after
  `retryBase`, doubling to `interval`.
- A Secret change or an endpoint change triggers an attempt at once.
- No endpoint is contacted twice within `minContactInterval`, whatever the
  trigger.
- Other events, such as another Device's key changing, re-derive conditions
  from retained status without contacting the BMC.
- The schedule is in memory. A restart contacts each BMC once, within the
  `concurrency` bound.

## What is read

Service root, then every Manager and every ComputerSystem (up to 16 of each),
with each one's EthernetInterfaces (up to 32). Collections follow
`Members@odata.nextLink` for up to 8 pages. Every member must sit under its
collection. Documents are decoded leniently: only the named properties are
read, so an off-schema vendor property elsewhere in a document does not fail
the read.

Not read: `Chassis`, `NetworkAdapters` and their ports, firmware inventory,
sessions. Basic authentication is used; no Redfish session is created.

## Tests

`go test -race ./internal/redfish/ ./internal/bmcdiscovery/
./internal/api/inventory/...` covers:

- pinning at the HTTP layer: the probe is one anonymous `GET`; a matching pin
  reads the inventory with `GET`s only; a mismatched pin fails the handshake
  and the server receives no request;
- the decision table: no credential is sent without an approved pin, on a
  mismatch, or with a conflicting or malformed Secret;
- that a failed attempt keeps earlier observations and reports Unknown;
- identity comparison, including SKU, model prefixes, UUID selection and
  stale endpoints, and that it never changes spec;
- that the reconciler writes only `devices/status` and never spec, skips
  Devices without `spec.bmc` or a Secret, honors the interval, backoff and
  per-endpoint gap, and contacts at once after a pin is approved;
- shared-key detection on both Devices, without a second contact;
- `--once` writing nothing and bounding concurrency;
- end to end against an `httptest` TLS service: first contact records the pin
  with no credential sent, then approval yields inventory and
  `IdentityMatched=True`;
- the Device status schema, the discoverer role, and the shipped Deployment's
  single unprivileged replica.

## Not yet qualified

- **Vendor BMCs.** Not run against any vendor Redfish service. Check
  property names and types, the service tag in `SerialNumber` or `SKU`,
  EthernetInterfaces under Managers, and rate limits.
- **Milo status authorization.** Which permission Milo checks for a
  `devices/status` patch has not been observed; the role carries both forms.
- **Anonymous service root.** Some BMCs may require authentication on
  `/redfish/v1/`. That is reported as `ServiceRootUnreadable` and does not
  block the pinned read.
- **Key rotation.** There is no workflow beyond re-approving a new pin.
- **NetworkAdapters.** MACs from `Chassis/NetworkAdapters` are not read.
- **Credential lifecycle.** Secrets are long-lived; rotation and a broker are
  out of scope.
