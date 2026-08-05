package apecmd

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestForwardPorts(t *testing.T) {
	for name, tc := range map[string]struct {
		args      []string
		ssh       bool
		wantLocal int
		wantGuest int
		wantErr   bool
	}{
		// The common case: one port means the same port both ends.
		"single port":       {args: []string{"dev", "8080"}, wantLocal: 8080, wantGuest: 8080},
		"local:guest":       {args: []string{"dev", "3000:8080"}, wantLocal: 3000, wantGuest: 8080},
		"ssh defaults":      {args: []string{"dev"}, ssh: true, wantLocal: defaultSSHLocalPort, wantGuest: 22},
		"ssh with a local":  {args: []string{"dev", "2022"}, ssh: true, wantLocal: 2022, wantGuest: 22},
		"explicit wins":     {args: []string{"dev", "2022:2222"}, ssh: true, wantLocal: 2022, wantGuest: 2222},
		"no port, no ssh":   {args: []string{"dev"}, wantErr: true},
		"bad local":         {args: []string{"dev", "http"}, wantErr: true},
		"bad guest":         {args: []string{"dev", "8080:http"}, wantErr: true},
		"port zero":         {args: []string{"dev", "0"}, wantErr: true},
		"port out of range": {args: []string{"dev", "70000"}, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			local, guest, err := forwardPorts(tc.args, tc.ssh)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("forwardPorts(%v, ssh=%v) = %d, %d, nil; want an error", tc.args, tc.ssh, local, guest)
				}
				return
			}
			if err != nil {
				t.Fatalf("forwardPorts(%v, ssh=%v): %v", tc.args, tc.ssh, err)
			}
			if local != tc.wantLocal || guest != tc.wantGuest {
				t.Errorf("forwardPorts(%v, ssh=%v) = %d → %d; want %d → %d",
					tc.args, tc.ssh, local, guest, tc.wantLocal, tc.wantGuest)
			}
		})
	}
}

// The guest end of a forward: dial the workspace's own loopback and shuttle
// bytes, verbatim, in both directions.
func TestPipeToGuestPortShuttlesBytes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer c.Close()
		// A server that reads to EOF and then answers — the shape that breaks if the
		// guest end does not half-close when its stdin ends.
		body, _ := io.ReadAll(c)
		_, _ = c.Write(append([]byte("echo:"), body...))
	}()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)

	var out bytes.Buffer
	cmd := &cobra.Command{}
	// Cobra sets the context during Execute; a command built directly for a unit
	// test has none, and the dialer would panic on it.
	cmd.SetContext(t.Context())
	cmd.SetIn(strings.NewReader("hello\r\n\x00\xff"))
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)

	if err := pipeToGuestPort(cmd, port); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "echo:hello\r\n\x00\xff"; got != want {
		t.Errorf("round trip = %q, want %q", got, want)
	}
}

// "Nothing is listening" is the most common forward failure and it must say so,
// naming the address it tried — not hang, and not report a generic dial error.
func TestPipeToGuestPortReportsNothingListening(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	// Port 1 on loopback: reliably refused.
	err := pipeToGuestPort(cmd, 1)
	if err == nil {
		t.Fatal("dialing a closed port succeeded")
	}
	if !strings.Contains(err.Error(), "nothing is listening") || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("error does not name the problem or the address: %v", err)
	}
}

func TestSandboxConnectRejectsANonPort(t *testing.T) {
	for _, arg := range []string{"0", "70000", "http", "-1"} {
		cmd := newSandboxConnectCmd()
		cmd.SetArgs([]string{arg})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil {
			t.Errorf("sandbox-connect %q was accepted", arg)
		}
	}
}

// Both guest-side commands are machinery aped runs, not verbs a human types.
func TestGuestSideCommandsAreHidden(t *testing.T) {
	if !newSandboxConnectCmd().Hidden {
		t.Error("sandbox-connect should be hidden")
	}
}
