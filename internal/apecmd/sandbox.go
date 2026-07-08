package apecmd

import (
	"fmt"
	"os"
	"os/exec"
	"text/tabwriter"
	"time"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/spf13/cobra"
)

// newSandboxCmd is the parent of the workspace-lifecycle verbs. A sandbox
// workspace is a long-lived, hardware-isolated Kata microVM you provision
// per project and work inside across many sessions (PLAN-16, Phase 1 of the
// APEX Process Platform). Kata/KVM is Linux-only; the verbs return a clear
// "requires Linux" error elsewhere.
func newSandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Provision and operate hardware-isolated Kata VM workspaces",
		Long: `Provision and operate long-lived, hardware-isolated Kata microVM
workspaces (own guest kernel, KVM) for local development.

A workspace is a durable VM you attach to and reuse — not an ephemeral
per-command sandbox. It mounts your project (host-fs by default), a
per-workspace composed ~/.claude, and controlled public egress; you SSH /
VS Code Remote in and run Claude Code, APEX pipelines, or Playwright inside.

  ape sandbox up <name>      Provision a workspace from a profile
  ape sandbox ls             List provisioned workspaces
  ape sandbox attach <name>  Open an interactive shell inside a workspace
  ape sandbox ssh <name>     SSH into a workspace over its forwarded port
  ape sandbox exec <name> -- <cmd>...   Run a command inside a workspace
  ape sandbox pause <name>   Suspend a workspace microVM
  ape sandbox resume <name>  Resume a paused workspace
  ape sandbox down <name>    Tear a workspace down

Requires Linux with KVM + containerd + Kata — run 'ape doctor' to check.
Profiles live in _apex/sandbox/<name>.yaml.`,
	}
	cmd.AddCommand(
		newSandboxUpCmd(),
		newSandboxLsCmd(),
		newSandboxAttachCmd(),
		newSandboxSSHCmd(),
		newSandboxExecCmd(),
		newSandboxPauseCmd(),
		newSandboxResumeCmd(),
		newSandboxDownCmd(),
		newSandboxProxyDaemonCmd(),
	)
	return cmd
}

// openRegistry resolves the default state dir and returns its workspace
// registry.
func openRegistry() (*sandbox.Registry, string, error) {
	stateDir, err := sandbox.DefaultStateDir()
	if err != nil {
		return nil, "", err
	}
	return sandbox.OpenRegistry(stateDir), stateDir, nil
}

// lookupWorkspace fetches a registered workspace by name, erroring clearly
// when it isn't provisioned.
func lookupWorkspace(name string) (sandbox.Workspace, error) {
	reg, _, err := openRegistry()
	if err != nil {
		return sandbox.Workspace{}, err
	}
	ws, ok, err := reg.Get(name)
	if err != nil {
		return sandbox.Workspace{}, err
	}
	if !ok {
		return sandbox.Workspace{}, fmt.Errorf("sandbox: workspace %q is not provisioned (run 'ape sandbox up %s')", name, name)
	}
	return ws, nil
}

func newSandboxUpCmd() *cobra.Command {
	var (
		profileName string
		cwdFlag     string
		proxyAddr   string
		sshPort     int
	)
	cmd := &cobra.Command{
		Use:   "up <name>",
		Short: "Provision a Kata workspace from a profile",
		Long: `Provision a long-lived Kata workspace named <name>.

The profile (--profile, default <name>) is loaded from
_apex/sandbox/<profile>.yaml. ape composes a per-workspace ~/.claude
(credentials, curated skills/agents, git), resolves the image (official
ape-sandbox unless the profile overrides it), and starts a detached Kata
container with the project mounted per the profile's mount mode.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			profile := profileName
			if profile == "" {
				profile = name
			}
			projectRoot, err := resolveProjectRoot(cwdFlag)
			if err != nil {
				return err
			}
			prof, err := sandbox.Load(projectRoot, profile)
			if err != nil {
				return err
			}

			reg, stateDir, err := openRegistry()
			if err != nil {
				return err
			}
			if _, ok, _ := reg.Get(name); ok {
				return fmt.Errorf("sandbox: workspace %q already exists (down it first, or pick another name)", name)
			}

			staging := sandbox.StagingDirFor(stateDir, name)
			if err := os.MkdirAll(staging, 0o700); err != nil {
				return fmt.Errorf("sandbox: create staging home: %w", err)
			}
			hostHome, _ := os.UserHomeDir()
			comp, err := sandbox.Compose(sandbox.ComposeOptions{
				Profile:    prof,
				StagingDir: staging,
				HostHome:   hostHome,
			})
			if err != nil {
				return err
			}

			// Wire public egress. --proxy points at an externally-run proxy;
			// otherwise a profile allowlist starts a supervised, detached
			// CONNECT proxy (fail-closed: `up` aborts if it can't start);
			// otherwise the workspace uses the default (open) network.
			var (
				proxyURL   string
				proxyState sandbox.ProxyState
				sup        *sandbox.ProxySupervisor
			)
			switch sandbox.PlanEgress(proxyAddr, prof.Network.AuthorizedDomains) {
			case sandbox.EgressExplicit:
				proxyURL = "http://" + proxyAddr
			case sandbox.EgressManaged:
				sup = &sandbox.ProxySupervisor{}
				st, serr := sup.Start(cmd.Context(), sandbox.ProxyStartOptions{
					Workspace: name,
					Dir:       sandbox.ProxyDirFor(stateDir, name),
					AuditLog:  sandbox.ProxyAuditLogFor(stateDir, name),
					Allow:     prof.Network.AuthorizedDomains,
				})
				if serr != nil {
					return fmt.Errorf("sandbox: start egress proxy (fail-closed): %w", serr)
				}
				proxyState = st
				proxyURL = st.ProxyURL()
				fmt.Fprintf(cmd.OutOrStdout(),
					"egress proxy up on %s (%d authorized domain(s)); audit: %s\n",
					st.Addr, len(prof.Network.AuthorizedDomains), st.AuditLog)
			case sandbox.EgressOpen:
				// No allowlist and no --proxy: default container network
				// (unrestricted public egress). Declare
				// network.authorized_domains to enforce a deny-by-default proxy.
			}

			image := sandbox.ResolveImage(prof)
			spec := sandbox.WorkspaceSpec{
				Name:       name,
				Image:      image,
				VMM:        prof.VMM,
				Mount:      prof.Mount,
				Comp:       comp,
				HTTPSProxy: proxyURL,
				SSHPort:    sshPort,
			}
			switch prof.Mount {
			case sandbox.MountHostFS:
				spec.ProjectRoot = projectRoot
			case sandbox.MountVolume:
				spec.Volume = sandbox.ContainerName(name) + "-workspace"
			case sandbox.MountEphemeral:
				// nothing from the host
			}

			runner := &sandbox.Runner{Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()}
			if err := runner.Provision(cmd.Context(), spec); err != nil {
				// Don't leak the proxy we just started for a container that
				// never came up.
				if sup != nil && proxyState.PID != 0 {
					_ = sup.Stop(proxyState)
				}
				return err
			}

			rec := sandbox.Workspace{
				Name:          name,
				Container:     spec.Container(),
				Profile:       profile,
				Backend:       string(prof.Backend),
				VMM:           string(prof.VMM),
				Image:         image,
				Mount:         string(prof.Mount),
				ProjectRoot:   spec.ProjectRoot,
				Volume:        spec.Volume,
				StagingDir:    staging,
				SSHPort:       sshPort,
				CreatedAt:     time.Now().UTC().Format(time.RFC3339),
				ProxyPID:      proxyState.PID,
				ProxyAddr:     proxyState.Addr,
				ProxyAuditLog: proxyState.AuditLog,
			}
			if err := reg.Put(rec); err != nil {
				// The proxy would be untracked (down can't find it) — stop it
				// so it doesn't leak. The container is left running; report it.
				if sup != nil && proxyState.PID != 0 {
					_ = sup.Stop(proxyState)
				}
				return fmt.Errorf("sandbox: workspace started but registry write failed: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "workspace %q up (%s, %s, mount=%s)\n", name, image, prof.VMM, prof.Mount)
			fmt.Fprintf(cmd.OutOrStdout(), "attach: ape sandbox attach %s\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "Profile name under _apex/sandbox/ (default: the workspace name)")
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root to mount (default: current working directory)")
	cmd.Flags().StringVar(&proxyAddr, "proxy", "", "host:port of a running CONNECT egress proxy to wire as HTTPS_PROXY")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 0, "Host loopback port to forward to the workspace's sshd (0: none)")
	return cmd
}

func newSandboxLsCmd() *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List provisioned workspaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, _, err := openRegistry()
			if err != nil {
				return err
			}
			list, err := reg.List()
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			switch format {
			case output.FormatJSON, output.FormatYAML:
				return output.Print(cmd.OutOrStdout(), format, list)
			default:
				if len(list) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "no workspaces (ape sandbox up <name>)")
					return nil
				}
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tPROFILE\tVMM\tMOUNT\tIMAGE\tCONTAINER")
				for i := range list {
					w := &list[i]
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						w.Name, w.Profile, w.VMM, w.Mount, w.Image, w.Container)
				}
				return tw.Flush()
			}
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newSandboxAttachCmd() *cobra.Command {
	var shell string
	cmd := &cobra.Command{
		Use:   "attach <name>",
		Short: "Open an interactive shell inside a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := lookupWorkspace(args[0])
			if err != nil {
				return err
			}
			runner := &sandbox.Runner{Stdin: cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()}
			return runner.Attach(cmd.Context(), ws.Container, shell)
		},
	}
	cmd.Flags().StringVar(&shell, "shell", sandbox.DefaultShell, "Login shell to open")
	return cmd
}

func newSandboxSSHCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "ssh <name>",
		Short: "SSH into a workspace over its forwarded loopback port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := lookupWorkspace(args[0])
			if err != nil {
				return err
			}
			if ws.SSHPort == 0 {
				return fmt.Errorf("sandbox: workspace %q has no forwarded ssh port (provision with --ssh-port)", ws.Name)
			}
			return runSSH(cmd, sandbox.SSHArgs(user, ws.SSHPort, "", nil))
		},
	}
	cmd.Flags().StringVar(&user, "user", "ape", "SSH user inside the workspace")
	return cmd
}

// runSSH shells out to the host's `ssh` client with the caller's terminal
// wired through, so VS Code Remote / a plain SSH session behaves normally.
func runSSH(cmd *cobra.Command, args []string) error {
	c := exec.CommandContext(cmd.Context(), "ssh", args...)
	c.Stdin = cmd.InOrStdin()
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		return fmt.Errorf("sandbox: ssh: %w", err)
	}
	return nil
}

func newSandboxExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <name> -- <cmd>...",
		Short: "Run a command inside a workspace",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := lookupWorkspace(args[0])
			if err != nil {
				return err
			}
			runner := &sandbox.Runner{Stdin: cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()}
			return runner.Exec(cmd.Context(), ws.Container, true, args[1:])
		},
	}
	return cmd
}

func newSandboxPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <name>",
		Short: "Suspend a workspace microVM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := lookupWorkspace(args[0])
			if err != nil {
				return err
			}
			runner := &sandbox.Runner{Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()}
			if err := runner.Pause(cmd.Context(), ws.Container); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace %q paused\n", ws.Name)
			return nil
		},
	}
}

func newSandboxResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <name>",
		Short: "Resume a paused workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := lookupWorkspace(args[0])
			if err != nil {
				return err
			}
			runner := &sandbox.Runner{Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()}
			if err := runner.Resume(cmd.Context(), ws.Container); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace %q resumed\n", ws.Name)
			return nil
		},
	}
}

func newSandboxDownCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "down <name>",
		Short: "Tear a workspace down",
		Long: `Force-remove the workspace container and drop its registry entry and
composed home. A persistent volume (mount: volume) is left in place — remove
it manually with 'nerdctl volume rm' if you want to discard its data.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			reg, _, err := openRegistry()
			if err != nil {
				return err
			}
			ws, ok, err := reg.Get(name)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("sandbox: workspace %q is not provisioned", name)
			}
			runner := &sandbox.Runner{Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()}
			if err := runner.Down(cmd.Context(), ws.Container); err != nil {
				return err
			}
			// Stop the supervised egress proxy, if any. The audit trail under
			// the proxy dir is left in place as a forensic record.
			if ws.ProxyPID != 0 {
				sup := &sandbox.ProxySupervisor{}
				if err := sup.Stop(sandbox.ProxyState{Workspace: ws.Name, PID: ws.ProxyPID, Addr: ws.ProxyAddr}); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not stop egress proxy (pid %d): %v\n", ws.ProxyPID, err)
				}
			}
			if ws.StagingDir != "" {
				_ = os.RemoveAll(ws.StagingDir)
			}
			if err := reg.Remove(name); err != nil {
				return fmt.Errorf("sandbox: container removed but registry cleanup failed: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace %q down\n", name)
			return nil
		},
	}
	return cmd
}
