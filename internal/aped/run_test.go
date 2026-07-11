package aped

import (
	"errors"
	"io"
	"testing"

	"github.com/exoport/apex_process_ape/internal/sandbox"
)

// TestBuildDriverSelection covers the --driver opt-in plumbing: the shell driver
// is the default, and an unknown driver fails closed with ErrConfig. The
// containerd path needs a live socket, so it is exercised in live validation,
// not here.
func TestBuildDriverSelection(t *testing.T) {
	reg := sandbox.OpenRegistry(t.TempDir())

	// Default (empty) → shell driver, non-nil provisioner, no error.
	be, prov, closeFn, err := buildDriver(ExecutorRunConfig{StateDir: t.TempDir()}, reg, io.Discard)
	if err != nil {
		t.Fatalf("default driver: %v", err)
	}
	if be == nil || prov == nil || closeFn == nil {
		t.Fatal("default driver returned a nil backend, provisioner, or closer")
	}
	closeFn()

	// Explicit shell.
	be, prov, closeFn, err = buildDriver(ExecutorRunConfig{Driver: DriverShell, StateDir: t.TempDir()}, reg, io.Discard)
	if err != nil || be == nil || prov == nil {
		t.Fatalf("shell driver: err=%v be=%v prov=%v", err, be, prov)
	}
	closeFn()

	// Unknown → ErrConfig (fail closed).
	if _, _, _, err := buildDriver(ExecutorRunConfig{Driver: "bogus"}, reg, io.Discard); !errors.Is(err, ErrConfig) {
		t.Fatalf("unknown driver: got %v, want ErrConfig", err)
	}
}
