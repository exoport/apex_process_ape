package commitowners

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The declaration as PLAN-64 5.2 ships it: two rows per batch skill, and
// an apex-orchestrator row for the conducting session — a persona ape
// never dispatches.
const declaration = `skill,commit_kind,message_regex
apex-story-batch-dev,dev,^dev: story \d+\.\d+ [a-z0-9 ]+$
apex-story-batch-dev,review,^review: story \d+\.\d+ [a-z0-9 ]+$
apex-epic-retrospective,retro,^retro: epic \d+$
apex-epic-retrospective,retro-waive,^retro-waive: epic \d+$
apex-orchestrator,release,^release: prepare .+$
`

func writeCSV(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644))
	return dir
}

func TestLoad_AbsentFileMeansNoSkillCommits(t *testing.T) {
	table, err := Load(t.TempDir())
	require.NoError(t, err)
	require.False(t, table.Present)
	require.False(t, table.Owns("apex-story-batch-dev"),
		"an absent declaration must put every skill on the non-committer assertion")
}

func TestLoad_TwoRowsPerSkillIsTheSchema(t *testing.T) {
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)
	require.True(t, table.Present)

	require.True(t, table.Owns("apex-story-batch-dev"))
	require.Len(t, table.Rows("apex-story-batch-dev"), 2,
		"a dev row and a review row are one skill's declaration, not a duplicate")
	require.Len(t, table.Rows("apex-epic-retrospective"), 2)
	require.False(t, table.Owns("apex-dev-story"),
		"a skill absent from the file owns no commits")
}

// TestLoad_TolerateRowsForSkillsApeNeverDispatches — apex-orchestrator is
// a persona that is adopted, never an `ape task` target. A reader that
// failed on such a row would break every dispatch in the project.
func TestLoad_TolerateRowsForSkillsApeNeverDispatches(t *testing.T) {
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)
	require.True(t, table.Owns("apex-orchestrator"),
		"the row is read like any other; it simply never comes up in a dispatch")
}

func TestLoad_MalformedIsFatal(t *testing.T) {
	for name, body := range map[string]string{
		"wrong header":     "skill,regex\napex-x,^x$\n",
		"bad regex":        "skill,commit_kind,message_regex\napex-x,dev,^([a-z$\n",
		"missing regex":    "skill,commit_kind,message_regex\napex-x,dev,\n",
		"ragged row":       "skill,commit_kind,message_regex\napex-x,dev\n",
		"empty file":       "",
		"header case-only": "SKILL,COMMIT_KIND,MESSAGE_REGEX\napex-x,dev,^x$\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeCSV(t, body))
			if name == "header case-only" {
				// Case is tolerated; the columns are what matter.
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrMalformed,
				"a broken declaration must not degrade to \"no skill commits\" — "+
					"that inversion is exactly what lets a suppressed commit through")
		})
	}
}

func TestMatchSubject_AnchoredByTheFileNotTheReader(t *testing.T) {
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	_, ok := table.MatchSubject("apex-story-batch-dev", "dev: story 1.2 add the thing")
	require.True(t, ok)

	// The row anchors itself, so a subject with a prefix must not match.
	_, ok = table.MatchSubject("apex-story-batch-dev", "wip dev: story 1.2 add the thing")
	require.False(t, ok, "the row's own ^ must still bind — the reader adds no anchors")
}

// --- the two assertions, against real repositories ----------------------

func gitInit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), "git %s", strings.Join(args, " "))
	}
	writeFile(t, dir, "seed.txt", "seed\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-qm", "seed")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

func subjectsSince(t *testing.T, dir, before string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--reverse", "--format=%s", before+"..HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// TestAssert_NonCommitterCleanDispatch is the ordinary case: a skill that
// edits the working tree and commits nothing.
func TestAssert_NonCommitterCleanDispatch(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	before := Capture(context.Background(), repo)
	writeFile(t, repo, "worked.txt", "the skill's output\n") // untracked, uncommitted
	after := Capture(context.Background(), repo)

	res := table.Assert("apex-dev-story", before, after, nil)
	require.True(t, res.OK(), "violations: %+v", res.Violations)
	require.False(t, res.Declared)
	require.False(t, res.Skipped)
}

func TestAssert_NonCommitterMovedHead(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	before := Capture(context.Background(), repo)
	writeFile(t, repo, "sneaky.txt", "x\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", "chore: sneak one in")
	after := Capture(context.Background(), repo)

	res := table.Assert("apex-dev-story", before, after,
		subjectsSince(t, repo, before.Head))
	require.False(t, res.OK())
	require.Equal(t, CheckHeadMoved, res.Violations[0].Check)
}

// TestAssert_NonCommitterStagedTheIndex is the check HEAD alone cannot
// make: `git add` leaves HEAD exactly where it was.
func TestAssert_NonCommitterStagedTheIndex(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	before := Capture(context.Background(), repo)
	writeFile(t, repo, "staged.txt", "x\n")
	runGit(t, repo, "add", ".")
	after := Capture(context.Background(), repo)

	require.Equal(t, before.Head, after.Head, "the premise: HEAD did not move")
	res := table.Assert("apex-dev-story", before, after, nil)
	require.False(t, res.OK())
	require.Equal(t, CheckIndexStaged, res.Violations[0].Check)
}

// TestAssert_NonCommitterStashed is the other check HEAD cannot make, and
// the one the operating rules care most about: a stash silently destroys
// the caller's working tree.
func TestAssert_NonCommitterStashed(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	writeFile(t, repo, "seed.txt", "the caller's uncommitted work\n")
	before := Capture(context.Background(), repo)
	runGit(t, repo, "stash", "push", "-q", "-m", "skill stashed the caller's tree")
	after := Capture(context.Background(), repo)

	require.Equal(t, before.Head, after.Head, "the premise: HEAD did not move")
	res := table.Assert("apex-dev-story", before, after, nil)
	require.False(t, res.OK())
	require.Equal(t, CheckStashChanged, res.Violations[0].Check)
}

// TestAssert_CommitterBatchMakesManyCommits is the predicate that is NOT
// "HEAD advanced by one": a batch dispatch makes a dev and a review
// commit per story, and all of them must match.
func TestAssert_CommitterBatchMakesManyCommits(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	before := Capture(context.Background(), repo)
	for _, subject := range []string{
		"dev: story 1.1 add the greeter",
		"review: story 1.1 add the greeter",
		"dev: story 1.2 add the parser",
		"review: story 1.2 add the parser",
	} {
		writeFile(t, repo, "f"+subject[:9]+".txt", subject+"\n")
		runGit(t, repo, "add", ".")
		runGit(t, repo, "commit", "-qm", subject)
	}
	after := Capture(context.Background(), repo)

	res := table.Assert("apex-story-batch-dev", before, after,
		subjectsSince(t, repo, before.Head))
	require.True(t, res.OK(), "violations: %+v", res.Violations)
	require.True(t, res.Declared)
	require.Len(t, res.Subjects, 4)
}

// TestAssert_CommitterSuppressedItsCommit is the failure PLAN-59 found and
// that this whole mechanism exists to catch.
func TestAssert_CommitterSuppressedItsCommit(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	before := Capture(context.Background(), repo)
	writeFile(t, repo, "done.txt", "the work, uncommitted\n")
	after := Capture(context.Background(), repo)

	res := table.Assert("apex-story-batch-dev", before, after, nil)
	require.False(t, res.OK())
	require.Equal(t, CheckNoCommit, res.Violations[0].Check)
	require.Contains(t, res.Violations[0].Message, "suppressed commit")
}

func TestAssert_CommitterWrongMessageFormat(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	before := Capture(context.Background(), repo)
	writeFile(t, repo, "a.txt", "x\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", "dev: story 1.1 add the greeter")
	writeFile(t, repo, "b.txt", "x\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", "wip: fixing it up")
	after := Capture(context.Background(), repo)

	res := table.Assert("apex-story-batch-dev", before, after,
		subjectsSince(t, repo, before.Head))
	require.False(t, res.OK())
	require.Len(t, res.Violations, 1, "only the offending commit is a violation")
	require.Equal(t, CheckMessageFormat, res.Violations[0].Check)
	require.Contains(t, res.Violations[0].Message, "wip: fixing it up")
}

// TestAssert_SkippedOutsideARepo — "we could not look" is not "we looked
// and it was fine", and only one of those is evidence.
func TestAssert_SkippedOutsideARepo(t *testing.T) {
	dir := t.TempDir()
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	before := Capture(context.Background(), dir)
	after := Capture(context.Background(), dir)

	res := table.Assert("apex-dev-story", before, after, nil)
	require.True(t, res.Skipped)
	require.NotEmpty(t, res.SkipReason)
	require.True(t, res.OK(), "a skip reports no violations, but callers branch on Skipped")
}

// TestAssert_PreStagedIndexIsTheCallersNotTheSkills — an operator who
// staged something before invoking `ape task` has not made the skill
// guilty of it. A bare "is the index empty" check would fail the skill
// for the caller's own state.
func TestAssert_PreStagedIndexIsTheCallersNotTheSkills(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	writeFile(t, repo, "operator.txt", "the caller staged this\n")
	runGit(t, repo, "add", "operator.txt")

	before := Capture(context.Background(), repo)
	require.NotEmpty(t, before.Staged, "the premise: the index is already dirty")
	after := Capture(context.Background(), repo)

	res := table.Assert("apex-dev-story", before, after, nil)
	require.True(t, res.OK(), "violations: %+v", res.Violations)
}

// TestAssert_StagingOnTopOfADirtyIndexIsStillCaught is the case a
// before/after boolean silently missed: the index was dirty going in, so
// "did it become dirty" is false, and the skill still staged a file.
func TestAssert_StagingOnTopOfADirtyIndexIsStillCaught(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	writeFile(t, repo, "operator.txt", "the caller staged this\n")
	runGit(t, repo, "add", "operator.txt")
	before := Capture(context.Background(), repo)

	writeFile(t, repo, "skill.txt", "the skill staged this\n")
	runGit(t, repo, "add", "skill.txt")
	after := Capture(context.Background(), repo)

	res := table.Assert("apex-dev-story", before, after, nil)
	require.False(t, res.OK())
	require.Equal(t, CheckIndexStaged, res.Violations[0].Check)
	require.Contains(t, res.Violations[0].Message, "skill.txt")
	require.NotContains(t, res.Violations[0].Message, "operator.txt",
		"the caller's own staged path must not be reported against the skill")
}

// TestAssert_CommitterInARepoWithNoPriorCommits — before.Head is empty
// there, and a subject list that came back empty would accuse a declared
// committer of suppressing the commit it just made.
func TestAssert_CommitterInARepoWithNoPriorCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		require.NoError(t, cmd.Run())
	}
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	before := Capture(context.Background(), repo)
	require.True(t, before.Known, "an initialised repo with no commits is a known state")
	require.Empty(t, before.Head)

	writeFile(t, repo, "a.txt", "x\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", "dev: story 1.1 add the greeter")
	after := Capture(context.Background(), repo)

	// The subject list the caller passes: with no prior HEAD, every commit
	// reachable from HEAD was made by the run.
	res := table.Assert("apex-story-batch-dev", before, after,
		[]string{"dev: story 1.1 add the greeter"})
	require.True(t, res.OK(), "violations: %+v", res.Violations)
}

// TestAssert_AbsentDeclarationDoesNotConvictACommitter is the eval
// blocker found in the gf-hello-world recapture: a successful
// apex-story-batch-dev dispatch made six correctly-formatted commits, in
// a project with no commit-owners.csv, and `ape task` exited 6.
//
// "Absent file = no skill commits" was the specified semantics, and read
// literally it enforces the NON-committer assertion on every dispatch —
// which turns "this project has not adopted the declaration" into "this
// project asserts that nothing may commit", a claim nobody made. Every
// framework skill that legitimately commits then fails on every project
// that has not adopted the CSV yet, which today is all of them.
func TestAssert_AbsentDeclarationDoesNotConvictACommitter(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(t.TempDir()) // no commit-owners.csv
	require.NoError(t, err)
	require.False(t, table.Present)

	before := Capture(context.Background(), repo)
	for _, subject := range []string{
		"dev: story 1.2 serve the greeting form at",
		"review: story 1.2 serve the greeting form at",
	} {
		writeFile(t, repo, "f"+subject[:9]+".txt", subject+"\n")
		runGit(t, repo, "add", ".")
		runGit(t, repo, "commit", "-qm", subject)
	}
	after := Capture(context.Background(), repo)

	res := table.Assert("apex-story-batch-dev", before, after,
		subjectsSince(t, repo, before.Head))
	require.True(t, res.OK(),
		"an absent declaration is no basis to convict a committer: %+v", res.Violations)
	require.True(t, res.Skipped, "and it must say it could not judge, not pass silently")
}

// TestAssert_HeadMovedButSubjectsUnreadableIsASkip covers the other
// candidate raised against this package: if `git log` fails while
// `rev-parse` succeeded, the two reads disagree. Reporting "suppressed
// commit" — the most serious verdict here — on the strength of the read
// that FAILED would be exactly backwards.
func TestAssert_HeadMovedButSubjectsUnreadableIsASkip(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)

	before := Capture(context.Background(), repo)
	writeFile(t, repo, "a.txt", "x\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", "dev: story 1.1 add the greeter")
	after := Capture(context.Background(), repo)

	// nil subjects simulates the log read failing, not an absent commit.
	res := table.Assert("apex-story-batch-dev", before, after, nil)
	require.True(t, res.Skipped, "a failed read is not evidence of a suppressed commit")
	require.True(t, res.OK(), "violations: %+v", res.Violations)
	require.Contains(t, res.SkipReason, "could not be read")
}

// TestAssert_DeclaredNonCommitterStillEnforced — the fix must not
// disarm the assertion where a declaration DOES exist and does not list
// the skill. That is a real statement about the skill, unlike an absent
// file.
func TestAssert_DeclaredNonCommitterStillEnforced(t *testing.T) {
	repo := gitInit(t)
	table, err := Load(writeCSV(t, declaration))
	require.NoError(t, err)
	require.True(t, table.Present)

	before := Capture(context.Background(), repo)
	writeFile(t, repo, "sneaky.txt", "x\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", "chore: sneak one in")
	after := Capture(context.Background(), repo)

	res := table.Assert("apex-dev-story", before, after,
		subjectsSince(t, repo, before.Head))
	require.False(t, res.OK(), "a declared non-committer that commits is still a violation")
	require.False(t, res.Skipped)
	require.Equal(t, CheckHeadMoved, res.Violations[0].Check)
}

// TestSkipped_IsNotAZeroResult is the distinction the envelope's own
// contract rests on: a consumer must be able to tell "asserted and clean"
// from "could not assert".
//
// The zero Result defeats that. It marshals as
// `{"skill":"","declared":false}` — no violations, OK() true, Skipped
// false — which reads as a clean non-committer assertion. `ape task
// --task-commit` used to emit exactly that, on the one path where nothing
// is asserted at all.
func TestSkipped_IsNotAZeroResult(t *testing.T) {
	var zero Result
	require.True(t, zero.OK(), "the zero value reads as a pass, which is the hazard")
	require.False(t, zero.Skipped)

	got := Skipped("apex-shard-doc", "--task-commit: ape makes the commit")
	require.True(t, got.Skipped)
	require.Equal(t, "apex-shard-doc", got.Skill, "a skip still names the dispatch it is about")
	require.Contains(t, got.SkipReason, "--task-commit")

	blob, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(blob), `"skipped":true`)
	require.Contains(t, string(blob), `"skip_reason"`)

	// A skip carries no violations, so it must not fail the run — the
	// caller branches on Skipped, never on OK() alone.
	require.True(t, got.OK())
}
