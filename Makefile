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

.PHONY: docs-cli-check
docs-cli-check:  ## Verify docs/reference/cli.md still matches the command tree.
	@# cli.md is GENERATED from the cobra tree, and every command's Long text
	@# lives in internal/apecmd. Editing help text therefore silently desyncs
	@# the committed reference, and nothing noticed: docs-check is a link
	@# checker, and neither it nor any CI job regenerates this file. The docs
	@# said "do not edit by hand" while having no way to tell that you had.
	@#
	@# Hermetic — no claude, no auth, no network, just the command tree — so
	@# unlike the check-* harness gates this one DOES belong in GitHub CI, and
	@# runs there as well as in ci-local.
	@# gen-docs announces "wrote <path>" on stderr, which here is a temp file
	@# nobody wants named in the log. Held rather than discarded, and replayed
	@# if the generator actually fails, so a real error is never swallowed.
	@tmp=$$(mktemp); log=$$(mktemp); \
	if ! go run ./cmd/ape gen-docs --out "$$tmp" >/dev/null 2>"$$log"; then \
		cat "$$log" >&2; rm -f "$$tmp" "$$log"; exit 1; \
	fi; \
	rm -f "$$log"; \
	if diff -u docs/reference/cli.md "$$tmp"; then \
		rm -f "$$tmp"; \
		echo "docs/reference/cli.md is in sync with the command tree."; \
	else \
		rm -f "$$tmp"; \
		echo; \
		echo "docs/reference/cli.md is STALE — the command tree moved and the generated"; \
		echo "reference did not. Run 'make docs-cli' and commit the result."; \
		exit 1; \
	fi

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

.PHONY: check-output-styles
check-output-styles:  ## Verify ape's built-in output-style table matches the locally-installed Claude Code.
	@# ape folds a built-in style's case before writing `outputStyle`, so the
	@# table in internal/bridge/config is a claim about a vendor surface that
	@# moves on its own schedule — `Concise` and `Proactive` only appeared in
	@# 2.1.237. A stale table does not fail loudly, it fails by HALVES:
	@# lowercase keeps working for the styles ape knows and silently stops for
	@# a newer one, after the working cases have taught users that case does
	@# not matter.
	@#
	@# Same standing as check-prices: hand-curated data about something ape
	@# does not control, gated against the installed binary rather than
	@# trusted. Reads the local install, so it is LOCAL ONLY and never in CI.
	@# Finding zero built-ins FAILS rather than skipping — a probe that cannot
	@# look is not a pass.
	APE_CLAUDE_LIVE=1 go test ./internal/bridge/config/ \
	  -run TestLive_OutputStyleBuiltins -v -count=1

.PHONY: check-hooks
check-hooks:  ## Seed a throwaway project with one real Claude session, then judge the hook contract.
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
	@# This gate used to be OBSERVATIONAL: `ape doctor --only hooks.contract_drift
	@# --cwd <some project you had to supply>`, reading whatever runlogs a past
	@# interactive run happened to leave behind. That made it a gate you had to
	@# find evidence for. It defaulted to this repo, which has no runlogs and
	@# therefore always skipped,
	@# and on the machine where it was finally checked NO project on the whole
	@# filesystem had a hook-events.jsonl — so in its entire existence it had
	@# never once fired, while contributing a permanent SKIP line to
	@# check-harness. That is the same defect that retired the eight
	@# TestParity_* gates in v0.0.55: a skip is not a pass, and a gate that
	@# can only skip reads as one.
	@#
	@# So it now produces its own corpus: copies testdata/apexproject to a temp
	@# dir, drives ONE unattended `ape prompt` session, and judges the runlog
	@# that session wrote. Reproducible on any machine with claude + auth, no
	@# pre-existing project required, and it always returns a verdict.
	@#
	@# The observational read did NOT go away — it is `ape doctor --only
	@# hooks.contract_drift --cwd <project>`, which is where you point it at a
	@# real project. Only the Makefile alias for it is gone.
	@#
	@# The seed spawns a subagent on purpose. `tool_response` is only counted
	@# on Agent-tool PostToolUse and `agent_id` only on SubagentStop, so a
	@# seed that merely answers a question would verify one field of three and
	@# still report green — Observation.OK() passes a field seen zero times.
	@# The test therefore fails a Seen == 0 field as a BROKEN SEED, separately
	@# from the drift verdict.
	@#
	@# Costs one short Haiku session. Never in `make test` or GitHub CI.
	APE_CLAUDE_LIVE=1 go test ./internal/hookdrift/ \
	  -run TestLive_HookContract -v -count=1 -timeout 20m

# The two inputs to check-framework, and they are different things:
#
#   APEX_FRAMEWORK_REPO  a CHECKOUT of apex_process_framework — the framework's
#                        own source. Unset means the checkout gates cannot run;
#                        they report that rather than passing.
#   APEX_PROJECT         a project with the framework INSTALLED — one carrying
#                        _apex/ape-commands.yaml. Used for the installed-command-
#                        surface half, which is the manifest as a skill actually
#                        meets it at run time.
#
#                        The default is this repo, and THIS REPO IS NOT SUCH A
#                        PROJECT: its _apex/ holds a README and nothing else. So
#                        the default skips, loudly, and the half is verified only
#                        when you point it somewhere real. It is also VERSIONED —
#                        a project installed from an older framework verifies the
#                        older contract, which is a stale gate wearing a green
#                        result. v0.15.0 declares 82 required commands, v0.16.0
#                        declares 84.
#
# APEX_PROJECT was called HOOK_PROJECT until check-hooks stopped reading a
# project at all (it now seeds its own runlog). The name then described
# nothing it did, so it follows the one gate that still uses it.
APEX_FRAMEWORK_REPO ?=
APEX_PROJECT ?= .

# HOOK_PROJECT is gone. Without this guard, a stale `make check-framework
# HOOK_PROJECT=~/proj` would silently ignore the flag and gate the default `.`
# instead — a command that looks like it targeted a project and did not. Loud
# beats silent; delete this after a release or two.
ifdef HOOK_PROJECT
$(error HOOK_PROJECT was renamed to APEX_PROJECT (check-hooks no longer reads a project; it seeds its own runlog). Use APEX_PROJECT=$(HOOK_PROJECT))
endif

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
	@#
	@# framework.output_styles rides here for the same reason as the contract
	@# table: both are framework-owned files whose ABSENCE is silent, and the
	@# install is the only place to see whether one arrived. Under --strict it
	@# reports Info (not Warn) when the table is absent, so a project on a
	@# framework that predates it stays green — version skew, not a failure —
	@# while a table that is present and unusable fails the gate.
	@# Guarded on the FILE the check reads, not on the directory. `-d _apex`
	@# is true of this repo — it holds a README and nothing else — so the
	@# guard passed, the doctor ran, both checks reported "not installed",
	@# and the gate printed "0 fail" while verifying nothing. That is the
	@# skip-looks-like-a-pass shape this whole target exists to refuse, and
	@# it fooled the author of these lines for a whole release.
	@if [ -f "$(APEX_PROJECT)/_apex/ape-commands.yaml" ]; then \
		echo "==> installed command surface in $(APEX_PROJECT)"; \
		go run ./cmd/ape doctor --strict --cwd "$(APEX_PROJECT)" \
		  --only framework.command_surface,framework.terminal_contracts,framework.output_styles; \
	else \
		echo "installed command surface NOT verified — this is a skip, not a pass."; \
		echo "  APEX_PROJECT=$(APEX_PROJECT) has no _apex/ape-commands.yaml, so there is no"; \
		echo "  installed manifest to check this binary against."; \
		echo "  Point it at a project that has run \`ape framework update\` against the"; \
		echo "  framework version you are releasing for:"; \
		echo "    make check-framework APEX_FRAMEWORK_REPO=<checkout> APEX_PROJECT=<project>"; \
		echo "  A project installed from an OLDER framework verifies the OLDER contract —"; \
		echo "  the manifest is versioned, so a stale install is a stale gate."; \
	fi

.PHONY: check-harness
check-harness: check-prices check-output-styles check-hooks check-claude ## All local-only gates against the installed Claude Code (prices + output styles + hooks + PTY/model).
	@echo
	@echo "Harness sweep complete against Claude Code $$(claude --version 2>/dev/null || echo 'unknown')."
	@echo "Read the output above: any gate that reported a SKIP was NOT verified — it found no evidence to judge."

.PHONY: ci-local
ci-local: test lint govulncheck docs-check docs-cli-check check-prices xcompile-windows snapshot ## Run every gate CI + release would run (Linux + Windows cross-compile + snapshot).
	@echo
	@echo "Local CI gates green. Safe to push + tag."
	@echo "Catches: Linux test failures, lint, vuln, Windows compile-time portability bugs, release-config regressions."
	@echo "Does NOT catch: Windows runtime behaviour (use a push-to-branch + GitHub Actions Windows runner for that)."
	@echo "Does NOT catch: the installed Claude Code breaking a contract ape drives it through (PTY, models,"
	@echo "                hook payloads) — run 'make check-harness'."
	@echo "Does NOT catch: ape no longer satisfying the APEX framework (command surface, config"
	@echo "                template) — run 'make check-framework APEX_FRAMEWORK_REPO=<checkout>'."
