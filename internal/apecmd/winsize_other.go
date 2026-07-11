//go:build windows

package apecmd

import (
	"context"

	"github.com/exoport/apex_process_ape/internal/vmmstream"
)

// watchWinsize is a no-op where SIGWINCH is unavailable (Windows): the session
// keeps its initial terminal size. Returning nil disables resize forwarding.
func watchWinsize(context.Context, int) <-chan vmmstream.WinSize { return nil }
