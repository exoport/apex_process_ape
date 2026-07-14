package sessiondriver

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// turnLine renders one assistant turn in the claude-code JSONL shape.
func turnLine(in, out int) string {
	return fmt.Sprintf(
		`{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":%d,"output_tokens":%d}}}`,
		in, out,
	)
}

func TestScanStep_NoTranscript(t *testing.T) {
	st := ScanStep(ScanParams{FlushGrace: time.Millisecond})
	require.NotNil(t, st)
	require.Zero(t, st.Totals.NumTurns)
	require.Contains(t, st.Note, "no transcript captured")
	require.Nil(t, st.Advance)
}

func TestScanStep_MainDeltaAndAdvance(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sess.jsonl")
	require.NoError(t, os.WriteFile(src, []byte(turnLine(100, 200)+"\n"+turnLine(50, 60)+"\n"), 0o600))

	// First scan: baseline zero → absolute totals; Advance carries the
	// absolute scan for the next step's baseline.
	st := ScanStep(ScanParams{Source: src, ParentSessionID: "sess-1", FlushGrace: time.Millisecond})
	require.Equal(t, 2, st.Totals.NumTurns)
	require.Equal(t, 150, st.Totals.InputTokens)
	require.Equal(t, 260, st.Totals.OutputTokens)
	require.Greater(t, st.Totals.CostUSD, 0.0)
	require.Len(t, st.Sessions, 1)
	require.Equal(t, "sess-1", st.Sessions[0].SessionID)
	require.NotNil(t, st.Advance)
	require.Equal(t, src, st.Advance.Path)

	// Second scan with the prior scan as baseline against the SAME path
	// after one more turn: delta is the single new turn.
	f, err := os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(turnLine(10, 20) + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	st2 := ScanStep(ScanParams{
		Source:          src,
		ParentSessionID: "sess-1",
		PrevPath:        st.Advance.Path,
		PrevTotals:      st.Advance.Totals,
		PrevByModel:     st.Advance.ByModel,
		FlushGrace:      time.Millisecond,
	})
	require.Equal(t, 1, st2.Totals.NumTurns, "delta is one new turn")
	require.Equal(t, 10, st2.Totals.InputTokens)
	require.Equal(t, 20, st2.Totals.OutputTokens)
}

// TestScanStep_ResetsBaselineOnPathChange: a `/clear` rotates the
// session_id → a new transcript path, so the previous cumulative must
// not be subtracted (that produced negative deltas historically).
func TestScanStep_ResetsBaselineOnPathChange(t *testing.T) {
	dir := t.TempDir()
	s1 := filepath.Join(dir, "s1.jsonl")
	s2 := filepath.Join(dir, "s2.jsonl")
	require.NoError(t, os.WriteFile(s1, []byte(turnLine(100, 200)+"\n"+turnLine(50, 60)+"\n"), 0o600))
	require.NoError(t, os.WriteFile(s2, []byte(turnLine(30, 40)+"\n"), 0o600))

	first := ScanStep(ScanParams{Source: s1, FlushGrace: time.Millisecond})
	require.Equal(t, 2, first.Totals.NumTurns)

	// Baseline is s1's cumulative but Source moved to s2 → reset to zero.
	second := ScanStep(ScanParams{
		Source:      s2,
		PrevPath:    s1,
		PrevTotals:  first.Advance.Totals,
		PrevByModel: first.Advance.ByModel,
		FlushGrace:  time.Millisecond,
	})
	require.Equal(t, 1, second.Totals.NumTurns, "s2 absolute, not s2-s1")
	require.Equal(t, 40, second.Totals.OutputTokens)
	require.GreaterOrEqual(t, second.Totals.CostUSD, 0.0, "no negative cost from path-change reset")
}

func TestScanStep_ZeroTurnsNote(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(empty, []byte(""), 0o600))
	st := ScanStep(ScanParams{Source: empty, ParentSessionID: "sess-e", FlushGrace: time.Millisecond})
	require.Zero(t, st.Totals.NumTurns)
	require.Contains(t, st.Note, "zero assistant turns")
}

func TestScanStep_MissingSourceNote(t *testing.T) {
	st := ScanStep(ScanParams{Source: "/nonexistent/does-not-exist.jsonl", FlushGrace: time.Millisecond})
	require.Zero(t, st.Totals.NumTurns)
	require.Contains(t, st.Note, "transcript missing")
}
