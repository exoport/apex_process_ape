package sandbox

import "testing"

func TestForwardArgvUsesTheDeliveredApe(t *testing.T) {
	argv, err := ForwardArgv(8080)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ApeBinDest + "/ape", "sandbox-connect", "8080"}
	if len(argv) != len(want) {
		t.Fatalf("ForwardArgv = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("ForwardArgv = %v, want %v", argv, want)
		}
	}
}

// The argv is built from a VALIDATED port by the process with the authority to
// run it, so a forward request can never become arbitrary in-guest execution.
func TestForwardArgvRejectsANonPort(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 1 << 20} {
		if argv, err := ForwardArgv(port); err == nil {
			t.Errorf("ForwardArgv(%d) = %v, want an error", port, argv)
		}
	}
}
