# Milo acceptance GitHub settings

The workflow-level trust gate is necessary but not sufficient. GitHub repository
and organization settings own credential release and runner isolation. Configure
them before adding `MILO_ACCEPTANCE_CONFIG` or placing kubeconfigs on a runner.

## Required state

- Repository environment: `milo-acceptance`.
- Deployment branches: selected branches only, exactly `main`; tags are denied.
- Required reviewers: `ecv` and `kevwilliams`; prevent self-review is enabled.
- Organization runner group: `fleet-milo-acceptance`, restricted to the selected
  repository `datum-cloud/fleet` rather than all repositories.
- Runner-group workflow access: `restricted_to_workflows` is `true`, and
  `selected_workflows` contains exactly
  `datum-cloud/fleet/.github/workflows/milo-acceptance.yml@refs/heads/main`.
- Runner labels: `self-hosted`, `datum-cloud-fleet`, and
  `fleet-milo-acceptance`. Do not place those labels on a shared runner.
- The runner must execute no workflows from forks or other repositories and must
  hold no credentials until every setting above has been independently verified.

The machine-readable desired state is
`.github/milo-acceptance-policy.json`. The designated reviewer user IDs reviewed
on 2026-11-04 are `ecv` (`684695`) and `kevwilliams` (`1724763`). IDs are inputs
to GitHub's environment API; logins remain the human review surface.

## Verification

An administrator with repository environment and organization runner-group read
access must capture and review these responses:

```sh
gh api repos/datum-cloud/fleet/environments/milo-acceptance
gh api repos/datum-cloud/fleet/environments/milo-acceptance/deployment-branch-policies
gh api orgs/datum-cloud/actions/runner-groups
gh api orgs/datum-cloud/actions/runner-groups/REPLACE_WITH_GROUP_ID
gh api repos/datum-cloud/fleet/actions/runners
```

Verify the environment has a required-reviewer protection rule containing the
two designated users, `prevent_self_review: true`, and a custom deployment branch
policy containing only `main`. Verify the named runner group selects only
`datum-cloud/fleet`, reports `restricted_to_workflows: true`, and has exactly the
single main-branch Milo workflow in `selected_workflows`; then verify the assigned
runner has every required label and is not registered to another repository or
shared group. Compare these live API responses with every field in
`.github/milo-acceptance-policy.json`; the checked-in policy is desired state,
not evidence that the live settings are safe.

Run the repository-side validation after reviewing the settings:

```sh
task milo:validate
```

Only after an administrator records and independently reviews the live responses
may they attach the non-secret config path as `MILO_ACCEPTANCE_CONFIG` and place
the owner-only kubeconfig files on the dedicated runner. A dispatch from a tag or
non-main branch must fail in `trust-gate` without requesting the protected
environment or the self-hosted runner.

## Current observed state

Read-only inspection on 2026-11-04 found no `milo-acceptance` repository
environment (the API returned `404`) and no repository-level self-hosted runner.
Organization runner-group inspection returned `403` because the inspecting token
lacked `admin:org` or fine-grained runner-group permission. Therefore live
credentials remain intentionally unattached and the workflow cannot run its
protected job yet. Issue #28 is not closeable until an administrator applies and
verifies the environment, runner-group, selected-workflow, and runner settings.
