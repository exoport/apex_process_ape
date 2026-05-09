package apecmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	Version   = "dev"
	BuildDate = "unknown"
	GitCommit = "unknown"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long:  "Print the version, build date, and git commit of the ape binary.",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("ape %s\n", Version)
			fmt.Printf("  build date: %s\n", BuildDate)
			fmt.Printf("  git commit: %s\n", GitCommit)
		},
	}
}
