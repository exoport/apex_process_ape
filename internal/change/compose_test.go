package change

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/deferred"
	"github.com/stretchr/testify/require"
)

// composeFixture is a repository mid-run: the skill has edited a product
// file, written its gate output under the evidence folder, and stopped.
// Nothing is committed and nothing is staged — which is exactly the
// state ape finds when the dispatch returns.
func composeFixture(t *testing.T) (root string, goals []Goal, l Layout) {
	t.Helper()
	root = gitRepo(t)
	write(t, root, "docs/reference/cli.md", "old\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")

	write(t, root, "docs/reference/cli.md", "regenerated\n")
	write(t, root, "development/governance/evidence/20260919-a/gate.txt", "PASS\n")

	l = testLayout()
	goals = []Goal{landedGoal()}
	require.NoError(t, l.ValidateGoals(goals))
	return root, goals, l
}

func logSubjects(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--format=%s")
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func commitBody(t *testing.T, root, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format=%B", rev)
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err)
	return string(out)
}

func TestCompose_EvidenceCommitComesFirstAndCarriesOnlyTheEvidence(t *testing.T) {
	root, goals, l := composeFixture(t)
	c := &Contract{Status: StatusLanded, GoalsTotal: 1, GoalsLanded: 1, Goals: goals}
	r := l.Reconcile(mustChanged(t, root), goals)
	require.Empty(t, r.Unclaimed)

	made, err := l.Compose(context.Background(), root, c, r, ComposeOptions{
		ChangeID:   "20260919-120000-abc1234",
		Request:    "the CLI reference is out of date",
		AllLanded:  true,
		MessageDir: t.TempDir(),
		Date:       "2026-09-19",
	})
	require.NoError(t, err)
	require.Len(t, made, 2)

	require.Equal(t, KindEvidence, made[0].Kind)
	require.Equal(t, "evidence: change 20260919-120000-abc1234 goal 1", made[0].Subject)
	require.Equal(t, KindGoal, made[1].Kind)
	require.Equal(t, "docs(cli): regenerate the reference", made[1].Subject)

	// Newest first: the goal's commit sits on top of the evidence one.
	require.Equal(t, []string{
		"docs(cli): regenerate the reference",
		"evidence: change 20260919-120000-abc1234 goal 1",
		"base",
	}, logSubjects(t, root))

	// Each commit carries ONLY its own paths. The evidence commit must
	// not carry the product file, or the release record's table 8 —
	// which selects on "touching no development/ path" — would drop the
	// goal.
	require.Equal(t, []string{"development/governance/evidence/20260919-a/gate.txt"},
		filesIn(t, root, made[0].SHA))
	require.Equal(t, []string{"docs/reference/cli.md"}, filesIn(t, root, made[1].SHA))

	body := commitBody(t, root, made[1].SHA)
	require.Contains(t, body, "Request: the CLI reference is out of date")
	require.Contains(t, body, "Gates: make docs-cli-check")
	require.Contains(t, body, "Evidence: development/governance/evidence/20260919-a")
	require.True(t, l.tidy(t, root), "the tree is clean once both commits are made")
}

// A goal whose gate wrote nothing gets NO evidence commit — never an
// empty one — and says so in its own trailer, because "no evidence" and
// "nobody wrote the line" are different claims.
func TestCompose_NoEvidenceFilesMeansNoEvidenceCommit(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "docs/reference/cli.md", "old\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")
	write(t, root, "docs/reference/cli.md", "new\n")

	l := testLayout()
	goals := []Goal{landedGoal()}
	require.NoError(t, l.ValidateGoals(goals))
	c := &Contract{Status: StatusLanded, GoalsTotal: 1, GoalsLanded: 1, Goals: goals}
	r := l.Reconcile(mustChanged(t, root), goals)

	made, err := l.Compose(context.Background(), root, c, r, ComposeOptions{
		ChangeID: "20260919-120000-abc1234", AllLanded: true, MessageDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, made, 1)
	require.Equal(t, KindGoal, made[0].Kind)
	require.Contains(t, commitBody(t, root, made[0].SHA), "Evidence: none")
}

// A partial run must not read as a discharge: the record keeps its place
// in the queue, and the commit says Refs: instead of Fixes:.
func TestCompose_APartialRunRefsTheRecordRatherThanFixingIt(t *testing.T) {
	root, goals, l := composeFixture(t)
	c := &Contract{Status: StatusHalted, GoalsTotal: 1, GoalsLanded: 1, Goals: goals}
	r := l.Reconcile(mustChanged(t, root), goals)

	made, err := l.Compose(context.Background(), root, c, r, ComposeOptions{
		ChangeID:   "20260919-120000-abc1234",
		FixesID:    "DW-20260919-a1b2c3",
		AllLanded:  false,
		MessageDir: t.TempDir(),
	})
	require.NoError(t, err)
	body := commitBody(t, root, made[1].SHA)
	require.Contains(t, body, "Refs: DW-20260919-a1b2c3")
	require.NotContains(t, body, "Fixes:", "a partial run never reads as discharged")
}

func TestCompose_DeferredFindingsBecomeRecordsAndACommit(t *testing.T) {
	root, goals, l := composeFixture(t)
	goals[0].Deferred = []Defer{{
		Title:   "the glob arm is still missing",
		Anchors: []string{"internal/change/validate.go:12"},
		Owner:   "maintenance",
		Trigger: "when a corpus shows a prescan that was wrong",
		Body:    "The finding, at length.\n",
	}}
	goals[0].FindingsDeferred = 1
	c := &Contract{Status: StatusLanded, GoalsTotal: 1, GoalsLanded: 1, Goals: goals}
	r := l.Reconcile(mustChanged(t, root), goals)

	store := deferred.New(filepath.Join(root, "development", "deferred"))
	made, err := l.Compose(context.Background(), root, c, r, ComposeOptions{
		ChangeID: "20260919-120000-abc1234", AllLanded: true,
		MessageDir: t.TempDir(), Store: store, Date: "2026-09-19",
	})
	require.NoError(t, err)
	require.Len(t, made, 3)
	require.Equal(t, KindDeferred, made[2].Kind)
	require.Equal(t, "chore(deferred): defer from 20260919-120000-abc1234 goal 1", made[2].Subject)
	require.Contains(t, commitBody(t, root, made[2].SHA), "Generator: ape change")

	loaded, err := store.Load(deferred.LoadOptions{})
	require.NoError(t, err)
	require.Len(t, loaded.Records, 1)
	require.Equal(t, "the glob arm is still missing", loaded.Records[0].Title)

	// The tree is clean after the goal, so the next one starts from the
	// same state this one did.
	require.True(t, l.tidy(t, root))
}

// --dry-run composes every message and makes no commit, leaving the
// tree exactly as the run left it.
func TestCompose_DryRunCommitsNothing(t *testing.T) {
	root, goals, l := composeFixture(t)
	c := &Contract{Status: StatusLanded, GoalsTotal: 1, GoalsLanded: 1, Goals: goals}
	r := l.Reconcile(mustChanged(t, root), goals)

	made, err := l.Compose(context.Background(), root, c, r, ComposeOptions{
		ChangeID: "20260919-120000-abc1234", Request: "a request",
		AllLanded: true, MessageDir: t.TempDir(), DryRun: true,
	})
	require.NoError(t, err)
	require.Len(t, made, 2)
	require.Empty(t, made[0].SHA, "nothing was committed, so there is no sha to report")
	require.Contains(t, made[1].Message, "Request: a request")
	require.Equal(t, []string{"base"}, logSubjects(t, root))
}

// A pre-commit hook is part of what makes a project's commits
// legitimate, so ape never skips it. When one refuses, the goals before
// it stay committed and the rest is left for the residue.
func TestCompose_AHookRefusalStopsAtThatGoal(t *testing.T) {
	root, goals, l := composeFixture(t)
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755))

	c := &Contract{Status: StatusLanded, GoalsTotal: 1, GoalsLanded: 1, Goals: goals}
	r := l.Reconcile(mustChanged(t, root), goals)

	made, err := l.Compose(context.Background(), root, c, r, ComposeOptions{
		ChangeID: "20260919-120000-abc1234", AllLanded: true, MessageDir: t.TempDir(),
	})
	require.ErrorIs(t, err, ErrCommitRejected)
	require.Empty(t, made, "the first commit is the one that was refused")
	require.Equal(t, []string{"base"}, logSubjects(t, root))
}

func mustChanged(t *testing.T, root string) []string {
	t.Helper()
	changed, err := Changed(context.Background(), root)
	require.NoError(t, err)
	return changed
}

func filesIn(t *testing.T, root, sha string) []string {
	t.Helper()
	cmd := exec.Command("git", "show", "--name-only", "--format=", sha)
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err)
	var files []string
	for l := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files
}

// tidy reports whether the working tree is clean.
func (l Layout) tidy(t *testing.T, root string) bool {
	t.Helper()
	return len(mustChanged(t, root)) == 0
}
