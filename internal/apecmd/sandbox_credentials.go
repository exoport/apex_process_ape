package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

// credentialMarkerName records HOW the credential was published, next to it.
//
// It exists because a STALE LINK and a real COPY are indistinguishable by stat alone:
// once the host replaces its credential file, the published inode has one link and no
// longer matches the source — exactly like a copy. Without this marker `status` reports
// "copy" for a decoupled link and gives the user no reason to re-publish. Measured
// against a real `claude /login` (2026-07-25), which DOES replace the file.
//
//nolint:gosec // G101 false positive: a marker file NAME, not a credential
const credentialMarkerName = ".ape-credential-publish.json"

// credentialACLUser is the account granted access to the shared credential: the user
// aped-front runs as. The grant is a POSIX ACL entry for exactly that user, which is
// narrower than a group grant in two ways that both matter:
//
//   - Only that ONE account gains access. Group `ape` is also the priv-socket gate, so
//     granting the group would silently hand the credential to every operator added
//     there later.
//   - `setfacl` only requires that you OWN the file, whereas `chgrp` requires the target
//     group in the caller's ACTIVE group list — so a shell opened before `usermod -aG
//     ape` fails with EPERM. Publishing must not depend on which session you happen to
//     be in.
//
// The cost is transparency: `ls -l` shows a mode with a trailing `+` and no hint of who
// the extra entry is for, so `ape sandbox credentials status` prints the effective grant
// and `ape doctor` checks the tooling is present.
const credentialACLUser = "aped"

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
  ape sandbox credentials watch      # re-publish automatically after a /login
  ape sandbox credentials revoke

Both modes give a workspace your Anthropic identity — that is what "use my
credentials" means. Use 'revoke' to take it back.`,
	}
	cmd.AddCommand(newCredentialsPublishCmd(), newCredentialsStatusCmd(),
		newCredentialsWatchCmd(), newCredentialsRevokeCmd())
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
			res, err := publishCredential(cmd.Context(), src, dest, copyMode)
			if err != nil {
				if errors.Is(err, ErrNoACLTooling) {
					// Fail rather than publish something aped cannot read: a publication
					// without the grant looks successful and then breaks every `up`.
					return fmt.Errorf("%w\n  ape grants access with a POSIX ACL for exactly the `%s` user; there is "+
						"no group fallback, because group `ape` is also the priv-socket gate and would share your "+
						"credential with every operator added to it", err, credentialACLUser)
				}
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

func newCredentialsWatchCmd() *cobra.Command {
	var (
		root        string
		source      string
		interval    time.Duration
		printUnit   bool
		installUnit bool
	)
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Re-publish automatically whenever your credential is replaced",
		Long: `Watch your credential and re-publish it the moment it is replaced.

This closes the one gap in credential sharing that nothing on the node can close. A
` + "`claude /login`" + ` REPLACES the credential file rather than editing it, so the published
hard link is left pointing at the old one — and aped cannot notice, because it runs as
another user with ProtectHome=yes and can never read your home. Until something
re-publishes, every workspace keeps using the pre-login token.

Any 'ape sandbox' command re-publishes as a side effect, so in normal use this is already
handled. Run this watcher when you want it handled with no command at all — as a user
service:

  install -D -m0644 deploy/user/ape-credentials-watch.service \
    ~/.config/systemd/user/ape-credentials-watch.service
  systemctl --user enable --now ape-credentials-watch
  sudo loginctl enable-linger $USER   # start at BOOT, not just at first login

It runs as YOU — that is the point: only your own session can read your home. It needs no
aped node, no running workspace, and no publication (with nothing published it idles), so
it is safe to enable before ever publishing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if interval <= 0 {
				interval = 2 * time.Second
			}
			if printUnit || installUnit {
				unit, err := watchUnitFile(interval)
				if err != nil {
					return err
				}
				if printUnit {
					fmt.Fprint(cmd.OutOrStdout(), unit)
					return nil
				}
				path, err := installWatchUnit(unit)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
				fmt.Fprintln(cmd.OutOrStdout(), "enable it:")
				fmt.Fprintln(cmd.OutOrStdout(), "  systemctl --user daemon-reload")
				fmt.Fprintln(cmd.OutOrStdout(), "  systemctl --user enable --now ape-credentials-watch")
				fmt.Fprintln(cmd.OutOrStdout(), "  sudo loginctl enable-linger $USER   # start at boot, not just at login")
				return nil
			}
			dest := credentialDest(root)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "watching %s (checking every %s; Ctrl-C to stop)\n", credentialSourceLabel(source), interval)
			// Nothing published is an IDLE state, not an error. This is meant to run as a
			// boot service: exiting here would crash-loop under Restart=on-failure on any
			// machine where the operator has not published yet (or has revoked), and would
			// then need re-enabling by hand once they did. Idling costs two stats a tick.
			if !fileExists(dest) {
				fmt.Fprintf(out, "nothing published at %s yet — idle until 'ape sandbox credentials publish' runs\n", dest)
			}

			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case <-t.C:
					repaired, err := RepairCredentialPublication(cmd.Context(), root, source)
					if err != nil {
						// Keep watching: a transient failure (the file mid-replacement) must not
						// end the watch and silently stop sharing for the rest of the session.
						fmt.Fprintf(cmd.ErrOrStderr(), "! re-publish failed: %v\n", err)
						continue
					}
					if repaired {
						fmt.Fprintf(out, "%s re-published (your credential had been replaced)\n",
							time.Now().Format("15:04:05"))
					}
				}
			}
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "Credential root the node reads")
	cmd.Flags().StringVar(&source, "source", "", "Credential file to watch (default: ~/.claude/.credentials.json)")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "How often to check for a replacement")
	cmd.Flags().BoolVar(&printUnit, "print-unit", false, "Print a systemd --user unit for this watcher (with THIS binary's path) and exit")
	cmd.Flags().BoolVar(&installUnit, "install-unit", false, "Write that unit to ~/.config/systemd/user/ and exit (safer than redirecting --print-unit)")
	return cmd
}

// apeBinaryPath returns the path to use in a generated unit.
//
// It deliberately prefers the path as INVOKED (resolved through $PATH) over
// /proc/self/exe, because installations are commonly a stable symlink onto a
// version-stamped binary — `~/go/bin/ape` → `ape-v0.0.48` is exactly what `go install`
// leaves. Baking the resolved target into a unit would pin it to today's version and
// break it at the next update; the symlink keeps working.
func apeBinaryPath() (string, error) {
	if p, err := exec.LookPath(os.Args[0]); err == nil {
		if abs, aerr := filepath.Abs(p); aerr == nil {
			return abs, nil
		}
	}
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine this binary's path: %w", err)
	}
	return self, nil
}

// unitTempDir is a seam: a test binary's own fixtures live under the real temp dir, so
// exercising the non-temp path needs this overridable.
var unitTempDir = os.TempDir

// installWatchUnit writes the rendered unit into the user's systemd directory.
//
// It exists because `--print-unit > file` is a trap: the shell TRUNCATES the target
// before the command runs, so an ape that cannot render the unit — an older build without
// the flag, say — leaves an empty unit file behind and systemd then refuses to load it.
// Rendering first and writing via temp+rename means the file is either untouched or
// complete.
func installWatchUnit(unit string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "ape-credentials-watch.service")
	tmp, err := os.CreateTemp(dir, "ape-credentials-watch.*.tmp")
	if err != nil {
		return "", fmt.Errorf("stage unit: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.WriteString(unit); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write unit: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("chmod unit: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close unit: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return "", fmt.Errorf("install unit: %w", err)
	}
	return path, nil
}

// watchUnitFile renders a systemd --user unit for the watcher.
func watchUnitFile(interval time.Duration) (string, error) {
	bin, err := apeBinaryPath()
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(bin, unitTempDir()) {
		return "", fmt.Errorf("this ape is running from a temporary path (%s) — a unit pointing at it would "+
			"break as soon as it is cleaned up. Run --print-unit from an INSTALLED ape", bin)
	}
	args := "sandbox credentials watch"
	if interval != 2*time.Second {
		args += " --interval " + interval.String()
	}
	return fmt.Sprintf(`# Re-publish the operator's Claude credential for `+"`ape sandbox`"+` workspaces.
#
# Generated by: ape sandbox credentials watch --print-unit
#
# This must be a USER service: a `+"`claude /login`"+` REPLACES the credential file, and only a
# process running as you can see your home to notice — aped runs as its own user with
# ProtectHome=yes. Without it, a host login reaches workspaces the next time any
# `+"`ape sandbox`"+` command runs (each one re-publishes); with it, no command is needed.
#
# Install:
#   ape sandbox credentials watch --print-unit > ~/.config/systemd/user/ape-credentials-watch.service
#   systemctl --user daemon-reload
#   systemctl --user enable --now ape-credentials-watch
#
# To start at BOOT rather than at first login (a user manager otherwise exists only while
# you have a session):
#   sudo loginctl enable-linger $USER
#
# It needs no aped node, no running workspace and no publication — with nothing published
# it idles — so enabling it before ever publishing is safe.
[Unit]
Description=Re-publish the Claude credential for ape sandbox workspaces
Documentation=https://github.com/exoport/apex_process_ape/blob/main/docs/how-to/run-aped.md

[Service]
Type=simple
ExecStart=%s %s
Restart=on-failure
RestartSec=5s
NoNewPrivileges=yes

[Install]
WantedBy=default.target
`, bin, args), nil
}

// credentialSourceLabel renders the watched source for the startup line.
func credentialSourceLabel(sourceFlag string) string {
	if src, err := resolveCredentialSource(sourceFlag); err == nil {
		return src
	}
	return "~/" + credentialRelPath
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
			st := credentialStatus(cmd.Context(), source, dest)
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
			// Drop the ACL entry first: with a hard link the grant lives on the shared
			// inode, so removing only the extra name would leave the operator's own
			// credential readable by the daemon for no reason.
			restored := revokeDaemonAccess(cmd.Context(), dest)
			if err := os.Remove(dest); err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintf(cmd.OutOrStdout(), "nothing published at %s\n", dest)
					return nil
				}
				return fmt.Errorf("remove %s: %w", dest, err)
			}
			_ = os.Remove(markerPath(dest))
			fmt.Fprintf(cmd.OutOrStdout(), "revoked %s\n", dest)
			if restored {
				fmt.Fprintf(cmd.OutOrStdout(), "removed the %s access grant from your credential\n", aclUser())
			}
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
	name := os.Getenv("USER")
	if name == "" {
		if home, err := os.UserHomeDir(); err == nil {
			name = filepath.Base(home)
		}
	}
	return filepath.Join(credentialRoot(rootFlag), name, credentialRelPath)
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
func publishCredential(ctx context.Context, src, dest string, copyMode bool) (string, error) {
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
		if err := grantDaemonAccess(ctx, dest); err != nil {
			return "", err
		}
		writeCredentialMarker(dest, credentialModeCopy, src)
		return fmt.Sprintf("published a COPY of %s → %s", src, dest), nil
	}
	if err := os.Link(src, dest); err != nil {
		return "", fmt.Errorf("hard link %s → %s: %w (different filesystems? use --copy)", src, dest, err)
	}
	// The grant is applied to the shared INODE, so it covers the operator's own file
	// too — see credentialSharedMode for why there is no narrower option.
	if err := grantDaemonAccess(ctx, dest); err != nil {
		return "", err
	}
	writeCredentialMarker(dest, credentialModeLink, src)
	return fmt.Sprintf("published %s → %s (hard link: one live credential, shared with workspaces)", src, dest), nil
}

// aclUser is a seam: tests grant their own account, since granting `aped` requires that
// account to exist on the machine running the tests.
var aclUser = func() string { return credentialACLUser }

// ErrNoACLTooling reports that setfacl is unavailable. There is deliberately NO fallback
// to a group grant: the two differ in WHO gains access, so silently substituting the
// broader one would hand the credential to every member of group `ape` while the
// operator believed they had granted a single account.
var ErrNoACLTooling = errors.New("setfacl not found: the acl package is required to share a credential with aped")

// grantDaemonAccess adds a POSIX ACL entry giving the aped user read+write on the
// credential, leaving owner, group and mode untouched.
func grantDaemonAccess(ctx context.Context, path string) error {
	if _, err := exec.LookPath("setfacl"); err != nil {
		return fmt.Errorf("%w\n  install it: sudo apt install acl", ErrNoACLTooling)
	}
	out, err := exec.CommandContext(ctx, "setfacl", "-m", "u:"+aclUser()+":rw", path).CombinedOutput() //nolint:gosec // fixed argv; path is ape-derived
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(msg), "not supported") {
			return fmt.Errorf("grant %s access to %s: %s\n"+
				"  the filesystem holding it is mounted without ACL support — mount it with `acl`, or keep "+
				"credentials on a filesystem that has it", aclUser(), path, msg)
		}
		return fmt.Errorf("grant %s access to %s: %s", aclUser(), path, msg)
	}
	return nil
}

// revokeDaemonAccess removes the ACL entry, returning the credential to exactly the
// permissions it had before publishing.
func revokeDaemonAccess(ctx context.Context, path string) bool {
	if _, err := exec.LookPath("setfacl"); err != nil {
		return false
	}
	return exec.CommandContext(ctx, "setfacl", "-x", "u:"+aclUser(), path).Run() == nil //nolint:gosec // fixed argv
}

// groupHasNoAccess reports whether the OWNING GROUP still has no access.
//
// It exists because of a genuinely confusing detail of POSIX ACLs: adding a user entry
// creates an ACL MASK, and the mask is stored in the mode's group bits — so `ls -l`
// starts showing `-rw-rw----+` even though `group::---` means the owning group can do
// nothing. Reading the mode alone would suggest the grant is far broader than it is, in
// either direction, so the narrowness of the grant is asserted here rather than inferred
// from permissions.
func groupHasNoAccess(ctx context.Context, path string) bool {
	if _, err := exec.LookPath("getfacl"); err != nil {
		return false
	}
	out, err := exec.CommandContext(ctx, "getfacl", "-cE", path).Output()
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.TrimSpace(line) == "group::---" {
			return true
		}
	}
	return false
}

// hasDaemonAccess reports whether the ACL entry is present. `status` shows this because
// `ls -l` renders only a bare `+` and says nothing about who the entry is for.
func hasDaemonAccess(ctx context.Context, path string) bool {
	if _, err := exec.LookPath("getfacl"); err != nil {
		return false
	}
	out, err := exec.CommandContext(ctx, "getfacl", "-cE", path).Output()
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "user:"+aclUser()+":") && strings.Contains(t, "r") && strings.Contains(t, "w") {
			return true
		}
	}
	return false
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// credentialMarker is the recorded publication: how it was published (which is the only
// way to tell a decoupled link from a deliberate copy) and what it came from.
type credentialMarker struct {
	Mode   string `json:"mode"`
	Source string `json:"source"`
}

// markerPath returns the marker beside a published credential.
func markerPath(dest string) string { return filepath.Join(filepath.Dir(dest), credentialMarkerName) }

// writeCredentialMarker records the publication mode. Best-effort: losing it degrades
// `status` to "cannot tell link from copy", never the credential itself.
func writeCredentialMarker(dest, mode, source string) {
	data, err := json.Marshal(credentialMarker{Mode: mode, Source: source})
	if err == nil {
		_ = os.WriteFile(markerPath(dest), data, 0o600)
	}
}

// readCredentialMarker returns the recorded publication, if any.
func readCredentialMarker(dest string) (credentialMarker, bool) {
	data, err := os.ReadFile(markerPath(dest))
	if err != nil {
		return credentialMarker{}, false
	}
	var m credentialMarker
	if err := json.Unmarshal(data, &m); err != nil || m.Mode == "" {
		return credentialMarker{}, false
	}
	return m, true
}

// RepairCredentialPublication re-links an EXISTING publication whose link decoupled,
// and reports whether it did anything.
//
// This runs from `ape sandbox up` because a host `claude /login` REPLACES the
// credential file (verified), which silently leaves every workspace holding the
// pre-login token. Requiring a manual re-publish after each login would make the
// feature quietly wrong most of the time.
//
// It deliberately never CREATES a publication — no publication means no grant, and
// `up` must not invent one. It also leaves a `--copy` publication alone: the user chose
// isolation, and silently re-linking would undo that choice.
func RepairCredentialPublication(ctx context.Context, rootFlag, sourceFlag string) (repaired bool, err error) {
	dest := credentialDest(rootFlag)
	// "Nothing published" is the normal case, not a failure: most workspaces never use
	// a host credential, so an absent publication means there is simply nothing to
	// repair — and it must NOT be turned into an error that fails `up`.
	if !fileExists(dest) {
		return false, nil
	}
	marker, ok := readCredentialMarker(dest)
	if !ok || marker.Mode != credentialModeLink {
		return false, nil // a copy (or unknown provenance) is left as the user left it
	}
	src, serr := resolveCredentialSource(sourceFlag)
	if serr != nil {
		return false, serr
	}
	si, serr := os.Stat(src)
	di, derr := os.Stat(dest)
	if serr != nil || derr != nil {
		// Either side vanished between the checks above and here. Repair is a
		// best-effort convenience on the way into `up`; `status` is where a user asks
		// for a diagnosis, so stay quiet rather than failing the create.
		return false, nil //nolint:nilerr // an unreadable pair means "cannot repair", not "create failed"
	}
	if os.SameFile(si, di) {
		// Same inode, but the grant may have been dropped (a login creates a fresh inode
		// with no ACL, and a re-published link inherits whatever it has); re-assert it so
		// the daemon does not silently lose access.
		if !hasDaemonAccess(ctx, dest) {
			return false, grantDaemonAccess(ctx, dest)
		}
		return false, nil
	}
	if _, err := publishCredential(ctx, src, dest, false); err != nil {
		return false, err
	}
	return true, nil
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
	Granted   bool   `json:"granted"`        // the aped user has the ACL entry it needs
	Detail    string `json:"detail,omitempty"`
}

// summary renders the human line.
func (s CredentialStatus) summary() string {
	if s.Present && !s.Granted {
		return fmt.Sprintf("published at %s but %s has NO access to it — workspaces will fail to start; "+
			"re-run 'ape sandbox credentials publish'", s.Published, credentialACLUser)
	}
	switch {
	case !s.Present:
		return fmt.Sprintf("not published (%s) — run 'ape sandbox credentials publish'", s.Published)
	case s.Mode == credentialModeLink && s.Live:
		return fmt.Sprintf("published as a live hard link: %s ↔ %s (%s granted rw via ACL)",
			s.Published, s.Source, credentialACLUser)
	case s.Mode == credentialModeLink:
		return fmt.Sprintf("published link is STALE — %s. Workspaces would get the old credential; "+
			"re-run 'ape sandbox credentials publish' (or just 'ape sandbox up', which repairs it)",
			s.Detail)
	default:
		return fmt.Sprintf("published as a copy: %s (%s)", s.Published, s.Detail)
	}
}

// credentialStatus inspects the published path and compares it with the source.
func credentialStatus(ctx context.Context, sourceFlag, dest string) CredentialStatus {
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
	st.Granted = hasDaemonAccess(ctx, dest)
	marker, haveMarker := readCredentialMarker(dest)
	if haveMarker {
		st.Mode = marker.Mode
	}
	if src != "" {
		si, serr := os.Stat(src)
		// A LIVE link is provable without the marker: same device + inode.
		if serr == nil && os.SameFile(si, di) {
			st.Mode, st.Live = credentialModeLink, true
			return st
		}
		// Not the same file. Whether that is a decoupled link or a deliberate copy is
		// NOT decidable from stat — both have one link and a different inode — so the
		// recorded mode is what distinguishes them.
		if st.Mode == credentialModeLink {
			st.Detail = "the host credential was replaced (a login does this), so this link points at the OLD one"
			return st
		}
		if !haveMarker && sameInodeCount(di) > 1 {
			st.Mode = credentialModeLink
			st.Detail = "link no longer shares an inode with the source"
			return st
		}
	}
	if st.Mode == "" {
		st.Mode = credentialModeCopy
	}
	if st.Detail == "" {
		st.Detail = fmt.Sprintf("%d bytes, modified %s", di.Size(), di.ModTime().Format("2006-01-02 15:04"))
	}
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
