package sprint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Verify exit codes, preserved EXACTLY from
// verify-sprint-status-row.py's documented table. They are load-bearing:
// apex-review-story and apex-code-review branch on 5 specifically ("the
// repair is to re-write the field as the committed value and re-run,
// never to stop the run"), so renumbering would silently change what two
// review skills do.
//
// Exit 1 is deliberately absent. In the Python it meant "PyYAML is not
// installed" — an environment failure that forced those skills to carry
// an eye-check fallback. A static binary cannot produce it, which is what
// lets the framework delete those fallback branches.
const (
	VerifyOK = 0
	// VerifyUnreadable — file unreadable or YAML malformed.
	VerifyUnreadable = 2
	// VerifyMismatch — key missing from development_status, or its value
	// differs from expected.
	VerifyMismatch = 3
	// VerifyTimestampsUnordered — updated_at precedes created_at.
	VerifyTimestampsUnordered = 4
	// VerifyBackwardsWrite — updated_at is earlier than the last committed
	// updated_at. Deterministically repairable: re-write the field as the
	// committed value (or later) and re-run. Never a reason to stop a run.
	VerifyBackwardsWrite = 5
)

// VerifyResult is the outcome of a single-row check.
type VerifyResult struct {
	File     string `json:"file"             yaml:"file"`
	Key      string `json:"key"              yaml:"key"`
	Expected string `json:"expected"         yaml:"expected"`
	Actual   string `json:"actual,omitempty" yaml:"actual,omitempty"`
	Code     int    `json:"code"             yaml:"code"`
	Message  string `json:"message"          yaml:"message"`
	// Committed is the last committed updated_at, when it was consulted.
	Committed string `json:"committed_updated_at,omitempty" yaml:"committed_updated_at,omitempty"`
	// Clamp is the value to re-write when Code is VerifyBackwardsWrite.
	Clamp string `json:"clamp,omitempty" yaml:"clamp,omitempty"`
}

// VerifyRow re-reads the tracker from disk and confirms the write landed:
// the row equals expected, updated_at does not precede created_at, and
// updated_at is not earlier than the value in the last committed revision.
//
// The committed comparison is the one that catches a backwards write.
// Comparing updated_at against created_at alone passes trivially, because
// both are written in the same operation.
func VerifyRow(ctx context.Context, path, key, expected string) VerifyResult {
	res := VerifyResult{File: path, Key: key, Expected: expected}

	tracker, err := Load(path)
	if err != nil {
		// A development_status that is present but not a mapping is a ROW
		// problem (3), not a read problem (2) — the file parsed fine, it
		// just cannot answer the question. That split is the Python's and
		// the two codes route to different branches in the calling skills.
		res.Code = VerifyUnreadable
		if errors.Is(err, ErrNotMapping) {
			res.Code = VerifyMismatch
		}
		res.Message = err.Error()
		return res
	}
	if tracker.Missing {
		res.Code = VerifyUnreadable
		res.Message = path + " does not exist"
		return res
	}
	if tracker.Raw == nil {
		// Empty, or nothing but comments: it did not parse to a mapping, so
		// there is no tracker here to verify against.
		res.Code = VerifyUnreadable
		res.Message = path + " did not parse to a mapping"
		return res
	}

	actual, ok := tracker.RowStatus(key)
	if !ok {
		res.Code = VerifyMismatch
		res.Message = fmt.Sprintf("key %q is absent from %s", key, StatusKey)
		return res
	}
	res.Actual = actual
	if actual != expected {
		res.Code = VerifyMismatch
		res.Message = fmt.Sprintf("row %q is %q, expected %q", key, actual, expected)
		return res
	}

	created := tracker.TopLevelString("created_at")
	updated := tracker.TopLevelString("updated_at")
	if created != "" && updated != "" && updated < created {
		res.Code = VerifyTimestampsUnordered
		res.Message = fmt.Sprintf("updated_at %q precedes created_at %q — corrupt write", updated, created)
		return res
	}

	committed, found := committedUpdatedAt(ctx, path)
	if found {
		res.Committed = committed
		// BOTH sides must be well-formed stamps before they are compared.
		// A lexicographic compare against anything else is meaningless and
		// dangerous in one specific direction: exit 5 tells the caller to
		// re-write updated_at AS THE REPORTED CLAMP, so comparing against a
		// malformed committed value would instruct a skill to write that
		// malformed value into the tracker.
		if isStamp(updated) && isStamp(committed) && updated < committed {
			res.Code = VerifyBackwardsWrite
			res.Clamp = committed
			res.Message = fmt.Sprintf(
				"updated_at %q is earlier than the last committed value %q — re-write it as the committed value (or later) and re-run",
				updated, committed)
			return res
		}
	}

	res.Code = VerifyOK
	res.Message = fmt.Sprintf("row %q is %q; timestamps ordered", key, actual)
	return res
}

// stampRe is the YYYYMMDDHHMMSS form these fields are specified to use.
// A value in any other shape is not compared at all.
var stampRe = regexp.MustCompile(`^\d{14}$`)

func isStamp(s string) bool { return stampRe.MatchString(s) }

// committedUpdatedAt reads updated_at from HEAD's version of the tracker.
// Absence is a normal outcome — an uncommitted tracker, a fresh repo, or
// no git at all — and the caller simply skips the comparison rather than
// treating it as a failure.
func committedUpdatedAt(ctx context.Context, path string) (string, bool) {
	dir := filepath.Dir(path)
	rel, err := gitRelPath(ctx, dir, path)
	if err != nil {
		return "", false
	}
	out, err := runGit(ctx, dir, "show", "HEAD:"+rel)
	if err != nil {
		return "", false
	}
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(out), &raw); err != nil {
		return "", false
	}
	v, ok := raw["updated_at"]
	if !ok || v == nil {
		return "", false
	}
	return fmt.Sprintf("%v", v), true
}

// gitRelPath returns path relative to the repository root, in git's
// forward-slash form.
func gitRelPath(ctx context.Context, dir, path string) (string, error) {
	top, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(strings.TrimSpace(top), abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// ErrGitFailed reports a git invocation that did not succeed.
var ErrGitFailed = errors.New("git command failed")

// runGit runs git in dir and returns stdout. Kept here rather than reused
// from internal/pipeline or internal/framework because both of those keep
// their helpers unexported; consolidating the three copies is noted in
// the plan as a follow-up rather than done mid-deliverable.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: git %s: %w (%s)",
			ErrGitFailed, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
