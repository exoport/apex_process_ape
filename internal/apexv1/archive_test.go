package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMergeDelta(t *testing.T) {
	hi := "### MODIFIED [greeting] Requirement: Say hello\nThe system SHALL print Hello, then Hi.\n#### Scenario: plain run\n- GIVEN nothing WHEN greet runs THEN it prints Hello\n- check: ./greet.sh | grep -q Hello\n#### Scenario: hi\n- check: ./greet.sh | grep -q Hi\n"
	cases := []struct {
		name      string
		delta     string
		touches   string
		want      map[string]string // capability → substrings the merged spec must hold
		absent    map[string]string // capability → substrings it must not hold
		order     []string          // requirement order in greeting after merge
		conflicts []string
	}{
		{
			name:  "ADDED appends",
			delta: addedWave, touches: "greeting",
			order: []string{"Say hello", "Say name", "Wave"},
		},
		{
			name:  "MODIFIED replaces in place",
			delta: hi, touches: "greeting",
			want:   map[string]string{"greeting": "The system SHALL print Hello, then Hi."},
			absent: map[string]string{"greeting": "The system SHALL print Hello.\n"},
			order:  []string{"Say hello", "Say name"},
		},
		{
			name:  "REMOVED deletes",
			delta: "### REMOVED [greeting] Requirement: Say name\nReason: r\n", touches: "greeting",
			absent: map[string]string{"greeting": "Say name"},
			order:  []string{"Say hello"},
		},
		{
			name:    "new capability is created",
			delta:   "### ADDED [cli] Requirement: Help\nThe system SHALL print help.\n#### Scenario: h\n- check: ./greet.sh --help\n",
			touches: "cli",
			want:    map[string]string{"cli": "# Cli\nPurpose: Because the greeter must change.\n\n### Requirement: Help\n"},
		},
		{
			name: "all three at once, two capabilities",
			delta: addedWave + "\n" + hi + "\n### REMOVED [greeting] Requirement: Say name\nReason: r\n\n" +
				"### ADDED [cli] Requirement: Help\nx\n#### Scenario: h\n- check: true\n",
			touches: "greeting, cli",
			order:   []string{"Say hello", "Wave"},
			want:    map[string]string{"cli": "### Requirement: Help"},
		},
		{
			name:  "ADDED conflicts with an existing heading",
			delta: "### ADDED [greeting] Requirement: Say name\nx\n#### Scenario: s\n- check: true\n", touches: "greeting",
			conflicts: []string{`ADDED [greeting] "Say name" already exists`},
		},
		{
			name:  "MODIFIED and REMOVED of missing headings, all reported",
			delta: "### MODIFIED [greeting] Requirement: Gone\nx\n#### Scenario: s\n- check: true\n\n### REMOVED [greeting] Requirement: Also gone\nReason: r\n", touches: "greeting",
			conflicts: []string{`MODIFIED [greeting] "Gone" is not in the spec`, `REMOVED [greeting] "Also gone" is not in the spec`},
		},
		{
			name:  "MODIFIED on a capability that does not exist",
			delta: "### MODIFIED [cli] Requirement: Help\nx\n#### Scenario: s\n- check: true\n", touches: "cli",
			conflicts: []string{`MODIFIED [cli] "Help" is not in the spec`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := fixture(t, baseFiles())
			c := ParseChange("c.md", changeDoc("add-x", tc.touches, tc.delta, "- T1 x — check: true\n"))
			require.Empty(t, c.parse)
			updates, conflicts := p.MergeDelta(c)
			if len(tc.conflicts) > 0 {
				require.Nil(t, updates)
				require.Len(t, conflicts, len(tc.conflicts))
				for i, want := range tc.conflicts {
					require.Contains(t, conflicts[i], want)
				}
				return
			}
			require.Empty(t, conflicts)
			byCap := map[string]SpecUpdate{}
			for _, u := range updates {
				byCap[u.Capability] = u
				require.Empty(t, ValidateSpec(u.Path, u.content), "every merged spec passes the project check")
			}
			for capName, want := range tc.want {
				require.Contains(t, byCap[capName].content, want)
			}
			for capName, gone := range tc.absent {
				require.NotContains(t, byCap[capName].content, gone)
			}
			if tc.order != nil {
				var names []string
				for _, r := range ParseSpec(byCap["greeting"].content).Requirements {
					names = append(names, r.Name)
				}
				require.Equal(t, tc.order, names)
			}
		})
	}
}

// reviewedWaveFrom is add-wave reviewed, T2 accepted, in a project made
// of files.
func reviewedWaveFrom(t *testing.T, files map[string]string) *Project {
	t.Helper()
	p := fixture(t, files)
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "development/changes/add-wave/review.md", "fine\n")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	_, err = p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	return p
}

// reviewedWave is a change ready to archive.
func reviewedWave(t *testing.T) *Project {
	t.Helper()
	p := verifiedWave(t)
	_, err := p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	return p
}

func TestArchive(t *testing.T) {
	p := reviewedWave(t)
	now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	mainBefore := run(t, p.Root, "rev-parse", "main")

	_, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.ErrorContains(t, err, "human-review tasks not accepted: T2")
	require.DirExists(t, p.ChangeDir("add-wave"))

	res, err := p.Archive(ctx(), "add-wave", true, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "development/archive/2026-10-15-add-wave", res.ArchivedTo)
	require.False(t, res.Rebased, "main did not move")
	require.False(t, res.Reverified, "only ape's own state commits moved HEAD since the verify")
	require.Equal(t, []string{"T2"}, res.PendingHuman)
	require.NoDirExists(t, p.ChangeDir("add-wave"))

	// D38: archive lands the change on its base branch as ONE commit,
	// subject the change's title, and deletes the change's branch.
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
	require.Equal(t, "main", res.BaseBranch)
	require.Equal(t, res.Commit, HeadSHA(ctx(), p.Root))
	require.Equal(t, mainBefore, run(t, p.Root, "rev-parse", "HEAD~1"), "exactly one new commit on main")
	require.Equal(t, "1", run(t, p.Root, "rev-list", "--count", "--no-merges", "HEAD~1..HEAD"))
	require.Equal(t, "Add wave", subjects(t, p)[0])
	require.Equal(t, "- feat: wave\n\nChange: add-wave", run(t, p.Root, "log", "-1", "--format=%b"),
		"the body carries the branch's own commit subjects, not ape's, then the trailer (H15)")
	require.NotContains(t, strings.Join(subjects(t, p), "\n"), "apex: add-wave in-progress", "no change-branch status commit reaches main")
	require.False(t, p.branchExists(ctx(), "change/add-wave"), "the change branch is deleted")
	require.NotEmpty(t, res.BranchTip)
	require.Equal(t, "apex: add-wave notes", run(t, p.Root, "log", "-1", "--format=%s", res.BranchTip),
		"the review.md the review left uncommitted was committed on the change's branch first")
	files := strings.Split(run(t, p.Root, "show", "--name-only", "--format=", "HEAD"), "\n")
	require.ElementsMatch(t, []string{
		"wave.sh", "development/specs/greeting/spec.md",
		"development/changes/add-wave/state.json", // change.md shows as renamed
		"development/archive/2026-10-15-add-wave/change.md", "development/archive/2026-10-15-add-wave/review.md",
		"development/archive/2026-10-15-add-wave/state.json",
	}, files, "the code, the merged spec and the archive move")
	require.Equal(t, "#!/bin/sh\necho wave\n", read(t, p, "wave.sh"), "main's tree holds the change's code")

	spec := read(t, p, "development/specs/greeting/spec.md")
	require.Contains(t, spec, "### Requirement: Wave")
	st, err := LoadState(filepath.Join(p.Root, res.ArchivedTo, "state.json"))
	require.NoError(t, err)
	require.Equal(t, StatusArchived, st.Status)
	require.True(t, st.AllowPendingHuman)
	require.Equal(t, res.BranchTip, st.BranchTip)
	require.Equal(t, "main", st.BaseBranch)
	// H16: the deleted branch's tip stays reachable.
	require.Equal(t, "refs/apex/archive/add-wave", st.TipRef)
	require.Equal(t, st.TipRef, res.TipRef)
	require.Equal(t, res.BranchTip, run(t, p.Root, "rev-parse", "refs/apex/archive/add-wave"))

	require.Empty(t, run(t, p.Root, "status", "--porcelain"), "the archive commit leaves the tree clean")
	require.Empty(t, p.Check(), "the merged project passes the project check")
	require.Empty(t, p.Changes())
}

// Archive also runs from the base branch: it switches to the change's
// branch, and lands back on the base.
func TestArchive_FromTheBaseBranch(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	commitAll(t, p, "review: add-wave")
	run(t, p.Root, "switch", "-q", "main")
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
	require.Equal(t, "Add wave", subjects(t, p)[0])
	require.NotEmpty(t, res.Commit)
}

// The base branch moved while the change was worked on: archive rebases the
// change branch onto it and re-verifies, because that moved the code.
func TestArchive_BaseMovedRebasesAndReverifies(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	commitAll(t, p, "review: add-wave")
	run(t, p.Root, "switch", "-q", "main")
	write(t, p, "hug.sh", "#!/bin/sh\necho hug\n")
	commitAll(t, p, "feat: hug")
	mainBefore := HeadSHA(ctx(), p.Root)

	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.True(t, res.Rebased)
	require.NotNil(t, res.CatchUp, "the one catch-up path (D40)")
	require.Equal(t, StatusReviewed, res.CatchUp.Status, "inside archive a reviewed change stays reviewed")
	require.True(t, res.CatchUp.VerifyStale)
	require.True(t, res.Reverified)
	require.True(t, res.Verify.Green)
	require.Equal(t, StatusReviewed, res.Verify.Status, "a green re-verify keeps the review")
	require.Equal(t, mainBefore, run(t, p.Root, "rev-parse", "HEAD~1"), "one commit on top of main's")
	require.Equal(t, []string{"Add wave", "feat: hug"}, subjects(t, p)[:2])
	require.FileExists(t, filepath.Join(p.Root, "hug.sh"))
	require.FileExists(t, filepath.Join(p.Root, "wave.sh"))
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
}

func TestArchive_ReverifiesWhenCodeMoved(t *testing.T) {
	// The base moved and broke the change: the re-verify after the rebase
	// is red, the change goes back to in-progress on its branch, and main
	// is untouched.
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	commitAll(t, p, "review: add-wave")
	run(t, p.Root, "switch", "-q", "main")
	write(t, p, "greet.sh", "#!/bin/sh\necho Bye $1\n")
	commitAll(t, p, "fix: say bye")
	mainBefore := HeadSHA(ctx(), p.Root)
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.ErrorContains(t, err, "re-verify at HEAD is red (greeting/Say hello/plain run exit 1")
	require.True(t, res.Reverified)
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	require.Equal(t, StatusInProgress, state(t, p, "add-wave").Status)
	require.Equal(t, greetingSpec, read(t, p, "development/specs/greeting/spec.md"))
	require.Equal(t, mainBefore, run(t, p.Root, "rev-parse", "main"), "main only ever receives verified, reviewed changes")
	require.True(t, p.branchExists(ctx(), "change/add-wave"))

	// Code moved on the change's branch since the review: refused, before
	// any rebase.
	p2 := reviewedWave(t)
	_, err = p2.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	write(t, p2, "wave.sh", "#!/bin/sh\necho wave, now louder\n")
	commitAll(t, p2, "feat: louder")
	_, err = p2.Archive(ctx(), "add-wave", false, RunOptions{})
	require.ErrorContains(t, err, "code changed since the review (wave.sh)")
	require.DirExists(t, p2.ChangeDir("add-wave"))
}

// A change branch that does not rebase onto the base branch: nothing
// reaches the base; the conflict is left on change/<id> (H8), the change
// back to in-progress for apex-apply.
func TestArchive_RebaseConflictLeftOnTheBranch(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	commitAll(t, p, "review: add-wave")
	run(t, p.Root, "switch", "-q", "main")
	write(t, p, "wave.sh", "#!/bin/sh\necho another wave\n")
	commitAll(t, p, "feat: someone else's wave")
	mainBefore := HeadSHA(ctx(), p.Root)

	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, CodeConflicted, ce.Code)
	require.Equal(t, []string{"wave.sh"}, ce.Conflicts)
	require.Equal(t, mainBefore, run(t, p.Root, "rev-parse", "main"))
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	require.Equal(t, []string{"wave.sh"}, p.unmergedPaths(ctx()))
	st := state(t, p, "add-wave")
	require.Equal(t, StatusInProgress, st.Status, "the review is stale: in-progress, not reviewed")
	require.Equal(t, "apex: add-wave conflicted rebasing onto main", subjects(t, p)[0])
}

// A review session may leave review.md uncommitted: archive commits it on
// the change's branch, so it is part of the squash. Anything else
// uncommitted refuses.
func TestArchive_DirtyTree(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	write(t, p, "scratch.txt", "x\n")
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.ErrorContains(t, err, "scratch.txt — commit or discard them first")
	require.NoError(t, os.Remove(filepath.Join(p.Root, "scratch.txt")))
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
}

func TestArchive_ConflictWritesNothing(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	// Another change archived "Wave" first.
	commitAll(t, p, "review: add-wave")
	run(t, p.Root, "switch", "-q", "main")
	write(t, p, "development/specs/greeting/spec.md", greetingSpec+"\n"+strings.Replace(addedWave, "### ADDED [greeting] Requirement:", "### Requirement:", 1))
	commitAll(t, p, "Someone else")
	specBefore := read(t, p, "development/specs/greeting/spec.md")
	mainBefore := HeadSHA(ctx(), p.Root)

	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Contains(t, ce.Conflicts[0], `ADDED [greeting] "Wave" already exists`)
	require.Equal(t, specBefore, read(t, p, "development/specs/greeting/spec.md"))
	require.DirExists(t, p.ChangeDir("add-wave"))
	require.Equal(t, StatusReviewed, state(t, p, "add-wave").Status)
	require.Equal(t, mainBefore, run(t, p.Root, "rev-parse", "main"))
}

func TestArchive_DestinationExistsIsAConflict(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(p.ArchiveDir(), Today()+"-add-wave"), 0o755))
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, greetingSpec, read(t, p, "development/specs/greeting/spec.md"))
}

func TestArchive_NeedsReviewPass(t *testing.T) {
	p := verifiedWave(t)
	_, err := p.Archive(ctx(), "add-wave", true, RunOptions{})
	require.ErrorContains(t, err, "archive needs a green verify and a review pass")
}

func TestArchive_MergedSpecOverCapIsRefused(t *testing.T) {
	p := fixture(t, baseFiles())
	big := "### ADDED [greeting] Requirement: Big\n" + strings.Repeat("The system SHALL be big.\n", 900) + "#### Scenario: s\n- check: true\n"
	addChange(t, p, "add-big", changeDoc("add-big", "greeting", big, "- T1 x — check: true\n"))
	c := ParseChange("", read(t, p, "development/changes/add-big/change.md"))
	updates, conflicts := p.MergeDelta(c)
	require.Empty(t, conflicts)
	require.Contains(t, findingMessages(ValidateSpec("s", updates[0].content)), "split into two capabilities")

	// H18: change check (and so start) finds it first.
	fs := ClassifyFindings(p.CheckChange("add-big"))
	var codes []string
	for _, f := range fs {
		codes = append(codes, f.Code)
	}
	require.Contains(t, codes, "change.spec-too-big")
	require.Contains(t, findingMessages(fs), "after this change, development/specs/greeting/spec.md would be")
	require.Contains(t, findingMessages(fs), "split the capability first")
	commitAll(t, p, "plan: add-big")
	_, err := p.Start(ctx(), "add-big", RunOptions{})
	require.Equal(t, CodeCheckFailed, failCode(t, err))
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
}

// H15: commit.subject renders the squash subject from the change's type,
// title and id; the project's hooks run on the squash (and only on it); a
// hook that refuses rolls archive back with archive.hook-failed and its
// output; the body carries the branch's own subjects and trailers.
func TestArchive_CommitSubjectHooksAndBody(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "project_name: greeter\ncommit:\n  subject: \"{type}: {title} ({id})\"\n"
	p := fixture(t, files)
	doc := strings.Replace(waveChange("add-wave"), "milestone: M1\n", "milestone: M1\ntype: feat\n", 1)
	addChange(t, p, "add-wave", doc)
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	run(t, p.Root, "add", "-A")
	run(t, p.Root, "commit", "-q", "-m", "feat: wave\n\nCarries: goldens/wave.txt\nChange: add-wave")
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n# tidy\n")
	run(t, p.Root, "add", "-A")
	run(t, p.Root, "commit", "-q", "-m", "feat: wave\n\nCarries: goldens/wave.txt\nRefs: #12")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "development/changes/add-wave/review.md", "fine\n")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	_, err = p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)

	// A refusing pre-commit hook: ape's own status commits skip it, the
	// squash does not.
	hook := filepath.Join(p.Root, ".git", "hooks", "pre-commit")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho lint: wave.sh is untidy >&2\nexit 1\n"), 0o755))
	mainBefore := run(t, p.Root, "rev-parse", "main")
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.Equal(t, CodeHookFailed, failCode(t, err))
	require.ErrorContains(t, err, "lint: wave.sh is untidy")
	require.ErrorContains(t, err, "nothing was committed")
	require.Equal(t, mainBefore, run(t, p.Root, "rev-parse", "main"))
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	require.DirExists(t, p.ChangeDir("add-wave"))
	clean(t, p, "a hook-refused archive")

	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho ran > \"$(git rev-parse --git-dir)/hook-ran\"\n"), 0o755))
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.True(t, res.Hooks)
	require.FileExists(t, filepath.Join(p.Root, ".git", "hook-ran"))
	require.Equal(t, "feat: Add wave (add-wave)", res.Subject)
	require.Equal(t, "feat: Add wave (add-wave)", subjects(t, p)[0])
	require.Equal(t, "- feat: wave\n\nCarries: goldens/wave.txt\nRefs: #12\nChange: add-wave", run(t, p.Root, "log", "-1", "--format=%b"))
	require.Equal(t, "add-wave", run(t, p.Root, "log", "-1", "--format=%(trailers:key=Change,valueonly)"))
}

// commit.hooks: skip keeps --no-verify on the squash too.
func TestArchive_CommitHooksSkip(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "commit:\n  hooks: skip\n"
	p := reviewedWaveFrom(t, files)
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755))
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.False(t, res.Hooks)
	require.Equal(t, "Add wave", res.Subject, "the default subject is the title")
}

// D47: type: is checked against the list, and required when the subject
// template uses {type}.
func TestCheckChange_Type(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "commit:\n  subject: \"{type}: {title}\"\n"
	p := fixture(t, files)
	addChange(t, p, "add-wave", waveChange("add-wave"))
	got := findingMessages(p.CheckChange("add-wave"))
	require.Contains(t, got, "frontmatter has no `type:`")
	require.Equal(t, "change.no-type", ClassifyFindings(p.CheckChange("add-wave"))[0].Code)
	addChange(t, p, "add-hug", strings.Replace(changeDoc("add-hug", "greeting", addedWave, "- T1 x — check: true"), "milestone: M1\n", "milestone: M1\ntype: feature\n", 1))
	fs := ClassifyFindings(p.CheckChange("add-hug"))
	require.Len(t, fs, 1)
	require.Equal(t, "change.bad-type", fs[0].Code)

	_, err := Open(writeConfig(t, "commit:\n  hooks: sometimes\n"))
	require.ErrorContains(t, err, "commit.hooks")
	_, err = Open(writeConfig(t, "commit:\n  subject: \"{kind}: {title}\"\n"))
	require.ErrorContains(t, err, "commit.subject")
}

func writeConfig(t *testing.T, cfg string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath(root), []byte(cfg), 0o644))
	return root
}
