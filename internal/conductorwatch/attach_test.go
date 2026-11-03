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

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/conductor"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// lockedPane is fakePane safe to change while conductor.Run reads it.
type lockedPane struct {
	mu sync.Mutex
	fakePane
}

func (l *lockedPane) set(screen, session string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.screen, l.session = screen, session
}

func (l *lockedPane) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.typed...)
}

func (l *lockedPane) Capture(ctx context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakePane.Capture(ctx)
}

func (l *lockedPane) Type(ctx context.Context, text string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakePane.Type(ctx, text)
}

func (l *lockedPane) Session(ctx context.Context) (id, why string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakePane.Session(ctx)
}

// v1.5 stage 1a, `ape conductor attach`: an ensemble's events reach the
// conductor's pane through the same gate as the 1.4.2 watcher's
// deliveries. A busy pane gets nothing (the control); an idle one gets the
// event, marked as ape's with its ack command; another session in the pane
// pauses delivery, and the event waiting is typed after a --repin from that
// session.
func TestAttach_DeliversThroughTheGate(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := t.Context()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))

	pane := &lockedPane{screen: busyScreen, conductor: true, session: "conductor-1"}
	state := t.TempDir()
	gate := &Gate{Pane: pane, StateDir: state}
	go func() {
		_ = conductor.Run(ctx, nc, conductor.Config{
			User: "dana", Ensemble: "shop", Projects: []string{"notes"}, Holder: "h1", Nonce: "n1",
			LeaseTTL: time.Minute, Renew: 10 * time.Second, AckWait: time.Minute,
			Session: &PaneSession{Ctx: ctx, Gate: gate},
		})
	}()
	time.Sleep(500 * time.Millisecond) // the consumer bound: DeliverNew
	publish := func(seq int64, event string) {
		_, err := events.Publish(ctx, js, events.Subject("dana", "notes", events.KindProject, "M1", event), events.MessageID("notes", "c1", seq), []byte(`{"reason":"x"}`))
		require.NoError(t, err)
	}

	publish(1, "block")
	time.Sleep(time.Second)
	require.Empty(t, pane.seen(), "the control: a busy pane gets nothing typed")

	pane.set(idleScreen, "conductor-1")
	require.Eventually(t, func() bool { return len(pane.seen()) == 1 }, 5*time.Second, 50*time.Millisecond)
	typed := pane.seen()[0]
	require.True(t, strings.HasPrefix(typed, apexv1.DeliveryTag), typed)
	require.Contains(t, typed, "ape conductor ack '$JS.ACK.", "the token single-quoted (V8g)")

	pane.set(idleScreen, "after-clear")
	publish(2, "question")
	time.Sleep(1500 * time.Millisecond)
	require.Len(t, pane.seen(), 1, "another session in the pane: paused, nothing typed")
	require.NoError(t, RequestRepin(state, "after-clear", time.Now()))
	require.Eventually(t, func() bool { return len(pane.seen()) == 2 }, 5*time.Second, 50*time.Millisecond, "typed after the --repin")
	require.Contains(t, pane.seen()[1], "question")
}

func (l *lockedPane) Enter(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakePane.Enter(ctx)
}

// v1.5 PRE-4: attach's PaneSession submits what it types, through the
// watcher's Submit: an Enter swallowed is pressed again; the control, a
// pane that never submits, gets submitEnters Enters and Type fails, so
// the event stays pending.
func TestPaneSession_SubmitsAsTheWatcherDoes(t *testing.T) {
	for _, c := range []struct {
		name    string
		swallow int
		enters  int
		err     bool
	}{
		{"one Enter: submitted", 0, 1, false},
		{"the first Enter swallowed: a second submits it", 1, 2, false},
		{"the control: no Enter ever submits", -1, 3, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			pane := &fakePane{screen: idleScreen, conductor: true, rows: true, swallow: c.swallow}
			var logged []string
			s := &PaneSession{
				Ctx: t.Context(), SubmitSettle: time.Millisecond, SubmitWait: 30 * time.Millisecond,
				Gate: &Gate{Pane: pane, StateDir: t.TempDir(), Log: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }},
			}
			err := s.Type("<ape-delivery seq=\"1\"> an event </ape-delivery>")
			require.Equal(t, c.enters, pane.enters)
			require.Len(t, pane.typed, 1)
			if c.err {
				require.Error(t, err)
				require.NotEmpty(t, pane.row, "the row keeps it")
				require.Len(t, logged, 1)
				require.Contains(t, logged[0], "not submitted")
				return
			}
			require.NoError(t, err)
			require.Empty(t, pane.row)
		})
	}
}

// v1.5 PRE-4, through the real tmux: the conductor's pane runs a stand-in
// named claude, registered as a session, that reads its tty in canonical
// mode — a line reaches it only after Enter, as a real REPL's prompt is
// submitted only by Enter. What attach's PaneSession types is received
// whole. PDFA-6's case: a stand-in that keeps the text in its input row
// after the first Enter, and takes it at the second, still gets it.
// Before PRE-4, Type only typed, and neither stand-in received anything.
func TestTmux_PaneSessionDeliveryIsSubmitted(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	data, err := os.ReadFile(bash)
	require.NoError(t, err)
	claude := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(claude, data, 0o755))
	// The idle screen, the prompt left open so the tty's echo lands in the
	// input row; swallow: the first line is put back in the row, and the
	// next Enter (an empty line) submits it.
	const stub = `idle() { printf '\033[H\033[2J● done\n\n────\n❯ %s' "$1"; }
held=""; n=0
while :; do
  idle "$held"
  IFS= read -r line || exit 0
  if [ -n "$held" ]; then line="$held$line"; held=""
  elif [ "$SWALLOW" = 1 ] && [ $n = 0 ] && [ -n "$line" ]; then n=1; held="$line"; continue
  fi
  [ -n "$line" ] && printf '%s\n' "$line" >> "$LOG"
done`
	for _, swallow := range []string{"0", "1"} {
		t.Run("swallow="+swallow, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "received")
			script := filepath.Join(dir, "stub.sh")
			require.NoError(t, os.WriteFile(script, []byte(stub), 0o600))
			sock := filepath.Join(dir, "tmux.sock")
			out, err := exec.Command("tmux", "-S", sock, "new-session", "-d", "-x", "120", "-y", "30", "-s", "c",
				"env", "SWALLOW="+swallow, "LOG="+log, claude, script).CombinedOutput()
			require.NoError(t, err, string(out))
			t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })
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
			cfg := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(cfg, "sessions"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(cfg, "sessions", pid+".json"), []byte(`{"pid":`+pid+`,"sessionId":"s-conductor"}`), 0o600))

			var logged []string
			var mu sync.Mutex
			s := &PaneSession{
				Ctx: t.Context(), SubmitSettle: 100 * time.Millisecond, SubmitWait: time.Second,
				Gate: &Gate{Pane: &Tmux{Target: "c", ConfigDir: cfg, socket: sock}, StateDir: t.TempDir(), Log: func(f string, a ...any) {
					mu.Lock()
					defer mu.Unlock()
					logged = append(logged, fmt.Sprintf(f, a...))
				}},
			}
			require.Eventually(t, s.Idle, 5*time.Second, 100*time.Millisecond, "the gate opens on the idle stand-in")
			delivery := conductor.Format(27, "n1", "ape.evt.bob.notes.project.-.question", []byte(`{"reason":"`+strings.Repeat("which way next? ", 25)+`"}`), "$JS.ACK.x.y.1")
			require.NoError(t, s.Type(delivery), "%v", logged)
			require.Eventually(t, func() bool {
				got, _ := os.ReadFile(log)
				return string(got) == delivery+"\n"
			}, 5*time.Second, 50*time.Millisecond, "the stand-in received the delivery, submitted")
		})
	}
}
