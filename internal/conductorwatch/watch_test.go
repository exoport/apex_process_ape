package conductorwatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
}

func (f *fakePane) Capture(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.screen, nil
}

func (f *fakePane) Type(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typed = append(f.typed, text)
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

// Without a pane the watcher only records (notify-only): nothing is typed
// and nothing alerts — the project's notifier already ran on the event.
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

// D665+ (research finding 3): the pane's session id is recorded at the
// watcher's first look; when another Claude Code session takes the pane,
// nothing more is typed, each project logs conductor-stalled once, and the
// watcher asks to be restarted with --pane. The arrival control: the same
// session gets the delivery.
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
			pane.session = "someone-else"
		}
		p.Emit(ctx, apexv1.Event{Event: apexv1.EventBlock, Change: "add-export", Reason: "which format?"}, nil)
		for range 3 {
			require.NoError(t, w.Tick(ctx))
		}
		stalled := 0
		for _, e := range p.EventsSince(0) {
			if e.Event == EventStalled {
				stalled++
				require.Contains(t, e.Reason, "someone-else")
				require.Contains(t, e.Action, "--pane")
			}
		}
		if !replaced {
			require.Len(t, pane.typed, 1, "the control: the recorded session gets the delivery")
			require.Zero(t, stalled)
			continue
		}
		require.Empty(t, pane.typed, "another session in the pane: nothing typed")
		require.Equal(t, 1, stalled, "the person is notified, once")
		require.Contains(t, logged.String(), "restart it with --pane")
		pane.session = "conductor-1"
		require.NoError(t, w.Tick(ctx))
		require.NoError(t, w.Tick(ctx))
		require.Empty(t, pane.typed, "stopped for good: a restart with --pane records the pane again")
	}
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
