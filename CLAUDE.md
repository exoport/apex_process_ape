# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Project overview

`ape` is a single-binary Go CLI that runs APEX framework pipelines on projects. It is a CLI only — no infrastructure services, no docker-compose stacks, no private module dependencies. The repo is intentionally focused.

### Directory map

| Path                      | Purpose                                                                                             |
| ------------------------- | --------------------------------------------------------------------------------------------------- |
| `cmd/ape/`                | Binary entry point (`main.go`).                                                                     |
| `internal/apecmd/`        | Cobra command definitions: pipeline, adr, pattern, trait, update, etc.                              |
| `internal/pipeline/`      | Pipeline runner, embedded YAML specs, pre-flight checks.                                            |
| `internal/tui/`           | Bubble Tea two-panel TUI.                                                                           |
| `internal/output/`        | Output-format helpers (human / json / yaml).                                                        |
| `internal/updatecache/`   | Cache layer for the background update-check.                                                        |
| `internal/trait/`         | Trait inspection helpers.                                                                           |
| `testdata/`               | Test fixtures consumed by `_test.go` files.                                                         |
| `docs/`                   | User-facing docs (Diátaxis-structured — see `docs/README.md`).                                      |
| `.github/workflows/`      | `ci.yml` (build + test + lint + govulncheck on push/PR) and `release.yml` (goreleaser on `v*` tag). |
| `.goreleaser.yaml`        | Release build config.                                                                               |
| `.golangci.yaml`          | Linter config.                                                                                      |
| `.pre-commit-config.yaml` | Pre-commit hooks (golangci-lint-mod, config_secrets).                                               |

## Workflow

### Make targets

```bash
make help          # available targets
make build         # → ./ape
make install       # → /usr/local/bin/ape (override INSTALL_DIR=...)
make test          # go test -race ./...
make test-cover    # with coverage profile
make lint          # golangci-lint (pinned via bingo)
make fmt           # gofumpt (pinned via bingo)
make pre-commit    # run all pre-commit hooks
make snapshot      # goreleaser snapshot (no upload) — for verifying release builds
make govulncheck   # scan for known vulnerabilities (pinned via bingo)
make tools         # pre-install all bingo-pinned tools
make tidy          # go mod tidy
make clean         # remove build artifacts
```

`golangci-lint`, `gofumpt`, and `goreleaser` are pinned via [bingo](https://github.com/bwplotka/bingo) — see `.bingo/Variables.mk` and the per-tool `.bingo/<name>.mod` files. Each Make target depends on the version-stamped binary path (e.g., `$(GOLANGCI_LINT)` → `$(GOBIN)/golangci-lint-v2.6.0`); the binary is rebuilt automatically when the corresponding `.mod` changes. To upgrade a tool: `bingo get <module>@<version>` (or `@latest`), commit the regenerated `.bingo/` files. To bootstrap bingo itself: `go install github.com/bwplotka/bingo@v0.10.0`.

### Commits

- Use [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`).
- **Do not** include Claude attribution or "Generated with Claude Code" in commit messages.
- Pre-commit hooks (`golangci-lint-mod`, `config_secrets`) must pass before commits land.

### Releases

Push a `v*` tag (e.g., `git tag v0.1.0 && git push origin v0.1.0`). The release workflow runs goreleaser, builds linux/darwin/windows × amd64/arm64, and uploads tarballs + checksums to GitHub Releases. No manual release steps.

## Conventions

- **Cobra command files** live under `internal/apecmd/<command>.go`. One command per file. Each file exports a `new<Command>Cmd()` constructor returning `*cobra.Command`.
- **Pipeline YAML specs** live under `internal/pipeline/spec/<name>.yaml` and are embedded via `go:embed` — they ship inside the binary, not loaded from the user's filesystem.
- **Output formatting**: commands that emit structured data accept `--output-format human|json|yaml` and route through `internal/output`. Match the existing pattern in `update.go` rather than rolling your own.
- **Version pinning** for invoked tooling (linters, formatters, release machinery) lives in the top-level `Makefile`. Change there, not scattered across CI configs.
- **No vendor directory.** Modules are fetched on demand. `go.mod` and `go.sum` are the source of truth.

## Documentation

User-facing docs live in `docs/` and follow the [Diátaxis](https://diataxis.fr/) structure: `tutorials/`, `how-to/`, `reference/`, `explanation/`. When adding a doc, place it in the quadrant that matches its primary user need — see [docs/README.md](docs/README.md) for the rubric.

The repo-level `README.md` is the entry point for first-time visitors: short intro, fast install, link into `docs/` for depth.
