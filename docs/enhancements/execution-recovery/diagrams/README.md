# Execution and recovery diagrams

These diagrams illustrate the [proposed execution contract](../README.md), not
implemented components. Graphviz DOT files are the editable source; SVG files are
checked-in renderings. No shared documentation index is modified.

- [Execution path](execution-path.svg), from [DOT](execution-path.dot): authority,
  local effects and the only permitted result return path. PhysicalMaintenance
  uses the same broker/journal for technician permits, not a second executor;
  passive field work has no BMC/boot prerequisite.
- [Operation state](operation-state.svg), from [DOT](operation-state.dot): unknown
  and stopping retain custody; only evidence-bearing settlement can become terminal.

From the repository root, with Graphviz installed:

```sh
dot -Tsvg docs/enhancements/execution-recovery/diagrams/execution-path.dot -o docs/enhancements/execution-recovery/diagrams/execution-path.svg
dot -Tsvg docs/enhancements/execution-recovery/diagrams/operation-state.dot -o docs/enhancements/execution-recovery/diagrams/operation-state.svg
git diff --check
```

Validate XML, local Markdown targets/anchors and pinned source paths after edits.
Render SVGs to PNG with an SVG renderer and inspect labels/arrows for clipping.
Graphviz layout may differ by version; do not treat byte equality across versions
as a protocol test. The failure matrix in the proposal is future acceptance
work, not tests performed by rendering these diagrams.
