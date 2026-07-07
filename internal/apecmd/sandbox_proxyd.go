package apecmd

import (
	"github.com/diegosz/apex_process_ape/internal/sandbox"
	"github.com/spf13/cobra"
)

// newSandboxProxyDaemonCmd is the hidden worker `ape sandbox up` re-execs to
// run the persistent host-side CONNECT egress proxy for a workspace
// (PLAN-16 D4). It is not user-facing: `up` starts it detached and records
// its pid/addr in the workspace registry; `down` stops it. Hidden so it
// stays out of `ape --help` and the generated CLI reference.
func newSandboxProxyDaemonCmd() *cobra.Command {
	var (
		workspace string
		listen    string
		audit     string
		allow     []string
		readyFD   int
	)
	cmd := &cobra.Command{
		Use:    "_proxyd",
		Short:  "(internal) run a workspace's detached egress proxy",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sandbox.RunProxyDaemon(cmd.Context(), sandbox.DaemonOptions{
				Workspace: workspace,
				Listen:    listen,
				AuditLog:  audit,
				Allow:     allow,
				ReadyFD:   readyFD,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&workspace, "workspace", "", "Workspace name (used as the audit job tag)")
	f.StringVar(&listen, "listen", "127.0.0.1:0", "Loopback address to listen on")
	f.StringVar(&audit, "audit", "", "Path to the egress-audit.jsonl trail")
	f.StringArrayVar(&allow, "allow", nil, "Authorized egress domain, exact or *.suffix (repeatable)")
	f.IntVar(&readyFD, "ready-fd", 0, "Inherited fd to report the bound address on (0: none)")
	return cmd
}
