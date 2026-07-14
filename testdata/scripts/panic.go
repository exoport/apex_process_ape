package main

import (
	"context"
)

// Main panics so the harness can prove panics are recovered and reported with
// the yaegi source-position stack rather than crashing the ape process.
func Main(ctx context.Context) error {
	boom()
	return nil
}

func boom() {
	panic("deliberate script panic")
}
