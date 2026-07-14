package main

import (
	"context"
	"fmt"
	"time"

	"github.com/exoport/apex_process_ape/apescript"
)

// Main blocks until the context is cancelled (SIGINT) or a generous backstop
// fires, then returns ctx.Err(). Used to prove SIGINT cancels the script ctx.
func Main(ctx context.Context) error {
	apescript.Log("waiting for cancellation")
	select {
	case <-ctx.Done():
		fmt.Println("cancelled")
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timed out waiting for cancellation")
	}
}
