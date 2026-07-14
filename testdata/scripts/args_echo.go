package main

import (
	"context"
	"fmt"

	"github.com/exoport/apex_process_ape/apescript"
)

// Main echoes each script arg on its own numbered line so a test can assert
// the exact args survived the `--` split.
func Main(ctx context.Context) error {
	for i, a := range apescript.Args() {
		fmt.Printf("arg[%d]=%s\n", i, a)
	}
	return nil
}
