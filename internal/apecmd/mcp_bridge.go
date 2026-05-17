package apecmd

import (
	"github.com/diegosz/apex_process_ape/internal/bridge/mcp"
	"github.com/spf13/cobra"
)

// newMCPBridgeCmd is the entry point Claude Code spawns over stdio
// when ape is declared as an MCP server in the inline `--mcp-config`
// blob. It speaks JSON-RPC on stdin/stdout and relays to/from the
// parent ape process over TCP IPC (APE_IPC_PORT). PLAN-5 / C1 + C3.
//
// The subcommand is intentionally minimal and undocumented in
// `ape --help`; users should never invoke it directly. The Hidden:
// true flag keeps it off the top-level help table.
func newMCPBridgeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "mcp-bridge",
		Short:  "(internal) Run the MCP bridge over stdio. Spawned by claude.",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return mcp.Run(cmd.Context(), mcp.Options{})
		},
	}
}
