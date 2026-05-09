package main

import (
	"os"

	"github.com/diegosz/apex_process_ape/internal/apecmd"
)

func main() {
	if err := apecmd.Execute(); err != nil {
		os.Exit(1)
	}
}
