package aped

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"gopkg.in/yaml.v3"
)

// Policy is aped's default-deny authorization boundary (PLAN-18 D9) — the real
// trust boundary, distinct from the subject authz that scopes who can reach the
// service at all. It binds what a fully-resolved command may request: allowed
// image refs (prefer digests), the canonical roots a host-fs mount must fall
// under (re-checked after symlink resolution — the CVE lesson), resource
// ceilings, and the device allowlist (the highest-value escalation target).
// The root executor loads it at startup and re-validates every command against
// it; the front-end pre-checks with the same rules to fail fast.
//
//nolint:tagliatelle // snake_case is the on-disk policy.yaml contract
type Policy struct {
	// Images is the allow-list of acceptable image references. Empty means no
	// image may be created (default-deny). Prefer digest-pinned refs.
	Images []string `yaml:"images"`
	// MountRoots are canonical host path prefixes a host-fs mount must resolve
	// under. Empty means host-fs mounts are refused (default-deny); volume and
	// ephemeral mounts are unaffected.
	MountRoots []string `yaml:"mount_roots"`
	// MountRootsRO are roots that may only ever be mounted READ-ONLY: a request for
	// a writable bind under one of them is denied (PLAN-20 D4). It lets an operator
	// export a shared directory to every workspace without letting any of them
	// write to it. Entries here do NOT need to be repeated in MountRoots — being
	// listed authorizes the root, read-only.
	MountRootsRO []string `yaml:"mount_roots_ro"`
	// Limits are optional resource ceilings; a zero field means "no ceiling".
	Limits Limits `yaml:"limits"`
	// Devices is the passthrough allow-list (Phase 3). Empty → every device
	// request is denied (correct default-deny for the non-device tier).
	Devices DevicePolicy `yaml:"devices"`
	// Egress bounds public network egress (PLAN-21). Absent/disabled → every
	// workspace stays networkless, the pre-PLAN-21 behaviour.
	Egress EgressPolicy `yaml:"egress"`
}

// EgressPolicy is the node's egress authorization: whether workspaces may have
// public egress at all, and the outer bound on which domains they may reach. A
// project's `.apesandbox.yaml` / profile asks for domains; aped intersects the
// request with AllowedDomains, so a project can narrow this set, never widen it.
//
//nolint:tagliatelle // snake_case is the on-disk policy.yaml contract
type EgressPolicy struct {
	// Enabled turns proxied egress on for this node. False (the default) denies
	// every egress request — the fail-closed posture.
	Enabled bool `yaml:"enabled"`
	// AllowedDomains is the outer allow-list. Entries are exact hostnames or a
	// single leading wildcard ("*.githubusercontent.com"); a wildcard entry
	// authorizes anything inside its suffix. Empty denies every domain even when
	// Enabled is true (default-deny, so enabling egress alone grants nothing).
	AllowedDomains []string `yaml:"allowed_domains"`
	// MaxDomains caps how many domains one workspace may be granted (0 → no cap).
	// It bounds blast radius when a project commits a sprawling allowlist.
	MaxDomains int `yaml:"max_domains"`
}

// Limits are per-workspace resource ceilings (0 = unlimited).
//
//nolint:tagliatelle // snake_case is the on-disk policy.yaml contract
type Limits struct {
	MaxVCPUs      int `yaml:"max_vcpus"`
	MaxMemMB      int `yaml:"max_mem_mb"`
	MaxWorkspaces int `yaml:"max_workspaces"`
	// MaxMounts caps the resolved mount list per workspace (0 → no ceiling). It
	// bounds fan-out when a project commits a sprawling `.apesandbox.yaml`.
	MaxMounts int `yaml:"max_mounts"`
}

// DevicePolicy is the passthrough allow-list: which PCI BDFs and which USB
// vendor:product pairs may be requested. Enforced from Phase 3; present now so
// the non-device tier's default-deny of any device request is explicit.
type DevicePolicy struct {
	PCI []string `yaml:"pci"`
	USB []string `yaml:"usb"`
}

// ResolvedCreate is the fully-resolved create the executor validates — canonical
// image + host path + concrete resources, never the thin wire request. Policy
// authorizes the concrete parsed request, never a summary (the CVE lesson, D9).
type ResolvedCreate struct {
	Image     string
	MountPath string // canonical host-fs mount path; "" for volume/ephemeral
	VCPUs     int
	MemMB     int
	Devices   []workspace.Device
	// EgressDomains is the GRANTED allowlist the resolved spec carries (PLAN-21).
	// The executor re-checks it against policy rather than trusting the front's
	// intersection — same lesson as the mount path: authorize the concrete value.
	EgressDomains []string
	// Mounts is the resolved bind list (PLAN-20), checked PER ENTRY: each source
	// against the mount-root allow-list, each destination against the reserved set,
	// and each writable request against the read-only roots. MountPath above stays
	// the primary project bind, so a pre-PLAN-20 create is authorized identically.
	Mounts []workspace.MountSpec
}

// LoadPolicy reads and validates a policy.yaml. A missing or malformed file is
// an error — the executor must never run without a policy (fail-closed).
func LoadPolicy(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("aped: read policy %s: %w", path, err)
	}
	var p Policy
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // reject unknown keys — a typo must not silently widen policy
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("aped: parse policy %s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks the policy is internally sane (non-negative ceilings). An
// empty policy is valid — it simply denies everything.
func (p *Policy) Validate() error {
	if p.Limits.MaxVCPUs < 0 || p.Limits.MaxMemMB < 0 || p.Limits.MaxWorkspaces < 0 || p.Limits.MaxMounts < 0 {
		return errors.New("aped: policy limits must be non-negative")
	}
	if p.Egress.MaxDomains < 0 {
		return errors.New("aped: policy egress.max_domains must be non-negative")
	}
	// A malformed pattern must fail at load, not silently authorize nothing (or,
	// worse, look like it authorizes something it does not).
	for _, d := range p.Egress.AllowedDomains {
		if err := sandbox.ValidateDomainPattern(d); err != nil {
			return fmt.Errorf("aped: policy egress.allowed_domains %q: %w", d, err)
		}
	}
	return nil
}

// CheckCreate authorizes a fully-resolved create against the policy, with
// currentCount workspaces already live (for the count ceiling). It returns a
// wrapped workspace.ErrPolicyDenied on any default-deny miss, so the vmm
// handler renders it as the DENIED wire code.
func (p *Policy) CheckCreate(rc ResolvedCreate, currentCount int) error {
	if err := p.checkImage(rc.Image); err != nil {
		return err
	}
	if err := p.checkMount(rc.MountPath); err != nil {
		return err
	}
	if err := p.checkMounts(rc.Mounts); err != nil {
		return err
	}
	if err := p.checkLimits(rc, currentCount); err != nil {
		return err
	}
	if err := p.checkEgress(rc.EgressDomains); err != nil {
		return err
	}
	return p.checkDevices(rc.Devices)
}

// checkEgress denies a resolved create whose granted allowlist exceeds policy:
// egress disabled, a domain outside the allowed set, or more domains than the cap.
// No egress requested is always allowed (a networkless workspace needs nothing).
func (p *Policy) checkEgress(domains []string) error {
	if len(domains) == 0 {
		return nil
	}
	if !p.Egress.Enabled {
		return fmt.Errorf("%w: network egress is disabled on this node (policy egress.enabled)", workspace.ErrPolicyDenied)
	}
	if p.Egress.MaxDomains > 0 && len(domains) > p.Egress.MaxDomains {
		return fmt.Errorf("%w: %d egress domains exceeds the ceiling of %d",
			workspace.ErrPolicyDenied, len(domains), p.Egress.MaxDomains)
	}
	_, refused := sandbox.IntersectDomains(domains, p.Egress.AllowedDomains)
	if len(refused) > 0 {
		return fmt.Errorf("%w: egress domain(s) %s are not in the policy allow-list",
			workspace.ErrPolicyDenied, strings.Join(refused, ", "))
	}
	return nil
}

func (p *Policy) checkImage(image string) error {
	if slices.Contains(p.Images, image) {
		return nil
	}
	return fmt.Errorf("%w: image %q is not in the policy allow-list", workspace.ErrPolicyDenied, image)
}

// protectHomeMaskedRoots are the paths systemd ProtectHome=yes renders empty and
// inaccessible. aped runs under it (deploy/systemd), so a host-fs mount source
// below one of these is invisible to the daemon — it cannot even canonicalize
// the path for the policy check, let alone bind it. Kept as a hint source so a
// mount that hits this fails with actionable guidance, not a raw lstat error.
var protectHomeMaskedRoots = []string{"/home", "/root"}

// checkMount denies a host-fs mount whose canonical (symlink-resolved) path is
// not under an allowed root. An empty MountPath (volume/ephemeral) is allowed.
func (p *Policy) checkMount(mountPath string) error {
	if mountPath == "" {
		return nil
	}
	resolved, err := canonicalPath(mountPath)
	if err != nil {
		if hint := protectHomeHint(mountPath, err); hint != "" {
			return fmt.Errorf("%w: %s", workspace.ErrValidation, hint)
		}
		return fmt.Errorf("%w: mount path %q: %s", workspace.ErrValidation, mountPath, err.Error())
	}
	// A read-only root authorizes the path too — just not a writable bind of it
	// (checkMounts enforces that separately), so an operator can export a shared
	// directory without also having to list it in mount_roots.
	for _, root := range append(append([]string(nil), p.MountRoots...), p.MountRootsRO...) {
		croot, err := canonicalPath(root)
		if err != nil {
			continue // a broken configured root cannot authorize anything
		}
		if pathUnder(resolved, croot) {
			return nil
		}
	}
	return fmt.Errorf("%w: mount path %q (resolved %q) is not under an allowed root", workspace.ErrPolicyDenied, mountPath, resolved)
}

// checkMounts authorizes the resolved bind list entry by entry (PLAN-20 D4).
//
// Nothing here trusts the front's own merge: every source is re-canonicalized and
// re-checked against the mount roots, every destination against the reserved set,
// and a writable bind under a read-only root is denied. The framework mount is
// exempt from the mount-root check ONLY when it is read-only and lands on its
// reserved destination — it is a system mount whose source aped resolved itself,
// not a caller path.
func (p *Policy) checkMounts(mounts []workspace.MountSpec) error {
	if p.Limits.MaxMounts > 0 && len(mounts) > p.Limits.MaxMounts {
		return fmt.Errorf("%w: %d mounts exceeds the ceiling of %d",
			workspace.ErrPolicyDenied, len(mounts), p.Limits.MaxMounts)
	}
	seen := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		if strings.TrimSpace(m.Dest) == "" {
			return fmt.Errorf("%w: mount of %q has no destination", workspace.ErrValidation, m.Source)
		}
		if seen[m.Dest] {
			return fmt.Errorf("%w: duplicate mount destination %q", workspace.ErrValidation, m.Dest)
		}
		seen[m.Dest] = true

		if m.Dest == sandbox.FrameworkDest {
			if !m.ReadOnly {
				return fmt.Errorf("%w: the framework mount %q must be read-only", workspace.ErrPolicyDenied, m.Dest)
			}
			continue
		}
		if err := p.checkMount(m.Source); err != nil {
			return err
		}
		if !m.ReadOnly {
			if root, ro := p.readOnlyRootFor(m.Source); ro {
				return fmt.Errorf("%w: mount %q is under the read-only root %q and cannot be writable",
					workspace.ErrPolicyDenied, m.Source, root)
			}
		}
	}
	return nil
}

// readOnlyRootFor reports whether a source falls under a read-only mount root.
func (p *Policy) readOnlyRootFor(source string) (string, bool) {
	resolved, err := canonicalPath(source)
	if err != nil {
		return "", false // an unresolvable path is already denied by checkMount
	}
	for _, root := range p.MountRootsRO {
		croot, err := canonicalPath(root)
		if err != nil {
			continue
		}
		if pathUnder(resolved, croot) {
			return root, true
		}
	}
	return "", false
}

func (p *Policy) checkLimits(rc ResolvedCreate, currentCount int) error {
	if p.Limits.MaxVCPUs > 0 && rc.VCPUs > p.Limits.MaxVCPUs {
		return fmt.Errorf("%w: %d vCPUs exceeds the ceiling of %d", workspace.ErrPolicyDenied, rc.VCPUs, p.Limits.MaxVCPUs)
	}
	if p.Limits.MaxMemMB > 0 && rc.MemMB > p.Limits.MaxMemMB {
		return fmt.Errorf("%w: %d MB exceeds the ceiling of %d", workspace.ErrPolicyDenied, rc.MemMB, p.Limits.MaxMemMB)
	}
	if p.Limits.MaxWorkspaces > 0 && currentCount >= p.Limits.MaxWorkspaces {
		return fmt.Errorf("%w: at the workspace ceiling of %d", workspace.ErrPolicyDenied, p.Limits.MaxWorkspaces)
	}
	return nil
}

// checkDevices denies any requested device not in the allow-list. Exactly one
// of PCI/USB is set per Device (the wire contract); an all-empty Device is a
// shape error.
func (p *Policy) checkDevices(devices []workspace.Device) error {
	for _, d := range devices {
		switch {
		case d.PCI != "" && d.USB != "":
			return fmt.Errorf("%w: device sets both pci and usb", workspace.ErrValidation)
		case d.PCI != "":
			if !slices.Contains(p.Devices.PCI, d.PCI) {
				return fmt.Errorf("%w: PCI device %q is not in the policy allow-list", workspace.ErrPolicyDenied, d.PCI)
			}
		case d.USB != "":
			if !slices.Contains(p.Devices.USB, d.USB) {
				return fmt.Errorf("%w: USB device %q is not in the policy allow-list", workspace.ErrPolicyDenied, d.USB)
			}
		default:
			return fmt.Errorf("%w: device sets neither pci nor usb", workspace.ErrValidation)
		}
	}
	return nil
}

// canonicalPath resolves symlinks and cleans a path so policy authorizes the
// real target, not a symlink that could later be repointed. The path must
// exist (server-side host paths do).
func canonicalPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// protectHomeHint returns actionable guidance when a host-fs mount source cannot
// be canonicalized because it lives under a ProtectHome-masked root (/home,
// /root) — the daemon runs with ProtectHome=yes and literally cannot see it. It
// fires when the path is under a masked root OR the failure was a permission
// error (the shape systemd's mask produces), and is empty otherwise so a genuine
// bad path keeps its raw error.
func protectHomeHint(mountPath string, err error) string {
	abs, aerr := filepath.Abs(mountPath)
	if aerr != nil {
		abs = mountPath
	}
	masked := false
	for _, root := range protectHomeMaskedRoots {
		if pathUnder(abs, root) {
			masked = true
			break
		}
	}
	if !masked && !errors.Is(err, fs.ErrPermission) {
		return ""
	}
	return fmt.Sprintf("host-fs mount path %q is not reachable by aped (%s): the daemon runs with "+
		"ProtectHome=yes, so paths under /home and /root are invisible to it. Mount from a root outside "+
		"/home and add it to policy mount_roots, or expose the directory with a systemd BindPaths= drop-in "+
		"on aped.service and aped-front.service (see docs/how-to/run-aped.md), or use --mount ephemeral|volume",
		mountPath, err.Error())
}

// pathUnder reports whether path is root itself or lies within it, using
// path-segment comparison (so /home/bob is not "under" /home/bo).
func pathUnder(path, root string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
