package repl

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeTerm stands in for a claude session behind the capture/send seams:
// a write is drawn into the pane after drawDelay (never, if negative), and
// every Enter is timestamped.
type fakeTerm struct {
	mu        sync.Mutex
	pane      string
	drawDelay time.Duration
	draw      func(text string) string // what the input box shows for text
	wroteAt   time.Time
	enters    []time.Time
}

func (f *fakeTerm) capture(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pane, nil
}

func (f *fakeTerm) sendText(_ context.Context, _, text string) error {
	f.mu.Lock()
	f.wroteAt = time.Now()
	delay := f.drawDelay
	f.mu.Unlock()
	if delay < 0 {
		return nil
	}
	shown := "❯ " + text
	if f.draw != nil {
		shown = f.draw(text)
	}
	time.AfterFunc(delay, func() {
		f.mu.Lock()
		f.pane += "\n" + shown + "\n"
		f.mu.Unlock()
	})
	return nil
}

func (f *fakeTerm) sendEnter(context.Context, string) error {
	f.mu.Lock()
	f.enters = append(f.enters, time.Now())
	f.mu.Unlock()
	return nil
}

func (f *fakeTerm) firstEnterAfterWrite() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.enters) == 0 {
		return -1
	}
	return f.enters[0].Sub(f.wroteAt)
}

func (f *fakeTerm) enterCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.enters)
}

// install points the package seams at f and shortens the waits, restoring
// everything when the test ends.
func (f *fakeTerm) install(t *testing.T) {
	t.Helper()
	prevCap, prevText, prevEnter := capturePaneFn, sendTextFn, sendEnterFn
	prevRender, prevRenderPoll := typedRenderTimeout, typedRenderPoll
	prevConfirm, prevSubmitPoll := submitConfirmTimeout, submitPoll
	capturePaneFn, sendTextFn, sendEnterFn = f.capture, f.sendText, f.sendEnter
	typedRenderTimeout, typedRenderPoll = 2*time.Second, 10*time.Millisecond
	submitConfirmTimeout, submitPoll = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() {
		capturePaneFn, sendTextFn, sendEnterFn = prevCap, prevText, prevEnter
		typedRenderTimeout, typedRenderPoll = prevRender, prevRenderPoll
		submitConfirmTimeout, submitPoll = prevConfirm, prevSubmitPoll
	})
}

// TestTypeLine_EnterWaitsForTheText is the load case: claude draws the line
// late, and Enter must not go out before it does, or it lands in the same
// read as the text and is taken as a newline inside a paste.
func TestTypeLine_EnterWaitsForTheText(t *testing.T) {
	f := &fakeTerm{drawDelay: 700 * time.Millisecond}
	f.install(t)

	require.NoError(t, SendCommand(context.Background(), "s", "/apex-sprint-sync --autonomous --no-commit"))
	require.GreaterOrEqual(t, f.firstEnterAfterWrite(), 700*time.Millisecond)
	require.Equal(t, 1, f.enterCount())
}

// TestTypeLine_PromptSettleIsAFloor — a line drawn at once still waits the
// old fixed delay, which guards the early-submit failure it was added for.
func TestTypeLine_PromptSettleIsAFloor(t *testing.T) {
	f := &fakeTerm{}
	f.install(t)

	require.NoError(t, SendCommand(context.Background(), "s", "/clear"))
	require.GreaterOrEqual(t, f.firstEnterAfterWrite(), PromptSettle)
}

// TestTypeLine_PlaceholderCounts — past 800 characters claude draws a
// "[Pasted text #N]" placeholder rather than the text.
func TestTypeLine_PlaceholderCounts(t *testing.T) {
	f := &fakeTerm{
		drawDelay: 500 * time.Millisecond,
		draw:      func(string) string { return "❯ [Pasted text #1]" },
	}
	f.install(t)

	shown, err := typeLine(context.Background(), "s", strings.Repeat("word ", 200))
	require.NoError(t, err)
	require.True(t, shown)
	require.GreaterOrEqual(t, time.Since(f.wroteAt), 500*time.Millisecond)
}

// TestTypeLine_TheSameTextAlreadyOnScreen — a later step can type a line
// whose text is still visible from an earlier one. Only a NEW occurrence
// counts, or Enter would go out at the floor regardless.
func TestTypeLine_TheSameTextAlreadyOnScreen(t *testing.T) {
	line := "/apex-dev-story --autonomous --no-commit story 3.2"
	f := &fakeTerm{pane: "● earlier\n❯ " + line + "\n", drawDelay: 600 * time.Millisecond}
	f.install(t)

	require.NoError(t, SendCommand(context.Background(), "s", line))
	require.GreaterOrEqual(t, f.firstEnterAfterWrite(), 600*time.Millisecond)
}

// TestTypeLine_WrappedAcrossRows — a long line wraps, and the wrap breaks
// it mid-word with an indent; the tail is still found.
func TestTypeLine_WrappedAcrossRows(t *testing.T) {
	line := "/apex-correct-course --autonomous " + strings.Repeat("alpha bravo charlie ", 20)
	f := &fakeTerm{
		drawDelay: 50 * time.Millisecond,
		draw: func(text string) string {
			var b strings.Builder
			b.WriteString("❯ ")
			for i, r := range text {
				if i > 0 && i%57 == 0 {
					b.WriteString("\n  ")
				}
				b.WriteRune(r)
			}
			return b.String()
		},
	}
	f.install(t)

	shown, err := typeLine(context.Background(), "s", line)
	require.NoError(t, err)
	require.True(t, shown)
}

// TestTypeLine_NeverDrawnFallsBack — when nothing is drawn, Enter still
// goes out once the render wait expires, and the caller is told.
func TestTypeLine_NeverDrawnFallsBack(t *testing.T) {
	f := &fakeTerm{drawDelay: -1}
	f.install(t)
	typedRenderTimeout = 400 * time.Millisecond

	shown, err := typeLine(context.Background(), "s", "/apex-x --autonomous")
	require.NoError(t, err)
	require.False(t, shown)
	require.GreaterOrEqual(t, time.Since(f.wroteAt), 400*time.Millisecond)
}

// probeAfter returns a SubmitProbe that reports one submit of text once
// the fake has seen n Enters.
func probeAfter(f *fakeTerm, n int, text string) SubmitProbe {
	return func() (uint64, string) {
		if f.enterCount() >= n {
			return 1, text
		}
		return 0, ""
	}
}

func TestDeliver_ConfirmedOnTheFirstEnter(t *testing.T) {
	f := &fakeTerm{}
	f.install(t)

	require.NoError(t, Deliver(context.Background(), "s", "/apex-x --autonomous", probeAfter(f, 1, "/apex-x --autonomous")))
	require.Equal(t, 1, f.enterCount())
}

// TestDeliver_PressesEnterAgain is the recovery: the first Enter was lost,
// the second submits.
func TestDeliver_PressesEnterAgain(t *testing.T) {
	f := &fakeTerm{}
	f.install(t)

	require.NoError(t, Deliver(context.Background(), "s", "/apex-x --autonomous", probeAfter(f, 2, "/apex-x --autonomous")))
	require.Equal(t, 2, f.enterCount())
}

// TestDeliver_NotSubmittedIsTyped — no submit through every Enter is a
// typed error the run record can name, not an idle timeout an hour later.
func TestDeliver_NotSubmittedIsTyped(t *testing.T) {
	f := &fakeTerm{}
	f.install(t)

	err := Deliver(context.Background(), "s", "/apex-x --autonomous", func() (uint64, string) { return 0, "" })
	var nse *NotSubmittedError
	require.ErrorAs(t, err, &nse)
	require.Equal(t, 1+submitRetries, nse.Enters)
	require.Equal(t, 1+submitRetries, f.enterCount())
	require.True(t, nse.Shown)
	require.Equal(t, "❯ /apex-x --autonomous", nse.Input)
	require.Contains(t, nse.Error(), "never submitted")
}

// TestDeliver_AClearSubmitDoesNotCount — ape types /clear between steps,
// and its hook can land after the baseline was taken.
func TestDeliver_AClearSubmitDoesNotCount(t *testing.T) {
	f := &fakeTerm{}
	f.install(t)

	probe := func() (uint64, string) {
		switch n := f.enterCount(); {
		case n >= 2:
			return 2, "/apex-x --autonomous"
		default:
			return 1, "/clear"
		}
	}
	require.NoError(t, Deliver(context.Background(), "s", "/apex-x --autonomous", probe))
	require.Equal(t, 2, f.enterCount(), "the stray /clear submit must not have confirmed the first Enter")
}

func TestDeliver_NilProbePressesEnterOnce(t *testing.T) {
	f := &fakeTerm{}
	f.install(t)

	require.NoError(t, Deliver(context.Background(), "s", "/clear", nil))
	require.Equal(t, 1, f.enterCount())
}

func TestDeliver_CancelledContextStops(t *testing.T) {
	f := &fakeTerm{}
	f.install(t)
	submitConfirmTimeout = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(600*time.Millisecond, cancel)
	err := Deliver(ctx, "s", "/apex-x", func() (uint64, string) { return 0, "" })
	require.ErrorIs(t, err, context.Canceled)
}

func TestFitsTypedLine(t *testing.T) {
	require.True(t, FitsTypedLine(strings.Repeat("a", TypedLineBudget)))
	require.False(t, FitsTypedLine(strings.Repeat("a", TypedLineBudget+1)))
	// Characters, not bytes: 700 two-byte runes still fit.
	require.True(t, FitsTypedLine(strings.Repeat("é", TypedLineBudget)))
}

func TestArgsPointerNamesThePath(t *testing.T) {
	p := ArgsPointer("/abs/run/stages/01-x/step-01-apex-x.args.md")
	require.Contains(t, p, "/abs/run/stages/01-x/step-01-apex-x.args.md")
	require.True(t, FitsTypedLine(p))
}
