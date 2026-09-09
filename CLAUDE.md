# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Project overview

`ape` is a single-binary Go CLI that runs APEX framework pipelines on projects. It is a CLI only — no infrastructure services, no docker-compose stacks, no private module dependencies. The repo is intentionally focused.

### Directory map

| Path                      | Purpose                                                                                             |
| ------------------------- | --------------------------------------------------------------------------------------------------- |
| `cmd/ape/`                | Binary entry point (`main.go`).                                                                     |
| `internal/apecmd/`        | Cobra command definitions: pipeline, adr, pattern, trait, update, etc.                              |
| `internal/apecmd/aboard.go` | The `ape aboard` **mount** — a whole command tree from the separate public `github.com/exoport/aboard` module, added rather than ported. Owns the three things hosting requires: the `Host`/`Argv0` identity, aboard's exit table surviving ape's `ExitCode`, and the `PersistentPreRun` shadow that keeps ape's update notice out of board output. Both hosts drive the same `.aboard/` and must produce an identical `capsHash`. |
| `internal/pipeline/`      | Pipeline runner and pre-flight checks. Specs are **not** embedded — they load from `<projectRoot>/_apex/pipelines/*.yaml` (v0.0.6; see `docs/explanation/why-project-local-pipelines.md`). |
| `internal/repl/`          | The PTY that drives `claude`: spawn + keystrokes + a vt10x-rendered pane. Owns the ready-signal vocabulary (`bypass permissions on`, `❯`), the pre-REPL modal table, `ScrubClaudeCodeEnv`, and `CLAUDE_CODE_EFFORT_LEVEL`. `TestLive_ClaudeCodeContract` (opt-in, `make check-claude`) checks all of it against the installed Claude Code. |
| `internal/hookdrift/`     | Detects Claude Code dropping the hook-payload fields the step-completion gates read. Sweeps every runlog under a project's `_output/`, and scopes the verdict to the harness version that wrote the newest run so an upgrade cannot mask fresh drift. Two ways in: `ape doctor --only hooks.contract_drift --cwd <project>` reads a real project's runlogs (observational, and skips when there are none), while `make check-hooks` seeds its own corpus with one `ape prompt` session so it always returns a verdict. |
| `internal/outputstyles/`  | The framework-owned per-skill output-style table (`_apex/output-styles.csv`), read by `ape task`. Same shape and tolerance as `internal/contract`. Deliberately **not** consulted for pipeline steps: it is keyed by skill, a stage is a chain of skills sharing one spawned process, and `--settings` is fixed at launch — so a stage with divergent per-skill styles could only honour one, silently. Pipelines declare `output-style:` at pipeline or stage level instead, which is the granularity a spawn can deliver. |
| `internal/contract/`      | The framework-owned *terminal* contract (`_apex/terminal-contracts.csv`) — did a run reach its summary step. Unrelated to the PTY contract above, despite the name. |
| `internal/tui/`           | Bubble Tea two-panel TUI.                                                                           |
| `internal/output/`        | Output-format helpers (human / json / yaml).                                                        |
| `internal/cost/`          | Transcript → USD: the embedded price table (`prices.yaml`), model-id normalization + family aliases, per-run rollups, price-table coverage, and `reprice`. Prices are hand-curated — there is no price API — so `prices.yaml` is data, not Go literals, and `make check-prices` gates it against the local Claude Code. The same rows carry each model's **context window**, curated the same way and from a documented source (`GET /v1/models` → `max_input_tokens`); `TestContextWindow_GenerationBoundary` locks the values so a change has to be deliberate. |
| `internal/updatecache/`   | Cache layer for the background update-check.                                                        |
| `internal/trait/`         | Trait inspection helpers.                                                                           |
| `internal/apexcfg/`       | Resolves `_apex/config.yaml` + the `config.local.yaml` overlay into the framework's folder variables. Every project-data command routes through it. |
| `internal/registry/`      | The record registries (ADRs, patterns, features, capabilities): verify, sync, update. `sync` **withholds** a removal it cannot prove is a phantom — a headerless record claims no id, so its index entry is the last copy of its metadata — and `restore-headers` is that repair, writing the entry back as frontmatter. |
| `internal/story/` · `internal/sprint/` | Story frontmatter projection/verification, and `sprint-status.yaml` divergence reporting, row gating, and epic reconciliation. `story verify --fix` owns exactly one class (a `depends_on` item YAML decoded as a number); every other finding is reported as remaining, never guessed at. `--file` mode additionally gates the story **shape** (`shape.go`, `governance.go`) — the derived section set, File List markers, placeholder residue, compliance-table headers, GCC line form, and the two ADR gates — on exit code 4. Those classes are `--file`-only on purpose: the corpus walk never opens a body, which is what keeps a 465-story sweep at 67 KB. |
| `internal/commitowners/` | The framework's `_apex/commit-owners.csv`, and the two per-dispatch git assertions `ape task` runs from it. A skill absent from the file must leave HEAD, the index and the stash alone — HEAD by itself is not the check, since `git add` and `git stash` both leave it where it was. A skill present must commit at least once, every commit matching one of its declared subject regexes. An absent file means "no skill commits"; a malformed one fails preflight, because degrading it to "empty" would invert every committer's assertion. |
| `internal/stamp/` | The monotonic issuer behind the framework's `timestamp` variable: local wall-clock `YYYYMMDDHHMMSS`, `max(now, last_issued)`, floor persisted at `runlog.TimestampStatePath` and seeded from `sprint-status.yaml`'s `updated_at` on a fresh clone. Wired in at `apecmd.resolveMonotonic` (so every project-data command routes through it) and at the run manifest. The guarantee used to be a paragraph carried verbatim in ~53 skills. |
| `internal/migration/`     | The framework's per-version upgrade list (`_apex/migrations/*.md`), run by `ape framework update`. Ordering is **semantic** — semver on `version`, integer on `seq`, honouring `after:` — never the filename's, under which `v0.9.0` sorts after `v0.10.0`. `kind:` is the authority model: `derivable` runs unattended, `judged` is listed with its skill and never executed under any flag. `check:` reports and never gates, and its exit code is read as `0` applied / `1` not applied / `≥2` the check itself failed — because a shell reports a missing binary as 127, and reading that as "not applied" re-applies migrations. Applied ids live in `_apex/framework.yaml` as an ordered list, which is what makes a second run a no-op. |
| `internal/release/`       | Projects the framework's release record for `ape release status`: `sprint-status.yaml`'s `release_slices:`/`active_slice:`, plus each record's **frontmatter only**. A slice is released when its record's `status:` says so and by no other route; an absent folder, an absent record and an *unreadable* one all mean not released. The nine body tables belong to `apex-release-record` — parsing them here would put a second source of truth behind the release's own verdict. |
| `internal/selfpath/`      | Puts the running binary at the front of a child's `PATH` as `ape`, so a migration's `check:` and the ~69 skill files that shell out inside a spawned session get the binary that started them rather than whatever is installed. Not hypothetical: `ape` 0.0.67 spawned a session that reported `ape version` 0.0.56. Used by `internal/migration` and `internal/repl`, and by `ape chat`. |
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
make check-claude  # LOCAL ONLY: spawn the installed Claude Code and verify ape's PTY/model contract
make check-hooks   # LOCAL ONLY: verify Claude Code still sends the hook fields the completion gates read.
                   #   Seeds its own corpus (one `ape prompt` session in a temp copy of
                   #   testdata/apexproject), so it needs no pre-existing project.
make check-harness # check-prices + check-hooks + check-claude — the whole "is the local Claude Code still compatible?" sweep
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

Two-step verification flow — see `docs/how-to/pre-tag-release.md` for the full guide. The automated walkthrough lives in `.claude/skills/release/SKILL.md` (`/release vX.Y.Z`).

1. **Local gate** — run `make ci-local`. Runs test + lint + vuln + docs-check + generated-CLI-reference sync + price-table coverage + Windows cross-compile + goreleaser snapshot. ~30–60 s. Catches per-platform compile errors, release-config regressions, and a model price table that has gone stale against the locally-installed Claude Code.

   **Then run `make check-harness`** (~60 s, developer machine only). `ci-local` proves ape is internally consistent; it cannot prove ape still *works*, because ape's real dependency is the auto-updating `claude` binary on the host. The sweep is three gates that read what that binary is actually doing: `check-prices` (model ids in transcripts), `check-hooks` (the hook fields the step-completion gates read, judged against a runlog it seeds itself with one unattended `ape prompt` session — so the verdict never depends on finding a project someone happened to run), and `check-claude` (a live PTY session — the ready-signal footer and `❯` glyph, an unknown pre-REPL modal, the spawn flags, `CLAUDE_CODE_EFFORT_LEVEL`, the family-alias model ids still naming real models, and transcript persistence). Deliberately **not** in `ci-local` and never in GitHub CI: they need `claude` + auth + network + local transcripts, so a CI run could only skip them, and a gate that always skips reads as a pass. Read the output — `check-prices` reports "not verified" rather than green when it has no evidence, and `check-hooks` fails a seed that produced no events rather than passing it as "nothing to judge".

   **And `make check-framework APEX_FRAMEWORK_REPO=<checkout> APEX_PROJECT=<install>`** — the other dependency axis. **Both variables**: the first checks ape against the framework checkout, the second against a framework *install*, which is the manifest as a skill meets it at run time. Omit the second and that half reports NOT verified; point it at a project last updated against an OLDER framework and it verifies the older contract while reporting green, because the manifest is versioned. `check-harness` asks whether the local *Claude Code* still honours what ape drives it through; this asks whether ape still satisfies what the local *APEX framework* requires: the command surface its shipped `_apex/ape-commands.yaml` declares, and the config variables its live template defines. Framework v0.11.0 deleted every fallback branch, so a missing command fails a skill mid-stage rather than degrading.
2. **Remote gate** — push commits to `main`:
   ```bash
   git push origin main
   ```
   The CI workflow re-runs the full Linux + Windows matrix against the pushed SHA. Wait for it to finish green before tagging.
3. **Final tag** — once main's CI is green:
   ```bash
   git tag -a v0.0.X -m "v0.0.X — what changed"
   git push origin v0.0.X
   ```
   `release.yml` builds linux/darwin/windows × amd64/arm64, signs the checksums file via keyless cosign (Sigstore Fulcio), and uploads everything to GitHub Releases. No manual release steps.

> The earlier flow used a `vX.Y.Z-rcN` pre-release tag as the remote gate. It was dropped after the v0.0.21 incident — rc and final annotated tags landing on the same commit confused goreleaser's `git describe`-based tag resolution and misrouted the build to the rc prerelease. The push-to-`main` CI run replaces that gate; the final tag is the only annotated tag on the commit, so goreleaser cannot pick the wrong one.

Tag filter shape:

| Workflow      | Triggered by                                                          |
| ------------- | --------------------------------------------------------------------- |
| `ci.yml`      | push to `main`, pull request                                          |
| `release.yml` | push of a final-semver tag `vX.Y.Z` only (no suffix)                  |

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
