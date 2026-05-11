package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// gitStatusPorcelain returns the parsed output of `git status
// --porcelain` against projectRoot. Non-empty result means the tree
// has uncommitted changes (untracked + modified + staged, .gitignore-
// honoring). Returns an error only when git itself fails — an empty
// working tree is success with an empty string.
func gitStatusPorcelain(ctx context.Context, projectRoot string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = projectRoot
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git status: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// gitAddAll stages every change in projectRoot (the equivalent of
// `git add -A`). Returns the captured stderr on failure so callers can
// record the diagnostic.
func gitAddAll(ctx context.Context, projectRoot string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "add", "-A")
	cmd.Dir = projectRoot
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git add -A: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// gitCommit runs `git commit -m <message>` against projectRoot. Returns
// the resulting commit SHA on success, or a non-nil error carrying the
// captured stderr on failure. We never pass `--no-verify`; if the
// repo's pre-commit hook fails, the commit fails and the caller must
// abort the pipeline (PLAN-4 / C4.4).
func gitCommit(ctx context.Context, projectRoot, message string) (sha string, err error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	cmd.Dir = projectRoot
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git commit: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return gitHeadSHA(ctx, projectRoot)
}

// gitHeadSHA returns the short SHA (7 chars) of HEAD in projectRoot.
// Used immediately after a successful gitCommit to capture the new
// commit's identifier for the manifest.
func gitHeadSHA(ctx context.Context, projectRoot string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--short=7", "HEAD")
	cmd.Dir = projectRoot
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// gitAvailable checks whether `git` is on PATH and the project root
// is inside a working tree. Returns nil when both are true. Used by
// the dirty-tree gate to fail fast with an actionable error before
// any pipeline step runs.
func gitAvailable(ctx context.Context, projectRoot string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git binary not found on PATH; install git or pass --no-commit to skip commit operations")
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = projectRoot
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("project root %q is not a git working tree (run `git init` or pass --no-commit): %w", projectRoot, err)
	}
	return nil
}
