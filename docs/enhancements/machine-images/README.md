---
status: provisional
stage: alpha
---

# OS images and bootstrap data

[Documentation overview](../../README.md) · [Functionality guide](../../functionality/README.md)

- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
- [Design details](#design-details)
  - [API design](#api-design)
  - [Publication and entitlement](#publication-and-entitlement)
  - [Admission](#admission)
  - [Artifact distribution](#artifact-distribution)
  - [Sealed bootstrap store](#sealed-bootstrap-store)
  - [Site delivery](#site-delivery)
  - [Implementation decisions](#implementation-decisions)
  - [Acceptance tests](#acceptance-tests)
- [Production readiness review questionnaire](#production-readiness-review-questionnaire)
- [Implementation history](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure needed](#infrastructure-needed)

## Summary

A customer chooses an OS image from a catalog that the provider publishes, and
supplies bootstrap data such as user data and SSH keys. Both are inputs to an
`Installation`.

The provider publishes each image as an immutable `MachineImage` that names its
artifacts by digest. Fleet resolves the customer's choice to that digest when
it admits the Installation, and keeps it. Fleet copies the bootstrap data into
a sealed store that staff cannot read, and delivers it only to the site worker
that runs the install.

This enhancement is a proposal. Nothing in it is implemented.

## Motivation

The [machine lifecycle](../machine-lifecycle/README.md#installation) defines
the Installation, but not where images come from or how bootstrap data reaches
the server. [Device custody](../device-operations/README.md#customer-data)
requires a sealed store for bootstrap data and leaves the image catalog and
bootstrap APIs to Metal. Without them, a customer cannot choose an OS or
configure the installed system.

### Goals

- The provider publishes images that cannot change after publication.
- A customer can install only the images published to their project.
- An admitted install keeps the exact image it resolved, whatever later
  happens to the catalog.
- Fleet refuses an image that does not match the server's architecture or
  firmware mode.
- No staff role can read bootstrap data.
- Sites fetch image artifacts only by digest, from a source the site
  configures, never from a URL in the API.
- No new database.

### Non-goals

- Building images, or which OS families to offer.
- Managed configuration policy for clusters, which the [OS configuration
  proposal](../../proposals/fleet-management/os-configuration.md) covers.
- Delegated customer PXE, which the PXE installation proposal in
  [#59][pxe-pr] covers.
- Secure Boot signing and measured boot.
- Guest health after the installed OS boots.

## Proposal

The provider publishes a `MachineImage` in its project and publishes it to the
consumer projects that may use it. A customer creates an Installation that
names a published image and, optionally, a bootstrap Secret in their own
project.

The Fleet provider for Metal admits the Installation. It resolves the image to
its digest, checks it against the allocated server, copies the bootstrap data
into the sealed store, and creates a `DeviceInstallation` that carries the
digest and an opaque reference to the sealed copy. Custody admits and delivers
it as for any [Device operation](../device-operations/README.md#admission).

![Image and bootstrap data flow from the provider and the customer to the site](diagrams/data-flow.svg)

### User stories

- A customer installs a published Linux image with their SSH key, then logs in.
- A customer reinstalls with new user data. The first install's data is not
  reused.
- A provider publishes a fixed release of an image and withdraws an old one.
  Machines installed from the old one keep running.
- A staff member investigates a failed install without seeing the customer's
  user data.

## Design details

### API design

Names are provisional. Examples use synthetic data.

#### MachineImage in the provider project

The provider defines each image release in its project:

```yaml
apiVersion: metal.datumapis.com/v1alpha1
kind: MachineImage
metadata:
  name: example-linux-1-2
  namespace: default
spec:
  definition:                     # immutable
    family: example-linux
    version: "1.2"
    architecture: amd64
    firmwareModes: [UEFI]         # UEFI | BIOS
    bootstrapFormats: [NoCloud]   # empty when the image takes no bootstrap data
    digest: "sha256:4f2a9c..."    # digest of the artifact manifest
  availability: Available         # Available | Withdrawn
```

The definition cannot change. A new release is a new MachineImage. The
provider can change only `availability`. A withdrawn image admits no new
Installations.

The digest identifies the image's artifact manifest, which lists the digest of
every file the installer writes or boots. The API holds no URL.

#### MachineImage in a consumer project

Publication creates a MachineImage in the consumer project that names its
provider source, the same way a [MachineClass](../machine-lifecycle/README.md#machineclass)
does:

```yaml
apiVersion: metal.datumapis.com/v1alpha1
kind: MachineImage
metadata:
  name: example-linux-1-2
  namespace: default
spec:
  source: {project: example-provider, name: example-linux-1-2}   # immutable
status:
  resolved:
    family: example-linux
    version: "1.2"
    architecture: amd64
    firmwareModes: [UEFI]
    bootstrapFormats: [NoCloud]
    digest: "sha256:4f2a9c..."
  conditions:
    - {type: Available, status: "True"}
```

A MachineImage has exactly one of `definition` or `source`.

#### Bootstrap Secret

The customer stores bootstrap data in a Secret of type
`metal.datumapis.com/bootstrap` in the Installation's namespace:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: example-bootstrap
  namespace: default
type: metal.datumapis.com/bootstrap
stringData:
  userData: |
    #cloud-config
    hostname: example
  sshAuthorizedKeys: |
    ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleOnly example@example.com
```

Both keys are optional, but at least one is required, and together they hold
at most 64 KiB. Fleet passes user data to the image unchanged and installs the
keys for the image's default user.

#### Installation

The Installation keeps its shape. `imageRef` names a MachineImage in the same
project and namespace, and `bootstrapConfigRef` names a bootstrap Secret there:

```yaml
apiVersion: metal.datumapis.com/v1alpha1
kind: Installation
metadata:
  name: example-os-1
  namespace: default
spec:
  machineRef: {name: example}
  expectedPredecessor: {type: None}
  imageRef: {name: example-linux-1-2}
  bootstrapConfigRef: {name: example-bootstrap}
status:
  resolvedImage: {name: example-linux-1-2, digest: "sha256:4f2a9c..."}
  conditions:
    - {type: Accepted, status: "True"}
```

`status.resolvedImage` reports the digest Fleet admitted. Status never shows
bootstrap data or its digest.

#### DeviceInstallation inputs

The [DeviceInstallation](../device-operations/README.md#operations) carries
the provider image's name and digest, and the sealed copy's opaque reference
and digest. It carries no consumer project name and no bootstrap bytes.

### Publication and entitlement

The provider's publisher creates a consumer project's MachineImage, as it does
for MachineClass. Customers cannot create, change, or delete MachineImages.
Fleet records the publication in a protected record in the provider project,
as the machine lifecycle requires for [classes and
profiles](../machine-lifecycle/README.md#entitlement-and-publication).

A project is entitled to an image only while all of these hold:

- the consumer MachineImage exists and matches its retained publication;
- its source is in the provider project that holds the Machine's allocation;
  and
- the provider MachineImage exists and is `Available`.

The caller also needs `machineimages.use` on the consumer MachineImage, and
`get` on the bootstrap Secret. Fleet checks both for the principal that created
the Installation, as for [install
permission](../machine-lifecycle/README.md#request-authority).

### Admission

The Fleet provider admits an Installation in this order. Each step must pass
before the next one runs, and nothing is copied or created until all pass:

1. The Machine has an active allocation, and the predecessor check passes.
2. The image is entitled and the caller may use it.
3. The image matches the allocation's hardware:
   - its architecture equals the CPU architecture;
   - it lists the server's firmware mode; and
   - if the Installation has bootstrap data, it lists a bootstrap format.
4. The caller can read the bootstrap Secret, and its contents are valid.
5. The sealed copy is written. See [Write order](#write-order-and-crash-recovery).
6. The DeviceInstallation is created.

The allocation records the server's hardware when Fleet allocates it, including
its firmware mode. Inventory records each Device's firmware mode, from BMC
discovery. A server with no recorded firmware mode installs no image.

Each refusal sets `Accepted=False` on the Installation with one of these
reasons:

| Reason | Refused by |
| --- | --- |
| `ImageNotFound` | No MachineImage of that name in the namespace |
| `ImageNotEntitled` | Publication, source project, or `machineimages.use` |
| `ImageWithdrawn` | The provider image is `Withdrawn` |
| `ImageIncompatible` | Architecture, firmware mode, or bootstrap format |
| `BootstrapNotFound` | No readable bootstrap Secret of that name |
| `BootstrapInvalid` | Wrong Secret type, no keys, unknown keys, or over 64 KiB |
| `AuthorizationUnavailable` | An authorization check could not complete |

Fleet evaluates a refused Installation again while it exists, because a missing
publication or Secret can appear later. Admission is final once the
DeviceInstallation exists. Later changes to either MachineImage, the
publication, or the Secret never change an admitted install.

The coordinator adds one [admission check](../device-operations/README.md#admission)
for DeviceInstallation: the sealed copy exists and matches its digest. If not,
it refuses with `BootstrapUnavailable`, and nothing is sent.

### Artifact distribution

The provider builds an image and pushes its artifacts and manifest to its
artifact registry, then creates the MachineImage with the manifest digest. The
registry is site configuration, not an API field. Customers cannot name a
registry, a URL, or an artifact.

Each site keeps a content-addressed store. It mirrors every `Available`
provider MachineImage by digest from its configured registry, and checks every
artifact against the manifest before it keeps any. It keeps an image's
artifacts while a MachineImage or an operation that has not ended names the
digest.

A site worker refuses an install whose image digest is not in the store, with
reason `ImageUnavailable`, before it sends anything. The site's boot service
serves an artifact only to a server booting under an open boot admission whose
operation names that image, and only if the artifact's digest matches the
manifest.

### Sealed bootstrap store

The sealed store is a dedicated kind, `SealedPayload`, in
`fleet.datumapis.com`, in the provider project. A Kubernetes Secret would not
work there: Milo grants permissions per kind or per object, not per namespace,
so every role that reads Secrets in the provider project would read every
payload.

```yaml
apiVersion: fleet.datumapis.com/v1alpha1
kind: SealedPayload
metadata:
  name: sealed-9a3f0c5e21b7d84e6f10
  namespace: default
spec:                          # immutable
  operationName: alloc-3b9d61c2-install-5d1e8a0b4c2f
  digest: "sha256:0c2b77e1d9a4..."
  data: <base64>               # the bootstrap Secret's two values, exactly
```

The name derives from the operation name and the digest. A retried write after
a crash creates the same name, and the same name always holds the same bytes.

| Identity | Can do on SealedPayload |
| --- | --- |
| Fleet provider for Metal | `create` only |
| Fleet coordinator, which includes the federation courier | `get`, `list`, `watch`, `delete` |
| Every staff role | Nothing |

The provider never reads a payload back. Production must encrypt this kind at
rest in the provider project's storage.

#### Write order and crash recovery

| Change | Order | After a crash between the writes |
| --- | --- | --- |
| Admit an Installation | SealedPayload, then DeviceInstallation | The retry writes the same SealedPayload name and creates the operation. |
| End an install | Custody result, then operation status, then delete SealedPayload | The coordinator deletes the payload of every ended operation. |

The coordinator also deletes a SealedPayload that no operation names after one
hour. If the provider creates the operation after that, admission refuses it
with `BootstrapUnavailable`, and the customer creates a new Installation.

### Site delivery

Federation delivers the payload to the site with the operation, as a separate
Secret, never inside the operation's handoff. The site's handoff names that
Secret, and the site grants that operation's worker `get` on exactly that
Secret. No site identity can list or watch these Secrets.

The worker checks the payload against the operation's digest and refuses with
`InputMismatch` before it sends anything if they differ. It hands the payload to
the server only through that operation's one-time boot: the boot service
serves it once, under the boot admission's capability, in the image's
bootstrap format. The boot service drops its copy when the admission closes.

The site deletes the payload Secret when the operation ends, when a higher
fence stops its worker, or when the coordinator answers its result, whichever
comes first. Federation removes its copy when the coordinator answers the
result.

### Implementation decisions

Record these before production:

- **Production OS.** Proposed: Debian 13 generic cloud image, amd64, UEFI,
  with cloud-init NoCloud. A person must choose and qualify it.
- **Encryption at rest.** Which key protects `SealedPayload` in Milo storage.
- **Firmware mode source.** BMC discovery reports it, or staff record it in
  inventory.
- **Artifact registry.** Which registry holds provider artifacts, and how
  sites authenticate to it.

### Acceptance tests

These tests are required and not implemented:

- A customer installs a published image, and the guest receives exactly its
  user data and SSH keys.
- A customer names an image that is not published to their project, and
  admission refuses it.
- A withdrawn image admits no new install, and a running install of it
  completes.
- The provider deletes and recreates a MachineImage with other artifacts under
  the same name, and an admitted install keeps the first digest.
- An image's architecture or firmware mode does not match the server, and
  admission refuses it.
- The customer edits the bootstrap Secret after admission, and the install uses
  the admitted data.
- The provider crashes between the sealed write and the operation, retries, and
  one payload and one operation exist.
- No staff role can get, list, or watch a SealedPayload, and no site identity
  other than the operation's worker can read its payload Secret.
- The payload is gone from the provider project and the site after the
  operation ends.

## Production readiness review questionnaire

These controls are proposed, not implemented.

### How do operators enable or disable the feature?

Register MachineImage, SealedPayload, and their IAM resources, then publish
images. To stop new installs of an image, withdraw it.

### Does enabling the feature change any default behavior?

Installations that name a bare digest stop resolving. A consumer project needs
a published MachineImage to install.

### What must operators monitor?

Refused Installations by reason, images a site has not mirrored, and
SealedPayloads older than their operation's deadline.

### What does the feature depend on?

Milo for project APIs and IAM, Device custody for execution, federation to
reach sites, and the provider's artifact registry.

### Does the feature add API types or calls?

It adds MachineImage to Metal and SealedPayload to Fleet. Each install adds
one SealedPayload write and one delete.

## Implementation history

- 2026-11-25: Proposed.

## Drawbacks

- Each site stores every available image.
- A customer can change bootstrap data only by creating a new Installation.
- The site's boot service holds the payload while the boot admission is open.

## Alternatives

- **Images listed on the MachineClass:** Every image release would edit the
  offering, and IAM could not grant one image without the others.
- **A URL in the MachineImage:** Sites would fetch from wherever the API
  points, which allows server-side request forgery.
- **A Secret in a protected namespace for the sealed store:** Milo IAM cannot
  scope Secret access to a namespace.
- **Carrying the payload in the operation:** Staff can read operations.

## Infrastructure needed

Testing needs one provider project, two consumer projects, one site with a
boot service, and an artifact registry. The local VM and Redfish lab modes can
prove the install path without hardware.

[pxe-pr]: https://github.com/datum-cloud/fleet/pull/59
