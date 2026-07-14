package main

import (
	"context"

	"github.com/exoport/apex_process_ape/apescript"
)

// Main writes two Log lines. With --quiet neither should appear; without it
// both should reach the log writer.
func Main(ctx context.Context) error {
	apescript.Log("first log line")
	apescript.Log("second log line value=%d", 42)
	return nil
}
