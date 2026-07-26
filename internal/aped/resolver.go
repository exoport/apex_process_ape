package aped

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

// guestCredsRel is where the per-VM .creds is bind-mounted inside the guest,
// relative to the guest $HOME. The in-VM ape resolves APE_NATS_CREDS to it and
// derives its identity offline (PLAN-13 D1 / PLAN-18 D6).
//
//nolint:gosec // G101 false positive: a bind-mount path, not a credential
const guestCredsRel = ".config/ape/vm.creds"

// cacheDirMode is the mode a durable tool cache dir is created with: group-writable
// so the host operator (in the `ape` group) can pre-warm a cache the guest — which
// writes as root through virtiofsd — also uses.
const cacheDirMode = 0o775

// Resolver turns a thin wire CreateRequest into a fully-resolved WorkspaceSpec,
// de-privileged, in aped-front (PLAN-18 D1). It reuses the PLAN-16 pure layers
// (Compose) and mints + injects a per-VM telemetry credential (D2/D6). Only the
// resolved spec crosses the AF_UNIX boundary to the executor.
type Resolver struct {
	stateDir    string
	hostHome    string
	natsURL     string
	credsExpiry time.Duration
	telemetry   Account
	network     string // nerdctl --network for provisioned specs (default NetworkNone)
	egress      EgressPlanner
	// frameworkRoot/frameworkRef locate the APEX framework this node serves: a host
	// directory holding one materialized checkout per ref. Empty root → the node
	// serves no framework and the mount is simply absent.
	frameworkRoot string
	frameworkRef  string
	// cacheRoot is the host directory holding the durable tool caches (PLAN-22 D4).
	// Empty → this node offers no caching and a cache request is ignored.
	cacheRoot string
	// apeBin is the verified `ape` this node delivers into every workspace (PLAN-23).
	// Resolved and checked once at startup, so a create never has to wonder. Zero value →
	// no delivery, which only happens in tests: the daemon refuses to start without one,
	// since a workspace with no `ape` is broken rather than degraded.
	apeBin        ApeBinary
	apeBinRecheck func() (ApeBinary, error)
	// credentials is the credential mode this node composes into workspaces when the
	// resolved profile does not state one. It is NODE configuration, never a request
	// field: which host identity a workspace gets is the operator's decision, not the
	// caller's. Empty → CredentialNone (inject nothing).
	credentials sandbox.CredentialMode

	// Injectable seams (default to the real implementations) so Resolve is
	// unit-testable without touching a profile file or the compose filesystem.
	loadProfile func(name string) (*sandbox.Profile, error)
	compose     func(sandbox.ComposeOptions) (*sandbox.Composition, error)
}

// EgressPlanner intersects a workspace's requested egress domains with node
// policy and stands up the CONNECT proxy that serves them (implemented by
// EgressSupervisor). It is an interface so the resolver is testable without
// binding a listener.
type EgressPlanner interface {
	Plan(name string, requested []string) (EgressPlan, error)
}

// ResolverConfig configures NewResolver.
type ResolverConfig struct {
	StateDir    string        // aped state dir (staging homes + per-VM creds live here)
	HostHome    string        // host home Compose sources ~/.claude from
	NatsURL     string        // guest-facing APE_NATS_URL ("" → per-VM creds skipped)
	CredsExpiry time.Duration // per-VM JWT lifetime (0 → no expiry)
	Telemetry   Account       // mints per-VM telemetry creds
	// Network is the nerdctl --network mode for provisioned workspaces. Empty
	// defaults to sandbox.NetworkNone: Phase-2 workspaces are networkless so
	// nerdctl's client-side CNI stays out of the hardened executor (PLAN-18 D1).
	// Overlay connectivity is Phase 3.
	Network string
	// Egress plans + supervises proxied egress (PLAN-21 D1/D2). Nil denies every
	// egress request: a workspace asking for domains on a node with no egress
	// support is told so, rather than silently booting networkless.
	Egress EgressPlanner
	// FrameworkRoot is the host directory holding materialized framework refs, one
	// subdirectory per ref (PLAN-20 D5). Empty disables the framework mount.
	FrameworkRoot string
	// FrameworkRef is the default ref to mount when a request names none.
	FrameworkRef string
	// CacheRoot is the host directory holding durable tool caches, one subdir per
	// cache (PLAN-22 D4). Empty disables cache mounts.
	CacheRoot string
	// ApeBin is the verified `ape` binary this node delivers into workspaces (PLAN-23),
	// resolved once at startup by ResolveApeBinary. The zero value disables delivery and
	// exists for tests only — the daemon will not start without a verified binary.
	ApeBin ApeBinary
	// ApeBinRecheck re-verifies the delivered binary at create time. Nil skips the
	// re-check and delivers ApeBin as resolved at startup.
	ApeBinRecheck func() (ApeBinary, error)
	// Credentials is the default credential mode for provisioned workspaces
	// ("oauth" composes the host credential published under HostHome; "none" injects
	// nothing). Empty → none, the fail-closed default.
	Credentials sandbox.CredentialMode
	// LoadProfile is an optional server-side profile source (by name). When nil,
	// the resolver builds a default profile from the request fields.
	LoadProfile func(name string) (*sandbox.Profile, error)
}

// NewResolver builds a Resolver.
func NewResolver(cfg ResolverConfig) *Resolver {
	network := cfg.Network
	if network == "" {
		network = sandbox.NetworkNone
	}
	return &Resolver{
		stateDir:      cfg.StateDir,
		hostHome:      cfg.HostHome,
		natsURL:       cfg.NatsURL,
		credsExpiry:   cfg.CredsExpiry,
		telemetry:     cfg.Telemetry,
		network:       network,
		egress:        cfg.Egress,
		frameworkRoot: cfg.FrameworkRoot,
		frameworkRef:  cfg.FrameworkRef,
		cacheRoot:     cfg.CacheRoot,
		apeBin:        cfg.ApeBin,
		apeBinRecheck: cfg.ApeBinRecheck,
		credentials:   cfg.Credentials,
		loadProfile:   cfg.LoadProfile,
		compose:       sandbox.Compose,
	}
}

// Resolve composes the staging home, resolves image/VMM/mount, and mints +
// injects the per-VM credential, returning the spec the executor provisions.
func (r *Resolver) Resolve(_ context.Context, req workspace.CreateRequest) (sandbox.WorkspaceSpec, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return sandbox.WorkspaceSpec{}, fmt.Errorf("%w: name is required", workspace.ErrValidation)
	}

	prof, err := r.profileFor(req)
	if err != nil {
		return sandbox.WorkspaceSpec{}, err
	}

	staging := sandbox.StagingDirFor(r.stateDir, name)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return sandbox.WorkspaceSpec{}, fmt.Errorf("aped: create staging home: %w", err)
	}
	comp, err := r.compose(sandbox.ComposeOptions{Profile: prof, StagingDir: staging, HostHome: r.hostHome})
	if err != nil {
		return sandbox.WorkspaceSpec{}, err
	}

	image := req.Image
	if image == "" {
		image = sandbox.ResolveImage(prof)
	}
	spec := sandbox.WorkspaceSpec{
		Name:    name,
		Image:   image,
		VMM:     prof.VMM,
		Mount:   prof.Mount,
		Network: r.network,
		Comp:    comp,
	}
	switch prof.Mount {
	case sandbox.MountHostFS:
		if strings.TrimSpace(req.MountSource) == "" {
			return sandbox.WorkspaceSpec{}, fmt.Errorf("%w: host-fs mount requires mount_source", workspace.ErrValidation)
		}
		spec.ProjectRoot = req.MountSource
	case sandbox.MountVolume:
		spec.Volume = sandbox.ContainerName(name) + "-workspace"
	case sandbox.MountEphemeral:
		// nothing from the host
	default:
		return sandbox.WorkspaceSpec{}, fmt.Errorf("%w: unknown mount mode %q", workspace.ErrValidation, prof.Mount)
	}

	if err := r.resolveMounts(&spec, req); err != nil {
		return sandbox.WorkspaceSpec{}, err
	}

	if err := r.resolveEgress(&spec, prof, req); err != nil {
		return sandbox.WorkspaceSpec{}, err
	}

	if err := r.injectVMCreds(name, comp); err != nil {
		return sandbox.WorkspaceSpec{}, err
	}

	// Hand the same environment to LOGIN shells (PLAN-23 D9). exec/attach inherit the
	// container env; an ssh or VS Code Remote session does not, because sshd builds a fresh
	// one — so without this a workspace reached over ssh has no GOPATH/GOBIN and quietly
	// writes to the ephemeral rootfs instead of the durable caches. Written last, once the
	// spec's env is complete, and allowlisted so credential material stays out of a file.
	if err := sandbox.WriteGuestProfileEnv(comp.StagingDir, append(append([]string(nil), comp.Env...), spec.Env...)); err != nil {
		return sandbox.WorkspaceSpec{}, err
	}
	return spec, nil
}

// resolveMounts assembles the workspace's bind list as `system ++ user` (PLAN-20).
//
// SYSTEM mounts are aped's own: the read-only APEX framework (its source resolved
// from THIS DAEMON's framework root, never from the request) and the project
// repos. USER mounts are the additive requests from the project's
// `.apesandbox.yaml` and `--mount` flags. The two are kept in that order and the
// system entries are appended first, so a user entry can never shadow one — and
// the executor re-checks the whole list against policy regardless.
func (r *Resolver) resolveMounts(spec *sandbox.WorkspaceSpec, req workspace.CreateRequest) error {
	// 1. The framework: present by default, read-only, independent of any request.
	fw, served, err := r.frameworkMount(req.FrameworkRef)
	if err != nil {
		return err
	}
	if served {
		spec.Mounts = append(spec.Mounts, fw)
	}

	// 1b. The `ape` binary: read-only, from THIS daemon's installation, never the request
	// (PLAN-23). Delivered at runtime rather than baked into the image so a workspace runs
	// the ape that matches the daemon provisioning it — the image would otherwise carry
	// whatever release was current when it was built, which is not the one whose fix you
	// upgraded for. The directory is mounted, not the file: it is what the rest of this
	// stack does, and it leaves room to add completions without a new reserved dest.
	if r.apeBin.Dir != "" {
		bin := r.apeBin
		if r.apeBinRecheck != nil {
			fresh, rerr := r.apeBinRecheck()
			if rerr != nil {
				return rerr
			}
			bin = fresh
		}
		spec.Mounts = append(spec.Mounts, workspace.MountSpec{
			Source:   bin.Dir,
			Dest:     sandbox.ApeBinDest,
			ReadOnly: true,
		})
		spec.ApeVersion = bin.Version
	}

	// 2. Project repos, each at /workspace/<name>. A single-repo request (the
	// pre-PLAN-20 shape) is derived from the host-fs mount source, so an old client
	// keeps working unchanged.
	repos := req.Repos
	if len(repos) == 0 && spec.Mount == sandbox.MountHostFS && strings.TrimSpace(spec.ProjectRoot) != "" {
		repos = []workspace.RepoMount{{
			Source: spec.ProjectRoot,
			Name:   filepath.Base(filepath.Clean(spec.ProjectRoot)),
			Main:   true,
		}}
	}
	mainSeen := false
	for i := range repos {
		rp := &repos[i]
		if strings.TrimSpace(rp.Source) == "" {
			return fmt.Errorf("%w: repo %q has no source", workspace.ErrValidation, rp.Name)
		}
		name := strings.TrimSpace(rp.Name)
		if name == "" {
			name = filepath.Base(filepath.Clean(rp.Source))
		}
		if err := sandbox.ValidateMountName(name); err != nil {
			return fmt.Errorf("%w: %w", workspace.ErrValidation, err)
		}
		dest := sandbox.RepoDest(name)
		spec.Mounts = append(spec.Mounts, workspace.MountSpec{Source: rp.Source, Dest: dest, ReadOnly: rp.ReadOnly})
		if rp.Main || len(repos) == 1 {
			if mainSeen {
				return fmt.Errorf("%w: more than one repo is flagged main", workspace.ErrValidation)
			}
			mainSeen = true
			spec.Cwd = dest
			// Keep ProjectRoot pointing at the main repo: it is what the policy check,
			// the registry record, and `ape sandbox ls` report as the workspace's project.
			spec.ProjectRoot = rp.Source
		}
	}
	if len(repos) > 1 && !mainSeen {
		return fmt.Errorf("%w: a multi-repo workspace must flag exactly one repo main", workspace.ErrValidation)
	}

	// 3. Durable tool caches (PLAN-22 D4). The request is a set of NAMES; the host
	// source, guest path, and the env that points each toolchain at it come from
	// aped's own table + cache root, so a caller cannot redirect GOPATH or
	// ASDF_DATA_DIR at a path of its choosing.
	if err := r.resolveCaches(spec, req.Caches); err != nil {
		return err
	}

	// 4. User mounts — additive only. Reserved destinations are refused here as well
	// as client-side, so a hand-crafted wire request cannot slip one through.
	for _, m := range req.Mounts {
		if err := sandbox.ValidateUserMountDest(m.Dest); err != nil {
			return fmt.Errorf("%w: %w", workspace.ErrPolicyDenied, err)
		}
		if !filepath.IsAbs(m.Source) {
			return fmt.Errorf("%w: mount source %q must be an absolute host path", workspace.ErrValidation, m.Source)
		}
		spec.Mounts = append(spec.Mounts, m)
	}
	return nil
}

// resolveCaches mounts the requested durable tool caches read-write and appends
// the environment that points each toolchain at its cache.
//
// The caches are created on demand under the node's cache root: a node that offers
// caching should not fail a create because nobody has run a Go build there yet.
// A node with no cache root configured ignores the request — the workspace simply
// keeps its state in the (ephemeral) rootfs, which is the pre-PLAN-22 behaviour.
func (r *Resolver) resolveCaches(spec *sandbox.WorkspaceSpec, requested []string) error {
	if len(requested) == 0 {
		return nil
	}
	if strings.TrimSpace(r.cacheRoot) == "" {
		return nil
	}
	names, err := sandbox.NormalizeToolCaches(requested)
	if err != nil {
		return fmt.Errorf("%w: %w", workspace.ErrValidation, err)
	}
	for _, name := range names {
		tc, err := sandbox.LookupToolCache(name)
		if err != nil {
			return fmt.Errorf("%w: %w", workspace.ErrValidation, err)
		}
		src := filepath.Join(r.cacheRoot, tc.SubDir)
		if err := os.MkdirAll(src, cacheDirMode); err != nil {
			return fmt.Errorf("aped: create tool cache %s: %w", src, err)
		}
		spec.Mounts = append(spec.Mounts, workspace.MountSpec{Source: src, Dest: tc.Dest, ReadOnly: false})
		spec.Env = append(spec.Env, tc.Env...)
	}
	return nil
}

// frameworkMount resolves the read-only framework system mount for a requested
// ref, or nil when this node serves no framework.
//
// The ref is a REQUEST for one of the refs the node has already materialized; the
// SOURCE is always <frameworkRoot>/<ref>, resolved here. That is what makes the
// mount unforgeable: a project can ask for a different version, never a different
// path. A missing ref is a clear, actionable error — aped never fetches (it holds
// no credentials, and a workspace must be buildable offline).
func (r *Resolver) frameworkMount(ref string) (mount workspace.MountSpec, served bool, err error) {
	if strings.TrimSpace(r.frameworkRoot) == "" {
		return workspace.MountSpec{}, false, nil // this node does not serve the framework
	}
	want := strings.TrimSpace(ref)
	if want == "" {
		want = r.frameworkRef
	}
	if want == "" {
		return workspace.MountSpec{}, false, nil
	}
	if err := sandbox.ValidateMountName(want); err != nil {
		return workspace.MountSpec{}, false, fmt.Errorf("%w: framework ref: %w", workspace.ErrValidation, err)
	}
	src := filepath.Join(r.frameworkRoot, want)
	st, serr := os.Stat(src)
	if serr != nil || !st.IsDir() {
		return workspace.MountSpec{}, false, fmt.Errorf(
			"%w: framework ref %q is not materialized on this node (expected %s). "+
				"Materialize it host-side: ape sandbox framework materialize %s",
			workspace.ErrValidation, want, src, want)
	}
	return workspace.MountSpec{Source: src, Dest: sandbox.FrameworkDest, ReadOnly: true}, true, nil
}

// resolveEgress folds the workspace's granted egress into the spec (PLAN-21 D1).
//
// The request is the union of what the server-side profile declares and what the
// project asked for on the wire (its `.apesandbox.yaml` egress: section). Both are
// REQUESTS: EgressPlanner intersects them with node policy, so the spec only ever
// carries domains policy already permits — and the executor re-checks that set
// again before provisioning.
//
// spec.Network stays NetworkNone even with egress granted: the guest reaches the
// world through the pre-wired netns the executor attaches, so if that wiring ever
// fails the workspace boots NETWORKLESS instead of falling back to an open bridge.
func (r *Resolver) resolveEgress(spec *sandbox.WorkspaceSpec, prof *sandbox.Profile, req workspace.CreateRequest) error {
	requested := sandbox.SortedDomains(append(append([]string(nil), prof.Network.AuthorizedDomains...), req.Egress.Domains()...))
	if len(requested) == 0 {
		return nil
	}
	if r.egress == nil {
		return fmt.Errorf("%w: this node does not provide workspace egress "+
			"(aped front --egress-bridge-ip / policy egress.enabled)", workspace.ErrPolicyDenied)
	}
	plan, err := r.egress.Plan(spec.Name, requested)
	if err != nil {
		return err
	}
	spec.EgressDomains = plan.Domains
	spec.HTTPSProxy = plan.ProxyURL
	return nil
}

// injectVMCreds mints a per-VM telemetry credential and injects it as a
// read-only .creds bind + APE_NATS_URL/APE_NATS_CREDS env (D2/D6). With no NATS
// URL configured it is a no-op — the workspace still boots, the in-VM agent
// just doesn't start (D6: agent launch is gated on creds presence).
func (r *Resolver) injectVMCreds(name string, comp *sandbox.Composition) error {
	if r.natsURL == "" {
		return nil
	}
	creds, _, err := MintVMCreds(r.telemetry, name, r.credsExpiry)
	if err != nil {
		return err
	}
	credsPath := filepath.Join(r.stateDir, "creds", name+".creds")
	if err := writeSecret(credsPath, creds); err != nil {
		return err
	}
	// The bind Dest is a guest (Linux) path — always POSIX-separated,
	// regardless of the host OS aped is analysed/built on. Use path.Join,
	// not filepath.Join (which would emit backslashes on Windows).
	guestPath := path.Join(comp.GuestHome, guestCredsRel)
	comp.Binds = append(comp.Binds, sandbox.BindMount{Source: credsPath, Dest: guestPath, ReadOnly: true})
	comp.Env = append(
		comp.Env,
		natsconn.EnvURL+"="+r.natsURL,
		natsconn.EnvCreds+"="+guestPath,
	)
	return nil
}

// profileFor resolves the profile for a request: a named server-side profile
// (when a loader is configured) or a default built from the request fields,
// with request fields overriding either way.
func (r *Resolver) profileFor(req workspace.CreateRequest) (*sandbox.Profile, error) {
	var prof *sandbox.Profile
	if r.loadProfile != nil && strings.TrimSpace(req.Profile) != "" {
		p, err := r.loadProfile(req.Profile)
		if err != nil {
			return nil, err
		}
		prof = p
	} else {
		prof = &sandbox.Profile{}
	}
	if rt := strings.TrimSpace(req.Runtime); rt != "" {
		prof.VMM = vmmFromRuntime(rt)
	}
	if m := strings.TrimSpace(req.Mount); m != "" {
		prof.Mount = sandbox.MountMode(m)
	}
	if prof.VMM == "" {
		prof.VMM = sandbox.VMMCloudHypervisor
	}
	if prof.Mount == "" {
		prof.Mount = sandbox.MountHostFS
	}
	if prof.Credentials == "" {
		// The node decides which host identity a workspace may receive, because that is
		// an operator grant and not something a caller should be able to ask for. With
		// nothing configured this stays CredentialNone — aped guesses no credentials it
		// was not told to hand out. A named profile that sets credentials still wins.
		prof.Credentials = r.credentials
		if prof.Credentials == "" {
			prof.Credentials = sandbox.CredentialNone
		}
	}
	return prof, nil
}

// vmmFromRuntime maps a wire runtime selector (kata-qemu | kata-clh) to a VMM.
func vmmFromRuntime(runtime string) sandbox.VMM {
	switch strings.TrimPrefix(runtime, "kata-") {
	case "qemu":
		return sandbox.VMMQemu
	case "clh":
		return sandbox.VMMCloudHypervisor
	default:
		return sandbox.VMMCloudHypervisor
	}
}
