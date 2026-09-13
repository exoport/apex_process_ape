package repl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ptyTailBytes bounds the flight recorder: the last 64 KiB of raw PTY
// output a session produced. claude's whole startup frame, trust dialog
// included, is under 2 KiB, and each repaint after a keystroke is a few
// hundred bytes, so this holds every frame a readiness failure could be
// about with room to spare, at a cost that does not grow with the session.
const ptyTailBytes = 64 << 10

// tailBuffer keeps the most recent ptyTailBytes of a byte stream.
//
// Resilience remedy R2. The claude 2.1.269 trust-dialog failure left a
// pane dump at the moment of failure and nothing else, and the pane was a
// faithful render of a desynchronised emulator — it showed a dialog
// "rendered twice" that claude never rendered. Two confident diagnoses
// came from that picture, and the real cause was only found by spawning
// claude again with a byte capture, which worked because the broken
// version was still installed. The bytes are the evidence a pane cannot
// be: what the child actually sent, independent of how any emulator drew
// it. Kept for every session, so the evidence exists before anyone knows
// it will be needed.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

// write appends p, keeping at most ptyTailBytes. The backing array is
// re-cut only when it doubles, so a steady stream costs one copy per
// ptyTailBytes of output rather than one per read.
func (t *tailBuffer) write(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*ptyTailBytes {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-ptyTailBytes:]...)
	}
}

// bytes returns a copy of the most recent ptyTailBytes.
func (t *tailBuffer) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	start := max(0, len(t.buf)-ptyTailBytes)
	return append([]byte(nil), t.buf[start:]...)
}

// RecentOutput returns the session's last raw PTY bytes — up to 64 KiB,
// escape sequences and all — or nil for an unknown session.
func RecentOutput(name string) []byte {
	s, ok := lookup(name)
	if !ok {
		return nil
	}
	return s.tail.bytes()
}

// recentOutputFn is RecentOutput behind a test seam, as capturePaneFn is.
var recentOutputFn = RecentOutput

// SaveOutput writes the raw PTY bytes carried by a readiness failure to
// path, creating its directory. It reports false, and writes nothing, when
// there were no bytes to save.
func (e *NotReadyError) SaveOutput(path string) (bool, error) {
	if len(e.Output) == 0 {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("repl: save raw PTY output: %w", err)
	}
	if err := os.WriteFile(path, e.Output, 0o644); err != nil { //nolint:gosec // run artifact, same mode as the manifest beside it
		return false, fmt.Errorf("repl: save raw PTY output: %w", err)
	}
	return true, nil
}

// WithSavedOutput saves a readiness failure's raw PTY bytes to path and
// says where in the returned error, so the location travels with the
// message a person reads. Any other error, or an empty path, is returned
// unchanged. The result still unwraps to the *NotReadyError, so exit-code
// mapping is unaffected.
func WithSavedOutput(err error, path string) error {
	nr, ok := errors.AsType[*NotReadyError](err)
	if !ok || path == "" {
		return err
	}
	saved, saveErr := nr.SaveOutput(path)
	switch {
	case saveErr != nil:
		return fmt.Errorf("%w\nraw PTY output could not be saved: %v", err, saveErr) //nolint:errorlint // the save failure is context, not a cause
	case saved:
		return fmt.Errorf("%w\nraw PTY output (the bytes the pane above was drawn from): %s", err, path)
	}
	return err
}
