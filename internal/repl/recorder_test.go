package repl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The recorder keeps the LAST 64 KiB, byte-exact, however the stream
// arrives, and a caller's copy does not alias the buffer.
func TestTailBuffer_KeepsTheMostRecentBytes(t *testing.T) {
	t.Parallel()
	var tb tailBuffer
	var all bytes.Buffer
	for i := range 5000 { // ~250 KiB in uneven chunks, crossing the re-cut repeatedly
		chunk := fmt.Appendf(nil, "%05d:%s|", i, bytes.Repeat([]byte{byte('a' + i%26)}, i%97))
		tb.write(chunk)
		all.Write(chunk)
	}
	got := tb.bytes()
	require.Len(t, got, ptyTailBytes)
	require.Equal(t, all.Bytes()[all.Len()-ptyTailBytes:], got)

	got[0] ^= 0xff
	require.NotEqual(t, got[0], tb.bytes()[0], "bytes returns a copy")

	var small tailBuffer
	small.write([]byte("short"))
	require.Equal(t, []byte("short"), small.bytes(), "under the bound, everything")
}

// A trust walk that cannot finish is the REPL not becoming ready, typed as
// such, with the pane and the raw bytes. It was returned bare, so `ape task`
// exited 1 rather than 3 and the failure carried nothing to diagnose from.
func TestWaitForReady_AFailedWalkIsANotReadyErrorWithTheBytes(t *testing.T) {
	m := &fakeMenu{
		header:  trustHeader,
		options: []string{"No, exit", "Maybe later", "Show security guide"},
		ready:   readyFooterPane,
	}
	installFakeMenu(t, m)
	raw := []byte("\x1b[?2026h\x1b[2G\x1b[38;5;153m❯\x1b[4GNo, exit")
	recentOutputFn = func(string) []byte { return raw }
	t.Cleanup(func() { recentOutputFn = RecentOutput })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := WaitForReady(ctx, "s")

	nr, ok := errors.AsType[*NotReadyError](err)
	require.True(t, ok, "a failed walk must be a *NotReadyError, got %T: %v", err, err)
	require.Contains(t, nr.Err.Error(), "could not reach a trust-granting option")
	require.Contains(t, nr.Pane, "No, exit")
	require.Equal(t, raw, nr.Output)
}

// WithSavedOutput writes the bytes beside the record, names the file in the
// message, and keeps the error a *NotReadyError for exit-code mapping.
func TestWithSavedOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "run", "pty-tail.bin")
	raw := []byte("\x1b[?u\x1b[c raw")

	err := WithSavedOutput(fmt.Errorf("stage \"dev\": %w", &NotReadyError{Name: "s", Err: context.DeadlineExceeded, Output: raw}), path)
	_, still := errors.AsType[*NotReadyError](err)
	require.True(t, still, "wrapping must not hide the type the exit code is mapped from")
	require.Contains(t, err.Error(), path)
	saved, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, raw, saved, "byte-exact")

	noBytes := &NotReadyError{Name: "s", Err: context.DeadlineExceeded}
	require.Equal(t, noBytes, WithSavedOutput(noBytes, filepath.Join(dir, "never.bin")), "nothing to save, nothing said")
	require.NoFileExists(t, filepath.Join(dir, "never.bin"))

	plain := errors.New("not a readiness failure")
	require.Equal(t, plain, WithSavedOutput(plain, path))
}
