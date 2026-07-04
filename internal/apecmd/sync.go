package apecmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSyncCmd() *cobra.Command {
	var check bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync governance artifacts",
	}

	cmd.PersistentFlags().BoolVar(&check, "check", false, "Check sync status without applying changes")

	cmd.AddCommand(
		newSyncPatternsCmd(),
		newSyncADRsCmd(),
	)

	return cmd
}

func newSyncPatternsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "patterns",
		Short:   "Sync patterns (not yet implemented)",
		Example: "  ape sync patterns",
		Hidden:  true,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println("sync patterns not yet implemented")
		},
	}
}

func newSyncADRsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "adrs",
		Short:   "Sync ADRs (not yet implemented)",
		Example: "  ape sync adrs",
		Hidden:  true,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println("sync adrs not yet implemented")
		},
	}
}
