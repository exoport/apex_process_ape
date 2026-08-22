package framework

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The project .gitignore entry for the sprint tracker's advisory-lock
// sidecar.
//
// `ape sprint reconcile` takes an exclusive lock on `<tracker>.lock` and
// never unlinks it — releasing a lock and deleting the file are different
// acts, and deleting one another process may be waiting on is how the
// mutual exclusion is lost. The framework reconciles at six boundaries, so
// the file appears on essentially every project that runs a batch, and it
// then sits untracked beside the tracker waiting for a `git add -A`. One
// reached a commit in ape's own repository exactly that way.
//
// `ape doctor`'s sprint.lock_ignored row reports the state; setup and
// update fix it, so a project is born ignoring the artifact rather than
// finding out after the first stray commit.

// GitignoreLockPattern is the entry written into the project .gitignore.
//
// It is the tracker's file NAME, deliberately not `*.lock`. A blanket
// `*.lock` is safe in ape's own repository and actively wrong in a user's:
// `Cargo.lock`, `flake.lock`, `Gemfile.lock`, `poetry.lock` and
// `composer.lock` all match it and all belong in history. Ignoring a
// dependency lockfile is the kind of damage that surfaces weeks later as
// an unreproducible build, and it would be ape's fault for writing a
// pattern broader than the problem.
//
// With no leading slash the pattern matches at any depth, so it keeps
// working when `implementation_folder` is configured somewhere else, or
// when a project keeps more than one tracker.
const GitignoreLockPattern = "sprint-status.yaml.lock"

// gitignoreLockBlock is what gets appended: the entry plus the reason.
// A bare pattern in someone's .gitignore is an unexplained line they will
// eventually delete; the comment is what stops that.
const gitignoreLockBlock = "# Advisory-lock sidecar left by `ape sprint reconcile`. It is a runtime\n" +
	"# artifact — the lock is released, but the file is deliberately not\n" +
	"# unlinked (deleting a lock another process may hold loses the mutual\n" +
	"# exclusion). Nothing should commit it.\n" +
	GitignoreLockPattern + "\n"

// ProjectGitignore is the ignore file ape maintains this entry in.
const ProjectGitignore = ".gitignore"

// EnsureLockIgnored adds GitignoreLockPattern to the project's .gitignore
// unless the lock sidecar is already ignored. Reports whether it wrote.
//
// Two ways of already being satisfied, and both are honoured, because a
// project that solved this itself must not get a second redundant entry:
//
//  1. git already ignores the path. This is the authoritative answer and
//     covers a project that wrote `*.lock`, or put the rule in a nested
//     .gitignore, .git/info/exclude, or core.excludesFile.
//  2. The pattern is already a line in the project .gitignore. The
//     fallback for a project that is not a git repository yet, where
//     question 1 has no answer at all.
//
// It only ever APPENDS. The alternative — an apex:managed block, as
// CLAUDE.md uses — would let ape rewrite and collapse regions of a file
// whose whole job is to be hand-curated, to manage exactly one line. The
// cost of appending is that a user who deletes the line gets it back on
// the next update; the cost of managing is that ape reformats an ignore
// file it does not own. Appending is the smaller of the two.
func EnsureLockIgnored(ctx context.Context, projectRoot string) (added bool, err error) {
	path := filepath.Join(projectRoot, ProjectGitignore)

	existing, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, fmt.Errorf("read %s: %w", path, readErr)
	}
	if hasIgnoreLine(existing, GitignoreLockPattern) {
		return false, nil
	}
	if gitAlreadyIgnores(ctx, projectRoot) {
		return false, nil
	}

	var out bytes.Buffer
	out.Write(existing)
	if len(existing) > 0 {
		if !bytes.HasSuffix(existing, []byte("\n")) {
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}
	out.WriteString(gitignoreLockBlock)
	if err := AtomicWriteFile(path, out.Bytes(), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// hasIgnoreLine reports whether pattern is already its own line, ignoring
// surrounding whitespace and comment lines.
func hasIgnoreLine(content []byte, pattern string) bool {
	for line := range strings.SplitSeq(string(content), "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == pattern {
			return true
		}
	}
	return false
}

// gitAlreadyIgnores asks git whether a representative lock path is already
// ignored, so a project that wrote its own broader rule does not collect a
// redundant entry.
//
// The probe path need not exist — `git check-ignore` matches patterns, not
// files. Outside a repository, or with no git on PATH, there is no answer
// and the caller falls back to the literal-line check.
func gitAlreadyIgnores(ctx context.Context, projectRoot string) bool {
	probe := filepath.Join(projectRoot, "development", "implementation", GitignoreLockPattern)
	cmd := exec.CommandContext(ctx, "git", "check-ignore", "-q", "--", probe)
	cmd.Dir = projectRoot
	// Only exit 0 — "git ignores this" — suppresses the write. Exit 1 is
	// "not ignored", and 128 (not a repository) or git being absent is no
	// answer at all; both of those want the entry written, since a project
	// that is not yet a repository is the one most likely to become one and
	// commit the sidecar on its first `git add -A`. The literal-line check
	// above is what stops that from duplicating an existing entry.
	return cmd.Run() == nil
}
