package apecmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/repocache"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/spf13/cobra"
)

// Framework delivery for sandbox workspaces (PLAN-20 D5).
//
// The `ape-sandbox` image is public and framework-free, so the private APEX
// framework is supplied at RUNTIME as a read-only mount instead of a baked layer:
// no consumer needs a registry credential, and the framework can be updated
// without rebuilding an image.
//
// The credentialed step stays on the HOST, with the developer's own git access:
// `ape sandbox framework materialize <ref>` copies one pinned ref out of a local
// framework checkout into the node's framework root, one self-contained directory
// per ref. aped then mounts <root>/<ref> read-only at /opt/apex-framework and
// errors clearly when the ref is absent. It never fetches — the daemon holds no
// credentials, and a workspace must be buildable offline.
//
// Why a CLONE and not `git worktree`: the materialized tree must (a) be
// self-contained, because a worktree's .git file points back at the source repo's
// gitdir, which is not mounted into the guest, and (b) sit on a local branch named
// `main`, which `ape framework setup` requires — and a worktree cannot check out a
// branch the primary repo already has. A local clone satisfies both.

// DefaultFrameworkRoot is where materialized framework refs live by default. It
// matches deploy/dev-host.sh (which creates it user-owned) and the aped front's
// --framework-root.
const DefaultFrameworkRoot = "/srv/apex-framework"

// DefaultGovernanceRoot is the same for the governance repo (ape v0.7.0):
// aped mounts <root>/<ref> read-only at /opt/apex-governance.
const DefaultGovernanceRoot = "/srv/apex-governance"

// mountedRepo is one repo a node materializes refs of: the framework, or
// the governance repo. Both are delivered the same way.
type mountedRepo struct {
	kind        string // "framework" | "governance"
	defaultRoot string
	rootEnv     string
	dest        string
	// resolveRepo finds the local clone refs are materialized from.
	resolveRepo func(flagValue string) (string, error)
	repoHelp    string
}

var (
	frameworkMountedRepo = mountedRepo{
		kind: "framework", defaultRoot: DefaultFrameworkRoot, rootEnv: "APE_FRAMEWORK_ROOT",
		dest: sandbox.FrameworkDest, resolveRepo: resolveFrameworkCheckout,
		repoHelp: "Local apex_process_framework checkout (default: $APEX_FRAMEWORK_REPO, else ape's own clone)",
	}
	governanceMountedRepo = mountedRepo{
		kind: "governance", defaultRoot: DefaultGovernanceRoot, rootEnv: "APE_GOVERNANCE_ROOT",
		dest: sandbox.GovernanceDest, resolveRepo: resolveGovernanceCheckout,
		repoHelp: "Local governance checkout (default: $APEX_GOVERNANCE_REPO, else the project's governance_repository_path or ape's own clone)",
	}
)

// root resolves the repo's materialization root: flag, env, then the default.
func (m mountedRepo) root(flagValue string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(m.rootEnv)); v != "" {
		return v
	}
	return m.defaultRoot
}

func newSandboxFrameworkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "framework",
		Short: "Manage the APEX framework refs a sandbox node can mount",
		Long: `Manage the materialized APEX framework refs on this host.

A sandbox workspace gets the framework as a READ-ONLY mount at /opt/apex-framework
rather than a baked image layer, so the public ape-sandbox image stays
framework-free and credential-free. This command is the host-side, credentialed
half: it copies one pinned ref out of your local framework checkout into the
node's framework root.

  ape sandbox framework materialize v0.3.1
  ape sandbox framework ls
  ape sandbox up dev --framework-ref v0.3.1

aped never fetches the framework itself: if a requested ref is not materialized,
'ape sandbox up' fails with the command to run. Inside the workspace, consume it
with 'ape framework setup --from-worktree --no-commit --no-fetch --repo /opt/apex-framework'.`,
	}
	cmd.AddCommand(newSandboxMaterializeCmd(frameworkMountedRepo), newSandboxMaterializedLsCmd(frameworkMountedRepo))
	return cmd
}

func newSandboxGovernanceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "governance",
		Short: "Manage the governance repo refs a sandbox node can mount",
		Long: `Manage the materialized governance repo refs on this host — the same
delivery as 'ape sandbox framework', for the canonical governance repo the
reconciliation skills read.

A workspace gets it as a READ-ONLY mount at /opt/apex-governance, with
$APEX_GOVERNANCE_REPO pointing there (set by aped, only when it mounts one).

  ape sandbox governance materialize v0.1.2
  ape sandbox governance ls
  ape sandbox up dev --governance-ref v0.1.2

aped never fetches it: if a requested ref is not materialized, 'ape sandbox
up' fails with the command to run.`,
	}
	cmd.AddCommand(newSandboxMaterializeCmd(governanceMountedRepo), newSandboxMaterializedLsCmd(governanceMountedRepo))
	return cmd
}

func newSandboxMaterializeCmd(m mountedRepo) *cobra.Command {
	var (
		repoPath string
		rootPath string
		force    bool
	)
	cmd := &cobra.Command{
		Use:   "materialize <ref>",
		Short: "Materialize a " + m.kind + " ref into the node's " + m.kind + " root",
		Long: `Materialize one ` + m.kind + ` ref (tag, branch, or commit) as a self-contained,
mountable checkout under the ` + m.kind + ` root.

The ref must ALREADY be present in the local ` + m.kind + ` repo — this command does
not fetch, so a stale checkout fails loudly instead of silently materializing an
older commit. Fetch first with your own credentials:
  git -C <repo> fetch --tags`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := strings.TrimSpace(args[0])
			if err := sandbox.ValidateMountName(ref); err != nil {
				return fmt.Errorf("%s ref %q is not usable as a directory name: %w", m.kind, ref, err)
			}
			repo, err := m.resolveRepo(repoPath)
			if err != nil {
				return err
			}
			root := m.root(rootPath)

			dest := filepath.Join(root, ref)
			if _, err := os.Stat(dest); err == nil {
				if !force {
					fmt.Fprintf(cmd.OutOrStdout(), "%s ref %s already materialized at %s (--force to replace)\n", m.kind, ref, dest)
					return nil
				}
				if err := os.RemoveAll(dest); err != nil {
					return fmt.Errorf("replace %s: %w", dest, err)
				}
			}

			ctx := cmd.Context()
			// Verify the ref BEFORE writing anything, so a typo or a stale checkout
			// never leaves a half-materialized directory behind.
			if err := verifyRef(ctx, m.kind, repo, ref); err != nil {
				return err
			}
			if err := os.MkdirAll(root, 0o755); err != nil {
				return fmt.Errorf("create %s root %s: %w", m.kind, root, err)
			}
			if err := materializeRef(ctx, m.kind, repo, ref, dest); err != nil {
				_ = os.RemoveAll(dest)
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "materialized %s → %s (branch main)\n", ref, dest)
			fmt.Fprintf(cmd.OutOrStdout(), "use it: ape sandbox up <name> --%s-ref %s\n", m.kind, ref)
			return nil
		},
	}
	cmd.Flags().StringVar(&repoPath, "repo", "", m.repoHelp)
	cmd.Flags().StringVar(&rootPath, "root", "", "Root the node mounts "+m.kind+" refs from (default: $"+m.rootEnv+" or "+m.defaultRoot+")")
	cmd.Flags().BoolVar(&force, "force", false, "Replace an already-materialized ref")
	return cmd
}

func newSandboxMaterializedLsCmd(m mountedRepo) *cobra.Command {
	var (
		rootPath     string
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the " + m.kind + " refs materialized on this host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root := m.root(rootPath)
			refs, err := materializedRefs(root)
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format == output.FormatJSON || format == output.FormatYAML {
				return output.Print(cmd.OutOrStdout(), format, map[string]any{"root": root, "refs": refs})
			}
			if len(refs) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no %s refs materialized under %s\n", m.kind, root)
				fmt.Fprintf(cmd.OutOrStdout(), "materialize one: ape sandbox %s materialize <ref>\n", m.kind)
				return nil
			}
			for _, r := range refs {
				fmt.Fprintln(cmd.OutOrStdout(), r)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rootPath, "root", "", "Root to list (default: $"+m.rootEnv+" or "+m.defaultRoot+")")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

// resolveFrameworkCheckout resolves the local framework checkout the same way
// `ape framework` does (flag, $APEX_FRAMEWORK_REPO, then ape's own clone once it
// exists) and confirms it really is a git checkout — materializing from a non-repo
// would otherwise fail with a raw git error.
func resolveFrameworkCheckout(flagValue string) (string, error) {
	repo, err := resolveFrameworkRepo(flagValue)
	if err != nil {
		return "", err
	}
	return gitCheckoutAbs(repo)
}

// resolveGovernanceCheckout resolves the local governance checkout: the flag,
// then the effective governance_repository_path of the project in the working
// directory (config, $APEX_GOVERNANCE_REPO, ape's own clone), then
// $APEX_GOVERNANCE_REPO alone when there is no project here.
func resolveGovernanceCheckout(flagValue string) (string, error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return gitCheckoutAbs(v)
	}
	if cfg, err := apexcfg.Resolve(".", nil); err == nil && cfg.GovernanceRepositoryPath != "" {
		return gitCheckoutAbs(cfg.GovernanceRepositoryPath)
	}
	if v := strings.TrimSpace(os.Getenv(repocache.EnvGovernanceRepo)); v != "" {
		return gitCheckoutAbs(v)
	}
	return "", fmt.Errorf("no governance repo: pass --repo, set $%s, or run this inside a project whose "+
		"governance repo `ape framework update` has synced", repocache.EnvGovernanceRepo)
}

func gitCheckoutAbs(repo string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(repo))
	if err != nil {
		return "", fmt.Errorf("resolve --repo %q: %w", repo, err)
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
		return "", fmt.Errorf("%s is not a git checkout (no .git)", abs)
	}
	return abs, nil
}

// verifyRef confirms the ref resolves in the local repo, with actionable guidance
// when it does not.
func verifyRef(ctx context.Context, kind, repo, ref string) error {
	out, err := runGitCapture(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || strings.TrimSpace(out) == "" {
		return fmt.Errorf("%s ref %q not found in %s; fetch it first: git -C %s fetch --tags", kind, ref, repo, repo)
	}
	return nil
}

// materializeRef clones the ref into dest as a self-contained repo on a local
// `main` branch.
func materializeRef(ctx context.Context, kind, repo, ref, dest string) error {
	// --no-hardlinks keeps dest independent of the source repo's object store, so
	// pruning or rewriting the source later cannot corrupt a mounted workspace.
	if _, err := runGitCapture(ctx, "", "clone", "--quiet", "--no-hardlinks", "--branch", ref, repo, dest); err != nil {
		// A commit SHA cannot be used with --branch; clone then check it out.
		if _, cerr := runGitCapture(ctx, "", "clone", "--quiet", "--no-hardlinks", repo, dest); cerr != nil {
			return fmt.Errorf("clone %s ref %s: %w", kind, ref, err)
		}
		if _, cerr := runGitCapture(ctx, dest, "checkout", "--quiet", ref); cerr != nil {
			return fmt.Errorf("check out %s ref %s: %w", kind, ref, cerr)
		}
	}
	// `ape framework setup` requires the framework repo to be on branch main
	// (install.go's framework_not_main check); a tag clone lands detached, so pin a
	// local main at that exact commit.
	if _, err := runGitCapture(ctx, dest, "checkout", "--quiet", "-B", "main"); err != nil {
		return fmt.Errorf("pin %s ref %s to a local main branch: %w", kind, ref, err)
	}
	return nil
}

// materializedRefs lists the ref directories under root, sorted.
func materializedRefs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var refs []string
	for _, e := range entries {
		if e.IsDir() {
			refs = append(refs, e.Name())
		}
	}
	sort.Strings(refs)
	return refs, nil
}

// runGitCapture runs git in dir (empty → the current directory) and returns
// stdout, folding stderr into the error.
func runGitCapture(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}
