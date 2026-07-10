package sandbox

import (
	"context"
	"fmt"
	"io"

	"github.com/exoport/apex_process_ape/internal/workspace"
)

// shellDriver implements workspace.Backend for the local non-device tier by
// shelling out to nerdctl/ctr through the existing Runner (PLAN-18 D3, the
// PLAN-16 → PLAN-18 "shellDriver" migration). It reuses the verbatim argument
// builders (RunArgs/ExecArgs/AttachArgs/PauseArgs/ResumeArgs/StartArgs/
// StopArgs/DownArgs), so its Create/Start/Stop/Exec/Attach/Destroy/Freeze/
// Unfreeze produce byte-identical commands to the direct Runner path.
//
// Suspend/Resume/Snapshot are VMM save/restore and return ErrUnsupported on
// this tier (D7). Logs/Events/Inspect need the containerd task API and return
// ErrUnsupported until the Phase-3 containerdDriver; List is served from the
// on-disk Registry.
type shellDriver struct {
	runner  *Runner
	reg     *Registry // optional; nil → List/Inspect report ErrUnsupported
	resolve SpecResolver
}

// SpecResolver turns a thin wire CreateRequest into a fully-resolved
// WorkspaceSpec — the composed ~/.claude home, egress proxy, forwarded ports,
// and env, which are host-resolved (client-side today; server-side in `aped`,
// Phase 2) and never travel on the wire. It is the seam where composition and
// policy plug into Create.
type SpecResolver func(ctx context.Context, req workspace.CreateRequest) (WorkspaceSpec, error)

// NewShellDriver returns a shellDriver as a workspace.Backend. runner defaults
// to a zero Runner (the `nerdctl` binary, process streams); reg may be nil
// (List/Inspect then report ErrUnsupported); resolve is required for Create.
func NewShellDriver(runner *Runner, reg *Registry, resolve SpecResolver) workspace.Backend {
	if runner == nil {
		runner = &Runner{}
	}
	return &shellDriver{runner: runner, reg: reg, resolve: resolve}
}

// Compile-time proof the shell driver satisfies the transport-agnostic Backend.
var _ workspace.Backend = (*shellDriver)(nil)

// Capabilities reports the runtime handlers this tier can drive. Device/IOMMU
// probing (GPUs, USB, VFIO readiness) is the Phase-3 containerdDriver's job;
// the shell tier reports only its known Kata runtimes and host-fs support.
func (d *shellDriver) Capabilities(context.Context) (workspace.Capabilities, error) {
	return workspace.Capabilities{
		Runtimes: []workspace.RuntimeInfo{
			{Name: runtimeHandler(VMMCloudHypervisor), VMM: string(VMMCloudHypervisor), Default: true},
			{Name: runtimeHandler(VMMQemu), VMM: string(VMMQemu)},
		},
		HostFS: true,
	}, nil
}

// Create resolves the request to a WorkspaceSpec and provisions it detached
// (nerdctl run -d) — byte-identical to Runner.Provision.
func (d *shellDriver) Create(ctx context.Context, req workspace.CreateRequest) (workspace.Workspace, error) {
	if d.resolve == nil {
		return workspace.Workspace{}, fmt.Errorf("%w: shell driver has no spec resolver", workspace.ErrValidation)
	}
	spec, err := d.resolve(ctx, req)
	if err != nil {
		return workspace.Workspace{}, err
	}
	if err := d.runner.Provision(ctx, spec); err != nil {
		return workspace.Workspace{}, err
	}
	return workspaceFromSpec(req, spec), nil
}

// Start / Stop / Destroy / Freeze / Unfreeze map the id to its container name
// and delegate to the matching Runner verb (verbatim arg builders).
func (d *shellDriver) Start(ctx context.Context, id string) error {
	return d.runner.Start(ctx, ContainerName(id))
}

func (d *shellDriver) Stop(ctx context.Context, id string) error {
	return d.runner.Stop(ctx, ContainerName(id))
}

func (d *shellDriver) Destroy(ctx context.Context, id string, _ workspace.DestroyRequest) error {
	// nerdctl rm -f already force-removes; volume retention is the caller's
	// policy (unchanged from PLAN-16 `down`).
	return d.runner.Down(ctx, ContainerName(id))
}

// Freeze cgroup-freezes the guest (nerdctl pause; RAM stays resident).
func (d *shellDriver) Freeze(ctx context.Context, id string) error {
	return d.runner.Freeze(ctx, ContainerName(id))
}

// Unfreeze thaws a frozen guest (nerdctl unpause).
func (d *shellDriver) Unfreeze(ctx context.Context, id string) error {
	return d.runner.Unfreeze(ctx, ContainerName(id))
}

// Exec runs a one-shot command inside the workspace. req.Env is not applied on
// the shell tier (workspace env is fixed at Create; ExecArgs stays verbatim).
func (d *shellDriver) Exec(ctx context.Context, id string, req workspace.ExecRequest) (workspace.ExitStatus, error) {
	if err := d.runner.Exec(ctx, ContainerName(id), req.TTY, req.Cmd); err != nil {
		return workspace.ExitStatus{}, err
	}
	return workspace.ExitStatus{Code: 0}, nil
}

// Attach opens an interactive login shell, wiring the Stream's stdio through a
// per-call Runner copy (nil Stream falls back to the driver's own streams).
func (d *shellDriver) Attach(ctx context.Context, id string, req workspace.AttachRequest, s workspace.Stream) (workspace.ExitStatus, error) {
	runner := d.runner
	if s != nil {
		rr := *d.runner
		rr.Stdin, rr.Stdout, rr.Stderr = s.Stdin(), s.Stdout(), s.Stderr()
		runner = &rr
	}
	if err := runner.Attach(ctx, ContainerName(id), req.Shell); err != nil {
		return workspace.ExitStatus{}, err
	}
	return workspace.ExitStatus{Code: 0}, nil
}

// Suspend / Resume / Snapshot are VMM save/restore — unreachable through
// Kata-via-containerd (D7). They return ErrUnsupported on this tier.
func (d *shellDriver) Suspend(context.Context, string) error { return workspace.ErrUnsupported }

func (d *shellDriver) Resume(context.Context, string) error { return workspace.ErrUnsupported }

func (d *shellDriver) Snapshot(context.Context, string, workspace.SnapshotRequest) (workspace.SnapshotRef, error) {
	return workspace.SnapshotRef{}, workspace.ErrUnsupported
}

// Logs / Events need the containerd task API (Phase-3 containerdDriver).
func (d *shellDriver) Logs(context.Context, string, workspace.LogsRequest) (io.ReadCloser, error) {
	return nil, workspace.ErrUnsupported
}

func (d *shellDriver) Events(context.Context) (<-chan workspace.Event, error) {
	return nil, workspace.ErrUnsupported
}

// List enumerates registered workspaces from the on-disk Registry.
func (d *shellDriver) List(context.Context) ([]workspace.Workspace, error) {
	if d.reg == nil {
		return nil, workspace.ErrUnsupported
	}
	recs, err := d.reg.List()
	if err != nil {
		return nil, err
	}
	out := make([]workspace.Workspace, 0, len(recs))
	for i := range recs {
		out = append(out, workspaceFromRecord(recs[i]))
	}
	return out, nil
}

// Inspect reports a workspace's live state. On the shell tier live state needs
// the containerd task API (Phase-3 containerdDriver); until then it validates
// existence against the Registry and reports ErrUnsupported for the state.
func (d *shellDriver) Inspect(_ context.Context, id string) (workspace.Status, error) {
	if d.reg == nil {
		return workspace.Status{}, workspace.ErrUnsupported
	}
	_, ok, err := d.reg.Get(id)
	if err != nil {
		return workspace.Status{}, err
	}
	if !ok {
		return workspace.Status{}, fmt.Errorf("%w: %s", workspace.ErrNotFound, id)
	}
	// The record exists, but its live run-state is not queryable on this tier.
	return workspace.Status{}, workspace.ErrUnsupported
}

// workspaceFromSpec builds the wire record for a freshly-created workspace.
func workspaceFromSpec(req workspace.CreateRequest, spec WorkspaceSpec) workspace.Workspace {
	return workspace.Workspace{
		ID:      spec.Name,
		Name:    spec.Name,
		Image:   spec.Image,
		Runtime: runtimeName(spec.VMM),
		Mount:   string(spec.Mount),
		Profile: req.Profile,
		Devices: req.Devices,
	}
}

// workspaceFromRecord maps an on-disk Registry row to the wire record.
func workspaceFromRecord(r Workspace) workspace.Workspace {
	return workspace.Workspace{
		ID:        r.Name,
		Name:      r.Name,
		Image:     r.Image,
		Runtime:   runtimeName(VMM(r.VMM)),
		Mount:     r.Mount,
		Profile:   r.Profile,
		CreatedAt: r.CreatedAt,
	}
}

// runtimeName renders a VMM as the short runtime selector used on the wire
// (CreateRequest.Runtime / Workspace.Runtime): clh → kata-clh, qemu → kata-qemu.
func runtimeName(vmm VMM) string {
	if vmm == "" {
		return ""
	}
	return "kata-" + string(vmm)
}
