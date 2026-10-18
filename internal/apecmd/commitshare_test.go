package apecmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessions"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// A commit made while another session's run in the same checkout went is
// claimed by neither run (eval rc.3 probe: a service job's apex-accept
// claimed a gate commit a concurrent child made). The run's own parent and
// children are its lineage, not another session; a run whose manifest is
// still "running" with no live row is a dead one, not a window; a session
// in another checkout does not count.
func TestSplitSharedCommits(t *testing.T) {
	outsideEverySession(t)
	root := t.TempDir()
	abs, err := filepath.Abs(root)
	require.NoError(t, err)
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	manifest := func(skill, run, body string) {
		dir := filepath.Join(runlog.TasksRoot(root), skill, run)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("run_id: "+run+"\n"+body), 0o644))
	}
	ts := func(s int) string { return at(s).Format(time.RFC3339Nano) }
	// Another session's finished run, 10 s to 30 s.
	manifest("apex-change", "other-run", "status: completed\nstarted_at: "+ts(10)+"\nended_at: "+ts(30)+"\n")
	// This run's child, over the whole window: lineage.
	manifest("apex-apply", "child-run", "status: completed\nparent_run: me-run\nstarted_at: "+ts(0)+"\nended_at: "+ts(100)+"\n")
	// A dead run's manifest, left running.
	manifest("apex-plan", "dead-run", "status: running\nstarted_at: "+ts(-1000)+"\n")
	// This run's own manifest.
	manifest("apex-orchestrator", "me-run", "status: running\nstarted_at: "+ts(0)+"\n")
	// The parent, live; an unrelated live session from 35 s; one elsewhere.
	pid := os.Getpid()
	registerRow(t, sessions.Session{PID: pid, CWD: abs, Command: "task", RunID: "parent-run", StartedAt: at(-100)})
	registerRow(t, sessions.Session{PID: pid, CWD: abs, Command: "prompt", RunID: "live-prompt", StartedAt: at(35)})
	registerRow(t, sessions.Session{PID: pid, CWD: filepath.Join(abs, "elsewhere"), Command: "task", RunID: "far-run", StartedAt: at(-100)})

	commits := []runCommit{{"before the other", at(5)}, {"during the other", at(20)}, {"between", at(33)}, {"during the live one", at(40)}}
	claimed, shared := splitSharedCommits(root, "me-run", "parent-run", commits, at(60))
	require.Equal(t, []string{"before the other", "between"}, claimed)
	require.Equal(t, []string{"during the other", "during the live one"}, shared)

	// Unknown run: nothing is judged shared.
	claimed, shared = splitSharedCommits(root, "", "", commits, at(60))
	require.Len(t, claimed, 4)
	require.Empty(t, shared)

	// The manifest records them; none, no field.
	dir := filepath.Join(runlog.TasksRoot(root), "apex-orchestrator", "me-run")
	recordCommitsShared(dir, nil)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	require.NoError(t, err)
	require.NotContains(t, string(data), "commits_shared")
	recordCommitsShared(dir, []string{"during the other"})
	data, err = os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	require.NoError(t, err)
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.Equal(t, []string{"during the other"}, m.CommitsShared)
}
