package apecmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// loopTranscript writes a loop session's transcript whose newest turn sent
// tokens of context to model.
func loopTranscript(t *testing.T, model string, tokens int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "loop.jsonl")
	body := `{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":1,"cache_read_input_tokens":10}}}` + "\n" +
		`{"type":"assistant","message":{"model":"` + model + `","usage":{"input_tokens":0,"cache_read_input_tokens":` +
		strconv.Itoa(tokens) + `,"cache_creation_input_tokens":0}}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// inRelayedLoop makes this process a child launch from the loop run
// orch-run, whose relay record names a transcript at tokens of model, with
// relays relays so far.
func inRelayedLoop(t *testing.T, root, model string, tokens, relays int) {
	t.Helper()
	outsideEverySession(t)
	inSession(t, "task/orch-run", 1)
	registerRow(t, orchestratorRow())
	require.NoError(t, apexv1.WriteRelayRecord(root, &apexv1.RelayRecord{Run: "orch-run", Relays: relays, Transcript: loopTranscript(t, model, tokens)}))
}

func relayRecord(t *testing.T, root string) *apexv1.RelayRecord {
	t.Helper()
	rec, err := apexv1.ReadRelayRecord(root, "orch-run")
	require.NoError(t, err)
	require.NotNil(t, rec)
	return rec
}

// OI-11, a child launch from the loop: below limits.context_relay it
// starts (depth 2); at it, session.relay (exit 2, stop) with the relay
// recorded for the loop's process; with the relays spent, limits.relays
// (exit 1, person). claude-sonnet-4-5 has a 200K window: 60 % is 120000.
func TestRelay_ChildLaunch(t *testing.T) {
	root := t.TempDir()

	t.Run("below the threshold: the child starts", func(t *testing.T) {
		inRelayedLoop(t, root, "claude-sonnet-4-5", 119_000, 0)
		l, err := gateSession("ape task", "apex-change", root)
		require.NoError(t, err)
		require.Equal(t, 2, l.depth)
		require.Nil(t, relayRecord(t, root).Requested)
	})

	t.Run("at the threshold: session.relay, recorded", func(t *testing.T) {
		inRelayedLoop(t, root, "claude-sonnet-4-5", 120_000, 0)
		_, err := gateSession("ape task", "apex-change", root)
		var ue *apexv1.UsageError
		require.ErrorAs(t, err, &ue)
		require.Equal(t, apexv1.CodeSessionRelay, ue.Code)
		refusal := sessionRefusal(err)
		require.Equal(t, ExitUsage, exitCodeOf(t, refusal))
		require.Contains(t, refusal.Error(), "context is at 60 % (120000 of 200000 tokens, claude-sonnet-4-5)")
		require.Contains(t, refusal.Error(), "(code session.relay, action stop)")
		req := relayRecord(t, root).Requested
		require.NotNil(t, req)
		require.False(t, req.Exhausted)
		require.Equal(t, "ape task", req.Verb)
		require.Equal(t, 120_000, req.Tokens)
		require.Equal(t, 200_000, req.Window)
		require.InDelta(t, 0.6, req.Threshold, 1e-9)
	})

	t.Run("every child-launching verb is the boundary", func(t *testing.T) {
		for _, s := range []struct{ verb, skill string }{
			{"ape change review --run", apexv1.ReviewSkill},
			{"ape change tool", "apex-change"},
			{"ape change accept --delegate", "apex-change"},
		} {
			inRelayedLoop(t, root, "claude-sonnet-4-5", 150_000, 0)
			_, err := gateSession(s.verb, s.skill, root)
			require.ErrorContains(t, err, s.verb+" refused: this "+apexv1.OrchestratorSkill+" session's context is at 75 %")
		}
	})

	t.Run("relays spent: limits.relays, a person's", func(t *testing.T) {
		inRelayedLoop(t, root, "claude-sonnet-4-5", 130_000, apexv1.DefaultRelays)
		_, err := gateSession("ape task", "apex-change", root)
		var fe *apexv1.FailError
		require.ErrorAs(t, err, &fe)
		require.Equal(t, apexv1.CodeRelaysExceeded, fe.Code)
		refusal := sessionRefusal(err)
		require.Equal(t, ExitRunFailed, exitCodeOf(t, refusal))
		require.Contains(t, refusal.Error(), "(code limits.relays, action person)")
		require.True(t, relayRecord(t, root).Requested.Exhausted)
	})

	t.Run("the configured threshold", func(t *testing.T) {
		cfgRoot := projectFor(t, "limits:\n  context_relay: 0.05\n  relays: 1\n")
		inRelayedLoop(t, cfgRoot, "claude-sonnet-4-5", 10_000, 0)
		_, err := gateSession("ape task", "apex-change", cfgRoot)
		require.ErrorContains(t, err, "at or over limits.context_relay 5 %")
		require.ErrorContains(t, err, "(relay 1 of 1)")
	})

	t.Run("an unknown window: no relay, said once", func(t *testing.T) {
		inRelayedLoop(t, root, "claude-nonesuch-9", 900_000, 0)
		var warn bytes.Buffer
		require.NoError(t, relayGate("ape task", root, "orch-run", &warn))
		require.NoError(t, relayGate("ape task", root, "orch-run", &warn))
		require.Equal(t, "warning: ape knows no context window for claude-nonesuch-9, so this "+apexv1.OrchestratorSkill+
			" session's context fill is unknown and it is not relayed (said once per run)\n", warn.String())
		rec := relayRecord(t, root)
		require.Equal(t, "claude-nonesuch-9", rec.ContextUnknown)
		require.Nil(t, rec.Requested)
	})

	t.Run("no relay record: a loop ape did not launch is not relayed", func(t *testing.T) {
		inRelayedLoop(t, root, "claude-sonnet-4-5", 190_000, 0)
		apexv1.RemoveRelayRecord(root, "orch-run")
		_, err := gateSession("ape task", "apex-change", root)
		require.NoError(t, err)
	})

	t.Run("a child still running: no relay (one at a time refuses the launch)", func(t *testing.T) {
		inRelayedLoop(t, root, "claude-sonnet-4-5", 190_000, 0)
		lock := filepath.Join(runlog.SessionLocksRoot(root), "child-orch-run.json")
		require.NoError(t, acquireSessionLock(lock, sessionLock{PID: os.Getppid(), Token: "other-child", StartedAt: time.Now()}, true))
		t.Cleanup(func() { _ = os.Remove(lock) })
		require.NoError(t, relayGate("ape task", root, "orch-run", &bytes.Buffer{}))
		require.Nil(t, relayRecord(t, root).Requested)
	})

	t.Run("a detached run's own ape is not judged again", func(t *testing.T) {
		inRelayedLoop(t, root, "claude-sonnet-4-5", 190_000, 0)
		t.Setenv(envDetachedID, "20261019-1200-abcd")
		_, err := gateSession("ape task", "apex-change", root)
		require.NoError(t, err)
	})
}

// The loop's side: the record is written at the run's start, learns the
// transcript without losing a child's fields, and is removed at the end.
func TestRelay_LoopRecord(t *testing.T) {
	root := t.TempDir()
	r := &loopRelay{root: root, relays: 2, relayOf: "run-1"}
	r.start("run-3")
	rec, err := apexv1.ReadRelayRecord(root, "run-3")
	require.NoError(t, err)
	require.Equal(t, 2, rec.Relays)
	require.Equal(t, "run-1", rec.RelayOf)
	rec.ContextUnknown = "m"
	require.NoError(t, apexv1.WriteRelayRecord(root, rec))
	r.transcript("/t/loop.jsonl")
	got := r.finish()
	require.Equal(t, "/t/loop.jsonl", got.Transcript)
	require.Equal(t, "m", got.ContextUnknown, "a child launch's field survives the transcript update")
	require.NoFileExists(t, apexv1.RelayRecordPath(root, "run-3"))

	var none *loopRelay
	none.start("x")
	none.transcript("y")
	require.Nil(t, none.finish())
	require.Empty(t, none.relayOfOrEmpty())
	require.Zero(t, none.count())
}

// A session that ended on limits.relays stops the loop run: exit 1, the
// person's code with a resume line, and termination relays in its manifest.
func TestRelay_RelaysStopped(t *testing.T) {
	runDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "manifest.yaml"), []byte("run_id: run-6\nstatus: done\n"), 0o644))
	o := taskOptions{skill: apexv1.OrchestratorSkill, args: "M2", projectRoot: t.TempDir()}
	run := relaysStopped(o, taskRun{RunID: "run-6", RunDir: runDir, Envelope: taskEnvelope{Success: true}},
		&apexv1.RelayRequest{Tokens: 130_000, Window: 200_000, Threshold: 0.6, Exhausted: true}, 5)
	require.Equal(t, ExitRunFailed, run.Envelope.ExitCode)
	require.False(t, run.Envelope.Success)
	fe, ok := errors.AsType[*apexv1.FailError](run.RunErr)
	require.True(t, ok)
	require.Equal(t, apexv1.CodeRelaysExceeded, fe.Code)
	require.Contains(t, *run.Envelope.Error, "after 5 relay(s), limits.relays 5")
	require.Contains(t, *run.Envelope.Error, "resume: `ape status` shows where M2 stopped; then `ape task "+apexv1.OrchestratorSkill+" --args M2`")
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.yaml"))
	require.NoError(t, err)
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.NotNil(t, m.Termination)
	require.Equal(t, pipeline.TerminationRelays, m.Termination.Kind)
}
