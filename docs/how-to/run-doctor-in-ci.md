# How-to — run `ape doctor` in CI

Gate a CI job on local-environment prerequisites by running `ape doctor` with `--strict --output-format json` and inspecting the exit code (and optionally the JSON payload). Doctor is purely diagnostic — no side effects, no network — so it's safe to run early in a job.

## Minimal invocation

```bash
ape doctor --strict --output-format json
```

Exit codes:

- `0` — every required check passed and no warnings (under `--strict`, any WARN is treated as failure).
- `1` — at least one required check failed, or at least one WARN was raised under `--strict`.

Drop `--strict` if you want WARN-level findings (e.g. "node not on PATH" for a pipeline you don't run) to remain advisory rather than fail the job.

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
ape doctor --strict --output-format json --skip node.binary,npx.binary,playwright.host_supported
```

The canonical names are stable across minor releases. Inspect the current set with:

```bash
ape doctor --output-format json | jq -r '.checks[].name'
```

## Running one check on its own

`--only` is the inverse: it runs just the checks you name, so a single verdict can be scripted as its own gate.

```bash
ape doctor --only hooks.contract_drift --strict --cwd "$PROJECT"
```

That is the observational read of hook drift, against a project you have really run. (`make check-hooks` answers the same question the other way round — it seeds its own runlog with one unattended session, so it never depends on finding such a project.) The two flags compose — `--only` selects, then `--skip` removes from the selection.

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
| `claude.binary FAIL`                   | claude not installed on the runner                                      | Install Claude Code in a setup step, or pin a base image that already has it.                          |
| `playwright.host_supported WARN`       | Runner OS not yet on the allowlist (e.g. fresh Ubuntu LTS)              | Skip the check if you don't run Excalidraw-rendering pipelines; otherwise pin to an older `ubuntu-*`. |
| `framework.metadata WARN`              | Repo doesn't have `_apex/framework.yaml` committed                       | Run `ape framework setup` in a prior step or commit the metadata.                                     |
| `operating_rules.fragment FAIL`        | A project that manages operating rules lost `_apex/apex-operating-rules.md` or the `CLAUDE.md` managed import | Run `ape framework update`. (Legacy / older-framework installs report WARN, not FAIL — see below.)   |
| `permissions.home_claude WARN`         | Container runs as a user without write access to `~/.claude`            | Mount or create the dir owned by the runner UID.                                                      |
| `cost.price_table_coverage SKIP`       | No Claude Code transcripts on the runner — the price table cannot be checked against real usage | Expected in CI, and SKIP never fails `--strict`. This check is meaningful on a developer machine; the release gate runs it as `make check-prices`. |
| `hooks.contract_drift SKIP`            | No runlogs under `_output/` from the last 30 days — the hook contract cannot be checked | Expected in CI, and SKIP never fails `--strict`. Only a project ape has actually run in can answer this read; the release gate sidesteps that by seeding its own runlog — `make check-hooks`. |
| `hooks.contract_drift WARN`            | The Claude Code that wrote your most recent run has stopped sending a hook field ape's step-completion gates read | The gates are now silently inactive — a run whose agent yields while a spawned agent is outstanding can again be reported as a success having done nothing. `ape update`; if it persists on the latest ape, report it. |
| `framework.command_surface FAIL`       | The installed framework requires ape commands this binary does not provide | **Required — fails the run.** From framework v0.11.0 the skills shell out to ape with no fallback branch, so each missing command fails a skill mid-stage. The message lists every one. `ape update`; if they are still missing on the latest, the framework requires an ape that does not exist yet. |
| `framework.command_surface SKIP`       | The framework ships no `_apex/ape-commands.yaml` | It predates the contract (pre-v0.11.0). Not a failure. |
| `aboard.skill_reference WARN`          | A `.claude/skills/aboard/` reference copied into this project was generated for a different `capsHash` than this binary serves | The board's renderers are compiled into ape and the skill is a COPY, so the two drift independently. An agent reading a stale one can write state no renderer reads — and the write still reports success, which is why nothing else catches it. Regenerate: `ape aboard capabilities --format md > .claude/skills/aboard/references/reference.generated.md`. |
| `aboard.skill_reference SKIP`          | No aboard skill reference copied into this project | The normal state — most projects never copy it, and absence is not drift. |
| `runs.legacy_layout WARN`              | Run artifacts still at the pre-`{output_folder}/ape` paths | ape's cost rollups and hook-contract check read a project with no history until they move. Nothing is lost — `ape framework update` relocates them, reporting rather than overwriting any collision. |
| `config.resolved FAIL`                 | `_apex/config.yaml` or `_apex/config.local.yaml` exists but does not parse | Fix the YAML the message names. This one is **required**: every other project-data check resolves its paths through it, so a broken config would otherwise make five checks report clean against the wrong tree. |
| `config.resolved INFO`                 | The checkout is not an APEX project                                     | Expected outside a project. INFO never fails.                                                          |
| `memory.size FAIL`                     | `team-memory.md` is past the 200 KiB hard ceiling                       | **Required, and intended to fail.** The file is approaching Claude Code's 256 KiB Read cap, past which the retrospective that writes it can no longer read it. Compact it; the soft gate should have caught this several runs earlier. |
| `memory.size WARN`                     | Over the 40 KiB soft budget                                             | Compaction is due at the next epic close. Nothing is broken yet.                                        |
| `registry.drift WARN`                  | A record is on disk but absent from `index.yaml`, or vice versa          | `ape registry verify --all` lists them; `ape registry sync --all` repairs what a tool can.              |
| `story.frontmatter WARN`               | Stories are missing extension-gated keys, or carry a type mismatch      | `ape story verify` lists them. Frontmatter is authored, so these are fixed by hand.                    |
| `sprint.divergence WARN`               | A tracker row and a story file disagree, or a row key is non-conforming | `ape sprint check` names both sides. Neither is assumed correct — a person decides, which is why this can never be more than a warn. A `sprint.nonstandard_row_key` finding is the one with a mechanical fix: the row is keyed `N-M` with no slug, so it names no story file *and* the two live epic-projection implementations count it differently. |
| `sprint.lock_ignored WARN`             | `sprint-status.yaml.lock` is not gitignored, or is already committed    | `ape sprint reconcile` takes an advisory lock on that sidecar and never unlinks it, so it sits beside the tracker waiting for a `git add -A`. Add `*.lock` to `.gitignore`; if it is already committed, `git rm --cached` it too, because ignoring a tracked file changes nothing. |
| `output.ape_ignored WARN`              | The resolved `{output_folder}/ape/` is not gitignored, or is already committed | ape rewrites every manifest, runlog and transcript link under that path on each run. Untracked, they wait in `git status` for a `git add -A`; tracked, every run dirties the tree and the next `ape pipeline` fails its dirty-tree pre-flight. Add the resolved path to `.gitignore`; if it is already committed, `git rm -r --cached` it too, because ignoring a tracked path changes nothing. Reports the **resolved** folder, so a project that renamed `output_folder` is judged on the path it actually writes to. |
| `migration.pending WARN`               | A legacy `deferred-work.md` has not been converted to record files      | `ape framework update` runs it, or `ape deferred migrate --dry-run` to look first. Nothing is committed either way. |

## Project-data checks

Eight checks report on the project's own records rather than on the host:
`config.resolved`, `registry.drift`, `story.frontmatter`,
`sprint.divergence`, `sprint.lock_ignored`, `output.ape_ignored`,
`memory.size` and `migration.pending`. All eight degrade to INFO outside a
project root — absence of a project is not a finding.

`sprint.lock_ignored` and `output.ape_ignored` **report and never write**.
`.gitignore` is the operator's file, and a tool that edits it uninvited is
worse than one that points. Both also separate *committed* from merely
*unignored*, because an ignore line does not untrack anything — on a
project that already committed the path, adding the line changes nothing
at all, which is the failure mode a write-it-for-you fix would have hidden
behind a success message.

Two are **required**, and both for a mechanical reason. `runDoctor`
downgrades a non-required FAIL to WARN, so:

- `memory.size` has to be required or nothing could surface a
  `team-memory.md` that has passed the Read cap. `ape memory check`
  deliberately exits 0 in that state — a failing exit there would abort the
  retrospective at exactly the moment compaction is due — so this row is
  where the breach becomes non-ignorable.
- `config.resolved` has to be required because the other five resolve their
  paths through it. A malformed `config.local.yaml` would otherwise send
  them all at the wrong tree, reporting clean.

The other four are warns by design. Registry drift, frontmatter gaps and
tracker divergence are findings a person acts on; a project that has some
should still be able to run `ape doctor` in CI without a red build. Add
`--strict` when you want them to gate.

To keep a CI gate to host prerequisites only:

```bash
ape doctor --skip config.resolved,registry.drift,story.frontmatter,sprint.divergence,sprint.lock_ignored,output.ape_ignored,memory.size,migration.pending
```

## Operating-rules checks (required, but self-gating)

The `operating_rules.fragment`, `operating_rules.import`, and
`operating_rules.orchestrator_skill` checks are **required** — a FAIL exits `1`
even without `--strict`. But they self-gate on `sources.operating_rules.managed`
in `framework.yaml`, so they only hard-fail when an install that *manages* the
operating rules has lost the fragment, the `CLAUDE.md` import, or the
`apex-orchestrator` skill (a real regression). Outside a framework project they
are INFO; on a legacy install or an older framework that predates the fragment
they are a WARN nudge (run `ape framework update`), never a hard failure. So a
plain repo without APEX installed stays green.

The `operating_rules.import` check is *syntactic* — it confirms the managed
`@import` line is present in `CLAUDE.md`; it does not prove Claude Code resolved
it at runtime. To verify resolution, drive a real session and check `/memory`.

## Related

- [Pipeline spec reference](../reference/pipeline-spec.md) — what each check guards against at pipeline-run time.
- [How to install ape](install.md) — used by the GitHub Actions snippet above.
