---
plan_id: PLAN-15
created_at: 2026-07-02
status: proposed
tags:
  - new-command
  - scripting
  - yaegi
  - orchestration
summary: New `ape script <file.go>` — run arbitrary Go code in-process via the yaegi interpreter (github.com/traefik/yaegi) with a predefined ape function library injected. Scripts orchestrate ape's primitives deterministically — RunPipeline / RunTask / RunCommand (all PTY-backed, calling the same internal runner the CLI uses), plus logging, args, cost scanning, event publishing, and blob upload — turning multi-run workflows (loops, conditionals, fan-out across component repos) into version-controlled .go files instead of shell scripts around the CLI. Supports an optional `--sandbox` mode that restricts the interpreter to yaegi's sandboxed stdlib symbol set (no os/exec, os.Exit, syscall, unsafe) while keeping the apescript orchestration surface. Exposed through the service as the `script.run` job kind with the same keyed-exclusivity admission; the service can force sandbox mode for remotely submitted scripts.
origin:
  - 2026-07-02 user request (added mid-planning) — "another command 'script' that we are able to run yaegi Go code with a library of predefined functions included in ape, this is for running any arbitrary code."
  - 2026-07-02 user addition — "scripts should support sandbox as an option": default stays unrestricted (arbitrary code), `--sandbox` opts in to the restricted interpreter.
  - Assumptions marked inline were made autonomously (user was mid-flow); flag at review.
---

# PLAN-15: `ape script` — yaegi-interpreted orchestration scripts

## Goal

```
ape script ops/nightly.go -- --target ./component-a
```

runs a plain Go file inside ape's process under yaegi, with an importable
`apescript` library whose functions drive the same code paths as the CLI —
so a script can run a pipeline, inspect its manifest, decide, run a task in a
sibling repo, and publish a summary event, all in one deterministic file
that lives in the project repo.

## Why now

- The service (PLAN-14) gives remote single jobs; scripts give *composed*
  jobs without inventing a YAML workflow language — Go is the workflow
  language, which fits a repo whose users already write Go.
- Every primitive the library needs exists (or lands in PLAN-9…13); this
  plan is mostly surface, not machinery.

## Non-goals

- No OS-level isolation (containers, seccomp, chroot). The `--sandbox`
  option (D5) is an *interpreter-level* restriction — it limits what symbols
  the script can call, not what the ape process itself can do. Default mode
  is arbitrary trusted code, same trust level as the user's shell. (The
  *service-side* exposure is gated separately — PLAN-14 D5.)
- No script package management: single-file scripts (plus yaegi's ability to
  import within GOPATH is explicitly not promised in v1).
- No stable public Go API for `internal/` — the script surface is the
  `apescript` library only, and it is versioned.

## Design

### D1: Command surface

```
ape script <file.go> [flags] [-- script-args...]

  --cwd            project root (as elsewhere)
  --sandbox        run the script in the restricted interpreter (see D5)
  --output-format  wraps the script's return value {result, duration, cost_usd}
  (PLAN-13 flags apply: --nats-url/--nats-creds for events from within runs)
```

Also `ape script -` to read from stdin (enables `script.run` with
`script_source` over the service). Everything after `--` is exposed to the
script as `apescript.Args()`.

### D2: Script contract

A script is a Go file with `func Main(ctx context.Context) error` (assumption:
this shape rather than `package main`/`func main()`, so ape controls the
context, cancellation, and exit-code mapping; yaegi evaluates the file then
calls `Main`). SIGINT cancels ctx; non-nil error → exit 1 with the error
printed; panics recovered and reported with the yaegi stack.

### D3: The `apescript` library

New public package `github.com/diegosz/apex_process_ape/apescript` (public
so scripts get editor autocomplete/type-checking by importing the real module
in their go.mod; at runtime yaegi resolves the import to the in-process
implementation via yaegi's Use/extract mechanism — this dual nature is the
standard yaegi embedding pattern). v1 surface (assumptions; trim in review):

```go
// Orchestration — all PTY-backed, thin facades over the internal runner:
func RunPipeline(ctx, PipelineOpts) (RunResult, error) // name, prompt, from, noCommit, cwd…
func RunTask(ctx, TaskOpts) (RunResult, error)         // PLAN-11 semantics
func RunCommand(ctx, CommandOpts) (RunResult, error)   // PLAN-12 semantics
// RunResult: {RunID, ManifestPath, Status, CostUSD, PerModel, CommitSHAs, Duration}

// Introspection:
func ReadManifest(path string) (Manifest, error)
func ScanTranscript(path string) (cost.ScanResult, error) // PLAN-10 shape
func Skills(cwd string) ([]SkillInfo, error)              // resolved skills/agents

// Plumbing:
func Log(format string, args ...any)      // structured, respects --quiet
func Args() []string                       // after --
func PublishEvent(subject string, v any) error // PLAN-13 publisher, if configured
func PutBlob(ctx, r io.Reader) (Digest, string, error) // PLAN-13 store, if configured
```

Interpreter setup: `interp.New(interp.Options{Unrestricted: !sandbox})`,
`Use(stdlib.Symbols)`, `Use(apescriptSymbols)` (generated with yaegi's
`extract` tool, regenerated via a make target). Default (no `--sandbox`) is
unrestricted — full stdlib, "arbitrary code" per the request. Of note: runs
launched by a script use the same `RunOptions` plumbing, so events,
manifests, telemetry, and commits behave exactly as CLI-launched runs.

### D4: Service integration

`script.run` (PLAN-14) executes `ape script` as a child like any job —
`script_path` (must resolve inside an allowlisted root) or `script_source`
(stdin variant; gated by `allow_script_source`). The service can force
`--sandbox` onto every script job via `service.yaml: force_script_sandbox:
true` (recommended default when `allow_script_source` is enabled).
Exclusivity applies to the *script job as a whole*; runs the script starts
inherit the child's process group so `job.stop` kills the whole tree.

### D5: `--sandbox` mode

Interpreter-level restriction using yaegi's sandbox: `interp.Options{
Unrestricted: false}` (the yaegi default), under which the loaded stdlib
symbol set blocks the dangerous surface — `os/exec`, `os.Exit`,
`syscall`, `unsafe`, process-manipulation helpers — while pure computation,
strings/encoding/regexp, and read-side `os`/`io` remain available. The
`apescript` orchestration functions stay fully available in sandbox mode:
they are the *intended* side-effect channel (spawning claude via the
supervised runner, publishing events, uploading blobs), and each already has
its own guardrails (preflight, allowlist, commit gates). What sandbox mode
removes is the script's ability to bypass ape and touch the system directly.
Restrictions are documented per-symbol-group in `reference/apescript.md`.

## Steps

1. Dependency + spike: `go get github.com/traefik/yaegi`; validate the
   `Main(ctx)` contract and stdlib symbols against a trivial script
   (yaegi has known gaps on some reflect-heavy code — the spike pins what we
   promise), and verify exactly which symbols the `Unrestricted: false`
   sandbox blocks (that empirical list becomes the D5 documentation).
2. `apescript` package: types + facades over internal (this forces the small
   `internal → apescript` boundary layer; internal packages stay internal).
3. Symbol extraction + `apecmd/script.go` + stdin mode.
4. Tests: golden scripts under `testdata/scripts/` (args echo, Log capture,
   ReadManifest on a fixture, a RunTask against the repl bash stand-in,
   ctx-cancellation, panic recovery; sandbox: a script calling `os/exec`
   fails with a clear symbol-not-allowed error under `--sandbox` and
   succeeds without it, while the same script's `RunTask` call works in
   both modes).
5. Service `script.run` wiring (lands with or after PLAN-14).
6. Docs: `how-to/write-ape-scripts.md` + `reference/apescript.md` (generated
   from doc comments).

## Acceptance

- `ape script testdata/scripts/hello.go -- a b` prints script output, exit 0;
  args visible; `-` stdin variant identical.
- A script chaining `RunTask` → `ReadManifest` → conditional second `RunTask`
  works against the fixture project.
- Ctrl-C mid-script cancels the in-flight run (PTY session killed, manifest
  finalized as stopped) and the script's ctx.
- A compile error in the script reports file:line from yaegi, exit 1, no
  claude spawn.

## Risks / notes

- **yaegi limitations** (generics support is partial, some stdlib corners
  differ): the spike (step 1) decides whether to document restrictions or
  reconsider. Fallback direction if yaegi disappoints: `go run` delegation
  with `apescript` as a real library and NATS-loopback for in-process
  coupling — noted, not planned.
- The `apescript` public package is a compatibility surface; keep it minimal
  and semver-honest (additive until v1.0).
- Binary size will grow noticeably (yaegi + stdlib symbols, ~10–20 MB).
  Accepted for a single-binary tool; measure in the spike and record.
