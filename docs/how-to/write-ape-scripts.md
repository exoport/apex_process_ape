# How to write and run an `ape script`

`ape script` runs a plain Go file inside ape's own process under the
[yaegi](https://github.com/traefik/yaegi) interpreter, with the public
[`apescript`](../reference/apescript.md) library injected. A script drives
the same primitives the CLI does — run a pipeline, task, or prompt (all
PTY-backed), read manifests, scan transcripts, log, publish events, upload
blobs — so multi-run workflows (loops, conditionals, fan-out across
component repos) become one version-controlled `.go` file instead of a
shell wrapper around the CLI.

## The script contract

A script is a Go file that defines exactly one entry point:

```go
package main

import (
	"context"
	"fmt"

	"github.com/exoport/apex_process_ape/apescript"
)

func Main(ctx context.Context) error {
	fmt.Println("args:", apescript.Args())
	return nil
}
```

ape evaluates the file, then calls `Main`:

- A **non-nil error** exits `1` with the error printed.
- A **panic** is recovered and reported with the yaegi source-position
  stack, then exits `1` — it never crashes the ape process.
- **SIGINT** (Ctrl-C) cancels `ctx`, so an in-flight run tears down
  cleanly (the PTY session is killed and its manifest finalized) and
  `Main` observes the cancellation.
- A **compile error** is reported as `file:line` and exits `1` **before
  any claude process spawns**.

The `github.com/exoport/apex_process_ape/apescript` import serves two
purposes: at authoring time your editor type-checks the script and offers
autocomplete; at run time ape resolves the import to its in-process
implementation, so `apescript.RunTask` drives the exact code path the
`ape task` command uses.

## Running a script

```bash
ape script ops/nightly.go -- --target ./component-a
```

Everything after `--` is exposed to the script as `apescript.Args()`.
Read the script from stdin with `-`:

```bash
cat ops/nightly.go | ape script -
```

## A real workflow: task → inspect → conditional task

```go
package main

import (
	"context"
	"fmt"

	"github.com/exoport/apex_process_ape/apescript"
)

func Main(ctx context.Context) error {
	res, err := apescript.RunTask(ctx, apescript.TaskOpts{
		Skill: "apex-create-prd",
		Agent: "apex-agent-pm",
	})
	if err != nil {
		return err
	}
	apescript.Log("PRD run %s finished: %s ($%.2f)", res.RunID, res.Status, res.CostUSD)

	if res.Status != "completed" {
		return fmt.Errorf("PRD run did not complete: %s", res.Status)
	}

	// Only shard the doc if the PRD run succeeded.
	shard, err := apescript.RunTask(ctx, apescript.TaskOpts{Skill: "apex-shard-doc", Args: "--doc prd"})
	if err != nil {
		return err
	}
	apescript.Log("sharded in %s", shard.Duration)
	return nil
}
```

Runs a script launches use the same `RunOptions` plumbing as CLI-launched
runs, so their events, manifests, telemetry, and commits behave identically.

## `--sandbox`: restrict what the script itself can touch

By default the interpreter is **unrestricted** — full stdlib, arbitrary
trusted code, the same trust level as your shell. `--sandbox` switches to
yaegi's restricted symbol set:

```bash
ape script --sandbox ops/untrusted.go
```

Under `--sandbox` the dangerous stdlib surface is blocked — `os/exec`,
`os.Exit`, `syscall`, `unsafe` — so the script cannot bypass ape and touch
the system directly. Reaching for a blocked package fails at evaluation
with a clear message and no claude spawn. The `apescript` orchestration
functions stay **fully available** in both modes: they are the intended,
guard-railed side-effect channel. See the
[per-group rules](../reference/apescript.md#sandbox-mode).

> `--sandbox` is an **interpreter-level** restriction, not OS isolation.
> It limits what symbols the script can call, not what the ape process can
> do. For OS-level isolation, run ape inside `ape sandbox`.

## Output formats

`--output-format json|yaml` wraps the invocation in a small envelope on
stdout — `{result, duration, cost_usd}`, where `cost_usd` is the summed
cost of every run the script launched — and diverts the script's own
stdout to stderr so the envelope stays machine-parseable:

```bash
ape script --output-format json ops/nightly.go
```

## Publishing progress and uploading blobs

With the NATS flags configured (`--nats-url` / `--nats-creds`, same as
`ape pipeline`), a script can publish its own events and upload blobs:

```go
_ = apescript.PublishEvent("target-done", map[string]any{"target": "component-a"})
digest, uri, _ := apescript.PutBlob(ctx, reportReader)
```

`PublishEvent` publishes **only** the identity-stamped subject
`ape.evt.<user>.<project>.script.<run-id>.<event>` — the caller chooses
just the final `<event>` token; the identity prefix is fixed so script
events stay attributable. Without NATS configured both return a clear
"not configured" error.

## Exit codes

| Code | Meaning                                                        |
| ---- | ------------------------------------------------------------- |
| 0    | `Main` returned nil                                           |
| 1    | `Main` returned an error, panicked, or a launched run failed  |
| 2    | usage or read error (no file, bad flags)                      |

## See also

- [`apescript` library reference](../reference/apescript.md) — the full
  v1 surface, per-symbol-group sandbox rules, and type shapes.
- [How to run a single skill with `ape task`](run-a-single-skill.md).
- [How to run an unattended session with `ape prompt`](run-a-prompt-session.md).
