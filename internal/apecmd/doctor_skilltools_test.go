package apecmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeBin writes an executable shell script named name into dir. POSIX
// only: the callers skip on Windows, where a script is not an executable.
func fakeBin(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
}

// onlyPath points PATH at a fresh directory holding nothing but what the
// test puts there, and returns it.
func onlyPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake executables are POSIX shell scripts")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	return dir
}

func TestCheckPython3Binary(t *testing.T) {
	dir := onlyPath(t)

	res := checkPython3Binary(context.Background(), doctorEnv{})
	require.Equal(t, StatusWarn, res.Status)
	require.Contains(t, res.Remediation, "apex-create-wireframes")
	require.Contains(t, res.Remediation, "apex-create-mockups")
	require.Empty(t, res.FixCommand)

	// Only the literal name counts: a `python` launcher does not satisfy
	// skills that call python3.
	fakeBin(t, dir, "python", "exit 0")
	require.Equal(t, StatusWarn, checkPython3Binary(context.Background(), doctorEnv{}).Status)

	fakeBin(t, dir, "python3", "exit 0")
	res = checkPython3Binary(context.Background(), doctorEnv{})
	require.Equal(t, StatusOK, res.Status)
	require.Equal(t, filepath.Join(dir, "python3"), res.Message)
}

// TestCheckPNGConverter covers both converters. Either one passes: this
// machine class has cairosvg and no rsvg-convert, and a check on
// rsvg-convert alone would warn forever there.
func TestCheckPNGConverter(t *testing.T) {
	t.Run("cairosvg imports", func(t *testing.T) {
		dir := onlyPath(t)
		fakeBin(t, dir, "python3", `[ "$1" = "-c" ] && [ "$2" = "import cairosvg" ] && exit 0; exit 9`)
		res := checkPNGConverter(context.Background(), doctorEnv{})
		require.Equal(t, StatusOK, res.Status)
		require.Contains(t, res.Message, "cairosvg")
	})
	t.Run("cairosvg missing, rsvg-convert present", func(t *testing.T) {
		dir := onlyPath(t)
		fakeBin(t, dir, "python3", "exit 1")
		fakeBin(t, dir, "rsvg-convert", "exit 0")
		res := checkPNGConverter(context.Background(), doctorEnv{})
		require.Equal(t, StatusOK, res.Status)
		require.Equal(t, "rsvg-convert at "+filepath.Join(dir, "rsvg-convert"), res.Message)
	})
	t.Run("no python3, rsvg-convert present", func(t *testing.T) {
		dir := onlyPath(t)
		fakeBin(t, dir, "rsvg-convert", "exit 0")
		require.Equal(t, StatusOK, checkPNGConverter(context.Background(), doctorEnv{}).Status)
	})
	t.Run("neither", func(t *testing.T) {
		dir := onlyPath(t)
		fakeBin(t, dir, "python3", "exit 1")
		res := checkPNGConverter(context.Background(), doctorEnv{})
		require.Equal(t, StatusWarn, res.Status)
		for _, want := range []string{"cairosvg", "rsvg-convert", "apex-inject-screen-stories"} {
			require.Contains(t, res.Message+res.Remediation, want)
		}
		require.Empty(t, res.FixCommand)
	})
	t.Run("a wedged interpreter cannot stall doctor", func(t *testing.T) {
		// Resolved before PATH is emptied: a bare `sleep` would not be
		// found, the fake would exit at once, and the timeout would go
		// untested.
		sleepBin, err := exec.LookPath("sleep")
		require.NoError(t, err)
		dir := onlyPath(t)
		fakeBin(t, dir, "python3", "exec "+sleepBin+" 30")
		prev := pngImportTimeout
		pngImportTimeout = 300 * time.Millisecond
		t.Cleanup(func() { pngImportTimeout = prev })

		start := time.Now()
		res := checkPNGConverter(context.Background(), doctorEnv{})
		elapsed := time.Since(start)
		require.Equal(t, StatusWarn, res.Status)
		require.GreaterOrEqual(t, elapsed, 300*time.Millisecond, "the probe must have waited on the import")
		require.Less(t, elapsed, 10*time.Second)
	})
}

// shadowFixture builds a project and a home, each with the named skills
// installed as <dir>/<name>/SKILL.md.
func shadowFixture(t *testing.T, userSkills, projectSkills []string) doctorEnv {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	install := func(base string, names []string) {
		for _, n := range names {
			dir := filepath.Join(base, n)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("stub"), 0o644))
		}
	}
	install(filepath.Join(home, ".claude", "skills"), userSkills)
	install(filepath.Join(root, ".claude", "skills"), projectSkills)
	return doctorEnv{ProjectRoot: root, Home: home}
}

func TestCheckSkillsShadowed(t *testing.T) {
	t.Run("apex-* in both warns, names each, deletes nothing", func(t *testing.T) {
		env := shadowFixture(t,
			[]string{"apex-two", "apex-one", "apex-user-only", "custom"},
			[]string{"apex-one", "apex-two", "apex-project-only", "custom"})
		res := checkSkillsShadowed(context.Background(), env)
		require.Equal(t, StatusWarn, res.Status)
		require.Contains(t, res.Message, "2 apex-* skill(s)")
		require.True(t, strings.HasSuffix(res.Message, ": apex-one, apex-two"), res.Message)
		require.NotContains(t, res.Message, "custom", "a non-framework name is the operator's own business")
		require.Contains(t, res.Remediation, "personal-first")
		require.Empty(t, res.FixCommand, "the personal copy may hold the operator's edits")

		for _, n := range []string{"apex-one", "apex-two"} {
			_, err := os.Stat(filepath.Join(env.Home, ".claude", "skills", n, "SKILL.md"))
			require.NoError(t, err)
		}
	})
	t.Run("no overlap is OK", func(t *testing.T) {
		env := shadowFixture(t, []string{"apex-user-only", "custom"}, []string{"apex-one", "custom"})
		require.Equal(t, StatusOK, checkSkillsShadowed(context.Background(), env).Status)
	})
	t.Run("a directory without SKILL.md is not a skill", func(t *testing.T) {
		env := shadowFixture(t, nil, []string{"apex-one"})
		require.NoError(t, os.MkdirAll(filepath.Join(env.Home, ".claude", "skills", "apex-one"), 0o755))
		require.Equal(t, StatusOK, checkSkillsShadowed(context.Background(), env).Status)
	})
	t.Run("outside a project it is skipped", func(t *testing.T) {
		res := checkSkillsShadowed(context.Background(), doctorEnv{ProjectRoot: t.TempDir(), Home: t.TempDir()})
		require.Equal(t, StatusInfo, res.Status)
	})
}

func TestDoctorRegistry_SkillToolRows(t *testing.T) {
	names := map[string]bool{}
	for _, c := range allChecks {
		names[c.Name] = true
		if c.Name == "python3.binary" || c.Name == "png.converter" || c.Name == "skills.shadowed" {
			require.False(t, c.Required, "%s must stay WARN-level: a missing tool or a shadowed skill is not a failed install", c.Name)
		}
	}
	for _, want := range []string{"python3.binary", "png.converter", "skills.shadowed"} {
		require.True(t, names[want], "%s missing from allChecks", want)
	}
}
