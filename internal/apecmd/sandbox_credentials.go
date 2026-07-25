package apecmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
)

// Host credential exposure for sandbox workspaces (PLAN-16 D5 credential mode A,
// made workable under PLAN-18's hardening).
//
// The problem this solves: a workspace's composed home wants the host's Claude OAuth
// material, but aped-front runs as its own service user with ProtectHome=yes, so it
// cannot see /home/<you>/.claude — and a typical home is 0750, which a service user
// cannot traverse even when the bind exposes it. Widening the home, or handing the
// daemon a path it cannot validate, are both worse than the problem.
//
// The mechanism instead: the CLIENT — which is you, with your own permissions —
// publishes the credential into a small directory the daemon is allowed to read
// (default /srv/ape-credentials/<user>), and aped's --host-home points there. Two
// modes, with genuinely different consequences:
//
//	link (default)  a HARD LINK: the same inode, so the workspace and the host share
//	                one live credential. A token refresh inside the workspace is
//	                immediately valid on the host and vice versa. File permissions do
//	                not change (still 0600, still yours) because the daemon only
//	                stat()s the path — Kata's virtiofsd does the I/O as root.
//	copy            an independent copy. The workspace cannot affect your host
//	                session, but the two DIVERGE the first time either side refreshes:
//	                OAuth refresh tokens rotate, so whichever side refreshes second is
//	                left holding an invalid token.
//
// Neither mode is free: both give the workspace your Anthropic identity. That is
// inherent in "use my credentials", and it is why this is an explicit, revocable
// command rather than something `up` does silently.

// DefaultCredentialRoot is where published credentials live by default: outside
// /home (so no home-permission change is needed) and per-user.
//
//nolint:gosec // G101 false positive: a directory path, not a credential
const DefaultCredentialRoot = "/srv/ape-credentials"

// credentialRelPath is the layout the composer expects under a host home:
// <root>/<user>/.claude/.credentials.json.
//
//nolint:gosec // G101 false positive: a relative path, not a credential
const credentialRelPath = ".claude/.credentials.json"

// credentialModeLink / credentialModeCopy name the two publication shapes in status
// output (and keep the strings in one place).
const (
	credentialModeLink = "link"
	credentialModeCopy = "copy"
)

// credentialDirMode is traverse-only for others: the daemon must reach the path, but
// only the owner may list it, and the credential file itself stays 0600.
const credentialDirMode = 0o751

func newSandboxCredentialsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credentials",
		Short: "Publish your Claude credentials for workspaces to use",
		Long: `Publish the host's Claude OAuth credential where the aped node can read it, so
workspaces get your Anthropic session instead of asking you to log in again.

aped-front runs as its own service user with ProtectHome=yes and cannot read
/home/<you>/.claude. Rather than widening your home, this command — which runs as
YOU — publishes the credential into a directory the daemon may read
(` + DefaultCredentialRoot + `/<user>), and the node is pointed at it with
'aped front --host-home'.

  ape sandbox credentials publish     # hard link: one live credential, shared
  ape sandbox credentials publish --copy   # independent copy: isolated, diverges
  ape sandbox credentials status
  ape sandbox credentials revoke

Both modes give a workspace your Anthropic identity — that is what "use my
credentials" means. Use 'revoke' to take it back.`,
	}
	cmd.AddCommand(newCredentialsPublishCmd(), newCredentialsStatusCmd(), newCredentialsRevokeCmd())
	return cmd
}

func newCredentialsPublishCmd() *cobra.Command {
	var (
		root     string
		source   string
		copyMode bool
	)
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Publish the host credential to the node's credential root",
		Long: `Publish ~/.claude/.credentials.json where aped can read it.

Default is a HARD LINK: the workspace and the host share one inode, so a token
refresh on either side is immediately valid on the other, and your file's
permissions are unchanged (the daemon only stat()s it; Kata's virtiofsd does the
I/O as root). --copy makes an independent copy instead, which isolates the
workspace but DIVERGES the first time either side refreshes, because OAuth refresh
tokens rotate.

Re-running is safe and is how you repair a link that decoupled — which happens if
the host rewrites the credential by replacing the file rather than editing it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			src, err := resolveCredentialSource(source)
			if err != nil {
				return err
			}
			dest := credentialDest(root)
			res, err := publishCredential(src, dest, copyMode)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s\n", res)
			fmt.Fprintf(out, "node: point aped at it with 'aped front --host-home %s' "+
				"(deploy/dev-host.sh does this) and provision with credentials: oauth\n", filepath.Dir(filepath.Dir(dest)))
			if copyMode {
				fmt.Fprintln(out, "note: a copy diverges from your host session once either side refreshes its token")
			} else {
				fmt.Fprintln(out, "note: workspaces now share your LIVE Anthropic session — 'ape sandbox credentials revoke' undoes it")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "Credential root the node reads (default: $APE_CREDENTIAL_ROOT or "+DefaultCredentialRoot+")")
	cmd.Flags().StringVar(&source, "source", "", "Credential file to publish (default: ~/.claude/.credentials.json)")
	cmd.Flags().BoolVar(&copyMode, "copy", false, "Publish an independent copy instead of a hard link (isolated, but diverges on token refresh)")
	return cmd
}

func newCredentialsStatusCmd() *cobra.Command {
	var (
		root         string
		source       string
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether a published credential is present and still live",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dest := credentialDest(root)
			st := credentialStatus(source, dest)
			format := output.Format(outputFormat)
			if format == output.FormatJSON || format == output.FormatYAML {
				return output.Print(cmd.OutOrStdout(), format, st)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", st.summary())
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "Credential root the node reads")
	cmd.Flags().StringVar(&source, "source", "", "Credential file to compare against (default: ~/.claude/.credentials.json)")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newCredentialsRevokeCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Remove the published credential so workspaces stop getting it",
		Long: `Remove the published credential. With a hard link this only drops the extra
name — your ~/.claude/.credentials.json is untouched, since a file survives until
its last link is gone. With a copy it deletes the copy.

Workspaces already running keep the credential that was composed into them until
they are torn down.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dest := credentialDest(root)
			if err := os.Remove(dest); err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintf(cmd.OutOrStdout(), "nothing published at %s\n", dest)
					return nil
				}
				return fmt.Errorf("remove %s: %w", dest, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "revoked %s (your ~/.claude credential is untouched)\n", dest)
			fmt.Fprintln(cmd.OutOrStdout(), "note: workspaces already running keep what was composed into them until 'down'")
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "Credential root the node reads")
	return cmd
}

// credentialRoot resolves the node's credential root: flag, env, then the default.
func credentialRoot(flagValue string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("APE_CREDENTIAL_ROOT")); v != "" {
		return v
	}
	return DefaultCredentialRoot
}

// credentialDest returns the published credential path for the current user:
// <root>/<user>/.claude/.credentials.json. The per-user directory keeps two
// operators on one node from publishing over each other.
func credentialDest(rootFlag string) string {
	user := os.Getenv("USER")
	if user == "" {
		if u, err := os.UserHomeDir(); err == nil {
			user = filepath.Base(u)
		}
	}
	return filepath.Join(credentialRoot(rootFlag), user, credentialRelPath)
}

// resolveCredentialSource returns the credential file to publish, confirming it
// exists — publishing a path that is not there would produce a workspace that fails
// to compose, far from here.
func resolveCredentialSource(flagValue string) (string, error) {
	src := strings.TrimSpace(flagValue)
	if src == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		src = filepath.Join(home, credentialRelPath)
	}
	st, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("credential %s not found — log in with claude first (or pass --source): %w", src, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("credential source %s is a directory", src)
	}
	return src, nil
}

// publishCredential links (or copies) src to dest, creating the directories the
// daemon needs to traverse. Re-publishing repairs a decoupled link.
func publishCredential(src, dest string, copyMode bool) (string, error) {
	dir := filepath.Dir(dest)
	// 0751 — traverse-only for others, deliberately, and it is the crux of this design
	// working without touching group membership: this command runs as YOU, so anything
	// it creates gets your primary group, which the daemon's service user is not in.
	// Granting "other" execute (not read) lets the daemon reach the path while the
	// credential itself stays 0600 — traversal reveals nothing but the path's
	// existence, and the enclosing per-user directory (created by
	// deploy/dev-host.sh as <user>:ape 0750) is what actually gates who gets that far.
	if err := os.MkdirAll(dir, credentialDirMode); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	// MkdirAll is a no-op on an existing dir and umask can strip bits from a new one,
	// so set the mode explicitly.
	if err := os.Chmod(dir, credentialDirMode); err != nil {
		return "", fmt.Errorf("chmod %s: %w", dir, err)
	}

	// Remove any previous publication first: os.Link refuses an existing target, and a
	// stale link/copy is exactly what re-publishing is meant to replace.
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("replace %s: %w", dest, err)
	}

	if copyMode {
		if err := copyFile(src, dest); err != nil {
			return "", err
		}
		return fmt.Sprintf("published a COPY of %s → %s", src, dest), nil
	}
	if err := os.Link(src, dest); err != nil {
		return "", fmt.Errorf("hard link %s → %s: %w (different filesystems? use --copy)", src, dest, err)
	}
	return fmt.Sprintf("published %s → %s (hard link: one live credential, shared with workspaces)", src, dest), nil
}

// copyFile copies src to dest with 0600, so a published copy is no more readable
// than the original.
func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy to %s: %w", dest, err)
	}
	return out.Close()
}

// CredentialStatus reports what is published and whether it is still the same file
// as the source.
type CredentialStatus struct {
	Source    string `json:"source"`
	Published string `json:"published"`
	Present   bool   `json:"present"`
	Mode      string `json:"mode,omitempty"` // link | copy
	Live      bool   `json:"live"`           // link still points at the source inode
	Detail    string `json:"detail,omitempty"`
}

// summary renders the human line.
func (s CredentialStatus) summary() string {
	switch {
	case !s.Present:
		return fmt.Sprintf("not published (%s) — run 'ape sandbox credentials publish'", s.Published)
	case s.Mode == credentialModeLink && s.Live:
		return fmt.Sprintf("published as a live hard link: %s ↔ %s", s.Published, s.Source)
	case s.Mode == credentialModeLink:
		return fmt.Sprintf("published link is STALE (%s no longer shares an inode with %s): re-run "+
			"'ape sandbox credentials publish'", s.Published, s.Source)
	default:
		return fmt.Sprintf("published as a copy: %s (%s)", s.Published, s.Detail)
	}
}

// credentialStatus inspects the published path and compares it with the source.
func credentialStatus(sourceFlag, dest string) CredentialStatus {
	st := CredentialStatus{Published: dest}
	src, err := resolveCredentialSource(sourceFlag)
	if err != nil {
		st.Detail = err.Error()
	}
	st.Source = src

	di, err := os.Stat(dest)
	if err != nil {
		return st
	}
	st.Present = true
	// A hard link is detectable without reading either file: same device + inode.
	if src != "" {
		si, serr := os.Stat(src)
		if serr == nil && os.SameFile(si, di) {
			st.Mode, st.Live = credentialModeLink, true
			return st
		}
		if serr == nil && sameInodeCount(di) > 1 {
			st.Mode = credentialModeLink
			st.Detail = "link no longer shares an inode with the source"
			return st
		}
	}
	st.Mode = credentialModeCopy
	st.Detail = fmt.Sprintf("%d bytes, modified %s", di.Size(), di.ModTime().Format("2006-01-02 15:04"))
	return st
}

// sameInodeCount returns the file's link count, or 1 when unavailable — used only to
// tell a decoupled hard link from a deliberate copy.
func sameInodeCount(fi os.FileInfo) uint64 {
	if n, ok := linkCount(fi); ok {
		return n
	}
	return 1
}
