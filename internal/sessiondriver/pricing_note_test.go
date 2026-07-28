package sessiondriver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTranscript drops a one-line transcript using the given model id.
func writeTranscript(t *testing.T, dir, model string, inputTokens int) string {
	t.Helper()
	path := filepath.Join(dir, "sid.jsonl")
	line := `{"type":"assistant","timestamp":"2026-07-20T10:00:00Z","sessionId":"s1","version":"2.1.0",` +
		`"message":{"id":"msg_1","model":"` + model + `","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":` + itoa(inputTokens) + `,"output_tokens":0}}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestScanStepNotesUnpricedModelButKeepsTokens is the regression lock for
// the trap in this file: note() returns a ZEROED Telemetry, so routing an
// unpriced-model diagnostic through it would trade a wrong cost for wrong
// tokens — strictly worse than the bug being fixed. The step must keep
// every token and turn, and say the cost is incomplete.
func TestScanStepNotesUnpricedModelButKeepsTokens(t *testing.T) {
	dir := t.TempDir()
	path := writeTranscript(t, dir, "nobody-knows-this-model", 1000)

	tele := ScanStep(ScanParams{Source: path, ParentSessionID: "s1", FlushGrace: 1})
	if tele == nil {
		t.Fatal("ScanStep returned nil")
	}
	if tele.Totals.InputTokens != 1000 {
		t.Errorf("InputTokens = %d, want 1000 — tokens must survive an unpriced model", tele.Totals.InputTokens)
	}
	if tele.Totals.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", tele.Totals.NumTurns)
	}
	if tele.Totals.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0", tele.Totals.CostUSD)
	}
	if !strings.Contains(tele.Note, "unpriced") {
		t.Errorf("Note = %q, want it to flag the unpriced model", tele.Note)
	}
	if !strings.Contains(tele.Note, "nobody-knows-this-model") {
		t.Errorf("Note = %q, want it to name the model", tele.Note)
	}
	if tele.Advance == nil {
		t.Error("Advance is nil — the baseline must still advance on a priced-badly step")
	}
}

// TestScanStepNoteIsAppendedNotReplaced locks that two conditions holding
// at once both survive. Telemetry.Note is single-valued; an assignment
// would silently drop whichever diagnostic ran second. Here the transcript
// has zero assistant turns AND (once turns exist) an unpriced model — the
// zero-turns breadcrumb must not be lost.
func TestScanStepNoteIsAppendedNotReplaced(t *testing.T) {
	tele := &Telemetry{}
	appendNote(tele, "first")
	appendNote(tele, "second")
	appendNote(tele, "")
	if tele.Note != "first; second" {
		t.Errorf("Note = %q, want %q", tele.Note, "first; second")
	}
}

// TestScanStepStaysSilentWhenFullyPriced — a healthy step must produce no
// note at all, or the warning becomes noise operators learn to ignore.
func TestScanStepStaysSilentWhenFullyPriced(t *testing.T) {
	dir := t.TempDir()
	path := writeTranscript(t, dir, "claude-opus-5", 1_000_000)

	tele := ScanStep(ScanParams{Source: path, ParentSessionID: "s1", FlushGrace: 1})
	if tele.Note != "" {
		t.Errorf("Note = %q, want empty on a fully-priced step", tele.Note)
	}
	if tele.Totals.CostUSD <= 0 {
		t.Errorf("CostUSD = %v, want > 0 for claude-opus-5", tele.Totals.CostUSD)
	}
}
