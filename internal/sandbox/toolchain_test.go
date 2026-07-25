package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupToolCacheAndNames(t *testing.T) {
	tc, err := LookupToolCache("go")
	require.NoError(t, err)
	assert.Equal(t, "/cache/go", tc.Dest)
	// The env is part of the definition, not caller input — that is what keeps a
	// caller from pointing GOPATH wherever it likes.
	assert.Contains(t, tc.Env, "GOPATH=/cache/go")
	assert.Contains(t, tc.Env, "GOMODCACHE=/cache/go/pkg/mod")

	// Case and whitespace are tolerated; an unknown name is refused with the list.
	_, err = LookupToolCache("  ASDF ")
	require.NoError(t, err)
	_, err = LookupToolCache("nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "known:")
	assert.Equal(t, []string{"asdf", "cargo", "go", "npm", "pub"}, ToolCacheNames())
}

func TestNormalizeToolCachesDedupesAndValidates(t *testing.T) {
	got, err := NormalizeToolCaches([]string{"go", "asdf", "go"})
	require.NoError(t, err)
	assert.Equal(t, []string{"asdf", "go"}, got)

	_, err = NormalizeToolCaches([]string{"go", "bogus"})
	require.Error(t, err)
}

func TestToolchainCachesDefaults(t *testing.T) {
	var none *Descriptor
	assert.Nil(t, none.ToolchainCaches(), "no descriptor → no caches")

	d := &Descriptor{Version: 1}
	assert.Nil(t, d.ToolchainCaches(), "no toolchain section → no caches")

	d.Toolchain = &DescriptorToolchain{Bingo: true}
	assert.Equal(t, DefaultToolCaches, d.ToolchainCaches(), "a toolchain with no caches named takes the defaults")

	d.Toolchain.Caches = []string{"cargo"}
	assert.Equal(t, []string{"cargo"}, d.ToolchainCaches())
}

func TestDescriptorRejectsUnknownCache(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, DescriptorName),
		[]byte("version: 1\ntoolchain:\n  caches: [go, nope]\n"), 0o600))
	_, err := LoadDescriptor(filepath.Join(root, DescriptorName))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "toolchain.caches")
}

func TestToolchainSetupScript(t *testing.T) {
	script, err := ToolchainSetupScript(&DescriptorToolchain{ToolVersions: ".tool-versions", Bingo: true}, "/workspace/app")
	require.NoError(t, err)
	assert.Contains(t, script, "set -e")
	assert.Contains(t, script, "cd '/workspace/app'")
	assert.Contains(t, script, "asdf install")
	assert.Contains(t, script, "bingo get")
	// Both steps degrade with a message instead of failing when the image lacks the
	// tool — the workspace is still usable.
	assert.Contains(t, script, "asdf is not in this image")
	assert.Contains(t, script, "bingo is not in this image")

	// No toolchain → no script.
	empty, err := ToolchainSetupScript(nil, "/workspace/app")
	require.NoError(t, err)
	assert.Empty(t, empty)

	// A repo dir is required: the step is meaningless outside the project.
	_, err = ToolchainSetupScript(&DescriptorToolchain{Bingo: true}, "")
	require.Error(t, err)
}

func TestToolchainSetupScriptQuotesInlineTools(t *testing.T) {
	// Inline tool strings reach a shell command, so they must be quoted. The nasty
	// input here would otherwise inject a second command.
	script, err := ToolchainSetupScript(&DescriptorToolchain{
		Tools: []string{"golang 1.23.4", "nodejs 20.18.0; rm -rf /"},
	}, "/workspace/app")
	require.NoError(t, err)
	assert.Contains(t, script, `'golang 1.23.4'`)
	assert.Contains(t, script, `'nodejs 20.18.0; rm -rf /'`)
	// The dangerous text only ever appears inside single quotes.
	for line := range strings.SplitSeq(script, "\n") {
		if strings.Contains(line, "rm -rf /") {
			assert.Contains(t, line, "'nodejs 20.18.0; rm -rf /'", "unquoted injection in %q", line)
		}
	}
	assert.Contains(t, script, ".tool-versions.ape")
}

func TestToolchainSetupScriptQuotesRepoDir(t *testing.T) {
	script, err := ToolchainSetupScript(&DescriptorToolchain{Bingo: true}, "/workspace/it's here")
	require.NoError(t, err)
	assert.Contains(t, script, `cd '/workspace/it'\''s here'`)
}
