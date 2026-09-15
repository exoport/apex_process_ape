package main

import (
	"os"

	"github.com/exoport/apex_process_ape/internal/apecmd"
)

func main() {
	// rootCmd sets SilenceErrors, so cobra never prints a returned RunE
	// error itself — commands are expected to print their own via
	// os.Exit(specificCode) before returning. This is the last-resort
	// net for anything that instead falls through with a bare `return
	// err`, so a bug like that fails loud instead of silently (exit 1,
	// no message). ExitCode also lets a command forward a specific status
	// (e.g. `ape sandbox exec` returning the guest's exit code) with defers
	// still running; silent errors already reported their own outcome.
	//
	// Report prints any error its command did not print itself. A
	// successful run exits 0 through the same call.
	if code := apecmd.Report(apecmd.Execute(), os.Stderr); code != 0 {
		os.Exit(code)
	}
}
