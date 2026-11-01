package conductorwatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// fakePane is a pane whose screen, registration and typed lines the test
// controls.
type fakePane struct {
	mu        sync.Mutex
	screen    string
	conductor bool
	// session is the registered session's id; "" is "s1".
	session string
	typed   []string
	// row is the input row's text after Type, until an Enter submits it
	// (rows: a test that models the row); swallow is how many Enters are
	// lost before one submits (PDFA-6), -1 for none ever.
	rows    bool
	row     string
	swallow int
	enters  int
}

func (f *fakePane) Capture(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.row != "" {
		return "● done\n\n────\n❯ " + f.row + "\n────\n", nil
	}
	return f.screen, nil
}

func (f *fakePane) Type(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typed = append(f.typed, text)
	if f.rows {
		f.row = text
	}
	return nil
}

func (f *fakePane) Enter(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enters++
	if f.swallow != 0 {
		if f.swallow > 0 {
			f.swallow--
		}
		return nil
	}
	f.row = ""
	return nil
}

func (f *fakePane) Session(context.Context) (id, why string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.conductor {
		return "", "the pane runs bash, not Claude Code"
	}
	if f.session == "" {
		return "s1", ""
	}
	return f.session, ""
}

const (
	idleScreen  = "● done\n\n────\n❯ \n────\n  ? for shortcuts\n"
	busyScreen  = "✻ Thinking… (esc to interrupt)\n────\n❯ \n────\n"
	typedScreen = "● done\n\n────\n❯ approve M2 bec\n────\n"
)

func project(t *testing.T) *apexv1.Project {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"_apex/state", "development"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), []byte("project_name: notes\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "development", "project.md"), []byte("# notes\n"), 0o644))
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	return p
}

func TestIdle(t *testing.T) {
	require.True(t, Idle(idleScreen))
	require.False(t, Idle(busyScreen), "a turn running")
	require.False(t, Idle(typedScreen), "the person's half-typed input: never typed after it")
	require.False(t, Idle("$ ls\nfoo\n$ "), "no input box at all")
}

// D664 + D664+: an event becomes a delivery; typed marked into the pane
// only when the pane is the conductor's, idle, its input empty and the
// screen steady; an unacknowledged delivery alerts once past the ack
// alert; an acknowledged one does not.
func TestWatcher(t *testing.T) {
	p := project(t)
	pane := &fakePane{screen: busyScreen, conductor: true}
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	w := New(Config{Projects: []string{p.Root}, StateDir: t.TempDir(), Pane: pane, Nonce: "n0", AckAlert: 15 * time.Minute, Now: func() time.Time { return now }})
	ctx := context.Background()

	p.Emit(ctx, apexv1.Event{Event: apexv1.EventBlock, Change: "add-export", Reason: "which format?"}, nil)
	p.Emit(ctx, apexv1.Event{Event: apexv1.EventRunEnd, Run: "r1"}, nil) // not one the conductor needs
	require.NoError(t, w.Tick(ctx))
	s, err := LoadState(w.c.StateDir)
	require.NoError(t, err)
	require.Len(t, s.Deliveries, 1)
	id := s.Deliveries[0].ID
	require.Empty(t, pane.typed, "busy: nothing typed")

	pane.screen = typedScreen
	require.NoError(t, w.Tick(ctx))
	require.NoError(t, w.Tick(ctx))
	require.Empty(t, pane.typed, "the person is typing: nothing typed")

	pane.conductor, pane.screen = false, idleScreen
	require.NoError(t, w.Tick(ctx))
	require.NoError(t, w.Tick(ctx))
	require.Empty(t, pane.typed, "the pane is a shell, not the registered session: nothing typed")

	pane.conductor = true
	require.NoError(t, w.Tick(ctx), "first idle look")
	require.Empty(t, pane.typed, "the screen must be steady across two looks")
	require.NoError(t, w.Tick(ctx))
	require.Len(t, pane.typed, 1)
	require.True(t, strings.HasPrefix(pane.typed[0], apexv1.DeliveryTag))
	require.Contains(t, pane.typed[0], `nonce="n0"`)
	require.Contains(t, pane.typed[0], "ape conductor ack "+id)

	now = now.Add(16 * time.Minute)
	require.NoError(t, w.Tick(ctx))
	require.NoError(t, w.Tick(ctx))
	evs := p.EventsSince(0)
	stalled := 0
	for _, e := range evs {
		if e.Event == EventStalled {
			stalled++
		}
	}
	require.Equal(t, 1, stalled, "unacknowledged past the alert: the person is notified, once")

	p.Emit(ctx, apexv1.Event{Event: apexv1.EventQuestion, Action: "answer Q1: <answer>", Reason: "EUR?"}, nil)
	require.NoError(t, w.Tick(ctx))
	require.NoError(t, w.Tick(ctx))
	require.Len(t, pane.typed, 2)
	s, _ = LoadState(w.c.StateDir)
	require.NoError(t, Ack(w.c.StateDir, s.Deliveries[1].ID, now))
	now = now.Add(time.Hour)
	require.NoError(t, w.Tick(ctx))
	stalled = 0
	for _, e := range p.EventsSince(0) {
		if e.Event == EventStalled {
			stalled++
		}
	}
	require.Equal(t, 1, stalled, "an acknowledged delivery does not alert")
}

// Without a pane the watcher only records (notify-only): nothing is typed;
// the project's notifier already ran on the event, and the ack alert
// still applies (TestWatcher_AlertsWhetherTypedOrNot).
func TestWatcher_NotifyOnly(t *testing.T) {
	p := project(t)
	w := New(Config{Projects: []string{p.Root}, StateDir: t.TempDir()})
	p.Emit(context.Background(), apexv1.Event{Event: apexv1.EventBlock, Change: "x"}, nil)
	require.NoError(t, w.Tick(context.Background()))
	s, err := LoadState(w.c.StateDir)
	require.NoError(t, err)
	require.Len(t, s.Deliveries, 1)
	require.Empty(t, s.Deliveries[0].DeliveredAt)
}

// D665+ (research finding 3) and D666+ F5: the pane's session id is
// recorded at the watcher's first look; when another Claude Code session
// takes the pane (a /clear), the watcher pauses — nothing typed, each
// project logs conductor-stalled once — until a --repin from that session,
// with the pane running it, pins it. A --repin from a session the pane
// does not run pins nothing. The arrival control: the pinned session gets
// the delivery.
func TestWatcher_PaneSessionReplaced(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		p := project(t)
		pane := &fakePane{screen: idleScreen, conductor: true, session: "conductor-1"}
		var logged strings.Builder
		w := New(Config{
			Projects: []string{p.Root}, StateDir: t.TempDir(), Pane: pane, Nonce: "n0",
			Log: func(format string, a ...any) { fmt.Fprintf(&logged, format, a...) },
		})
		ctx := context.Background()
		require.NoError(t, w.Tick(ctx), "start: the session is recorded")
		if replaced {
			pane.session = "after-clear"
		}
		p.Emit(ctx, apexv1.Event{Event: apexv1.EventBlock, Change: "add-export", Reason: "which format?"}, nil)
		for range 3 {
			require.NoError(t, w.Tick(ctx))
		}
		stalled := func() int {
			n := 0
			for _, e := range p.EventsSince(0) {
				if e.Event == EventStalled {
					n++
					require.Contains(t, e.Reason, "after-clear")
					require.Equal(t, "ape conductor watcher --repin", e.Action)
				}
			}
			return n
		}
		if !replaced {
			require.Len(t, pane.typed, 1, "the control: the pinned session gets the delivery")
			require.Zero(t, stalled())
			continue
		}
		require.Empty(t, pane.typed, "another session in the pane: paused, nothing typed")
		require.Equal(t, 1, stalled(), "the person is notified, once")

		require.NoError(t, RequestRepin(w.c.StateDir, "someone-else", time.Now()))
		for range 3 {
			require.NoError(t, w.Tick(ctx))
		}
		require.Empty(t, pane.typed, "a --repin from a session the pane does not run pins nothing")
		require.Contains(t, logged.String(), `--repin from session "someone-else" ignored`)
		require.Equal(t, 1, stalled(), "still paused, not notified again")

		require.NoError(t, RequestRepin(w.c.StateDir, "after-clear", time.Now()))
		for range 3 {
			require.NoError(t, w.Tick(ctx))
		}
		require.Len(t, pane.typed, 1, "re-pinned from the new session: typing again")
		require.Contains(t, logged.String(), "re-pinned to Claude Code session after-clear")
	}
}

// D666+ F1: an event unacknowledged past the ack alert notifies the person
// whether or not it was typed — a busy pane, and no pane at all, delay the
// typing, never the alert. Within the alert nothing is raised.
func TestWatcher_AlertsWhetherTypedOrNot(t *testing.T) {
	for name, pane := range map[string]Pane{"busy pane": &fakePane{screen: busyScreen, conductor: true}, "notify-only": nil} {
		p := project(t)
		now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
		w := New(Config{Projects: []string{p.Root}, StateDir: t.TempDir(), Pane: pane, AckAlert: 15 * time.Minute, Now: func() time.Time { return now }})
		ctx := context.Background()
		p.Emit(ctx, apexv1.Event{Event: apexv1.EventBlock, Change: "x"}, nil)
		require.NoError(t, w.Tick(ctx))
		now = now.Add(14 * time.Minute)
		require.NoError(t, w.Tick(ctx))
		var stalled []apexv1.Event
		collect := func() {
			stalled = nil
			for _, e := range p.EventsSince(0) {
				if e.Event == EventStalled {
					stalled = append(stalled, e)
				}
			}
		}
		collect()
		require.Empty(t, stalled, name+": within the alert")
		now = now.Add(2 * time.Minute)
		require.NoError(t, w.Tick(ctx))
		require.NoError(t, w.Tick(ctx))
		collect()
		require.Len(t, stalled, 1, name+": past the alert, once")
		require.Contains(t, stalled[0].Reason, "never typed", name)
	}
}

// --repin names the Claude Code session it runs under: the nearest
// ancestor registered under the config dir.
func TestCallerSession(t *testing.T) {
	cfg := t.TempDir()
	require.Empty(t, CallerSession(cfg), "no registered ancestor")
	require.NoError(t, os.MkdirAll(filepath.Join(cfg, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "sessions", strconv.Itoa(os.Getppid())+".json"), []byte(`{"sessionId":"s-parent"}`), 0o600))
	require.Equal(t, "s-parent", CallerSession(cfg))
}

// The real tmux: a shell pane is not the conductor (the negative control),
// whatever its screen shows. Skipped where tmux is not installed.
func TestTmux_ShellPaneIsNotTheConductor(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	t.Setenv("TMUX_TMPDIR", filepath.Dir(sock))
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-S", sock}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("new-session", "-d", "-s", "w", "sh")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })
	tm := &Tmux{Target: "w", ConfigDir: t.TempDir(), socket: sock}
	id, why := tm.Session(context.Background())
	require.Empty(t, id)
	require.Contains(t, why, "not Claude Code")
}

// The real tmux, the positive side: a pane whose foreground process is
// claude, registered under the config dir, is the conductor; the same pane
// unregistered is not.
func TestTmux_RegisteredClaudePaneIsTheConductor(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// A process whose name is claude: a copy of bash waiting on a builtin
	// (a script shows its interpreter's name; a copied sleep may be a
	// multi-call binary that dispatches on its name).
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	data, err := os.ReadFile(bash)
	require.NoError(t, err)
	claude := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(claude, data, 0o755))
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	out, err := exec.Command("tmux", "-S", sock, "new-session", "-d", "-s", "c", claude+" -c 'read -t 60 x'").CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })
	cfg := t.TempDir()
	tm := &Tmux{Target: "c", ConfigDir: cfg, socket: sock}
	var pid string
	require.Eventually(t, func() bool {
		o, err := exec.Command("tmux", "-S", sock, "display-message", "-p", "-t", "c", "#{pane_pid} #{pane_current_command}").Output()
		f := strings.Fields(string(o))
		if err != nil || len(f) < 2 || f[1] != "claude" {
			return false
		}
		pid = f[0]
		return true
	}, 5*time.Second, 50*time.Millisecond, "the pane runs claude")
	id, why := tm.Session(context.Background())
	require.Empty(t, id, "not registered yet")
	require.Contains(t, why, "no session registered")
	require.NoError(t, os.MkdirAll(filepath.Join(cfg, "sessions"), 0o755))
	reg := filepath.Join(cfg, "sessions", pid+".json")
	require.NoError(t, os.WriteFile(reg, []byte("{}"), 0o600))
	id, why = tm.Session(context.Background())
	require.Empty(t, id, "registered without a sessionId")
	require.Contains(t, why, "no sessionId")
	require.NoError(t, os.WriteFile(reg, []byte(`{"pid":`+pid+`,"sessionId":"ea461431-cb9b"}`), 0o600))
	id, why = tm.Session(context.Background())
	require.Equal(t, "ea461431-cb9b", id, why)
}

// 1.4.3 PDFA-6: on Claude Code 2.1.311 the Enter after a long typed delivery
// (pdfa.9, ≈ 500 characters) was swallowed. The delivery sat in the input
// row, unsubmitted, and every later delivery saw a busy row. The watcher now
// confirms the submit (the row empty, or a turn running) and presses Enter
// again, at most three times in all. The control: a pane that never submits
// gets three Enters, the delivery is recorded unsubmitted, nothing is typed
// over it, and the ack alert still reaches the person.
func TestWatcher_ConfirmsTheSubmit(t *testing.T) {
	long := strings.Repeat("the conductor's next step, as the orders read it; ", 10)
	for _, c := range []struct {
		name        string
		swallow     int
		enters      int
		unsubmitted bool
	}{
		{"the first Enter swallowed: a second submits it", 1, 2, false},
		{"the control: no Enter ever submits", -1, 3, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := project(t)
			pane := &fakePane{screen: idleScreen, conductor: true, rows: true, swallow: c.swallow}
			now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
			w := New(Config{
				Projects: []string{p.Root}, StateDir: t.TempDir(), Pane: pane, Nonce: "n0", AckAlert: 15 * time.Minute,
				SubmitSettle: time.Millisecond, SubmitWait: 30 * time.Millisecond, Now: func() time.Time { return now },
			})
			ctx := context.Background()
			p.Emit(ctx, apexv1.Event{Event: apexv1.EventBlock, Change: "add-toc", Reason: long}, nil)
			require.NoError(t, w.Tick(ctx))
			require.NoError(t, w.Tick(ctx))
			require.Len(t, pane.typed, 1)
			require.Equal(t, c.enters, pane.enters)
			s, err := LoadState(w.c.StateDir)
			require.NoError(t, err)
			require.NotEmpty(t, s.Deliveries[0].DeliveredAt)
			require.Equal(t, c.unsubmitted, s.Deliveries[0].Unsubmitted)
			if !c.unsubmitted {
				require.Empty(t, pane.row, "submitted: the row is empty again")
				return
			}
			p.Emit(ctx, apexv1.Event{Event: apexv1.EventBlock, Change: "add-toc", Reason: "a second event"}, nil)
			require.NoError(t, w.Tick(ctx))
			require.NoError(t, w.Tick(ctx))
			require.Len(t, pane.typed, 1, "nothing is typed over the unsubmitted delivery")
			now = now.Add(16 * time.Minute)
			require.NoError(t, w.Tick(ctx))
			stalled := 0
			for _, e := range p.EventsSince(0) {
				if e.Event == EventStalled {
					stalled++
				}
			}
			require.Positive(t, stalled, "unacknowledged: the person is notified")
		})
	}
}
