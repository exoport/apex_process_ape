---
plan_id: PLAN-8
created_at: 2026-05-22
status: proposed
tags:
  - interactive-exec
  - refactor
  - dependency-removal
  - windows-support
  - cross-platform
summary: Replace ape's external `tmux` dependency with an in-process pseudo-terminal driver (`internal/repl`, backed by `github.com/aymanbagabas/go-pty` — Unix PTY on Linux/macOS, ConPTY on Windows incl. Git Bash). Pipeline interactive exec and `ape chat` both stop shelling out to a `tmux` binary; bytes go `Write → PTY master → kernel → child stdin`, the same delivery path tmux `send-keys` used. `CapturePane` is upgraded to feed PTY output through a VT-grid emulator so the returned text is ANSI-free and byte-equivalent to tmux's rendered output (consumer-facing parity). `ape chat` switches to direct stdio inheritance — it loses Ctrl-B-D detach but gains cross-platform support and removes the tmux dependency. Lands as a single drop-in PR; the proof binary `apepty v0.0.13-pty` already validates the API-compatible package swap end-to-end against a real sandbox project.
origin:
  - 2026-05-21 user research request — investigate 3mux as a tmux alternative; the deeper finding was that 3mux is structurally unfit (no scripting surface, dormant project, no Windows) but the right question is "do we need a multiplexer at all?" The answer for ape is no — we used tmux as a programmable PTY harness, and an in-process PTY does that better.
  - 2026-05-22 sandbox proof. Parallel build `/home/diegos/_dev/github/diegosz/apex_process_ape_pty/` mirrors ape with `internal/tmux` → `internal/repl` and renamed binary `apepty`. Compiles, full test suite passes under `-race`, three PTY-driven repl smoke tests pass against a real bash stand-in. User confirmed `apepty v0.0.13-pty` works against a real sandbox project.
  - PLAN-6 (the tmux pivot) abandoned an earlier PTY design — but that one delivered prompts as `await_message` MCP **tool-call return values**, so claude's CLI never saw a leading `/`. The new PTY design writes bytes to the master end (= keystrokes on the child's stdin), identical in shape to tmux `send-keys -l`. PLAN-6's failure mode does not apply. Detail in [`_output/implementation-notes.html`](../../_output/implementation-notes.html) under "Surprises".
  - User decisions (2026-05-22, captured during plan drafting): drop external live attach with no replacement; integrate VT-grid emulator in CapturePane now (not deferred); drop `ape chat` detach with no replacement; land as a single drop-in PR.
---

# PLAN-8: Migrate `tmux` → in-process PTY

## Goal

Remove the runtime `tmux` dependency from ape. After PLAN-8 lands, `ape` is a single self-contained binary that works on:

- Linux (any distro, no `tmux` package install required)
- macOS (no Homebrew tmux required)
- **Windows 11 native, including Git Bash, PowerShell, and cmd.exe** — currently impossible because tmux is POSIX-only and there is no Windows build.

The interactive runner (`ape pipeline <name> --tui` / `ape chat`) keeps every behaviour that's load-bearing in PLAN-6 + the PLAN-6 follow-up fixes: per-stage `claude` REPL, slash-command delivery as real keystrokes, `/clear` between steps, `Stop`-hook-driven step completion, idle timeout, step-tag-aware hook routing. The bridge runtime, manifest writer, per-step commit machinery, runlog format, `--web` and `-P` modes — all untouched.

What disappears: external `tmux attach -t ape-<stage>-<pid>` for live debugging of a running stage, and `Ctrl-B-D` detach in `ape chat`. Both confirmed acceptable by the user. The `_output/ape/runs/<id>/stages/<n>/events.ndjson` per-step event log already covers most live-debug needs; users wanting persistence past terminal disconnect can wrap `ape chat` in an external multiplexer.

## Why now

- **Sandbox proof exists and works.** `apepty v0.0.13-pty` is built, tested, and end-to-end-confirmed by the user. The structural risk has already been paid.
- **PLAN-6's PTY-failure mode does not apply.** The previous PTY attempt failed because it routed prompts through an MCP tool return; this design writes keystrokes to the child's stdin, just like tmux `send-keys` does. Empirically validated by the `TestSendCommandOrderingWithClear` test in `internal/repl/repl_test.go`.
- **Windows support is a strict superset.** Today every Windows user must either install tmux under WSL2 (which means leaving Windows-native development) or skip interactive mode entirely. PLAN-8 makes ape work in their native shell with no migration on their end.
- **Single external dependency removed.** The pre-flight requirement for `tmux` on PATH disappears. The release artifact set already builds windows/amd64 and windows/arm64 zips — those become functional instead of producing a binary that errors on first interactive invocation.

## Non-goals

- **Multi-pane / tiled UI.** Single REPL per stage as today. No layout features beyond the fixed 200×50 grid.
- **Session persistence across `ape` restart.** Sessions die with the parent process. If you need a session to survive `ape` exiting, wrap externally.
- **Reproducing tmux's scripting surface.** No `send-keys`-as-a-public-command, no socket protocol, no scripting language. The new package is internal-only.
- **External live attach.** Dropped per user decision; documentation pointing at `tmux attach` is removed.
- **`ape chat` detach.** Dropped per user decision; help text recommends external multiplexing if needed.

## Scope — IN

### F0: Sandbox proof carryover

Mechanical port of the sandbox proof from `/home/diegos/_dev/github/diegosz/apex_process_ape_pty/` into the main repo. Single commit on a `plan-8-pty-migration` branch. The branch is the PR.

**Steps.**

1. Add dependency: `go get github.com/aymanbagabas/go-pty@v0.2.3` (pulls in `github.com/creack/pty v1.1.24`, `github.com/u-root/u-root` for u-root's TTY helpers).
2. Create `internal/repl/repl.go` and `internal/repl/repl_test.go` from the sandbox proof (already complete; ~280 LOC + ~140 LOC of tests).
3. Delete `internal/tmux/` (one .go file, one _test.go).
4. In `internal/pipeline/interactive.go`: change `import ".../internal/tmux"` → `internal/repl`; rename every `tmux.X` call to `repl.X` (6 sites); update one stderr debug tag from `[tmux/...]` to `[pty/...]`. No call-site signature changes — the package API is byte-identical.
5. In `internal/apecmd/chat.go`: see FB for the rewrite (chat takes a different path from pipeline).
6. `go mod tidy`; `make test`; `make build`; verify the resulting `./ape` binary runs and the test suite is green.

**Tests.** Both the existing PLAN-6 interactive tests (white-box on `assembleInteractivePromptLine` / `buildInteractiveArgv`) and the new `internal/repl/repl_test.go` smoke tests (lifecycle / clear-ordering / literal-text against a real bash PTY). Skip the repl smoke tests on `runtime.GOOS == "windows"` — see FD for the Windows test story.

**Acceptance.** `make test` green, `make build` produces a single binary, the binary's `--help` listing is unchanged (no command added, no command removed), `go.mod` shows the new direct dependency `github.com/aymanbagabas/go-pty`.

**Risk.** Lowest in the plan — this is a structural port of code the user has already validated end-to-end. If anything regresses, it's the small set of behaviours the next phases address.

### FA: VT-grid `CapturePane`

The sandbox proof returns raw PTY bytes from `CapturePane`, including ANSI control sequences (cursor moves, color codes, line clears). Today's downstream consumers (`diffPaneSnapshot`, `WaitForReady`'s glyph check) tolerate the noise — both rely on substring search. But two things are degraded vs the tmux variant:

- **Debug output** under `APE_INTERACTIVE_DEBUG=1` becomes much noisier.
- **Manifest `step-out` capture** (`recordStep`'s output buffer) contains escape sequences instead of rendered text. If anything reads these manifests for human display, the text is harder to read.

User decision (2026-05-22): integrate a VT-grid emulator now so `CapturePane` returns ANSI-free rendered text, matching tmux's `capture-pane -p` semantics byte-for-byte for the same prompt sequence.

**Implementation.**

- Add VT emulator dependency. Two viable candidates:
  - `github.com/hinshun/vt10x` — small, focused, used by `cdr/coder` and others. Last release 2024; still works. Pure Go.
  - Charm's `github.com/charmbracelet/x/cellbuf` / sibling `parser` packages — already a transitive dep, actively maintained. Less proven as a full VT emulator but the cell-buffer primitives exist.

  **Recommend `vt10x`** — its API is purpose-built for "stream bytes in, ask for the grid", which is exactly what we need. Charm's primitives are lower-level and would require us to write the state machine ourselves. Re-evaluate if Charm publishes a higher-level VT package before this lands.
- In `internal/repl/repl.go`, change the per-session pump goroutine: instead of appending raw bytes to a `[]byte`, feed them through `vt10x.Terminal.Write`. The terminal keeps the grid in memory.
- `CapturePane(ctx, name)` reads the grid by walking rows top-to-bottom: for each row, take cells 0..width-1, trim trailing spaces, append `\n`. Return the joined string. ANSI-free, deterministic shape.
- `WaitForReady` and `diffPaneSnapshot` continue to work — they're substring-based, and the rendered grid contains the literal text. `diffPaneSnapshot` actually becomes more reliable because the trailing-line anchor no longer competes with cursor-positioning bytes.

**Tests.**

- Extend `internal/repl/repl_test.go: TestCapturePaneNoANSI`: drive a bash with prompts that emit explicit color escapes (`echo -e '\033[31mhello\033[0m'`), then `CapturePane` and assert (a) `hello` appears and (b) no byte in the output is in the C0/CSI range (`< 0x20` except `\n`).
- Extend `TestSendCommandOrderingWithClear` to assert the captured pane contains exactly `/clear` and `/apex-some-skill` substrings without surrounding escapes.
- White-box test in `internal/repl/`: feed a known byte sequence with cursor-up redraws into the pump, verify the captured grid reflects the final state (not the intermediate redraw layers).

**Pre-work — VT library evaluation (one afternoon, blocks the rest of FA).**

Before committing to `vt10x` the implementation must verify:

1. **Scrollback support.** tmux's `capture-pane -p -S -` returns the full history, not just the visible grid. claude's per-step output frequently exceeds 50 rows (the fixed pane height), so off-screen lines must be retrievable. `vt10x.New` accepts a `vt10x.Option` for buffer size — verify it's adequate (need ≥ ~10k lines) or wrap in a parallel scrollback ring.
2. **`❯` glyph rendering.** The ReadyGlyph is a multi-byte Unicode char. Verify the emulator's cell model preserves it (cell-as-rune, not cell-as-byte).
3. **claude-emitted escapes.** Capture one real `_output/ape/runs/<id>/stages/0/events.ndjson` from a sandbox stage's hook stream, replay it through the emulator, and confirm the grid looks sane (no missing tool-call lines, no cursor corruption).
4. **API ergonomics.** Concurrency safety on `Write` vs `String`; the pump goroutine writes, `CapturePane` reads. tmux's lock is implicit; vt10x's must be checked.

If `vt10x` fails any of (1)–(4), fall back to `github.com/charmbracelet/x/cellbuf` primitives plus a hand-rolled CSI state machine. The fallback is ~300 LOC vs vt10x's zero-LOC, so the bias is toward making vt10x work.

**Acceptance.** `CapturePane` output for the PLAN-6 / B6 smoke run (`ape pipeline pattern-governance --tui` against the sandbox `greeter` project) is **semantically equivalent** to the tmux variant's output for the same run — same lines in same order, modulo trailing-whitespace and per-line line-ending differences (compare with `diff --strip-trailing-cr --ignore-trailing-space`). `APE_INTERACTIVE_DEBUG=1` traces no longer contain any byte in the `\x1b[…` CSI escape range. The `diffPaneSnapshot` tail-anchor matches exactly across `n` consecutive step captures in a 10-step stage.

**Risk.** Moderate. The VT emulator must handle every escape claude's REPL emits. `vt10x` covers the ECMA-48 / xterm subset; if claude uses an exotic escape (private modes, OSC commands like setting window title), the emulator may either ignore it cleanly (fine) or corrupt the grid (bad). Mitigation: the golden-file regression test in the pre-work step. If golden-file replay diverges from the live capture by more than whitespace, that's the signal to switch libraries.

### FB: `ape chat` rewrite — stdio inheritance, no detach

`ape chat` today spawns claude inside a tmux session, then `exec`s `tmux attach`. The tmux variant's chat help advertises Ctrl-B-D detach and reattach-later semantics. PTY can't replicate that without writing a small daemon process — and per user decision, the feature is dropped.

**Implementation.**

- Rewrite `runChat` in `internal/apecmd/chat.go` (the sandbox proof has the working version).
- Bridge runtime construction + `Listen` / `Serve` goroutine unchanged.
- Spawn claude as `exec.CommandContext` with `cmd.Stdin/Stdout/Stderr = os.Stdin/Stdout/Stderr`. ape already holds the user's TTY; claude inherits it as a real controlling terminal without any PTY layer in the middle.
- `cmd.Run()` blocks until claude exits. ExitError from claude itself is treated as a clean exit (same as the tmux variant's "tmux attach exits non-zero when claude exits" path).
- Update `cmd.Long` help text:
  - Drop the `Ctrl+B D detach` line.
  - Add a note: "Wrap `ape chat` in `tmux` / `screen` externally if you need detach/reattach."
- Drop the unused `time` import that was only there for `WaitForReady`'s context timeout.

**Tests.** No process-level test for chat (it's an interactive command). Manual smoke in the smoke matrix below.

**Acceptance.** `ape chat` against a project with `_apex/config.yaml` opens a claude REPL, hooks are captured to `_output/ape/chats/<id>/`, `/exit` or Ctrl-D ends the session, `ape` exits cleanly. Works on Linux, macOS, and Windows Git Bash.

**Risk.** Low — direct stdio inheritance is the standard pattern for "spawn an interactive subcommand". The only sharp edge is signal forwarding (Ctrl-C in claude should kill claude, not just ape's outer loop); `exec.CommandContext` handles this correctly because the foreground process group contains both.

### FC: Documentation + in-code comment sweep

Eight docs files reference tmux as a hard dependency or describe interactive mode in tmux terms, plus two Go files have stale tmux-flavoured comments after the package swap. None of these are wrong about the *mechanics* (slash-command delivery via keystrokes, `/clear` between steps, Stop-hook step completion) — only the *who-owns-the-PTY* paragraph needs swapping. Term-level edits, no rewrites.

**Docs.**

| Path                                          | Change                                                                                                          |
| --------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `docs/how-to/interactive-vs-programmatic.md`  | Drop the `tmux attach -t ape-<stage>-<pid>` mid-run line. Drop the "Requires `tmux` on `PATH`" line. |
| `docs/reference/invocation-matrix.md`         | Replace "tmux is required for interactive exec" with "interactive exec has no external runtime dependency". Rewrite the "tmux send-keys" mechanics line. |
| `docs/reference/claude-spawn-modes.md`        | Rewrite the "(tmux)" cells to "(PTY)" across the entire mode table; rewrite the description of `ape chat` to remove `tmux attach`; update the "where in the code" table to point at `internal/repl/` instead of `internal/tmux/`. |
| `docs/reference/step-contract.md`             | Replace every `tmux send-keys` reference with "PTY Write to claude's stdin"; the rest of the contract mechanics is unchanged. |
| `docs/reference/README.md`                    | Update the one-line description of `claude-spawn-modes.md` (it currently says "tmux-hosted `claude` REPL"). |
| `docs/explanation/exec-modes.md`              | Replace "running inside a per-stage tmux session" with "running inside a per-stage PTY"; replace `tmux send-keys` with "writing bytes to the PTY master end (= keystrokes on claude's stdin)"; remove the "Ctrl+B D" / `tmux attach` notes. Keep the rest. |
| `docs/explanation/bridge-architecture.md`     | Update the "PLAN-6 tmux pivot" note to "PLAN-6 tmux pivot → PLAN-8 PTY migration" with one-sentence summary. |
| `docs/how-to/run-artefacts.md`                | Remove the two paragraphs referencing tmux session naming / tmux spawn-and-attach (lines ~34 and ~46). |
| `CHANGELOG.md`                                | New entry for PLAN-8 / vNext: migration, dropped features, Windows support, new direct dependency. |

**In-code comments (no behavioural change, just words).**

| Path                                            | Change                                                                                |
| ----------------------------------------------- | ------------------------------------------------------------------------------------- |
| `internal/pipeline/runner.go`                   | Comments at lines ~102, ~124 — replace "tmux session" / "tmux send-keys" wording.     |
| `internal/bridge/orchestrator/contract.go`      | Comments at lines ~13, ~20 — same.                                                    |
| `internal/pipeline/interactive.go`              | Whole file's doc comments (header + `runStageInteractive` doc) — already touched in F0 for the main body, sweep remaining tmux mentions in comments. |
| `internal/pipeline/interactive_test.go`         | Comments referencing "what the runner types into the tmux pane".                       |
| `internal/repl/repl.go`                         | Already PTY-aware (sandbox-authored). No change.                                       |

**Out of scope** (historical record, deliberately left in place): the `development/planning/plan-6_*.md` body; `_output/implementation-notes-1.html`; `_output/implementation-notes.html` (this work's notes); `development/pending/cost-discrepancy-20260521.md`; any `CHANGELOG.md` history rows below the new PLAN-8 entry.

**Acceptance.** `grep -rn "tmux" internal/ docs/ CLAUDE.md README.md` returns no matches outside the historical-record paths above. `grep -rn "send-keys" internal/ docs/` returns zero matches.

**Disambiguation note for readers.** `internal/sessions/` and the `ape sessions` subcommand are **not** tmux sessions — they're the cross-project registry of live `ape chat` / `ape pipeline` invocations stored in `~/.ape/registry.json` (PLAN-5 / C5). Already cross-platform (`lock_unix.go` + `lock_windows.go`). Untouched by PLAN-8. Spelling this out because "ape sessions" + "tmux sessions" is a likely reader confusion during code review.

### FD: CI matrix expansion

Today `.github/workflows/ci.yml` has three separate jobs (`test`, `lint`, `vuln`), all hardcoded to `ubuntu-latest`. There is no `strategy: matrix` to extend. PLAN-8 converts the `test` job to a matrix; `lint` and `vuln` stay on Ubuntu (their tools are Linux-centric and the benefit doesn't justify multiplying runners).

**Implementation.**

- In `test` job, add:
  ```yaml
  strategy:
    fail-fast: false
    matrix:
      os: [ubuntu-latest, macos-latest, windows-latest]
  runs-on: ${{ matrix.os }}
  ```
- On Windows runners, `make` is provided via the MSYS2 toolchain (`choco install make` is already pre-installed on `windows-latest`), so `make build` / `make test` work without changes. If a sharp edge surfaces, fall back to `go build ./cmd/ape` and `go test -race ./...` in shell-specific steps.
- On Windows, the `internal/repl` PTY smoke tests skip themselves (`runtime.GOOS == "windows"` early-return in `requireUnix`) — the production code path is exercised end-to-end by the `apepty version` smoke step below; the bash-PS1 stand-in just doesn't transplant to ConPTY.
- Add a Windows-only `repl_windows_test.go` with a minimal stand-in using `cmd.exe /c "prompt $G&cmd"` or `powershell.exe -NoLogo -NoProfile` to drive the same lifecycle assertion. Goal: prove `pty.New` + `Resize` + `Read`/`Write` + `Kill` work on ConPTY in CI, even if the prompt-glyph match needs a different sentinel.
- Add a smoke step at end of `test` job: `./ape version` (or `./ape.exe version` on Windows) — catches `//go:build` regressions where a constraint accidentally excludes the build target.
- `lint` and `vuln`: unchanged. `bingo`-pinned `golangci-lint` and `govulncheck` are pure Go and cross-compile; we're choosing not to run them on every OS to keep CI minutes reasonable.

**Acceptance.** PR is green across all three matrix permutations of the `test` job. `lint` and `vuln` pass on Ubuntu.

**Risk.** Low. `aymanbagabas/go-pty`'s ConPTY backend is used in `charmbracelet/wish`, `charmbracelet/soft-serve`, and other production projects. Both `aymanbagabas/go-pty` and its `creack/pty` Unix backend are pure Go (no CGO) — confirmed by file-by-file check of the module cache; `CGO_ENABLED=0` (goreleaser default) keeps working.

### FE: Process-group hardening (deferrable)

The programmatic-exec path's `configureProcessGroup` (`internal/pipeline/proc_unix.go`) installs a `Setpgid` + SIGTERM-then-SIGKILL escalator on cancel. The PTY interactive path doesn't have an equivalent today — `go-pty` calls `Setsid` (which makes the child a session leader, its own pgid = pid) but the cleanup-on-cancel hook isn't installed.

Today this is theoretical: PLAN-6 + follow-up fixes confirm that claude REPL inside an interactive stage doesn't spawn long-lived grandchildren (the orphan-subagent problem F1 solved for programmatic exec was a `claude -p` issue, where Task-tool grandchildren outlived their parent). But the symmetry is worth having if anyone ever extends claude's interactive mode to launch background processes.

**Implementation.**

- New file `internal/repl/proc_unix.go` (build tag `linux || darwin`) with a small `configureSessionCancel` that mirrors `configureProcessGroup`: on `KillSession`, send SIGTERM to the child's pgid, sleep `procGroupKillGrace`, send SIGKILL. `go-pty` already exposes the child's PID via `cmd.Process`.
- `internal/repl/proc_windows.go` (build tag `windows`) is a no-op.
- Wire into `repl.KillSession` before the existing `Process.Kill()` call.
- Test: extend `TestSessionLifecycle` to spawn a bash that backgrounds `sleep 60 &`, then KillSession, then `pgrep sleep` to confirm the grandchild is gone.

**Acceptance.** `pgrep`-based test passes on Linux + macOS; Windows path compiles but doesn't run that test.

**Risk.** Very low (it's defensive). May be split out to a follow-up if the PR's diff is large; not blocking.

## Scope — OUT

- **Switching from `aymanbagabas/go-pty` to another PTY library** (creack/pty fork, photostorm/pty, microsoft/go-pty, custom ConPTY wrapper). go-pty is the most actively maintained cross-platform option in 2026, used by the Charm projects. Re-evaluate only if a concrete blocker emerges.
- **Pluggable PTY backend.** No interface abstraction layer over go-pty. If we ever need to swap, we'll do it then.
- **Reproducing `tmux attach` over a local Unix socket** (an `ape attach <stage>` subcommand). Dropped per user decision.
- **`ape chat --daemon` for detach-style chat.** Dropped per user decision.
- **Per-step idle-timeout retuning under PTY.** Reuse PLAN-6's 30s ready timeout and 300ms PromptSettle. If observed flakiness, address in a follow-up.

## Smoke matrix

Run against `/home/diegos/_dev/ape-web-sandbox/greeter/` (current sandbox state) **and** under Git Bash on a Windows 11 host (new for PLAN-8). Each row should produce the same `_output/ape/runs/<id>/manifest.json` shape and the same per-step stdout in the manifest's `step-out` field as the tmux variant did pre-migration.

| Invocation                                           | Linux  | macOS  | Windows Git Bash | Tmux variant baseline |
| ---------------------------------------------------- | ------ | ------ | ---------------- | --------------------- |
| `ape pipeline pattern-governance --tui`              | green  | green  | green            | green                 |
| `ape pipeline pattern-governance --tui -P`           | green  | green  | green            | unchanged             |
| `ape pipeline pattern-governance --web -P`           | green  | green  | green            | unchanged             |
| `ape pipeline design --print`                        | green  | green  | green            | unchanged             |
| `ape chat`                                           | green  | green  | green            | green (sans detach)   |
| `ape pipeline pattern-governance --tui` with `APE_INTERACTIVE_DEBUG=1` | trace ANSI-free | trace ANSI-free | trace ANSI-free | trace ANSI-free |
| Without `tmux` installed on PATH                     | green  | green  | green            | n/a (tmux missing → failure) |

**Verification command for the "without tmux" row** — actually unset tmux, not just remove its dir from PATH (PATH-filtering misses cases where tmux is at `/usr/bin/tmux` and `/usr/bin` is required for everything else):

```bash
# Linux/macOS: temporarily rename tmux so exec.LookPath fails
sudo mv "$(command -v tmux)" /tmp/tmux.bak
./ape pipeline pattern-governance --tui   # must run end-to-end
sudo mv /tmp/tmux.bak "$(dirname /tmp/tmux.bak)/tmux"

# Or simpler — run in a Docker container with no tmux installed:
docker run --rm -v $(pwd):/work -w /work golang:1.26 \
  bash -c 'go build -o ape ./cmd/ape && ./ape pipeline pattern-governance --tui'
```

After PLAN-8, ape's binary has zero PATH-time external dependencies for its core flows (`git` is still required for commit / framework operations, but that's not interactive-exec-specific). The migration's value proposition is verified by this row.

**Note on the doc claim ape "errors clearly if tmux is not on PATH"** (`docs/reference/invocation-matrix.md:37`): there is no actual preflight check in the code today — the docs over-promise. The error today is the raw `exec: "tmux": executable file not found in $PATH` from `os/exec`, which is functional but not "clear" in any UX sense. PLAN-8 removes the doc line entirely (no preflight needed when no external tool is required), so the pre-existing doc lie self-resolves.

## Acceptance criteria

1. `make build` produces a single binary; `make test` is green on Linux, macOS, Windows.
2. No reference to `tmux` in `internal/`, `cmd/`, `go.mod` (beyond CHANGELOG history).
3. `ape pipeline <name> --tui` works without tmux on PATH — verified with `PATH="$(echo $PATH | tr ':' '\n' | grep -v tmux | paste -sd:)"` or in a Docker container with no tmux.
4. The smoke matrix above is fully green.
5. The PLAN-8 entry in CHANGELOG.md lists: tmux removal, Windows support added, dropped features (external attach, chat detach), and the new `aymanbagabas/go-pty` dependency.
6. `_output/implementation-notes.html` updated with the final tradeoffs taken vs the open questions at draft time.

## PR structure

Even though PLAN-8 lands as a single PR, the phases are organised as separate commits so a reviewer can walk them sequentially. Order is **F0 → FA → FB → FC → FD** with FE optional. Each commit:

| Commit | Phase | Files touched | Tests added | Reviewability |
| --- | --- | --- | --- | --- |
| 1 | F0 — package swap | `internal/repl/*`, `internal/pipeline/interactive.go`, `internal/apecmd/chat.go` (mechanical), `internal/tmux/` deleted, `go.mod`/`go.sum` | repl smoke tests | Read this commit first; it's the structural shape change. |
| 2 | FA — VT emulator | `internal/repl/repl.go` (pump + CapturePane changes), `go.mod`/`go.sum` (vt10x dep), `internal/repl/testdata/*.bytes` (golden files) | new test for ANSI-stripped output | The biggest *new* code in the PR. Review against the FA pre-work verification matrix. |
| 3 | FB — chat rewrite | `internal/apecmd/chat.go` only | none | Smallest commit; obvious diff. |
| 4 | FC — doc/comment sweep | `docs/**`, `internal/pipeline/runner.go` comments, `internal/bridge/orchestrator/contract.go` comments, `CHANGELOG.md` | none | Mostly markdown; skim quickly. |
| 5 | FD — CI matrix | `.github/workflows/ci.yml`, `internal/repl/repl_windows_test.go` | Windows stand-in test | Tightly scoped to CI config. |
| 6 (opt) | FE — process group | `internal/repl/proc_unix.go`, `internal/repl/proc_windows.go`, `internal/repl/repl.go` (call site), `internal/repl/repl_test.go` | grandchild-reaper test | May land in a follow-up if PR is large. |

Squash-merge at land time is fine; the per-commit history lives on the branch ref for archaeology.

## Versioning

Currently `v0.0.13`. PLAN-8 is a meaningful change:

- **Drops a runtime dependency** (`tmux` no longer required).
- **Adds a major platform** (native Windows / Git Bash).
- **Drops two user-visible features** (external attach during a run, `ape chat` Ctrl-B-D detach).

Under strict semver pre-1.0 this is fine to ship as `v0.1.0`; under "all-zero pre-stable" interpretation it could be `v0.0.14`. Pick at tag time. The recommendation here is **`v0.1.0`** — the cross-platform binary is a coherent product story worth marking; future feature additions can resume `v0.1.x`.

## Rollback

Single PR, reverted with `git revert <merge-sha>` (or the per-commit reverts in reverse order if a partial rollback is desired). The migration introduces no data migration, no config schema change, no on-disk format change to `_output/ape/runs/`. The bridge's MCP wire format is unchanged.

If a user reports a regression we can't fix in-PR, the revert path puts every behaviour back exactly as it was. The sandbox proof binary `apepty` remains at `/home/diegos/_dev/ape-web-sandbox/.bin/apepty` for the entire transition window so the user can A/B against it.

## PLAN-6 fixes that need explicit re-verification

The tmux interactive runner took multiple follow-up commits after PLAN-6 to stabilise. None of the fixes live in `internal/tmux/`; they all live in `interactive.go` and ride along. But each one's behaviour deserves a tick on the migration checklist because the PTY's slightly-different timing or buffer semantics could re-expose the original bug:

| Fix | Commit | Verification under PTY |
| --- | --- | --- |
| Idle timeout for stuck stages | `797da9a` | Run a stage with a deliberately slow skill (e.g. a wait); confirm the idle-timeout fires at the configured threshold. |
| Step-tag-aware hook routing in `--web -P` | `e959da5` | Run `--web -P`; confirm `hook-events.jsonl` has `step` field populated correctly. |
| Stage-boundary commits (PLAN-6 / C2 Phase D) | `2b79566` | Run a multi-stage pipeline; confirm per-step commits land at the right boundaries. |
| Transcript-scan telemetry path (`StepTelemetryFn`) | `dc651af` (referenced from `cost-discrepancy-20260521.md`) | Run a stage; confirm cost / token telemetry populates the manifest's `usage` block. |
| `/clear` ordering before first prompt of step `i>0` | already covered by `TestSendCommandOrderingWithClear` in F0 | already green in sandbox proof. |

## Open questions to resolve during implementation

- **Long-prompt submission race.** `PromptSettle = 300ms` was tuned for tmux `send-keys -l`. PTY Write semantics may differ slightly — bytes hit the kernel buffer immediately rather than queued through a tmux command. If a long prompt occasionally truncates or splits, instrument with logs and re-tune. No evidence of this yet.
- **VT emulator escape coverage.** First time we surface a claude escape sequence `vt10x` doesn't handle, the grid corrupts. Mitigation: golden-file regression test captures the actual byte stream from a sandbox stage and asserts the grid stays sane. If `vt10x` proves under-powered, Charm's primitives + a small hand-rolled state machine are the fallback.
- **Process-group hardening (FE) — needed or premature?** Strictly: defensive. Decision can be deferred; if the FE diff inflates the PR, split it to a PLAN-8.1 follow-up.

## Notes

- The sandbox parallel directory `/home/diegos/_dev/github/diegosz/apex_process_ape_pty/` is reference material, not a separate code stream. Once PLAN-8 lands, that directory and its binary `apepty` become historical / debugging tools and can be deleted at the user's convenience.
- Detailed pre-implementation decisions, tradeoffs, surprises, and the PLAN-6 archaeology are in [`_output/implementation-notes.html`](../../_output/implementation-notes.html).
- This plan inherits PLAN-6's interactive runner semantics — the `/clear` between steps, PAT-25 prompt shape, Stop-hook step boundary, idle-timeout, per-step ndjson event log, contract verifier prefix-match. None of those change. The only thing that changes is **who owns the PTY** (in-process Go code via go-pty, vs out-of-process tmux daemon).
