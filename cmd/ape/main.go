package main

import (
	"fmt"
	"os"

	"github.com/diegosz/apex_process_ape/internal/apecmd"
)

func main() {
	// rootCmd sets SilenceErrors, so cobra never prints a returned RunE
	// error itself — commands are expected to print their own via
	// os.Exit(specificCode) before returning. This is the last-resort
	// net for anything that instead falls through with a bare `return
	// err`, so a bug like that fails loud instead of silently (exit 1,
	// no message).
	if err := apecmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}
