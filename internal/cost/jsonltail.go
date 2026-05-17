package cost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// DefaultTailInterval is the polling cadence between EOF probes. 200ms
// matches PLAN-5 / C7. Override via APE_COST_TAIL_INTERVAL_MS (undocumented
// debug knob) before calling NewTailer.
const DefaultTailInterval = 200 * time.Millisecond

// AssistantLine is the minimal shape Tailer extracts from each JSONL
// row. Lines that don't have `type:"assistant"` are skipped.
type AssistantLine struct {
	Type    string `json:"type"`
	Message struct {
		Model string     `json:"model"`
		Usage UsageBlock `json:"usage"`
	} `json:"message"`
}

// Tailer is a polling-based reader of one session JSONL. Stop closes
// the reader and the goroutine. Concurrent Read / Stop is safe.
type Tailer struct {
	path     string
	interval time.Duration
	out      chan AssistantLine

	cancel context.CancelFunc

	mu       sync.Mutex
	totals   Totals
	lastModel string
}

// NewTailer opens path lazily — the file does not need to exist at
// construction time. Use TailIntervalFromEnv() to honour
// APE_COST_TAIL_INTERVAL_MS for debugging.
func NewTailer(path string, interval time.Duration) *Tailer {
	if interval <= 0 {
		interval = DefaultTailInterval
	}
	return &Tailer{
		path:     path,
		interval: interval,
		out:      make(chan AssistantLine, 128),
	}
}

// TailIntervalFromEnv reads APE_COST_TAIL_INTERVAL_MS and returns it
// as a time.Duration, falling back to DefaultTailInterval. Used by
// the orchestrator when starting per-step tailers.
func TailIntervalFromEnv() time.Duration {
	v := os.Getenv("APE_COST_TAIL_INTERVAL_MS")
	if v == "" {
		return DefaultTailInterval
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms <= 0 {
		return DefaultTailInterval
	}
	return time.Duration(ms) * time.Millisecond
}

// Start kicks off the tail goroutine. Run until ctx cancels or Stop
// is called. The first time path appears on disk, the reader opens it
// at byte 0 (PLAN-5 / C7 — do NOT seek to end; the file may already
// have content by the time we start).
func (t *Tailer) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	go t.run(ctx)
}

// Stop cancels the tail goroutine and closes the output channel after
// a final drain attempt. Idempotent.
func (t *Tailer) Stop() {
	if t.cancel != nil {
		t.cancel()
	}
}

// Lines returns the channel of parsed assistant lines. Closes when
// the tailer exits.
func (t *Tailer) Lines() <-chan AssistantLine { return t.out }

// Totals returns a snapshot of the running totals. Safe to call any time.
func (t *Tailer) Totals() Totals {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.totals
}

// LastModel returns the most recent model name seen on an assistant
// line. Empty if no lines yet. Useful for stamping the chat
// session.yaml.
func (t *Tailer) LastModel() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastModel
}

// run is the polling loop. Maintains a partial-line buffer so a read
// returning a short JSON chunk is rejoined on the next tick. PLAN-5 / C7.
func (t *Tailer) run(ctx context.Context) {
	defer close(t.out)

	// Wait for the file (or its symlink target) to exist. The 30 s
	// deadline matches the orchestrator bootstrap pattern.
	waitDeadline := time.Now().Add(30 * time.Second)
	for {
		_, err := os.Stat(t.path)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(waitDeadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(t.interval):
		}
	}

	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer f.Close()

	var partial bytes.Buffer
	buf := make([]byte, 32*1024)
	emptyTicks := 0
	for {
		select {
		case <-ctx.Done():
			t.drainOnce(f, &partial, buf)
			return
		default:
		}
		n, err := f.Read(buf)
		if n > 0 {
			partial.Write(buf[:n])
			t.consumeLines(&partial)
			emptyTicks = 0
		}
		if err == io.EOF || n == 0 {
			emptyTicks++
			select {
			case <-ctx.Done():
				return
			case <-time.After(t.interval):
			}
			continue
		}
		if err != nil {
			return
		}
		// PLAN-5 / C7: drain on stage-end — two consecutive empty
		// ticks with no new bytes means "no more data right now",
		// which is what we want at stage-end. The caller (Tracker)
		// stops the tailer on stage-end; the emptyTicks counter is
		// kept here only for future hand-off if a future plan wants
		// to add an idle-timeout exit.
		_ = emptyTicks
	}
}

func (t *Tailer) drainOnce(f *os.File, partial *bytes.Buffer, buf []byte) {
	for {
		n, err := f.Read(buf)
		if n > 0 {
			partial.Write(buf[:n])
			t.consumeLines(partial)
		}
		if err == io.EOF || n == 0 {
			return
		}
		if err != nil {
			return
		}
	}
}

func (t *Tailer) consumeLines(partial *bytes.Buffer) {
	for {
		idx := bytes.IndexByte(partial.Bytes(), '\n')
		if idx < 0 {
			return
		}
		line := partial.Next(idx + 1)
		// Drop the trailing newline.
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
		var al AssistantLine
		if err := json.Unmarshal(line, &al); err != nil {
			continue
		}
		if al.Type != "assistant" {
			continue
		}
		t.mu.Lock()
		t.lastModel = al.Message.Model
		price, _ := Lookup(al.Message.Model)
		t.totals.Add(al.Message.Usage, price)
		t.mu.Unlock()
		select {
		case t.out <- al:
		default:
		}
	}
}

// reader interface kept for future expansion. Currently unused.
var _ = bufio.NewReader
