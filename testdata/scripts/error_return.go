package main

import (
	"context"
	"errors"
)

// Main returns a non-nil error so the harness can prove a returned error maps
// to exit 1 with the message printed.
func Main(ctx context.Context) error {
	return errors.New("deliberate script failure")
}
