package apecmd

import (
	"io"
	"os"
	"path/filepath"
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
		[]byte("skill,pattern\napex-story-batch-review,^run_status:\n"), 0o600))

	transcript := filepath.Join(projectRoot, "session.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte(
		`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll wait for it to finish."}]}}`+"\n"),
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

// A skill with no row in the table is inert even when a table exists.
func TestContracts_UnenrolledSkillSilent(t *testing.T) {
	projectRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, "_apex", "terminal-contracts.csv"),
		[]byte("skill,pattern\napex-story-batch-review,^run_status:\n"), 0o600))

	transcript := filepath.Join(projectRoot, "session.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte(
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Batch Story Creation Complete"}]}}`+"\n"),
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
