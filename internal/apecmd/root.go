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
	Long: `ape runs APEX framework work against your project through an
interactive Claude Code REPL.

Common commands:
  ape pipeline <name>   Run a multi-stage pipeline (design, governance, epics).
  ape task <skill>      Run a single framework skill without a pipeline YAML.
  ape chat              Open an interactive Claude session in the project.
  ape costs             Show this project's Claude cost rollup.

Also: framework setup/update, doctor, sessions, planning, trait/pattern/adr
inspection. Every claude invocation runs in an in-process PTY — there is no
"claude -p" programmatic path.`,
	PersistentPreRun: func(cmd *cobra.Command, _ []string) {
		// Skip the background update check for hidden / utility commands
		// (mcp-bridge, notify) — they run inside the spawned claude on
		// hot paths where a network check is noise, not user-facing.
		// PLAN-9 F3.
		if cmd.Hidden {
			return
		}
		go checkForUpdatesBackground()
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	// Set here (not in init) so Version reflects the build-info backfill
	// applied by version.go's init — cobra reads rootCmd.Version at
	// Execute time when handling `--version`. A non-empty Version makes
	// cobra register the `--version` flag automatically.
	rootCmd.Version = Version
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
		newTaskCmd(),
		newPlanningCmd(),
		newFrameworkCmd(),
		newChatCmd(),
		newMCPBridgeCmd(),
		newNotifyCmd(),
		newSessionsCmd(),
		newCostsCmd(),
		newDoctorCmd(),
		newGenDocsCmd(),
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
