package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/registry"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// PLAN-25 retires ten framework Python scripts and gates each retirement on
// "a golden byte-compare against the Python". This file is that gate.
//
// It runs the REAL scripts out of a framework checkout, side by side with the
// commands that replace them, and compares what the calling skills actually
// consume: exit codes where a skill branches on one, bytes where a skill
// re-reads the output, and the specific JSON fields where a skill reads a
// field. Hand-transcribed expectations cannot do this job — they encode what
// the porter BELIEVED the script did, and every divergence found while
// writing this file was one nobody had transcribed wrongly on purpose.
//
// Opt-in, because it needs a sibling checkout and a python3 with PyYAML:
//
//	APEX_FRAMEWORK_REPO=/path/to/apex_process_framework go test ./internal/apecmd/ -run TestParity
//
// It is not in the default `make test` gate for that reason. Run it before
// any framework bump that repoints a call site, and whenever you touch one of
// the replacing commands.

// pythonParity resolves the framework checkout and a usable python3, or
// skips.
func pythonParity(t *testing.T) (repo, python string) {
	t.Helper()
	repo = os.Getenv("APEX_FRAMEWORK_REPO")
	if repo == "" {
		t.Skip("set APEX_FRAMEWORK_REPO to a framework checkout to run the Python parity gate")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	probe := exec.CommandContext(context.Background(), python, "-c", "import yaml")
	if err := probe.Run(); err != nil {
		t.Skip("python3 has no PyYAML — the scripts under comparison refuse to run without it")
	}
	return repo, python
}

// runPython runs a framework script and returns its exit code.
func runPython(t *testing.T, python, script string, args ...string) int {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), python, append([]string{script}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case asExitError(err, &exitErr):
		return exitErr.ExitCode()
	default:
		t.Fatalf("running %s: %v (%s)", script, err, out.String())
		return -1
	}
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError) //nolint:errorlint // exec.ExitError is returned directly, never wrapped
	if ok {
		*target = e
	}
	return ok
}

// runAPE executes one cobra command in-process and returns its exit code.
func runAPE(t *testing.T, cmd *cobra.Command, args ...string) int {
	t.Helper()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	code, _ := ExitCode(cmd.Execute())
	return code
}

// --- verify-story-frontmatter.py → ape story verify --file ---------------

// TestParity_StoryFrontmatterExitCodes is the drop-in gate for the per-file
// story check. Its three exit codes are branched on by apex-create-story,
// apex-story-amend and apex-lift-project.
//
// The one asymmetry is deliberate and asserted as such: ape accepts a
// UTF-8 BOM that the Python rejects. The invariant that matters is
// one-directional — nothing the Python ACCEPTS may be rejected by ape,
// because that is the direction that turns a working project into a halted
// one.
func TestParity_StoryFrontmatterExitCodes(t *testing.T) {
	repo, python := pythonParity(t)
	script := filepath.Join(repo, ".claude", "skills", "apex-create-story",
		"scripts", "verify-story-frontmatter.py")
	requireFile(t, script)

	const valid = "---\nstory_id: \"1.2\"\nepic: \"1\"\nstatus: ready-for-dev\n" +
		"output_document: \"dev/1-2.md\"\n---\n\n# Story\n"

	cases := []struct {
		name       string
		body       string
		exts       string
		bomExempt  bool // ape is deliberately more permissive here
		wantAtMost int  // only set for the exempt case
	}{
		{name: "happy path", body: valid},
		{name: "all extensions satisfied", exts: "ext-adrs,ext-patterns,ext-features,ext-capabilities", body: "---\nstory_id: \"1.2\"\nepic: \"1\"\nstatus: ready-for-dev\noutput_document: \"dev/1-2.md\"\ngovernance:\n  adrs: [ADR-0001]\n  patterns: []\nfeatures: []\ncapabilities: []\n---\n\n# Story\n"},
		{name: "malformed yaml", body: "---\nstory_id: \"1.2\"\n  bad: indentation: error\n---\n\n# S\n"},
		{name: "no opening delimiter", body: "# Story without frontmatter\n"},
		{name: "missing output_document", body: "---\nstory_id: \"1.2\"\nepic: \"1\"\nstatus: ready-for-dev\n---\n\n# S\n"},
		{name: "empty required value", body: "---\nstory_id: \"\"\nepic: \"1\"\nstatus: ready-for-dev\noutput_document: \"d\"\n---\n"},
		{name: "zero is a value, not empty", body: "---\nstory_id: 0\nepic: 0\nstatus: ready-for-dev\noutput_document: \"d\"\n---\n"},
		{name: "empty frontmatter block", body: "---\n---\n\n# S\n"},
		{name: "scalar frontmatter", body: "---\njust a string\n---\n\n# S\n"},
		{name: "sha malformed", body: "---\nstory_id: \"1.2\"\nepic: \"1\"\nstatus: in-progress\noutput_document: \"d\"\ndev_started_at_sha: \"not-a-sha!\"\n---\n"},
		{name: "sha valid", body: "---\nstory_id: \"1.2\"\nepic: \"1\"\nstatus: in-progress\noutput_document: \"d\"\ndev_started_at_sha: a1b2c3d\n---\n"},
		{name: "ext-adrs required and absent", exts: "ext-adrs", body: valid},
		{name: "ext-features required and absent", exts: "ext-features", body: valid},
		{name: "ext-capabilities required and absent", exts: "ext-capabilities", body: valid},
		{name: "unknown extension is inert", exts: "ext-nonsense", body: valid},
		{name: "governance is not a mapping", exts: "ext-adrs", body: "---\nstory_id: \"1.2\"\nepic: \"1\"\nstatus: ready-for-dev\noutput_document: \"d\"\ngovernance: \"oops\"\n---\n"},
		{name: "BOM before the delimiter", body: "\xEF\xBB\xBF" + valid, bomExempt: true, wantAtMost: 0},
	}

	dir := t.TempDir()
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "story-"+itoa(i)+".md")
			require.NoError(t, os.WriteFile(path, []byte(tc.body), 0o644))

			pyArgs := []string{"--story-file", path}
			apeArgs := []string{"--file", path}
			if tc.exts != "" {
				pyArgs = append(pyArgs, "--active-extensions", tc.exts)
				apeArgs = append(apeArgs, "--active-extensions", tc.exts)
			}
			pyCode := runPython(t, python, script, pyArgs...)
			apeCode := runAPE(t, newStoryVerifyCmd(), apeArgs...)

			if tc.bomExempt {
				require.Equal(t, tc.wantAtMost, apeCode,
					"ape reads a BOM'd story the Python could not (python said %d)", pyCode)
				return
			}
			require.Equal(t, pyCode, apeCode, "exit code differs from the script this replaces")
		})
	}
}

// --- verify-sprint-status-row.py → ape sprint verify ----------------------

// TestParity_SprintRowExitCodes covers the five codes verbatim. Exit 5 is
// the one apex-review-story and apex-code-review branch on by number, and
// its repair instruction is "re-write updated_at as the value this reports",
// so a spurious 5 does not merely mis-report — it dictates a write.
func TestParity_SprintRowExitCodes(t *testing.T) {
	repo, python := pythonParity(t)
	script := filepath.Join(repo, ".claude", "skills", "apex-review-story",
		"scripts", "verify-sprint-status-row.py")
	requireFile(t, script)

	const base = "created_at: \"20260101000000\"\nupdated_at: \"20260301120000\"\n" +
		"development_status:\n  1-1_thing: done\n"

	cases := []struct {
		name     string
		body     string
		key      string
		expected string
	}{
		{"row matches", base, "1-1_thing", "done"},
		{"row differs", base, "1-1_thing", "review"},
		{"key missing", base, "9-9_ghost", "review"},
		{"yaml malformed", "development_status:\n  bad: [unclosed\n", "x", "y"},
		{"empty file", "", "1-1_thing", "done"},
		{"comments only", "# nothing here\n", "1-1_thing", "done"},
		{"development_status is a string", "development_status: \"oops\"\n", "1-1_thing", "done"},
		{"development_status is a list", "development_status:\n  - 1-1_thing\n", "1-1_thing", "done"},
		{"top level is a list", "- a\n- b\n", "1-1_thing", "done"},
		{"no development_status at all", "created_at: \"20260101000000\"\n", "1-1_thing", "done"},
		{"null row value", "development_status:\n  1-1_thing:\n", "1-1_thing", "done"},
		{"timestamps inverted", "created_at: \"20260301000000\"\nupdated_at: \"20260101000000\"\n" +
			"development_status:\n  1-1_thing: done\n", "1-1_thing", "done"},
		{"unquoted timestamps", "created_at: 20260101000000\nupdated_at: 20260301120000\n" +
			"development_status:\n  1-1_thing: done\n", "1-1_thing", "done"},
		{"malformed updated_at, no created_at", "updated_at: \"2026-04-19\"\n" +
			"development_status:\n  1-1_thing: done\n", "1-1_thing", "done"},
	}

	dir := t.TempDir()
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "tracker-"+itoa(i)+".yaml")
			require.NoError(t, os.WriteFile(path, []byte(tc.body), 0o644))

			pyCode := runPython(t, python, script,
				"--file", path, "--key", tc.key, "--expected", tc.expected)
			apeCode := runAPE(t, newSprintVerifyCmd(),
				"--file", path, "--key", tc.key, "--expected", tc.expected)
			require.Equal(t, pyCode, apeCode, "exit code differs from the script this replaces")
		})
	}
}

// TestParity_SprintRowBackwardsWrite is exit 5 against a real commit. It
// needs its own repository per case, because the comparison reads HEAD.
func TestParity_SprintRowBackwardsWrite(t *testing.T) {
	repo, python := pythonParity(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	script := filepath.Join(repo, ".claude", "skills", "apex-review-story",
		"scripts", "verify-sprint-status-row.py")
	requireFile(t, script)

	cases := []struct {
		name              string
		committed, actual string
		// apeStricter marks a case where ape catches a backwards write the
		// Python misses. The Python's guard demands a str, so an UNQUOTED
		// 14-digit committed value decodes as an int and the comparison is
		// skipped — a real regression waved through. ape compares the
		// rendered value and catches it.
		apeStricter bool
	}{
		{name: "backwards", committed: "\"20260301120000\"", actual: "\"20260201120000\""},
		{name: "forwards", committed: "\"20260301120000\"", actual: "\"20260401120000\""},
		{name: "equal", committed: "\"20260301120000\"", actual: "\"20260301120000\""},
		{name: "malformed working value", committed: "\"20260301120000\"", actual: "\"2026-04-19\""},
		{name: "malformed committed value", committed: "\"9999-01-01\"", actual: "\"20260419224402\""},
		{name: "unquoted committed value", committed: "20260301120000", actual: "\"20260201120000\"", apeStricter: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitInit(t, dir)
			path := filepath.Join(dir, "sprint-status.yaml")
			write := func(stamp string) {
				require.NoError(t, os.WriteFile(path,
					[]byte("updated_at: "+stamp+"\ndevelopment_status:\n  1-1_thing: done\n"), 0o644))
			}
			write(tc.committed)
			gitCommitAll(t, dir, "tracker")
			write(tc.actual)

			pyCode := runPython(t, python, script,
				"--file", path, "--key", "1-1_thing", "--expected", "done")
			apeCode := runAPE(t, newSprintVerifyCmd(),
				"--file", path, "--key", "1-1_thing", "--expected", "done")
			if tc.apeStricter {
				require.Equal(t, 0, pyCode, "the Python misses this one")
				require.Equal(t, 5, apeCode, "ape catches the backwards write the Python's type guard drops")
				return
			}
			require.Equal(t, pyCode, apeCode)
		})
	}
}

// --- shard-doc.py → ape doc verify|shard|assemble -------------------------

// TestParity_ShardDocIsByteIdentical is the strongest of these gates and the
// cheapest: the shard files, index.md included, must come out byte for byte
// the same, or apex-shard-doc's own Step 4 verification fails against a
// replacement whose Go tests are green.
func TestParity_ShardDocIsByteIdentical(t *testing.T) {
	repo, python := pythonParity(t)
	script := filepath.Join(repo, ".claude", "skills", "apex-shard-doc", "resources", "shard-doc.py")
	requireFile(t, script)

	const doc = `# Architecture

Intro with a [link](../other.md).

## Overview

Prose, a [ref][r1] and an image ![x](assets/pic.png).

[r1]: ./notes.md "Notes"

<img src="diagrams/a.svg">

### Nested

deep

## Data Model & Storage

A [fragment](../top.md#goals) and an [absolute](https://example.com) link.

## Appendix

Tail.
`
	for _, level := range []string{"2", "3"} {
		for _, numbered := range []bool{false, true} {
			name := "level" + level
			if numbered {
				name += "-numbered"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				src := filepath.Join(dir, "src.md")
				require.NoError(t, os.WriteFile(src, []byte(doc), 0o644))
				pyOut, apeOut := filepath.Join(dir, "py-shards"), filepath.Join(dir, "ape-shards")

				pyArgs := []string{"explode", src, pyOut, "--level", level}
				apeArgs := []string{src, apeOut, "--level", level}
				if numbered {
					pyArgs = append(pyArgs, "--numbered")
					apeArgs = append(apeArgs, "--numbered")
				}
				require.Equal(t, 0, runPython(t, python, script, pyArgs...))
				require.Equal(t, 0, runAPE(t, newDocShardCmd(), apeArgs...))
				requireTreesIdentical(t, pyOut, apeOut)
				require.FileExists(t, filepath.Join(apeOut, "index.md"),
					"apex-shard-doc/SKILL.md verifies index.md exists and treats its absence as failure")

				// And the reverse direction produces the same document.
				pyBack, apeBack := filepath.Join(dir, "py-back.md"), filepath.Join(dir, "ape-back.md")
				require.Equal(t, 0, runPython(t, python, script, "assemble", pyOut, pyBack))
				require.Equal(t, 0, runAPE(t, newDocAssembleCmd(), apeOut, apeBack))
				requireFilesIdentical(t, pyBack, apeBack)
			})
		}
	}
}

// TestParity_ShardDocVerifyExitCodes: `verify` is a GATE. Its caller relies
// on the non-zero exit to stop before writing anything.
func TestParity_ShardDocVerifyExitCodes(t *testing.T) {
	repo, python := pythonParity(t)
	script := filepath.Join(repo, ".claude", "skills", "apex-shard-doc", "resources", "shard-doc.py")
	requireFile(t, script)

	cases := []struct{ name, doc string }{
		{"clean", "# D\n\n## Alpha\na\n\n## Beta\nb\n"},
		{"duplicate slugs", "# D\n\n## Alpha\na\n\n## Alpha\nb\n"},
		{"slug collision through punctuation", "# D\n\n## Alpha!\na\n\n## Alpha?\nb\n"},
		{"no headings at level", "# D\n\nbody only\n"},
	}
	dir := t.TempDir()
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := filepath.Join(dir, "doc-"+itoa(i)+".md")
			require.NoError(t, os.WriteFile(src, []byte(tc.doc), 0o644))
			pyCode := runPython(t, python, script, "check", src, "--level", "2")
			apeCode := runAPE(t, newDocVerifyCmd(), src, "--level", "2")
			require.Equal(t, pyCode, apeCode)
		})
	}
}

// --- render-index-update.py → ape <family> update -------------------------

// TestParity_IndexUpdateIsSemanticNotByteIdentical states the one place the
// replacement is deliberately NOT byte-compatible, and pins what it must be
// instead.
//
// The Python rewrites the whole file through yaml.dump, which DESTROYS the
// comments — including the schema notes a real ADR index carries saying
// "keep file: filename-only, apex-adr-reconciliation copies these entries
// downstream". ape edits the YAML node tree and keeps them. So a byte
// compare is the wrong assertion: what has to match is the DATA after the
// write, and the fail-fast behaviour on an unknown id.
func TestParity_IndexUpdateIsSemanticNotByteIdentical(t *testing.T) {
	repo, python := pythonParity(t)
	script := filepath.Join(repo, ".claude", "skills", "apex-pattern-update",
		"resources", "render-index-update.py")
	requireFile(t, script)

	const index = `# A comment the Python destroys and ape keeps.
generated_at: "20260101000000"
project: demo
adrs:
  - id: ADR-0001
    slug: a
    file: adr-0001_a.md
    status: accepted
  - id: ADR-0002
    slug: b
    file: adr-0002_b.md
    status: proposed
`
	updates := `{"ADR-0002": {"status": "accepted", "version": "v3"}}`

	// Python side: a bare index file.
	pyDir := t.TempDir()
	pyIndex := filepath.Join(pyDir, "index.yaml")
	updatesFile := filepath.Join(pyDir, "updates.json")
	require.NoError(t, os.WriteFile(pyIndex, []byte(index), 0o644))
	require.NoError(t, os.WriteFile(updatesFile, []byte(updates), 0o644))
	require.Equal(t, 0, runPython(t, python, script,
		"--index", pyIndex, "--list-key", "adrs", "--updates", updatesFile,
		"--generated-at", "20260822120000"))

	// ape side: the same index inside a project.
	root := newTestProject(t, allExtensionsConfig)
	apeIndex := filepath.Join(root, "development", "governance", "adrs", "index.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(apeIndex), 0o755))
	require.NoError(t, os.WriteFile(apeIndex, []byte(index), 0o644))
	require.Equal(t, 0, runAPE(t, newFamilyUpdateCmd(mustFamily(t, "adrs")),
		"--updates", updatesFile, "--generated-at", "20260822120000"))

	require.Equal(t, decodeYAML(t, pyIndex), decodeYAML(t, apeIndex),
		"the data after the write must be identical even though the bytes are not")

	apeBytes, err := os.ReadFile(apeIndex)
	require.NoError(t, err)
	pyBytes, err := os.ReadFile(pyIndex)
	require.NoError(t, err)
	require.Contains(t, string(apeBytes), "# A comment the Python destroys",
		"ape must preserve the comments — that is the improvement, and it is why bytes differ")
	require.NotContains(t, string(pyBytes), "# A comment the Python destroys",
		"if the Python ever starts preserving comments, switch this gate to a byte compare")

	// Fail-fast on an unknown id, without touching the file: the contract
	// the calling prose's "on non-zero exit, HALT" depends on.
	before := apeBytes
	unknown := filepath.Join(pyDir, "unknown.json")
	require.NoError(t, os.WriteFile(unknown, []byte(`{"ADR-9999": {"status": "accepted"}}`), 0o644))
	require.NotEqual(t, 0, runAPE(t, newFamilyUpdateCmd(mustFamily(t, "adrs")),
		"--updates", unknown, "--generated-at", "20260822130000"))
	after, err := os.ReadFile(apeIndex)
	require.NoError(t, err)
	require.Equal(t, before, after, "a rejected update must not have written a byte")
}

// --- reconcile-epic-status.py → ape sprint reconcile ----------------------

// TestParity_EpicProjection compares the projection itself across the truth
// table, on trackers written the way a real one is.
func TestParity_EpicProjection(t *testing.T) {
	repo, python := pythonParity(t)
	script := filepath.Join(repo, ".claude", "skills", "apex-sprint-sync",
		"scripts", "reconcile-epic-status.py")
	requireFile(t, script)

	header := "created_at: \"20260101000000\"\nupdated_at: \"20260101000000\"\n" +
		"development_status:\n  epic-1: backlog\n"
	rows := func(statuses ...string) string {
		var b strings.Builder
		b.WriteString(header)
		for i, s := range statuses {
			b.WriteString("  1-" + itoa(i+1) + "_story: " + s + "\n")
		}
		return b.String()
	}

	cases := []struct {
		name string
		body string
	}{
		{"all done", rows("done", "done")},
		{"all backlog", rows("backlog", "backlog")},
		{"mixed", rows("done", "backlog")},
		{"blocked holds it open", rows("done", "blocked")},
		{"review holds it open", rows("done", "review")},
		{"drafted holds it open", rows("done", "drafted")},
		{"cancelled excluded", rows("done", "cancelled")},
		{"all cancelled leaves it alone", rows("cancelled", "cancelled")},
		{"no story rows leaves it alone", header},
		{"unrecognised value holds it open", rows("done", "wat")},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pyPath := filepath.Join(dir, "py.yaml")
			apePath := filepath.Join(dir, "ape.yaml")
			require.NoError(t, os.WriteFile(pyPath, []byte(tc.body), 0o644))
			require.NoError(t, os.WriteFile(apePath, []byte(tc.body), 0o644))

			require.Equal(t, 0, runPython(t, python, script, "--sprint-status", pyPath, "--epic", "1"))
			require.Equal(t, 0, runAPE(t, newSprintReconcileCmd(), "--file", apePath, "--epic", "1"))

			require.Equal(t, epicRow(t, pyPath), epicRow(t, apePath),
				"case %d: the projection differs from the script this replaces", i)

			// Both refresh the body updated_at on mutation and neither
			// touches it otherwise.
			pyStamp := topLevel(t, pyPath, "updated_at")
			apeStamp := topLevel(t, apePath, "updated_at")
			moved := epicRow(t, pyPath) != "backlog"
			if moved {
				require.NotEqual(t, "20260101000000", apeStamp,
					"a mutation must refresh updated_at, as the Python does (it wrote %q)", pyStamp)
			} else {
				require.Equal(t, "20260101000000", apeStamp, "a no-op must not stamp the file")
			}
		})
	}
}

// --- analyze_sources.py → ape doc analyze ---------------------------------

// TestParity_AnalyzeConsumedFields compares the fields apex-distillator
// actually reads. The rest of the payload deliberately differs (doc-type
// vocabulary, group keys, reason prose) and no caller reads it — asserting
// on those would be pinning noise.
func TestParity_AnalyzeConsumedFields(t *testing.T) {
	repo, python := pythonParity(t)
	script := filepath.Join(repo, ".claude", "skills", "apex-distillator",
		"scripts", "analyze_sources.py")
	requireFile(t, script)

	src := t.TempDir()
	for name, body := range map[string]string{
		"product-brief.md":                 strings.Repeat("brief content. ", 40),
		"product-brief-discovery-notes.md": strings.Repeat("notes. ", 40),
		"architecture.md":                  strings.Repeat("arch. ", 40),
	} {
		require.NoError(t, os.WriteFile(filepath.Join(src, name), []byte(body), 0o644))
	}

	pyOut := pythonJSON(t, python, script, src)
	apeOut := apeJSON(t, newDocAnalyzeCmd(), src, "--output-format", "json")

	require.Equal(t, pyOut["status"], apeOut["status"])
	require.Equal(t,
		nested(t, pyOut, "routing", "recommendation"),
		nested(t, apeOut, "routing", "recommendation"),
		"routing.recommendation picks single vs fan-out — the skill branches on it")
	require.Equal(t,
		nested(t, pyOut, "split_prediction", "prediction"),
		nested(t, apeOut, "split_prediction", "prediction"),
		"split_prediction.prediction is passed verbatim to every compressor agent")
	require.Equal(t,
		nested(t, pyOut, "summary", "total_files"),
		nested(t, apeOut, "summary", "total_files"))

	// Every source file must reach exactly one group, or a fan-out silently
	// drops a document.
	require.ElementsMatch(t, groupedPaths(t, pyOut), groupedPaths(t, apeOut),
		"the set of files routed to compressor agents must match")
}

// nested reads payload[section][key], failing the test if the shape is wrong
// — a missing section is a contract break, not a nil comparison that passes.
func nested(t *testing.T, payload map[string]any, section, key string) any {
	t.Helper()
	sub, ok := payload[section].(map[string]any)
	require.True(t, ok, "payload has no %q object", section)
	v, ok := sub[key]
	require.True(t, ok, "%s has no %q field", section, key)
	return v
}

func groupedPaths(t *testing.T, payload map[string]any) []string {
	t.Helper()
	var out []string
	groups, _ := payload["groups"].([]any)
	for _, g := range groups {
		files, _ := g.(map[string]any)["files"].([]any)
		for _, f := range files {
			if p, ok := f.(map[string]any)["path"].(string); ok {
				out = append(out, filepath.Base(p))
			}
		}
	}
	return out
}

// --- helpers -------------------------------------------------------------

func mustFamily(t *testing.T, name string) registry.Family {
	t.Helper()
	family, err := registry.FamilyByName(name)
	require.NoError(t, err)
	return family
}

func requireFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not in this framework checkout (already retired?): %v", path, err)
	}
}

func requireFilesIdentical(t *testing.T, a, b string) {
	t.Helper()
	ab, err := os.ReadFile(a)
	require.NoError(t, err)
	bb, err := os.ReadFile(b)
	require.NoError(t, err)
	require.Equal(t, string(ab), string(bb), "%s and %s differ", a, b)
}

func requireTreesIdentical(t *testing.T, a, b string) {
	t.Helper()
	names := func(dir string) []string {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}
	require.ElementsMatch(t, names(a), names(b), "the two shard directories hold different files")
	for _, name := range names(a) {
		requireFilesIdentical(t, filepath.Join(a, name), filepath.Join(b, name))
	}
}

func decodeYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, yaml.Unmarshal(data, &out))
	return out
}

func epicRow(t *testing.T, path string) string {
	t.Helper()
	doc := decodeYAML(t, path)
	rows, _ := doc["development_status"].(map[string]any)
	v, _ := rows["epic-1"].(string)
	return v
}

func topLevel(t *testing.T, path, key string) string {
	t.Helper()
	v, ok := decodeYAML(t, path)[key]
	if !ok || v == nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(fmt.Sprintf("%v", v)), `"'`)
}

func pythonJSON(t *testing.T, python, script string, args ...string) map[string]any {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), python, append([]string{script}, args...)...)
	out, err := cmd.Output()
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(out, &payload))
	return payload
}

func apeJSON(t *testing.T, cmd *cobra.Command, args ...string) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	require.NoError(t, cmd.Execute())
	var payload map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &payload))
	return payload
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
