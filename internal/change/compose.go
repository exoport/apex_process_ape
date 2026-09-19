package change

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/deferred"
)

// Composing the commits: the half of the lane that is ape's alone.
//
// The skill never runs git. It edits, it gates, and it writes down what
// it did; ape turns that into commits. Three kinds, in this order per
// landed goal:
//
//  1. the EVIDENCE commit, carrying the gate output and the triage note.
//     It comes first and stays separate because the release record
//     selects a repair commit by "touching no {development_folder}
//     path", and evidence resolves under {development_folder} on the
//     installs the migration pins. Swept into the goal's commit, the
//     evidence would drop the goal out of that table; left uncommitted,
//     it would fail the next run's preflight;
//  2. the GOAL commit, over the goal's own paths, carrying the request
//     and the gates in its trailers;
//  3. the DEFERRED commit, for the records ape writes from the goal's
//     structured findings, so the tree is clean again before the next
//     goal starts.
//
// Every one of them stages with a literal pathspec and commits with the
// same pathspec: `git add -A -- <paths>` then `git commit -- <paths>`.
// The pathspec on the commit is what makes it only-semantics — a bare
// `git commit -F` takes whatever else is staged, which on a repository
// someone is working in is not a hypothetical.

// CommitKind labels what a composed commit is for.
const (
	KindEvidence = "evidence"
	KindGoal     = "goal"
	KindDeferred = "deferred"
)

// Commit is one commit ape made, or — under --dry-run — one it would
// have made.
type Commit struct {
	SHA     string   `json:"sha"               yaml:"sha"`
	Subject string   `json:"subject"           yaml:"subject"`
	Goal    int      `json:"goal"              yaml:"goal"`
	Kind    string   `json:"kind"              yaml:"kind"`
	Paths   []string `json:"paths"             yaml:"paths"`
	Message string   `json:"message,omitempty" yaml:"message,omitempty"`
}

// ComposeOptions is what the commits need that the contract does not
// carry.
type ComposeOptions struct {
	// ChangeID names the run in every message it composes.
	ChangeID string
	// Request is ape's own copy of the operator's words, byte-verbatim.
	// Empty under --fixes alone, where there is no request and so no
	// Request: trailer.
	Request string
	// FixesID is the deferred record this change discharges.
	FixesID string
	// AllLanded decides between Fixes: and Refs:. A partial run must not
	// read as a discharge — see trailerFor.
	AllLanded bool
	// MessageDir is where ape writes the commit-message files it passes
	// to `git commit -F`. It sits inside the change directory, which is
	// under the ignored ape subtree, so the files never reach the tree.
	MessageDir string
	// Store and Date are the deferred store and the resolved local date
	// its record ids are stamped with.
	Store *deferred.Store
	Date  string
	// Carries holds, per goal, the ownership rows its commit records —
	// derived by ape over the paths that actually changed, never copied
	// from the skill's claim.
	Carries map[int][]string
	// DryRun composes every message and makes no commit.
	DryRun bool
}

// ErrCommitRejected reports a `git commit` that ran and did not produce
// a commit. In practice that is a hook: ape never passes --no-verify, so
// a project's own pre-commit hook can refuse or rewrite at any goal.
//
// The run ends there with the earlier goals committed, this goal and
// every later one left in the tree, and the residue saved. It is exit 1
// rather than a refusal, because nothing about the contract was wrong.
var ErrCommitRejected = errors.New("a commit was rejected")

// errNothingStaged says a commit was not made because there was nothing
// under its paths to commit — a gate that wrote no output, a goal whose
// files were reverted, a run with no deferred findings.
//
// A sentinel rather than a nil commit, because "no commit" is an
// ordinary outcome here and every caller has to handle it: an empty
// commit would assert work that did not happen.
var errNothingStaged = errors.New("nothing to commit under these paths")

// Compose makes every commit the contract earns, in order.
//
// It stops at the first failure and returns what it had made by then:
// the caller needs that list to tell the operator which goals landed
// before the stop, and to save the rest as residue.
func (l Layout) Compose(
	ctx context.Context, root string, c *Contract, r *Reconciliation, o ComposeOptions,
) ([]Commit, error) {
	made := []Commit{}
	for i := range c.Goals {
		g := &c.Goals[i]
		n := i + 1
		if g.Status != GoalLanded {
			// A halted goal commits nothing: its edits are residue, and
			// a goal that never started has none.
			continue
		}
		evidence, err := l.commitEvidence(ctx, root, g, n, r, o)
		hasEvidence := err == nil
		switch {
		case err == nil:
			made = append(made, evidence)
		case !errors.Is(err, errNothingStaged):
			return made, err
		}

		goalCommit, err := l.commitGoal(ctx, root, g, n, hasEvidence, o)
		switch {
		case err == nil:
			made = append(made, goalCommit)
		case !errors.Is(err, errNothingStaged):
			return made, err
		}

		defers, err := l.commitDeferred(ctx, root, g, n, o)
		switch {
		case err == nil:
			made = append(made, defers)
		case !errors.Is(err, errNothingStaged):
			return made, err
		}
	}
	return made, nil
}

// commitEvidence carries every changed file under the goal's evidence
// path, the triage note among them.
//
// Nothing to carry means NO COMMIT, never an empty one: `git add -A` on
// a path that matches nothing exits 128, and an empty commit would
// assert a gate ran and left no output.
func (l Layout) commitEvidence(
	ctx context.Context, root string, g *Goal, n int, r *Reconciliation, o ComposeOptions,
) (Commit, error) {
	files := r.EvidenceFiles[n]
	if len(files) == 0 {
		return Commit{}, errNothingStaged
	}
	subject := fmt.Sprintf("evidence: change %s goal %d", o.ChangeID, n)
	return l.commit(ctx, root, commitSpec{
		subject: subject,
		message: subject + "\n",
		paths:   []string{g.Evidence},
		goal:    n,
		kind:    KindEvidence,
	}, o)
}

// commitGoal makes the goal's own commit, over the goal's paths only.
func (l Layout) commitGoal(
	ctx context.Context, root string, g *Goal, n int, hasEvidence bool, o ComposeOptions,
) (Commit, error) {
	if len(g.Paths) == 0 {
		// A landed goal that changed nothing. The reconciliation has
		// already reported the claim as unmatched; there is no commit to
		// make and nothing is lost by saying so quietly here.
		return Commit{}, errNothingStaged
	}
	msg := composeGoalMessage(g, n, hasEvidence, o)
	return l.commit(ctx, root, commitSpec{
		subject: g.Subject,
		message: msg,
		paths:   g.Paths,
		goal:    n,
		kind:    KindGoal,
	}, o)
}

// composeGoalMessage is the goal's subject and its trailers, in the
// order the framework's readers expect them.
func composeGoalMessage(g *Goal, n int, hasEvidence bool, o ComposeOptions) string {
	var b strings.Builder
	b.WriteString(g.Subject)
	b.WriteString("\n\n")
	if o.FixesID != "" {
		b.WriteString(trailerFor(o) + ": " + o.FixesID + "\n")
	}
	if o.Request != "" {
		// ape's own copy, never the skill's: the request must reach the
		// trailer byte-for-byte, and a paraphrase here would be
		// undetectable afterwards.
		b.WriteString("Request: " + o.Request + "\n")
	}
	if !None(g.Governance) {
		b.WriteString("Governance: " + g.Governance + "\n")
	}
	if !None(g.Gates) {
		b.WriteString("Gates: " + g.Gates + "\n")
	}
	if hasEvidence {
		b.WriteString("Evidence: " + g.Evidence + "\n")
	} else {
		// Stated rather than omitted: "no evidence" and "nobody wrote
		// the line" are different claims, and a reader counting evidence
		// has to be able to tell them apart.
		b.WriteString("Evidence: none\n")
	}
	// One row per owner whose story this change makes less than the whole
	// truth about the path. Derived by ape over what actually changed, so
	// a row the skill did not write still appears.
	for _, row := range o.Carries[n] {
		b.WriteString("Carries: " + row + "\n")
	}
	return b.String()
}

// trailerFor picks Fixes: or Refs:.
//
// `Fixes:` is what discharges a deferred record — the release record and
// the drain both read it as "this is done". On a partial run the work is
// not done, so the record must keep its place in the queue and the
// commit says `Refs:` instead. ape knows the run's outcome before it
// composes anything, which is what makes this decidable at all.
func trailerFor(o ComposeOptions) string {
	if o.AllLanded {
		return "Fixes"
	}
	return "Refs"
}

// commitDeferred writes the goal's structured findings as records and
// commits them, so the tree is clean before the next goal starts.
func (l Layout) commitDeferred(
	ctx context.Context, root string, g *Goal, n int, o ComposeOptions,
) (Commit, error) {
	if len(g.Deferred) == 0 || o.Store == nil {
		return Commit{}, errNothingStaged
	}
	items := make([]deferred.Structured, 0, len(g.Deferred))
	for _, d := range g.Deferred {
		items = append(items, deferred.Structured{
			Title:   d.Title,
			Anchors: d.Anchors,
			Owner:   d.Owner,
			Trigger: d.Trigger,
			Body:    d.Body,
		})
	}
	subject := fmt.Sprintf("chore(deferred): defer from %s goal %d", o.ChangeID, n)
	if o.DryRun {
		return Commit{
			Subject: subject,
			Goal:    n,
			Kind:    KindDeferred,
			Message: subject + "\n\nGenerator: ape change\n",
		}, nil
	}
	res, err := o.Store.IngestStructured(items, deferred.IngestOptions{
		Skill: "apex-maintenance",
		Date:  o.Date,
	})
	if err != nil {
		return Commit{}, fmt.Errorf("write the deferred records: %w", err)
	}
	paths := make([]string, 0, len(res.Stored))
	for _, p := range res.Stored {
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return Commit{}, fmt.Errorf("resolve %s: %w", p, relErr)
		}
		paths = append(paths, filepath.ToSlash(rel))
	}
	return l.commit(ctx, root, commitSpec{
		subject: subject,
		// The Generator: trailer says which door wrote these, so a
		// record ape filed is never mistaken for one a person did.
		message: subject + "\n\nGenerator: ape change\n",
		paths:   paths,
		goal:    n,
		kind:    KindDeferred,
	}, o)
}

// commitSpec is one commit's inputs.
type commitSpec struct {
	subject string
	message string
	paths   []string
	goal    int
	kind    string
}

// CommitFiles makes one only-semantics commit over paths, for a caller
// outside the goal loop — `--queue` and `--drain`, which commit the
// deferred records they write so the tree is clean for the next
// preflight.
//
// Same rules as every commit ape composes: a literal pathspec on both
// the add and the commit, a verbatim message, and hooks left alone.
func CommitFiles(ctx context.Context, root, messageDir, name, message string, paths []string) (Commit, error) {
	return Layout{}.commit(ctx, root, commitSpec{
		subject: firstMessageLine(message),
		message: message,
		paths:   paths,
		kind:    name,
	}, ComposeOptions{MessageDir: messageDir})
}

// firstMessageLine is the subject of a composed message.
func firstMessageLine(msg string) string {
	line, _, _ := strings.Cut(msg, "\n")
	return line
}

// IsNothingStaged reports the sentinel: the commit was not made because
// nothing under its paths differed from HEAD.
func IsNothingStaged(err error) bool { return errors.Is(err, errNothingStaged) }

// commit stages the paths and commits exactly them.
func (l Layout) commit(ctx context.Context, root string, spec commitSpec, o ComposeOptions) (Commit, error) {
	out := Commit{
		Subject: spec.subject,
		Goal:    spec.goal,
		Kind:    spec.kind,
		Paths:   spec.paths,
		Message: spec.message,
	}
	if o.DryRun {
		return out, nil
	}
	if err := gitAdd(ctx, root, spec.paths); err != nil {
		return Commit{}, err
	}
	staged, err := gitHasStaged(ctx, root, spec.paths)
	if err != nil {
		return Commit{}, err
	}
	if !staged {
		// Nothing under these paths differs from HEAD. Committing would
		// make an empty commit asserting work that did not happen.
		return Commit{}, errNothingStaged
	}
	msgPath := filepath.Join(o.MessageDir,
		fmt.Sprintf("commit-%d-%s.msg", spec.goal, spec.kind))
	if err := os.MkdirAll(o.MessageDir, 0o755); err != nil {
		return Commit{}, fmt.Errorf("create the message directory: %w", err)
	}
	if err := os.WriteFile(msgPath, []byte(spec.message), 0o600); err != nil {
		return Commit{}, fmt.Errorf("write the commit message: %w", err)
	}
	if err := gitCommit(ctx, root, msgPath, spec.paths); err != nil {
		return Commit{}, err
	}
	sha, err := gitHead(ctx, root)
	if err != nil {
		return Commit{}, err
	}
	out.SHA = sha
	return out, nil
}

// gitAdd stages exactly these paths, with pathspec magic turned OFF.
//
// GIT_LITERAL_PATHSPECS is what stops a path from being read as a
// pattern: a file legitimately named `docs/*.md` or one starting with
// `:` would otherwise match things nobody claimed, and these paths come
// from a model.
func gitAdd(ctx context.Context, root string, paths []string) error {
	args := append([]string{"add", "-A", "--"}, paths...)
	return runGit(ctx, root, args, "git add")
}

// gitCommit commits exactly these paths.
//
// --cleanup=verbatim keeps the message byte-for-byte: the default strips
// comment lines, and a request beginning with `#` would silently lose
// its first line. Hooks are NEVER skipped — a project's pre-commit hook
// is part of what makes its commits legitimate, and a lane that bypassed
// it would be a door around the project's own gate.
func gitCommit(ctx context.Context, root, msgPath string, paths []string) error {
	args := append([]string{"commit", "-F", msgPath, "--cleanup=verbatim", "--"}, paths...)
	if err := runGit(ctx, root, args, "git commit"); err != nil {
		return fmt.Errorf("%w: %w", ErrCommitRejected, err)
	}
	return nil
}

// gitHasStaged reports whether anything under paths is staged.
func gitHasStaged(ctx context.Context, root string, paths []string) (bool, error) {
	args := append([]string{"diff", "--cached", "--quiet", "--"}, paths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = literalPathspecEnv()
	err := cmd.Run()
	if err == nil {
		return false, nil // --quiet exits 0 when there is no difference
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff --cached: %w", err)
}

// gitHead returns the sha HEAD points at.
func gitHead(ctx context.Context, root string) (string, error) {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = root
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runGit(ctx context.Context, root string, args []string, what string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = literalPathspecEnv()
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w (%s)", what, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// literalPathspecEnv turns pathspec magic off for a git invocation.
func literalPathspecEnv() []string {
	return append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
}
