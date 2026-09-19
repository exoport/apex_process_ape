package apecmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The value written is the path the project's skills were ALREADY
// resolving, which is what makes the migration safe: no install moves.
func TestConfigPin_WritesTheFallbackTheProjectWasAlreadyUsing(t *testing.T) {
	t.Run("an existing governance evidence folder", func(t *testing.T) {
		root := projectFor(t, allExtensionsConfig)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "development", "governance", "evidence"), 0o755))

		cfg := resolveProjectConfig(root)
		require.Equal(t, "development/governance/evidence", resolveEvidenceFallback(cfg))
	})

	t.Run("no evidence folder anywhere", func(t *testing.T) {
		root := projectFor(t, allExtensionsConfig)
		cfg := resolveProjectConfig(root)
		require.Equal(t, "evidence", resolveEvidenceFallback(cfg))
	})
}

// The key is appended, and every other line survives byte-for-byte:
// this is a file a person wrote, and a YAML round-trip would reorder
// the keys and drop the comments.
func TestAppendConfigKey_LeavesTheRestOfTheFileAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := "# the project's own comment\nproject_name: fx   # trailing note\nextensions: []\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	require.NoError(t, appendConfigKey(path, "evidence_folder", "development/governance/evidence"))

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original+"evidence_folder: development/governance/evidence\n", string(after))
}

// A file with no trailing newline must not have the key glued onto its
// last line.
func TestAppendConfigKey_AddsTheMissingNewlineFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("project_name: fx"), 0o644))

	require.NoError(t, appendConfigKey(path, "evidence_folder", "evidence"))

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "project_name: fx\nevidence_folder: evidence\n", string(after))
}

// `ape config pin evidence_folder --check` is a migration's check:, and
// the runner reads 0 as applied, 1 as not applied and ANYTHING ELSE as
// "the check itself failed". Exit 1 is therefore the only code that may
// mean "unset".
func TestConfigPinCheck_ExitCodes(t *testing.T) {
	t.Run("unset is exit 1", func(t *testing.T) {
		root := projectFor(t, allExtensionsConfig)
		cmd := newConfigPinCmd()
		cmd.SetArgs([]string{"evidence_folder", "--check", "--cwd", root})
		cmd.SetOut(&bytes.Buffer{})
		err := cmd.Execute()
		require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	})

	t.Run("set is exit 0", func(t *testing.T) {
		root := projectFor(t, allExtensionsConfig+"evidence_folder: development/governance/evidence\n")
		cmd := newConfigPinCmd()
		cmd.SetArgs([]string{"evidence_folder", "--check", "--cwd", root})
		cmd.SetOut(&bytes.Buffer{})
		require.NoError(t, cmd.Execute())
	})

	t.Run("a key ape does not pin is a usage error", func(t *testing.T) {
		root := projectFor(t, allExtensionsConfig)
		cmd := newConfigPinCmd()
		cmd.SetArgs([]string{"output_folder", "--cwd", root})
		cmd.SetOut(&bytes.Buffer{})
		require.Equal(t, ExitUsage, exitCodeOf(t, cmd.Execute()))
	})
}

// Pinning twice writes once: the second run sees the key and stops.
func TestConfigPin_IsANoOpWhenTheKeyIsSet(t *testing.T) {
	root := projectFor(t, allExtensionsConfig)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", "governance", "evidence"), 0o755))

	for range 2 {
		cmd := newConfigPinCmd()
		cmd.SetArgs([]string{"evidence_folder", "--cwd", root})
		cmd.SetOut(&bytes.Buffer{})
		require.NoError(t, cmd.Execute())
	}

	body, err := os.ReadFile(filepath.Join(root, "_apex", "config.yaml"))
	require.NoError(t, err)
	require.Equal(t, 1, countLines(string(body), "evidence_folder: development/governance/evidence"))
}

func countLines(body, want string) int {
	n := 0
	for line := range splitLines(body) {
		if line == want {
			n++
		}
	}
	return n
}

func splitLines(body string) func(func(string) bool) {
	return func(yield func(string) bool) {
		start := 0
		for i := range len(body) {
			if body[i] == '\n' {
				if !yield(body[start:i]) {
					return
				}
				start = i + 1
			}
		}
		if start < len(body) {
			yield(body[start:])
		}
	}
}
