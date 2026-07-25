package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/exoport/apex_process_ape/internal/workspace"
)

// The project sandbox descriptor (PLAN-20 D3). ONE committable file per project
// describes the whole workspace — repos, extra mounts, egress, toolchain — instead
// of a new dotfile per concern. This file owns parsing + client-side resolution;
// each section is owned by its plan (repos/mounts → PLAN-20, egress → PLAN-21,
// toolchain → PLAN-22).
//
// Everything in it is a REQUEST, never a grant. Sources are canonicalized here,
// on the client, against the descriptor's own directory; aped then re-checks every
// resolved source against its mount-root allow-list and refuses reserved
// destinations. So a committed file can express intent but never exceed policy.

// DescriptorName is the committed per-project descriptor file name.
const DescriptorName = ".apesandbox.yaml"

// DescriptorVersion is the only schema version this build understands.
const DescriptorVersion = 1

// Guest-side layout constants.
const (
	// WorkspaceRoot is the reserved root every project repo is mounted under, as
	// /workspace/<name> — always, even for a single repo, so a second repo never
	// changes the first one's path.
	WorkspaceRoot = "/workspace"
	// FrameworkDest is where the pinned APEX framework is mounted read-only.
	FrameworkDest = "/opt/apex-framework"
	// UserMountRoot is the default parent for a user mount that declares no dest:
	// /mnt/<basename of source>.
	UserMountRoot = "/mnt"
)

// reservedDests are guest paths a USER mount may never target: they are system
// mounts aped applies on its own authority. /workspace is reserved as a whole
// subtree (a repo's path lives under it), the others exactly.
var reservedDests = []string{WorkspaceRoot, FrameworkDest, DefaultGuestHome}

// Descriptor is the parsed `.apesandbox.yaml`.
//
// Unknown TOP-LEVEL keys are deliberately ignored (yaml's default), so a newer
// project file carrying a section this build predates still loads instead of
// failing the workspace. Within a known section a misspelled key is likewise
// ignored rather than fatal — the cost of a stricter mode is that adding
// `toolchain:` in PLAN-22 would break every older ape.
type Descriptor struct {
	Version   int                  `yaml:"version"`
	Repos     []DescriptorRepo     `yaml:"repos,omitempty"`
	Mounts    []DescriptorMount    `yaml:"mounts,omitempty"`
	Egress    *DescriptorEgress    `yaml:"egress,omitempty"`
	Toolchain *DescriptorToolchain `yaml:"toolchain,omitempty"`

	// path records where the descriptor was loaded from (diagnostics + relative
	// source resolution).
	path string `yaml:"-"`
}

// DescriptorRepo is one project repository entry.
//
//nolint:tagliatelle // snake_case is the stable, documented on-disk schema
type DescriptorRepo struct {
	Source string `yaml:"source"`
	Name   string `yaml:"name,omitempty"`
	Main   bool   `yaml:"main,omitempty"`
	// ReadOnly defaults to false for repos: you work in them.
	ReadOnly bool `yaml:"readonly,omitempty"`
}

// DescriptorMount is one extra non-repo mount.
//
//nolint:tagliatelle // snake_case is the stable, documented on-disk schema
type DescriptorMount struct {
	Source string `yaml:"source"`
	Dest   string `yaml:"dest,omitempty"`
	// ReadOnly is a POINTER so an omitted key can default to true (read-only is the
	// safe default for a user mount) while `readonly: false` stays meaningful.
	ReadOnly *bool `yaml:"readonly,omitempty"`
}

// DescriptorEgress is the egress section (PLAN-21): a request that aped
// intersects with its own policy.
//
//nolint:tagliatelle // snake_case is the stable, documented on-disk schema
type DescriptorEgress struct {
	AuthorizedDomains []string `yaml:"authorized_domains,omitempty"`
	DirectAllow       []string `yaml:"direct_allow,omitempty"`
}

// DescriptorToolchain is the toolchain section (PLAN-22): asdf runtimes + the
// repo's pinned Go tools, referenced rather than duplicated.
//
//nolint:tagliatelle // snake_case is the stable, documented on-disk schema
type DescriptorToolchain struct {
	// ToolVersions is a repo-relative path to an asdf .tool-versions file.
	ToolVersions string `yaml:"tool_versions,omitempty"`
	// Bingo installs the repo's .bingo-pinned Go tools.
	Bingo bool `yaml:"bingo,omitempty"`
	// Tools inlines "plugin version" pairs when a project prefers not to commit a
	// separate .tool-versions.
	Tools []string `yaml:"tools,omitempty"`
	// Caches names the durable host caches this project wants mounted (see
	// toolchain.go). Empty with a toolchain declared → DefaultToolCaches. The names
	// are validated against a closed table; the guest paths and env they imply are
	// resolved server-side.
	Caches []string `yaml:"caches,omitempty"`
}

// Path returns the file the descriptor was loaded from ("" when synthesized).
func (d *Descriptor) Path() string { return d.path }

// Dir returns the descriptor's directory — the base for relative sources.
func (d *Descriptor) Dir() string {
	if d.path == "" {
		return ""
	}
	return filepath.Dir(d.path)
}

// DescriptorPath returns the descriptor path for a project root.
func DescriptorPath(projectRoot string) string {
	return filepath.Join(projectRoot, DescriptorName)
}

// FindDescriptor returns the descriptor path for projectRoot when it exists.
func FindDescriptor(projectRoot string) (string, bool) {
	p := DescriptorPath(projectRoot)
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p, true
	}
	return "", false
}

// LoadDescriptor reads and validates a descriptor from an explicit file path.
func LoadDescriptor(file string) (*Descriptor, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("sandbox: no %s at %s", DescriptorName, file)
		}
		return nil, fmt.Errorf("sandbox: read %s: %w", file, err)
	}
	var d Descriptor
	// KnownFields is deliberately NOT enabled — see the Descriptor doc comment.
	if err := yaml.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("sandbox: parse %s: %w", file, err)
	}
	d.path = file
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("sandbox: invalid %s: %w", file, err)
	}
	return &d, nil
}

// Validate checks the descriptor's internal invariants: a supported version, at
// most one main repo, unique and safe repo names, absolute-and-unreserved
// destinations, and well-formed egress patterns.
func (d *Descriptor) Validate() error {
	if d.Version == 0 {
		return fmt.Errorf("version is required (expected %d)", DescriptorVersion)
	}
	if d.Version != DescriptorVersion {
		return fmt.Errorf("unsupported version %d (this ape understands %d)", d.Version, DescriptorVersion)
	}

	mains := 0
	seenExplicit := map[string]bool{}
	for i := range d.Repos {
		r := &d.Repos[i]
		if strings.TrimSpace(r.Source) == "" {
			return fmt.Errorf("repos[%d]: source is required", i)
		}
		// Only an EXPLICIT name can be validated here. An omitted one is derived from
		// the resolved absolute source — `source: .` has no meaningful basename until
		// it is made absolute — so its validation (and the duplicate check across all
		// names) happens in Resolve.
		if explicit := strings.TrimSpace(r.Name); explicit != "" {
			if err := ValidateMountName(explicit); err != nil {
				return fmt.Errorf("repos[%d]: %w", i, err)
			}
			if seenExplicit[explicit] {
				return fmt.Errorf("repos[%d]: duplicate repo name %q (each maps to %s/<name>)", i, explicit, WorkspaceRoot)
			}
			seenExplicit[explicit] = true
		}
		if r.Main {
			mains++
		}
	}
	if mains > 1 {
		return errors.New("repos: exactly one entry may set main: true")
	}
	if mains == 0 && len(d.Repos) > 1 {
		return errors.New("repos: one entry must set main: true (it sets the working directory)")
	}

	for i := range d.Mounts {
		m := &d.Mounts[i]
		if strings.TrimSpace(m.Source) == "" {
			return fmt.Errorf("mounts[%d]: source is required", i)
		}
		if m.Dest != "" {
			if err := validateGuestDest(m.Dest); err != nil {
				return fmt.Errorf("mounts[%d]: %w", i, err)
			}
		}
	}

	if d.Toolchain != nil {
		if _, err := NormalizeToolCaches(d.Toolchain.Caches); err != nil {
			return fmt.Errorf("toolchain.caches: %w", err)
		}
	}

	if d.Egress != nil {
		for _, dom := range d.Egress.AuthorizedDomains {
			if err := validateDomainPattern(dom); err != nil {
				return fmt.Errorf("egress.authorized_domains %q: %w", dom, err)
			}
		}
		for _, hp := range d.Egress.DirectAllow {
			if err := validateHostPort(hp); err != nil {
				return fmt.Errorf("egress.direct_allow %q: %w", hp, err)
			}
		}
	}
	return nil
}

// MountName returns the repo's mount name: its explicit name, else the basename
// of its source (so `{source: ., …}` in /home/me/app becomes "app").
func (r DescriptorRepo) MountName() string {
	if n := strings.TrimSpace(r.Name); n != "" {
		return n
	}
	return filepath.Base(filepath.Clean(r.Source))
}

// EgressDomains returns the requested egress domains (nil-safe).
func (d *Descriptor) EgressDomains() []string {
	if d == nil || d.Egress == nil {
		return nil
	}
	return d.Egress.AuthorizedDomains
}

// ---- resolution ------------------------------------------------------------

// ResolvedDescriptor is a descriptor turned into wire-ready values: canonical
// absolute sources, explicit destinations, and one repo flagged main.
type ResolvedDescriptor struct {
	Repos  []workspace.RepoMount
	Mounts []workspace.MountSpec
	Egress *workspace.EgressRequest
}

// Resolve canonicalizes the descriptor against baseDir (its own directory, or
// projectRoot for a synthesized descriptor) and returns the wire values.
//
// Canonicalization happens HERE, on the client, for a reason: aped runs with
// ProtectHome=yes and cannot resolve a caller's relative path, and a path it
// cannot resolve it must not trust. Every source is made absolute, symlink-
// resolved, and confirmed to exist, so the wire only ever carries real paths.
func (d *Descriptor) Resolve(baseDir string) (ResolvedDescriptor, error) {
	if baseDir == "" {
		baseDir = d.Dir()
	}
	var out ResolvedDescriptor

	seenNames := make(map[string]bool, len(d.Repos))
	for i := range d.Repos {
		r := &d.Repos[i]
		src, err := resolveSource(baseDir, r.Source)
		if err != nil {
			return ResolvedDescriptor{}, fmt.Errorf("repos[%d] (%s): %w", i, r.Source, err)
		}
		// The name is derived from the RESOLVED source when not given, so `source: .`
		// becomes the project directory's own name.
		name := strings.TrimSpace(r.Name)
		if name == "" {
			name = filepath.Base(src)
		}
		if err := ValidateMountName(name); err != nil {
			return ResolvedDescriptor{}, fmt.Errorf("repos[%d] (%s): %w", i, r.Source, err)
		}
		if seenNames[name] {
			return ResolvedDescriptor{}, fmt.Errorf("repos[%d]: duplicate repo name %q (each maps to %s/<name>)",
				i, name, WorkspaceRoot)
		}
		seenNames[name] = true
		out.Repos = append(out.Repos, workspace.RepoMount{
			Source: src, Name: name, Main: r.Main, ReadOnly: r.ReadOnly,
		})
	}
	// A single repo is implicitly main — requiring the flag for the common
	// one-repo case would be noise.
	if len(out.Repos) == 1 {
		out.Repos[0].Main = true
	}

	for i := range d.Mounts {
		m := &d.Mounts[i]
		src, err := resolveSource(baseDir, m.Source)
		if err != nil {
			return ResolvedDescriptor{}, fmt.Errorf("mounts[%d] (%s): %w", i, m.Source, err)
		}
		ro := true // safe default for a user mount
		if m.ReadOnly != nil {
			ro = *m.ReadOnly
		}
		out.Mounts = append(out.Mounts, workspace.MountSpec{
			Source: src, Dest: defaultDest(m.Dest, src), ReadOnly: ro,
		})
	}

	if d.Egress != nil && (len(d.Egress.AuthorizedDomains) > 0 || len(d.Egress.DirectAllow) > 0) {
		out.Egress = &workspace.EgressRequest{
			AuthorizedDomains: SortedDomains(d.Egress.AuthorizedDomains),
			DirectAllow:       d.Egress.DirectAllow,
		}
	}
	return out, nil
}

// MergeUserMounts merges CLI-declared mounts over file-declared ones, later
// winning by destination, and returns the result sorted by dest for a stable
// wire payload. Reserved destinations are refused here (fail fast, with the file
// or flag named) — aped re-checks them regardless.
func MergeUserMounts(fileMounts, cliMounts []workspace.MountSpec) ([]workspace.MountSpec, error) {
	byDest := make(map[string]workspace.MountSpec, len(fileMounts)+len(cliMounts))
	order := make([]string, 0, len(fileMounts)+len(cliMounts))
	for _, m := range append(append([]workspace.MountSpec(nil), fileMounts...), cliMounts...) {
		if err := validateGuestDest(m.Dest); err != nil {
			return nil, err
		}
		if _, seen := byDest[m.Dest]; !seen {
			order = append(order, m.Dest)
		}
		byDest[m.Dest] = m
	}
	sort.Strings(order)
	out := make([]workspace.MountSpec, 0, len(order))
	for _, dest := range order {
		out = append(out, byDest[dest])
	}
	return out, nil
}

// ParseMountFlag parses a `--mount <source>[:<dest>][:ro|:rw]` value. The mode
// suffix is optional and defaults to ro (matching the descriptor's default); an
// omitted dest defaults to /mnt/<basename>.
//
// Windows-style sources ("C:\path") are not supported here: the source is a path
// on the aped HOST, which is Linux.
func ParseMountFlag(baseDir, value string) (workspace.MountSpec, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return workspace.MountSpec{}, errors.New("sandbox: --mount is empty")
	}
	parts := strings.Split(raw, ":")
	if len(parts) > 3 {
		return workspace.MountSpec{}, fmt.Errorf("sandbox: --mount %q: expected <source>[:<dest>][:ro|:rw]", value)
	}

	ro := true
	if len(parts) > 1 {
		switch last := parts[len(parts)-1]; last {
		case "ro":
			ro, parts = true, parts[:len(parts)-1]
		case "rw":
			ro, parts = false, parts[:len(parts)-1]
		}
	}
	src, err := resolveSource(baseDir, parts[0])
	if err != nil {
		return workspace.MountSpec{}, fmt.Errorf("sandbox: --mount %q: %w", value, err)
	}
	dest := ""
	if len(parts) > 1 {
		dest = parts[1]
	}
	dest = defaultDest(dest, src)
	if err := validateGuestDest(dest); err != nil {
		return workspace.MountSpec{}, fmt.Errorf("sandbox: --mount %q: %w", value, err)
	}
	return workspace.MountSpec{Source: src, Dest: dest, ReadOnly: ro}, nil
}

// RepoDest returns the guest path a repo name mounts at.
func RepoDest(name string) string { return path.Join(WorkspaceRoot, name) }

// ValidateUserMountDest re-checks a USER mount destination server-side: absolute,
// clean, and not a reserved system-mount path. The client enforces this too, to
// fail fast with the file/flag named — but aped is the authority, because the wire
// request is caller input like any other.
func ValidateUserMountDest(dest string) error { return validateGuestDest(dest) }

// ValidateMountName bounds a repo mount name to a single safe path segment: it
// becomes a guest directory under /workspace and must not escape it.
func ValidateMountName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid repo name %q", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("repo name %q must be a single path segment", name)
	}
	return nil
}

// resolveSource makes a source absolute against baseDir, resolves symlinks, and
// confirms it exists — the three things that let aped treat the value as a real
// host path instead of caller input.
func resolveSource(baseDir, src string) (string, error) {
	s := strings.TrimSpace(src)
	if s == "" {
		return "", errors.New("source is empty")
	}
	if strings.HasPrefix(s, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot expand ~: %w", err)
		}
		s = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(s, "~"), string(filepath.Separator)))
	}
	if !filepath.IsAbs(s) {
		if baseDir == "" {
			return "", fmt.Errorf("relative source %q with no project root to resolve against", src)
		}
		s = filepath.Join(baseDir, s)
	}
	resolved, err := filepath.EvalSymlinks(s)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: %w", s, err)
	}
	return filepath.Clean(resolved), nil
}

// defaultDest returns the declared dest, or /mnt/<basename of source>.
func defaultDest(dest, source string) string {
	if d := strings.TrimSpace(dest); d != "" {
		return path.Clean(d)
	}
	return path.Join(UserMountRoot, filepath.Base(source))
}

// validateGuestDest rejects a destination that is relative, unclean, or targets a
// system mount. Guest paths are always slash-separated, so this uses path (not
// filepath) — a Windows client must produce the same guest paths as a Linux one.
func validateGuestDest(dest string) error {
	d := strings.TrimSpace(dest)
	if d == "" {
		return errors.New("mount dest is empty")
	}
	if !strings.HasPrefix(d, "/") {
		return fmt.Errorf("mount dest %q must be absolute", dest)
	}
	clean := path.Clean(d)
	if clean != d {
		return fmt.Errorf("mount dest %q must be a clean path (got %q, want %q)", dest, d, clean)
	}
	for _, res := range reservedDests {
		if clean == res || strings.HasPrefix(clean, res+"/") {
			return fmt.Errorf("mount dest %q is reserved: %s is a system mount aped applies itself "+
				"(the framework, the composed home, and project repos cannot be redirected)", dest, res)
		}
	}
	return nil
}
