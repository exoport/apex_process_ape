package apecmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/exoport/apex_process_ape/internal/updatecache"
	"github.com/spf13/cobra"
)

const (
	cmdUseList   = "list"
	keyConflicts = "conflicts"
)

// rootShell is ape's root command WITHOUT its subcommands.
//
// Split out so newRootCmd can build a private tree from the same literal
// the process-wide rootCmd is built from — two trees that could drift apart
// would make a test asserting on one prove nothing about the other.
func rootShell() *cobra.Command {
	return &cobra.Command{
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
}

// rootSubcommands constructs one fresh instance of every top-level command.
func rootSubcommands() []*cobra.Command {
	return []*cobra.Command{
		newVersionCmd(),
		newBootstrapCmd(),
		newConfigCmd(),
		newTraitCmd(),
		newPatternCmd(),
		newADRCmd(),
		newFeatureCmd(),
		newCapabilityCmd(),
		newRegistryCmd(),
		newMemoryCmd(),
		newContextCmd(),
		newStoryCmd(),
		newSprintCmd(),
		newReleaseCmd(),
		newDeferredCmd(),
		newDocCmd(),
		newSyncCmd(),
		newUpdateCmd(),
		newRollbackCmd(),
		newPipelineCmd(),
		newTaskCmd(),
		newPromptCmd(),
		newScriptCmd(),
		newPlanningCmd(),
		newFrameworkCmd(),
		newChatCmd(),
		newMCPBridgeCmd(),
		newNotifyCmd(),
		newSessionsCmd(),
		newCostsCmd(),
		newEventCmd(),
		newLogCmd(),
		newMetricsCmd(),
		newTranscriptCmd(),
		newDoctorCmd(),
		newSandboxCmd(),
		// A top-level sibling of `sandbox`, not a child of it: this one runs INSIDE a
		// workspace and is launched by aped, while every `ape sandbox …` verb runs
		// outside and drives aped. Nesting it would put a command that needs a per-VM
		// credential under a group whose flags are all about reaching a node.
		newSandboxAgentCmd(),
		newSandboxConnectCmd(),
		newServiceCmd(),
		newGenDocsCmd(),
		// A whole command tree from a separate module, mounted rather than
		// ported — see aboard.go for what the two hosts share and what
		// distinguishes them.
		newAboardCmd(),
	}
}

// newRootCmd builds a COMPLETE and PRIVATE copy of ape's command tree.
//
// It exists for tests that assert on the surface. `cobra.Command.Find` is
// not read-only — `findNext` writes `commandCalledAs` on every command it
// walks, and `mergePersistentFlags` lazily writes flag state — so a test
// resolving against the process-wide rootCmd races every other test doing
// the same, which is what made `Test(ubuntu-latest)` fail intermittently on
// main. missingCommands serialises its own access with commandTreeMu; a
// test that walks the shared tree without taking that lock defeats it, and
// a helper that is safe only because of its caller's scheduling is a trap.
//
// A private tree is independent BY CONSTRUCTION rather than by scheduling
// luck, and it costs nothing: these constructors only build cobra values.
// It asserts on the same thing the shared tree would, because both come
// from rootShell + rootSubcommands.
func newRootCmd() *cobra.Command {
	root := rootShell()
	root.AddCommand(rootSubcommands()...)
	return root
}

// rootCmd is the process-wide tree Execute runs. Tests must NOT resolve
// against it — see newRootCmd.
var rootCmd = rootShell()

func Execute() error {
	// Set here (not in init) so Version reflects the build-info backfill
	// applied by version.go's init — cobra reads rootCmd.Version at
	// Execute time when handling `--version`. A non-empty Version makes
	// cobra register the `--version` flag automatically.
	rootCmd.Version = Version

	// A signal-cancelled context, so Ctrl-C and SIGTERM reach the command
	// through cmd.Context() instead of killing the process where it stands.
	//
	// The bug that made this necessary: `ape aboard serve` never shut down
	// gracefully. aboard installs its own handler inside its cli.Execute, and
	// the whole point of the mount is that ape does NOT call that — it adds
	// aboard's tree to this one. So the board ran under a context nothing ever
	// cancelled, its shutdown path never ran, and every stopped board left
	// `.aboard/run/instance.json` behind. A stale record is what makes tooling
	// believe a dead board is alive; it cost an afternoon of the VS Code
	// extension showing no "Start the Board" button, misdiagnosed as a crash
	// when it was in fact every ape-hosted board that was ever stopped.
	//
	// At the root rather than on the board subtree because nothing about it is
	// aboard-specific: every long-running command here — chat, pipeline,
	// sandbox exec — was being killed outright rather than asked to stop.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// And the second signal still kills. signal.NotifyContext keeps swallowing
	// signals until stop(), so without this a second impatient Ctrl-C on a
	// command that ignores its context would do nothing at all — trading a
	// process that dies too eagerly for one that cannot be stopped.
	go func() {
		<-ctx.Done()
		stop()
	}()

	return rootCmd.ExecuteContext(ctx)
}

// The subcommands are added in init rather than in rootCmd's own
// initialiser so the ordering that has always held is preserved: package
// variables initialise before every init, and a constructor that reads a
// value another file's init backfills — Version is one — would otherwise
// capture the zero value.
func init() {
	rootCmd.AddCommand(rootSubcommands()...)
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
