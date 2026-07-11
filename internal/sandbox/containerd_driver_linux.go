//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"syscall"
	"time"

	client "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/exoport/apex_process_ape/internal/workspace"
)

// containerdDriver drives non-device Kata-QEMU workspaces through the containerd
// v2 Go client instead of shelling out to nerdctl (PLAN-18 D3). It exists to fix
// the executor-sandbox dead end: the nerdctl shellDriver's Create does a
// client-side mount(2) to resolve the image user/GIDs (oci.WithImageConfig →
// WithAdditionalGIDs temp-mounts the rootfs), which the hardened executor forbids
// (@mount denied, empty capability set). This driver builds the OCI spec as a
// typed object WITHOUT that mount (applyImageConfig) and leaves all
// snapshot/rootfs mounting to the containerd daemon + Kata shim — their own
// privileged units. It is OPT-IN (`aped run --driver containerd`); the default
// stays the shellDriver.
//
// The full lifecycle is validated live (Tier 2, a KVM+containerd+Kata host); the
// barrier-3-free spec construction is Tier-1 tested (imagespec_test.go).
type containerdDriver struct {
	cli     *client.Client
	ns      string
	reg     *Registry
	resolve SpecResolver
}

var _ ProvisioningBackend = (*containerdDriver)(nil)

// NewContainerdDriver dials containerd and returns the driver. The caller must
// Close it. It does not verify Kata is installed — that surfaces at Create.
func NewContainerdDriver(cfg ContainerdConfig) (ProvisioningBackend, error) {
	addr := cfg.Address
	if addr == "" {
		addr = DefaultContainerdAddress
	}
	ns := cfg.Namespace
	if ns == "" {
		ns = DefaultContainerdNamespace
	}
	cli, err := client.New(addr, client.WithDefaultNamespace(ns))
	if err != nil {
		return nil, fmt.Errorf("containerd driver: connect %s: %w", addr, err)
	}
	return &containerdDriver{cli: cli, ns: ns, reg: cfg.Registry, resolve: cfg.Resolve}, nil
}

// nsctx binds the containerd namespace onto ctx (every client call needs it).
func (d *containerdDriver) nsctx(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, d.ns)
}

// Close releases the client connection.
func (d *containerdDriver) Close() error { return d.cli.Close() }

// Capabilities reports the Kata runtime handlers this tier can drive (device/
// IOMMU probing is the Phase-3 device work).
func (d *containerdDriver) Capabilities(context.Context) (workspace.Capabilities, error) {
	return workspace.Capabilities{
		Runtimes: []workspace.RuntimeInfo{
			{Name: runtimeHandler(VMMCloudHypervisor), VMM: string(VMMCloudHypervisor), Default: true},
			{Name: runtimeHandler(VMMQemu), VMM: string(VMMQemu)},
		},
		HostFS: true,
	}, nil
}

// Create resolves a wire request to a spec and provisions it. aped drives the
// driver via Provision (resolving front-side), so it configures no resolver and
// Create reports ErrValidation — mirroring NewShellDriver(runner, reg, nil).
func (d *containerdDriver) Create(ctx context.Context, req workspace.CreateRequest) (workspace.Workspace, error) {
	if d.resolve == nil {
		return workspace.Workspace{}, fmt.Errorf("%w: containerd driver has no spec resolver", workspace.ErrValidation)
	}
	spec, err := d.resolve(ctx, req)
	if err != nil {
		return workspace.Workspace{}, err
	}
	return d.Provision(ctx, spec)
}

// Provision pulls the image, builds the OCI spec WITHOUT a client-side rootfs
// mount, creates the Kata container + a detached task, and records it in the
// registry. This is the barrier-3-free replacement for `nerdctl run -d`.
func (d *containerdDriver) Provision(ctx context.Context, spec WorkspaceSpec) (workspace.Workspace, error) {
	ctx = d.nsctx(ctx)
	if strings.TrimSpace(spec.Image) == "" {
		return workspace.Workspace{}, fmt.Errorf("%w: image is empty", workspace.ErrValidation)
	}

	img, err := d.getOrPull(ctx, spec.Image)
	if err != nil {
		return workspace.Workspace{}, err
	}
	cfg, err := imageConfig(ctx, img)
	if err != nil {
		return workspace.Workspace{}, err
	}

	id := spec.Container()
	specOpts := []oci.SpecOpts{
		oci.WithDefaultSpec(),
		oci.WithDefaultUnixDevices,
		oci.WithMounts(containerdMounts(spec)),
		withImageConfigNoMount(cfg, spec.Command, containerdEnv(spec), spec.Network == NetworkNone),
	}
	container, err := d.cli.NewContainer(
		ctx, id,
		client.WithRuntime(runtimeHandler(spec.VMM), nil),
		client.WithNewSnapshot(id+"-snapshot", img),
		client.WithNewSpec(specOpts...),
		client.WithContainerLabels(map[string]string{"ape.managed": "true", "ape.workspace": spec.Name}),
	)
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("containerd driver: create container %s: %w", id, err)
	}

	task, err := container.NewTask(ctx, cio.NullIO)
	if err != nil {
		_ = container.Delete(ctx, client.WithSnapshotCleanup)
		return workspace.Workspace{}, fmt.Errorf("containerd driver: create task %s: %w", id, err)
	}
	if err := task.Start(ctx); err != nil {
		_, _ = task.Delete(ctx)
		_ = container.Delete(ctx, client.WithSnapshotCleanup)
		return workspace.Workspace{}, fmt.Errorf("containerd driver: start task %s: %w", id, err)
	}

	if d.reg != nil {
		rec := Workspace{
			Name: spec.Name, Container: id, Backend: "containerd",
			VMM: string(spec.VMM), Image: spec.Image, Mount: string(spec.Mount),
			ProjectRoot: spec.ProjectRoot, Volume: spec.Volume,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if spec.Comp != nil {
			rec.StagingDir = spec.Comp.StagingDir
		}
		if err := d.reg.Put(rec); err != nil {
			return workspace.Workspace{}, fmt.Errorf("containerd driver: registry write for %s: %w", spec.Name, err)
		}
	}
	return workspace.Workspace{
		ID: spec.Name, Name: spec.Name, Image: spec.Image,
		Runtime: runtimeName(spec.VMM), Mount: string(spec.Mount),
	}, nil
}

// getOrPull returns an already-present image or pulls+unpacks it. A found image
// that was imported WITHOUT unpacking (e.g. `ctr images import`, which does not
// unpack) has no snapshots, so WithNewSnapshot would fail with an opaque error;
// unpack it for the default snapshotter first. "" resolves to the same default
// snapshotter WithNewSnapshot uses. Best-effort: an unpack failure is left to
// surface at snapshot creation with a clearer message than we could add here.
func (d *containerdDriver) getOrPull(ctx context.Context, ref string) (client.Image, error) {
	if img, err := d.cli.GetImage(ctx, ref); err == nil {
		if unpacked, uerr := img.IsUnpacked(ctx, ""); uerr == nil && !unpacked {
			_ = img.Unpack(ctx, "")
		}
		return img, nil
	}
	img, err := d.cli.Pull(ctx, ref, client.WithPullUnpack)
	if err != nil {
		return nil, fmt.Errorf("containerd driver: pull %s: %w", ref, err)
	}
	return img, nil
}

// Start (re)starts a stopped workspace's task; a running one is left as-is.
func (d *containerdDriver) Start(ctx context.Context, id string) error {
	ctx = d.nsctx(ctx)
	container, err := d.cli.LoadContainer(ctx, ContainerName(id))
	if err != nil {
		return mapContainerdErr(err)
	}
	if task, err := container.Task(ctx, nil); err == nil {
		st, serr := task.Status(ctx)
		if serr == nil && st.Status == client.Running {
			return nil // already running
		}
		return task.Start(ctx)
	}
	task, err := container.NewTask(ctx, cio.NullIO)
	if err != nil {
		return fmt.Errorf("containerd driver: new task %s: %w", id, err)
	}
	return task.Start(ctx)
}

// Stop kills and deletes the task, leaving the container + snapshot so Start can
// bring it back.
func (d *containerdDriver) Stop(ctx context.Context, id string) error {
	ctx = d.nsctx(ctx)
	task, err := d.loadTask(ctx, id)
	if err != nil {
		return err
	}
	return d.killTask(ctx, task)
}

// Freeze / Unfreeze are the containerd cgroup-freeze (guest RAM stays resident).
func (d *containerdDriver) Freeze(ctx context.Context, id string) error {
	ctx = d.nsctx(ctx)
	task, err := d.loadTask(ctx, id)
	if err != nil {
		return err
	}
	return task.Pause(ctx)
}

func (d *containerdDriver) Unfreeze(ctx context.Context, id string) error {
	ctx = d.nsctx(ctx)
	task, err := d.loadTask(ctx, id)
	if err != nil {
		return err
	}
	return task.Resume(ctx)
}

// Destroy kills the task and deletes the container + its snapshot.
func (d *containerdDriver) Destroy(ctx context.Context, id string, _ workspace.DestroyRequest) error {
	ctx = d.nsctx(ctx)
	container, err := d.cli.LoadContainer(ctx, ContainerName(id))
	if err != nil {
		if errdefs.IsNotFound(err) {
			d.regRemove(id)
			return nil // already gone
		}
		return mapContainerdErr(err)
	}
	if task, terr := container.Task(ctx, nil); terr == nil {
		_ = d.killTask(ctx, task)
	}
	if err := container.Delete(ctx, client.WithSnapshotCleanup); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("containerd driver: delete container %s: %w", id, err)
	}
	d.regRemove(id)
	return nil
}

// Exec runs a one-shot command in the workspace and returns its exit code. Bulk
// stdio rides the NATS exec-session subjects, not this call, so it uses NullIO
// and reports only the status (PLAN-18 D2 interactive path is Tier-2).
func (d *containerdDriver) Exec(ctx context.Context, id string, req workspace.ExecRequest) (workspace.ExitStatus, error) {
	ctx = d.nsctx(ctx)
	container, err := d.cli.LoadContainer(ctx, ContainerName(id))
	if err != nil {
		return workspace.ExitStatus{}, mapContainerdErr(err)
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return workspace.ExitStatus{}, mapContainerdErr(err)
	}
	spec, err := container.Spec(ctx)
	if err != nil {
		return workspace.ExitStatus{}, fmt.Errorf("containerd driver: load spec %s: %w", id, err)
	}
	pspec := *spec.Process
	pspec.Args = req.Cmd
	pspec.Terminal = false

	execID := fmt.Sprintf("ape-exec-%d", time.Now().UnixNano())
	process, err := task.Exec(ctx, execID, &pspec, cio.NullIO)
	if err != nil {
		return workspace.ExitStatus{}, fmt.Errorf("containerd driver: exec %s: %w", id, err)
	}
	defer func() { _, _ = process.Delete(ctx) }()

	statusC, err := process.Wait(ctx)
	if err != nil {
		return workspace.ExitStatus{}, fmt.Errorf("containerd driver: wait exec %s: %w", id, err)
	}
	if err := process.Start(ctx); err != nil {
		return workspace.ExitStatus{}, fmt.Errorf("containerd driver: start exec %s: %w", id, err)
	}
	status := <-statusC
	code, _, err := status.Result()
	if err != nil {
		return workspace.ExitStatus{}, fmt.Errorf("containerd driver: exec result %s: %w", id, err)
	}
	return workspace.ExitStatus{Code: int(code)}, nil
}

// List enumerates registered workspaces from the authoritative registry (same
// source of truth as the shellDriver).
func (d *containerdDriver) List(context.Context) ([]workspace.Workspace, error) {
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

// Inspect reports a workspace's LIVE state from the containerd task — the
// containerd driver's advantage over the shellDriver (which can only report
// ErrUnsupported). Existence is validated against the registry first.
func (d *containerdDriver) Inspect(ctx context.Context, id string) (workspace.Status, error) {
	if d.reg != nil {
		if _, ok, err := d.reg.Get(id); err != nil {
			return workspace.Status{}, err
		} else if !ok {
			return workspace.Status{}, fmt.Errorf("%w: %s", workspace.ErrNotFound, id)
		}
	}
	ctx = d.nsctx(ctx)
	container, err := d.cli.LoadContainer(ctx, ContainerName(id))
	if err != nil {
		if errdefs.IsNotFound(err) {
			// The registry lists it but containerd doesn't → destroyed out-of-band.
			return workspace.Status{Name: id, State: workspace.StateStopped}, nil
		}
		return workspace.Status{}, mapContainerdErr(err)
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return workspace.Status{Name: id, State: workspace.StateCreated}, nil // container, no task
		}
		return workspace.Status{}, mapContainerdErr(err)
	}
	st, err := task.Status(ctx)
	if err != nil {
		return workspace.Status{}, fmt.Errorf("containerd driver: task status %s: %w", id, err)
	}
	return workspace.Status{Name: id, State: mapState(st.Status)}, nil
}

// Suspend / Resume / Snapshot are VMM save/restore — unreachable through
// Kata-via-containerd (D7). Logs / Events / Attach ride other channels.
func (d *containerdDriver) Suspend(context.Context, string) error { return workspace.ErrUnsupported }

func (d *containerdDriver) Resume(context.Context, string) error { return workspace.ErrUnsupported }

func (d *containerdDriver) Snapshot(context.Context, string, workspace.SnapshotRequest) (workspace.SnapshotRef, error) {
	return workspace.SnapshotRef{}, workspace.ErrUnsupported
}

func (d *containerdDriver) Attach(context.Context, string, workspace.AttachRequest, workspace.Stream) (workspace.ExitStatus, error) {
	return workspace.ExitStatus{}, workspace.ErrUnsupported
}

func (d *containerdDriver) Logs(context.Context, string, workspace.LogsRequest) (io.ReadCloser, error) {
	return nil, workspace.ErrUnsupported
}

func (d *containerdDriver) Events(context.Context) (<-chan workspace.Event, error) {
	return nil, workspace.ErrUnsupported
}

// loadTask loads a workspace's running task, mapping a missing container/task to
// ErrNotFound.
func (d *containerdDriver) loadTask(ctx context.Context, id string) (client.Task, error) {
	container, err := d.cli.LoadContainer(ctx, ContainerName(id))
	if err != nil {
		return nil, mapContainerdErr(err)
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return nil, mapContainerdErr(err)
	}
	return task, nil
}

// killTask SIGKILLs a task, waits for it to exit, then deletes it.
func (d *containerdDriver) killTask(ctx context.Context, task client.Task) error {
	statusC, err := task.Wait(ctx)
	if err == nil {
		if kerr := task.Kill(ctx, syscall.SIGKILL); kerr != nil && !errdefs.IsNotFound(kerr) {
			return fmt.Errorf("containerd driver: kill task: %w", kerr)
		}
		select {
		case <-statusC:
		case <-time.After(15 * time.Second):
		}
	}
	if _, err := task.Delete(ctx); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("containerd driver: delete task: %w", err)
	}
	return nil
}

func (d *containerdDriver) regRemove(id string) {
	if d.reg != nil {
		_ = d.reg.Remove(id)
	}
}

// withImageConfigNoMount adapts the pure applyImageConfig into an oci.SpecOpts —
// the barrier-3-free replacement for oci.WithImageConfig.
func withImageConfigNoMount(cfg ocispec.ImageConfig, args, env []string, networkless bool) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		return applyImageConfig(s, ContainerdSpecOptions{
			Config: cfg, Args: args, Env: env, Networkless: networkless,
		})
	}
}

// imageConfig reads the image's OCI config from the content store (no rootfs
// mount) so the process user/env/args can be projected directly (barrier 3).
func imageConfig(ctx context.Context, img client.Image) (ocispec.ImageConfig, error) {
	desc, err := img.Config(ctx)
	if err != nil {
		return ocispec.ImageConfig{}, fmt.Errorf("containerd driver: image config descriptor: %w", err)
	}
	blob, err := content.ReadBlob(ctx, img.ContentStore(), desc)
	if err != nil {
		return ocispec.ImageConfig{}, fmt.Errorf("containerd driver: read image config: %w", err)
	}
	var image ocispec.Image
	if err := json.Unmarshal(blob, &image); err != nil {
		return ocispec.ImageConfig{}, fmt.Errorf("containerd driver: parse image config: %w", err)
	}
	return image.Config, nil
}

// containerdMounts renders a spec's workspace binds as OCI mounts (the kata
// shim/agent mounts them IN-GUEST — no client-side mount(2), so no barrier 3).
func containerdMounts(spec WorkspaceSpec) []specs.Mount {
	dest := spec.ProjectDest
	if dest == "" {
		dest = DefaultProjectDest
	}
	var mounts []specs.Mount
	switch spec.Mount {
	case MountHostFS:
		if strings.TrimSpace(spec.ProjectRoot) != "" {
			mounts = append(mounts, bindMount(dest, spec.ProjectRoot, false))
		}
	case MountVolume:
		if strings.TrimSpace(spec.Volume) != "" {
			mounts = append(mounts, bindMount(dest, spec.Volume, false))
		}
	case MountEphemeral:
		// nothing from the host
	}
	if spec.Comp != nil {
		mounts = append(mounts, bindMount(spec.Comp.GuestHome, spec.Comp.StagingDir, false))
		for _, b := range spec.Comp.Binds {
			mounts = append(mounts, bindMount(b.Dest, b.Source, b.ReadOnly))
		}
	}
	return mounts
}

func bindMount(dest, src string, ro bool) specs.Mount {
	opt := "rw"
	if ro {
		opt = "ro"
	}
	return specs.Mount{Destination: dest, Source: src, Type: "bind", Options: []string{"rbind", opt}}
}

// containerdEnv assembles the guest env in the same order as the nerdctl path:
// HOME, egress proxy vars, the composition env, then extra spec env.
func containerdEnv(spec WorkspaceSpec) []string {
	var env []string
	if spec.Comp != nil {
		env = append(env, "HOME="+spec.Comp.GuestHome)
	}
	env = append(env, ProxyEnv(spec.HTTPSProxy)...)
	if spec.Comp != nil {
		env = append(env, spec.Comp.Env...)
	}
	return append(env, spec.Env...)
}

// mapState maps a containerd process status to the workspace state vocabulary.
func mapState(s client.ProcessStatus) workspace.State {
	switch s {
	case client.Running:
		return workspace.StateRunning
	case client.Paused, client.Pausing:
		return workspace.StateFrozen
	case client.Created:
		return workspace.StateCreated
	case client.Stopped:
		return workspace.StateStopped
	default:
		return workspace.State(string(s))
	}
}

// mapContainerdErr maps containerd's not-found to the workspace sentinel so the
// vmm contract returns NOT_FOUND across the wire.
func mapContainerdErr(err error) error {
	if err == nil {
		return nil
	}
	if errdefs.IsNotFound(err) {
		return fmt.Errorf("%w: %w", workspace.ErrNotFound, err)
	}
	return err
}
