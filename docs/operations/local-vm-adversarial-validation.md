# Local VM prototype: independent validation

Review date: 2026-11-14. This report covers a shared, uncommitted working tree.
Only the three new adversarial test files and this report belong to this review.
Implementation files, existing tests, and pre-existing dirty changes were not edited.

## Observed results

Parent follow-up: both findings below are fixed in the shared working tree.
The coordinator now compares every terminal result's input digest with the exact
request, including cleanup and recovered results. Lookup rejects `.` and `..`
before transport. Synthetic executor fixtures now return genuine request digests.
`go test -race ./internal/controller ./internal/executor ./internal/simulator
./internal/milo ./internal/staging ./internal/standalone` passed, including both
new regression tests. This follow-up is not independent approval of the VM adapter.
The original review observations below describe the pre-fix implementation.

Commands executed locally:

```sh
go test ./internal/controller ./internal/executor ./internal/simulator ./internal/standalone
go test -race ./internal/controller ./internal/executor ./internal/simulator ./internal/standalone
git diff --check
```

Both Go commands failed on the two new regression tests described below. Simulator
and standalone packages passed; standalone results were cached. No race warning
was reported. Whitespace validation passed. No VM, Docker, kind, deployment,
external review, or hosted CI resources were started by this reviewer.

### High: wrong-asset cleanup evidence releases custody

At review time, `validateTerminalResult` in
[lifecycle.go](../../internal/controller/lifecycle.go) (line 1531) checks operation
ID, kind, phase, completion presence, and result text, but does not compare
`InputDigest` with the exact retained request. Both direct completion and lookup
recovery use this validator.

[The regression test](../../internal/controller/evidence_adversarial_test.go)
returns a successful cleanup with the requested operation ID and a valid digest
computed for a different physical asset. Both direct and recovered responses
produce `State:Released`, `SafeToRemoveFinalizer:true`, and no error. The waiting
consumer then allocates the asset successfully. These are observed unit-test
results, not a hypothetical gap inferred only from code.

Required fix: compare verified result evidence against the exact request before
any custody transition, including cleanup and recovery. Missing or mismatched
digests must remain unknown. Update synthetic executor fixtures to supply genuine
request digests rather than allowing absent evidence for compatibility. The
standalone runner's additional install-history digest check does not protect
the coordinator's cleanup path.

### Medium: dot-segment lookup leaves the operation path

[client.go](../../internal/executor/client.go) (lines 140–144) rejects slash-bearing
IDs but passes `.` and `..` through `path.Join`. The new
[lookup test](../../internal/executor/path_adversarial_test.go) observes requests
to `/v1/operations` and `/v1`, respectively. A 404 produces no error. This is an
HTTP path-confusion finding; it does not establish shell execution or filesystem
access.

Required fix: reject dot segments before transport, or construct a URL whose
single opaque path segment cannot be normalized out of the operation endpoint.
Retain the negative tests for both values.

### Passing adversarial coverage

[The simulator test](../../internal/simulator/binding_adversarial_test.go) retains
an installation, reopens its store, and individually substitutes asset, binding,
epoch, fence, and image under the same operation ID. Every attempt returns
`ErrConflict` without changing the snapshot. This establishes simulator request
binding, not real executor fencing or artifact verification.

## Existing coverage evaluated

- [Lifecycle tests](../../internal/controller/lifecycle_test.go): exclusive
  allocation, competing candidate selection, custody tombstones, release and
  epoch reuse, missing authority, private predecessor checks, unknown-operation
  recovery, malformed completion, retained routing identity, and network cleanup
  ordering. Tests predominantly use an in-memory executor and fake Kubernetes
  client; they do not demonstrate remote-worker fencing or API-server behavior.
- [Simulator tests](../../internal/simulator/store_test.go): serialized duplicate
  submissions, request conflicts, fencing, cleanup retirement, lost responses,
  persistence failure boundaries, corrupt-state rejection, and digest retention.
  These test synthetic effects; no OS is installed.
- [Standalone reinstall tests](../../internal/standalone/reinstall_test.go): exact
  operation recovery across simulator reopen, stale predecessors, concurrent
  reinstall convergence, unknown result retaining custody, and rejection of
  missing/substituted terminal history. These tests were already dirty/untracked
  when this review began and were not authored or modified here.
- [Executor tests](../../internal/executor/client_test.go): authentication, token
  rotation, lost-response lookup, malformed evidence, operation-specific result
  text, and bounded HTTP timeout. They do not qualify libvirt commands or XML.

## Real adapter inspection boundary

During ordinary review, [the image build](../../hack/local-vm/Containerfile),
[installer](../../hack/local-vm/init), and [disk boot script](../../hack/local-vm/boot)
appeared. The installer partitions `/dev/vda`, writes a filesystem and receipt,
installs GRUB, and emits `FLEET-INSTALLED`. The boot script checks an ext4 root on
`/dev/vda1` and emits `FLEET-DISK-BOOT` with the receipt. This is source inspection
only. Separate markers are useful, but there is no observed VM execution here.

The build generates SHA256SUMS, but its Debian base tag and apt package versions
are mutable. Generated checksums alone do not establish trusted artifact
provenance or prove that the executor enforces an admitted checksum. A reproducible
build and verification of the exact locally built bytes are distinct claims.

No Go real-VM adapter was present in the inspected source inventory. Final adapter
validation remains pending. The parent should request a fresh review after the
adapter and its execution commands are available; this report is not approval of
implementation added after the inspection boundary.

## Concrete acceptance assertions still required

These are proposed checks, not successful results. Run destructive cases only
against uniquely scoped disposable domains and disks with a decoy resource to
detect unintended effects.

1. **Exclusive allocation:** race two consumers for one capacity through the
   shared coordinator. Exactly one immutable binding wins, including under stale
   `Available=True`. Restart during selection/commit; never admit effects for the
   loser or briefly expose occupied capacity. Keep Device, capacity, allocation,
   Installation, operation, and libvirt domain identities distinct.
2. **Exact authority:** before the first external command, retain operation ID,
   Installation incarnation, predecessor, asset, binding, epoch/fence, domain
   UUID, canonical disk identity, artifacts, and configuration digests. Change each
   field individually on replay and require rejection without commands. A result
   for another target or old epoch must not complete installation or cleanup.
3. **Actual installation and disk boot:** record that the target disk is newly
   blank and uniquely owned. Observe partition/filesystem and bootloader creation.
   Require a separate subsequent boot with installer kernel/initrd/media removed
   from the domain definition. Capture root-device evidence and an exact
   installation-specific receipt tied to retained inputs from that boot. Installer
   output, a generic login prompt, or a preinstalled image alone cannot pass.
   Negative cases: stop after installer boot, omit disk boot, supply old serial
   output, or return a predecessor receipt; readiness must remain unverified.
4. **Restart/retry:** interrupt before command, after effect but before journal
   update, after completion but before response, and during evidence collection.
   Reopen the executor and retry the same operation. Assert one domain/disk set,
   stable IDs, no repeated format/install, and bounded recovery. Race two executor
   processes, not just goroutines. Ambiguous crash windows retain custody instead
   of blindly recreating or reinstalling resources.
5. **Reinstall predecessor:** require a new Installation naming the durable current
   predecessor. Race two successors; only one may produce effects. Preserve the
   allocation and epoch, change installation evidence, reject late predecessor
   callbacks, and ensure stale serial receipts cannot complete the successor.
6. **Unknown outcomes:** inject timeout, command disconnect, missing/corrupt journal,
   unavailable lookup, absent completion, and mismatched receipt/digest. Do not
   mark ready, advance predecessor, remove finalizers, or admit another consumer.
   Cleanup must not race an unresolved installer or surviving worker.
7. **Exact cleanup and reuse:** verify shutdown and absence of the exact retained
   domain UUID, installer access, and owned disk artifacts before returning clean.
   Preserve decoy domains/files even when names share prefixes. Replace a domain
   under the same name or replace a disk path; reject changed identity. Inject
   partial cleanup and failure to inspect resources; retain custody. A subsequent
   allocation requires a later fence epoch and blank storage; stale cleanup and
   install retries must not touch that allocation.
8. **Path/XML/command injection:** exercise traversal, absolute paths, symlinks,
   path replacement races, dot segments, quotes, XML markup, newlines, shell
   metacharacters, and option-prefixed values in every external-input field.
   Capture argv and parse generated XML; assert literal values, bounded paths,
   and no unexpected device, host filesystem, network, or command access. Unknown
   asset IDs must not create arbitrary domains. Reject requests before effects.
9. **Artifact integrity:** change kernel, installer, disk payload, checksum file,
   or symlink after admission. Require verification against an independently
   retained expected digest before use, with a defined defense against replacement
   between verification and execution. Unsupported/unpinned artifacts, truncated
   content, or a valid checksum for another build must fail closed. Pin/check the
   relationship between the admitted image and the actual installer payload.

Real hardware and firmware, production Milo/IAM/quota/network integration, and
hosted CI qualification remain separate acceptance tracks. The synthetic
[standalone smoke](standalone-smoke.md) cannot qualify OS installation.

## Follow-up: adapter durability and regression coverage

A subsequent independent adapter review identified a missing directory sync
between disk creation and the `prepared` journal transition, plus insufficient
adapter-specific tests. The parent implementation now syncs the created disk and
its operation directory before advancing the journal. Verified serial evidence
files and their directory are also synced before recording the corresponding
verified stage. Journal writes retain their existing root-directory sync.

Private provider fields allow tests to substitute a temporary state root, command
execution, and directory-sync failure without changing the production constructor
or exposing new configuration. The new
[adapter lifecycle tests](../../internal/localvm/lifecycle_test.go) exercise exact
request substitution, reopened journal replay without duplicate commands,
authority advancement only after cleanup, live-domain cleanup rejection, decoy
preservation, stale cleanup replay after reuse, directory-sync failure propagation,
exclusive disk creation after uncertain preparation, and absent/stale/wrong-stage
receipt recovery without repeating the installer. These are filesystem and fake
command tests, not completed guest installation evidence or power-loss tests.

## PXE HTTP evidence correction

Independent review of the PXE implementation corroborated firmware network boot,
DHCP, real installation, and disk-only boot in the saved acceptance run. It also
reproduced a logging defect: a zero-byte failed HTTP write still recorded the
intended response length as admitted delivery. The parent corrected this by
retaining `admitted` as an attempt and adding `write-complete` only when the
response writer reports the entire body written without error. Both adapter and
harness verification now require completed entries. Regression tests cover failed
writes, short writes without errors, and successful writes.

A completed server write does not prove client receipt. The separate installer
and disk-boot receipts remain mandatory. The earlier saved PXE acceptance run
uses the old attempt-only logs; it is not evidence that this corrected logging
path has passed a fresh VM run. Subsequent end-to-end acceptance must capture the
new entries. Neither logging scheme alone proves successful OS provisioning.
