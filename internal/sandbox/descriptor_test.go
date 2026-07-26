package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeDescriptor lays out a project dir with a descriptor and the dirs it names.
func writeDescriptor(t *testing.T, body string, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, DescriptorName), []byte(body), 0o600))
	return root
}

func TestLoadDescriptorResolvesRelativeSources(t *testing.T) {
	root := writeDescriptor(t, `
version: 1
repos:
  - { source: ., name: app, main: true }
  - { source: ../shared, name: shared, readonly: false }
mounts:
  - { source: ./fixtures, dest: /data/fixtures }
  - { source: ./cache, dest: /data/cache, readonly: false }
egress:
  authorized_domains: ["github.com", "*.githubusercontent.com"]
toolchain:
  tool_versions: .tool-versions
  bingo: true
`, "fixtures", "cache")
	// ../shared must exist relative to the project dir.
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(root), "shared"), 0o755))

	d, err := LoadDescriptor(filepath.Join(root, DescriptorName))
	require.NoError(t, err)
	assert.Equal(t, 1, d.Version)
	require.NotNil(t, d.Toolchain)
	assert.True(t, d.Toolchain.Bingo)

	res, err := d.Resolve(root)
	require.NoError(t, err)

	// Repos: each at /workspace/<name>, absolute sources, one main.
	require.Len(t, res.Repos, 2)
	assert.Equal(t, "app", res.Repos[0].Name)
	assert.True(t, res.Repos[0].Main)
	assert.True(t, filepath.IsAbs(res.Repos[0].Source))
	assert.Equal(t, "shared", res.Repos[1].Name)
	assert.False(t, res.Repos[1].Main)

	// User mounts: absolute, explicit dest, read-only unless opted out.
	require.Len(t, res.Mounts, 2)
	assert.Equal(t, "/data/fixtures", res.Mounts[0].Dest)
	assert.True(t, res.Mounts[0].ReadOnly, "a user mount defaults to read-only")
	assert.Equal(t, "/data/cache", res.Mounts[1].Dest)
	assert.False(t, res.Mounts[1].ReadOnly, "readonly: false is honoured")

	require.NotNil(t, res.Egress)
	assert.Equal(t, []string{"*.githubusercontent.com", "github.com"}, res.Egress.AuthorizedDomains)
}

func TestLoadDescriptorSingleRepoIsImplicitlyMain(t *testing.T) {
	root := writeDescriptor(t, "version: 1\nrepos:\n  - { source: . }\n")
	d, err := LoadDescriptor(filepath.Join(root, DescriptorName))
	require.NoError(t, err)
	res, err := d.Resolve(root)
	require.NoError(t, err)
	require.Len(t, res.Repos, 1)
	assert.True(t, res.Repos[0].Main, "a lone repo needs no main: true")
	assert.Equal(t, filepath.Base(root), res.Repos[0].Name)
}

func TestLoadDescriptorIgnoresUnknownTopLevelKeys(t *testing.T) {
	// Forward compatibility is load-bearing: a project file carrying a section this
	// build predates must still work, or adding a section breaks every older ape.
	root := writeDescriptor(t, "version: 1\nsomething_from_the_future:\n  wat: true\nrepos:\n  - { source: . }\n")
	d, err := LoadDescriptor(filepath.Join(root, DescriptorName))
	require.NoError(t, err)
	assert.Len(t, d.Repos, 1)
}

func TestLoadDescriptorValidationErrors(t *testing.T) {
	cases := map[string]string{
		"missing version":     "repos:\n  - { source: . }\n",
		"unsupported version": "version: 99\n",
		"two mains":           "version: 1\nrepos:\n  - { source: ., name: a, main: true }\n  - { source: ., name: b, main: true }\n",
		"no main":             "version: 1\nrepos:\n  - { source: ., name: a }\n  - { source: ., name: b }\n",
		"duplicate name":      "version: 1\nrepos:\n  - { source: ., name: a, main: true }\n  - { source: ., name: a }\n",
		"repo name traversal": "version: 1\nrepos:\n  - { source: ., name: ../etc }\n",
		"empty repo source":   "version: 1\nrepos:\n  - { name: a }\n",
		"relative dest":       "version: 1\nmounts:\n  - { source: ., dest: data }\n",
		"bad egress domain":   "version: 1\negress:\n  authorized_domains: [\"ev*l.com\"]\n",
		"bad direct allow":    "version: 1\negress:\n  direct_allow: [\"github.com\"]\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeDescriptor(t, body)
			_, err := LoadDescriptor(filepath.Join(root, DescriptorName))
			require.Error(t, err)
		})
	}
}

func TestDescriptorRejectsReservedDestinations(t *testing.T) {
	// A committed file must not be able to redirect a system mount — this is the
	// load-bearing half of the trust boundary on the client side.
	for _, dest := range []string{"/workspace", "/workspace/app", "/opt/apex-framework", "/sandbox/home", "/sandbox/home/.claude"} {
		root := writeDescriptor(t, "version: 1\nmounts:\n  - { source: ., dest: "+dest+" }\n")
		_, err := LoadDescriptor(filepath.Join(root, DescriptorName))
		require.Error(t, err, dest)
		assert.Contains(t, err.Error(), "reserved", dest)
	}
}

func TestResolveRejectsMissingSource(t *testing.T) {
	root := writeDescriptor(t, "version: 1\nmounts:\n  - { source: ./nope, dest: /data }\n")
	d, err := LoadDescriptor(filepath.Join(root, DescriptorName))
	require.NoError(t, err) // shape is valid...
	_, err = d.Resolve(root)
	require.Error(t, err) // ...but the source must exist to be canonicalized
	assert.Contains(t, err.Error(), "cannot resolve")
}

func TestParseMountFlag(t *testing.T) {
	// ParseMountFlag canonicalizes the source through filepath.EvalSymlinks (see
	// resolveSource), so the expectations have to be built from the canonical root — on
	// Windows that call also expands the 8.3 short name t.TempDir() returns
	// (…\RUNNER~1\… → …\runneradmin\…), and on any host it resolves a symlinked temp dir.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "data"), 0o755))

	// source only → /mnt/<basename>, read-only.
	m, err := ParseMountFlag(root, "data")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "data"), m.Source)
	assert.Equal(t, "/mnt/data", m.Dest)
	assert.True(t, m.ReadOnly)

	// source:dest
	m, err = ParseMountFlag(root, "data:/srv/in")
	require.NoError(t, err)
	assert.Equal(t, "/srv/in", m.Dest)
	assert.True(t, m.ReadOnly)

	// source:dest:rw
	m, err = ParseMountFlag(root, "data:/srv/in:rw")
	require.NoError(t, err)
	assert.False(t, m.ReadOnly)

	// source:ro (no dest)
	m, err = ParseMountFlag(root, "data:ro")
	require.NoError(t, err)
	assert.Equal(t, "/mnt/data", m.Dest)
	assert.True(t, m.ReadOnly)

	for _, bad := range []string{"", "nope:/srv/in", "data:/workspace/app", "data:/srv:rw:extra", "data:relative"} {
		_, err := ParseMountFlag(root, bad)
		require.Error(t, err, bad)
	}
}

func TestMergeUserMountsCLIWinsByDest(t *testing.T) {
	file := []workspace.MountSpec{
		{Source: "/srv/a", Dest: "/data/a", ReadOnly: true},
		{Source: "/srv/b", Dest: "/data/b", ReadOnly: true},
	}
	cli := []workspace.MountSpec{
		{Source: "/srv/a2", Dest: "/data/a", ReadOnly: false}, // same dest → wins
		{Source: "/srv/c", Dest: "/data/c", ReadOnly: true},
	}
	merged, err := MergeUserMounts(file, cli)
	require.NoError(t, err)
	require.Len(t, merged, 3)
	// Sorted by dest for a stable wire payload.
	assert.Equal(t, "/data/a", merged[0].Dest)
	assert.Equal(t, "/srv/a2", merged[0].Source, "the CLI entry replaced the file entry")
	assert.False(t, merged[0].ReadOnly)
	assert.Equal(t, "/data/b", merged[1].Dest)
	assert.Equal(t, "/data/c", merged[2].Dest)

	_, err = MergeUserMounts(nil, []workspace.MountSpec{{Source: "/srv/x", Dest: "/opt/apex-framework"}})
	require.Error(t, err, "a reserved dest is refused even from a flag")
}

func TestFindDescriptor(t *testing.T) {
	root := t.TempDir()
	_, ok := FindDescriptor(root)
	assert.False(t, ok)
	require.NoError(t, os.WriteFile(DescriptorPath(root), []byte("version: 1\n"), 0o600))
	path, ok := FindDescriptor(root)
	assert.True(t, ok)
	assert.Equal(t, DescriptorPath(root), path)
}

func TestRepoDestAndMountNameValidation(t *testing.T) {
	assert.Equal(t, "/workspace/app", RepoDest("app"))
	require.NoError(t, ValidateMountName("app"))
	require.NoError(t, ValidateMountName("v0.3.1"))
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`} {
		require.Error(t, ValidateMountName(bad), bad)
	}
}

func TestRegistryTouchStampsLastUsed(t *testing.T) {
	reg := OpenRegistry(t.TempDir())
	require.NoError(t, reg.Put(Workspace{Name: "dev", Container: "ape-ws-dev"}))

	// An unknown workspace is a no-op, not an error: the caller may be stamping
	// something that was just destroyed.
	require.NoError(t, reg.Touch("ghost", "2026-07-25T12:00:00Z"))

	require.NoError(t, reg.Touch("dev", "2026-07-25T12:00:00Z"))
	got, ok, err := reg.Get("dev")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "2026-07-25T12:00:00Z", got.LastUsedAt)
	assert.Equal(t, "ape-ws-dev", got.Container, "touching must not clobber the rest of the record")
}
