package apecmd

import (
	"fmt"
	"path/filepath"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/spf13/cobra"
)

// Workspace lifecycle + toolchain verbs (PLAN-22 D3/D5a).
//
// stop/start were already implemented end to end — driver, Backend, the ape.vmm
// contract, and vmmclient — with only the CLI verb missing, so this is exposure,
// not new machinery. They matter because they are the only way to reclaim RAM
// while KEEPING a workspace: freeze holds the guest's RAM resident (and does not
// survive a reboot), whereas stop kills the task and keeps the container +
// snapshot, so state survives both a stop and a host reboot.

func newSandboxStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a workspace (free its RAM, keep its rootfs + state)",
		Long: `Stop a workspace: kill the guest task while keeping the container and its
snapshot, so 'ape sandbox start' revives it with its filesystem intact.

Choosing between the three:
  freeze  cgroup-freeze — RAM stays RESIDENT, instant unfreeze, lost on reboot
  stop    task killed  — RAM FREED, rootfs + state kept, survives a reboot
  down    destroyed    — rootfs deleted (a 'volume' mount survives unless
                        --remove-volume)

Toolchain and dependency state lives in durable cache mounts, so a stopped —
or even a destroyed — workspace loses nothing that a warm cache can restore.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backend, done, err := vmmBackend(cmd)
			if err != nil {
				return err
			}
			defer done()
			if err := backend.Stop(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace %q stopped (RAM freed; state kept — ape sandbox start %s)\n", args[0], args[0])
			return nil
		},
	}
}

func newSandboxStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Start a stopped workspace",
		Long: `Start a stopped workspace from its retained container + snapshot. A workspace
that is already running is left alone.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backend, done, err := vmmBackend(cmd)
			if err != nil {
				return err
			}
			defer done()
			if err := backend.Start(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace %q started\n", args[0])
			return nil
		},
	}
}

// newSandboxSetupCmd runs the in-guest toolchain install step (PLAN-22 D3).
func newSandboxSetupCmd() *cobra.Command {
	var (
		cwdFlag    string
		configPath string
		dryRun     bool
	)
	cmd := &cobra.Command{
		Use:   "setup <name>",
		Short: "Materialize the project's declared toolchain inside a workspace",
		Long: `Run the project's toolchain install step inside a running workspace: 'asdf
install' for the runtime versions the project declares, then 'bingo get' for its
pinned Go tools.

The toolchain comes from the .apesandbox.yaml toolchain: section, which should
reference the native files (.tool-versions, .bingo/) rather than duplicate the
versions. The step is idempotent and becomes a no-op — fully offline — once the
durable tool caches are warm; the FIRST run needs the workspace to have egress
(the registries it downloads from must be in its allowlist).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot(cwdFlag)
			if err != nil {
				return err
			}
			desc, err := loadDescriptorForSetup(root, configPath)
			if err != nil {
				return err
			}
			if desc == nil || desc.Toolchain == nil {
				return fmt.Errorf("no toolchain: section in %s — nothing to set up "+
					"(declare tool_versions / bingo / tools)", sandbox.DescriptorPath(root))
			}

			// The script runs in the MAIN repo's guest path: that is where the project's
			// .tool-versions and .bingo live.
			repoDir, err := mainRepoGuestDir(desc, root)
			if err != nil {
				return err
			}
			script, err := sandbox.ToolchainSetupScript(desc.Toolchain, repoDir)
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Fprint(cmd.OutOrStdout(), script)
				return nil
			}
			return runSandboxExec(cmd, args[0], []string{"/bin/sh", "-c", script})
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root holding the descriptor (default: current working directory)")
	cmd.Flags().StringVar(&configPath, "sandbox-config", "", "Path to a non-default .apesandbox.yaml")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the setup script instead of running it in the workspace")
	return cmd
}

// loadDescriptorForSetup loads the descriptor from an explicit path or the project
// root, returning nil when the project has none.
func loadDescriptorForSetup(projectRoot, explicit string) (*sandbox.Descriptor, error) {
	if explicit != "" {
		return sandbox.LoadDescriptor(explicit)
	}
	path, ok := sandbox.FindDescriptor(projectRoot)
	if !ok {
		return nil, nil //nolint:nilnil // "this project has no descriptor" is not an error
	}
	return sandbox.LoadDescriptor(path)
}

// mainRepoGuestDir returns the guest path of the descriptor's main repo — the
// directory the setup step runs in. It mirrors the resolver's naming so the client
// and the daemon agree on where a repo landed.
func mainRepoGuestDir(desc *sandbox.Descriptor, projectRoot string) (string, error) {
	resolved, err := desc.Resolve(projectRoot)
	if err != nil {
		return "", err
	}
	for _, r := range resolved.Repos {
		if r.Main {
			return sandbox.RepoDest(r.Name), nil
		}
	}
	// No repos declared: the workspace has the single repo aped derived from --cwd.
	return sandbox.RepoDest(filepath.Base(filepath.Clean(projectRoot))), nil
}
