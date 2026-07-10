// Package apedcmd defines the cobra commands for the `aped` binary — the
// rootful Kata-QEMU VM-management daemon (PLAN-18 Phase 2). aped is a separate
// binary from `ape` (LOCKED 8: `ape` stays dependency-light; `aped` carries the
// heavier VM-management deps), split into two subcommands that map to the
// two-process architecture (D1):
//
//	aped run    the network-less root executor (privileged)
//	aped front  the de-privileged NATS surface + vmm micro service
//
// See deploy/systemd for the hardened units that run these (Appendix A).
package apedcmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// NewRootCmd builds the `aped` root command.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "aped",
		Short: "Rootful Kata-QEMU VM-management daemon (PLAN-18)",
		Long: `aped is the only rootful component of the ape platform: a narrow, audited
VM-management daemon that drives Kata-QEMU microVM workspaces. It runs as two
processes joined by a typed AF_UNIX command boundary (PLAN-18 D1):

  aped run    the network-less root executor — serves /run/aped/priv.sock,
              SO_PEERCRED-gated, re-validates every command against policy, and
              drives containerd/nerdctl. Holds no network address family.
  aped front  the de-privileged NATS surface — embeds nats-server (HOST_OPS +
              TELEMETRY accounts), runs the vmm micro service on
              ape.vmm.<node>.>, resolves requests, mints per-VM creds, and
              forwards typed commands to the executor.

Requires Linux with KVM + containerd + Kata. Deploy with the hardened systemd
units in deploy/systemd (Appendix A of PLAN-18).`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newRunCmd(), newFrontCmd())
	return root
}

// Execute runs the root command with a signal-cancelled context (SIGINT/SIGTERM
// trigger a graceful drain in both subcommands).
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := NewRootCmd().ExecuteContext(ctx)
	stop() // release the signal handler before any os.Exit
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(exitCodeFor(err))
	}
}
