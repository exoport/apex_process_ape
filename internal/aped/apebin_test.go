package aped

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// moduleAnchor is any directory inside this module: `go build` resolves a package path
// from the module it finds by walking upwards, so the working directory only has to be
// somewhere inside it.
const moduleAnchor = ".."

// buildFakeApe compiles a binary whose main package path is the real ape's, so its build
// info passes the identity check. Compiled rather than faked because the whole point of
// the verification is that it reads REAL build info — a hand-written fixture would prove
// nothing about buildinfo.ReadFile.
//
// ldflags optionally stamps a version the way goreleaser does.
func buildFakeApe(t *testing.T, version string) string {
	t.Helper()
	if runtime.GOOS == goosWindows {
		t.Skip("delivery is a Linux-guest mechanism; the mode/exec checks are POSIX")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go not on PATH: %v", err)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "ape")
	args := []string{"build", "-o", out}
	if version != "" {
		args = append(args, "-ldflags",
			"-X github.com/exoport/apex_process_ape/internal/apecmd.Version="+version)
	}
	args = append(args, "github.com/exoport/apex_process_ape/cmd/ape")
	cmd := exec.CommandContext(t.Context(), "go", args...)
	cmd.Dir = moduleAnchor
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build a fixture ape (%v): %s", err, b)
	}
	return out
}

func TestResolveApeBinaryAcceptsTheRealApe(t *testing.T) {
	path := buildFakeApe(t, "0.0.50")

	bin, err := ResolveApeBinary(path, "0.0.50", "")
	require.NoError(t, err)
	assert.Equal(t, path, bin.Path)
	assert.Equal(t, filepath.Dir(path), bin.Dir, "the DIRECTORY is what gets mounted")
	assert.Equal(t, "0.0.50", bin.Version, "read from the ldflags stamp, not by running it")
	assert.Contains(t, bin.String(), "0.0.50")
}

func TestResolveApeBinaryRefusesAVersionMismatch(t *testing.T) {
	// The staleness case: aped redeployed, ape left behind (or vice versa). Refused, not
	// warned — a workspace running an ape older than its daemon is the bug this exists for.
	path := buildFakeApe(t, "0.0.49")

	_, err := ResolveApeBinary(path, "0.0.50", "")
	require.ErrorIs(t, err, ErrNoApeBinary)
	assert.Contains(t, err.Error(), "0.0.49")
	assert.Contains(t, err.Error(), "0.0.50")
	assert.Contains(t, err.Error(), "same release")
}

func TestResolveApeBinaryTellsLocalBuildsApart(t *testing.T) {
	// A partial `make build` — rebuild one binary, leave the other — is the staleness case
	// that has bitten this project repeatedly. It is caught WITHOUT any ldflags because
	// since Go 1.24 an unstamped build still carries a VCS-derived pseudo-version, and that
	// embeds the commit hash. So the plain version comparison separates two local builds
	// from different commits; the revision check below is the belt to that suspenders.
	path := buildFakeApe(t, "")

	bin, err := ResolveApeBinary(path, "", "")
	require.NoError(t, err, "with no version to compare against, nothing to refuse")
	assert.NotEqual(t, "dev", bin.Version,
		"an unstamped local build still reports a pseudo-version, which is what makes the "+
			"version comparison meaningful for a dev loop")

	t.Run("a version from another commit is refused", func(t *testing.T) {
		_, err := ResolveApeBinary(path, "0.0.50-0.20200101000000-000000000000", "")
		require.ErrorIs(t, err, ErrNoApeBinary)
		assert.Contains(t, err.Error(), "same release")
	})

	t.Run("same version, different commit is refused", func(t *testing.T) {
		if bin.Revision == "" {
			t.Skip("this toolchain stamped no vcs.revision, so there is nothing to compare")
		}
		_, err := ResolveApeBinary(path, bin.Version, strings.Repeat("a", 40))
		require.ErrorIs(t, err, ErrNoApeBinary)
		assert.Contains(t, err.Error(), "different builds")
	})
}

func TestResolveApeBinaryRefusesADifferentProgram(t *testing.T) {
	// A binary named `ape` that is not ape. The identity check is the main package path,
	// which no version string could establish — a wrong binary self-reports anything.
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go not on PATH: %v", err)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "ape")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", out,
		"github.com/exoport/apex_process_ape/cmd/aped") // aped standing in for "something else"
	cmd.Dir = moduleAnchor
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build the fixture (%v): %s", err, b)
	}

	_, err := ResolveApeBinary(out, "", "")
	require.ErrorIs(t, err, ErrNoApeBinary)
	assert.Contains(t, err.Error(), "different program")
	assert.Contains(t, err.Error(), "cmd/aped")
}

func TestResolveApeBinaryRefusesNonGoAndMissingFiles(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()

	t.Run("a shell script wearing the name", func(t *testing.T) {
		p := filepath.Join(dir, "script-ape")
		require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\necho ape version 9.9.9\n"), 0o755))
		_, err := ResolveApeBinary(p, "", "")
		require.ErrorIs(t, err, ErrNoApeBinary)
		assert.Contains(t, err.Error(), "no Go build info")
	})

	t.Run("absent, with the actionable hint", func(t *testing.T) {
		_, err := ResolveApeBinary(filepath.Join(dir, "nope"), "", "")
		require.ErrorIs(t, err, ErrNoApeBinary)
		assert.Contains(t, err.Error(), "both binaries")
		assert.Contains(t, err.Error(), "--ape-binary")
	})

	t.Run("not executable", func(t *testing.T) {
		p := filepath.Join(dir, "noexec-ape")
		require.NoError(t, os.WriteFile(p, []byte("\x7fELF"), 0o644))
		_, err := ResolveApeBinary(p, "", "")
		require.ErrorIs(t, err, ErrNoApeBinary)
		assert.Contains(t, err.Error(), "not executable")
	})
}

func TestResolveApeBinaryOnWritability(t *testing.T) {
	// Mounted into every workspace and executed there as root-in-VM, so who can rewrite it
	// matters. The two cases are deliberately NOT treated alike: world-writable has no
	// legitimate cause, while group-writable is just `go build` under a 002 umask.
	path := buildFakeApe(t, "0.0.50")

	t.Run("world-writable is fatal", func(t *testing.T) {
		require.NoError(t, os.Chmod(path, 0o777))
		_, err := ResolveApeBinary(path, "0.0.50", "")
		require.ErrorIs(t, err, ErrNoApeBinary)
		assert.Contains(t, err.Error(), "world-writable")
	})

	t.Run("group-writable is served, with a warning", func(t *testing.T) {
		require.NoError(t, os.Chmod(path, 0o775)) // what `go build` leaves under umask 002
		bin, err := ResolveApeBinary(path, "0.0.50", "")
		require.NoError(t, err, "refusing this would reject ordinary build output")
		require.Len(t, bin.Warnings, 1)
		assert.Contains(t, bin.Warnings[0], "group-writable")
	})

	t.Run("0755 is clean", func(t *testing.T) {
		require.NoError(t, os.Chmod(path, 0o755)) // what `install -m 0755` leaves
		bin, err := ResolveApeBinary(path, "0.0.50", "")
		require.NoError(t, err)
		assert.Empty(t, bin.Warnings)
	})
}
