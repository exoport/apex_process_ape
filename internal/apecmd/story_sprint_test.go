package apecmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/sprint"
	"github.com/exoport/apex_process_ape/internal/story"
	"github.com/stretchr/testify/require"
)

// writeStory drops a file into the project's implementation folder.
func writeStory(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, "development", "implementation")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

const storyFrontmatter = `---
story_id: 1-1
epic: 1
status: done
output_document: development/implementation/1-1_thing.md
governance:
  adrs:
    - ADR-0001
  patterns:
    - PAT-0001
---

## Story

body
`

// --- story fields ---

func TestStoryFields_JSONTrailer(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "1-1_thing.md", storyFrontmatter)
	writeStory(t, root, "epic-1-retro.md", "# Retrospective\n\nno frontmatter\n")

	out := runCmd(t, newStoryFieldsCmd(), "--select", "story_id,epic,features", "--output-format", "json")
	var res story.FieldsResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))

	require.Equal(t, 2, res.Trailer.FilesScanned)
	require.Equal(t, 1, res.Trailer.StoriesMatched, "the retro is not a story")
	require.Equal(t, 1, res.Trailer.PerFieldPresent["story_id"])
	require.Equal(t, 0, res.Trailer.PerFieldPresent["features"],
		"a field no story has reports 0 rather than vanishing")
	require.Positive(t, res.Trailer.BytesRead)
}

func TestStoryFields_Human(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "1-1_thing.md", storyFrontmatter)

	out := runCmd(t, newStoryFieldsCmd(), "--select", "story_id,governance")
	require.Contains(t, out, "1-1_thing.md")
	require.Contains(t, out, "{2}", "the governance block has two keys, summarised by shape")
	require.Contains(t, out, "present in 1")
}

func TestStoryFields_WarningsGoToStderrAndRunSucceeds(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "1-1_ok.md", storyFrontmatter)
	writeStory(t, root, "1-2_broken.md", "---\nstory_id: [oops\n---\n\nx\n")

	cmd := newStoryFieldsCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"--select", "story_id"})
	require.NoError(t, cmd.Execute(), "one bad file must not fail the run")
	require.Contains(t, errBuf.String(), "1-2_broken.md")
	require.Contains(t, out.String(), "1-1_ok.md")
}

// --- story verify (corpus) ---

func TestStoryVerify_CorpusReportsAndExitsZero(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "1-1_thing.md", "---\nstory_id: 1-1\n---\n\nx\n")

	out := runCmd(t, newStoryVerifyCmd(), "--output-format", "json")
	var report story.Report
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.NotEmpty(t, report.Findings, "epic/status/output_document are missing")
	// runCmd asserts no error, i.e. exit 0 despite findings.
}

func TestStoryVerify_CorpusHumanClean(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	adrDir := filepath.Join(root, "development", "governance", "adrs")
	require.NoError(t, os.MkdirAll(adrDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(adrDir, "adr-0001_x.md"),
		[]byte("---\nid: ADR-0001\n---\n\nx\n"), 0o644))
	patDir := filepath.Join(root, "development", "governance", "patterns")
	require.NoError(t, os.MkdirAll(patDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(patDir, "pat-0001_x.md"),
		[]byte("---\nid: PAT-0001\n---\n\nx\n"), 0o644))
	writeStory(t, root, "1-1_thing.md", storyFrontmatter)

	out := runCmd(t, newStoryVerifyCmd())
	require.Contains(t, out, "no findings")
	require.Contains(t, out, "1 story checked")
}

// --- story verify --file (the gate replacing the Python) ---

func TestStoryVerifyFile_OK(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	path := writeStory(t, root, "1-1_thing.md", storyFrontmatter)

	out := runCmd(t, newStoryVerifyCmd(), "--file", path, "--active-extensions", "ext-adrs,ext-patterns")
	require.Contains(t, out, "OK:")
}

// TestStoryVerifyFile_ExitCodesTravelAsErrors is what the gate/exitError
// split buys: the command's verdict is assertable in-process. Calling
// os.Exit inside RunE would have killed the test binary here.
func TestStoryVerifyFile_ExitCodesTravelAsErrors(t *testing.T) {
	root := newTestProject(t, realProjectConfig)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"valid", storyFrontmatter, ExitOK},
		{"parse failure", "# no frontmatter\n", story.FileParseFailure},
		{"key problem", "---\nstory_id: 1-3\n---\n\nx\n", story.FileKeyProblem},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeStory(t, root, fmt.Sprintf("case-%d.md", i), tc.body)
			cmd := newStoryVerifyCmd()
			var out, errBuf bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errBuf)
			cmd.SetArgs([]string{"--file", path, "--active-extensions", "ext-adrs,ext-patterns"})
			err := cmd.Execute()

			code, _ := ExitCode(err)
			require.Equal(t, tc.want, code, "stderr: %s", errBuf.String())
			if tc.want == ExitOK {
				require.NoError(t, err)
				require.Contains(t, out.String(), "OK:")
			} else {
				require.Error(t, err)
				require.Contains(t, errBuf.String(), "FAIL:", "the findings reach stderr for the caller to quote")
			}
		})
	}
}

// TestSprintVerify_ExitCodesTravelAsErrors covers the five codes two
// review skills branch on, through the command surface.
func TestSprintVerify_ExitCodesTravelAsErrors(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	path := writeStory(t, root, "sprint-status.yaml", "development_status:\n  1-1: done\n")

	cases := []struct {
		name     string
		key      string
		expected string
		want     int
	}{
		{"ok", "1-1", "done", sprint.VerifyOK},
		{"key absent", "9-9", "done", sprint.VerifyMismatch},
		{"value differs", "1-1", "in-progress", sprint.VerifyMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newSprintVerifyCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"--file", path, "--key", tc.key, "--expected", tc.expected})
			code, _ := ExitCode(cmd.Execute())
			require.Equal(t, tc.want, code)
		})
	}
}

// TestGateErr keeps the constructor honest: success is a nil error, so a
// caller can return it unconditionally.
func TestGateErr(t *testing.T) {
	require.NoError(t, gateErr(ExitOK, nil))

	err := gateErr(3, nil)
	require.Error(t, err)
	code, silent := ExitCode(err)
	require.Equal(t, 3, code)
	require.True(t, silent, "the command already printed its own diagnostic")

	wrapped := gateErr(2, errors.New("boom"))
	require.ErrorContains(t, wrapped, "boom")
	code, _ = ExitCode(wrapped)
	require.Equal(t, 2, code)
}

// --- sprint check ---

func TestSprintCheck_AlwaysExitsZeroAndNamesBothSides(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "sprint-status.yaml", "development_status:\n  1-1: done\n")
	writeStory(t, root, "1-1_thing.md", "---\nstory_id: 1-1\nstatus: in-progress\n---\n\nx\n")

	out := runCmd(t, newSprintCheckCmd(), "--output-format", "json")
	var report sprint.CheckReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Len(t, report.Findings, 1)
	require.Equal(t, "done", report.Findings[0].Tracker)
	require.Equal(t, "in-progress", report.Findings[0].Story)

	human := runCmd(t, newSprintCheckCmd())
	require.Contains(t, human, "neither side is assumed correct")
}

func TestSprintCheck_NoStrictFlagExists(t *testing.T) {
	cmd := newSprintCheckCmd()
	require.Nil(t, cmd.Flags().Lookup("strict"),
		"sprint check must not offer --strict: it reports findings no tool can resolve")
}

func TestSprintCheck_Clean(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "sprint-status.yaml", "development_status:\n  1-1: drafted\n")
	writeStory(t, root, "1-1_thing.md", "---\nstory_id: 1-1\nstatus: ready-for-dev\n---\n\nx\n")

	out := runCmd(t, newSprintCheckCmd())
	require.Contains(t, out, "no divergence", "drafted normalises to ready-for-dev")
}

// --- sprint verify ---

func TestSprintVerify_OK(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	path := writeStory(t, root, "sprint-status.yaml", "development_status:\n  1-1: done\n")

	out := runCmd(t, newSprintVerifyCmd(), "--file", path, "--key", "1-1", "--expected", "done")
	require.Contains(t, out, "OK:")
}

func TestSprintVerify_JSONCarriesTheCode(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	path := writeStory(t, root, "sprint-status.yaml", "development_status:\n  1-1: done\n")

	cmd := newSprintVerifyCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", path, "--key", "1-1", "--expected", "done", "--output-format", "json"})
	require.NoError(t, cmd.Execute())

	var res sprint.VerifyResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	require.Equal(t, sprint.VerifyOK, res.Code)
}

// --- sprint reconcile ---

func TestSprintReconcile_TargetedWriteThroughTheCommand(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	tracker := `# generated header
created_at: '20260101000000'
updated_at: '20260101000000'
development_status:
  epic-1: in-progress   # keep this comment
  1-1: done
  1-2: done
`
	path := writeStory(t, root, "sprint-status.yaml", tracker)

	out := runCmd(t, newSprintReconcileCmd(), "--epic", "1")
	require.Contains(t, out, "reconciled epic-1: in-progress -> done")

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "epic-1: done   # keep this comment")
	require.Contains(t, text, "# generated header")
	require.Equal(t, strings.Count(tracker, "\n"), strings.Count(text, "\n"),
		"no lines added or removed")
}

func TestSprintReconcile_CheckAndAll(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	path := writeStory(t, root, "sprint-status.yaml", `development_status:
  epic-1: in-progress
  1-1: done
  epic-2: in-progress
  2-1: backlog
`)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	out := runCmd(t, newSprintReconcileCmd(), "--all", "--check")
	require.Contains(t, out, "would reconcile epic-1")
	require.Contains(t, out, "would reconcile epic-2")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "--check writes nothing")
}

func TestSprintReconcile_UnrecognisedStatusIsReportedNotFatal(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "sprint-status.yaml", "development_status:\n  epic-1: backlog\n  1-1: banana\n")

	out := runCmd(t, newSprintReconcileCmd(), "--epic", "1")
	require.Contains(t, out, "unrecognised status")
	require.Contains(t, out, "banana")
	require.Contains(t, out, "-> in-progress")
}

func TestSprintReconcile_NothingToDo(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "sprint-status.yaml", "development_status:\n  epic-1: done\n  1-1: done\n")

	out := runCmd(t, newSprintReconcileCmd(), "--epic", "1")
	require.Contains(t, out, "no epic row needed to move")
}

func TestStoryAndSprintCmd_Surface(t *testing.T) {
	storyCmd := newStoryCmd()
	require.Contains(t, storyCmd.Aliases, "stories")
	names := map[string]bool{}
	for _, sub := range storyCmd.Commands() {
		names[sub.Name()] = true
	}
	require.True(t, names["fields"])
	require.True(t, names["verify"])

	sprintCmd := newSprintCmd()
	names = map[string]bool{}
	for _, sub := range sprintCmd.Commands() {
		names[sub.Name()] = true
	}
	for _, verb := range []string{"check", "verify", "reconcile"} {
		require.True(t, names[verb], "ape sprint %s missing", verb)
	}
}

func TestFormatFieldValue(t *testing.T) {
	require.Equal(t, "-", formatFieldValue(nil))
	require.Equal(t, "[2]", formatFieldValue([]any{1, 2}))
	require.Equal(t, "{1}", formatFieldValue(map[string]any{"a": 1}))
	require.Equal(t, "done", formatFieldValue("done"))
}
