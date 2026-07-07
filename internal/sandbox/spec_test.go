package sandbox

import (
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func baseComp() *Composition {
	return &Composition{
		StagingDir: "/host/staging",
		GuestHome:  DefaultGuestHome,
		Env:        []string{"ANTHROPIC_API_KEY=sk-test"},
		Binds: []BindMount{
			{Source: "/host/key", Dest: "/sandbox/home/.ssh/id_ed25519", ReadOnly: true},
		},
	}
}

func TestBuildSpecReadonlyHostRootfs(t *testing.T) {
	spec, err := BuildSpec(SpecOptions{
		Comp:        baseComp(),
		ProjectRoot: "/host/project",
		Args:        []string{"ape", "task", "x", "--no-tui"},
	})
	require.NoError(t, err)
	assert.Equal(t, "/", spec.Root.Path)
	assert.True(t, spec.Root.Readonly, "host rootfs must be read-only")
}

func TestBuildSpecMasksHomes(t *testing.T) {
	spec, err := BuildSpec(SpecOptions{Comp: baseComp(), ProjectRoot: "/p", Args: []string{"x"}})
	require.NoError(t, err)
	for _, want := range []string{"/home", "/root", "/etc/ssh"} {
		assert.Contains(t, spec.Linux.MaskedPaths, want)
	}
}

func TestBuildSpecShadowsSensitiveDirsWithTmpfs(t *testing.T) {
	// The primary masking mechanism: an empty tmpfs mounted over /home and
	// /root (maskedPaths is silently ignored by rootless runsc — see the
	// PLAN-16 spike findings).
	spec, err := BuildSpec(SpecOptions{Comp: baseComp(), ProjectRoot: "/p", Args: []string{"x"}})
	require.NoError(t, err)
	for _, dir := range []string{"/home", "/root"} {
		m := findMount(t, spec.Mounts, dir)
		assert.Equal(t, "tmpfs", m.Type, "%s must be shadowed by tmpfs, not left as host content", dir)
		assert.Contains(t, m.Options, "ro")
	}
}

func TestBuildSpecMounts(t *testing.T) {
	spec, err := BuildSpec(SpecOptions{
		Comp:        baseComp(),
		ProjectRoot: "/host/project",
		Args:        []string{"x"},
		ExtraRW:     []string{"/host/repo2"},
	})
	require.NoError(t, err)

	project := findMount(t, spec.Mounts, DefaultProjectDest)
	assert.Equal(t, "/host/project", project.Source)
	assert.Contains(t, project.Options, "rw")

	home := findMount(t, spec.Mounts, DefaultGuestHome)
	assert.Equal(t, "/host/staging", home.Source)

	tmp := findMount(t, spec.Mounts, "/tmp")
	assert.Equal(t, "tmpfs", tmp.Type)

	// Composition bind carried through read-only.
	key := findMount(t, spec.Mounts, "/sandbox/home/.ssh/id_ed25519")
	assert.Contains(t, key.Options, "ro")

	// Extra rw bind mounted at the same path.
	repo := findMount(t, spec.Mounts, "/host/repo2")
	assert.Contains(t, repo.Options, "rw")
}

func TestBuildSpecEnvAndNetns(t *testing.T) {
	spec, err := BuildSpec(SpecOptions{
		Comp:        baseComp(),
		ProjectRoot: "/p",
		Args:        []string{"x"},
		Env:         []string{"HTTPS_PROXY=http://127.0.0.1:8888"},
	})
	require.NoError(t, err)
	assert.Contains(t, spec.Process.Env, "HOME="+DefaultGuestHome)
	assert.Contains(t, spec.Process.Env, "ANTHROPIC_API_KEY=sk-test")
	assert.Contains(t, spec.Process.Env, "HTTPS_PROXY=http://127.0.0.1:8888")

	var hasNet bool
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			hasNet = true
		}
	}
	assert.True(t, hasNet, "a private network namespace must be declared")
}

func TestBuildSpecCwdDefaultsToProjectDest(t *testing.T) {
	spec, err := BuildSpec(SpecOptions{Comp: baseComp(), ProjectRoot: "/p", Args: []string{"x"}})
	require.NoError(t, err)
	assert.Equal(t, DefaultProjectDest, spec.Process.Cwd)
}

func TestBuildSpecValidation(t *testing.T) {
	_, err := BuildSpec(SpecOptions{ProjectRoot: "/p", Args: []string{"x"}})
	require.Error(t, err, "nil comp")
	_, err = BuildSpec(SpecOptions{Comp: baseComp(), Args: []string{"x"}})
	require.Error(t, err, "empty project root")
	_, err = BuildSpec(SpecOptions{Comp: baseComp(), ProjectRoot: "/p"})
	require.Error(t, err, "empty args")
}

func findMount(t *testing.T, mounts []specs.Mount, dest string) specs.Mount {
	t.Helper()
	for _, m := range mounts {
		if m.Destination == dest {
			return m
		}
	}
	t.Fatalf("no mount at %s", dest)
	return specs.Mount{}
}
