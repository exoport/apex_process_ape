# `apescript` library reference

`github.com/exoport/apex_process_ape/apescript` is the public, versioned
library that scripts run by [`ape script`](../how-to/write-ape-scripts.md)
import to orchestrate ape's primitives. This page is the v1 surface. The
package is **semver-honest**: additive until v1.0.

For the yaegi runtime, the script contract, `--sandbox`, and exit codes,
see [How to write and run an `ape script`](../how-to/write-ape-scripts.md).

## Entry point

A script defines:

```go
func Main(ctx context.Context) error
```

ape evaluates the file, then calls `Main`. The functions below only work
while a script runs under `ape script` (the command installs the runtime
before evaluation); called outside that window they return `ErrNoRuntime`.
The pure introspection helpers — `ReadManifest`, `ScanTranscript`,
`Skills` — need no runtime and work anywhere.

## Orchestration

All three run through the same PTY-backed runners the CLI uses, and return
a [`RunResult`](#runresult) derived from the run's manifest (or prompt
record).

| Function | CLI equivalent | Notes |
| -------- | -------------- | ----- |
| `RunPipeline(ctx, PipelineOpts) (RunResult, error)` | `ape pipeline` | multi-stage |
| `RunTask(ctx, TaskOpts) (RunResult, error)` | `ape task` (PLAN-11) | single skill |
| `RunPrompt(ctx, PromptOpts) (RunResult, error)` | `ape prompt` (PLAN-12) | unattended session |

```go
// PipelineOpts mirrors `ape pipeline`.
type PipelineOpts struct {
	Name     string // pipeline to run
	Prompt   string // forwarded to the prompt_flag step, if any
	From     string // resume at this stage
	NoCommit bool
	Cwd      string // overrides the script's project root when set
}

// TaskOpts mirrors `ape task`.
type TaskOpts struct {
	Skill       string        // required
	Agent       string        // PAT-25 passthrough agent
	Model       string        // e.g. "opus[1m]"
	Args        string        // verbatim skill args
	Prompt      string        // forwarded via PromptFlag
	PromptFlag  string
	NoCommit    bool          // skill-layer --no-commit
	TaskCommit  string        // non-empty commits the whole task with this message
	AllowDirty  bool
	IdleTimeout time.Duration
	Cwd         string
}

// PromptOpts mirrors `ape prompt`.
type PromptOpts struct {
	Text        string        // initial prompt (exclusive with Handoff)
	Handoff     string        // seed from a handoff doc (exclusive with Text)
	Agent       string
	Model       string
	Workflow    bool          // append a "run via a workflow" directive
	Ultracode   bool          // prepend the ultracode keyword
	IdleTimeout time.Duration
	Cwd         string
}
```

### RunResult

```go
type RunResult struct {
	RunID        string             // manifest run_id (or prompt id)
	ManifestPath string             // absolute manifest.yaml path ("" for prompt runs)
	Status       string             // "completed" | "failed" | "cancelled"
	CostUSD      float64
	PerModel     map[string]Totals  // per normalized model id
	CommitSHAs   []string           // full SHAs the run produced (oldest first)
	Duration     time.Duration
}
```

## Introspection

| Function | Returns |
| -------- | ------- |
| `ReadManifest(path string) (Manifest, error)` | parsed run manifest; `path` may be the run dir or the `manifest.yaml` file |
| `ScanTranscript(path string) (ScanResult, error)` | cost/token totals + per-model breakdown of one `.jsonl` transcript (PLAN-10) |
| `Skills(cwd string) ([]SkillInfo, error)` | resolved skills — project-scoped first, then non-shadowed user-scoped |

`Manifest`, `ScanResult`, `Totals`, and `Digest` are type aliases for
ape's internal `pipeline` / `cost` / `blobstore` shapes, so a script sees
the real, documented field sets (see the
[run-manifest reference](pipeline-run-manifest.md)) without importing
internal packages.

```go
type SkillInfo struct {
	Name      string // skill directory name
	Scope     string // "project" | "user"
	Path      string // absolute SKILL.md path
	Framework bool   // managed by ape's framework (apex-*)
}
```

## Plumbing

| Function | Behaviour |
| -------- | --------- |
| `Log(format string, args ...any)` | structured log line to stderr; suppressed by `--quiet` |
| `Args() []string` | the tokens after `--` on the command line |
| `PublishEvent(event string, v any) error` | publishes the identity-stamped subject only (see below) |
| `PutBlob(ctx, r io.Reader) (Digest, string, error)` | content-addressed blob upload; returns digest + locator URI |

### PublishEvent subject

`PublishEvent` publishes **only**:

```
ape.evt.<user>.<project>.script.<run-id>.<event>
```

The caller chooses only the final `<event>` token; the identity-stamped
prefix (`<user>`/`<project>`/`script`/`<run-id>`) is fixed. Scripts cannot
publish arbitrary subjects, which preserves the PLAN-13/PLAN-17
traceability contract (see the [events reference](events.md)). The payload
`v` rides under a `payload` field of the versioned envelope.

`PublishEvent` and `PutBlob` require the NATS flags
(`--nats-url` / `--nats-creds`); without them they return a clear "not
configured" error. Outside a run both return `ErrNoRuntime`.

## Sandbox mode

`ape script --sandbox` runs the interpreter with yaegi's **restricted**
symbol set. This is an *interpreter-level* restriction — it limits the
symbols the script may call, not what the ape process can do.

| Symbol group | Unrestricted (default) | `--sandbox` |
| ------------ | :--------------------: | :---------: |
| pure computation, `strings`, `bytes`, `math`, `sort`, `encoding/*`, `regexp` | ✅ | ✅ |
| read-side `os` / `io` / `fmt`, `time`, `context` | ✅ | ✅ |
| `os/exec` (subprocesses) | ✅ | ❌ blocked |
| `os.Exit` and process-exit helpers | ✅ | ❌ blocked |
| `syscall` | ✅ | ❌ blocked |
| `unsafe` | ✅ | ❌ blocked |
| **`apescript` orchestration** (`RunTask`/`RunPipeline`/`RunPrompt`, `PublishEvent`, `PutBlob`, …) | ✅ | ✅ available |

The apescript surface stays available in both modes: it is the intended,
guard-railed side-effect channel (each function has its own preflight,
allowlist, and commit gates). What `--sandbox` removes is the script's
ability to bypass ape and reach the system directly. A blocked import
fails at evaluation with a clear symbol-not-allowed message and no claude
spawn.

> **yaegi limitation.** The stdlib symbol set tracks yaegi's supported Go
> version (currently the go1.22 surface); some reflect-heavy or very new
> stdlib corners may differ from the compiler. Scripts that stick to the
> `apescript` surface plus common stdlib are unaffected.

## Regenerating the interpreter symbols

The yaegi symbol table for this package is generated and committed under
`internal/apescriptsym/`. Regenerate it whenever the surface changes:

```bash
make apescript-symbols
```
