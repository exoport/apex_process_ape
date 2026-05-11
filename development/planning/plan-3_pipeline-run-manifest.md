---
plan_id: PLAN-3
created_at: 2026-05-11
implemented_at: 2026-05-11
status: done
tags:
  - pipeline
  - on-disk-artifacts
  - per-step-metrics
  - observability
summary: Persist every `ape pipeline <name>` run as a structured on-disk artifact under `<project_root>/_output/pipelines/<name>/<run_id>/`. The artifact is a YAML manifest (canonical schema) plus per-step `.ndjson` captures of the raw stream-json claude emits, plus a human-readable Markdown report rendered from the manifest. The terminal `result` event in claude's stream-json carries cost / token / duration / num_turns totals that ape currently throws away after the TUI dies; this plan tees them to disk and surfaces them in the report. Always-on, no flag, ~few KB plus the NDJSON size per step. Unblocks the eval (apex_process_framework_eval PLAN-9) which can then attribute cost / tokens per skill in regression runs.
origin:
  - 2026-05-11 session in apex_process_framework_eval after the v0.0.8 smoke. T2/T3 in that conversation surfaced "we cannot see per-skill cost/tokens during an ape pipeline run."
  - Handoff prompt mirrored at apex_process_framework_eval/_output/ape-plan-pipeline-run-manifest.md; eval-side consumer drafted at apex_process_framework_eval/development/planning/plan-9_ape-pipeline-run-manifest-consumer.md.
---

# PLAN-3: Pipeline run manifest + per-step metrics capture

## Goal

After every `ape pipeline <name>` invocation — TUI mode, `--no-tui`, or eval-harness mode — produce an immutable on-disk record of the run that exposes per-skill metrics + per-skill raw event stream. The record lives under the project's own tree (`_output/pipelines/`) so users can browse it without leaving the project; the eval can read the same record without parsing TUI output.

End state: closing the TUI does not destroy any data. CI logs can link to the report path. The eval consumer reads `manifest.yaml` deterministically.

## Why now

PLAN-1 / PLAN-2 settled the live UX. PLAN-3 closes the durability gap: today the metrics live only in TUI memory or stdout-stream lines that scroll past. The eval-side PLAN-9 cannot ship without this artifact, and real-project users have been asking for a "what did that run cost" answer they can grep.

## Scope — IN

### M1: Manifest schema + types

- New file `internal/pipeline/manifest.go`. Defines exported types matching the schema below. Hand-rolled YAML encoding via `gopkg.in/yaml.v3` (already imported by `spec.go`); we control both the producer and the consumer, so no marshal/unmarshal asymmetry.

  ```yaml
  schema_version: 1
  ape_version: 0.0.9
  pipeline:
    name: design
    source: _apex/pipelines/design.yaml
    digest: sha256:...
  project_root: /home/foo/myproject
  run_id: 20260511-094530-a0d06c8
  started_at: 2026-05-11T09:45:30Z
  ended_at: 2026-05-11T10:38:12Z
  duration_seconds: 3162.4
  status: completed
  totals:
    cost_usd: 4.83
    tokens_input: 412334
    tokens_output: 28910
    tokens_cache_read: 187420
    tokens_cache_creation: 9211
    steps_run: 13
    steps_failed: 0
  stages:
    - index: 1
      name: prd
      started_at: 2026-05-11T09:45:30Z
      ended_at: 2026-05-11T09:58:11Z
      duration_seconds: 760.5
      status: completed
      steps:
        - index: 1
          skill: apex-create-prd
          agent: apex-agent-pm
          args: ""
          prompt: ""
          model: ""
          started_at: 2026-05-11T09:45:30Z
          ended_at: 2026-05-11T09:58:11Z
          duration_seconds: 760.5
          status: completed
          exit_code: 0
          cost_usd: 1.42
          tokens_input: 84012
          tokens_output: 8910
          tokens_cache_read: 41208
          tokens_cache_creation: 2811
          num_turns: 47
          events_path: stages/01-prd/step-01-apex-create-prd.ndjson
  ```

- Status enum: `completed | failed | cancelled`. `failed` is set when a step's exit code is non-zero or claude's terminal `result` event has `subtype != "success"`. `cancelled` is reserved for context-cancel (PLAN-2 / F1 quit path).
- `schema_version: 1` is the first stable contract. Future additions: new fields are backward-compatible; deletions or semantic changes bump major.

### M2: `internal/pipeline/manifest_writer.go` — disk writer

- Computes `<run_id> = YYYYMMDD-HHMMSS-<short-hash>`; short-hash is the first 7 chars of a SHA-256 of `(time.Now().UnixNano(), pipeline.name, project_root)`. Cheap dedupe under concurrent invocations.
- Creates `<project_root>/_output/pipelines/<pipeline-name>/<run_id>/` and the `stages/01-<name>/` skeleton ahead of time so per-step writes are atomic.
- Maintains a `latest` symlink at `<pipeline-name>/latest -> <run_id>` (atomically: write to `latest.tmp`, rename). Symlink creation is best-effort; on Windows it falls back to writing `latest.txt` with the run_id.
- Atomic file writes: write to `path.tmp` then `os.Rename`. Keeps the tree readable mid-run.
- `WriteManifest()` is called both at start (partial, `status: running`) and at end (finalized).
- `OpenStepEventLog(stage, idx, skill) -> io.WriteCloser` returns a writer for the per-step `.ndjson`. The runner tees `runClaude`'s line stream into it. On close, the writer's path is recorded in `step.events_path`.
- `Finalize(status)` writes the final manifest, renders `pipeline-report.md`, returns the manifest path.

### M3: `internal/pipeline/result_event.go` — terminal `result` parser

- Scans the accumulated step output for the terminal NDJSON event with `"type":"result"`. Decodes via `encoding/json` into a struct mirroring claude's payload:

  ```go
  type resultEvent struct {
      Type          string  `json:"type"`
      Subtype       string  `json:"subtype"`
      DurationMS    int64   `json:"duration_ms"`
      NumTurns      int     `json:"num_turns"`
      TotalCostUSD  float64 `json:"total_cost_usd"`
      Usage         struct {
          InputTokens             int `json:"input_tokens"`
          OutputTokens            int `json:"output_tokens"`
          CacheReadInputTokens    int `json:"cache_read_input_tokens"`
          CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
      } `json:"usage"`
  }
  ```

- Returns `nil, nil` if no result event present (degraded — manifest still written, step metrics zeroed). Returns an error only on malformed JSON in what claims to be a result event.

### M4: `internal/pipeline/runner.go` — integration

- `RunOptions` adds optional `ManifestDir string` (defaults to `<ProjectRoot>/_output/pipelines`) and `DisableManifest bool` (escape hatch for tests / one-off invocations that don't want to litter the tree). Production code never sets `DisableManifest`.
- `runClaude` extends to accept an `eventLog io.Writer`. After each scanned line, the existing call to `observer.OnStepLine` stays; the new `eventLog.Write(line + "\n")` tees the same line to disk. Writer is allowed to be nil (test path).
- `Run` constructs a `manifestWriter` after preflight, writes the initial partial manifest, then for each step: opens the event-log writer, runs the step, parses the terminal result event from the returned output, records the step record, closes the writer, persists. On stage/pipeline completion: finalize.
- On context cancellation: the in-flight manifest is finalized with `status: cancelled`; outer return path unchanged.

### M5: Report renderer

- `internal/pipeline/report.go` renders `pipeline-report.md` from a finalized `Manifest`. Plain Go `text/template`; no Markdown dependency. Sections:
  - Header: pipeline name, ape version, started/ended/duration, overall status.
  - Totals table: cost, tokens (in/out/cache), steps run/failed.
  - Per-stage table: name, duration, status, step count.
  - Per-step table: skill (+ agent), duration, cost, status, link to NDJSON.
  - Failure detail (if any): which step failed, its tail (last 20 lines of the NDJSON).

### M6: CLI surface

- `internal/apecmd/pipeline.go` end-of-run: prints `📊 report: _output/pipelines/<name>/<run_id>/pipeline-report.md` on stdout (TUI and `--no-tui` both). In `--no-tui --quiet`, prints the path on its own line after the existing summary.
- New flag: `ape pipeline --manifest-dir <path>` (optional) to override the default `_output/pipelines` location. Useful for the eval and for users who want pipeline runs in a non-default place. Documented in `pipeline-spec.md`.

### M7: Tests

- **Unit (manifest.go):** round-trip a fully populated Manifest through YAML; parse + reserialize; assert byte-identical (modulo whitespace).
- **Unit (result_event.go):** parser handles success/failure subtypes; ignores non-result events; returns nil-nil on absent.
- **Unit (manifest_writer.go):** writer creates expected directory layout; atomic-rename behavior; symlink updates point at the latest run_id.
- **Integration (runner with shim):** an existing test shim (`internal/pipeline/runner_test.go` uses one) replays a canned stream-json sequence. Extend it to include a real terminal `result` event with known totals. Assert the on-disk manifest matches.
- **Integration (cancel path):** context-cancel mid-run; assert `status: cancelled` in the partial manifest; assert the partial NDJSON is still readable.

### M8: Docs

- New `docs/reference/pipeline-run-manifest.md` — schema reference, example, "how to read it" snippet.
- Update `docs/reference/pipeline-spec.md` with a "Runs are recorded" cross-link.
- Update `README.md` quick-start to mention "every run leaves a manifest at `_output/pipelines/`."
- `CHANGELOG.md` v0.0.9 section.

## Scope — OUT

- Per-step **context-pressure** metric (not in stream-json today; defer).
- Per-step **judge scoring** (the eval owns scoring; ape just provides metadata).
- `ape pipeline diff <run-a> <run-b>` CLI (plausible follow-up).
- A `--manifest-only` no-render mode (cosmetic; add later if asked).
- Backfilling pre-v0.0.9 runs (none exist on disk; nothing to backfill).

## Verification plan

1. `make lint` zero issues. `go test ./...` clean.
2. Run `ape pipeline test` (existing testdata) against a tmp project; assert `_output/pipelines/test/<run_id>/` tree is well-formed.
3. Smoke `ape pipeline design` against a fresh `ape-gf-hello-world` style sandbox; eyeball the rendered `pipeline-report.md`; confirm step totals look plausible vs the TUI's last-known values.
4. Cancel mid-run with `q` + `y`; confirm the partial manifest exists with `status: cancelled` and the in-progress step's NDJSON has a non-empty tail.
5. Hand the resulting manifest to the eval (PLAN-9) once shipped; confirm round-trip ingestion.

## Release / coordination

- Target ape v0.0.9. After implementation lands on `main`, cut the tag, run the release workflow, push the cosign-signed artifacts.
- The eval bumps `apex_eval/.bin/ape` to v0.0.9 and the `ape_min_version` for ape-mode fixtures, then unblocks its PLAN-9. Coordinate the cut so PLAN-9 can land within the same week.

## Open caveats / risks

- **Big NDJSON files.** A long step (`apex-create-prd` ~13 min, ~47 turns) can produce hundreds of KB of NDJSON. Acceptable in `_output/` (gitignored), but worth a sentence in docs that says "delete `_output/pipelines/<old-run>/` to reclaim disk." No automatic cleanup in v1.
- **Atomic-rename on Windows.** `os.Rename` over an existing file errors on some Windows filesystems. The writer falls back to `os.Remove + os.Rename` on Windows; covered by a unit test.
- **Concurrent runs against same project.** Two `ape pipeline` invocations get different `run_id` but race on the `latest` symlink; the last finalizer wins. Acceptable.
