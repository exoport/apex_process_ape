package apecmd

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/vmmclient"
	"github.com/exoport/apex_process_ape/internal/vmmstream"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Sandbox-wide connection flags (persistent on the parent). ape sandbox is a
// thin aped client (PLAN-18): every verb speaks the ape.vmm.<node>.> contract
// over NATS. The daemonless runner path (PLAN-16) was retired — aped owns
// composition, egress, and the workspace registry server-side.
var (
	sandboxNode      string
	sandboxNatsURL   string
	sandboxNatsCreds string
)

// errNoAped is returned when no aped endpoint is configured.
//
//nolint:revive,staticcheck // deliberately multi-line operator guidance
var errNoAped = errors.New(`ape sandbox requires an aped endpoint.
Set APE_NATS_URL (and APE_NATS_CREDS) to your aped node, or pass --nats-url/--nats-creds,
and select the node with --node (env APE_APED_NODE; default: hostname).
The daemonless runner path was retired in PLAN-18 — ape is always an aped client.
Stand up aped with the units in deploy/systemd (see docs/how-to/run-aped.md).`)

// newSandboxCmd is the parent of the workspace-lifecycle verbs. A sandbox
// workspace is a long-lived, hardware-isolated Kata microVM aped provisions per
// project and you work inside across many sessions (PLAN-16 mechanics, PLAN-18
// control plane).
func newSandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Provision and operate hardware-isolated Kata VM workspaces (via aped)",
		Long: `Provision and operate long-lived, hardware-isolated Kata microVM
workspaces (own guest kernel, KVM) through a rootful aped daemon.

ape drives aped over embedded NATS using the ape.vmm.<node>.> contract; aped
provisions the microVM, composes the workspace home, mints a per-VM telemetry
credential, and owns the workspace registry. ape never runs as root.

  ape sandbox up <name>      Provision a workspace
  ape sandbox ls             List provisioned workspaces
  ape sandbox inspect <name> Show a workspace's live state
  ape sandbox exec <name> -- <cmd>...   Run a command inside a workspace
  ape sandbox freeze <name>    Freeze a workspace (cgroup-freeze; RAM resident)
  ape sandbox unfreeze <name>  Unfreeze a frozen workspace
  ape sandbox suspend <name>   Suspend a workspace microVM — not yet on Kata
  ape sandbox down <name>      Tear a workspace down
  ape sandbox framework …      Materialize the framework refs a node can mount

Point ape at your aped node with APE_NATS_URL + APE_NATS_CREDS (the operator
credential aped mints at startup) and --node. Requires a running aped on a
Linux host with KVM + containerd + Kata.`,
	}
	pf := cmd.PersistentFlags()
	pf.StringVar(&sandboxNode, "node", "", "aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname)")
	pf.StringVar(&sandboxNatsURL, "nats-url", "", "aped management NATS URL (env APE_NATS_URL)")
	pf.StringVar(&sandboxNatsCreds, "nats-creds", "", "operator .creds for aped (env APE_NATS_CREDS)")

	cmd.AddCommand(
		newSandboxUpCmd(),
		newSandboxLsCmd(),
		newSandboxInspectCmd(),
		newSandboxAttachCmd(),
		newSandboxSSHCmd(),
		newSandboxExecCmd(),
		newSandboxFreezeCmd(),
		newSandboxUnfreezeCmd(),
		newSandboxSuspendCmd(),
		newSandboxDownCmd(),
		newSandboxFrameworkCmd(),
		newSandboxProxyDaemonCmd(),
	)
	return cmd
}

// dialVMM builds the ape.vmm client for the configured node and returns it along
// with the underlying NATS connection (the interactive attach/exec path drives
// the session subjects on it directly) and a closer that drains the connection.
// It returns errNoAped when no endpoint is configured.
func dialVMM(cmd *cobra.Command) (*vmmclient.Client, *nats.Conn, func(), error) {
	node := sandboxNode
	if node == "" {
		node = os.Getenv("APE_APED_NODE")
	}
	if node == "" {
		node, _ = os.Hostname()
	}
	cfg := natsconn.Resolve(sandboxNatsURL, sandboxNatsCreds)
	if !cfg.Enabled() {
		return nil, nil, nil, errNoAped
	}
	nc, err := natsconn.Connect(cmd.Context(), cfg, "ape-sandbox/"+Version)
	if err != nil {
		return nil, nil, nil, err
	}
	return vmmclient.New(nc, natsconn.SubjectToken(node), 0), nc, func() { _ = nc.Drain() }, nil
}

// vmmBackend builds the ape.vmm NATS client for the configured node, or returns
// errNoAped when no endpoint is set. The returned closer drains the connection.
func vmmBackend(cmd *cobra.Command) (workspace.Backend, func(), error) {
	client, _, done, err := dialVMM(cmd)
	if err != nil {
		return nil, nil, err
	}
	return client, done, nil
}

// streamAttach runs the client half of an interactive session: it wires local
// stdio to the session subjects (raw terminal + SIGWINCH-driven resize when tty
// and stdin is a real terminal) and returns the process exit code. The server
// gated its output until we prime, so no early output is lost.
func streamAttach(cmd *cobra.Command, nc *nats.Conn, prefix string, tty bool) (int, error) {
	inFd := int(os.Stdin.Fd())
	var resize <-chan vmmstream.WinSize
	if tty && term.IsTerminal(inFd) {
		if old, err := term.MakeRaw(inFd); err == nil {
			defer func() { _ = term.Restore(inFd, old) }()
		}
		resize = watchWinsize(cmd.Context(), inFd)
	}
	return vmmstream.Attach(cmd.Context(), nc, prefix, vmmstream.ClientStreams{
		Stdin:  os.Stdin,
		Stdout: cmd.OutOrStdout(),
		Stderr: cmd.ErrOrStderr(),
		Resize: resize,
	}, 0)
}

func newSandboxUpCmd() *cobra.Command {
	var (
		profileName  string
		cwdFlag      string
		image        string
		runtime      string
		mount        string
		mountFlags   []string
		configPath   string
		noConfig     bool
		frameworkRef string
		egressDomain []string
	)
	cmd := &cobra.Command{
		Use:   "up <name>",
		Short: "Provision a Kata workspace",
		Long: `Provision a long-lived Kata workspace named <name> on the target aped node.

aped resolves the profile, composes a per-workspace ~/.claude, mints a per-VM
telemetry credential, and starts the detached microVM. For a host-fs mount the
project at --cwd is sent as the mount source; aped canonicalizes it and
re-checks it against its policy mount-root allow-list before binding it.

A committed .apesandbox.yaml at the project root describes the rest of the
workspace — the repos to mount (each at /workspace/<name>, one flagged main),
extra mounts, and the egress domains to request. --mount flags merge on top of it
(CLI wins by destination). Everything there is a REQUEST: aped re-checks every
source against its mount roots, refuses reserved destinations, and intersects the
egress domains with its own policy, so a project can narrow what a node permits
but never widen it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot(cwdFlag)
			if err != nil {
				return err
			}

			req := workspace.CreateRequest{
				Name:         args[0],
				Image:        image,
				Runtime:      runtime,
				Mount:        mount,
				Profile:      profileName,
				FrameworkRef: frameworkRef,
			}
			if req.Mount == "" || req.Mount == "host-fs" {
				req.MountSource = root
			}
			if err := applySandboxConfig(cmd, &req, root, sandboxConfigOptions{
				Path: configPath, Disabled: noConfig, MountFlags: mountFlags, EgressDomains: egressDomain,
			}); err != nil {
				return err
			}

			backend, done, err := vmmBackend(cmd)
			if err != nil {
				return err
			}
			defer done()

			ws, err := backend.Create(cmd.Context(), req)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace %q up (%s, %s, mount=%s)\n", ws.Name, ws.Image, ws.Runtime, ws.Mount)
			fmt.Fprintf(cmd.OutOrStdout(), "exec: ape sandbox exec %s -- <cmd>\n", ws.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "Profile name aped resolves (default: derived from the request)")
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root to mount for host-fs (default: current working directory)")
	cmd.Flags().StringVar(&image, "image", "", "Image ref override (default: aped's pinned image)")
	cmd.Flags().StringVar(&runtime, "runtime", "", "Runtime handler: kata-qemu | kata-clh")
	cmd.Flags().StringVar(&mount, "mount", "", "Mount mode: host-fs | volume | ephemeral (default: host-fs)")
	cmd.Flags().StringArrayVar(&mountFlags, "mount-path", nil, "Extra mount <source>[:<dest>][:ro|:rw] (repeatable; ro by default; merges with .apesandbox.yaml)")
	cmd.Flags().StringVar(&configPath, "sandbox-config", "", "Path to a non-default .apesandbox.yaml")
	cmd.Flags().BoolVar(&noConfig, "no-sandbox-config", false, "Ignore any .apesandbox.yaml in the project")
	cmd.Flags().StringVar(&frameworkRef, "framework-ref", "", "APEX framework ref to mount read-only (must be materialized on the node)")
	cmd.Flags().StringArrayVar(&egressDomain, "egress-domain", nil, "Request an egress domain (repeatable; still gated by the node's policy)")
	return cmd
}

// sandboxConfigOptions carries the descriptor/flag inputs for one `up`.
type sandboxConfigOptions struct {
	Path          string
	Disabled      bool
	MountFlags    []string
	EgressDomains []string
}

// applySandboxConfig folds the project descriptor and the CLI mount/egress flags
// into the create request (PLAN-20 D2/D3).
//
// Resolution happens here, client-side, because only the client can resolve a
// path relative to the project (and aped, running with ProtectHome=yes, must never
// be handed a path it cannot canonicalize). The merge order is documented:
// descriptor first, then CLI, later winning by destination.
func applySandboxConfig(cmd *cobra.Command, req *workspace.CreateRequest, projectRoot string, opts sandboxConfigOptions) error {
	var desc *sandbox.Descriptor
	switch {
	case opts.Disabled:
		// --no-sandbox-config: an explicit path with it is contradictory.
		if opts.Path != "" {
			return errors.New("--sandbox-config and --no-sandbox-config are mutually exclusive")
		}
	case opts.Path != "":
		d, err := sandbox.LoadDescriptor(opts.Path)
		if err != nil {
			return err
		}
		desc = d
	default:
		if path, ok := sandbox.FindDescriptor(projectRoot); ok {
			d, err := sandbox.LoadDescriptor(path)
			if err != nil {
				return err
			}
			desc = d
		}
	}

	var fileMounts []workspace.MountSpec
	if desc != nil {
		resolved, err := desc.Resolve(projectRoot)
		if err != nil {
			return err
		}
		req.Repos = resolved.Repos
		fileMounts = resolved.Mounts
		req.Egress = resolved.Egress
		// A descriptor with repos supersedes --cwd as the project source: the main repo
		// is the project, and aped derives ProjectRoot from it.
		for i := range resolved.Repos {
			if resolved.Repos[i].Main {
				req.MountSource = resolved.Repos[i].Source
			}
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "using %s (%d repo(s), %d mount(s))\n",
			desc.Path(), len(resolved.Repos), len(fileMounts))
	}

	cliMounts := make([]workspace.MountSpec, 0, len(opts.MountFlags))
	for _, raw := range opts.MountFlags {
		m, err := sandbox.ParseMountFlag(projectRoot, raw)
		if err != nil {
			return err
		}
		cliMounts = append(cliMounts, m)
	}
	merged, err := sandbox.MergeUserMounts(fileMounts, cliMounts)
	if err != nil {
		return err
	}
	req.Mounts = merged

	if len(opts.EgressDomains) > 0 {
		domains := opts.EgressDomains
		if req.Egress != nil {
			domains = append(append([]string(nil), req.Egress.AuthorizedDomains...), domains...)
		}
		req.Egress = &workspace.EgressRequest{AuthorizedDomains: sandbox.SortedDomains(domains)}
	}
	return nil
}

func newSandboxLsCmd() *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List provisioned workspaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			backend, done, err := vmmBackend(cmd)
			if err != nil {
				return err
			}
			defer done()
			list, err := backend.List(cmd.Context())
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
				fmt.Fprintln(tw, "NAME\tPROFILE\tRUNTIME\tMOUNT\tIMAGE")
				for i := range list {
					w := &list[i]
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", w.Name, w.Profile, w.Runtime, w.Mount, w.Image)
				}
				return tw.Flush()
			}
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newSandboxInspectCmd() *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:   "inspect <name>",
		Short: "Show a workspace's live state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backend, done, err := vmmBackend(cmd)
			if err != nil {
				return err
			}
			defer done()
			st, err := backend.Inspect(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format == output.FormatJSON || format == output.FormatYAML {
				return output.Print(cmd.OutOrStdout(), format, st)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", st.Name, st.State)
			return nil
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newSandboxAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <name>",
		Short: "Open an interactive shell inside a workspace",
		Long: `Open an interactive shell inside a workspace, wiring your terminal's
stdin/stdout/stderr to the guest over the aped exec session subjects (PLAN-18 D2,
credit-based flow control; the terminal goes raw and resizes forward on SIGWINCH).

Requires an aped node running the containerd driver (aped run --driver
containerd); a shell-driver node reports the session UNSUPPORTED.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, nc, done, err := dialVMM(cmd)
			if err != nil {
				return err
			}
			defer done()
			open, err := client.AttachOpen(cmd.Context(), args[0], workspace.AttachRequest{TTY: true})
			if err != nil {
				if errors.Is(err, workspace.ErrUnsupported) {
					return fmt.Errorf("interactive attach is not available on this aped node (needs 'aped run --driver containerd'); use 'ape sandbox exec %s -- <cmd>'", args[0])
				}
				return err
			}
			_, err = streamAttach(cmd, nc, open.SubjectPrefix, true)
			return err
		},
	}
}

func newSandboxSSHCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh <name>",
		Short: "SSH into a workspace (Tier-2)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			// Port forwarding is resolved by aped per-VM networking (Phase 2
			// leaves the overlay to Tier-2/Phase-3).
			return fmt.Errorf("ssh access is resolved by aped networking (Tier-2); use 'ape sandbox exec %s -- <cmd>'", args[0])
		},
	}
	return cmd
}

func newSandboxExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <name> -- <cmd>...",
		Short: "Run a command inside a workspace",
		Long: `Run a command inside a workspace, streaming its stdout/stderr back over the
aped exec session subjects and returning its exit code.

On an aped node without an interactive backend (the nerdctl shell driver) it
falls back to a request/reply exec that reports only the exit status (output goes
to the node's logs).`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSandboxExec(cmd, args[0], args[1:])
		},
	}
}

// runSandboxExec streams an exec through the interactive session when the node
// supports it, else falls back to the exit-status-only exec verb.
func runSandboxExec(cmd *cobra.Command, name string, argv []string) error {
	client, nc, done, err := dialVMM(cmd)
	if err != nil {
		return err
	}
	defer done()

	tty := term.IsTerminal(int(os.Stdin.Fd()))
	open, err := client.AttachOpen(cmd.Context(), name, workspace.AttachRequest{Cmd: argv, TTY: tty})
	if err != nil {
		if errors.Is(err, workspace.ErrUnsupported) {
			st, xerr := client.Exec(cmd.Context(), name, workspace.ExecRequest{Cmd: argv})
			if xerr != nil {
				return xerr
			}
			return exitCodeError(st.Code)
		}
		return err
	}
	code, err := streamAttach(cmd, nc, open.SubjectPrefix, tty)
	if err != nil {
		return err
	}
	return exitCodeError(code)
}

// exitCodeError maps a workspace process exit code to a CLI error that carries
// the EXACT code, so `ape sandbox exec` exits with the guest's status (ssh-style)
// rather than a generic 1. nil on 0. main routes it through ExitCode, which also
// suppresses a redundant "Error:" line — the guest already streamed its output.
func exitCodeError(code int) error {
	if code == 0 {
		return nil
	}
	return &exitError{code: code, err: fmt.Errorf("workspace command exited with code %d", code)}
}

func newSandboxFreezeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "freeze <name>",
		Short: "Freeze a workspace (cgroup-freeze; guest RAM stays resident)",
		Long: `Freeze cgroup-freezes the workspace's guest processes: the guest stops
consuming CPU but its RAM stays fully resident, so unfreeze resumes instantly.
This is a freeze, not a VM suspend (see 'ape sandbox suspend').`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backend, done, err := vmmBackend(cmd)
			if err != nil {
				return err
			}
			defer done()
			if err := backend.Freeze(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace %q frozen\n", args[0])
			return nil
		},
	}
}

func newSandboxUnfreezeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unfreeze <name>",
		Short: "Unfreeze a frozen workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backend, done, err := vmmBackend(cmd)
			if err != nil {
				return err
			}
			defer done()
			if err := backend.Unfreeze(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace %q unfrozen\n", args[0])
			return nil
		},
	}
}

// newSandboxSuspendCmd is the distinct verb for a real VM suspend (save guest
// RAM to disk), kept separate from freeze (PLAN-18 D7). It is not reachable
// through Kata-via-containerd today, so aped returns UNSUPPORTED.
func newSandboxSuspendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "suspend <name>",
		Short: "Suspend a workspace microVM (save guest RAM to disk) — not yet supported on Kata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backend, done, err := vmmBackend(cmd)
			if err != nil {
				return err
			}
			defer done()
			return backend.Suspend(cmd.Context(), args[0])
		},
	}
}

func newSandboxDownCmd() *cobra.Command {
	var (
		force        bool
		removeVolume bool
	)
	cmd := &cobra.Command{
		Use:   "down <name>",
		Short: "Tear a workspace down",
		Long: `Destroy the workspace microVM and drop its aped registry entry. A
persistent volume (mount: volume) is retained unless --remove-volume is set.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backend, done, err := vmmBackend(cmd)
			if err != nil {
				return err
			}
			defer done()
			if err := backend.Destroy(cmd.Context(), args[0], workspace.DestroyRequest{Force: force, RemoveVolume: removeVolume}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace %q down\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Force teardown")
	cmd.Flags().BoolVar(&removeVolume, "remove-volume", false, "Also remove the persistent volume (mount: volume)")
	return cmd
}
