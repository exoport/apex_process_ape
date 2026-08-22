package apecmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexdoc"
	"github.com/stretchr/testify/require"
)

const docFixture = `# Product Requirements

See the [architecture](architecture.md).

## Goals

Ship it.

## Non-goals

Not that.
`

func writeDoc(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func TestDocVerify_CleanAndDuplicate(t *testing.T) {
	dir := t.TempDir()

	clean := writeDoc(t, dir, "clean.md", docFixture)
	out := runCmd(t, newDocVerifyCmd(), clean)
	require.Contains(t, out, "no duplicate H2 slugs")

	dupe := writeDoc(t, dir, "dupe.md", "# T\n\n## Goals\n\nx\n\n## GOALS\n\ny\n")
	cmd := newDocVerifyCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{dupe})
	err := cmd.Execute()
	require.Error(t, err, "verify is a gate — its caller relies on the non-zero exit")
	code, _ := ExitCode(err)
	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), `slug "goals"`)
	require.Contains(t, stderr.String(), "line 3")
	require.Contains(t, stderr.String(), "line 7")
}

func TestDocVerify_JSON(t *testing.T) {
	dir := t.TempDir()
	src := writeDoc(t, dir, "d.md", docFixture)
	out := runCmd(t, newDocVerifyCmd(), src, "--output-format", "json")
	var res apexdoc.VerifyResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Empty(t, res.Duplicates)
	require.Equal(t, 2, res.Headings)
}

// TestDocShardAssemble_RoundTripThroughTheCommands is the acceptance
// criterion, exercised end to end at the CLI boundary.
func TestDocShardAssemble_RoundTripThroughTheCommands(t *testing.T) {
	dir := t.TempDir()
	src := writeDoc(t, dir, "prd.md", docFixture)
	writeDoc(t, dir, "architecture.md", "# Arch\n")
	shardDir := filepath.Join(dir, "prd")

	out := runCmd(t, newDocShardCmd(), src, shardDir)
	require.Contains(t, out, "2 section(s)")
	require.Contains(t, out, apexdoc.IndexFileName)
	require.Contains(t, out, "goals.md")

	// The index exists and links every shard — the contract the calling
	// skill verifies for itself.
	index, err := os.ReadFile(filepath.Join(shardDir, apexdoc.IndexFileName))
	require.NoError(t, err)
	require.Contains(t, string(index), "- [Goals](goals.md)")
	require.Contains(t, string(index), "- [Non-goals](non-goals.md)")

	reassembled := filepath.Join(dir, "out.md")
	out = runCmd(t, newDocAssembleCmd(), shardDir, reassembled)
	require.Contains(t, out, "assembled 2 section(s)")

	got, err := os.ReadFile(reassembled)
	require.NoError(t, err)
	require.Equal(t, docFixture, string(got), "the round trip must be byte-identical")
}

func TestDocShard_RefusesOnDuplicateSlugs(t *testing.T) {
	dir := t.TempDir()
	src := writeDoc(t, dir, "d.md", "# T\n\n## Goals\n\nx\n\n## GOALS\n\ny\n")

	cmd := newDocShardCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{src, filepath.Join(dir, "out")})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "Fix the source first")

	_, statErr := os.Stat(filepath.Join(dir, "out"))
	require.Error(t, statErr, "nothing written")
}

func TestDocShard_Numbered(t *testing.T) {
	dir := t.TempDir()
	src := writeDoc(t, dir, "prd.md", docFixture)
	out := runCmd(t, newDocShardCmd(), src, filepath.Join(dir, "prd"), "--numbered")
	require.Contains(t, out, "01-goals.md")
	require.Contains(t, out, "02-non-goals.md")
}

func TestDocShard_NoHeadingsAtLevel(t *testing.T) {
	dir := t.TempDir()
	src := writeDoc(t, dir, "d.md", "# Only H1\n\nbody\n")
	out := runCmd(t, newDocShardCmd(), src, filepath.Join(dir, "d"))
	require.Contains(t, out, "no H2 headings")
	require.Contains(t, out, apexdoc.IndexFileName)
}

func TestDocAssemble_MissingShardWarns(t *testing.T) {
	dir := t.TempDir()
	src := writeDoc(t, dir, "prd.md", docFixture)
	shardDir := filepath.Join(dir, "prd")
	runCmd(t, newDocShardCmd(), src, shardDir)
	require.NoError(t, os.Remove(filepath.Join(shardDir, "non-goals.md")))

	cmd := newDocAssembleCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{shardDir, filepath.Join(dir, "out.md")})
	require.NoError(t, cmd.Execute(), "a missing shard is reported, not fatal")
	require.Contains(t, stdout.String(), "assembled 1 section(s)")
	require.Contains(t, stderr.String(), "non-goals.md")
}

func TestDocAnalyze_HumanAndJSON(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "feature-brief.md", strings.Repeat("x", 4000))
	writeDoc(t, dir, "feature-brief-discovery-notes.md", strings.Repeat("x", 2000))
	writeDoc(t, dir, "unrelated-prd.md", strings.Repeat("x", 1000))

	out := runCmd(t, newDocAnalyzeCmd(), dir, "--output-format", "json")
	var res apexdoc.AnalyzeResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Equal(t, "ok", res.Status)
	require.Equal(t, 3, res.Summary.TotalFiles)
	require.Equal(t, apexdoc.RoutingSingle, res.Routing.Recommendation,
		"3 files and ~1750 tokens is under both limits")

	human := runCmd(t, newDocAnalyzeCmd(), dir)
	require.Contains(t, human, "feature-brief.md")
	require.Contains(t, human, "brief")
	require.Contains(t, human, "groups:")
	require.Contains(t, human, "primary")
	require.Contains(t, human, "companion")
	require.Contains(t, human, "routing: single")
}

func TestDocAnalyze_ThresholdFlags(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		writeDoc(t, dir, string(rune('a'+i))+".md", "x")
	}
	out := runCmd(t, newDocAnalyzeCmd(), dir, "--output-format", "json")
	var res apexdoc.AnalyzeResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Equal(t, apexdoc.RoutingFanOut, res.Routing.Recommendation)

	out = runCmd(t, newDocAnalyzeCmd(), dir, "--single-max-files", "10", "--output-format", "json")
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Equal(t, apexdoc.RoutingSingle, res.Routing.Recommendation)
}

func TestDocCmd_Surface(t *testing.T) {
	cmd := newDocCmd()
	require.Contains(t, cmd.Aliases, "docs")
	names := map[string]bool{}
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	for _, verb := range []string{"verify", "shard", "assemble", "analyze"} {
		require.True(t, names[verb], "ape doc %s missing", verb)
	}
}
