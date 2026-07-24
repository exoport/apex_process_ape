package aped

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mountResolver builds a resolver with a materialized framework root, so the
// system-mount half of PLAN-20 is exercised without a real node.
func mountResolver(t *testing.T, ref string) (resolver *Resolver, fwRootPath string) {
	t.Helper()
	fwRoot := t.TempDir()
	if ref != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(fwRoot, ref), 0o755))
	}
	r := NewResolver(ResolverConfig{
		StateDir:      t.TempDir(),
		FrameworkRoot: fwRoot,
		FrameworkRef:  ref,
	})
	r.compose = func(sandbox.ComposeOptions) (*sandbox.Composition, error) {
		return &sandbox.Composition{StagingDir: t.TempDir(), GuestHome: sandbox.DefaultGuestHome}, nil
	}
	return r, fwRoot
}

// mountByDest finds a resolved mount by its guest destination.
func mountByDest(mounts []workspace.MountSpec, dest string) (workspace.MountSpec, bool) {
	for _, m := range mounts {
		if m.Dest == dest {
			return m, true
		}
	}
	return workspace.MountSpec{}, false
}

func TestResolveMountsAppliesFrameworkSystemMount(t *testing.T) {
	r, fwRoot := mountResolver(t, "v0.3.1")
	project := t.TempDir()

	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", MountSource: project,
	})
	require.NoError(t, err)

	fw, ok := mountByDest(spec.Mounts, sandbox.FrameworkDest)
	require.True(t, ok, "the framework mount is present by DEFAULT, with no request for it")
	assert.Equal(t, filepath.Join(fwRoot, "v0.3.1"), fw.Source)
	assert.True(t, fw.ReadOnly, "the framework is always read-only")
}

func TestResolveMountsFrameworkRefIsARequestNotAPath(t *testing.T) {
	r, fwRoot := mountResolver(t, "v0.3.1")
	require.NoError(t, os.MkdirAll(filepath.Join(fwRoot, "v0.4.0"), 0o755))
	project := t.TempDir()

	// A request may select a DIFFERENT VERSION...
	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", MountSource: project, FrameworkRef: "v0.4.0",
	})
	require.NoError(t, err)
	fw, _ := mountByDest(spec.Mounts, sandbox.FrameworkDest)
	assert.Equal(t, filepath.Join(fwRoot, "v0.4.0"), fw.Source)

	// ...but never a different PATH: the ref is a single path segment resolved under
	// the node's own framework root, so traversal is rejected outright.
	_, err = r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", MountSource: project, FrameworkRef: "../../etc",
	})
	require.ErrorIs(t, err, workspace.ErrValidation)
}

func TestResolveMountsMissingFrameworkRefIsActionable(t *testing.T) {
	r, _ := mountResolver(t, "v0.3.1")
	_, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", MountSource: t.TempDir(), FrameworkRef: "v9.9.9",
	})
	require.ErrorIs(t, err, workspace.ErrValidation)
	// aped holds no credentials and must never fetch — so it says exactly what to run.
	assert.Contains(t, err.Error(), "ape sandbox framework materialize v9.9.9")
}

func TestResolveMountsNoFrameworkRootMeansNoFrameworkMount(t *testing.T) {
	r := NewResolver(ResolverConfig{StateDir: t.TempDir()})
	r.compose = func(sandbox.ComposeOptions) (*sandbox.Composition, error) {
		return &sandbox.Composition{StagingDir: t.TempDir(), GuestHome: sandbox.DefaultGuestHome}, nil
	}
	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{Name: "dev", MountSource: t.TempDir()})
	require.NoError(t, err)
	_, ok := mountByDest(spec.Mounts, sandbox.FrameworkDest)
	assert.False(t, ok)
}

func TestResolveMountsSingleRepoFromMountSource(t *testing.T) {
	// The pre-PLAN-20 shape: no repos on the wire, just a host-fs mount source. It
	// must still land at /workspace/<name> and set the working directory.
	r, _ := mountResolver(t, "")
	project := filepath.Join(t.TempDir(), "myapp")
	require.NoError(t, os.MkdirAll(project, 0o755))

	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{Name: "dev", MountSource: project})
	require.NoError(t, err)

	repo, ok := mountByDest(spec.Mounts, "/workspace/myapp")
	require.True(t, ok)
	assert.Equal(t, project, repo.Source)
	assert.False(t, repo.ReadOnly)
	assert.Equal(t, "/workspace/myapp", spec.Cwd)
	assert.Equal(t, project, spec.ProjectRoot)
}

func TestResolveMountsMultiRepoUsesMainForCwd(t *testing.T) {
	r, _ := mountResolver(t, "")
	base := t.TempDir()
	app, shared := filepath.Join(base, "app"), filepath.Join(base, "shared")
	require.NoError(t, os.MkdirAll(app, 0o755))
	require.NoError(t, os.MkdirAll(shared, 0o755))

	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", MountSource: app,
		Repos: []workspace.RepoMount{
			{Source: shared, Name: "shared", ReadOnly: true},
			{Source: app, Name: "app", Main: true},
		},
	})
	require.NoError(t, err)

	sharedMount, ok := mountByDest(spec.Mounts, "/workspace/shared")
	require.True(t, ok)
	assert.True(t, sharedMount.ReadOnly)
	_, ok = mountByDest(spec.Mounts, "/workspace/app")
	require.True(t, ok)
	// The main repo sets cwd and is what the workspace reports as its project.
	assert.Equal(t, "/workspace/app", spec.Cwd)
	assert.Equal(t, app, spec.ProjectRoot)
}

func TestResolveMountsRejectsTwoMainRepos(t *testing.T) {
	r, _ := mountResolver(t, "")
	dir := t.TempDir()
	_, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", MountSource: dir,
		Repos: []workspace.RepoMount{
			{Source: dir, Name: "a", Main: true},
			{Source: dir, Name: "b", Main: true},
		},
	})
	require.ErrorIs(t, err, workspace.ErrValidation)
}

func TestResolveMountsAppendsUserMountsAfterSystemMounts(t *testing.T) {
	r, _ := mountResolver(t, "v0.3.1")
	project := t.TempDir()
	data := t.TempDir()

	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", MountSource: project,
		Mounts: []workspace.MountSpec{{Source: data, Dest: "/data/fixtures", ReadOnly: true}},
	})
	require.NoError(t, err)

	// Order is system-then-user, so a user entry can never precede (or shadow) one.
	require.GreaterOrEqual(t, len(spec.Mounts), 3)
	assert.Equal(t, sandbox.FrameworkDest, spec.Mounts[0].Dest)
	assert.Equal(t, "/data/fixtures", spec.Mounts[len(spec.Mounts)-1].Dest)
}

func TestResolveMountsRefusesReservedUserDest(t *testing.T) {
	r, _ := mountResolver(t, "v0.3.1")
	project := t.TempDir()
	// A hand-crafted wire request bypassing the client's own check must still be
	// refused: aped is the authority on the trust boundary.
	for _, dest := range []string{sandbox.FrameworkDest, "/workspace/app", sandbox.DefaultGuestHome} {
		_, err := r.Resolve(context.Background(), workspace.CreateRequest{
			Name: "dev", MountSource: project,
			Mounts: []workspace.MountSpec{{Source: project, Dest: dest, ReadOnly: false}},
		})
		require.ErrorIs(t, err, workspace.ErrPolicyDenied, dest)
	}
}

func TestResolveMountsRefusesRelativeUserSource(t *testing.T) {
	r, _ := mountResolver(t, "")
	_, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", MountSource: t.TempDir(),
		Mounts: []workspace.MountSpec{{Source: "relative/path", Dest: "/data"}},
	})
	require.ErrorIs(t, err, workspace.ErrValidation)
}

// ---- policy (D4) -----------------------------------------------------------

func TestPolicyCheckMountsPerEntry(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "project")
	require.NoError(t, os.MkdirAll(inside, 0o755))
	outside := t.TempDir()

	p := &Policy{Images: []string{"img"}, MountRoots: []string{root}}
	rc := ResolvedCreate{Image: "img"}

	t.Run("every entry is checked, not just the first", func(t *testing.T) {
		r := rc
		r.Mounts = []workspace.MountSpec{
			{Source: inside, Dest: "/workspace/project"},
			{Source: outside, Dest: "/data/extra", ReadOnly: true},
		}
		err := p.CheckCreate(r, 0)
		require.ErrorIs(t, err, workspace.ErrPolicyDenied)
		assert.Contains(t, err.Error(), outside)
	})

	t.Run("allowed roots pass", func(t *testing.T) {
		r := rc
		r.Mounts = []workspace.MountSpec{{Source: inside, Dest: "/workspace/project"}}
		require.NoError(t, p.CheckCreate(r, 0))
	})

	t.Run("duplicate destinations are rejected", func(t *testing.T) {
		r := rc
		r.Mounts = []workspace.MountSpec{
			{Source: inside, Dest: "/data"},
			{Source: inside, Dest: "/data"},
		}
		require.ErrorIs(t, p.CheckCreate(r, 0), workspace.ErrValidation)
	})

	t.Run("max_mounts bounds fan-out", func(t *testing.T) {
		capped := &Policy{Images: []string{"img"}, MountRoots: []string{root}, Limits: Limits{MaxMounts: 1}}
		r := rc
		r.Mounts = []workspace.MountSpec{
			{Source: inside, Dest: "/a"},
			{Source: inside, Dest: "/b"},
		}
		require.ErrorIs(t, capped.CheckCreate(r, 0), workspace.ErrPolicyDenied)
	})
}

func TestPolicyReadOnlyRootDeniesWritableMount(t *testing.T) {
	shared := t.TempDir()
	p := &Policy{Images: []string{"img"}, MountRootsRO: []string{shared}}
	rc := ResolvedCreate{Image: "img"}

	// Read-only under an ro-only root: allowed, and authorized by that root alone
	// (no duplicate entry in mount_roots needed).
	ro := rc
	ro.Mounts = []workspace.MountSpec{{Source: shared, Dest: "/data/shared", ReadOnly: true}}
	require.NoError(t, p.CheckCreate(ro, 0))

	// Writable under the same root: denied, naming the root.
	rw := rc
	rw.Mounts = []workspace.MountSpec{{Source: shared, Dest: "/data/shared", ReadOnly: false}}
	err := p.CheckCreate(rw, 0)
	require.ErrorIs(t, err, workspace.ErrPolicyDenied)
	assert.Contains(t, err.Error(), "read-only root")
}

func TestPolicyFrameworkMountMustBeReadOnly(t *testing.T) {
	// The framework's source is aped's own, so it is exempt from the mount-root
	// check — but only while it stays read-only on its reserved destination.
	p := &Policy{Images: []string{"img"}}
	rc := ResolvedCreate{Image: "img", Mounts: []workspace.MountSpec{
		{Source: "/srv/apex-framework/v0.3.1", Dest: sandbox.FrameworkDest, ReadOnly: true},
	}}
	require.NoError(t, p.CheckCreate(rc, 0))

	rc.Mounts[0].ReadOnly = false
	require.ErrorIs(t, p.CheckCreate(rc, 0), workspace.ErrPolicyDenied)
}
