//go:build !windows

package apecmd

import "testing"

func TestExitCodeError(t *testing.T) {
	if err := exitCodeError(0); err != nil {
		t.Errorf("exitCodeError(0) = %v, want nil (success)", err)
	}
	err := exitCodeError(3)
	if err == nil {
		t.Fatal("exitCodeError(3) = nil, want a non-nil error so ape exits non-zero")
	}
	// The exact guest code must propagate as ape's exit status (not a generic 1),
	// and the error is silent — the guest already streamed its own output.
	code, silent := ExitCode(err)
	if code != 3 {
		t.Errorf("ExitCode = %d, want 3 (the exact guest code)", code)
	}
	if !silent {
		t.Error("ExitCode silent = false, want true (no redundant Error: line)")
	}
	if c, s := ExitCode(nil); c != 0 || s {
		t.Errorf("ExitCode(nil) = (%d,%v), want (0,false)", c, s)
	}
	if c, s := ExitCode(errNoAped); c != ExitRunFailed || s {
		t.Errorf("ExitCode(generic) = (%d,%v), want (%d,false)", c, s, ExitRunFailed)
	}
}

func TestClampUint16(t *testing.T) {
	for _, c := range []struct {
		in   int
		want uint16
	}{
		{-5, 0}, {0, 0}, {80, 80}, {24, 24}, {65535, 65535}, {70000, 65535},
	} {
		if got := clampUint16(c.in); got != c.want {
			t.Errorf("clampUint16(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
