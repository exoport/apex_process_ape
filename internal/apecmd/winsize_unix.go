//go:build !windows

package apecmd

import (
	"context"
	"math"
	"os"
	"os/signal"
	"syscall"

	"github.com/exoport/apex_process_ape/internal/vmmstream"
	"golang.org/x/term"
)

// watchWinsize emits the terminal's current size immediately and again on each
// SIGWINCH, until ctx is done, closing the channel on exit. It backs interactive
// attach/exec resize forwarding; fd is the local terminal fd.
func watchWinsize(ctx context.Context, fd int) <-chan vmmstream.WinSize {
	ch := make(chan vmmstream.WinSize, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	send := func() {
		if w, h, err := term.GetSize(fd); err == nil {
			select {
			case ch <- vmmstream.WinSize{Cols: clampUint16(w), Rows: clampUint16(h)}:
			default:
			}
		}
	}
	go func() {
		defer signal.Stop(sig)
		defer close(ch)
		send() // initial size
		for {
			select {
			case <-sig:
				send()
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

// clampUint16 bounds a terminal dimension into uint16 (defensive — real terminal
// dimensions are far smaller, but term.GetSize returns an int).
func clampUint16(n int) uint16 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxUint16:
		return math.MaxUint16
	default:
		return uint16(n)
	}
}
