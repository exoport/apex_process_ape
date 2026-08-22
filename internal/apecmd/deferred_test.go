package apecmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/deferred"
	"github.com/stretchr/testify/require"
)

// deferBullet is the shape apex-review-story emits.
const deferBullet = "- [ ] [Defer] Tighten `resolveModelArg` when `--model` is empty " +
	"[internal/apecmd/modelarg.go:42] — defer: outside-story=yes ; cross-cycle=no ; " +
	"non-blocking=yes ; owner=platform ; trigger: story 54-2 lands\n"

// ingestVia pipes bullets through the command, returning stdout/stderr.
func ingestVia(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newDeferredIngestCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errBuf.String(), err
}

func TestDeferredIngest_StdinRoundTrip(t *testing.T) {
	root := newTestProject(t, realProjectConfig)

	out, _, err := ingestVia(t, deferBullet, "--story", "54-1", "--skill", "apex-review-story")
	require.NoError(t, err)
	require.Contains(t, out, "stored 1 record(s)")

	store := deferred.New(filepath.Join(root, "development", "deferred"))
	res, err := store.Load(deferred.LoadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Records, 1)
	require.Equal(t, deferBullet, res.Records[0].Body, "byte-intact through the command")
	require.Equal(t, "platform", res.Records[0].Owner)
	require.Equal(t, "54-1", res.Records[0].SourceStory)
}

// TestDeferredIngest_BodyFileIsTheFrameworkCallForm covers the path the
// skills use, which avoids shell redirection entirely.
func TestDeferredIngest_BodyFileIsTheFrameworkCallForm(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	scratch := filepath.Join(t.TempDir(), "defer-54-1.txt")
	require.NoError(t, os.WriteFile(scratch, []byte(deferBullet), 0o644))

	out, _, err := ingestVia(t, "", "--story", "54-1", "--body-file", scratch)
	require.NoError(t, err)
	require.Contains(t, out, "stored 1 record(s)")

	store := deferred.New(filepath.Join(root, "development", "deferred"))
	res, err := store.Load(deferred.LoadOptions{})
	require.NoError(t, err)
	require.Equal(t, deferBullet, res.Records[0].Body)
}

// TestDeferredIngest_NeverFailsForContent is the C1 lock at the command
// boundary: neither empty input nor an unparseable bullet may produce a
// non-zero exit, because that converts a defer into a patch and demotes
// the story two skills away.
func TestDeferredIngest_NeverFailsForContent(t *testing.T) {
	newTestProject(t, realProjectConfig)

	out, _, err := ingestVia(t, "", "--story", "54-1")
	require.NoError(t, err, "empty stdin is a no-op, not a failure")
	require.Contains(t, out, "nothing stored")

	out, errText, err := ingestVia(t, "just some prose nobody formatted\n", "--story", "54-1")
	require.NoError(t, err, "an unparseable bullet must never fail this command")
	require.Contains(t, out, "stored 1 record(s)")
	require.Contains(t, errText, "free-form")
}

func TestDeferredIngest_JSONPayload(t *testing.T) {
	newTestProject(t, realProjectConfig)
	out, _, err := ingestVia(t, deferBullet, "--story", "54-1", "--output-format", "json")
	require.NoError(t, err)

	var res deferred.IngestResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Equal(t, 1, res.Count)
	require.Len(t, res.Stored, 1)
}

func TestDeferredList_FiltersAndDetail(t *testing.T) {
	newTestProject(t, realProjectConfig)
	_, _, err := ingestVia(t,
		"- [Defer] Alpha [pkg/a.go:1] — defer: owner=alice\n"+
			"- [Defer] Beta [pkg/b.go:2] — defer: owner=bob\n", "--story", "1-1")
	require.NoError(t, err)

	out := runCmd(t, newDeferredListCmd())
	require.Contains(t, out, "Alpha")
	require.Contains(t, out, "Beta")
	require.Contains(t, out, "2 record(s)")

	out = runCmd(t, newDeferredListCmd(), "--owner", "alice")
	require.Contains(t, out, "Alpha")
	require.NotContains(t, out, "Beta")

	out = runCmd(t, newDeferredListCmd(), "--detail", "full")
	require.Contains(t, out, "anchors:")
	require.Contains(t, out, "owner: alice")

	out = runCmd(t, newDeferredListCmd(), "--path", "pkg/b")
	require.Contains(t, out, "Beta")
	require.NotContains(t, out, "Alpha")
}

func TestDeferredList_EmptyStore(t *testing.T) {
	newTestProject(t, realProjectConfig)
	out := runCmd(t, newDeferredListCmd())
	require.Contains(t, out, "no matching deferred records")
}

func TestDeferredClose_MovesAndRequiresBy(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	_, _, err := ingestVia(t, deferBullet, "--story", "54-1")
	require.NoError(t, err)

	store := deferred.New(filepath.Join(root, "development", "deferred"))
	res, err := store.Load(deferred.LoadOptions{})
	require.NoError(t, err)
	id := res.Records[0].ID

	// --by is mandatory: a discharge with no recorded reason is not one.
	cmd := newDeferredCloseCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{id})
	require.Error(t, cmd.Execute())

	out := runCmd(t, newDeferredCloseCmd(), id, "--by", "54-2")
	require.Contains(t, out, "closed "+id)
	require.Contains(t, out, filepath.Join("deferred", "closed"))

	// It left the open set but not the disk.
	open := runCmd(t, newDeferredListCmd())
	require.Contains(t, open, "no matching deferred records")
	all := runCmd(t, newDeferredListCmd(), "--status", "all")
	require.Contains(t, all, id)
}

func TestDeferredVerify_CandidatesAreTagged(t *testing.T) {
	newTestProject(t, realProjectConfig)
	// An anchor pointing at a file that does not exist in this project.
	_, _, err := ingestVia(t, "- [Defer] Gone [pkg/removed.go:12]\n", "--story", "1-1")
	require.NoError(t, err)

	out := runCmd(t, newDeferredVerifyCmd(), "--output-format", "json")
	var report deferred.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, 1, report.Summary.Candidates)
	require.Equal(t, deferred.ConfidenceCandidate, report.Findings[0].Confidence)

	human := runCmd(t, newDeferredVerifyCmd())
	require.Contains(t, human, "candidates for a human")
}

func TestDeferredVerify_ExitZeroWithFindingsUnlessStrict(t *testing.T) {
	newTestProject(t, realProjectConfig)
	_, _, err := ingestVia(t, "- [Defer] Gone [pkg/removed.go:12]\n", "--story", "1-1")
	require.NoError(t, err)

	// Default: findings, exit 0.
	runCmd(t, newDeferredVerifyCmd())

	cmd := newDeferredVerifyCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--strict"})
	code, _ := ExitCode(cmd.Execute())
	require.Equal(t, 1, code)
}

// TestDeferredVerify_TriggerHeuristicUsesTheTracker wires the fired-trigger
// candidate to sprint-status.yaml.
func TestDeferredVerify_TriggerHeuristicUsesTheTracker(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "sprint-status.yaml", "development_status:\n  54-2: done\n")
	_, _, err := ingestVia(t,
		"- [Defer] Wait [internal/apecmd/root.go:1] — defer: trigger: story 54-2 lands\n", "--story", "54-1")
	require.NoError(t, err)

	out := runCmd(t, newDeferredVerifyCmd(), "--output-format", "json")
	var report deferred.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	found := false
	for _, f := range report.Findings {
		if f.Check == deferred.CheckTriggerFired {
			found = true
			require.Equal(t, deferred.ConfidenceCandidate, f.Confidence)
		}
	}
	require.True(t, found, "a trigger naming a done story is a candidate: %+v", report.Findings)
}

func TestDeferredVerify_LintAlias(t *testing.T) {
	cmd := newDeferredVerifyCmd()
	require.Contains(t, cmd.Aliases, "lint", "muscle memory from the upstream spelling still resolves")
}

func TestDeferredCmd_Surface(t *testing.T) {
	cmd := newDeferredCmd()
	names := map[string]bool{}
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	for _, verb := range []string{"ingest", "list", "close", "verify"} {
		require.True(t, names[verb], "ape deferred %s missing", verb)
	}
}

func TestIncludeClosed(t *testing.T) {
	require.False(t, includeClosed("open"))
	require.False(t, includeClosed(""))
	require.True(t, includeClosed("closed"))
	require.True(t, includeClosed("all"))
}

func TestReadDeferredInput(t *testing.T) {
	data, err := readDeferredInput(strings.NewReader("x\n"), "")
	require.NoError(t, err)
	require.Equal(t, "x\n", string(data))

	path := filepath.Join(t.TempDir(), "b.txt")
	require.NoError(t, os.WriteFile(path, []byte("y\n"), 0o644))
	data, err = readDeferredInput(strings.NewReader(""), path)
	require.NoError(t, err)
	require.Equal(t, "y\n", string(data))

	_, err = readDeferredInput(strings.NewReader(""), filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

// TestDeferredStore_LivesOutsideImplementation is the siting guarantee:
// ten skills glob {implementation_folder}/**/*.md across 17 sites, and
// record files under that folder would feed every one of them.
func TestDeferredStore_LivesOutsideImplementation(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	_, _, err := ingestVia(t, deferBullet, "--story", "54-1")
	require.NoError(t, err)

	implementation := filepath.Join(root, "development", "implementation")
	entries, err := os.ReadDir(implementation)
	if err == nil {
		for _, e := range entries {
			require.NotContains(t, e.Name(), "DW-", "no record file may land under implementation_folder")
		}
	}
	_, err = os.Stat(filepath.Join(root, "development", "deferred"))
	require.NoError(t, err, "the store is a sibling of implementation/, not a child")
}
