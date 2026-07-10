package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

// DefaultGuestHome is where the staging directory is mounted inside the
// guest and what $HOME points at. It is deliberately *not* /home/<user>
// or /root — those host paths are masked in the OCI spec so the real home
// is unreadable; the synthetic home lives at its own path.
const DefaultGuestHome = "/sandbox/home"

// BindMount is an extra host→guest bind the runner must add to the OCI
// spec beyond the staging dir itself: the mode-A credentials file, a
// deploy key, the ssh-agent socket, or the NATS creds file.
type BindMount struct {
	Source   string // absolute host path
	Dest     string // absolute path inside the guest
	ReadOnly bool
}

// Composition is the result of assembling a synthetic home for one job:
// the populated staging dir, the extra binds and env the runner injects,
// and the resolved guest $HOME path.
type Composition struct {
	StagingDir string      // host path populated by Compose (becomes guest $HOME)
	GuestHome  string      // absolute $HOME inside the guest
	Binds      []BindMount // binds beyond StagingDir → GuestHome
	Env        []string    // KEY=VALUE entries injected into the guest process
}

// ComposeOptions drives Compose. Only Profile and StagingDir are
// required; the rest default off HostHome.
type ComposeOptions struct {
	Profile    *Profile
	StagingDir string // host dir to populate; created if absent
	HostHome   string // host user home — source for by-name skills/agents + mode-A OAuth
	GuestHome  string // default: DefaultGuestHome

	// userSkillsDir / userAgentsDir override the by-name source dirs;
	// tests set them, production derives them from HostHome.
	userSkillsDir string
	userAgentsDir string
}

// Compose assembles the guest ~/.claude staging tree described by the
// profile: the generated .claude.json + settings.json, the hand-picked
// skills/agents, the credential-mode wiring, and the git-credential
// files. It returns the extra binds/env the runner must apply. It never
// reads anything from the host home except the explicitly named skills,
// agents, and (mode A) the OAuth credential file — nothing leaks in by
// omission.
func Compose(opts ComposeOptions) (*Composition, error) {
	if opts.Profile == nil {
		return nil, errors.New("compose: profile is nil")
	}
	if opts.StagingDir == "" {
		return nil, errors.New("compose: staging dir is empty")
	}
	guestHome := opts.GuestHome
	if guestHome == "" {
		guestHome = DefaultGuestHome
	}
	userSkills := opts.userSkillsDir
	if userSkills == "" && opts.HostHome != "" {
		userSkills = filepath.Join(opts.HostHome, ".claude", "skills")
	}
	userAgents := opts.userAgentsDir
	if userAgents == "" && opts.HostHome != "" {
		userAgents = filepath.Join(opts.HostHome, ".claude", "agents")
	}

	comp := &Composition{StagingDir: opts.StagingDir, GuestHome: guestHome}

	claudeDir := filepath.Join(opts.StagingDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		return nil, fmt.Errorf("compose: mkdir staging .claude: %w", err)
	}

	if err := writeClaudeJSON(opts.StagingDir, opts.Profile); err != nil {
		return nil, err
	}
	if err := writeSettingsJSON(claudeDir, opts.Profile); err != nil {
		return nil, err
	}
	if err := composeSkills(opts.Profile.Skills, userSkills, filepath.Join(claudeDir, "skills")); err != nil {
		return nil, err
	}
	if err := composeAgents(opts.Profile.Agents, userAgents, filepath.Join(claudeDir, "agents")); err != nil {
		return nil, err
	}
	if err := composeCredentials(opts, comp, claudeDir); err != nil {
		return nil, err
	}
	if err := composeGit(opts, comp); err != nil {
		return nil, err
	}
	if err := composeSSHAccess(opts, comp); err != nil {
		return nil, err
	}
	return comp, nil
}

// composeSSHAccess stages the guest ~/.ssh/authorized_keys from the profile's
// access.authorized_keys so `ape sandbox ssh` can authenticate by key
// (PLAN-16 D7). Each entry is a public-key literal or a path to a .pub /
// authorized_keys file (a leading ~ expands to the host home). Empty → no
// file is written (key auth stays unconfigured; use attach/exec). The keys
// are copied into the staged home like the rest of ~/.claude — nothing is
// bound, and nothing from the real ~/.ssh is used unless a path names it.
func composeSSHAccess(opts ComposeOptions, comp *Composition) error {
	entries := opts.Profile.Access.AuthorizedKeys
	if len(entries) == 0 {
		return nil
	}
	var lines []string
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if isSSHPublicKeyLiteral(e) {
			lines = append(lines, e)
			continue
		}
		path := expandHome(e, opts.HostHome)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("compose authorized_keys: read %s: %w", path, err)
		}
		for ln := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
			if ln = strings.TrimSpace(ln); ln != "" {
				lines = append(lines, ln)
			}
		}
	}
	if len(lines) == 0 {
		return nil
	}
	sshDir := filepath.Join(comp.StagingDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return fmt.Errorf("compose authorized_keys: mkdir .ssh: %w", err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(sshDir, "authorized_keys"), []byte(content), 0o600); err != nil {
		return fmt.Errorf("compose authorized_keys: write: %w", err)
	}
	return nil
}

// isSSHPublicKeyLiteral reports whether s is an inline public key (rather
// than a filesystem path to read one from).
func isSSHPublicKeyLiteral(s string) bool {
	for _, prefix := range []string{"ssh-", "ecdsa-sha2-", "sk-ssh-", "sk-ecdsa-"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// expandHome resolves a leading ~ or ~/ in path against home (the host home).
func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok && home != "" {
		return filepath.Join(home, rest)
	}
	return path
}

// writeClaudeJSON generates the guest ~/.claude.json. It carries the
// onboarding-complete marker (so claude doesn't prompt in a headless
// guest) plus any profile preferences the user routed here. Mode-A OAuth
// state lives in the bound .credentials.json, not here (see
// composeCredentials) — the minimal working file set is a spike task, so
// this stays deliberately small.
func writeClaudeJSON(stagingDir string, _ *Profile) error {
	doc := map[string]any{
		"hasCompletedOnboarding": true,
	}
	path := filepath.Join(stagingDir, ".claude.json")
	return writeJSONFile(path, doc)
}

// writeSettingsJSON generates the user-layer .claude/settings.json from
// the profile's preferences map. This is the layer ape fully authors
// inside the guest; the CLI --settings bridge hooks are injected
// separately and unchanged (they must survive every profile).
func writeSettingsJSON(claudeDir string, p *Profile) error {
	doc := map[string]any{}
	maps.Copy(doc, p.Preferences)
	return writeJSONFile(filepath.Join(claudeDir, "settings.json"), doc)
}

// composeSkills copies each profile skill into destDir. An entry is
// either a bare name (resolved under userSkillsDir) or an absolute path
// to a skill directory. The default is empty — nothing from the host
// user skills leaks in by omission.
func composeSkills(skills []string, userSkillsDir, destDir string) error {
	return composeNamedDir(skills, userSkillsDir, destDir, "skill", copyTree)
}

// composeAgents copies each profile agent. Agents are single <name>.md
// files (by-name → userAgentsDir/<name>.md) or an absolute path to a .md.
func composeAgents(agents []string, userAgentsDir, destDir string) error {
	if len(agents) == 0 {
		return nil
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("compose agents: mkdir %s: %w", destDir, err)
	}
	for _, a := range agents {
		var src, name string
		if filepath.IsAbs(a) {
			src = a
			name = filepath.Base(a)
		} else {
			if userAgentsDir == "" {
				return fmt.Errorf("compose agents: %q is a name but no host agents dir is available", a)
			}
			name = a
			if !strings.HasSuffix(name, ".md") {
				name += ".md"
			}
			src = filepath.Join(userAgentsDir, name)
		}
		if err := copyFile(src, filepath.Join(destDir, name)); err != nil {
			return fmt.Errorf("compose agent %q: %w", a, err)
		}
	}
	return nil
}

// composeNamedDir is the shared by-name-or-path directory copier used for
// skills (and any future dir-shaped component).
func composeNamedDir(items []string, userDir, destDir, kind string, cp func(src, dst string) error) error {
	if len(items) == 0 {
		return nil
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("compose %ss: mkdir %s: %w", kind, destDir, err)
	}
	for _, it := range items {
		var src, name string
		if filepath.IsAbs(it) {
			src, name = it, filepath.Base(it)
		} else {
			if userDir == "" {
				return fmt.Errorf("compose %ss: %q is a name but no host %s dir is available", kind, it, kind)
			}
			src, name = filepath.Join(userDir, it), it
		}
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("compose %s %q: %w", kind, it, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("compose %s %q: %s is not a directory", kind, it, src)
		}
		if err := cp(src, filepath.Join(destDir, name)); err != nil {
			return fmt.Errorf("compose %s %q: %w", kind, it, err)
		}
	}
	return nil
}

// composeCredentials wires the credential mode. Mode A records a rw bind
// of the host's real .credentials.json into the guest home (refresh
// writes back). Mode B resolves the scoped API key and injects it via
// env — no credential file touches the guest.
func composeCredentials(opts ComposeOptions, comp *Composition, _ string) error {
	switch opts.Profile.Credentials {
	case CredentialOAuth:
		if opts.HostHome == "" {
			return errors.New("compose: credentials: oauth needs a host home to bind the credentials file")
		}
		hostCred := filepath.Join(opts.HostHome, ".claude", ".credentials.json")
		if _, err := os.Stat(hostCred); err != nil {
			return fmt.Errorf("compose: mode-A credentials file not found at %s: %w", hostCred, err)
		}
		comp.Binds = append(comp.Binds, BindMount{
			Source:   hostCred,
			Dest:     filepath.Join(comp.GuestHome, ".claude", ".credentials.json"),
			ReadOnly: false, // token refresh writes back
		})
	case CredentialAPIKey:
		key, err := ResolveSecret(opts.Profile.APIKeySource)
		if err != nil {
			return fmt.Errorf("compose: resolve api key: %w", err)
		}
		comp.Env = append(comp.Env, "ANTHROPIC_API_KEY="+key)
	case CredentialNone:
		// No Anthropic credentials injected (ephemeral/preview or aped default).
	default:
		return fmt.Errorf("compose: unknown credential mode %q", opts.Profile.Credentials)
	}
	return nil
}

// writeJSONFile writes v as indented JSON with 0600 perms, creating
// parent dirs as needed.
func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// copyTree recursively copies src dir to dst, preserving the regular-file
// mode bits. Symlinks are skipped (a curated skill dir should be plain
// files; following links would defeat the "only what's named" guarantee).
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, 0o700)
		case info.Mode().IsRegular():
			return copyFile(path, target)
		default:
			// Skip symlinks, devices, sockets — not expected in a skill dir.
			return nil
		}
	})
}

// copyFile copies a single regular file, creating parent dirs. The
// destination is 0600 unless the source is executable, in which case
// 0700 (a skill may ship a helper script).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	info, err := in.Stat()
	if err != nil {
		return err
	}
	perm := os.FileMode(0o600)
	if info.Mode()&0o100 != 0 {
		perm = 0o700
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
