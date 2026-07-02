package repl

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// trustModalPane reproduces the claude-code folder-trust modal shape:
// the menu item carries the ❯ glyph that false-triggered the
// pre-hardening bare-glyph ready check.
const trustModalPane = ` Do you trust the files in this folder?

 /tmp/some-project

 Is this a project you created or one you trust?

 ❯ 1. Yes, I trust this folder
   2. No, exit`

// readyFooterPane is a minimal real-REPL pane: the
// bypass-permissions footer ape always gets from
// --dangerously-skip-permissions.
const readyFooterPane = `╭──────────────────────────╮
│ ❯ Try "fix lint errors"  │
╰──────────────────────────╯
  ⏵⏵ bypass permissions on (shift+tab to cycle)`

// fakePane swaps the WaitForReady seams for a scripted pane. frames
// are returned in order; the last frame repeats. Enter keystrokes are
// counted. Restores the real seams on test cleanup.
type fakePane struct {
	mu     sync.Mutex
	frames []string
	calls  int
	enters int
}

func installFakePane(t *testing.T, frames []string) *fakePane {
	t.Helper()
	f := &fakePane{frames: frames}
	capturePaneFn = func(_ context.Context, _ string) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		i := f.calls
		if i >= len(f.frames) {
			i = len(f.frames) - 1
		}
		f.calls++
		return f.frames[i], nil
	}
	sendEnterFn = func(_ context.Context, _ string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.enters++
		return nil
	}
	t.Cleanup(func() {
		capturePaneFn = CapturePane
		sendEnterFn = SendEnter
	})
	return f
}

// TestWaitForReadyDismissesTrustModal drives the trust-then-ready
// sequence: the pane shows the trust modal for two polls, then the
// real footer. dismissBlockingModals must fire and WaitForReady must
// return nil.
func TestWaitForReadyDismissesTrustModal(t *testing.T) {
	f := installFakePane(t, []string{trustModalPane, trustModalPane, readyFooterPane})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := WaitForReady(ctx, "fake"); err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	// Dismissal is a BARE Enter — no "1" selection keystroke that could
	// leak as a UserPromptSubmit into the step-contract window.
	if f.enters == 0 {
		t.Fatalf("expected dismissal to press Enter")
	}
}

// TestMenuGlyphIsNotReady is the regression guard for the exact bug:
// a modal menu item containing the ❯ glyph must not satisfy replReady.
func TestMenuGlyphIsNotReady(t *testing.T) {
	if replReady("❯ 1. Yes, I trust this folder") {
		t.Fatalf("menu item with glyph must not be ready")
	}
	if replReady(trustModalPane) {
		t.Fatalf("trust modal pane must not be ready")
	}
}

// TestReplReadySignals pins the two positive ready markers.
func TestReplReadySignals(t *testing.T) {
	if !replReady(readyFooterPane) {
		t.Fatalf("bypass-permissions footer must signal ready")
	}
	if !replReady("some output\n❯\nmore") {
		t.Fatalf("empty prompt line must signal ready")
	}
	// snapshot() trims trailing spaces, but tolerate an untrimmed one.
	if !replReady("❯ ") {
		t.Fatalf("empty prompt line with trailing space must signal ready")
	}
}

// TestWaitForReadyUnknownModalTimesOut asserts the fail-fast contract:
// an unrecognized blocking screen times out at ctx expiry and the
// error carries the last pane snapshot for diagnosis.
func TestWaitForReadyUnknownModalTimesOut(t *testing.T) {
	const unknownPane = ` Welcome to Claude Code vNext!
 ❯ 1. Pick a theme
   2. Skip`
	installFakePane(t, []string{unknownPane})

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	err := WaitForReady(ctx, "fake")
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	var nre *NotReadyError
	if !errors.As(err, &nre) {
		t.Fatalf("expected *NotReadyError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "Pick a theme") {
		t.Fatalf("error must include the last pane snapshot, got:\n%v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("NotReadyError must unwrap to the ctx error, got: %v", errors.Unwrap(err))
	}
}

// TestDismissBlockingModalsActsAtMostOnce verifies the at-most-one
// contract and the no-match fast path.
func TestDismissBlockingModalsActsAtMostOnce(t *testing.T) {
	f := installFakePane(t, []string{trustModalPane})

	handled, err := dismissBlockingModals(context.Background(), "fake", trustModalPane)
	if err != nil || !handled {
		t.Fatalf("expected handled=true, err=nil; got %v, %v", handled, err)
	}
	handled, err = dismissBlockingModals(context.Background(), "fake", "plain output")
	if err != nil || handled {
		t.Fatalf("expected handled=false on non-modal pane; got %v, %v", handled, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.enters != 1 {
		t.Fatalf("expected exactly one dismissal Enter, got %d", f.enters)
	}
}
