package commitowners

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// State is the git facts a dispatch assertion compares across a run.
//
// Every field is best-effort: a directory that is not a repo, a repo
// with no commits, or a missing git binary all produce a State whose
// Known is false. That is not a silent pass — Assert reports it as a
// skipped assertion, because "we could not look" and "we looked and it
// was fine" are different answers and only one of them is evidence.
type State struct {
	// Head is the full HEAD SHA, empty in a repo with no commits.
	Head string
	// Staged is the sorted list of paths with staged content.
	//
	// The whole list rather than a bool, because the assertion is about
	// what the DISPATCH did. An operator who staged something before
	// invoking `ape task` has not made the skill guilty of it, and a bare
	// "is the index empty" would fail the skill for the caller's own
	// state; comparing the sets catches a skill that added to an already
	// dirty index, which a before/after boolean silently misses.
	Staged []string
	// StashRef is the SHA refs/stash points at, empty when there is no
	// stash.
	StashRef string
	// StashDepth is the number of entries in the stash reflog.
	StashDepth int
	// Known reports whether the reads succeeded.
	Known bool
}

// Capture reads the current state of the repository at dir.
func Capture(ctx context.Context, dir string) State {
	var st State
	head, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		// Either not a repo, or a repo with no commits. Distinguish them:
		// the second is a legitimate state a dispatch can run in.
		if _, insideErr := git(ctx, dir, "rev-parse", "--is-inside-work-tree"); insideErr != nil {
			return st
		}
	}
	st.Head = head
	st.Known = true
	st.Staged = staged(ctx, dir)
	st.StashRef, _ = git(ctx, dir, "rev-parse", "--quiet", "--verify", "refs/stash")
	st.StashDepth = stashDepth(ctx, dir)
	return st
}

// emptyTree is git's well-known empty-tree object. Diffing the index
// against it is how a repo with no commits answers "what is staged?" —
// `git diff --cached` alone needs a HEAD to compare with.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func staged(ctx context.Context, dir string) []string {
	rev := "HEAD"
	if _, err := git(ctx, dir, "rev-parse", "HEAD"); err != nil {
		rev = emptyTree
	}
	out, err := git(ctx, dir, "diff", "--cached", "--name-only", rev)
	if err != nil || out == "" {
		return nil
	}
	paths := strings.Split(out, "\n")
	sort.Strings(paths)
	return paths
}

func stashDepth(ctx context.Context, dir string) int {
	out, err := git(ctx, dir, "reflog", "show", "--format=%H", "refs/stash")
	if err != nil || out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

// Violation is one failed assertion.
type Violation struct {
	// Check names the assertion: see the Check* constants.
	Check string `json:"check" yaml:"check"`
	Skill string `json:"skill" yaml:"skill"`
	// Message states the defect in the terms the framework's declaration
	// uses, so an operator can match it to a CSV row.
	Message string `json:"message" yaml:"message"`
}

// Assertion check names.
const (
	// CheckHeadMoved — a non-committer advanced HEAD.
	CheckHeadMoved = "dispatch.head_moved"
	// CheckIndexStaged — a non-committer left content staged. `git add`
	// leaves HEAD alone, so this is not redundant with CheckHeadMoved.
	CheckIndexStaged = "dispatch.index_staged"
	// CheckStashChanged — a non-committer touched the stash. A stash
	// silently destroys the caller's working tree, which is why the
	// operating rules forbid it outright.
	CheckStashChanged = "dispatch.stash_changed"
	// CheckNoCommit — a declared committer made none.
	CheckNoCommit = "dispatch.no_commit"
	// CheckMessageFormat — a declared committer's commit does not match
	// any of its declared message formats.
	CheckMessageFormat = "dispatch.message_format"
)

// Result is the verdict of one dispatch's assertions.
//
//nolint:tagliatelle // this rides `ape task --json`, whose envelope is snake_case by contract
type Result struct {
	Skill string `json:"skill" yaml:"skill"`
	// Declared reports whether the skill is in the CSV — which of the two
	// assertions ran.
	Declared bool `json:"declared" yaml:"declared"`
	// Skipped reports that the git state could not be read, so neither
	// assertion could run. Never reported as a pass.
	Skipped bool `json:"skipped,omitempty" yaml:"skipped,omitempty"`
	// SkipReason says why, when Skipped.
	SkipReason string `json:"skip_reason,omitempty" yaml:"skip_reason,omitempty"`
	// Subjects are the commit subjects observed across the dispatch.
	Subjects   []string    `json:"subjects,omitempty"   yaml:"subjects,omitempty"`
	Violations []Violation `json:"violations,omitempty" yaml:"violations,omitempty"`
}

// OK reports whether the dispatch honoured its declaration. A skipped
// assertion is not OK-by-default: callers branch on Skipped separately.
func (r Result) OK() bool { return len(r.Violations) == 0 }

// Skipped builds the verdict for a dispatch whose assertion was never
// ATTEMPTED, as distinct from one that ran and found nothing.
//
// It exists because the zero Result is the wrong thing to emit there.
// `{"skill":"","declared":false}` marshals with no violations and OK()
// true, so a consumer reads "the non-committer assertion ran and was
// clean" — a pass nobody earned, on a dispatch where nothing was checked.
// The envelope field's own contract is that a consumer must be able to
// tell "asserted and clean" from "could not assert"; a zero value defeats
// exactly that.
func Skipped(skill, reason string) Result {
	return Result{Skill: skill, Skipped: true, SkipReason: reason}
}

// Assert compares the state before and after a dispatch of skill against
// the table's declaration.
//
// subjects is the commit subjects made across the dispatch, oldest
// first, which the caller reads with `git log before..HEAD`. It is
// passed in rather than read here because the caller already collects it
// for the run envelope.
func (t *Table) Assert(skill string, before, after State, subjects []string) Result {
	res := Result{Skill: skill, Declared: t.Owns(skill), Subjects: subjects}
	if !before.Known || !after.Known {
		res.Skipped = true
		res.SkipReason = "the project is not a git repository, or git could not be run"
		return res
	}

	// No declaration on disk means there is nothing to assert against.
	//
	// "An absent file means no skill commits" is the declared semantics,
	// and taken literally it would put every dispatch on the
	// non-committer assertion. That is wrong in a way that only shows up
	// on a real project: it converts "this project has not adopted the
	// declaration" into "this project asserts that nothing may commit",
	// which is a claim nobody made — and every framework skill that
	// legitimately commits then fails on every project that has not
	// adopted the CSV, which today is all of them.
	//
	// Found by the framework's own eval: a successful
	// `apex-story-batch-dev` dispatch made six correctly-formatted
	// commits and `ape task` exited 6, discarding an 89-minute capture.
	//
	// The authority for either assertion is the declaration. Without one
	// this is a SKIP — reported, never a silent pass — for the same
	// reason an unreadable git state is: a check that cannot tell must
	// say so rather than inventing a verdict in either direction.
	if !t.Present {
		res.Skipped = true
		res.SkipReason = "no " + FileName + " in the project: nothing declares which skills commit, " +
			"so neither assertion has a basis"
		return res
	}

	if !res.Declared {
		t.assertNonCommitter(&res, before, after)
		return res
	}
	t.assertCommitter(&res, skill, before, after, subjects)
	return res
}

// assertNonCommitter is the three-part assertion. All three are checked
// and reported together rather than short-circuiting: a skill that both
// staged content and stashed has two defects, and reporting one would
// send the operator round the loop twice.
func (t *Table) assertNonCommitter(res *Result, before, after State) {
	if before.Head != after.Head {
		res.Violations = append(res.Violations, Violation{
			Check: CheckHeadMoved, Skill: res.Skill,
			Message: fmt.Sprintf(
				"HEAD moved from %s to %s, but this skill is absent from %s — "+
					"a skill that is not a declared committer must leave HEAD alone",
				shortSHA(before.Head), shortSHA(after.Head), FileName,
			),
		})
	}
	if added := addedPaths(before.Staged, after.Staged); len(added) > 0 {
		res.Violations = append(res.Violations, Violation{
			Check: CheckIndexStaged, Skill: res.Skill,
			Message: fmt.Sprintf(
				"the dispatch staged %d path(s) that were not staged before it (%s) — "+
					"`git add` leaves HEAD unchanged, so this is a separate defect from a commit",
				len(added), strings.Join(added, ", ")),
		})
	}
	if before.StashRef != after.StashRef || before.StashDepth != after.StashDepth {
		res.Violations = append(res.Violations, Violation{
			Check: CheckStashChanged, Skill: res.Skill,
			Message: fmt.Sprintf(
				"the stash changed across the dispatch (%s, depth %d → %s, depth %d) — "+
					"a stash silently destroys the caller's working tree, which is why "+
					"the operating rules forbid it outright",
				stashLabel(before.StashRef), before.StashDepth,
				stashLabel(after.StashRef), after.StashDepth,
			),
		})
	}
}

// assertCommitter is the predicate that catches a suppressed commit: at
// least one commit, and EVERY commit matching one of the skill's rows.
func (t *Table) assertCommitter(res *Result, skill string, before, after State, subjects []string) {
	// HEAD moved but the subject list came back empty: the two reads
	// disagree, and the subject list is the one that failed. `git log`
	// erroring — a cancelled context after a long run, a transient lock —
	// must not be reported as a suppressed commit, which is the most
	// serious verdict this package issues. Skip and say why.
	if before.Head != after.Head && len(subjects) == 0 {
		res.Skipped = true
		res.SkipReason = "HEAD advanced but the commit subjects could not be read, " +
			"so the declared message formats cannot be checked"
		return
	}
	if before.Head == after.Head || len(subjects) == 0 {
		rows := t.Rows(skill)
		formats := make([]string, 0, len(rows))
		for _, row := range rows {
			formats = append(formats, row.Source)
		}
		res.Violations = append(res.Violations, Violation{
			Check: CheckNoCommit, Skill: skill,
			Message: fmt.Sprintf(
				"%s declares this skill a committer (%s) but the dispatch produced no commit — "+
					"a declared committer that commits nothing is a suppressed commit",
				FileName, strings.Join(formats, ", "),
			),
		})
		return
	}
	for _, subject := range subjects {
		if _, ok := t.MatchSubject(skill, subject); ok {
			continue
		}
		res.Violations = append(res.Violations, Violation{
			Check: CheckMessageFormat, Skill: skill,
			Message: fmt.Sprintf(
				"commit subject %q matches none of the message formats %s declares for this skill",
				subject, FileName,
			),
		})
	}
}

// addedPaths returns the paths staged after the dispatch that were not
// staged before it. A path the caller had already staged is the
// caller's, not the skill's.
func addedPaths(before, after []string) []string {
	was := make(map[string]bool, len(before))
	for _, p := range before {
		was[p] = true
	}
	var added []string
	for _, p := range after {
		if !was[p] {
			added = append(added, p)
		}
	}
	return added
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	if sha == "" {
		return "(none)"
	}
	return sha
}

func stashLabel(ref string) string {
	if ref == "" {
		return "no stash"
	}
	return shortSHA(ref)
}

// git runs one git command and returns trimmed stdout.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (%s)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
