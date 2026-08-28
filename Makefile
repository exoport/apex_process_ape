.DEFAULT_GOAL := help

BIN          := ape
INSTALL_DIR  ?= /usr/local/bin
COVER_FILE   := coverage.out

# Tooling pinned via bingo. See .bingo/Variables.mk for $(GOLANGCI_LINT),
# $(GOFUMPT), $(GORELEASER), $(BINGO) — each variable expands to a
# version-stamped binary path under $(GOBIN), and the included rules
# (re)build the tool when its .mod file changes. Update versions with
# `bingo get <module>@<version>`.
include .bingo/Variables.mk

.PHONY: help
help:        ## Show this help.
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort \
	  | awk 'BEGIN {FS = ":[^#]*## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build:       ## Build the ape + aped binaries into ./ape and ./aped.
	go build -o $(BIN) ./cmd/ape
	go build -o aped ./cmd/aped

.PHONY: install
install:     ## Build and install ape + aped to INSTALL_DIR (default: /usr/local/bin).
	@go build -o $(BIN) ./cmd/ape
	@go build -o aped ./cmd/aped
	@install -m 755 $(BIN) $(INSTALL_DIR)/$(BIN)
	@install -m 755 aped $(INSTALL_DIR)/aped
	@rm -f $(BIN) aped
	@echo "installed $(BIN) + aped to $(INSTALL_DIR)/"

.PHONY: test
test:        ## Run all tests with the race detector.
	go test -race ./...

# Packages whose SUBJECT is the Linux host stack: the rootful Kata VM daemon and its
# privileged network helper. Both still get COMPILED everywhere — the Windows CI job
# builds them and xcompile-windows links their test binaries, which is what catches
# portability breaks — but RUNNING their tests off Linux only asserts things the platform
# has no equivalent of (POSIX mode bits, /run/netns paths, systemd units), so a failure
# there describes the runner rather than ape.
NON_PORTABLE_PKG_RE := /internal/(aped|netd)$$

.PHONY: test-portable
test-portable: ## Run the tests that are meaningful off Linux (the Windows CI gate).
	go test -race $$(go list ./... | grep -vE '$(NON_PORTABLE_PKG_RE)')

.PHONY: test-cover
test-cover:  ## Run tests and produce a coverage profile.
	go test -race -coverprofile=$(COVER_FILE) ./...
	@echo "view coverage: go tool cover -html=$(COVER_FILE)"

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint (pinned via bingo).
	$(GOLANGCI_LINT) run ./...

.PHONY: fmt
fmt: $(GOFUMPT) ## Format Go source with gofumpt (pinned via bingo).
	$(GOFUMPT) -l -w .

.PHONY: pre-commit
pre-commit:  ## Run pre-commit hooks across all files.
	pre-commit run --all-files

.PHONY: snapshot
snapshot: $(GORELEASER) ## Build release snapshot artifacts via goreleaser (no upload, no sign).
	# --skip=sign avoids the cosign OIDC device flow in local runs.
	# Real releases sign via release.yml, which runs on a GitHub Actions
	# runner whose ambient OIDC token is automatically exchanged with
	# Fulcio. Locally we just want to verify the archive layout.
	$(GORELEASER) release --snapshot --clean --skip=publish --skip=sign

.PHONY: govulncheck
govulncheck: $(GOVULNCHECK) ## Scan for known vulnerabilities (pinned via bingo); allow-lists documented unfixable advisories.
	python3 scripts/govulncheck-gate.py $(GOVULNCHECK) ./...

.PHONY: tools
tools: $(GOLANGCI_LINT) $(GOFUMPT) $(GORELEASER) $(GOVULNCHECK) ## Pre-install all bingo-pinned tools.
	@echo "tools installed under $(GOBIN)"

.PHONY: tidy
tidy:        ## Update go.mod and go.sum.
	go mod tidy

.PHONY: clean
clean:       ## Remove build artifacts.
	rm -f $(BIN) $(COVER_FILE)
	rm -rf dist/

.PHONY: xcompile-windows
xcompile-windows: ## Cross-compile + cross-vet for Windows; catches portability compile errors.
	@echo "==> GOOS=windows go vet ./..."
	@GOOS=windows GOARCH=amd64 go vet ./...
	@echo "==> GOOS=windows go build ./..."
	@GOOS=windows GOARCH=amd64 go build ./...
	@echo "==> GOOS=windows go test -c (per package, output discarded)"
	@for pkg in $$(go list ./...); do \
		GOOS=windows GOARCH=amd64 go test -c -o /dev/null $$pkg \
		  || { echo "FAIL: $$pkg"; exit 1; }; \
	done

.PHONY: docs-cli
docs-cli:    ## Regenerate docs/reference/cli.md from the cobra command tree.
	go run ./cmd/ape gen-docs --out docs/reference/cli.md

.PHONY: apescript-symbols
apescript-symbols:  ## Regenerate the yaegi symbol table for the public apescript package (PLAN-15).
	go generate ./internal/apescriptsym/

.PHONY: docs-check
docs-check:  ## Verify docs/ links resolve and every doc is reachable from docs/README.md.
	python3 scripts/check-docs-links.py docs

.PHONY: check-prices
check-prices:  ## Verify the built-in price table covers the models the locally-installed Claude Code emits.
	@# ape's price table is hand-curated (no price API exists) and Claude Code
	@# ships independently, so a model id can change under a released ape at
	@# any time — tokens keep counting, cost silently goes to zero. This gate
	@# reads the transcripts the LOCAL Claude Code is writing, so it is only
	@# meaningful on a developer machine. With no transcripts (CI) it prints a
	@# skip and exits 0: absence of evidence is not coverage.
	go run ./cmd/ape costs coverage --strict

.PHONY: check-claude
check-claude:  ## Spawn the LOCAL Claude Code and verify it still honours the PTY/model contract ape drives it through.
	@# ape does not call a Claude Code API — it types into a TUI over a PTY
	@# and reads the rendered grid back. Every one of those couplings (the
	@# `bypass permissions on` footer, the ❯ glyph, --dangerously-skip-permissions,
	@# --model, CLAUDE_CODE_EFFORT_LEVEL, transcript persistence after the env
	@# scrub) is an undocumented detail of a binary that auto-updates on a
	@# schedule ape does not control. When one moves, nothing errors: ape keeps
	@# running and silently stops doing the thing the coupling bought.
	@#
	@# Not hermetic — needs claude on PATH, auth, and network — so it is opt-in
	@# and NEVER part of `make test` or GitHub CI. Run it before a release, and
	@# after any Claude Code upgrade.
	@#
	@# Costs one short Haiku turn; APE_CLAUDE_LIVE_TOKENS=0 skips that subtest.
	APE_CLAUDE_LIVE=1 go test ./internal/repl/ \
	  -run TestLive_ClaudeCodeContract -v -count=1 -timeout 20m

# The project whose runlogs `check-hooks` reads. Hook drift is observed from
# the hook-events.jsonl files ape itself wrote under <project>/_output/tasks,
# so it can only be judged against a project ape has actually run interactive
# pipelines in — NOT against this repo, which has no runlogs and will always
# report a skip. Point it at a real one:
#   make check-hooks HOOK_PROJECT=~/work/some-apex-project
HOOK_PROJECT ?= .

.PHONY: check-hooks
check-hooks:  ## Verify Claude Code still sends the hook fields ape's completion gates read (set HOOK_PROJECT).
	@# ape's step-completion gates read fields off Claude Code's hook payloads:
	@# `background_tasks` on Stop decides whether a turn boundary really means
	@# the step is done, and an Agent-tool `tool_response` catches a spawn that
	@# detached. Claude Code makes no compatibility promise about payload shape.
	@#
	@# If one is renamed or dropped, nothing errors — the gate just stops firing,
	@# and ape silently returns to reporting success on runs that did nothing.
	@# That is worse than having no gate, because it turns an absent protection
	@# into a believed-present one.
	@#
	@# Exits 0 with "hook contract not verified" when the project has no recent
	@# runlogs. That is a SKIP, not a pass: absence of evidence is not coverage.
	go run ./cmd/ape doctor --only hooks.contract_drift --strict --cwd $(HOOK_PROJECT)

# A checkout of apex_process_framework, for the contract gates below. Unset
# means those gates cannot run — they report that rather than passing.
APEX_FRAMEWORK_REPO ?=

.PHONY: check-framework
check-framework:  ## LOCAL ONLY: verify ape still satisfies the APEX framework's contract (set APEX_FRAMEWORK_REPO).
	@# The OTHER axis from check-harness. That one asks "is the local Claude
	@# Code still compatible?"; this asks "is the local FRAMEWORK still
	@# compatible?" — a different dependency that also moves on its own
	@# schedule, and the one an eval capture nearly measured eight hours of
	@# broken runs against.
	@#
	@# Two gates, both needing a sibling framework checkout:
	@#   TestCommandSurface_AgainstRealManifest  every command the framework's
	@#     shipped _apex/ape-commands.yaml requires resolves against this binary
	@#   TestContract_LiveConfigTemplate         the config variables ape resolves
	@#     match the framework's live template, not a copied fixture
	@#
	@# The eight TestParity_* gates were removed in v0.0.55. They ran the ten
	@# retired Python scripts side by side with the commands replacing them, so
	@# they could only fire against a pre-v0.11.0 framework — against any
	@# supported one they skipped, adding eight SKIP lines to the output of a
	@# gate whose whole discipline is "a skip is not a pass". Their value was
	@# the differential itself and it was spent at migration; the behaviours
	@# are covered natively, and the one asymmetry they alone asserted (an
	@# unquoted 14-digit updated_at, which the Python's str guard dropped) is
	@# now internal/sprint's TestVerifyRow_BackwardsWriteAgainstUnquotedCommittedValue.
	@#
	@# Both framework layouts resolve: released (_apex/ at the repo root) and
	@# build (nested under framework/). The whole guard is ONE shell block
	@# because a bare `exit 0` in a make recipe ends only that line's shell —
	@# the first draft printed "NOT verified" and then ran the tests anyway.
	@if [ -z "$(APEX_FRAMEWORK_REPO)" ]; then \
		echo "framework contract NOT verified — this is a skip, not a pass."; \
		echo "  set APEX_FRAMEWORK_REPO=/path/to/apex_process_framework to run it."; \
	elif [ ! -f "$(APEX_FRAMEWORK_REPO)/_apex/config.yaml" ] \
	  && [ ! -f "$(APEX_FRAMEWORK_REPO)/framework/_apex/config.yaml" ]; then \
		echo "APEX_FRAMEWORK_REPO=$(APEX_FRAMEWORK_REPO) is not a framework checkout:"; \
		echo "  no _apex/config.yaml there (released layout) or under framework/ (build layout)."; \
		echo "  Setting the variable says you want this gate to RUN, so a path that resolves to"; \
		echo "  nothing is a typo rather than a skip — every gate would have passed green."; \
		exit 1; \
	else \
		echo "==> framework contract against $(APEX_FRAMEWORK_REPO)"; \
		APEX_FRAMEWORK_REPO="$(APEX_FRAMEWORK_REPO)" go test ./internal/apecmd/ -count=1 -v \
		  -run 'TestCommandSurface_AgainstRealManifest|TestContract_Live'; \
	fi
	@# The gates above compare ape to a framework CHECKOUT. This compares it to
	@# a framework INSTALL — the manifest as a project actually received it,
	@# which is what a skill meets at run time.
	@if [ -d "$(HOOK_PROJECT)/_apex" ]; then \
		echo "==> installed command surface in $(HOOK_PROJECT)"; \
		go run ./cmd/ape doctor --strict --cwd "$(HOOK_PROJECT)" \
		  --only framework.command_surface,framework.terminal_contracts; \
	else \
		echo "installed command surface NOT verified — HOOK_PROJECT=$(HOOK_PROJECT) has no _apex/."; \
	fi

.PHONY: check-harness
check-harness: check-prices check-hooks check-claude ## All local-only gates against the installed Claude Code (prices + hooks + PTY/model).
	@echo
	@echo "Harness sweep complete against Claude Code $$(claude --version 2>/dev/null || echo 'unknown')."
	@echo "Read the output above: any gate that reported a SKIP was NOT verified — it found no evidence to judge."

.PHONY: ci-local
ci-local: test lint govulncheck docs-check check-prices xcompile-windows snapshot ## Run every gate CI + release would run (Linux + Windows cross-compile + snapshot).
	@echo
	@echo "Local CI gates green. Safe to push + tag."
	@echo "Catches: Linux test failures, lint, vuln, Windows compile-time portability bugs, release-config regressions."
	@echo "Does NOT catch: Windows runtime behaviour (use a push-to-branch + GitHub Actions Windows runner for that)."
	@echo "Does NOT catch: the installed Claude Code breaking a contract ape drives it through (PTY, models,"
	@echo "                hook payloads) — run 'make check-harness HOOK_PROJECT=<a project ape has run>'."
	@echo "Does NOT catch: ape no longer satisfying the APEX framework (command surface, config"
	@echo "                template) — run 'make check-framework APEX_FRAMEWORK_REPO=<checkout>'."
