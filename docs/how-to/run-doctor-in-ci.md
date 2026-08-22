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
| `config.resolved FAIL`                 | `_apex/config.yaml` or `_apex/config.local.yaml` exists but does not parse | Fix the YAML the message names. This one is **required**: every other project-data check resolves its paths through it, so a broken config would otherwise make five checks report clean against the wrong tree. |
| `config.resolved INFO`                 | The checkout is not an APEX project                                     | Expected outside a project. INFO never fails.                                                          |
| `memory.size FAIL`                     | `team-memory.md` is past the 200 KiB hard ceiling                       | **Required, and intended to fail.** The file is approaching Claude Code's 256 KiB Read cap, past which the retrospective that writes it can no longer read it. Compact it; the soft gate should have caught this several runs earlier. |
| `memory.size WARN`                     | Over the 40 KiB soft budget                                             | Compaction is due at the next epic close. Nothing is broken yet.                                        |
| `registry.drift WARN`                  | A record is on disk but absent from `index.yaml`, or vice versa          | `ape registry verify --all` lists them; `ape registry sync --all` repairs what a tool can.              |
| `story.frontmatter WARN`               | Stories are missing extension-gated keys, or carry a type mismatch      | `ape story verify` lists them. Frontmatter is authored, so these are fixed by hand.                    |
| `sprint.divergence WARN`               | A tracker row and a story file disagree                                 | `ape sprint check` names both sides. Neither is assumed correct — a person decides, which is why this can never be more than a warn. |
| `migration.pending WARN`               | A legacy `deferred-work.md` has not been converted to record files      | `ape framework update` runs it, or `ape deferred migrate --dry-run` to look first. Nothing is committed either way. |

## Project-data checks

Six checks report on the project's own records rather than on the host:
`config.resolved`, `registry.drift`, `story.frontmatter`,
`sprint.divergence`, `memory.size` and `migration.pending`. All six degrade
to INFO outside a project root — absence of a project is not a finding.

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
ape doctor --skip config.resolved,registry.drift,story.frontmatter,sprint.divergence,memory.size,migration.pending
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
