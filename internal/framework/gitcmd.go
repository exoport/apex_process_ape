package framework

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// GitCmd is the git executable name. Indirection lets tests substitute
// a stub binary when verifying error paths.
var GitCmd = "git"

// runGit executes `git <args...>` inside repoDir and returns trimmed
// stdout. Stderr is captured and embedded in the error on failure.
func runGit(ctx context.Context, repoDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, GitCmd, args...)
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// IsGitRepo reports whether dir is the working tree of a git repo.
func IsGitRepo(ctx context.Context, dir string) bool {
	_, err := runGit(ctx, dir, "rev-parse", "--git-dir")
	return err == nil
}

// CurrentBranch returns the abbreviated branch name (e.g., "main"),
// or "HEAD" when the repo is in a detached-HEAD state.
func CurrentBranch(ctx context.Context, repoDir string) (string, error) {
	return runGit(ctx, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
}

// HeadSHA returns the full 40-char SHA at HEAD.
func HeadSHA(ctx context.Context, repoDir string) (string, error) {
	return runGit(ctx, repoDir, "rev-parse", "HEAD")
}

// ExactTag returns the annotated/lightweight tag at HEAD, or empty
// string + nil when HEAD is not on a tagged commit. Distinguishes the
// "no tag" case from real errors.
func ExactTag(ctx context.Context, repoDir string) (string, error) {
	tag, err := runGit(ctx, repoDir, "describe", "--tags", "--exact-match", "HEAD")
	if err != nil {
		// `describe --exact-match` exits 128 when HEAD is not on a
		// tag. Different git versions phrase the message differently
		// ("no tag exactly matches", "No names found, cannot describe
		// anything"); both indicate the same condition.
		msg := err.Error()
		if strings.Contains(msg, "no tag exactly matches") ||
			strings.Contains(msg, "No names found") ||
			strings.Contains(msg, "cannot describe anything") {
			return "", nil
		}
		return "", err
	}
	return tag, nil
}

// RemoteOrigin returns the URL for the "origin" remote, or an error
// if no such remote is configured.
func RemoteOrigin(ctx context.Context, repoDir string) (string, error) {
	return runGit(ctx, repoDir, "remote", "get-url", "origin")
}

// IsClean reports whether the working tree has no modifications.
// Untracked files are included in the porcelain output by default.
func IsClean(ctx context.Context, repoDir string) (bool, error) {
	out, err := runGit(ctx, repoDir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// PorcelainEntry models one line of `git status --porcelain` output:
// the two-char status code plus the affected path. This package treats
// the prefix `??` (untracked) specially during the project-side
// skill-deletion safety check.
type PorcelainEntry struct {
	Status string // 2-char status code, e.g. " M", "M ", "??", "A "
	Path   string
}

// IsUntracked reports whether the entry is an "untracked file" line.
func (p PorcelainEntry) IsUntracked() bool {
	return p.Status == "??"
}

// ParsePorcelain parses the output of `git status --porcelain`.
// Returns nil for empty input.
func ParsePorcelain(out string) []PorcelainEntry {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	lines := strings.Split(out, "\n")
	entries := make([]PorcelainEntry, 0, len(lines))
	for _, line := range lines {
		if len(line) < 4 { //nolint:mnd // 2-char status + space + at least 1-char path = 4
			continue
		}
		entries = append(entries, PorcelainEntry{
			Status: line[:2],
			Path:   strings.TrimSpace(line[3:]),
		})
	}
	return entries
}

// SkillsPorcelain returns the porcelain entries restricted to paths
// under <repoDir>/.claude/skills/apex-*. Used for the project-side
// skill-deletion safety check.
func SkillsPorcelain(ctx context.Context, repoDir string) ([]PorcelainEntry, error) {
	out, err := runGit(ctx, repoDir, "status", "--porcelain", "--", ".claude/skills/apex-*")
	if err != nil {
		return nil, err
	}
	return ParsePorcelain(out), nil
}

// FetchAndFastForward runs `git fetch origin <branch>` then
// `git merge --ff-only origin/<branch>`. Any non-fast-forward
// situation returns an error naming the divergence.
func FetchAndFastForward(ctx context.Context, repoDir, branch string) error {
	if _, err := runGit(ctx, repoDir, "fetch", "origin", branch); err != nil {
		return err
	}
	if _, err := runGit(ctx, repoDir, "merge", "--ff-only", "origin/"+branch); err != nil {
		return fmt.Errorf("framework branch %q diverged from origin (cannot fast-forward): %w", branch, err)
	}
	return nil
}

// ErrGitMissing signals the git binary is absent from PATH. Returned
// from EnsureGitAvailable.
var ErrGitMissing = errors.New("git executable not found on PATH")

// EnsureGitAvailable returns ErrGitMissing if `git` cannot be found.
func EnsureGitAvailable() error {
	if _, err := exec.LookPath(GitCmd); err != nil {
		return ErrGitMissing
	}
	return nil
}
