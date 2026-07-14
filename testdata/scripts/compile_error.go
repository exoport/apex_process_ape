package main

import (
	"context"
)

// Main has a deliberate syntax error (dangling assignment) so the harness can
// prove yaegi reports file:line and ape exits 1 without spawning claude.
func Main(ctx context.Context) error {
	x :=
	return nil
}
