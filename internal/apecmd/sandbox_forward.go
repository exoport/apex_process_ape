package apecmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/exoport/apex_process_ape/internal/vmmclient"
	"github.com/exoport/apex_process_ape/internal/vmmstream"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
)

// `ape sandbox forward` — look at what you are building (PLAN-24 D2).
//
// Until this existed a workspace had ZERO inbound reachability, from anywhere,
// including its own host: the per-netns ruleset is `policy drop` on input,
// output AND forward, and the host bridge drops forwarding both ways. exec and
// attach work only because they ride containerd's shim channel rather than the
// network. So there was no way to open a browser on a dev server running inside
// a workspace.
//
// This changes none of that. There is still no listener on the workspace and no
// hole in either wall: the guest end DIALS OUT to 127.0.0.1 inside its own
// loopback, and the bytes ride the authenticated, audited session transport aped
// already runs.
//
// What it gives is deliberately narrow: THE OPERATOR gets to a port, over a
// channel aped authenticates. It is not a public URL and it is not
// workspace-to-workspace — both of those need machinery this does not have, and
// are parked rather than half-built.

// forwardGuestSSHPort is the guest port --ssh forwards. sshd already runs inside
// the workspace and ~/.ssh is already composed with a pinned known_hosts, so the
// guest half of an ssh (and therefore VS Code Remote) workflow has existed for
// some time with no way to reach it.
const forwardGuestSSHPort = 22

func newSandboxForwardCmd() *cobra.Command {
	var (
		useSSH   bool
		bindAddr string
	)
	cmd := &cobra.Command{
		Use:   "forward <name> <local>[:<guest>]",
		Short: "Forward a local port to a port inside a workspace",
		Long: `Forward a port on THIS machine to a TCP port inside a workspace, so you can
open a browser (or point a client) at something running in there.

  ape sandbox forward dev 8080          # localhost:8080 → workspace :8080
  ape sandbox forward dev 3000:8080     # localhost:3000 → workspace :8080
  ape sandbox forward dev --ssh         # localhost:2222 → workspace :22

Nothing is exposed. The workspace gets no listener and neither firewall changes:
the guest end dials its OWN loopback and the bytes ride the same authenticated
session transport as exec and attach, which aped audits. The forward is private
to you, it is not a public URL, and it does not connect workspaces to each other.

Forwards live with this command, not with the workspace: Ctrl-C ends them and
the workspace is untouched. Run several at once in separate terminals, or the
same command twice for two ports.

--ssh gives you an ssh (and therefore VS Code Remote) target. The image ships
sshd and aped composes ~/.ssh, but nothing starts sshd for you, so start it once
per workspace first:

  ape sandbox exec dev -- sh -c 'mkdir -p /run/sshd && /usr/sbin/sshd'
  ape sandbox forward dev --ssh &
  ssh -p 2222 root@127.0.0.1`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			local, guest, err := forwardPorts(args, useSSH)
			if err != nil {
				return err
			}
			return runSandboxForward(cmd, args[0], bindAddr, local, guest)
		},
	}
	cmd.Flags().BoolVar(&useSSH, "ssh", false, "Forward the workspace's sshd (guest :22, local :2222 unless a local port is given)")
	// Loopback by default and configurable rather than fixed: a forward is private
	// to this machine, and binding 0.0.0.0 would republish a workspace's port to
	// the local network — which is the one thing this deliberately does not do
	// unless someone asks for it explicitly.
	cmd.Flags().StringVar(&bindAddr, "bind", "127.0.0.1", "Local address to listen on (0.0.0.0 exposes the forward to your network)")
	return cmd
}

// defaultSSHLocalPort is the local port --ssh uses when none is given. 2222
// rather than 22 because binding 22 needs root and would collide with the host's
// own sshd.
const defaultSSHLocalPort = 2222

// forwardPorts resolves the local and guest ports from the arguments and --ssh.
func forwardPorts(args []string, useSSH bool) (local, guest int, err error) {
	if len(args) == 1 {
		if !useSSH {
			return 0, 0, errors.New("give a port to forward (e.g. `ape sandbox forward <name> 8080`) or pass --ssh")
		}
		return defaultSSHLocalPort, forwardGuestSSHPort, nil
	}

	spec := args[1]
	localStr, guestStr, split := strings.Cut(spec, ":")
	local, err = parsePort(localStr)
	if err != nil {
		return 0, 0, fmt.Errorf("local port: %w", err)
	}
	switch {
	case split:
		if guest, err = parsePort(guestStr); err != nil {
			return 0, 0, fmt.Errorf("workspace port: %w", err)
		}
	case useSSH:
		guest = forwardGuestSSHPort
	default:
		guest = local // the common case: same port both ends
	}
	return local, guest, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("%q is not a TCP port (1..65535)", s)
	}
	return p, nil
}

// runSandboxForward listens locally and opens one forward session per accepted
// connection.
//
// One session per connection, not one per command: each carries an independent
// byte stream with its own lifetime, and multiplexing several TCP conversations
// down one session would mean inventing a framing layer on top of a transport
// that already has one. Sessions are cheap — the same thing an exec costs.
func runSandboxForward(cmd *cobra.Command, name, bindAddr string, local, guest int) error {
	client, nc, done, err := dialVMM(cmd)
	if err != nil {
		return err
	}
	defer done()

	listenAddr := net.JoinHostPort(bindAddr, strconv.Itoa(local))
	var lc net.ListenConfig
	ln, err := lc.Listen(cmd.Context(), "tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", listenAddr, err)
	}
	defer ln.Close()

	// Probe the workspace before announcing a forward that cannot work: a
	// listening socket that refuses every connection is a worse experience than a
	// clear error here, and the failure modes (no such workspace, stopped, a node
	// that does not support forwarding) all have different fixes.
	if err := checkForwardable(cmd.Context(), client, name); err != nil {
		return err
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "▶ forwarding %s → %s:%d (workspace %q) — Ctrl-C to stop\n",
		listenAddr, "127.0.0.1", guest, name)
	if bindAddr != "127.0.0.1" && bindAddr != "localhost" {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"! bound to %s: this forward is reachable from your network, not just this machine\n", bindAddr)
	}

	// Close the listener when the command's context ends, so Ctrl-C unblocks the
	// Accept below instead of waiting for one more connection.
	go func() { <-cmd.Context().Done(); _ = ln.Close() }()

	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, aerr := ln.Accept()
		if aerr != nil {
			if cmd.Context().Err() != nil {
				return nil // Ctrl-C: a clean stop, not a failure
			}
			return fmt.Errorf("accept on %s: %w", listenAddr, aerr)
		}
		wg.Go(func() { serveForwardConn(cmd, client, nc, name, guest, conn) })
	}
}

// checkForwardable reports why a forward cannot be opened, in the caller's
// terms.
func checkForwardable(ctx context.Context, client *vmmclient.Client, name string) error {
	st, err := client.Inspect(ctx, name)
	if err != nil {
		return err
	}
	if st.State != workspace.StateRunning {
		return fmt.Errorf("workspace %q is %s — start it first: ape sandbox start %s", name, st.State, name)
	}
	return nil
}

// serveForwardConn runs one connection's forward session to completion.
//
// A failure on one connection is reported and dropped, never fatal: a browser
// that opened six connections and had one refused should see one broken request,
// not a forward that tore itself down.
func serveForwardConn(cmd *cobra.Command, client *vmmclient.Client, nc *nats.Conn, name string, guest int, conn net.Conn) {
	open, err := client.ForwardOpen(cmd.Context(), name, guest)
	if err != nil {
		_ = conn.Close()
		if errors.Is(err, workspace.ErrUnsupported) {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"! port-forwarding is not available on this aped node (it needs 'aped run --driver containerd')\n")
			return
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "! forward to %s:%d failed: %v\n", name, guest, err)
		return
	}
	if _, err := vmmstream.Forward(cmd.Context(), nc, open.SubjectPrefix, vmmstream.ForwardStreams{
		Local: conn,
		// The guest end's stderr — a "nothing is listening on :8080" is the common
		// one, and it is the whole answer to "why did my browser get nothing?".
		Diag: cmd.ErrOrStderr(),
	}, 0); err != nil && cmd.Context().Err() == nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "! forward session ended: %v\n", err)
	}
	// The guest end reports an absent target on its own diagnostic channel, which
	// is already printed. For :22 specifically, add what to do about it: the image
	// ships sshd but nothing starts it, so "nothing is listening" is the expected
	// first experience rather than a fault.
	if guest == forwardGuestSSHPort {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"  (if nothing is listening: sshd ships in the image but is not started — "+
				"ape sandbox exec %s -- sh -c 'mkdir -p /run/sshd && /usr/sbin/sshd')\n", name)
	}
}
