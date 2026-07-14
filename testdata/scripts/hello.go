package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/exoport/apex_process_ape/apescript"
)

// Main prints a greeting plus the script args, then logs a line. It is the
// acceptance fixture: `ape script testdata/scripts/hello.go -- a b`.
func Main(ctx context.Context) error {
	fmt.Println("hello from ape script")
	fmt.Printf("args: %s\n", strings.Join(apescript.Args(), " "))
	apescript.Log("logged %d arg(s)", len(apescript.Args()))
	return nil
}
