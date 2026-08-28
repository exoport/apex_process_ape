package sessiondriver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The vocabulary is anchored on the prefix. These are the shapes a local
// transcript corpus actually contained.
func TestIsTerminalAPIError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{"529 overloaded", "API Error: 529 Overloaded. This is a server-side issue, usually temporary — try again in a moment.", true},
		{"522 cloudflare", `API Error: 522 {"type":"https://developers.cloudflare.com/support/"}`, true},
		{"connection closed", "API Error: Connection closed mid-response. The response above may be incomplete.", true},
		{"leading whitespace", "\n  API Error: 529 Overloaded.", true},

		// The reason this is a prefix match and not a substring one. An
		// assistant discussing an outage is not an outage — a transcript of
		// the change that added this check contains several such lines, and
		// a substring match would have called every one of them terminal.
		{"assistant discussing an error", "The run died because `API Error: 529 Overloaded` left a dead session.", false},
		{"error quoted mid-sentence", "Nothing in the tree greps for `API Error`, `Overloaded` or `529`.", false},

		{"ordinary output", "Wrote development/planning/prd/index.md", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsTerminalAPIError(tc.text))
		})
	}
}

func TestAPIErrorGraceWindow(t *testing.T) {
	t.Parallel()

	t.Run("defaults to the grace constant", func(t *testing.T) {
		t.Parallel()
		d := NewDriver(nil, time.Hour)
		require.Equal(t, DefaultAPIErrorGrace, d.apiErrorGraceWindow())
	})

	// A caller who sets a 60s idle timeout has said the whole step is
	// decidable in 60s; a 3-minute grace inside that could never fire.
	t.Run("never exceeds the idle window", func(t *testing.T) {
		t.Parallel()
		d := NewDriver(nil, 30*time.Second)
		require.Equal(t, 30*time.Second, d.apiErrorGraceWindow())
	})

	t.Run("disabled when grace is zero", func(t *testing.T) {
		t.Parallel()
		d := NewDriver(nil, time.Hour)
		d.apiErrorGrace = 0
		require.Greater(t, d.apiErrorGraceWindow(), 24*time.Hour, "must never fire")
	})

	t.Run("disabled when no reader is wired", func(t *testing.T) {
		t.Parallel()
		d := NewDriver(nil, time.Hour)
		d.lastAssistantText = nil
		require.Greater(t, d.apiErrorGraceWindow(), 24*time.Hour)
	})
}

func TestTerminalAPIError_ReadsAndMemoizes(t *testing.T) {
	t.Parallel()

	newDriver := func(text string, reads *int) *Driver {
		d := NewDriver(nil, time.Hour)
		d.activeTranscript = "/does/not/need/to/exist.jsonl"
		d.lastAssistantText = func(string) (string, bool) {
			*reads++
			return text, true
		}
		return d
	}

	t.Run("reports the message verbatim", func(t *testing.T) {
		t.Parallel()
		reads := 0
		d := newDriver("API Error: 529 Overloaded. Try again.", &reads)
		msg, ok := d.terminalAPIError(10, time.Unix(1, 0))
		require.True(t, ok)
		require.Equal(t, "API Error: 529 Overloaded. Try again.", msg,
			"the caller needs the upstream text, not a paraphrase")
	})

	t.Run("healthy output is not an error", func(t *testing.T) {
		t.Parallel()
		reads := 0
		d := newDriver("Wrote the PRD.", &reads)
		_, ok := d.terminalAPIError(10, time.Unix(1, 0))
		require.False(t, ok)
	})

	// LastAssistantText scans a whole multi-megabyte file; the poll loop
	// must not re-read an unchanged one every tick for the length of a stall.
	t.Run("unchanged transcript is read once", func(t *testing.T) {
		t.Parallel()
		reads := 0
		d := newDriver("API Error: 529 Overloaded.", &reads)
		sig := time.Unix(1, 0)
		for range 5 {
			_, ok := d.terminalAPIError(10, sig)
			require.True(t, ok)
		}
		require.Equal(t, 1, reads, "memoized on the transcript signature")
	})

	// ...but a transcript that grew must be re-read: that growth is exactly
	// how a recovering session proves it is alive.
	t.Run("changed transcript is re-read", func(t *testing.T) {
		t.Parallel()
		reads := 0
		d := newDriver("API Error: 529 Overloaded.", &reads)
		_, _ = d.terminalAPIError(10, time.Unix(1, 0))
		_, _ = d.terminalAPIError(20, time.Unix(2, 0))
		require.Equal(t, 2, reads)
	})

	t.Run("no active transcript is not an error", func(t *testing.T) {
		t.Parallel()
		reads := 0
		d := newDriver("API Error: 529 Overloaded.", &reads)
		d.activeTranscript = ""
		_, ok := d.terminalAPIError(10, time.Unix(1, 0))
		require.False(t, ok)
		require.Zero(t, reads)
	})
}

func TestTerminalAPIError_Message(t *testing.T) {
	t.Parallel()
	err := &TerminalAPIError{
		Label:   "interactive step",
		Message: "API Error: 529 Overloaded. This is a server-side issue.",
		Quiet:   3 * time.Minute,
	}
	require.Contains(t, err.Error(), "interactive step")
	require.Contains(t, err.Error(), "3m0s")
	require.Contains(t, err.Error(), "529 Overloaded",
		"the upstream text has to survive into the error a caller reads")
}

// --- through WaitStepDone, where the grace window has to do its job ---

// A dead session: the transcript's last word is an upstream error and
// nothing follows. Today this waits out the idle ceiling and reports
// "nothing happened"; it should fail immediately with the reason.
func TestWaitStepDone_TerminalAPIErrorFailsFast(t *testing.T) {
	t.Parallel()
	// pollInterval floors at 1s and scales with the idle window, so the
	// window has to be short enough to tick quickly while still being far
	// enough away that the idle backstop cannot be what fires.
	const idle = 20 * time.Second // far enough that only the grace can fire
	d := newTestDriver(idle)
	d.apiErrorGrace = 40 * time.Millisecond
	d.activeTranscript = filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(d.activeTranscript, []byte("{}\n"), 0o600))
	d.lastAssistantText = func(string) (string, bool) {
		return "API Error: 529 Overloaded. This is a server-side issue.", true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := d.WaitStepDone(ctx)
	elapsed := time.Since(start)

	var tae *TerminalAPIError
	require.ErrorAs(t, err, &tae, "want TerminalAPIError, got %v", err)
	require.Contains(t, tae.Message, "529 Overloaded", "upstream text carried verbatim")
	// The property is "decided long before the idle ceiling", not a
	// particular latency — a loaded CI runner polls slower than a laptop,
	// and pinning an absolute number would make this flaky rather than
	// strict. The gap here is 20s of headroom against a ~1s poll.
	require.Less(t, elapsed, idle/2, "must not wait out the idle ceiling")
}

// The case the local corpus says is the COMMON one: a session hits a 529
// and carries on. Every such error found on disk had its next transcript
// entry at the same timestamp, and none was the transcript's last line —
// so failing on sight of the message would kill healthy runs. Transcript
// growth resets the quiet window and the check must never fire.
func TestWaitStepDone_RecoveringSessionIsNotKilled(t *testing.T) {
	t.Parallel()
	d := newTestDriver(4 * time.Second)
	d.apiErrorGrace = 40 * time.Millisecond
	d.activeTranscript = filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(d.activeTranscript, []byte("{}\n"), 0o600))
	// The message never stops looking like an error; the session's own
	// liveness is what has to save it.
	d.lastAssistantText = func(string) (string, bool) {
		return "API Error: 529 Overloaded. This is a server-side issue.", true
	}

	stop := make(chan struct{})
	appendEvery(t, d.activeTranscript, 200*time.Millisecond, stop)
	defer close(stop)

	// Long enough to span several poll ticks — a budget shorter than one
	// tick would pass without the check ever running.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := d.WaitStepDone(ctx)

	var tae *TerminalAPIError
	require.NotErrorAs(t, err, &tae,
		"a session still writing its transcript must never be reported as dead: %v", err)
	require.ErrorIs(t, err, context.DeadlineExceeded, "should have run to the test deadline")
}

// With the check off, the old behaviour stands: wait out the idle window.
func TestWaitStepDone_APIErrorCheckDisabled(t *testing.T) {
	t.Parallel()
	d := newTestDriver(2 * time.Second)
	d.apiErrorGrace = 0 // off
	d.activeTranscript = filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(d.activeTranscript, []byte("{}\n"), 0o600))
	d.lastAssistantText = func(string) (string, bool) {
		return "API Error: 529 Overloaded.", true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var ite *IdleTimeoutError
	require.ErrorAs(t, d.WaitStepDone(ctx), &ite, "should fall through to the idle backstop")
}
