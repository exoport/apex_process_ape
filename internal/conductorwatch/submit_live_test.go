package conductorwatch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// TestLive_LongDeliveryIsSubmitted is PDFA-6's live check (1.4.3): a real
// Claude Code session in a tmux pane gets a ≈ 600-character delivery from a
// real watcher Tick, and its own transcript must record it as a submitted
// prompt — the watcher's judgement is not the evidence. On 2.1.311 the
// Enter after such a text was swallowed (pdfa.9). Run it per version with
// that version's claude first on PATH. One short Haiku turn.
func TestLive_LongDeliveryIsSubmitted(t *testing.T) {
	if os.Getenv("APE_CLAUDE_LIVE") != "1" {
		t.Skip("set APE_CLAUDE_LIVE=1 (needs claude on PATH, auth, network and tmux) for the live delivery check")
	}
	claude, err := exec.LookPath("claude")
	require.NoError(t, err)
	_, err = exec.LookPath("tmux")
	require.NoError(t, err)
	ver, _ := exec.Command(claude, "--version").Output()
	t.Logf("claude %s", strings.TrimSpace(string(ver)))

	p := project(t)
	work := t.TempDir()
	state := t.TempDir()
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	tm := func(args ...string) string {
		out, _ := exec.Command("tmux", append([]string{"-S", sock}, args...)...).CombinedOutput()
		return string(out)
	}
	// The session's own ape (if the model runs the ack line) writes to
	// this test's state, never the operator's.
	cmd := "cd " + work + " && XDG_STATE_HOME=" + state + " exec " + claude + " --dangerously-skip-permissions --model haiku"
	out, err := exec.Command("tmux", "-S", sock, "new-session", "-d", "-s", "c", "-x", "220", "-y", "50", cmd).CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() { tm("kill-server") })

	pane := &Tmux{Target: "c", socket: sock}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// Through the trust dialog (an untrusted scratch folder) to the REPL.
	for ctx.Err() == nil {
		screen := tm("capture-pane", "-p", "-t", "c")
		if strings.Contains(screen, "bypass permissions on") && Idle(screen) {
			break
		}
		if strings.Contains(screen, "Yes, I trust this folder") {
			// The dialog's default is "No, exit": select Yes, then Enter.
			if strings.Contains(screen, "❯ No") {
				tm("send-keys", "-t", "c", "Down")
				time.Sleep(300 * time.Millisecond)
			}
			tm("send-keys", "-t", "c", "Enter")
		}
		time.Sleep(time.Second)
	}
	require.NoError(t, ctx.Err(), "the REPL did not come up:\n%s", tm("capture-pane", "-p", "-t", "c"))
	var id string
	require.Eventually(t, func() bool { id, _ = pane.Session(ctx); return id != "" }, 30*time.Second, 500*time.Millisecond,
		"the pane's claude registered no session")

	counted := &countingPane{Tmux: pane}
	w := New(Config{Projects: []string{p.Root}, StateDir: state, Pane: counted, Nonce: "live", Poll: time.Second})
	reason := strings.Repeat("The conductor reads ape status for this event and answers nothing yet. ", 8) + "Reply with the single word OK."
	require.Greater(t, len(reason), 500)
	p.Emit(ctx, apexv1.Event{Event: apexv1.EventBlock, Change: "add-toc", Reason: reason}, nil)
	var d Delivery
	require.Eventually(t, func() bool {
		_ = w.Tick(ctx)
		s, err := LoadState(state)
		if err != nil || len(s.Deliveries) == 0 {
			return false
		}
		d = s.Deliveries[0]
		return d.DeliveredAt != ""
	}, 60*time.Second, time.Second, "never typed:\n%s", tm("capture-pane", "-p", "-t", "c"))
	require.False(t, d.Unsubmitted, "the watcher found it unsubmitted after three Enters:\n%s", tm("capture-pane", "-p", "-t", "c"))

	// The evidence: the session's transcript holds the delivery as a prompt.
	home, _ := os.UserHomeDir()
	transcripts, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", id+".jsonl"))
	require.Eventually(t, func() bool {
		if len(transcripts) == 0 {
			transcripts, _ = filepath.Glob(filepath.Join(home, ".claude", "projects", "*", id+".jsonl"))
			return false
		}
		data, err := os.ReadFile(transcripts[0])
		return err == nil && promptCarries(data, apexv1.DeliveryTag)
	}, 60*time.Second, time.Second, "the transcript records no submitted delivery:\n%s", tm("capture-pane", "-p", "-t", "c"))
	t.Logf("submitted: %s (%d chars) after %d Enter(s), transcript %s", d.ID, len(Mark(d, "live")), counted.enters, transcripts[0])
	t.Cleanup(func() {
		if !t.Failed() {
			_ = os.RemoveAll(filepath.Dir(transcripts[0]))
		}
	})
}

// countingPane counts the Enters the watcher presses: how many a submit
// took on this Claude Code.
type countingPane struct {
	*Tmux
	enters int
}

func (c *countingPane) Enter(ctx context.Context) error {
	c.enters++
	return c.Tmux.Enter(ctx)
}

// promptCarries reports a user prompt in a transcript carrying s.
func promptCarries(data []byte, s string) bool {
	for line := range strings.SplitSeq(string(data), "\n") {
		var e struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &e) != nil || e.Type != "user" {
			continue
		}
		if txt, err := strconv.Unquote(string(e.Message.Content)); err == nil && strings.Contains(txt, s) {
			return true
		}
		if strings.Contains(string(e.Message.Content), s) {
			return true
		}
	}
	return false
}
