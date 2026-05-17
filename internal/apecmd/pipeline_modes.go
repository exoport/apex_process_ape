package apecmd

import (
	"errors"
	"fmt"
	"io"
)

// resolveModeFlags interprets the three pipeline-mode flags and returns
// optOutTUI = true if either `--print` or `--no-tui` was set.
// Multiple flags is an error. `--no-tui` is a deprecated alias for
// `--print`; using it prints a stderr warning. PLAN-5 / C1.
//
// `--tui` is currently inert (default behaviour) — it lands as a
// surface flag so a future release-cycle merge can flip the default
// in one line. The function returns (false, nil) for `--tui` alone.
func resolveModeFlags(tui, print, noTUI bool, stderr io.Writer) (optOutTUI bool, err error) {
	count := 0
	for _, f := range []bool{tui, print, noTUI} {
		if f {
			count++
		}
	}
	if count > 1 {
		return false, errors.New("--tui, --print, and --no-tui are mutually exclusive")
	}
	if noTUI {
		fmt.Fprintln(stderr, "warning: --no-tui is deprecated; use --print instead")
	}
	return print || noTUI, nil
}
