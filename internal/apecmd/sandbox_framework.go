package apecmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/output"
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
with 'ape framework setup --no-fetch --repo /opt/apex-framework'.`,
	}
	cmd.AddCommand(newSandboxFrameworkMaterializeCmd(), newSandboxFrameworkLsCmd())
	return cmd
}

func newSandboxFrameworkMaterializeCmd() *cobra.Command {
	var (
		repoPath string
		rootPath string
		force    bool
	)
	cmd := &cobra.Command{
		Use:   "materialize <ref>",
		Short: "Materialize a framework ref into the node's framework root",
		Long: `Materialize one framework ref (tag, branch, or commit) as a self-contained,
mountable checkout under the framework root.

The ref must ALREADY be present in the local framework repo — this command does
not fetch, so a stale checkout fails loudly instead of silently materializing an
older commit. Fetch first with your own credentials:
  git -C <repo> fetch --tags`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := strings.TrimSpace(args[0])
			if err := sandbox.ValidateMountName(ref); err != nil {
				return fmt.Errorf("framework ref %q is not usable as a directory name: %w", ref, err)
			}
			repo, err := resolveFrameworkCheckout(repoPath)
			if err != nil {
				return err
			}
			root := frameworkRoot(rootPath)

			dest := filepath.Join(root, ref)
			if _, err := os.Stat(dest); err == nil {
				if !force {
					fmt.Fprintf(cmd.OutOrStdout(), "framework ref %s already materialized at %s (--force to replace)\n", ref, dest)
					return nil
				}
				if err := os.RemoveAll(dest); err != nil {
					return fmt.Errorf("replace %s: %w", dest, err)
				}
			}

			ctx := cmd.Context()
			// Verify the ref BEFORE writing anything, so a typo or a stale checkout
			// never leaves a half-materialized directory behind.
			if err := verifyRef(ctx, repo, ref); err != nil {
				return err
			}
			if err := os.MkdirAll(root, 0o755); err != nil {
				return fmt.Errorf("create framework root %s: %w", root, err)
			}
			if err := materializeRef(ctx, repo, ref, dest); err != nil {
				_ = os.RemoveAll(dest)
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "materialized %s → %s (branch main)\n", ref, dest)
			fmt.Fprintf(cmd.OutOrStdout(), "use it: ape sandbox up <name> --framework-ref %s\n", ref)
			return nil
		},
	}
	cmd.Flags().StringVar(&repoPath, "repo", "", "Local apex_process_framework checkout (default: $APEX_FRAMEWORK_REPO)")
	cmd.Flags().StringVar(&rootPath, "root", "", "Framework root the node mounts from (default: $APE_FRAMEWORK_ROOT or "+DefaultFrameworkRoot+")")
	cmd.Flags().BoolVar(&force, "force", false, "Replace an already-materialized ref")
	return cmd
}

func newSandboxFrameworkLsCmd() *cobra.Command {
	var (
		rootPath     string
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the framework refs materialized on this host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root := frameworkRoot(rootPath)
			refs, err := materializedRefs(root)
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format == output.FormatJSON || format == output.FormatYAML {
				return output.Print(cmd.OutOrStdout(), format, map[string]any{"root": root, "refs": refs})
			}
			if len(refs) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no framework refs materialized under %s\n", root)
				fmt.Fprintln(cmd.OutOrStdout(), "materialize one: ape sandbox framework materialize <ref>")
				return nil
			}
			for _, r := range refs {
				fmt.Fprintln(cmd.OutOrStdout(), r)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rootPath, "root", "", "Framework root to list (default: $APE_FRAMEWORK_ROOT or "+DefaultFrameworkRoot+")")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

// frameworkRoot resolves the framework root: flag, env, then the default.
func frameworkRoot(flagValue string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("APE_FRAMEWORK_ROOT")); v != "" {
		return v
	}
	return DefaultFrameworkRoot
}

// resolveFrameworkCheckout resolves the local framework checkout the same way
// `ape framework` does (flag, then $APEX_FRAMEWORK_REPO) and additionally confirms
// it really is a git checkout — materializing from a non-repo would otherwise fail
// with a raw git error.
func resolveFrameworkCheckout(flagValue string) (string, error) {
	repo, err := resolveFrameworkRepo(flagValue)
	if err != nil {
		return "", err
	}
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
func verifyRef(ctx context.Context, repo, ref string) error {
	out, err := runGitCapture(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || strings.TrimSpace(out) == "" {
		return fmt.Errorf("framework ref %q not found in %s; fetch it first: git -C %s fetch --tags", ref, repo, repo)
	}
	return nil
}

// materializeRef clones the ref into dest as a self-contained repo on a local
// `main` branch.
func materializeRef(ctx context.Context, repo, ref, dest string) error {
	// --no-hardlinks keeps dest independent of the source repo's object store, so
	// pruning or rewriting the source later cannot corrupt a mounted workspace.
	if _, err := runGitCapture(ctx, "", "clone", "--quiet", "--no-hardlinks", "--branch", ref, repo, dest); err != nil {
		// A commit SHA cannot be used with --branch; clone then check it out.
		if _, cerr := runGitCapture(ctx, "", "clone", "--quiet", "--no-hardlinks", repo, dest); cerr != nil {
			return fmt.Errorf("clone framework ref %s: %w", ref, err)
		}
		if _, cerr := runGitCapture(ctx, dest, "checkout", "--quiet", ref); cerr != nil {
			return fmt.Errorf("check out framework ref %s: %w", ref, cerr)
		}
	}
	// `ape framework setup` requires the framework repo to be on branch main
	// (install.go's framework_not_main check); a tag clone lands detached, so pin a
	// local main at that exact commit.
	if _, err := runGitCapture(ctx, dest, "checkout", "--quiet", "-B", "main"); err != nil {
		return fmt.Errorf("pin framework ref %s to a local main branch: %w", ref, err)
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
		return nil, fmt.Errorf("read framework root %s: %w", root, err)
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
