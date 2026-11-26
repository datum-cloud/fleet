# OS images and bootstrap data

[Documentation overview](../README.md) · [Implementation status](README.md)

This page records how Fleet implements the [OS images and bootstrap data
proposal](https://github.com/datum-cloud/fleet/pull/184) (#184). The
proposal's API names are provisional. Each piece below is marked
**implemented and verified**, **implemented in lab compositions only**, or
**proposed**.

## Status

| Increment | What it covers | Status |
| --- | --- | --- |
| 1: image catalog | `MachineImage` and `MachineImagePublication` kinds, publication, admission-time resolution to a pinned digest, architecture and firmware mode checks, the worker's firmware mode check, and IAM | **Implemented and verified.** The CRD rules are checked with the API server's own schema validator, resolution and publication with a fake API server, and installs of a resolved image against a real API server and in the standalone smoke. The multi-plane runs are not yet verified. |
| 2: sealed bootstrap store | `SealedPayload`, the coordinator's check and sweep, delivery to the operation's worker, deletion at the site, IAM | **Implemented and verified** through the simulator site, by unit tests, tests against a real API server, and the standalone smoke. Admission refuses bootstrap data for a Device served through federation, which the local VM and Redfish labs use. |
| 3: lab proof | A chosen image with bootstrap data installs in the local VM and Redfish labs, and the guest reports the data it received | **Proposed** |

## What increment 1 adds

- **Kinds.** `MachineImage` holds either a provider `definition` or a published
  `source`: a `publisherRef` and the image's name there. CEL makes the
  definition and the source immutable and lets the provider change only
  `availability`. `MachineImagePublication` is the immutable record, in the
  provider project, of one consumer copy published from one provider image
  incarnation and digest. Its `consumerRef` names the consumer tenant. Its
  name derives from the tenant reference and UID, the copy's namespace, name
  and UID, and the provider image's name and UID. Its spec holds only names
  and the digest, never a UID.
- **Tenant references.** `publisherRef` and `consumerRef` are typed
  `{apiGroup, kind, name}` references with names only. A Milo `Project` is
  the tenant in multi-plane mode, and a `Namespace` in standalone mode, where
  the provider namespace is the publisher. The CRD rules and the catalog
  refuse every other kind, and the catalog writes nothing for one. The
  catalog takes its consumer from the shared tenancy resolver, with the
  reference, incarnation and API client together, and uses the same typed
  reference and source shape as MachineClass.
- **Publication.** The publisher writes the consumer copy, then the record.
  Until the record exists the copy entitles nothing, so a crash between the
  writes fails closed and a retry completes it.
- **Resolution.** Admission reads the consumer copy and the provider image,
  derives the record's name from their incarnations and the consumer
  tenant's, and refuses unless that record exists and matches the names and
  the provider image's digest. A recreated tenant, copy or provider image
  finds no record and is refused. The digest comes from the record, never from
  the copy's status. The provider definition is immutable, so the digest pins
  its content; `availability` is read live. A Fleet deployment serves one
  publisher, which also holds every allocation, so a copy must name that
  publisher.
- **Republishing.** A record never changes. If a copy or its source is
  recreated, publishing it again writes a new record under its own name. The
  old record entitles nothing, and the publisher may delete it.
- **Compatibility.** The allocation's hardware snapshot records the server's
  firmware mode. Admission refuses an image whose architecture differs, whose
  firmware modes do not include the server's, or which takes no bootstrap data
  when the Installation has some. An unknown firmware mode installs nothing.
- **Retention.** The DeviceInstallation keeps the image name, digest and
  firmware modes, so later catalog changes never change an admitted install.
  The Installation reports `status.resolvedImage`.
- **Worker check.** The Redfish worker reads the system's
  `BootSourceOverrideMode` and refuses with `ImageIncompatible` before sending
  anything if the image does not list it. Redfish defines that property as the
  mode of the next override boot, which is the boot the installer uses. A BMC
  that does not report it installs nothing. The Redfish lab reports `Legacy`.
  Physical BMCs are not qualified. The local VM worker refuses any image that
  does not list BIOS, which its guests boot.
- **IAM.** Customer roles may only read and use images. Only the provider and
  publisher roles write images, only the publisher writes records, and only
  the publisher and the Fleet provider read records.

## What increment 2 adds

- **Bootstrap Secret.** A Secret of type `metal.datumapis.com/bootstrap` with
  `userData`, `sshAuthorizedKeys`, or both, as UTF-8 text, at most 64 KiB and
  at most 96 KiB once encoded, so a site always receives it whole. Admission
  refuses anything else with `BootstrapInvalid`, and a missing or unreadable
  Secret with `BootstrapNotFound`.
- **Sealed store.** `SealedPayload` in `fleet.datumapis.com`, in the provider
  project. Its spec never changes. It holds a random 32-byte salt and the data,
  and its digest covers both. Its name derives from the operation name and the
  digest.
- **Admission.** If the DeviceInstallation already exists, admission returns
  it and reads nothing. Otherwise the Fleet provider seals the data, creates
  the payload, then creates the operation, which carries only the payload's
  name and digest. Of two concurrent admissions, one operation wins and the
  other's payload is swept.
- **Coordinator check.** At the custody update, every site serving the Device
  must be able to receive payloads, or the operation fails with
  `BootstrapUnsupported`. The payload must exist under the derived name, name
  the operation, and match a digest recomputed from its salt and data, or the
  operation fails with `BootstrapUnavailable`. Either way nothing is sent. The
  coordinator reads through to the API server, and a failed read refuses, as
  in every kind check.
- **Sweep.** Each coordinator pass deletes, with a UID precondition, every
  payload whose operation ended, whose operation names another payload, or
  that has no operation and an allocation that is not `Active`.
- **Delivery.** The courier reads the payload, checks it again, and sends it to
  the site beside the assignment. The site takes the payload reference from
  the admitted inputs, not from the transport, stores the payload only if it
  matches, and the runner checks it again before anything is sent.
- **Site deletion.** The site deletes the payload when the result is stored,
  when the coordinator answers the result, and whenever it raises the Device's
  fence, for a newer operation or a recovery, which removes every payload
  below it, including one whose worker never ran. A worker stopped from
  outside keeps it for its replacement.
- **Evidence.** A successful install's evidence names the digest of the data
  the worker handed to the server, recomputed from what it handed. Custody
  accepts a success only if that equals the admitted digest, and only if it is
  empty when there was no bootstrap data.
- **IAM.** Only the coordinator role reads `SealedPayload`; the installation
  requester may only create it. A test walks every Milo role the repository
  ships, resolves inheritance, and fails if any other role can read it or holds
  a wildcard.

## Not implemented yet

- The validating webhook that rejects image writes from anyone but the
  publisher, and definitions outside provider projects. IAM alone keeps
  customers from writing images today.
- A Milo IAM image-use check. The standalone and multi-plane compositions use a
  fixture authorizer that allows every creator.
- Artifact mirroring by digest from a provider registry. Each lab site holds
  one pinned image and serves only that one.
- Firmware mode from BMC discovery. Lab fixtures write it on the capacity.
- Federation delivery of bootstrap payloads to a cell, and handing them to a
  local VM or Redfish server. Admission refuses such an operation with
  `BootstrapUnsupported` before the Device's fence moves.
- The Milo check that the Installation's creator can read the bootstrap
  Secret. The lab compositions read it with their own identity.
- Encryption at rest for `SealedPayload` and an audit policy that omits its
  bodies.
