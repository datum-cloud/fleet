# Working in Fleet

Read `README.md`, `docs/design.md`, and the relevant pinned source proposal before
changing subsystem behavior. The repository currently contains planning documents;
the API names in them are provisional, and there are no build or test commands yet.

## Development conventions

- Keep implementation decisions and their rationale in repository documentation.
  Distinguish proposed contracts from implemented and verified behavior.
- Add source directories, dependencies, generation, and CI when the first concrete
  implementation needs them. Do not infer a module path or backend choice from
  illustrative names in the proposals.
- Preserve separate physical asset, allocation, installation, and managed node
  identities. Keep one authoritative writer for each configuration surface.
- Use the same allocator and installation contracts for standalone and managed
  consumers. Keep networking, IPAM, routing, and service policy with their owners.
- Retain operation authority and exact inputs before effects. Treat uncertain
  execution, cleanup, or verification as unknown; do not convert it to readiness.
- Keep adoption observational until an explicit ownership transition. Creating or
  importing records must not trigger provisioning or expose occupied capacity.
- Keep credentials, kubeconfigs, rendered secret-bearing configuration, and local
  provider token files out of Git. Use synthetic data for examples and fixtures.
- Validate documentation links and whitespace for documentation changes. When
  code is introduced, document its actual commands and add tests for relevant
  concurrency, authorization, lifecycle, and recovery behavior.
