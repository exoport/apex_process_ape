# How-to — run `ape doctor` in CI

Gate a CI job on local-environment prerequisites by running `ape doctor` with `--strict --output-format json` and inspecting the exit code (and optionally the JSON payload). Without `--fix`, doctor is purely diagnostic — no side effects, no network — so it's safe to run early in a job.

## Minimal invocation

```bash
ape doctor --strict --output-format json
```

Exit codes:

- `0` — every required check passed and no warnings (under `--strict`, any WARN is treated as failure).
- `1` — at least one required check failed, at least one WARN was raised under `--strict`, or `--only` named a check that does not exist.

Which project rows run depends on the project at `--cwd`:

- **APEX v1 project** (`_apex/state/` or `development/project.md`): the host rows, the framework rows that apply to any install (`skills.*`, `framework.output_styles`, `framework.command_surface`, `framework.ape_version`, `framework.tag_moved`, `migrations.pending`), and the v1 rows: `project.check`, `hooks.project`, `hooks.prompt`, `hooks.pre-commit`, `plugins.project`, `claude.version`, `state.gitignore` and `settings.shared`. None of APEX v0's rows run, so `--strict` is green on a healthy v1 project.
- **Anywhere else**: the host and framework rows, the v0 rows, and `apex.v0`, which WARNs in an APEX v0 project that this ape retired the v0 commands its skills call: use the project's pinned v0 ape (`.bingo`), or lift it ([How to lift a v0 project](lift-a-v0-project.md)).

`--only` runs exactly the checks it names, whatever the project. `ape doctor --help` describes every row.

Drop `--strict` if you want WARN-level findings (e.g. `playwright.host_supported` on a runner OS Playwright does not list) to remain advisory rather than fail the job.

## GitHub Actions snippet

```yaml
jobs:
  prereqs:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - name: Install ape
        run: |
          curl -fsSL https://github.com/exoport/apex_process_ape/releases/latest/download/ape_linux_amd64.tar.gz \
            | tar -xz -C /usr/local/bin ape
      - name: Probe environment
        run: ape doctor --strict --output-format json | tee doctor-report.json
      - name: Upload report
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: doctor-report
          path: doctor-report.json
```

The `tee` keeps the JSON for later artifact upload while still letting the exit code propagate. `if: always()` makes sure the report uploads even when the gate fails — that's the file you'll want when debugging.

## Skipping specific checks

In CI you usually know which workflows the job will exercise. Skip checks irrelevant to that job:

```bash
ape doctor --strict --output-format json --skip node.binary,npx.binary,python3.binary,png.converter,playwright.host_supported
```

The canonical names are stable across minor releases. Inspect the current set with:

```bash
ape doctor --output-format json | jq -r '.checks[].name'
```

To keep a gate to host prerequisites only, name them with `--only`. Skipping the project rows is not enough: the framework rows (`framework.ape_version`, `framework.command_surface`, `framework.tag_moved`, `migrations.pending`, `skills.*`) still run and still gate. An APEX v1 project needs Claude Code and git on the host:

```bash
ape doctor --strict --only claude.binary,git.binary,permissions.home_claude
```

`--only` errors on a name this ape does not have, so the list fails loudly if a row is renamed. Add `node.binary,npx.binary` or the sandbox rows (`kvm.available`, `containerd.running`, `kata.runtime`) when the gate is for a machine that needs them.

## Running one check on its own

`--only` is the inverse: it runs just the checks you name, so a single verdict can be scripted as its own gate.

```bash
ape doctor --only hooks.contract_drift --strict --cwd "$PROJECT"
```

That is the observational read of hook drift, against a project you have really run (doctor reads the runlogs under `_apex/state/runs/` in an APEX v1 project, `{output_folder}/ape/` outside one). (`make check-hooks` answers the same question the other way round — it seeds its own runlog with one unattended session, so it never depends on finding such a project.) The two flags compose — `--only` selects, then `--skip` removes from the selection.

The asymmetry between them is deliberate: an unknown name in `--skip` is ignored (the check simply runs, which costs nothing), but an unknown name in `--only` is a **hard error** listing the valid names. A typo there would otherwise select nothing, run zero checks, and exit 0 — a gate reporting success while checking nothing, which is the one outcome a single-check flag must never produce.

## Parsing the JSON

The report's shape is stable:

```json
{
  "checks": [
    {
      "name": "claude.binary",
      "status": "ok",
      "message": "/usr/local/bin/claude",
      "duration_ms": 0
    }
  ],
  "summary": {"ok": 1, "warn": 0, "fail": 0, "skip": 0, "info": 0}
}
```

Useful jq one-liners:

```bash
# Just the failing checks, one per line:
ape doctor --output-format json | jq -r '.checks[] | select(.status == "fail") | "\(.name): \(.message)"'

# Fail the script if ape's update_available check is WARN (i.e., the
# CI image's ape binary is behind):
ape doctor --output-format json | jq -e '.checks[] | select(.name == "ape.update_available" and .status == "warn") | halt_error(1)'
```

## Common findings in CI runners

| Finding                                | Likely cause                                                            | Mitigation                                                                                            |
| -------------------------------------- | ----------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `claude.binary FAIL`                   | claude not installed on the runner                                      | **Required.** Install Claude Code in a setup step, or pin a base image that already has it.            |
| `playwright.host_supported WARN`       | Runner OS not yet on the allowlist (e.g. fresh Ubuntu LTS)              | Skip the check if nothing in the job renders through Playwright; otherwise pin to an older `ubuntu-*`. |
| `permissions.home_claude WARN`         | Container runs as a user without write access to `~/.claude`            | Mount or create the dir owned by the runner UID.                                                      |
| `cost.price_table_coverage SKIP`       | No Claude Code transcripts on the runner — the price table cannot be checked against real usage | Expected in CI, and SKIP never fails `--strict`. This check is meaningful on a developer machine; the release gate runs it as `make check-prices`. |
| `hooks.contract_drift SKIP`            | No runlogs from the last 30 days — the hook contract cannot be checked | Expected in CI, and SKIP never fails `--strict`. Only a project ape has actually run in can answer this read; the release gate sidesteps that by seeding its own runlog — `make check-hooks`. |
| `hooks.contract_drift WARN`            | The Claude Code that wrote your most recent run has stopped sending a hook field ape's step-completion gates read | The gates are now silently inactive — a run whose agent yields while a spawned agent is outstanding can again be reported as a success having done nothing. `ape update`; if it persists on the latest ape, report it. |
| `framework.ape_version FAIL`           | This ape is below the installed framework's `min_ape_version` (`_apex/ape-commands.yaml`) | **Required — fails the run.** Update ape: `ape update`, or, when the project pins ape in `.bingo/ape.mod`, `bingo get github.com/exoport/apex_process_ape/cmd/ape@v<min>`. A dev build or pseudo-version cannot be compared and reports WARN. |
| `framework.command_surface FAIL`       | The installed framework's manifest requires ape commands this binary does not provide | **Required — fails the run.** The message lists every missing command. `ape update`; if they are still missing on the latest, the framework requires an ape that does not exist yet. |
| `framework.command_surface SKIP`       | The framework ships no `_apex/ape-commands.yaml`, or it is an APEX v1 manifest, which declares only `min_ape_version` | Not a failure. In a v1 project `framework.ape_version` is the row that guards the floor. |
| `aboard.skill_reference WARN`          | A `.claude/skills/aboard/` reference copied into this project was generated for a different `capsHash` than this binary serves | The board's renderers are compiled into ape and the skill is a COPY, so the two drift independently. An agent reading a stale one can write state no renderer reads — and the write still reports success, which is why nothing else catches it. Regenerate: `ape aboard capabilities --format md > .claude/skills/aboard/references/reference.generated.md`. |
| `aboard.skill_reference SKIP`          | No aboard skill reference copied into this project | The normal state — most projects never copy it, and absence is not drift. |
| `runs.legacy_layout WARN`              | Run artifacts still at the pre-`{output_folder}/ape` paths | ape's cost rollups and hook-contract check read a project with no history until they move. Nothing is lost — `ape framework update` relocates them, reporting rather than overwriting any collision. |

## APEX v1 project findings

None of the v1 rows is required: a FAIL is reported as WARN, so they gate the job only under `--strict`.

| Finding | Likely cause | Mitigation |
| --- | --- | --- |
| `project.check WARN` | `ape project check` has findings: an empty `development/project.md` section, a malformed record, and so on. The message shows the first three | `ape project check --cwd <root>` lists every finding with its fix. `ape project codes` explains each code. |
| `hooks.project WARN` | ape's hooks are missing from, or altered in, `.claude/settings.json` | `ape project init` merges them back, keeping existing keys and hooks. `ape task` sessions get them inline either way. See the [hooks reference](../reference/hooks.md). |
| `hooks.prompt WARN` | The UserPromptSubmit hook (`ape hook user-prompt`) is missing from, or altered in, `.claude/settings.json` | Without it, a person act typed in a Claude Code session is refused, because no prompt is recorded to grant it. `ape project init` restores it. |
| `hooks.pre-commit WARN` | git's pre-commit hook does not run ape's secret scan: it is absent (git does not version hooks, so a fresh clone has none), it is another tool's hook, or it is not executable | Absent: `ape project init` installs ape's. Another tool's: add the line `ape hook pre-commit \|\| exit 1` to it (ape never edits a hook it did not write). Not executable: `chmod +x .git/hooks/pre-commit`. Until then, a commit ape does not make is not scanned for secrets until the archive. |
| `plugins.project WARN` | A plugin `_apex/config.yaml` lists is not installed at project scope at its ref, or `.claude/settings.json` does not declare and enable it | `ape doctor --fix` installs or updates each plugin through the `claude plugin` CLI, writes the settings and commits them as "apex: plugins". It is the only fixer. SKIP when the config lists no plugins. |
| `claude.version WARN` | `claude --version` printed nothing | Reinstall Claude Code. OK otherwise: the version is reported only. |
| `state.gitignore WARN` | `_apex/state/`'s whitelist is broken: `milestones.json` or `decisions.json` ignored, ape's run records, logs or counters not ignored, or something else there tracked | Restore `_apex/state/.gitignore` to the whitelist ape writes (`*`, `!.gitignore`, `!milestones.json`, `!decisions.json`), and `git rm --cached` whatever is tracked there besides those two records. |
| `settings.shared WARN` | `.claude/settings.json` or `.claude/skills/` is ignored by git, so ape's hooks or the framework's skills reach no other checkout | Un-ignore them. `ape project init` writes the shape that shares them: `.claude/*`, `!.claude/settings.json`, `!.claude/skills/`. |

## APEX v0 project findings

These rows run only outside an APEX v1 project. In an APEX v0 project, run the commands below **with the project's pinned v0 ape** (`.bingo`): this ape retired them. Its docs are at https://github.com/exoport/apex_process_ape/tree/v0.10.0/docs.

| Finding | Likely cause | Mitigation (with the project's pinned v0 ape) |
| --- | --- | --- |
| `apex.v0 WARN` | The project is an APEX v0 one | Use its pinned v0 ape, or lift it: `ape framework update` to a v1 release runs `ape project lift` ([How to lift a v0 project](lift-a-v0-project.md)). |
| `framework.metadata WARN` | Repo doesn't have `_apex/framework.yaml` committed | `ape framework setup` in a prior step, or commit the metadata. |
| `operating_rules.fragment FAIL` | A project that manages operating rules lost `_apex/apex-operating-rules.md` or the `CLAUDE.md` managed import | `ape framework update`. Required, but self-gating: it hard-fails only when the install manages the operating rules (`sources.operating_rules.managed` in `framework.yaml`); a legacy or older install gets a WARN, and a repo without APEX gets INFO. `operating_rules.import` and `operating_rules.orchestrator_skill` work the same way. |
| `config.resolved FAIL` | `_apex/config.yaml` or `_apex/config.local.yaml` exists but does not parse | Fix the YAML the message names. **Required**: the other project-data rows resolve their paths through it. INFO outside a project. |
| `memory.size FAIL` | `team-memory.md` is past the 200 KiB hard ceiling | **Required, and intended to fail.** Compact it. Over the 40 KiB soft budget it WARNs instead. |
| `registry.drift WARN` | A record is on disk but absent from `index.yaml`, or vice versa, or an entry lacks a field its schema requires | `ape registry verify --all` lists them; `ape registry sync --all` and `ape registry backfill --all` repair them. |
| `story.frontmatter WARN` | Stories are missing extension-gated keys, or carry a type mismatch | `ape story verify` lists them. Frontmatter is authored, so these are fixed by hand. |
| `sprint.divergence WARN` | A tracker row and a story file disagree, or a row key is non-conforming | `ape sprint check` names both sides. A person decides which is right. |
| `output.ape_ignored WARN` | The resolved `{output_folder}/ape/` is not gitignored, or is already committed | Add the output folder (`_output/` by default) to `.gitignore`; if it is already committed, `git rm -r --cached` it too. |
| `migration.pending WARN` | A legacy `deferred-work.md` has not been converted to record files | `ape framework update` runs it, or `ape deferred migrate --dry-run` to look first. |

The v0 project-data rows (`config.resolved`, `config.folders`, `registry.drift`, `story.frontmatter`, `sprint.divergence`, `output.ape_ignored`, `memory.size`, `migration.pending`) degrade to INFO outside a project root, and `output.ape_ignored` reports and never writes. To keep a gate on a v0 project to host prerequisites, use the same `--only` list as above, adding the host rows that project's skills need (`node.binary` and `npx.binary`, for example; each row's remediation says which skills use it). A `--skip` list would still leave the Required `operating_rules.*` and framework rows gating.

## Related

- [How to install ape](install.md) — used by the GitHub Actions snippet above.
- [Hooks reference](../reference/hooks.md) — the hooks `hooks.project`, `hooks.prompt` and `hooks.pre-commit` check.
- [CLI reference](../reference/cli.md) — `ape doctor` flags.
