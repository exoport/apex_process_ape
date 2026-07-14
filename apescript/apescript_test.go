package apescript_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/apescript"
	"github.com/stretchr/testify/require"
)

// TestArgs_NoRuntime returns nil when no environment is installed.
func TestArgs_NoRuntime(t *testing.T) {
	require.Nil(t, apescript.Args())
}

// TestOrchestration_NoRuntime: the runner/plumbing facades report ErrNoRuntime
// when called outside a live `ape script` invocation.
func TestOrchestration_NoRuntime(t *testing.T) {
	_, err := apescript.RunTask(context.Background(), apescript.TaskOpts{Skill: "x"})
	require.ErrorIs(t, err, apescript.ErrNoRuntime)
	_, err = apescript.RunPipeline(context.Background(), apescript.PipelineOpts{Name: "x"})
	require.ErrorIs(t, err, apescript.ErrNoRuntime)
	_, err = apescript.RunPrompt(context.Background(), apescript.PromptOpts{Text: "x"})
	require.ErrorIs(t, err, apescript.ErrNoRuntime)
	require.ErrorIs(t, apescript.PublishEvent("e", nil), apescript.ErrNoRuntime)
	_, _, err = apescript.PutBlob(context.Background(), strings.NewReader("x"))
	require.ErrorIs(t, err, apescript.ErrNoRuntime)
}

// TestActivate_Dispatch proves the facades dispatch through the installed
// hooks and that restore clears the environment.
func TestActivate_Dispatch(t *testing.T) {
	var gotTask apescript.TaskOpts
	var published string
	restore := apescript.Activate(apescript.Config{
		Args: []string{"one", "two"},
		RunTask: func(_ context.Context, o apescript.TaskOpts) (apescript.RunResult, error) {
			gotTask = o
			return apescript.RunResult{RunID: "r1", Status: "completed", CostUSD: 1.5}, nil
		},
		Publish: func(event string, _ any) error { published = event; return nil },
	})

	require.Equal(t, []string{"one", "two"}, apescript.Args())

	res, err := apescript.RunTask(context.Background(), apescript.TaskOpts{Skill: "apex-x", Model: "opus[1m]"})
	require.NoError(t, err)
	require.Equal(t, "r1", res.RunID)
	require.InDelta(t, 1.5, res.CostUSD, 1e-9)
	require.Equal(t, "apex-x", gotTask.Skill)
	require.Equal(t, "opus[1m]", gotTask.Model)

	require.NoError(t, apescript.PublishEvent("summary", map[string]any{"k": "v"}))
	require.Equal(t, "summary", published)

	restore()
	require.Nil(t, apescript.Args())
	_, err = apescript.RunTask(context.Background(), apescript.TaskOpts{Skill: "x"})
	require.ErrorIs(t, err, apescript.ErrNoRuntime)
}

// TestLog_Quiet: Log writes to the configured writer, and --quiet suppresses it.
func TestLog_Quiet(t *testing.T) {
	var buf bytes.Buffer
	restore := apescript.Activate(apescript.Config{LogWriter: &buf})
	apescript.Log("hello %s", "world")
	restore()
	require.Equal(t, "hello world\n", buf.String())

	buf.Reset()
	restore = apescript.Activate(apescript.Config{LogWriter: &buf, Quiet: true})
	apescript.Log("suppressed")
	restore()
	require.Empty(t, buf.String())
}

// TestPublishPutBlob_NotConfigured: with a runtime but no NATS hooks, the
// event/blob facades return a clear "not configured" error (not ErrNoRuntime).
func TestPublishPutBlob_NotConfigured(t *testing.T) {
	restore := apescript.Activate(apescript.Config{})
	defer restore()
	err := apescript.PublishEvent("e", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not configured")
	_, _, err = apescript.PutBlob(context.Background(), strings.NewReader("x"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not configured")
}

// TestReadManifest reads a fixture manifest by run-dir and by manifest path.
func TestReadManifest(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run-1")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	manifest := `schema_version: 2
run_id: run-1
status: completed
totals:
  cost_usd: 2.5
  num_turns: 7
`
	mpath := filepath.Join(runDir, "manifest.yaml")
	require.NoError(t, os.WriteFile(mpath, []byte(manifest), 0o644))

	byDir, err := apescript.ReadManifest(runDir)
	require.NoError(t, err)
	require.Equal(t, "run-1", byDir.RunID)
	require.Equal(t, "completed", string(byDir.Status))
	require.InDelta(t, 2.5, byDir.Totals.CostUSD, 1e-9)

	byPath, err := apescript.ReadManifest(mpath)
	require.NoError(t, err)
	require.Equal(t, "run-1", byPath.RunID)
}

// TestScanTranscript scans a minimal session transcript.
func TestScanTranscript(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sess.jsonl")
	line := `{"type":"assistant","sessionId":"s1","message":{"id":"m1","model":"claude-opus-4-8","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":20}}}`
	require.NoError(t, os.WriteFile(p, []byte(line+"\n"), 0o644))
	res, err := apescript.ScanTranscript(p)
	require.NoError(t, err)
	require.Equal(t, 10, res.Totals.InputTokens)
	require.Equal(t, 20, res.Totals.OutputTokens)
	require.Equal(t, 1, res.Totals.NumTurns)
}

// TestSkills lists a project-scoped skill and tags it framework/custom.
func TestSkills(t *testing.T) {
	cwd := t.TempDir()
	for _, name := range []string{"apex-create-prd", "my-custom"} {
		d := filepath.Join(cwd, ".claude", "skills", name)
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("# "+name), 0o644))
	}
	skills, err := apescript.Skills(cwd)
	require.NoError(t, err)
	byName := map[string]apescript.SkillInfo{}
	for _, s := range skills {
		byName[s.Name] = s
	}
	require.Contains(t, byName, "apex-create-prd")
	require.Contains(t, byName, "my-custom")
	require.True(t, byName["apex-create-prd"].Framework)
	require.False(t, byName["my-custom"].Framework)
	require.Equal(t, "project", byName["apex-create-prd"].Scope)
}

// TestPutBlob_Dispatch proves the PutBlob facade streams through the hook.
func TestPutBlob_Dispatch(t *testing.T) {
	restore := apescript.Activate(apescript.Config{
		PutBlob: func(_ context.Context, r io.Reader) (apescript.Digest, string, error) {
			b, _ := io.ReadAll(r)
			return apescript.Digest{Algo: "sha256", Hex: "abc"}, "nats://x", errors.New("sentinel:" + string(b))
		},
	})
	defer restore()
	d, uri, err := apescript.PutBlob(context.Background(), strings.NewReader("payload"))
	require.Equal(t, "sha256:abc", d.String())
	require.Equal(t, "nats://x", uri)
	require.ErrorContains(t, err, "sentinel:payload")
}
