package conductorterm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// fakeDriver is a terminal whose readiness, output time and death the test
// sets; it records what was typed.
type fakeDriver struct {
	mu    sync.Mutex
	ready bool
	last  time.Time
	typed []string
	done  chan struct{}
}

func (d *fakeDriver) Start(context.Context) error { return nil }
func (d *fakeDriver) Type(_ context.Context, line string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.typed = append(d.typed, line)
	return nil
}

func (d *fakeDriver) Pane(context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ready {
		return "  bypass permissions on\n❯ ", nil
	}
	return "working… (esc to interrupt)", nil
}

func (d *fakeDriver) LastOutput() (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last, true
}
func (d *fakeDriver) Done() <-chan struct{}      { return d.done }
func (d *fakeDriver) Stop(context.Context) error { return nil }

func (d *fakeDriver) set(ready bool, last time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ready, d.last = ready, last
}

func (d *fakeDriver) lines() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.typed...)
}

type events struct {
	mu  sync.Mutex
	got []string
}

func (e *events) emit(ev, reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, ev+": "+reason)
}

func (e *events) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.got...)
}

func appendReply(t *testing.T, path, text string, at time.Time) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer f.Close()
	line, err := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": at.UTC().Format(time.RFC3339Nano),
		"message": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}},
	})
	require.NoError(t, err)
	_, err = f.Write(append(line, '\n'))
	require.NoError(t, err)
}

func project(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex", "state"), 0o755))
	return root
}

// D659 A8 + D660.2 on a fake terminal: the first line once ready; a
// conductor message typed only while idle, marked; an idle terminal with a
// message pending past the threshold logs orchestrator-idle once; a reply
// clears it; a dead session logs orchestrator-dead.
func TestServe(t *testing.T) {
	root := project(t)
	transcript := filepath.Join(t.TempDir(), "s.jsonl")
	d := &fakeDriver{done: make(chan struct{})}
	ev := &events{}
	ctx := t.Context()
	served := make(chan error, 1)
	go func() {
		served <- Serve(ctx, Config{
			Root: root, Name: "orch", Driver: d, First: "/apex-orchestrator", Nonce: "n0",
			IdleAlert: 300 * time.Millisecond, Poll: 20 * time.Millisecond, Quiet: 50 * time.Millisecond,
			Ready:      func(p string) bool { return strings.Contains(p, "bypass permissions on") },
			Transcript: func(time.Time) string { return transcript },
			Emit:       ev.emit,
		})
	}()

	// Busy: nothing typed, even the first line.
	d.set(false, time.Now())
	m, err := Send(root, "approve nothing yet;\n check the gate", time.Now())
	require.NoError(t, err)
	time.Sleep(150 * time.Millisecond)
	require.Empty(t, d.lines(), "nothing typed into a busy terminal")

	// Idle: the first line, then the queued message, marked, one line.
	d.set(true, time.Now().Add(-time.Second))
	require.Eventually(t, func() bool { return len(d.lines()) == 2 }, 2*time.Second, 10*time.Millisecond)
	lines := d.lines()
	require.Equal(t, "/apex-orchestrator", lines[0])
	require.True(t, strings.HasPrefix(lines[1], apexv1.DeliveryTag), lines[1])
	require.Contains(t, lines[1], `nonce="n0"`)
	require.Contains(t, lines[1], "approve nothing yet; check the gate")
	require.NotContains(t, lines[1], "\n")
	_, err = os.Stat(filepath.Join(inboxDir(root), "x"))
	require.True(t, os.IsNotExist(err))

	// Idle with the message pending past the threshold: one event.
	require.Eventually(t, func() bool { return len(ev.list()) == 1 }, 2*time.Second, 10*time.Millisecond)
	require.Contains(t, ev.list()[0], EventIdle+": ")
	time.Sleep(400 * time.Millisecond)
	require.Len(t, ev.list(), 1, "once per idle stretch")
	st, err := LoadState(root)
	require.NoError(t, err)
	require.Equal(t, []int64{m.Seq}, st.Pending)

	// A reply clears pending; Read returns it.
	appendReply(t, transcript, "M2 gate is red on 3 scenarios, as planned", time.Now())
	require.Eventually(t, func() bool { st, _ := LoadState(root); return len(st.Pending) == 0 }, 2*time.Second, 10*time.Millisecond)
	rs, err := Read(root, 0)
	require.NoError(t, err)
	require.Len(t, rs, 1)
	require.Equal(t, "M2 gate is red on 3 scenarios, as planned", rs[0].Text)
	rs, err = Read(root, 1)
	require.NoError(t, err)
	require.Empty(t, rs)

	// The session dies: orchestrator-dead, Serve returns.
	close(d.done)
	require.Error(t, <-served)
	require.Contains(t, ev.list()[len(ev.list())-1], EventDead+": ")
	st, err = LoadState(root)
	require.NoError(t, err)
	require.False(t, st.Alive)
}

// The v1.4-shaped control: with no message pending, an idle orchestrator
// is no event (a wait for a person is the person event's).
func TestServe_IdleWithNothingPendingIsNoEvent(t *testing.T) {
	root := project(t)
	d := &fakeDriver{done: make(chan struct{})}
	d.set(true, time.Now().Add(-time.Second))
	ev := &events{}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = Serve(ctx, Config{
		Root: root, Name: "orch", Driver: d, Nonce: "n0", IdleAlert: 50 * time.Millisecond, Poll: 10 * time.Millisecond, Quiet: 10 * time.Millisecond,
		Ready: func(string) bool { return true }, Emit: ev.emit,
	})
	require.Empty(t, ev.list())
}

func TestSend_Limits(t *testing.T) {
	root := project(t)
	_, err := Send(root, "   ", time.Now())
	require.Error(t, err)
	_, err = Send(root, strings.Repeat("x", MaxMessage+1), time.Now())
	require.ErrorContains(t, err, "write the details to a file")
	_, err = LoadState(root)
	require.ErrorIs(t, err, ErrNoTerminal)
}
