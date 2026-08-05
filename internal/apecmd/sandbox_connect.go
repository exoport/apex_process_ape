package apecmd

import (
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// `ape sandbox-connect` — the guest end of a port-forward (PLAN-24 D2).
//
// aped execs this INSIDE a workspace with a port it validated, and pipes its
// stdio over the same NATS session subjects that carry exec and attach. It dials
// 127.0.0.1:<port> in the guest's own loopback — the one destination the
// workspace's ruleset accepts unconditionally (`iifname "lo" accept`) — and
// shuttles bytes.
//
// # Why this rather than publishing a port
//
// The nerdctl driver can publish `-p 127.0.0.1:N:22`, and it is the wrong tool
// here. The containerd driver aped actually uses builds an OCI spec plus a
// pre-wired netns directly, and that netns's `input` chain is `policy drop`: a
// published port would need the wall relaxed, which is exactly the posture
// PLAN-21 built and live-validated. This costs the wall nothing — no listener
// exists on the workspace, and nothing inbound is ever required.
//
// # Why it is a subcommand of the delivered ape
//
// Same reason as the agent: PLAN-23 already mounts a verified, version-matched
// `ape` into every workspace. Depending on `nc` or `socat` being in the image
// would add an image requirement, a version to track, and a silent failure mode
// on any image that lacks it.

// connectDialTimeout bounds the guest-local dial. Loopback inside the guest, so
// a second is generous: the interesting failure is "nothing is listening", which
// is instant, and reporting it fast is what lets the operator see the real
// error instead of a hang.
const connectDialTimeout = 5 * time.Second

func newSandboxConnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "sandbox-connect <port>",
		Short:  "Pipe stdio to a TCP port on this workspace's loopback (runs inside a workspace)",
		Hidden: true,
		Long: `Connect to 127.0.0.1:<port> inside this workspace and pipe stdin/stdout to
it. aped runs this inside a workspace to serve 'ape sandbox forward'; you do not
normally run it yourself.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[0])
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("ape sandbox-connect: %q is not a TCP port (1..65535)", args[0])
			}
			return pipeToGuestPort(cmd, port)
		},
	}
}

// pipeToGuestPort dials the guest-local port and copies both directions until
// either side closes.
//
// Errors go to STDERR, never stdout: stdout is the forwarded byte stream, and a
// diagnostic written into it would be delivered to the operator's TCP client as
// data — corrupting whatever protocol is riding the tunnel in a way that looks
// like a bug in the forwarded application.
func pipeToGuestPort(cmd *cobra.Command, port int) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	dialer := net.Dialer{Timeout: connectDialTimeout}
	conn, err := dialer.DialContext(cmd.Context(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("ape sandbox-connect: nothing is listening on %s inside this workspace: %w", addr, err)
	}
	defer conn.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	// stdin → guest service. On EOF (the operator's client hung up) half-close so
	// the service sees the end of the request rather than waiting on a socket that
	// will never speak again — the difference between a clean HTTP response and a
	// hang for anything that reads to EOF.
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, cmd.InOrStdin())
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	// guest service → stdout.
	go func() {
		defer wg.Done()
		_, _ = io.Copy(cmd.OutOrStdout(), conn)
		// Closing stdout ends the session on the host side. os.Stdout is what the
		// exec's cio actually holds; a test's buffer is not closable and does not
		// need to be.
		if c, ok := cmd.OutOrStdout().(*os.File); ok {
			_ = c.Close()
		}
	}()
	wg.Wait()
	return nil
}
