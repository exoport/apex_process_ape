package main

import (
	"context"
	"fmt"

	"github.com/exoport/apex_process_ape/apescript"
)

// Main reads the manifest whose path is the first script arg and prints its
// run id, status, and total cost.
func Main(ctx context.Context) error {
	args := apescript.Args()
	if len(args) == 0 {
		return fmt.Errorf("usage: read_manifest.go -- <run-dir-or-manifest-path>")
	}
	m, err := apescript.ReadManifest(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("run_id=%s status=%s cost=%.2f\n", m.RunID, m.Status, m.Totals.CostUSD)
	return nil
}
