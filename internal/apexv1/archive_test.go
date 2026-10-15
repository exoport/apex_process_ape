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
	hi := "### MODIFIED [greeting] Requirement: Say hello\nThe system SHALL print Hi.\n#### Scenario: hi\n- check: ./greet.sh | grep -q Hi\n"
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
			want:   map[string]string{"greeting": "The system SHALL print Hi."},
			absent: map[string]string{"greeting": "The system SHALL print Hello."},
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

	_, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.ErrorContains(t, err, "human-review tasks not accepted: T2")
	require.DirExists(t, p.ChangeDir("add-wave"))

	res, err := p.Archive(ctx(), "add-wave", true, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "development/changes/archive/2026-10-15-add-wave", res.ArchivedTo)
	require.False(t, res.Reverified, "only ape's own state commits moved HEAD since the verify")
	require.Equal(t, []string{"T2"}, res.PendingHuman)
	require.NoDirExists(t, p.ChangeDir("add-wave"))

	spec := read(t, p, "development/specs/greeting/spec.md")
	require.Contains(t, spec, "### Requirement: Wave")
	st, err := LoadState(filepath.Join(p.Root, res.ArchivedTo, "state.json"))
	require.NoError(t, err)
	require.Equal(t, StatusArchived, st.Status)
	require.True(t, st.AllowPendingHuman)

	require.Equal(t, "archive: add-wave", subjects(t, p)[0])
	require.Contains(t, run(t, p.Root, "log", "-1", "--format=%b"), "Change: add-wave")
	require.Equal(t, res.Commit, HeadSHA(ctx(), p.Root))
	require.Empty(t, run(t, p.Root, "status", "--porcelain"), "the archive commit leaves the tree clean")
	require.Empty(t, p.Check(), "the merged project passes the project check")
}

func TestArchive_ReverifiesWhenCodeMoved(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)

	write(t, p, "wave.sh", "#!/bin/sh\nexit 1\n")
	commitAll(t, p, "fix: oops")
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.ErrorContains(t, err, "re-verify at HEAD is red (T1 exit 1)")
	require.True(t, res.Reverified)
	require.Equal(t, StatusInProgress, state(t, p, "add-wave").Status)
	require.Equal(t, greetingSpec, read(t, p, "development/specs/greeting/spec.md"))

	// Code moved but still green: the review no longer covers it.
	p2 := reviewedWave(t)
	_, err = p2.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	write(t, p2, "wave.sh", "#!/bin/sh\necho wave, now louder\n")
	commitAll(t, p2, "feat: louder")
	_, err = p2.Archive(ctx(), "add-wave", false, RunOptions{})
	require.ErrorContains(t, err, "code changed since the review (wave.sh)")
	require.DirExists(t, p2.ChangeDir("add-wave"))
}

func TestArchive_ConflictWritesNothing(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	// Another change archived "Wave" first.
	write(t, p, "development/specs/greeting/spec.md", greetingSpec+"\n"+strings.Replace(addedWave, "### ADDED [greeting] Requirement:", "### Requirement:", 1))
	commitAll(t, p, "archive: someone-else")
	specBefore := read(t, p, "development/specs/greeting/spec.md")
	head := HeadSHA(ctx(), p.Root)

	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Contains(t, ce.Conflicts[0], `ADDED [greeting] "Wave" already exists`)
	require.Equal(t, specBefore, read(t, p, "development/specs/greeting/spec.md"))
	require.DirExists(t, p.ChangeDir("add-wave"))
	require.Equal(t, StatusReviewed, state(t, p, "add-wave").Status)
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
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
}
