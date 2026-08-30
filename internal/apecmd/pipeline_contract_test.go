package apecmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// captureStderr runs fn with os.Stderr redirected, returning what it
// wrote. The reader must be drained to EOF *after* the writer is closed —
// polling it with a non-blocking receive would return "" whenever the
// reader goroutine had not been scheduled yet, which makes every
// "expected no output" assertion pass vacuously.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()
	require.NoError(t, w.Close())
	os.Stderr = orig
	out := <-done // blocks until EOF, so nothing written can be missed
	require.NoError(t, r.Close())
	return out
}

// nilRunLog is the "no runlog" getter every core in these tests uses.
func nilRunLog() *runlog.Writer { return nil }

// TestContracts_AbsentTableEnrolsNothing is the degradation lock for
// Gate C: a project whose framework predates _apex/terminal-contracts.csv
// — or that never installed one — must behave exactly as it did before
// the check existed. No error, no warning, no transcript read, no
// behaviour change of any kind.
func TestContracts_AbsentTableEnrolsNothing(t *testing.T) {
	projectRoot := t.TempDir() // no _apex/terminal-contracts.csv

	core := newInteractiveCore(func() {}, nilRunLog)
	out := captureStderr(t, func() { core.setContracts(projectRoot) })

	require.Empty(t, out, "an absent contract table must not warn")
	require.Zero(t, core.contracts.Len(), "nothing is enrolled")

	// And the per-step check is a no-op: it must not even look at a
	// transcript, let alone report anything.
	core.stepMu.Lock()
	core.activeSkill = "apex-story-batch-review"
	core.stepMu.Unlock()
	core.transcriptMu.Lock()
	core.activeTranscript = filepath.Join(projectRoot, "does-not-exist.jsonl")
	core.transcriptMu.Unlock()

	out = captureStderr(t, func() { core.checkTerminalContract() })
	require.Empty(t, out, "no table means no check and no output")
}

// TestContracts_PresentTableChecks proves the same code path does fire
// once the framework ships a table — so the silence above is genuinely
// "not configured", not "wired up wrong".
func TestContracts_PresentTableChecks(t *testing.T) {
	projectRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, "_apex", "terminal-contracts.csv"),
		[]byte("skill,pattern\napex-story-batch-review,^run_status:\n"), 0o600,
	))

	transcript := filepath.Join(projectRoot, "session.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte(
		`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll wait for it to finish."}]}}`+"\n",
	),
		0o600))

	core := newInteractiveCore(func() {}, nilRunLog)
	core.setContracts(projectRoot)
	require.Equal(t, 1, core.contracts.Len())

	core.stepMu.Lock()
	core.activeSkill = "apex-story-batch-review"
	core.stepMu.Unlock()
	core.transcriptMu.Lock()
	core.activeTranscript = transcript
	core.transcriptMu.Unlock()

	out := captureStderr(t, func() { core.checkTerminalContract() })
	require.Contains(t, out, "did not reach its summary step")
	require.Contains(t, out, "apex-story-batch-review")
}

// TestContracts_VerdictIsPersisted is the whole of the v0.0.60 change:
// the verdict used to be printed to stderr and dropped, so the base rate
// the warn-only release exists to establish was never recorded anywhere a
// later reader could find it.
//
// Both durable sinks are asserted together because they answer the
// question at different ranges — the manifest field survives the run
// directory being swept for transcripts, the checkpoint row carries the
// diagnostic text.
func TestContracts_VerdictIsPersisted(t *testing.T) {
	for _, tc := range []struct {
		name       string
		closing    string
		wantStatus string
	}{
		{"emitted", "run_status: complete", "present"},
		{"not emitted", "I'll wait for it to finish.", "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, "_apex"), 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(projectRoot, "_apex", "terminal-contracts.csv"),
				[]byte("skill,pattern\napex-story-batch-review,^run_status:\n"), 0o600,
			))
			transcript := filepath.Join(projectRoot, "session.jsonl")
			require.NoError(t, os.WriteFile(transcript, []byte(
				`{"type":"assistant","message":{"content":[{"type":"text","text":"`+tc.closing+`"}]}}`+"\n",
			), 0o600))

			rl, err := runlog.New(filepath.Join(projectRoot, "run"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = rl.Close() })

			core := newInteractiveCore(func() {}, func() *runlog.Writer { return rl })
			core.setContracts(projectRoot)
			core.stepMu.Lock()
			core.activeSkill = "apex-story-batch-review"
			core.activeStep = "build/1-apex-story-batch-review"
			core.stepMu.Unlock()
			core.transcriptMu.Lock()
			core.activeTranscript = transcript
			core.transcriptMu.Unlock()

			var got string
			_ = captureStderr(t, func() { got = core.checkTerminalContract() })
			require.Equal(t, tc.wantStatus, got, "the token the manifest records")

			row := readCheckpointKind(t, rl.Dir(), "contract")
			require.NotNil(t, row, "the run directory answers without re-reading a transcript")
			require.Equal(t, "build/1-apex-story-batch-review", row["step"],
				"the verdict is attributed to the step that earned it")
			payload, ok := row["payload"].(map[string]any)
			require.True(t, ok, "payload: %v", row["payload"])
			require.Equal(t, tc.wantStatus, payload["status"])
			require.Equal(t, "apex-story-batch-review", payload["skill"])
		})
	}
}

// A skill the table does not enrol records NOTHING — not "not-enrolled".
//
// The alternative looks harmless and is not: Check short-circuits an
// absent table to the same status, so a project whose framework ships no
// table would record nothing while a project that enrolled one unrelated
// skill would record "not-enrolled" for every other skill it ran. The same
// step would carry two different values for the same reason, and any base
// rate computed across projects would be measuring which projects have a
// table rather than which skills emit their contract.
func TestContracts_UnenrolledSkillRecordsNothing(t *testing.T) {
	projectRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, "_apex", "terminal-contracts.csv"),
		[]byte("skill,pattern\napex-story-batch-review,^run_status:\n"), 0o600,
	))
	transcript := filepath.Join(projectRoot, "session.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte(
		`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`+"\n",
	), 0o600))

	rl, err := runlog.New(filepath.Join(projectRoot, "run"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rl.Close() })

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return rl })
	core.setContracts(projectRoot)
	core.stepMu.Lock()
	core.activeSkill = "apex-story-batch-create" // no row in the table
	core.stepMu.Unlock()
	core.transcriptMu.Lock()
	core.activeTranscript = transcript
	core.transcriptMu.Unlock()

	require.Empty(t, core.checkTerminalContract(), "omitted, not `not-enrolled`")
	require.Nil(t, readCheckpointKind(t, rl.Dir(), "contract"),
		"and nothing reaches the run's stream either")
}

// An empty table stays completely inert — the degradation every project on
// an older framework takes.
func TestContracts_AbsentTableRecordsNothing(t *testing.T) {
	projectRoot := t.TempDir() // no _apex/terminal-contracts.csv
	rl, err := runlog.New(filepath.Join(projectRoot, "run"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rl.Close() })

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return rl })
	core.setContracts(projectRoot)
	core.stepMu.Lock()
	core.activeSkill = "apex-story-batch-review"
	core.stepMu.Unlock()

	require.Empty(t, core.checkTerminalContract())
	require.Nil(t, readCheckpointKind(t, rl.Dir(), "contract"))
}

// readCheckpointKind returns the first checkpoints.jsonl row of `kind`,
// or nil when there is none.
func readCheckpointKind(t *testing.T, runDir, kind string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "checkpoints.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		if row["kind"] == kind {
			return row
		}
	}
	return nil
}

// A skill with no row in the table is inert even when a table exists.
func TestContracts_UnenrolledSkillSilent(t *testing.T) {
	projectRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, "_apex", "terminal-contracts.csv"),
		[]byte("skill,pattern\napex-story-batch-review,^run_status:\n"), 0o600,
	))

	transcript := filepath.Join(projectRoot, "session.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte(
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Batch Story Creation Complete"}]}}`+"\n",
	),
		0o600))

	core := newInteractiveCore(func() {}, nilRunLog)
	core.setContracts(projectRoot)

	core.stepMu.Lock()
	core.activeSkill = "apex-story-batch-create" // emits no contract by design
	core.stepMu.Unlock()
	core.transcriptMu.Lock()
	core.activeTranscript = transcript
	core.transcriptMu.Unlock()

	out := captureStderr(t, func() { core.checkTerminalContract() })
	require.Empty(t, out, "absence of a contract that was never promised is not evidence")
}
