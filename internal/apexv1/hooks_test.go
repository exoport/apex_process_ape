package apexv1

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func editPayload(t *testing.T, cwd, tool, path string) HookPayload {
	t.Helper()
	key := "file_path"
	if tool == "NotebookEdit" {
		key = "notebook_path"
	}
	raw, err := json.Marshal(map[string]any{
		"session_id": "s1", "cwd": cwd, "hook_event_name": "PreToolUse",
		"tool_name": tool, "tool_input": map[string]any{key: path, "content": "x"},
	})
	require.NoError(t, err)
	p, err := ParseHookPayload(raw)
	require.NoError(t, err)
	return p
}

func TestPreTool(t *testing.T) {
	p := fixture(t, baseFiles())
	sub := filepath.Join(p.Root, "internal")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	cases := []struct {
		tool, path, cwd string
		deny            bool
		reason          string
	}{
		{"Edit", "development/specs/greeting/spec.md", "", true, "specs change only through `ape change archive`"},
		{"Write", "development/specs/new-cap/spec.md", "", true, "living spec"},
		{"MultiEdit", filepath.Join(p.Root, filepath.FromSlash("development/specs/greeting/spec.md")), "", true, "living spec"},
		{"Write", "development/changes/add-wave/state.json", "", true, "written only by ape"},
		{"Write", "development/changes/archive/2026-10-01-x/change.md", "", true, "archived change"},
		{"NotebookEdit", "development/.apex/milestones.json", "", true, "ape's own record"},
		{"Edit", "../development/specs/greeting/spec.md", sub, true, "living spec"},
		{"Write", "development/changes/add-wave/change.md", "", false, ""},
		{"Write", "development/changes/add-wave/design.md", "", false, ""},
		{"Write", "development/changes/add-wave/assets/state.json", "", false, ""},
		{"Write", "development/project.md", "", false, ""},
		{"Write", "development/roadmap.md", "", false, ""},
		{"Edit", "internal/main.go", "", false, ""},
		{"Read", "development/specs/greeting/spec.md", "", false, ""},
		{"Bash", "development/specs/greeting/spec.md", "", false, ""},
		{"Write", "/etc/development/specs/x.md", "", false, ""},
		{"Write", "development/specsheet.md", "", false, ""},
	}
	for _, tc := range cases {
		cwd := tc.cwd
		if cwd == "" {
			cwd = p.Root
		}
		d := PreTool(editPayload(t, cwd, tc.tool, tc.path))
		require.Equal(t, tc.deny, d.Deny, "%s %s", tc.tool, tc.path)
		if !tc.deny {
			out, err := d.Output()
			require.NoError(t, err)
			require.Nil(t, out)
			continue
		}
		require.Contains(t, d.Reason, tc.reason)
		require.Contains(t, d.Reason, "specs change only through")
		out, err := d.Output()
		require.NoError(t, err)
		var got struct {
			HookSpecificOutput struct {
				HookEventName            string `json:"hookEventName"`
				PermissionDecision       string `json:"permissionDecision"`
				PermissionDecisionReason string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		require.NoError(t, json.Unmarshal(out, &got))
		require.Equal(t, "PreToolUse", got.HookSpecificOutput.HookEventName)
		require.Equal(t, "deny", got.HookSpecificOutput.PermissionDecision)
		require.Equal(t, d.Reason, got.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestPreTool_SymlinkIntoSpecsIsStillSpecs(t *testing.T) {
	p := fixture(t, baseFiles())
	link := filepath.Join(p.Root, "specs-link")
	require.NoError(t, os.Symlink(p.SpecsDir(), link))
	require.True(t, PreTool(editPayload(t, p.Root, "Edit", "specs-link/greeting/spec.md")).Deny)
}

func TestPreTool_NotAV1Project(t *testing.T) {
	dir := t.TempDir()
	require.False(t, PreTool(editPayload(t, dir, "Write", "development/specs/x/spec.md")).Deny)
}

func stopPayload(cwd, event string, active bool) HookPayload {
	p := HookPayload{SessionID: "sess-1", Cwd: cwd, HookEventName: event, StopHookActive: active}
	if event == "SubagentStop" {
		p.AgentID = "agent-7"
	}
	return p
}

func TestStopHook(t *testing.T) {
	t.Run("no change in progress allows", func(t *testing.T) {
		p := fixture(t, baseFiles())
		d := StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
		require.False(t, d.Block)
		require.Equal(t, StopAllow, d.Record.Verdict)
		require.Equal(t, "0 changes in progress", d.Record.Reason)
	})
	t.Run("red check blocks with its last lines, three times, then allows", func(t *testing.T) {
		p := startedWave(t)
		d := StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
		require.True(t, d.Block)
		require.Contains(t, d.Reason, "Change add-wave is in progress and 1 command check(s) are red (stop blocked 1/3)")
		require.Contains(t, d.Reason, "T1 `./wave.sh` exit 127")
		require.Contains(t, d.Reason, "not found")
		require.Contains(t, d.Reason, "ape change block add-wave")
		out, err := d.Output()
		require.NoError(t, err)
		var got map[string]string
		require.NoError(t, json.Unmarshal(out, &got))
		require.Equal(t, "block", got["decision"])
		require.Equal(t, d.Reason, got["reason"])

		for i := 2; i <= MaxConsecutiveStopBlocks; i++ {
			d = StopHook(ctx(), stopPayload(p.Root, "Stop", true), RunOptions{})
			require.True(t, d.Block, "block %d", i)
			require.Equal(t, i, d.Record.ConsecutiveBlocks)
		}
		d = StopHook(ctx(), stopPayload(p.Root, "Stop", true), RunOptions{})
		require.False(t, d.Block, "the fourth consecutive stop is let through")
		require.Contains(t, d.Record.Reason, "after 3 consecutive blocks")

		// A fresh stop (stop_hook_active false) starts counting again.
		d = StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
		require.True(t, d.Block)
		require.Equal(t, 1, d.Record.ConsecutiveBlocks)
	})
	t.Run("stop_hook_active false resets the count", func(t *testing.T) {
		p := startedWave(t)
		for range 2 {
			require.True(t, StopHook(ctx(), stopPayload(p.Root, "Stop", true), RunOptions{}).Block)
		}
		d := StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
		require.True(t, d.Block)
		require.Equal(t, 1, d.Record.ConsecutiveBlocks)
	})
	t.Run("green checks allow", func(t *testing.T) {
		p := startedWave(t)
		write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
		d := StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
		require.False(t, d.Block)
		require.Equal(t, "all command checks green", d.Record.Reason)
	})
	t.Run("a blocked change allows", func(t *testing.T) {
		p := startedWave(t)
		_, err := p.Block(ctx(), "add-wave", "question for a person")
		require.NoError(t, err)
		require.False(t, StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{}).Block)
	})
	t.Run("two changes in progress allow", func(t *testing.T) {
		p := startedWave(t)
		addChange(t, p, "add-other", waveChange("add-other"))
		require.NoError(t, SaveState(p.StateFile("add-other"), &State{ID: "add-other", Status: StatusInProgress}))
		d := StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
		require.False(t, d.Block)
		require.Equal(t, "2 changes in progress", d.Record.Reason)
	})
	t.Run("SubagentStop blocks too, counted apart from the main session", func(t *testing.T) {
		p := startedWave(t)
		d := StopHook(ctx(), stopPayload(p.Root, "SubagentStop", false), RunOptions{})
		require.True(t, d.Block)
		require.FileExists(t, filepath.Join(p.StopGateDir(), "sess-1-agent-7.json"))
		require.NoFileExists(t, filepath.Join(p.StopGateDir(), "sess-1.json"),
			"the main-session record is what ape task reads; a sub-agent's must not land there")
	})
	t.Run("the hook writes nothing tracked", func(t *testing.T) {
		p := startedWave(t)
		StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
		require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	})
	t.Run("not a v1 project allows", func(t *testing.T) {
		require.False(t, StopHook(ctx(), stopPayload(t.TempDir(), "Stop", false), RunOptions{}).Block)
	})
}

func TestStopGateApplies(t *testing.T) {
	p := startedWave(t)
	require.False(t, StopGateApplies(p.Root), "no settings.json registers the hook")
	_, err := MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	require.True(t, StopGateApplies(p.Root))
	_, err = p.Block(ctx(), "add-wave", "q")
	require.NoError(t, err)
	require.False(t, StopGateApplies(p.Root))
}

func TestWaitStopVerdict(t *testing.T) {
	p := startedWave(t)
	stopAt := time.Now()

	t.Run("no record appears", func(t *testing.T) {
		_, err := WaitStopVerdict(ctx(), p.Root, "nobody", stopAt, "", 300*time.Millisecond)
		require.ErrorIs(t, err, ErrNoStopVerdict)
	})

	d := StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
	require.True(t, d.Block)
	t.Run("the verdict for this stop", func(t *testing.T) {
		rec, err := WaitStopVerdict(ctx(), p.Root, "sess-1", stopAt, "", time.Second)
		require.NoError(t, err)
		require.Equal(t, StopBlock, rec.Verdict)
		require.Equal(t, "add-wave", rec.Change)
	})
	t.Run("an already consumed record is not reused", func(t *testing.T) {
		_, err := WaitStopVerdict(ctx(), p.Root, "sess-1", stopAt, d.Record.StartedAt, 300*time.Millisecond)
		require.ErrorIs(t, err, ErrNoStopVerdict)
	})
	t.Run("a record from long before this stop is not this stop's", func(t *testing.T) {
		_, err := WaitStopVerdict(ctx(), p.Root, "sess-1", stopAt.Add(time.Minute), "", 300*time.Millisecond)
		require.ErrorIs(t, err, ErrNoStopVerdict)
	})
	t.Run("a pending record is waited for past the appear window", func(t *testing.T) {
		path := p.stopRecordPath("sess-2", "")
		started := time.Now().UTC().Format(time.RFC3339Nano)
		p.saveStopRecord(path, StopRecord{SessionID: "sess-2", StartedAt: started, Verdict: StopPending})
		go func() {
			time.Sleep(600 * time.Millisecond)
			p.saveStopRecord(path, StopRecord{SessionID: "sess-2", StartedAt: started, Verdict: StopAllow})
		}()
		rec, err := WaitStopVerdict(ctx(), p.Root, "sess-2", time.Now(), "", 200*time.Millisecond)
		require.NoError(t, err)
		require.Equal(t, StopAllow, rec.Verdict)
	})
	t.Run("context cancellation ends the wait", func(t *testing.T) {
		path := p.stopRecordPath("sess-3", "")
		p.saveStopRecord(path, StopRecord{SessionID: "sess-3", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Verdict: StopPending})
		c, cancel := context.WithTimeout(ctx(), 300*time.Millisecond)
		defer cancel()
		_, err := WaitStopVerdict(c, p.Root, "sess-3", time.Now(), "", time.Second)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestSessionStartOutput(t *testing.T) {
	out, err := SessionStartOutput("APEX status — x\n")
	require.NoError(t, err)
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, "SessionStart", got.HookSpecificOutput.HookEventName)
	require.Equal(t, "APEX status — x\n", got.HookSpecificOutput.AdditionalContext)
}

func TestParseHookPayload(t *testing.T) {
	p, err := ParseHookPayload([]byte(`{"session_id":"a","cwd":"/x","hook_event_name":"Stop","stop_hook_active":true,"extra":1}`))
	require.NoError(t, err)
	require.True(t, p.StopHookActive)
	require.Equal(t, "Stop", p.HookEventName)
	p, err = ParseHookPayload([]byte("  \n"))
	require.NoError(t, err)
	require.Empty(t, p.SessionID)
	_, err = ParseHookPayload([]byte("not json"))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "panic")
}

// A bare development/ folder (ape's own repo has one) is not a v1
// project: the hooks must not adopt it and write into it. A test of the
// stop hook once did exactly that to this repository.
func TestHooks_IgnoreABareDevelopmentFolder(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", "planning"), 0o755))
	sub := filepath.Join(root, "internal", "pkg")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NotEqual(t, root, FindRoot(sub), "development/ alone is not a marker")
	require.False(t, StopHook(ctx(), stopPayload(sub, "Stop", false), RunOptions{}).Block)
	require.NoDirExists(t, filepath.Join(root, "development", ".apex"))

	require.NoError(t, os.WriteFile(filepath.Join(root, "development", "project.md"), []byte("# x\n"), 0o644))
	require.Equal(t, root, FindRoot(sub), "a development/project.md is")
}
