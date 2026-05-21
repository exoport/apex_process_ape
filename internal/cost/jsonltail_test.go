package cost

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTailer_AppendedLinesProcessed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Write one assistant line upfront (the file exists before the
	// tailer starts — PLAN-5 / C7 read-from-byte-0 contract).
	if _, err := f.WriteString(
		`{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":1000000,"output_tokens":0}}}` + "\n",
	); err != nil {
		t.Fatal(err)
	}

	tailer := NewTailer(path, 30*time.Millisecond)
	ctx := t.Context()
	tailer.Start(ctx)

	// Read the first line.
	select {
	case <-tailer.Lines():
	case <-time.After(2 * time.Second):
		t.Fatal("tailer did not deliver the first line")
	}

	// Append a second line.
	if _, err := f.WriteString(
		`{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":0,"output_tokens":500000}}}` + "\n",
	); err != nil {
		t.Fatal(err)
	}

	select {
	case <-tailer.Lines():
	case <-time.After(2 * time.Second):
		t.Fatal("tailer did not deliver the appended line")
	}

	tailer.Stop()
	// Wait for goroutine to finish so totals stabilise.
	deadline := time.After(1 * time.Second)
	for {
		select {
		case _, ok := <-tailer.Lines():
			if !ok {
				goto checked
			}
		case <-deadline:
			t.Fatal("tailer goroutine did not exit")
		}
	}
checked:

	got := tailer.Totals()
	// 1M input tokens at $5/M (opus 4.7) = $5.00
	// 0.5M output tokens at $25/M (opus 4.7) = $12.50
	// Total = $17.50
	if got.CostUSD < 17.4 || got.CostUSD > 17.6 {
		t.Errorf("cost = %f, want ~17.50 (opus 4.7 rates: $5 in / $25 out)", got.CostUSD)
	}
}

func TestTailer_PartialLineRejoined(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tailer := NewTailer(path, 20*time.Millisecond)
	ctx := t.Context()
	tailer.Start(ctx)

	// Write two halves of a JSON line with no trailing newline; then
	// follow up with the newline. The tailer must NOT emit a parse
	// error for the partial; it should rejoin and emit one line.
	line := `{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":100,"output_tokens":50}}}`
	if _, err := f.WriteString(line[:30]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := f.WriteString(line[30:] + "\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tailer.Lines():
	case <-time.After(2 * time.Second):
		t.Fatal("tailer did not rejoin the partial line")
	}
}

func TestTailer_StopWithoutFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never-arrives.jsonl")
	tailer := NewTailer(path, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailer.Start(ctx)
	tailer.Stop()
	// Should exit without panic; lines channel closes.
	var wg sync.WaitGroup
	wg.Go(func() {
		<-tailer.Lines() // blocks until close
	})
	cancel()
	wg.Wait()
}

func TestTailIntervalFromEnv(t *testing.T) {
	t.Setenv("APE_COST_TAIL_INTERVAL_MS", "75")
	if got := TailIntervalFromEnv(); got != 75*time.Millisecond {
		t.Errorf("env override ignored: got %s", got)
	}
	t.Setenv("APE_COST_TAIL_INTERVAL_MS", "junk")
	if got := TailIntervalFromEnv(); got != DefaultTailInterval {
		t.Errorf("malformed env should fall back: got %s", got)
	}
}
