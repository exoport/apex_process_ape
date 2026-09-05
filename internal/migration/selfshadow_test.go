package migration

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeScript drops an executable shell script and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	return path
}

// TestSelfShadow_TheDecoyOnPathLoses is the regression test for a real
// defect, found by running the framework's own first migration entry on a
// real machine rather than against a fixture of it.
//
// That entry's check is `ape sprint check … | jq -e …`, and `ape` there
// resolves through PATH. The machine had ape v0.0.56 in ~/go/bin. That
// binary HAS `sprint check --output-format json`, emits perfectly valid
// JSON, and reports `findings: []` because the check class did not exist
// yet — so jq said `true`, the check reported SATISFIED, and the runner
// would have recorded the entry as applied on a project that had not been
// migrated. The ledger makes that permanent.
//
// The stand-in was answering for the thing. Here the decoy prints
// STALE and the shadow prints CURRENT, and the check must see CURRENT.
func TestSelfShadow_TheDecoyOnPathLoses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the framework's migration lines are POSIX shell")
	}
	decoyDir := filepath.Join(t.TempDir(), "stale-bin")
	writeScript(t, decoyDir, SelfName, `echo STALE`)

	current := writeScript(t, filepath.Join(t.TempDir(), "current"), "the-real-one", `echo CURRENT`)
	shadowDir, cleanup, err := selfShadow(current)
	require.NoError(t, err)
	defer cleanup()

	env := prependPath(append(os.Environ(), "PATH="+decoyDir), shadowDir)
	out := filepath.Join(t.TempDir(), "seen")
	code, err := ShellRunner{Env: env}.Run(t.Context(), t.TempDir(),
		SelfName+" > "+out)
	require.NoError(t, err)
	require.Equal(t, 0, code)

	seen, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "CURRENT", strings.TrimSpace(string(seen)),
		"a check naming %q must be answered by the binary running the migration", SelfName)
}

// TestSelfShadow_WithoutItTheDecoyWins is the other half: the same setup
// with no shadow reproduces the defect, so the test above is proving the
// fix rather than an accident of PATH ordering.
func TestSelfShadow_WithoutItTheDecoyWins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the framework's migration lines are POSIX shell")
	}
	decoyDir := filepath.Join(t.TempDir(), "stale-bin")
	writeScript(t, decoyDir, SelfName, `echo STALE`)

	out := filepath.Join(t.TempDir(), "seen")
	env := prependPath(os.Environ(), decoyDir)
	code, err := ShellRunner{Env: env}.Run(t.Context(), t.TempDir(), SelfName+" > "+out)
	require.NoError(t, err)
	require.Equal(t, 0, code)

	seen, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "STALE", strings.TrimSpace(string(seen)))
}

// TestSelfShadow_ShadowsOnlyItsOwnName — the directory carries one entry,
// so nothing else on PATH changes resolution order.
func TestSelfShadow_ShadowsOnlyItsOwnName(t *testing.T) {
	current := writeScript(t, filepath.Join(t.TempDir(), "current"), "the-real-one", `echo CURRENT`)
	dir, cleanup, err := selfShadow(current)
	require.NoError(t, err)
	defer cleanup()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, SelfName+exeSuffix(), entries[0].Name())

	cleanup()
	require.NoDirExists(t, dir)
}

func TestPrependPath(t *testing.T) {
	got := prependPath([]string{"HOME=/h", "PATH=/a:/b"}, "/shadow")
	require.Contains(t, got, "PATH=/shadow"+string(os.PathListSeparator)+"/a:/b")
	require.Contains(t, got, "HOME=/h")

	// Windows spells it `Path`; appending a second PATH= would leave the
	// original in force, so the match is case-insensitive.
	got = prependPath([]string{"Path=/a"}, "/shadow")
	require.Len(t, got, 1)
	require.Equal(t, "Path=/shadow"+string(os.PathListSeparator)+"/a", got[0])

	// No PATH at all: one is created rather than the shadow being dropped.
	got = prependPath([]string{"HOME=/h"}, "/shadow")
	require.Contains(t, got, "PATH=/shadow")
}

// TestNewShellRunner_ShadowsThisBinary: the exported constructor arranges
// the shadow and reports no notice when it succeeds. A notice is not a
// failure — it is the fallback being visible, which is the point, since
// running with the wrong `ape` SILENTLY is the defect.
func TestNewShellRunner_ShadowsThisBinary(t *testing.T) {
	runner, cleanup, notice := NewShellRunner()
	defer cleanup()
	require.Empty(t, notice)
	require.NotEmpty(t, runner.Env)

	var pathValue string
	for _, kv := range runner.Env {
		if after, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathValue = after
			break
		}
	}
	require.NotEmpty(t, pathValue)
	first, _, _ := strings.Cut(pathValue, string(os.PathListSeparator))
	require.FileExists(t, filepath.Join(first, SelfName+exeSuffix()))
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
