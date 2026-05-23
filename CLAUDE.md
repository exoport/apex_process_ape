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
| `.github/workflows/`      | `ci.yml` (build + test + lint + govulncheck on push/PR + rc-tag) and `release.yml` (goreleaser on final-semver tag `vX.Y.Z` only). |
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
make snapshot      # goreleaser snapshot (no upload, no sign) — for verifying release builds
make govulncheck   # scan for known vulnerabilities (pinned via bingo)
make xcompile-windows  # cross-compile + cross-vet for Windows; catches portability compile errors
make ci-local      # full pre-push gate: test + lint + vuln + xcompile-windows + snapshot
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

Two-step verification flow — see `docs/how-to/pre-tag-release.md` for the full guide.

1. **Local gate** — run `make ci-local`. Runs test + lint + vuln + Windows cross-compile + goreleaser snapshot. ~30–60 s. Catches per-platform compile errors and release-config regressions before push.
2. **Remote gate** — push commits to `main`, then a pre-release tag:
   ```bash
   git push origin main
   git tag -a v0.0.X-rc1 -m "v0.0.X release candidate 1"
   git push origin v0.0.X-rc1
   ```
   CI re-runs the full Linux + Windows matrix against the exact tagged SHA. **Release.yml is deliberately not triggered** by rc tags (its filter is final-semver only). If the rc CI fails, push a fix and increment the rc number; there's no limit.
3. **Final tag** — once the rc CI is green:
   ```bash
   git tag -a v0.0.X -m "v0.0.X — what changed"
   git push origin v0.0.X
   ```
   `release.yml` builds linux/darwin/windows × amd64/arm64, signs the checksums file via keyless cosign (Sigstore Fulcio), and uploads everything to GitHub Releases. No manual release steps.

Tag filter shape:

| Workflow      | Triggered by                                                          |
| ------------- | --------------------------------------------------------------------- |
| `ci.yml`      | push to `main`, pull request, or push of any `vX.Y.Z-*` rc tag        |
| `release.yml` | push of a final-semver tag `vX.Y.Z` only (no suffix)                  |

Verifying a release locally:

```bash
cosign verify-blob \
  --certificate ape_checksums.txt.pem \
  --signature ape_checksums.txt.sig \
  --certificate-identity "https://github.com/diegosz/apex_process_ape/.github/workflows/release.yml@refs/tags/vX.Y.Z" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  ape_checksums.txt
```

## Conventions

- **Cobra command files** live under `internal/apecmd/<command>.go`. One command per file. Each file exports a `new<Command>Cmd()` constructor returning `*cobra.Command`.
- **Pipeline YAML specs** live under `internal/pipeline/spec/<name>.yaml` and are embedded via `go:embed` — they ship inside the binary, not loaded from the user's filesystem.
- **Output formatting**: commands that emit structured data accept `--output-format human|json|yaml` and route through `internal/output`. Match the existing pattern in `update.go` rather than rolling your own.
- **Version pinning** for invoked tooling (linters, formatters, release machinery) lives in the top-level `Makefile`. Change there, not scattered across CI configs.
- **No vendor directory.** Modules are fetched on demand. `go.mod` and `go.sum` are the source of truth.

## Documentation

User-facing docs live in `docs/` and follow the [Diátaxis](https://diataxis.fr/) structure: `tutorials/`, `how-to/`, `reference/`, `explanation/`. When adding a doc, place it in the quadrant that matches its primary user need — see [docs/README.md](docs/README.md) for the rubric.

The repo-level `README.md` is the entry point for first-time visitors: short intro, fast install, link into `docs/` for depth.
