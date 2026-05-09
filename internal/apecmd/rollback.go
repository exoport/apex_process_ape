package apecmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback",
		Short: "Rollback ape to the previous version",
		Long:  "Restore the backup binary created during the last update.",
		RunE: func(_ *cobra.Command, _ []string) error {
			exePath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("cannot determine executable path: %w", err)
			}

			backupPath := exePath + ".bak"
			if _, err := os.Stat(backupPath); os.IsNotExist(err) {
				return fmt.Errorf("no backup found at %s", backupPath)
			}

			if err := os.Rename(backupPath, exePath); err != nil {
				return fmt.Errorf("cannot restore backup: %w", err)
			}

			fmt.Printf("Rolled back: restored %s from %s\n", exePath, backupPath)
			return nil
		},
	}
}
