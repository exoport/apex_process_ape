package aped

import (
	"context"
	"fmt"
	"os"
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

	// Injectable seams (default to the real implementations) so Resolve is
	// unit-testable without touching a profile file or the compose filesystem.
	loadProfile func(name string) (*sandbox.Profile, error)
	compose     func(sandbox.ComposeOptions) (*sandbox.Composition, error)
}

// ResolverConfig configures NewResolver.
type ResolverConfig struct {
	StateDir    string        // aped state dir (staging homes + per-VM creds live here)
	HostHome    string        // host home Compose sources ~/.claude from
	NatsURL     string        // guest-facing APE_NATS_URL ("" → per-VM creds skipped)
	CredsExpiry time.Duration // per-VM JWT lifetime (0 → no expiry)
	Telemetry   Account       // mints per-VM telemetry creds
	// LoadProfile is an optional server-side profile source (by name). When nil,
	// the resolver builds a default profile from the request fields.
	LoadProfile func(name string) (*sandbox.Profile, error)
}

// NewResolver builds a Resolver.
func NewResolver(cfg ResolverConfig) *Resolver {
	return &Resolver{
		stateDir:    cfg.StateDir,
		hostHome:    cfg.HostHome,
		natsURL:     cfg.NatsURL,
		credsExpiry: cfg.CredsExpiry,
		telemetry:   cfg.Telemetry,
		loadProfile: cfg.LoadProfile,
		compose:     sandbox.Compose,
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
		Name:  name,
		Image: image,
		VMM:   prof.VMM,
		Mount: prof.Mount,
		Comp:  comp,
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

	if err := r.injectVMCreds(name, comp); err != nil {
		return sandbox.WorkspaceSpec{}, err
	}
	return spec, nil
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
	guestPath := filepath.Join(comp.GuestHome, guestCredsRel)
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
