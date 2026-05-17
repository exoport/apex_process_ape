package apecmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/diegosz/apex_process_ape/internal/updatecache"
	"github.com/spf13/cobra"
)

const (
	cmdUseList   = "list"
	keyConflicts = "conflicts"
)

var rootCmd = &cobra.Command{
	Use:   "ape",
	Short: "APE — APEX Process Engine CLI",
	Long: `APE is the APEX Process Engine CLI tool.
It manages governance artifacts, traits, patterns, and ADRs for your project.`,
	PersistentPreRun: func(_ *cobra.Command, _ []string) {
		go checkForUpdatesBackground()
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddCommand(
		newVersionCmd(),
		newBootstrapCmd(),
		newTraitCmd(),
		newPatternCmd(),
		newADRCmd(),
		newSyncCmd(),
		newUpdateCmd(),
		newRollbackCmd(),
		newPipelineCmd(),
		newFrameworkCmd(),
		newChatCmd(),
		newMCPBridgeCmd(),
		newNotifyCmd(),
	)
}

func checkForUpdatesBackground() {
	// GITHUB_TOKEN is optional now that the repo is public. When set,
	// requests use the 5000/h authenticated rate limit; without it,
	// they use the 60/h unauthenticated limit (per IP), which is
	// plenty for a once-cached background check.
	token := os.Getenv("GITHUB_TOKEN")

	entry := updatecache.Load()
	if entry != nil {
		if isNewerVersion(Version, entry.LatestVersion) {
			fmt.Fprintf(os.Stderr, "update available: %s → run 'ape update'\n", entry.LatestVersion)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:mnd // 5 seconds is an arbitrary background-check timeout; short enough not to delay startup
	defer cancel()

	latest, err := fetchLatestVersion(ctx, token)
	if err != nil {
		return
	}

	updatecache.Save(latest)

	if isNewerVersion(Version, latest) {
		fmt.Fprintf(os.Stderr, "update available: %s → run 'ape update'\n", latest)
	}
}
