package governance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/stretchr/testify/require"
)

// storyProject writes a project whose stories and tracker are the two
// halves this reads. Each story is (key, frontmatter status, File List
// body); a tracker status of "" leaves the row out entirely.
func storyProject(t *testing.T, stories []story) *apexcfg.Resolved {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"), []byte(
		"config_schema_version: \"1\"\nproject_name: fx\nextensions: []\n"+
			"development_folder: development\nimplementation_folder: development/implementation\n"), 0o644))

	impl := filepath.Join(root, "development", "implementation")
	require.NoError(t, os.MkdirAll(impl, 0o755))

	rows := &strings.Builder{}
	rows.WriteString("development_status:\n")
	for _, s := range stories {
		body := "---\nstory_id: \"x\"\nstatus: " + s.frontmatter + "\n---\n\n" +
			"## Tasks\n\n- [ ] edit `internal/mentioned-in-tasks.go`\n\n" +
			"### File List\n\n" + s.fileList + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(impl, s.key+".md"), []byte(body), 0o644))
		if s.tracker != "" {
			rows.WriteString("  " + s.key + ": " + s.tracker + "\n")
		}
	}
	// The tracker lives beside the stories, under the implementation
	// folder — which is where apexcfg resolves it.
	require.NoError(t, os.WriteFile(filepath.Join(impl, "sprint-status.yaml"), []byte(rows.String()), 0o644))

	cfg, err := apexcfg.ResolveAt(root, nil)
	require.NoError(t, err)
	return cfg
}

type story struct {
	key         string
	frontmatter string
	tracker     string
	fileList    string
}

func entryFor(path string) string { return "- `" + path + "` (modified)" }

// The table, row by row, over one path each.
func TestMatch_TheOwnershipTable(t *testing.T) {
	const path = "internal/repl/pty.go"
	for _, tc := range []struct {
		name        string
		frontmatter string
		tracker     string
		wantEffect  string
		wantVeto    bool
	}{
		{"in-progress", "in-progress", "in-progress", EffectVeto, true},
		{"review", "review", "review", EffectVeto, true},
		{"blocked", "blocked", "blocked", EffectCarries, false},
		{"backlog", "backlog", "backlog", EffectCarries, false},
		{"ready-for-dev", "ready-for-dev", "drafted", EffectCarries, false},
		{"done", "done", "done", EffectCarries, false},
		{"cancelled", "cancelled", "cancelled", EffectCarries, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := storyProject(t, []story{{
				key: "12-1_a-story", frontmatter: tc.frontmatter, tracker: tc.tracker,
				fileList: entryFor(path),
			}})
			rep, err := Match(cfg, []string{path})
			require.NoError(t, err)
			require.Len(t, rep.Paths[0].Owners, 1)
			require.Equal(t, tc.wantEffect, rep.Paths[0].Owners[0].Effect, rep.Paths[0].Owners[0].Reason)
			require.Equal(t, tc.wantVeto, rep.Veto)
			if tc.wantEffect == EffectCarries {
				require.Equal(t, []string{"12-1_a-story " + path}, rep.Carries)
			}
		})
	}
}

// `ready-for-dev` in a file and `drafted` in the tracker are the same
// status. Comparing them raw would report every drafted story as a
// disagreement — a veto on every project with a drafted story.
func TestMatch_DraftedAndReadyForDevAreNotADisagreement(t *testing.T) {
	const path = "internal/x.go"
	cfg := storyProject(t, []story{{
		key: "12-1_a-story", frontmatter: "ready-for-dev", tracker: "drafted", fileList: entryFor(path),
	}})
	rep, err := Match(cfg, []string{path})
	require.NoError(t, err)
	require.False(t, rep.Veto)
	require.Equal(t, EffectCarries, rep.Paths[0].Owners[0].Effect)
}

// Fail-closed, and the rule a re-implementation drops: a story
// mid-transition is exactly the one a patch must not quietly overwrite.
func TestMatch_DisagreeingStatusesAreTreatedAsOwned(t *testing.T) {
	const path = "internal/x.go"
	cfg := storyProject(t, []story{{
		key: "12-1_a-story", frontmatter: "done", tracker: "in-progress", fileList: entryFor(path),
	}})
	rep, err := Match(cfg, []string{path})
	require.NoError(t, err)
	require.True(t, rep.Veto)
	require.Equal(t, EffectVeto, rep.Paths[0].Owners[0].Effect)
	require.Contains(t, rep.Paths[0].Owners[0].Reason, "disagree")
	require.Empty(t, rep.Carries, "a vetoed path earns no trailer")
}

// Where one side is missing the other is used; where both are, the
// status is unknown and the path is treated as owned.
func TestMatch_OneSidedAndUnknownStatuses(t *testing.T) {
	const path = "internal/x.go"

	cfg := storyProject(t, []story{{
		key: "12-1_a-story", frontmatter: "done", tracker: "", fileList: entryFor(path),
	}})
	rep, err := Match(cfg, []string{path})
	require.NoError(t, err)
	require.False(t, rep.Veto, "the one status present is the status")

	cfg = storyProject(t, []story{{
		key: "12-2_a-story", frontmatter: "", tracker: "", fileList: entryFor(path),
	}})
	rep, err = Match(cfg, []string{path})
	require.NoError(t, err)
	require.True(t, rep.Veto)
	require.Contains(t, rep.Paths[0].Owners[0].Reason, "ownership could not be established")
}

// A marker outside the vocabulary is not a status ape can read, so the
// table's fail-closed row covers it.
func TestMatch_AnUnknownStatusVetoes(t *testing.T) {
	const path = "internal/x.go"
	cfg := storyProject(t, []story{{
		key: "12-1_a-story", frontmatter: "marinating", tracker: "marinating", fileList: entryFor(path),
	}})
	rep, err := Match(cfg, []string{path})
	require.NoError(t, err)
	require.True(t, rep.Veto)
	require.Contains(t, rep.Paths[0].Owners[0].Reason, "not one the lane's table knows")
}

// The three rules a re-implementation drops, each on its own.
func TestMatch_WhatDoesAndDoesNotEstablishOwnership(t *testing.T) {
	t.Run("a whole path token, never a suffix", func(t *testing.T) {
		cfg := storyProject(t, []story{{
			key: "12-1_a-story", frontmatter: "in-progress", tracker: "in-progress",
			fileList: entryFor("internal/sqlstore.go"),
		}})
		rep, err := Match(cfg, []string{"internal/store.go"})
		require.NoError(t, err)
		require.Empty(t, rep.Paths[0].Owners, "store.go must never match sqlstore.go")
		require.False(t, rep.Veto)
	})

	t.Run("a planned or deferred entry claims nothing", func(t *testing.T) {
		for _, marker := range []string{"planned", "deferred"} {
			cfg := storyProject(t, []story{{
				key: "12-1_a-story", frontmatter: "in-progress", tracker: "in-progress",
				fileList: "- `internal/x.go` (" + marker + ")",
			}})
			rep, err := Match(cfg, []string{"internal/x.go"})
			require.NoError(t, err)
			require.Empty(t, rep.Paths[0].Owners, "%s records an intention, not a change", marker)
		}
	})

	t.Run("a Tasks mention is advisory", func(t *testing.T) {
		cfg := storyProject(t, []story{{
			key: "12-1_a-story", frontmatter: "in-progress", tracker: "in-progress",
			fileList: entryFor("internal/x.go"),
		}})
		rep, err := Match(cfg, []string{"internal/mentioned-in-tasks.go"})
		require.NoError(t, err)
		require.Empty(t, rep.Paths[0].Owners, "only the File List establishes ownership")
	})

	t.Run("an entry that forgot its backticks is still an entry", func(t *testing.T) {
		cfg := storyProject(t, []story{{
			key: "12-1_a-story", frontmatter: "in-progress", tracker: "in-progress",
			fileList: "- internal/x.go (modified)",
		}})
		rep, err := Match(cfg, []string{"internal/x.go"})
		require.NoError(t, err)
		require.Len(t, rep.Paths[0].Owners, 1)
	})
}

// Above the threshold a path says one thing instead of saying the same
// thing many times: a commit message is read by a person.
func TestMatch_ManyFinishedOwnersCollapseToOneSharedRow(t *testing.T) {
	const path = "go.mod"
	keys := []string{"1-1_a", "1-2_b", "1-3_c", "1-4_d", "1-5_e", "1-6_f"}
	stories := make([]story, 0, len(keys))
	for _, key := range keys {
		stories = append(stories, story{key: key, frontmatter: "done", tracker: "done", fileList: entryFor(path)})
	}
	cfg := storyProject(t, stories)

	rep, err := Match(cfg, []string{path})
	require.NoError(t, err)
	require.Len(t, rep.Paths[0].Owners, 6)
	require.Equal(t, []string{"shared go.mod (6 owners)"}, rep.Carries)

	// Five is not more than five.
	rep, err = Match(storyProject(t, stories[:5]), []string{path})
	require.NoError(t, err)
	require.Len(t, rep.Carries, 5)
}

// A path nobody claims earns nothing and vetoes nothing — the table's
// last row, and the ordinary case for a maintenance fix.
func TestMatch_NoOwnerIsNotAFinding(t *testing.T) {
	cfg := storyProject(t, []story{{
		key: "12-1_a-story", frontmatter: "done", tracker: "done", fileList: entryFor("internal/other.go"),
	}})
	rep, err := Match(cfg, []string{"internal/untouched.go"})
	require.NoError(t, err)
	require.False(t, rep.Veto)
	require.Empty(t, rep.Carries)
	require.Empty(t, rep.Paths[0].Owners)
	require.NotNil(t, rep.Paths[0].Owners, "an empty list, never null")
}

// One vetoed path vetoes the run, even beside paths that are clear.
func TestMatch_OneVetoIsTheVerdict(t *testing.T) {
	cfg := storyProject(t, []story{
		{
			key: "12-1_in-flight", frontmatter: "in-progress", tracker: "in-progress",
			fileList: entryFor("internal/owned.go"),
		},
		{
			key: "12-2_finished", frontmatter: "done", tracker: "done",
			fileList: entryFor("internal/free.go"),
		},
	})
	rep, err := Match(cfg, []string{"internal/free.go", "internal/owned.go"})
	require.NoError(t, err)
	require.True(t, rep.Veto)
	require.False(t, rep.Paths[0].Veto)
	require.True(t, rep.Paths[1].Veto)
}
