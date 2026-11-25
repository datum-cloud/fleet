# Local VM site: proposed adversarial checks

[Local VM site](local-vm.md) · [Operations guide](README.md)

Status: **proposed**. This page lists checks to apply to the [local VM
site](local-vm.md). None has been run as written.

Earlier reviews examined the prototype that the cutover to Device custody
deleted: its Secret-backed coordinator, executor client, simulator store and
single-plane executor. Their findings were fixed in that code and are not
retained, because the code is gone. The site that replaced it keeps per-Device
fences, a journal, and results keyed by operation and fence, so several checks
below now map to the site framework's tests, such as
`TestStoreRefusesOlderFencesAndSurvivesARestart` and
`TestAResumedBootIsObservedNotRepeated`. They do not replace a live run.

## Checks

These are proposed checks, not results. Run destructive cases only
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
   Restart the worker and retry the same operation. Assert one domain/disk set,
   stable IDs, no repeated format/install, and bounded recovery. Race two worker
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

Real hardware and firmware, production Milo, IAM, quota and network
integration, and hosted CI qualification remain separate acceptance tracks. The
synthetic [standalone smoke](standalone-smoke.md) cannot qualify OS
installation.
