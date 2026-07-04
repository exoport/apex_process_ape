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
build:       ## Build the ape binary into ./ape.
	go build -o $(BIN) ./cmd/ape

.PHONY: install
install:     ## Build and install ape to INSTALL_DIR (default: /usr/local/bin).
	@go build -o $(BIN) ./cmd/ape
	@install -m 755 $(BIN) $(INSTALL_DIR)/$(BIN)
	@rm -f $(BIN)
	@echo "installed $(BIN) to $(INSTALL_DIR)/$(BIN)"

.PHONY: test
test:        ## Run all tests with the race detector.
	go test -race ./...

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
govulncheck: $(GOVULNCHECK) ## Scan for known vulnerabilities (pinned via bingo).
	$(GOVULNCHECK) ./...

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

.PHONY: docs-check
docs-check:  ## Verify docs/ links resolve and every doc is reachable from docs/README.md.
	python3 scripts/check-docs-links.py docs

.PHONY: ci-local
ci-local: test lint govulncheck docs-check xcompile-windows snapshot ## Run every gate CI + release would run (Linux + Windows cross-compile + snapshot).
	@echo
	@echo "Local CI gates green. Safe to push + tag."
	@echo "Catches: Linux test failures, lint, vuln, Windows compile-time portability bugs, release-config regressions."
	@echo "Does NOT catch: Windows runtime behaviour (use a push-to-branch + GitHub Actions Windows runner for that)."
