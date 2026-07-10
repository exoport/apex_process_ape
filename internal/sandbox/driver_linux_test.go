//go:build linux

package sandbox

import (
	"context"
	"testing"

	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShellDriverByteIdenticalArgs is the Phase-1 acceptance: every shellDriver
// lifecycle verb shells out the exact same argument vector as the direct Runner
// path (and the documented golden vector). A runFunc seam captures the argv
// without executing nerdctl. Linux-only: the capture rides Runner.run, which is
// the Linux build's method (the !linux stubs return ErrUnsupported before run).
func TestShellDriverByteIdenticalArgs(t *testing.T) {
	ctx := context.Background()

	// capturing records the last argv a Runner would exec, running nothing.
	var got []string
	capturing := func() *Runner {
		return &Runner{runFunc: func(_ context.Context, _ bool, args []string) error {
			got = append([]string(nil), args...)
			return nil
		}}
	}

	spec := WorkspaceSpec{
		Name: "dev", Image: "img:tag", VMM: VMMCloudHypervisor, Mount: MountHostFS,
		ProjectRoot: "/proj", Comp: specComp(), HTTPSProxy: "http://127.0.0.1:9",
		SSHPort: 2222, Env: []string{"FOO=bar"},
	}
	wantRun, err := spec.RunArgs()
	require.NoError(t, err)
	var resolve SpecResolver = func(context.Context, workspace.CreateRequest) (WorkspaceSpec, error) {
		return spec, nil
	}

	const id = "dev"
	container := ContainerName(id) // ape-ws-dev

	cases := []struct {
		name   string
		viaDrv func(workspace.Backend) error
		viaRun func(*Runner) error
		want   []string
	}{
		{
			"create",
			func(b workspace.Backend) error { _, e := b.Create(ctx, workspace.CreateRequest{Name: id}); return e },
			func(r *Runner) error { return r.Provision(ctx, spec) },
			wantRun,
		},
		{
			"start",
			func(b workspace.Backend) error { return b.Start(ctx, id) },
			func(r *Runner) error { return r.Start(ctx, container) },
			StartArgs(container),
		},
		{
			"stop",
			func(b workspace.Backend) error { return b.Stop(ctx, id) },
			func(r *Runner) error { return r.Stop(ctx, container) },
			StopArgs(container),
		},
		{
			"freeze",
			func(b workspace.Backend) error { return b.Freeze(ctx, id) },
			func(r *Runner) error { return r.Freeze(ctx, container) },
			PauseArgs(container),
		},
		{
			"unfreeze",
			func(b workspace.Backend) error { return b.Unfreeze(ctx, id) },
			func(r *Runner) error { return r.Unfreeze(ctx, container) },
			ResumeArgs(container),
		},
		{
			"exec",
			func(b workspace.Backend) error {
				_, e := b.Exec(ctx, id, workspace.ExecRequest{TTY: true, Cmd: []string{"ls", "-la"}})
				return e
			},
			func(r *Runner) error { return r.Exec(ctx, container, true, []string{"ls", "-la"}) },
			ExecArgs(container, true, []string{"ls", "-la"}),
		},
		{
			"attach",
			func(b workspace.Backend) error {
				_, e := b.Attach(ctx, id, workspace.AttachRequest{Shell: "/bin/bash"}, nil)
				return e
			},
			func(r *Runner) error { return r.Attach(ctx, container, "/bin/bash") },
			AttachArgs(container, "/bin/bash"),
		},
		{
			"destroy",
			func(b workspace.Backend) error { return b.Destroy(ctx, id, workspace.DestroyRequest{Force: true}) },
			func(r *Runner) error { return r.Down(ctx, container) },
			DownArgs(container),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got = nil
			require.NoError(t, c.viaDrv(NewShellDriver(capturing(), nil, resolve)))
			viaDriver := got

			got = nil
			require.NoError(t, c.viaRun(capturing()))
			viaRunner := got

			assert.Equal(t, c.want, viaDriver, "driver argv must match the golden vector")
			assert.Equal(t, viaRunner, viaDriver, "driver argv must be byte-identical to the direct Runner path")
		})
	}
}
