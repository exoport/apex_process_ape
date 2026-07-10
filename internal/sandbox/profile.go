// Package sandbox provisions and operates long-lived, hardware-isolated
// Kata microVM workspaces for local development (PLAN-16, Phase 1 of the
// APEX Process Platform). It is split into layers that build cleanly on
// every platform — profiles, per-workspace ~/.claude composition, the OCI
// config/spec builder, the egress proxy — and a Kata runner that only
// makes sense on Linux with containerd + Kata present. ape drives Kata by
// shelling out to nerdctl/ctr (no containerd Go client), so the runner is
// just command construction (pure, unit-tested everywhere) plus the
// exec.Command execution behind //go:build linux with a portable Windows
// stub, keeping `make test`/`make build` green on the Windows CI leg.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProfilesDirName is the project-relative directory that holds versioned,
// reviewable sandbox profiles: `_apex/sandbox/<name>.yaml`.
const ProfilesDirName = "_apex/sandbox"

// Backend selects the isolation backend. Phase 1 is kata-only (gVisor was
// dropped after the spike — see PLAN-16 "Spike findings").
type Backend string

// BackendKata is the only supported backend: a Kata microVM (own guest
// kernel, KVM), driven via containerd.
const BackendKata Backend = "kata"

// VMM selects the virtual machine monitor Kata launches the workspace
// with. Cloud-Hypervisor (clh) is the default; QEMU is reserved for the
// device tier (GPU/USB passthrough — a later phase).
type VMM string

const (
	// VMMCloudHypervisor is the default: fast, low-overhead, production-proven.
	VMMCloudHypervisor VMM = "clh"
	// VMMQemu is for the device tier (most mature GPU/USB VFIO passthrough).
	VMMQemu VMM = "qemu"
)

// MountMode selects how the project is made available inside the workspace
// (PLAN-16 D3 / the platform north-star's three modes).
type MountMode string

const (
	// MountHostFS shares the host project into the guest over virtio-fs —
	// edits reflect both ways. The local-dev default.
	MountHostFS MountMode = "host-fs"
	// MountVolume gives the workspace a persistent block volume for the
	// project, VM-owned and surviving pause/resume (server default).
	MountVolume MountMode = "volume"
	// MountEphemeral binds nothing from the host; the workspace clones the
	// repo in-guest and throws it away on teardown (untrusted/preview work).
	MountEphemeral MountMode = "ephemeral"
)

// CredentialMode selects how the guest authenticates to the Anthropic API.
type CredentialMode string

const (
	// CredentialOAuth (mode A) bind-mounts the host's real OAuth material
	// into the synthetic home — and nothing else from the real home.
	CredentialOAuth CredentialMode = "oauth"
	// CredentialAPIKey (mode B) injects a scoped ANTHROPIC_API_KEY via env;
	// no credential files touch the guest filesystem.
	CredentialAPIKey CredentialMode = "api-key"
	// CredentialNone injects no Anthropic credentials at all. It is the safe
	// default for server-provisioned (aped) ephemeral/preview workspaces where
	// the operator supplied no credential config — the guest boots without auth
	// rather than the daemon guessing at host credentials it does not have.
	CredentialNone CredentialMode = "none"
)

// GitMode selects how git credentials (if any) are composed for the job.
type GitMode string

const (
	// GitNone is the default: no git credentials. Read-only public clones
	// still work through the proxy when the domains are authorized.
	GitNone GitMode = "none"
	// GitToken serves a scoped token over HTTPS via a generated credential
	// helper. Recommended.
	GitToken GitMode = "token"
	// GitDeployKey mounts a dedicated per-project deploy key read-only.
	GitDeployKey GitMode = "deploy-key"
	// GitAgent bind-mounts the host ssh-agent socket (live signing
	// capability for the job's duration — see the D6 caveat in PLAN-16).
	GitAgent GitMode = "agent"
)

// Profile is the parsed `_apex/sandbox/<name>.yaml`. Field order and names
// mirror the schema documented in PLAN-16 D3 and reference/sandbox-profile.md.
//
//nolint:tagliatelle // snake_case is the stable, documented on-disk profile schema
type Profile struct {
	Name          string         `yaml:"name"`
	Backend       Backend        `yaml:"backend,omitempty"`
	VMM           VMM            `yaml:"vmm,omitempty"`
	Image         string         `yaml:"image,omitempty"`
	Mount         MountMode      `yaml:"mount,omitempty"`
	Credentials   CredentialMode `yaml:"credentials"`
	APIKeySource  string         `yaml:"api_key_source,omitempty"`
	Skills        []string       `yaml:"skills,omitempty"`
	Agents        []string       `yaml:"agents,omitempty"`
	Hooks         []string       `yaml:"hooks,omitempty"`
	ProjectSkills string         `yaml:"project_skills_overlay,omitempty"`
	IgnoreProject bool           `yaml:"ignore_project_settings,omitempty"`
	Preferences   map[string]any `yaml:"preferences,omitempty"`
	Network       NetworkPolicy  `yaml:"network"`
	Git           GitPolicy      `yaml:"git"`
	Mounts        MountPolicy    `yaml:"mounts"`
	Access        AccessPolicy   `yaml:"access,omitempty"`

	// path records where the profile was loaded from, for diagnostics.
	path string `yaml:"-"`
}

// AccessPolicy configures inbound access to the workspace (PLAN-16 D7).
//
//nolint:tagliatelle // snake_case is the stable, documented on-disk profile schema
type AccessPolicy struct {
	// AuthorizedKeys lists SSH public keys allowed to `ape sandbox ssh` into
	// the workspace. Each entry is either a public-key literal ("ssh-ed25519
	// AAAA… me@host") or a path to a .pub / authorized_keys file
	// ("~/.ssh/id_ed25519.pub"; a leading ~ expands to the host home). The
	// composer writes them to the guest ~/.ssh/authorized_keys. Empty → key
	// auth is unconfigured (use `ape sandbox attach`/`exec`).
	AuthorizedKeys []string `yaml:"authorized_keys,omitempty"`
}

// NetworkPolicy is the per-profile egress allowlist (PLAN-16 D4).
//
//nolint:tagliatelle // snake_case is the stable, documented on-disk profile schema
type NetworkPolicy struct {
	// AuthorizedDomains are hostnames (exact or leading-wildcard, e.g.
	// "*.githubusercontent.com") reachable over the CONNECT proxy on 443.
	AuthorizedDomains []string `yaml:"authorized_domains,omitempty"`
	// DirectAllow are fixed host:port pairs reachable directly in the
	// per-job netns (non-HTTP endpoints — NATS, github.com:22).
	DirectAllow []string `yaml:"direct_allow,omitempty"`
}

// GitPolicy composes git credentials for the job (PLAN-16 D5).
//
//nolint:tagliatelle // snake_case is the stable, documented on-disk profile schema
type GitPolicy struct {
	Mode        GitMode `yaml:"mode"`
	TokenSource string  `yaml:"token_source,omitempty"`
	DeployKey   string  `yaml:"deploy_key,omitempty"`
}

// MountPolicy carries extra writable bind mounts beyond the project root.
//
//nolint:tagliatelle // snake_case is the stable, documented on-disk profile schema
type MountPolicy struct {
	ExtraRW []string `yaml:"extra_rw,omitempty"`
}

// Path returns the file the profile was loaded from (empty when built
// in memory, e.g. in tests).
func (p *Profile) Path() string { return p.path }

// ProfilePath returns the on-disk path a named profile resolves to under
// projectRoot, without loading it.
func ProfilePath(projectRoot, name string) string {
	return filepath.Join(projectRoot, filepath.FromSlash(ProfilesDirName), name+".yaml")
}

// Load reads and validates the named profile from projectRoot's
// `_apex/sandbox/` directory. A missing file returns an error whose text
// names the expected path so a typo is diagnosable in one look.
func Load(projectRoot, name string) (*Profile, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("sandbox: profile name is empty")
	}
	// Reject path separators in the name — a profile is a bare filename
	// under _apex/sandbox/, never a traversal.
	if strings.ContainsAny(name, `/\`) || name == ".." {
		return nil, fmt.Errorf("sandbox: invalid profile name %q (must be a bare name, not a path)", name)
	}
	return LoadFile(ProfilePath(projectRoot, name))
}

// LoadFile reads and validates a profile from an explicit path.
func LoadFile(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("sandbox: profile not found at %s", path)
		}
		return nil, fmt.Errorf("sandbox: read profile %s: %w", path, err)
	}
	var p Profile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // a stray/misspelled key is a hard error, not a silent drop
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("sandbox: parse profile %s: %w", path, err)
	}
	p.path = path
	p.applyDefaults()
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("sandbox: invalid profile %s: %w", path, err)
	}
	return &p, nil
}

// applyDefaults fills the zero-value fields that have a documented default
// (backend kata, VMM clh, mount host-fs, git mode "none") so downstream
// code never has to special-case empties.
func (p *Profile) applyDefaults() {
	if p.Backend == "" {
		p.Backend = BackendKata
	}
	if p.VMM == "" {
		p.VMM = VMMCloudHypervisor
	}
	if p.Mount == "" {
		p.Mount = MountHostFS
	}
	if p.Git.Mode == "" {
		p.Git.Mode = GitNone
	}
}

// Validate enforces the cross-field invariants the schema promises. It is
// deliberately strict: a profile that would produce a broken or insecure
// guest should fail at load, not at run.
func (p *Profile) Validate() error {
	// Normalise first so Validate is self-sufficient: a profile built in
	// memory (e.g. in a test) validates the same as one through Load, which
	// applies defaults before calling here. applyDefaults is idempotent.
	p.applyDefaults()

	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name is required")
	}

	// Backend is kata-only in Phase 1 (gVisor was dropped). Reject anything
	// else loudly rather than silently ignoring it.
	if p.Backend != BackendKata {
		return fmt.Errorf("backend must be kata, got %q", p.Backend)
	}
	switch p.VMM {
	case VMMCloudHypervisor, VMMQemu:
		// ok
	default:
		return fmt.Errorf("vmm must be clh or qemu, got %q", p.VMM)
	}
	switch p.Mount {
	case MountHostFS, MountVolume, MountEphemeral:
		// ok
	default:
		return fmt.Errorf("mount must be host-fs, volume or ephemeral, got %q", p.Mount)
	}

	switch p.Credentials {
	case CredentialOAuth:
		if p.APIKeySource != "" {
			return errors.New("api_key_source is only valid with credentials: api-key")
		}
	case CredentialAPIKey:
		if strings.TrimSpace(p.APIKeySource) == "" {
			return errors.New("credentials: api-key requires api_key_source (e.g. env:APE_JOB_ANTHROPIC_KEY)")
		}
		if err := validateSecretSource(p.APIKeySource); err != nil {
			return fmt.Errorf("api_key_source: %w", err)
		}
	case CredentialNone:
		if p.APIKeySource != "" {
			return errors.New("api_key_source is only valid with credentials: api-key")
		}
	case "":
		return errors.New("credentials is required (oauth | api-key | none)")
	default:
		return fmt.Errorf("credentials must be oauth, api-key or none, got %q", p.Credentials)
	}

	// The top-level `hooks:` name-list is reserved for a future hook
	// registry. In v1 the generated user-layer settings.json is built from
	// `preferences` (which may itself carry a claude `hooks` block), so a
	// non-empty list here would silently do nothing — reject it instead.
	if len(p.Hooks) > 0 {
		return errors.New("hooks: name-list is not supported in v1 — express user-layer hooks under preferences.hooks")
	}

	if err := p.Git.validate(); err != nil {
		return err
	}

	for _, hp := range p.Network.DirectAllow {
		if err := validateHostPort(hp); err != nil {
			return fmt.Errorf("network.direct_allow %q: %w", hp, err)
		}
	}
	for _, d := range p.Network.AuthorizedDomains {
		if err := validateDomainPattern(d); err != nil {
			return fmt.Errorf("network.authorized_domains %q: %w", d, err)
		}
	}
	for i, k := range p.Access.AuthorizedKeys {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("access.authorized_keys[%d] is empty", i)
		}
	}
	return nil
}

func (g GitPolicy) validate() error {
	switch g.Mode {
	case GitNone, "":
		// Empty normalises to "none" (applyDefaults); nothing else required.
	case GitToken:
		if strings.TrimSpace(g.TokenSource) == "" {
			return errors.New("git.mode: token requires git.token_source")
		}
		if err := validateSecretSource(g.TokenSource); err != nil {
			return fmt.Errorf("git.token_source: %w", err)
		}
	case GitDeployKey:
		if strings.TrimSpace(g.DeployKey) == "" {
			return errors.New("git.mode: deploy-key requires git.deploy_key (host path)")
		}
	case GitAgent:
		// The socket comes from SSH_AUTH_SOCK at run time; nothing in the
		// profile to validate. The live-signing caveat is documented, not
		// enforced.
	default:
		return fmt.Errorf("git.mode must be none|token|deploy-key|agent, got %q", g.Mode)
	}
	return nil
}

// validateSecretSource accepts the `env:NAME` and `file:PATH` forms used
// by api_key_source / token_source. Everything else is rejected — a bare
// literal secret in a reviewable, committed profile is a footgun.
func validateSecretSource(src string) error {
	scheme, rest, ok := strings.Cut(src, ":")
	if !ok || rest == "" {
		return fmt.Errorf("must be env:NAME or file:PATH, got %q", src)
	}
	switch scheme {
	case "env", "file":
		return nil
	default:
		return fmt.Errorf("unsupported scheme %q (want env: or file:)", scheme)
	}
}

// validateHostPort checks a "host:port" pair for direct_allow. Ports must
// be numeric; hosts must be non-empty and wildcard-free (CDNs rot, so
// direct allows are fixed hosts only — PLAN-16 D4).
func validateHostPort(hp string) error {
	host, port, ok := strings.Cut(hp, ":")
	if !ok {
		return errors.New("expected host:port")
	}
	if host == "" {
		return errors.New("empty host")
	}
	if strings.Contains(host, "*") {
		return errors.New("wildcards not allowed for direct_allow (fixed hosts only)")
	}
	if port == "" {
		return errors.New("empty port")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return fmt.Errorf("non-numeric port %q", port)
		}
	}
	return nil
}

// validateDomainPattern checks an authorized_domains entry: an exact
// hostname or a single leading-wildcard label ("*.example.com").
func validateDomainPattern(d string) error {
	if d == "" {
		return errors.New("empty domain")
	}
	if strings.HasPrefix(d, "*.") {
		rest := d[2:]
		if rest == "" || strings.Contains(rest, "*") {
			return errors.New("only a single leading-wildcard label is supported")
		}
		return nil
	}
	if strings.Contains(d, "*") {
		return errors.New("wildcard only allowed as a leading label (*.example.com)")
	}
	return nil
}
