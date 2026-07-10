package sandbox

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func specComp() *Composition {
	return &Composition{
		StagingDir: "/state/homes/dev",
		GuestHome:  DefaultGuestHome,
		Env:        []string{"ANTHROPIC_API_KEY=sk"},
		Binds: []BindMount{
			{Source: "/host/key", Dest: "/sandbox/home/.ssh/id_ed25519", ReadOnly: true},
		},
	}
}

// TestRunArgsHostFS locks the exact nerdctl command shape for the common
// local-dev case: host-fs project mount, composed home, egress proxy,
// forwarded sshd port. This is the contract the Linux runner shells out.
func TestRunArgsHostFS(t *testing.T) {
	spec := WorkspaceSpec{
		Name:        "dev",
		Image:       "img:tag",
		VMM:         VMMCloudHypervisor,
		Mount:       MountHostFS,
		ProjectRoot: "/proj",
		Comp:        specComp(),
		HTTPSProxy:  "http://127.0.0.1:9",
		SSHPort:     2222,
		Env:         []string{"FOO=bar"},
	}
	args, err := spec.RunArgs()
	require.NoError(t, err)
	assert.Equal(t, []string{
		"run", "-d",
		"--name", "ape-ws-dev",
		"--runtime", "io.containerd.kata-clh.v2",
		"--label", "ape.managed=true",
		"--label", "ape.workspace=dev",
		"-v", "/proj:/workspace",
		"-v", "/state/homes/dev:/sandbox/home",
		"-v", "/host/key:/sandbox/home/.ssh/id_ed25519:ro",
		"-e", "HOME=/sandbox/home",
		"-e", "HTTPS_PROXY=http://127.0.0.1:9",
		"-e", "HTTP_PROXY=http://127.0.0.1:9",
		"-e", "NO_PROXY=localhost,127.0.0.1",
		"-e", "ANTHROPIC_API_KEY=sk",
		"-e", "FOO=bar",
		"-p", "127.0.0.1:2222:22",
		"img:tag",
	}, args)
}

func TestRunArgsQemuRuntimeHandler(t *testing.T) {
	spec := WorkspaceSpec{
		Name: "gpu", Image: "img", VMM: VMMQemu, Mount: MountHostFS,
		ProjectRoot: "/p", Comp: specComp(),
	}
	args, err := spec.RunArgs()
	require.NoError(t, err)
	assert.Contains(t, args, "io.containerd.kata-qemu.v2")
}

func TestRunArgsVolumeMode(t *testing.T) {
	spec := WorkspaceSpec{
		Name: "srv", Image: "img", VMM: VMMCloudHypervisor, Mount: MountVolume,
		Volume: "ape-ws-srv-data", Comp: specComp(),
	}
	args, err := spec.RunArgs()
	require.NoError(t, err)
	assert.True(t, hasPair(args, "-v", "ape-ws-srv-data:/workspace"), "volume mount must use the named volume")
	// No host project bind in volume mode.
	assert.False(t, hasPair(args, "-v", "/proj:/workspace"))
}

func TestRunArgsEphemeralHasNoProjectBind(t *testing.T) {
	spec := WorkspaceSpec{
		Name: "eph", Image: "img", VMM: VMMCloudHypervisor, Mount: MountEphemeral,
		Comp: specComp(),
	}
	args, err := spec.RunArgs()
	require.NoError(t, err)
	// Only the home bind — nothing mounted at /workspace.
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-v" {
			assert.NotContains(t, args[i+1], ":/workspace", "ephemeral mode must not bind a project")
		}
	}
}

func TestRunArgsNoProxyWhenUnset(t *testing.T) {
	spec := WorkspaceSpec{
		Name: "dev", Image: "img", VMM: VMMCloudHypervisor, Mount: MountHostFS,
		ProjectRoot: "/p", Comp: specComp(),
	}
	args, err := spec.RunArgs()
	require.NoError(t, err)
	for _, a := range args {
		assert.NotContains(t, a, "HTTPS_PROXY", "no proxy env when HTTPSProxy is empty")
	}
}

func TestRunArgsCommandOverride(t *testing.T) {
	spec := WorkspaceSpec{
		Name: "dev", Image: "img", VMM: VMMCloudHypervisor, Mount: MountHostFS,
		ProjectRoot: "/p", Comp: specComp(), Command: []string{"sleep", "infinity"},
	}
	args, err := spec.RunArgs()
	require.NoError(t, err)
	// Command trails the image.
	assert.Equal(t, []string{"img", "sleep", "infinity"}, args[len(args)-3:])
}

func TestRunArgsValidation(t *testing.T) {
	cases := map[string]WorkspaceSpec{
		"empty name":       {Image: "img", Mount: MountHostFS, ProjectRoot: "/p", Comp: specComp()},
		"empty image":      {Name: "d", Mount: MountHostFS, ProjectRoot: "/p", Comp: specComp()},
		"nil comp":         {Name: "d", Image: "img", Mount: MountHostFS, ProjectRoot: "/p"},
		"host-fs no root":  {Name: "d", Image: "img", Mount: MountHostFS, Comp: specComp()},
		"volume no volume": {Name: "d", Image: "img", Mount: MountVolume, Comp: specComp()},
		"bad mount":        {Name: "d", Image: "img", Mount: MountMode("weird"), Comp: specComp()},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := spec.RunArgs()
			require.Error(t, err)
		})
	}
}

func TestExecAndAttachArgs(t *testing.T) {
	assert.Equal(t, []string{"exec", "-it", "ape-ws-dev", "ls", "-la"},
		ExecArgs("ape-ws-dev", true, []string{"ls", "-la"}))
	assert.Equal(t, []string{"exec", "ape-ws-dev", "true"},
		ExecArgs("ape-ws-dev", false, []string{"true"}))
	assert.Equal(t, []string{"exec", "-it", "ape-ws-dev", "/bin/bash", "-l"},
		AttachArgs("ape-ws-dev", ""))
	assert.Equal(t, []string{"exec", "-it", "ape-ws-dev", "/bin/zsh", "-l"},
		AttachArgs("ape-ws-dev", "/bin/zsh"))
}

func TestLifecycleArgs(t *testing.T) {
	assert.Equal(t, []string{"pause", "ape-ws-dev"}, PauseArgs("ape-ws-dev"))
	assert.Equal(t, []string{"unpause", "ape-ws-dev"}, ResumeArgs("ape-ws-dev"))
	assert.Equal(t, []string{"start", "ape-ws-dev"}, StartArgs("ape-ws-dev"))
	assert.Equal(t, []string{"stop", "ape-ws-dev"}, StopArgs("ape-ws-dev"))
	assert.Equal(t, []string{"rm", "-f", "ape-ws-dev"}, DownArgs("ape-ws-dev"))
}

func TestSSHArgs(t *testing.T) {
	args := SSHArgs("ape", 2222, "/state/known_hosts", nil)
	assert.Equal(t, []string{
		"-p", "2222",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=/state/known_hosts",
		"ape@127.0.0.1",
	}, args)
	// Empty user defaults to "ape"; extra flags land before the destination.
	args = SSHArgs("", 22, "", []string{"-v"})
	assert.Equal(t, "ape@127.0.0.1", args[len(args)-1])
	assert.Contains(t, args, "-v")
}

func TestResolveImage(t *testing.T) {
	assert.Equal(t, DefaultImage, ResolveImage(&Profile{}))
	assert.Equal(t, "custom:1", ResolveImage(&Profile{Image: "custom:1"}))
	assert.Equal(t, DefaultImage, ResolveImage(nil))
}

func TestContainerName(t *testing.T) {
	assert.Equal(t, "ape-ws-dev", ContainerName("dev"))
}

func TestStagingDirFor(t *testing.T) {
	assert.Equal(t, filepath.FromSlash("/state/homes/dev"), StagingDirFor("/state", "dev"))
}

func TestRegistryRoundTrip(t *testing.T) {
	base := t.TempDir()
	reg := OpenRegistry(base)

	// Missing file reads as empty.
	list, err := reg.List()
	require.NoError(t, err)
	assert.Empty(t, list)
	_, ok, err := reg.Get("dev")
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, reg.Put(Workspace{Name: "dev", Container: "ape-ws-dev", Mount: "host-fs"}))
	require.NoError(t, reg.Put(Workspace{Name: "app", Container: "ape-ws-app", Mount: "volume"}))

	list, err = reg.List()
	require.NoError(t, err)
	require.Len(t, list, 2)
	// Sorted by name: app before dev.
	assert.Equal(t, "app", list[0].Name)
	assert.Equal(t, "dev", list[1].Name)

	got, ok, err := reg.Get("dev")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "ape-ws-dev", got.Container)

	// Put replaces.
	require.NoError(t, reg.Put(Workspace{Name: "dev", Container: "ape-ws-dev", SSHPort: 2200}))
	got, _, _ = reg.Get("dev")
	assert.Equal(t, 2200, got.SSHPort)

	// Remove is a no-op for absent names, and drops present ones.
	require.NoError(t, reg.Remove("nope"))
	require.NoError(t, reg.Remove("dev"))
	_, ok, _ = reg.Get("dev")
	assert.False(t, ok)

	// The backing file exists after a Put.
	assert.FileExists(t, reg.Path())
}

func TestRegistryPutEmptyNameRejected(t *testing.T) {
	reg := OpenRegistry(t.TempDir())
	require.Error(t, reg.Put(Workspace{Name: ""}))
}

// hasPair reports whether args contains flag immediately followed by value.
func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
