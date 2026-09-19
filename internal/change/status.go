// Package change carries one maintenance change from the skill's
// terminal contract to the commits ape makes from it.
//
// The division of labour is the point: the skill does the work and
// writes down what it did; ape reads that, checks it against the
// repository, and holds the pen. Nothing here trusts a path the skill
// reported — every one is reconciled against what git says changed.
package change

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Changed lists every path in the working tree that differs from HEAD:
// staged, unstaged and untracked, with both sides of a rename.
//
// `--porcelain=v1 -z --untracked-files=all` is one invocation with three
// properties this needs and the default lacks:
//
//   - `-uall` lists the FILES inside a new directory. The default
//     collapses them to a single `dir/` entry, and a change whose
//     evidence landed in a new folder would then reconcile as one path
//     nobody claimed — a refusal for tidiness;
//   - `-z` turns off path quoting, so a filename with a space, a quote
//     or a non-ASCII byte comes back as the bytes git holds. Quoted
//     output would never equal a path read from the contract;
//   - `v1` pins the format against a future default.
//
// `status.showUntrackedFiles=no` in a project's config would hide
// untracked files from this. That is a project saying it does not want
// to see them, and it makes both the clean-tree gate and the
// reconciliation blind in the same direction: callers that must not be
// fooled check emptiness against a tree they also staged.
func Changed(ctx context.Context, root string) ([]string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	cmd.Dir = root
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git status: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return parseStatusZ(stdout.Bytes()), nil
}

// parseStatusZ reads the NUL-separated porcelain v1 stream.
//
// Each record is `XY <path>`. A rename or copy is followed by a SECOND
// NUL-terminated field holding the original path, which is why this
// cannot be a plain split-and-map: read that field as a record and every
// rename would contribute a two-character status line as a path.
func parseStatusZ(out []byte) []string {
	// `XY <path>`: two status letters, a space, and at least one byte of
	// path. Anything shorter is the trailing empty field or a truncation.
	const shortestRecord = 4

	fields := strings.Split(string(out), "\x00")
	seen := map[string]bool{}
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if len(rec) < shortestRecord {
			continue // trailing empty field, or a truncated record
		}
		x, y := rec[0], rec[1]
		seen[rec[3:]] = true
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			// The rename's source. Both sides changed: one path stopped
			// existing and another started.
			if i+1 < len(fields) && fields[i+1] != "" {
				seen[fields[i+1]] = true
				i++
			}
		}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// DetachedHEAD reports whether HEAD names no branch.
//
// A detached HEAD is refused rather than handled: ape is about to make
// several commits, and on a detached HEAD they belong to no branch. The
// operator would find the work only through the reflog, which is the
// same as losing it.
//
// An unborn HEAD — a repository with no commits — is NOT detached. It is
// on a branch that does not exist yet, and the first commit creates it.
func DetachedHEAD(ctx context.Context, root string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--quiet", "HEAD")
	cmd.Dir = root
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git symbolic-ref HEAD: %w", err)
}
