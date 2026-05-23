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
          curl -fsSL https://github.com/diegosz/apex_process_ape/releases/latest/download/ape_linux_amd64.tar.gz \
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
| `permissions.home_claude WARN`         | Container runs as a user without write access to `~/.claude`            | Mount or create the dir owned by the runner UID.                                                      |

## Related

- [Pipeline spec reference](../reference/pipeline-spec.md) — what each check guards against at pipeline-run time.
- [How to install ape](install.md) — used by the GitHub Actions snippet above.
