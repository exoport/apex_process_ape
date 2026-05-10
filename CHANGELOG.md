# CHANGELOG

## v0.0.6 (2026-05-10) ⚠️ BREAKING

This release moves pipeline specs out of the binary and adds a first-class
install path for the framework's skills + pipelines + project bootstrap.

### Breaking changes 💥

- **Pipeline specs are no longer embedded into the ape binary.** They now live
  at `<project>/_apex/pipelines/*.yaml` and must be installed before
  `ape pipeline <name>` will work. Existing v0.0.5 installs that ran
  `ape pipeline design` against bare projects will break with:
      pipeline "design" not found at <project>/_apex/pipelines/design.yaml — run
      "ape framework update" to install pipelines from the framework repo

  Migration is one command:
      export APEX_FRAMEWORK_REPO=/path/to/apex_process_framework
      ape framework update

  See [docs/how-to/framework-update.md](docs/how-to/framework-update.md).

- **`LoadSpec(name string)` → `LoadSpec(name, projectRoot string)`.** Internal
  API change in `internal/pipeline`; only relevant if you've imported the
  package directly. Callers that pass an empty `projectRoot` get an explicit
  error with the resolved path.

- **`ape pipeline list` (introduced earlier on this branch) is now `ape pipeline`
  with no positional arg.** `--output-format human|json|yaml` works in list mode
  (no positional). With a name, `ape pipeline <name>` runs the pipeline as
  before. Tab completion still surfaces installed pipelines.

### Features ✨

- **`ape framework update`.** Installs/refreshes the framework's `apex-*` skills
  into `<project>/.claude/skills/` and the canonical pipelines into
  `<project>/_apex/pipelines/`. On first run, opens an interactive Bubble Tea
  prompt to seed `_apex/config.yaml` (project_name + extensions). Headless
  contexts use `--project-name`, `--extensions`, or `--no-bootstrap`.
  Refuses to clobber tracked-but-modified `apex-*` skills without `--force`;
  untracked apex-\* leftovers are safe to overwrite.

- **`ape framework status`.** Reads `<project>/_apex/framework.yaml` and prints
  the installed framework version. With `--repo` or `$APEX_FRAMEWORK_REPO` set,
  fetches the framework HEAD and emits a drift report (hash + tag).

- **`<project>/_apex/framework.yaml`.** New metadata file generated on every
  `framework update` run. Records framework SHA + tag, the ape version that
  performed the install, and the list of installed assets. Should be committed
  alongside the project. Schema:
  [docs/reference/framework-yaml.md](docs/reference/framework-yaml.md).

- **`ape pipeline` (no args).** Lists pipelines installed at
  `<project>/_apex/pipelines/`, with `--output-format human|json|yaml`.

### Internals ⚙️

- New `internal/framework` package implementing the install/status flow:
  copy primitives, git CLI wrappers, metadata schema, two-phase Bubble Tea
  bootstrap TUI, full `Update(ctx, *UpdateOptions)` orchestration. Test
  coverage via `testify/require`: copy primitives, git wrappers against
  ephemeral repos, metadata roundtrip, full Update flow happy path,
  idempotent re-run, stale-skill removal, dirty-framework refusal,
  modified-skill refusal, untracked-skill safe-clobber, missing-subtree
  error, drift detection.

- `internal/pipeline/spec/` (the embedded yaml directory) is gone. The
  three canonical pipelines now live in `apex_process_framework` at
  `framework/_apex/pipelines/` (introduced in framework v0.0.71).

### Documentation 📚

- New [how-to/framework-update.md](docs/how-to/framework-update.md).
- New [reference/pipeline-spec.md](docs/reference/pipeline-spec.md) — formalizes
  the on-disk pipeline YAML schema.
- New [reference/framework-yaml.md](docs/reference/framework-yaml.md).
- New [explanation/why-project-local-pipelines.md](docs/explanation/why-project-local-pipelines.md).
- [how-to/install.md](docs/how-to/install.md) updated with a "next step"
  pointer to `framework update`.

### Compatibility envelope

ape v0.0.6 requires a framework with `framework/_apex/pipelines/` populated
(framework v0.0.71 or later).
