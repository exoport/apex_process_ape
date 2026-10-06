# AGENTS.md

Guidance for coding agents working in this repository.

## This file, and why there is no CLAUDE.md

This repository's instructions live here and only here. Claude Code (2.1.277+)
loads a root `AGENTS.md` natively, but **only when no `CLAUDE.md` exists** in the
working directory or any parent: when both exist it reads `CLAUDE.md` alone,
and this file silently stops applying.

**Never create a `CLAUDE.md` in this repository.** Two ordinary actions create
one without asking:

- `ape framework setup` silently writes a repo-root `CLAUDE.md` carrying the
  framework's managed block when none exists — correct in a framework
  PROJECT, wrong here. This repository is not one.
- Claude Code's `/init` writes a `CLAUDE.md`.

Either one, run by habit, turns these instructions off with no error.
`make check-agents-md` (part of `make check-harness`) asks the installed Claude
Code a question only this file answers, from the repo root with every tool
disabled, and fails if it cannot.

The PROJECTS ape installs the framework into are different: they keep
`CLAUDE.md`, and ape's managed block, install and `ape doctor` checks are about
theirs, not this file.

## Project overview

`ape` is a single-binary Go CLI that runs APEX framework pipelines on projects. It is a CLI only — no infrastructure services, no docker-compose stacks, no private module dependencies. The repo is intentionally focused.

### Directory map

| Path                      | Purpose                                                                                             |
| ------------------------- | --------------------------------------------------------------------------------------------------- |
| `cmd/ape/`                | Binary entry point (`main.go`).                                                                     |
| `internal/apecmd/`        | Cobra command definitions: pipeline, adr, pattern, trait, update, etc.                              |
| `internal/apecmd/aboard.go` | The `ape aboard` **mount** — a whole command tree from the separate public `github.com/exoport/aboard` module, added rather than ported. Owns the three things hosting requires: the `Host`/`Argv0` identity, aboard's exit table surviving ape's `ExitCode`, and the `PersistentPreRun` shadow that keeps ape's update notice out of board output. Both hosts drive the same `.aboard/` and must produce an identical `capsHash`. |
| `internal/pipeline/`      | Pipeline runner and pre-flight checks. Specs are **not** embedded — they load from `<projectRoot>/_apex/pipelines/*.yaml` (v0.0.6; see `docs/explanation/why-project-local-pipelines.md`). |
| `internal/repl/`          | The PTY that drives `claude`: spawn + keystrokes + a rendered pane. Owns the ready-signal vocabulary (`bypass permissions on`, `❯`), the pre-REPL modal table, `ScrubClaudeCodeEnv`, `CLAUDE_CODE_EFFORT_LEVEL` (explicit overrides only; see `internal/effort`), `APE_SESSION` (the nesting marker every spawned session carries; the session-starting commands refuse under it), `CLAUDE_CODE_DISABLE_BG_SHELL_PRESSURE_REAP` (set on every spawn: Claude Code otherwise kills a running background shell on a memory-pressure event, which an unattended run cannot see), `CLAUDE_CODE_FORK_SUBAGENT=0` (set on every unattended spawn: Claude Code otherwise launches every Agent call async in an interactive session, `run_in_background: false` included), `CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS=30000` (the per-Read token cap, pinned on every spawn in the environment AND in `--settings` `env`, because a project or user settings `env` overrides the environment), and `screen` — the charmbracelet/x/vt grid every pane read comes from (it replaced hinshun/vt10x, whose misreading of claude 2.1.269's kitty-keyboard query desynchronised the whole grid; vt10x survives in test code as `vt10xOracle`, a second opinion every captured byte fixture must agree with). `TestLive_ClaudeCodeContract` (opt-in, `make check-claude`) checks all of it against the installed Claude Code. |
| `internal/claudeprobe/`   | The once-per-Claude-Code-version startup check `ape task`/`pipeline`/`prompt` run before their first spawn: `repl.ProbeClaude` (a zero-token spawn in an untrusted scratch dir, through the trust dialog to a REPL showing both ready signals), cached per (claude version, ape version) in the user cache dir. Broken stops the run with pane + raw bytes; undetermined (claude drew nothing) warns and continues uncached; `APE_CLAUDE_PROBE=off` skips, loudly. Exists because nothing reacted to a claude auto-update — 2.1.269 broke every spawn two days after v0.0.68 shipped green. `make check-claude`'s `startup_probe` subtest is what keeps the check from misjudging a working claude. |
| `internal/claudesurface/` | The reviewed record of the installed Claude Code's surface — its tool list (from a `-p` init event), the `CLAUDE_*` names and model ids in its binary, and how far its public CHANGELOG has been read — committed as `testdata/claude-surface.json`. `make check-claude-surface` fails on a tool-list change, on a variable ape sets vanishing, on a model id new to the binary, or on unread CHANGELOG entries that touch agents, background work, hooks, tools or settings; `make update-claude-surface` records the review. Exists for the change nobody knew to assert: TaskOutput's removal (2.1.277) and the forced-async Agent default both had CHANGELOG entries nothing read; Sonnet 5.5 (2.1.284) had none at all, only a new id in the binary. |
| `internal/hookdrift/`     | Detects Claude Code dropping the hook-payload fields the step-completion gates read. Sweeps every runlog under a project's `_output/`, and scopes the verdict to the harness version that wrote the newest run so an upgrade cannot mask fresh drift. Two ways in: `ape doctor --only hooks.contract_drift --cwd <project>` reads a real project's runlogs (observational, and skips when there are none), while `make check-hooks` seeds its own corpus with one `ape prompt` session so it always returns a verdict. |
| `internal/outputstyles/`  | The framework-owned per-skill output-style table (`_apex/output-styles.csv`), read by `ape task`. Same shape and tolerance as `internal/contract`. Deliberately **not** consulted for pipeline steps: it is keyed by skill, a stage is a chain of skills sharing one spawned process, and `--settings` is fixed at launch — so a stage with divergent per-skill styles could only honour one, silently. Pipelines declare `output-style:` at pipeline or stage level instead, which is the granularity a spawn can deliver. |
| `internal/effort/`        | The reasoning effort a spawn gets, from the framework's `_apex/effort-defaults.yaml` (model family → effort, plus a fallback). `effort.Decide` is the one place the rule lives: an explicit step/stage/pipeline `effort:` or `--effort` goes out as `CLAUDE_CODE_EFFORT_LEVEL`, process-wide; otherwise the table rides in `--settings` as `modelSettings.<model>.effortLevel` plus a top-level fallback, so each model, sub-agents included, gets its family's row; with no table, the legacy `xhigh`, process-wide. A family word matches only the model it currently aliases, so the keys are the word plus every known id of the family. Measured on claude 2.1.283 with values the machine's own user settings could not produce. `ape config effort` reports it. |
| `internal/contract/`      | The framework-owned *terminal* contract (`_apex/terminal-contracts.csv`) — did a run reach its summary step. Unrelated to the PTY contract above, despite the name. |
| `internal/tui/`           | Bubble Tea two-panel TUI.                                                                           |
| `internal/output/`        | Output-format helpers (human / json / yaml).                                                        |
| `internal/cost/`          | Transcript → USD: the embedded price table (`prices.yaml`), model-id normalization + family aliases, per-run rollups, price-table coverage, and `reprice`. Prices are hand-curated — there is no price API — so `prices.yaml` is data, not Go literals, and `make check-prices` gates it against the local Claude Code. The same rows carry each model's **context window**, curated the same way and from a documented source (`GET /v1/models` → `max_input_tokens`); `TestContextWindow_GenerationBoundary` locks the values so a change has to be deliberate. |
| `internal/updatecache/`   | Cache layer for the background update-check.                                                        |
| `internal/trait/`         | Trait inspection helpers.                                                                           |
| `internal/apexcfg/`       | Resolves `_apex/config.yaml` + the `config.local.yaml` overlay into the framework's folder variables. Every project-data command routes through it. |
| `internal/registry/`      | The record registries (ADRs, patterns, features, capabilities): verify, sync, update. `sync` **withholds** a removal it cannot prove is a phantom — a headerless record claims no id, so its index entry is the last copy of its metadata — and `restore-headers` is that repair, writing the entry back as frontmatter. |
| `internal/story/` · `internal/sprint/` | Story frontmatter projection/verification, and `sprint-status.yaml` divergence reporting, row gating, and epic reconciliation. `story verify --fix` owns exactly one class (a `depends_on` item YAML decoded as a number); every other finding is reported as remaining, never guessed at. `--file` mode additionally gates the story **shape** (`shape.go`, `governance.go`) — the derived section set, File List markers, placeholder residue, compliance-table headers, GCC line form, and the two ADR gates — on exit code 4. Those classes are `--file`-only on purpose: the corpus walk never opens a body, which is what keeps a 465-story sweep at 67 KB. |
| `internal/commitowners/` | The framework's `_apex/commit-owners.csv`, and the two per-dispatch git assertions `ape task` runs from it. A skill absent from the file must leave HEAD, the index and the stash alone, and must not grow HEAD's reflog — HEAD by itself is not the check, since `git add` and `git stash` both leave it where it was, and a commit-then-reset or stash push-then-pop leaves all three where they were. A skill present must commit at least once, every commit matching one of its declared subject regexes. An absent file means "no skill commits"; a malformed one fails preflight, because degrading it to "empty" would invert every committer's assertion. |
| `internal/stamp/` | The monotonic issuer behind the framework's `timestamp` variable: local wall-clock `YYYYMMDDHHMMSS`, `max(now, last_issued)`, floor persisted at `runlog.TimestampStatePath` and seeded from `sprint-status.yaml`'s `updated_at` on a fresh clone. Wired in at `apecmd.resolveMonotonic` (so every project-data command routes through it) and at the run manifest. The guarantee used to be a paragraph carried verbatim in ~53 skills. |
| `internal/migration/`     | The framework's per-version upgrade list (`_apex/migrations/*.md`), run by `ape framework update`. Ordering is **semantic** — semver on `version`, integer on `seq`, honouring `after:` — never the filename's, under which `v0.9.0` sorts after `v0.10.0`. `kind:` is the authority model: `derivable` runs unattended, `judged` is listed with its skill and never executed under any flag. `check:` reports and never gates, and its exit code is read as `0` applied / `1` not applied / `≥2` the check itself failed — because a shell reports a missing binary as 127, and reading that as "not applied" re-applies migrations. Applied ids live in `_apex/framework.yaml` as an ordered list, which is what makes a second run a no-op. |
| `internal/release/`       | Projects the framework's release record for `ape release status`: `sprint-status.yaml`'s `release_slices:`/`active_slice:`, plus each record's **frontmatter only**. A slice is released when its record's `status:` says so and by no other route; an absent folder, an absent record and an *unreadable* one all mean not released. The nine body tables belong to `apex-release-record` — parsing them here would put a second source of truth behind the release's own verdict. |
| `internal/selfpath/`      | Puts the running binary at the front of a child's `PATH` as `ape`, so a migration's `check:` and the ~69 skill files that shell out inside a spawned session get the binary that started them rather than whatever is installed. Not hypothetical: `ape` 0.0.67 spawned a session that reported `ape version` 0.0.56. Used by `internal/migration` and `internal/repl`, and by `ape chat`. |
| `internal/repocache/`     | The framework and governance clones as caches of their newest release (v0.7.0; `docs/explanation/framework-and-governance-clones.md`). Resolves each clone — flag/env/config, else ape's own under `os.UserCacheDir()/ape/<kind>/<host>/<path>`, keyed by URL because skills read the governance clone live — in two halves — `Prepare` (clone, fetch, pick the newest final tag) before the install's checks, `Move` after them, so a refused install moves nothing. Ownership decides: ape's clone is detached and overwritten; a user's clone only moves forward (ff on its branch; ahead or diverged is kept and reported, because the framework's ship repo carries untagged promotion commits) and refuses local changes unless `--force-clone`; a read-only one (the sandbox mounts) is pinned. A leaf package: `apexcfg` resolves the effective `governance_repository_path` through it. |
| `internal/memory/` · `internal/deferred/` | Bounded team-memory reads, and the deferred-work record store.                          |
| `internal/apexdoc/` · `internal/frontmatter/` | Markdown shard/assemble/analyze, and the shared frontmatter parser.             |
| `internal/atomicfile/`    | Temp-write-and-rename, for the writers that rewrite a document a person authored — `os.WriteFile` truncates first, so an interrupted call destroys the original. `internal/deferred` predates it and keeps its own tested copy. |
| `internal/sandbox/`       | `ape sandbox` Kata VM workspaces (PLAN-16): profile, `~/.claude`/git composer, OCI-config + nerdctl command builder, CONNECT egress proxy, workspace registry. Also the PLAN-24 local-polish pieces: node capacity probes, the proxy's node-owned system route, the in-guest heartbeat + sampler, and the idle-stop vocabulary. |
| `images/ape-sandbox/`     | Pointer only (README) — the official **public, framework-free** `ape-sandbox` image (PLAN-16 D6 / PLAN-20) is built from the separate public `exoport/ape-sandbox` repo. The private framework is not baked; `aped` mounts it read-only at runtime. |
| `cmd/aped/` + `internal/aped/` + `internal/apedcmd/` | `aped`, the rootful Kata-QEMU VM-management daemon (PLAN-18 Phase 2): two-process split (root executor + de-privileged NATS front), policy authz, `ape.vmm.<node>.>` contract. |
| `internal/vmmclient/`     | The `ape`-side client of the `ape.vmm` contract (drives `aped` over NATS).                          |
| `internal/vmmstream/`     | Interactive exec/attach stream transport (PLAN-18 D2): per-session NATS subjects, ≤32 KiB frames, channel-tagged credit flow control, server/client session halves. |
| `deploy/`                 | `aped` deploy assets (systemd units, tmpfiles, policy, auditd rules) + `tier2-setup.sh`, the idempotent Tier-2 host-stack provisioner (see `docs/how-to/run-aped.md`). |
| `testdata/`               | Test fixtures consumed by `_test.go` files.                                                         |
| `docs/`                   | User-facing docs (Diátaxis-structured — see `docs/README.md`).                                      |
| `.github/workflows/`      | `ci.yml` (build + test + lint + generated-CLI-reference sync + govulncheck on push to `main` / PR; the Windows job builds everything but runs `make test-portable`) and `release.yml` (goreleaser on final-semver tag `vX.Y.Z` only). |
| `.goreleaser.yaml`        | Release build config.                                                                               |
| `.golangci.yaml`          | Linter config.                                                                                      |
| `.pre-commit-config.yaml` | Pre-commit hooks (golangci-lint via `make lint`, config_secrets). The lint hook is `repo: local` on purpose — a hook resolving the bare `golangci-lint` name lints with whatever is first on $PATH, not the pinned version. |

## Workflow

### Make targets

```bash
make help          # available targets
make build         # → ./ape
make install       # → /usr/local/bin/ape (override INSTALL_DIR=...)
make test          # go test -race ./...
make test-portable # tests that are meaningful off Linux — the Windows CI gate (skips internal/aped, internal/netd)
make test-cover    # with coverage profile
make lint          # golangci-lint (pinned via bingo)
make fmt           # gofumpt (pinned via bingo)
make pre-commit    # run all pre-commit hooks
make snapshot      # goreleaser snapshot (no upload, no sign) — for verifying release builds
make govulncheck   # scan for known vulnerabilities (pinned via bingo)
make xcompile-windows  # cross-compile + cross-vet for Windows; catches portability compile errors
make docs-cli      # regenerate docs/reference/cli.md from the cobra command tree
make docs-cli-check # verify that generated reference is still in sync (also runs in GitHub CI)
make ci-local      # full pre-push gate: test + lint + vuln + docs + prices + xcompile-windows + snapshot
make check-prices  # verify the price table covers the models the local Claude Code emits
make check-output-styles # verify ape's built-in output-style table matches the local Claude Code
                   #   (ape folds a built-in's case before writing `outputStyle`, so a stale
                   #   table fails by halves: lowercase keeps working for the styles ape knows
                   #   and silently stops for a newer one)
make check-claude  # LOCAL ONLY: spawn the installed Claude Code and verify ape's PTY/model contract
                   #   (incl. bg_shell_reap_switch: the background-shell pressure-reap variable
                   #   ape sets on every spawn is still the switch claude reads; and
                   #   model_settings_effort: claude still applies ape's per-model effort
                   #   table, sub-agents included — one Opus+Sonnet turn, a few cents; and
                   #   foreground_agent_sync: a run_in_background:false Agent call still
                   #   returns synchronously under ape's CLAUDE_CODE_FORK_SUBAGENT=0; and
                   #   model_aliases: each family word starts the model ape's alias pins,
                   #   and that model has an exact price)
make check-hooks   # LOCAL ONLY: verify Claude Code still sends the hook fields the completion gates read.
                   #   Seeds its own corpus (one `ape prompt` session in a temp copy of
                   #   testdata/apexproject), so it needs no pre-existing project.
make check-claude-surface # LOCAL ONLY: diff the installed claude's tool list, env vars and model ids against
                   #   the reviewed baseline, and list unread CHANGELOG entries about agents, background
                   #   work, hooks, tools and settings; fails until reviewed
make update-claude-surface # record that review (rewrites internal/claudesurface/testdata/claude-surface.json)
make check-task-subagents # LOCAL ONLY: `ape task` on a stand-in skill that fans out to two foreground
                   #   sub-agents, as the framework's batch skills do — results inline, concurrent,
                   #   APE_SESSION/fork gate/ape pin seen inside, manifest and hooks account for both
make check-agents-md # LOCAL ONLY: the installed claude still loads this repo's AGENTS.md (asked from
                   #   the repo root with every tool off; an empty dir is the negative control)
make check-harness # check-prices + check-output-styles + check-hooks + check-claude + check-claude-surface
                   #   + check-task-subagents + check-agents-md — the whole
                   #   "is the local Claude Code still compatible?" sweep
make check-framework # LOCAL ONLY: does ape still satisfy the APEX framework?
                   #   Set BOTH: APEX_FRAMEWORK_REPO=<checkout> and APEX_PROJECT=<an
                   #   install of that framework version>. Without the second, half the
                   #   gate reports NOT verified — the default is this repo, which is
                   #   not a framework install.
                   #   command surface, live config template — the other dependency axis
make tools         # pre-install all bingo-pinned tools
make tidy          # go mod tidy
make clean         # remove build artifacts
```

`golangci-lint`, `gofumpt`, and `goreleaser` are pinned via [bingo](https://github.com/bwplotka/bingo) — see `.bingo/Variables.mk` and the per-tool `.bingo/<name>.mod` files. Each Make target depends on the version-stamped binary path (e.g., `$(GOLANGCI_LINT)` → `$(GOBIN)/golangci-lint-v2.13.2`); the binary is rebuilt automatically when the corresponding `.mod` changes. That stamping is the whole safety property, so never invoke the unversioned name — `bingo get` also maintains a bare `$(GOBIN)/golangci-lint` symlink, and anything resolving it gets whichever version was installed last rather than the pin. To upgrade a tool: `bingo get <module>@<version>` (or `@latest`), commit the regenerated `.bingo/` files. To bootstrap bingo itself: `go install github.com/bwplotka/bingo@v0.10.0`.

### Commits

- Use [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`).
- **Do not** include Claude attribution or "Generated with Claude Code" in commit messages.
- Pre-commit hooks (`golangci-lint`, `config_secrets`) must pass before commits land. The lint hook shells out to `make lint`, so it, `make lint` and CI all run the one bingo-pinned binary.

### Releases

**Evaluate before releasing.** The framework, ape and the eval share one release workflow (agreed 2026-09-29): nothing that can change framework behaviour or eval results reaches the teams who consume these releases until the eval has measured it. ape's part:

- **Every change has a class**, and a release is gated by the highest class it contains:
  - **B, session-driving**: model aliases, effort, spawn flags, the PTY and hook contract, idle/timeouts, nesting. Needs an rc and an eval gate; a new eval baseline when the model or effort changes.
  - **C, a surface skills or the eval use**: anything in the framework's `_apex/ape-commands.yaml`, `ape framework setup/update` and the migration runner, and the files the eval reads (`manifest.json`, `prompt.yaml`, step ndjson, `hook-events.jsonl`, cost fields). Needs an rc, the eval's conformance check, and the stages that call it.
  - **D, independent**: new verbs no skill calls, TUI, docs, `ape aboard`, `ape sandbox`/`aped`, release tooling, tests. ape's own gates plus the eval's free tier; released directly, any time.
  - Pricing alone changes recorded costs, not behaviour: `ape costs reprice` re-prices an existing corpus, so it needs no new capture.
- **B and C ship through a release candidate.** `/release vX.Y.Z-rc.N` publishes a signed GitHub **prerelease**, which `ape update`, the install docs and `releases/latest/download` all skip. The eval pins it by tag AND commit. A finding is fixed with new commits and a new rc; tags never move.
- **Release = promotion.** `/release vX.Y.Z` tags the last rc's EXACT commit final, with no new commit, after the user confirms the eval passed. The binary is rebuilt (its version and date are stamped at build time), so the release's identity is the COMMIT; the eval then smoke-tests the released binary. The dated `## vX.Y.Z` CHANGELOG section is written before the first rc, so promotion adds nothing.
- **ape tags its final before any framework release that raises the framework's ape floor.**

Two-step verification flow — see `docs/how-to/pre-tag-release.md` for the full guide. The automated walkthrough lives in `.claude/skills/release/SKILL.md` (`/release vX.Y.Z-rc.N`, `/release vX.Y.Z`).

1. **Local gate** — run `make ci-local`. Runs test + lint + vuln + docs-check + generated-CLI-reference sync + price-table coverage + Windows cross-compile + goreleaser snapshot. ~30–60 s. Catches per-platform compile errors, release-config regressions, and a model price table that has gone stale against the locally-installed Claude Code.

   **Then run `make check-harness`** (~3–4 min, developer machine only). `ci-local` proves ape is internally consistent; it cannot prove ape still *works*, because ape's real dependency is the auto-updating `claude` binary on the host. The sweep is seven gates that read what that binary is actually doing: `check-prices` (model ids in transcripts), `check-output-styles` (the built-in style names ape folds declarations onto), `check-hooks` (the hook fields the step-completion gates read, judged against a runlog it seeds itself with one unattended `ape prompt` session — so the verdict never depends on finding a project someone happened to run), `check-claude` (a live PTY session — the ready-signal footer and `❯` glyph, an unknown pre-REPL modal, the spawn flags, `CLAUDE_CODE_EFFORT_LEVEL`, the family-alias model ids still naming real models and the same ones Claude Code's own family words start, a foreground sub-agent staying foreground, and transcript persistence), `check-claude-surface` (the tool list, the binary's `CLAUDE_*` names and model ids, and unread CHANGELOG entries against a reviewed baseline — it fails until someone reads them and runs `make update-claude-surface`), `check-task-subagents` (one `ape task` run of a framework-shaped skill that fans out to two foreground sub-agents), and `check-agents-md` (this repo's own instructions still load, since it has no CLAUDE.md). Deliberately **not** in `ci-local` and never in GitHub CI: they need `claude` + auth + network + local transcripts, so a CI run could only skip them, and a gate that always skips reads as a pass. Read the output — `check-prices` reports "not verified" rather than green when it has no evidence, and `check-hooks` fails a seed that produced no events rather than passing it as "nothing to judge".

   **And `make check-framework APEX_FRAMEWORK_REPO=<checkout> APEX_PROJECT=<install>`** — the other dependency axis. **Both variables**: the first checks ape against the framework checkout, the second against a framework *install*, which is the manifest as a skill meets it at run time. Omit the second and that half reports NOT verified; point it at a project last updated against an OLDER framework and it verifies the older contract while reporting green, because the manifest is versioned. `check-harness` asks whether the local *Claude Code* still honours what ape drives it through; this asks whether ape still satisfies what the local *APEX framework* requires: the command surface its shipped `_apex/ape-commands.yaml` declares, and the config variables its live template defines. Framework v0.11.0 deleted every fallback branch, so a missing command fails a skill mid-stage rather than degrading.
2. **Remote gate** — push commits to `main`:
   ```bash
   git push origin main
   ```
   The CI workflow re-runs the full Linux + Windows matrix against the pushed SHA. Wait for it to finish green before tagging.
3. **Tag** — once main's CI is green: an rc (`vX.Y.Z-rc.N`), a promotion (`vX.Y.Z` on the last rc's commit), or a direct class-D release (`vX.Y.Z`):
   ```bash
   git tag -a v0.4.0-rc.1 -m "v0.4.0-rc.1 — release candidate: what changed"
   git push origin v0.4.0-rc.1
   ```
   `release.yml` builds linux/darwin/windows × amd64/arm64, signs the checksums file via keyless cosign (Sigstore Fulcio), and uploads everything to GitHub Releases, as a prerelease for an rc. No manual release steps.

> **Two tags on one commit is the v0.0.21 incident**, and promotion does it on purpose. goreleaser resolves its tag with `git describe`, so with an rc and a final on the same commit it picked the rc and built the final as the rc. That is why the rc cycle was once dropped. release.yml now sets `GORELEASER_CURRENT_TAG` to the pushed tag, and for a final sets `GORELEASER_PREVIOUS_TAG` to the previous FINAL, so its changelog covers every rc and not only the last one. Both were reproduced and fixed with goreleaser v2.18.0 on a dual-tagged scratch commit. The release skill still checks the published tag, the prerelease flag, `releases/latest` and the binary's own version, because the real run is the only proof.

Tag filter shape:

| Workflow      | Triggered by                                                          |
| ------------- | --------------------------------------------------------------------- |
| `ci.yml`      | push to `main`, pull request                                          |
| `release.yml` | push of `vX.Y.Z` (final) or `vX.Y.Z-rc.N` (prerelease); any other shape fails the "Validate tag" step before a build |

Verifying a release locally (releases ship `ape_checksums.txt.bundle`, a
Sigstore bundle — cert + signature + SCT + Rekor proof — verifiable offline;
`ape update` does this same check automatically via embedded `sigstore-go`, no
`cosign` binary required):

```bash
cosign verify-blob \
  --bundle ape_checksums.txt.bundle \
  --new-bundle-format \
  --certificate-identity "https://github.com/exoport/apex_process_ape/.github/workflows/release.yml@refs/tags/vX.Y.Z" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  ape_checksums.txt
```

> Releases ≤ `v0.0.43` shipped detached `ape_checksums.txt.sig` +
> `ape_checksums.txt.pem` instead; verify those with `--certificate … --signature …`
> (no `--bundle`/`--new-bundle-format`).

## Conventions

- **Cobra command files** live under `internal/apecmd/<command>.go`. One command per file. Each file exports a `new<Command>Cmd()` constructor returning `*cobra.Command`.
- **Everything ape writes into a project goes under `{output_folder}/ape/`, and the paths live in `internal/runlog/layout.go` — nowhere else.** The output folder belongs to the framework: its path comes from the `output_folder` config variable (default `_output`), and the skills write handoffs, briefs and verify reports there. `runlog` resolves it via `apexcfg`, falling back to `_output` when the config is absent or unparseable — run paths must resolve even then. Never build a run path with `filepath.Join(projectRoot, "_output", …)`: call `runlog.PipelineRunDir`, `runlog.TasksRoot`, `runlog.RunRoots`, etc. Consumers that sweep every run **must** iterate `runlog.RunRoots` rather than listing roots themselves — a hand-written list is what made `hookdrift` miss `ape pipeline` runs entirely, and made two other consumers' coverage accidental. A new run kind is one line in `layout.go`.
- **Pipeline YAML specs are project-local**, not embedded: they load from `<projectRoot>/_apex/pipelines/<name>.yaml`, installed there by `ape framework setup|update`. They are *not* compiled into the binary — the embedded-spec design was dropped in v0.0.6 so a project can pin its own pipelines without an ape release ([rationale](docs/explanation/why-project-local-pipelines.md)).
- **Output formatting**: commands that emit structured data accept `--output-format human|json|yaml` and route through `internal/output`. Match the existing pattern in `update.go` rather than rolling your own.
- **Version pinning** for invoked tooling (linters, formatters, release machinery) lives in the top-level `Makefile`. Change there, not scattered across CI configs.
- **No vendor directory.** Modules are fetched on demand. `go.mod` and `go.sum` are the source of truth.

## Documentation

User-facing docs live in `docs/` and follow the [Diátaxis](https://diataxis.fr/) structure: `tutorials/`, `how-to/`, `reference/`, `explanation/`. When adding a doc, place it in the quadrant that matches its primary user need — see [docs/README.md](docs/README.md) for the rubric.

The repo-level `README.md` is the entry point for first-time visitors: short intro, fast install, link into `docs/` for depth.
