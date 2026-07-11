//go:build !windows

package apecmd

import "testing"

func TestExitCodeError(t *testing.T) {
	if err := exitCodeError(0); err != nil {
		t.Errorf("exitCodeError(0) = %v, want nil (success)", err)
	}
	if err := exitCodeError(3); err == nil {
		t.Error("exitCodeError(3) = nil, want a non-nil error so ape exits non-zero")
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
