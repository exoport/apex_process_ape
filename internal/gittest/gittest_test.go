package gittest

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	QuietMaintenance()
	os.Exit(m.Run())
}

// gitConfigGet runs `git config --get key` in dir with only the
// environment's configuration (no global or system file), returning the
// value and whether git found the key.
func gitConfigGet(t *testing.T, dir, key string) (string, bool) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "config", "--get", key)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func hermeticRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	out, err := exec.CommandContext(t.Context(), "git", "init", "-q", dir).CombinedOutput()
	require.NoError(t, err, string(out))
	return dir
}

// AppendConfig adds its key and keeps TestMain's: maintenance.auto is still
// false after a test adds an insteadOf rule.
func TestAppendConfig_KeepsEarlierKeys(t *testing.T) {
	dir := hermeticRepo(t)
	AppendConfig(t, "url.git@private.alias:.insteadOf", "https://example.invalid/")

	got, ok := gitConfigGet(t, dir, "maintenance.auto")
	require.True(t, ok, "maintenance.auto is gone: AppendConfig replaced the list instead of extending it")
	require.Equal(t, "false", got)
	got, ok = gitConfigGet(t, dir, "gc.auto")
	require.True(t, ok)
	require.Equal(t, "0", got)
	got, ok = gitConfigGet(t, dir, "url.git@private.alias:.insteadOf")
	require.True(t, ok)
	require.Equal(t, "https://example.invalid/", got)
	require.Equal(t, "3", os.Getenv(countEnv))

	AppendConfig(t, "user.name", "gittest")
	got, ok = gitConfigGet(t, dir, "user.name")
	require.True(t, ok)
	require.Equal(t, "gittest", got)
	require.Equal(t, "4", os.Getenv(countEnv))
}

// The control: the pattern AppendConfig replaced. Setting the count to 1 for
// one key drops TestMain's maintenance.auto.
func TestAppendConfig_OverwritingTheCountDropsTestMainKeys(t *testing.T) {
	dir := hermeticRepo(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.git@private.alias:.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://example.invalid/")
	_, ok := gitConfigGet(t, dir, "maintenance.auto")
	require.False(t, ok, "expected the overwritten list to have lost maintenance.auto")
}

// With no list in the environment, AppendConfig starts one at index 0.
func TestAppendConfig_StartsAListWhenThereIsNone(t *testing.T) {
	dir := hermeticRepo(t)
	t.Setenv(countEnv, "")
	AppendConfig(t, "user.name", "gittest")
	require.Equal(t, "1", os.Getenv(countEnv))
	require.Equal(t, "user.name", os.Getenv(keyEnv+"0"))
	got, ok := gitConfigGet(t, dir, "user.name")
	require.True(t, ok)
	require.Equal(t, "gittest", got)
}

// setsCountDirectly matches a test setting GIT_CONFIG_COUNT itself, which
// replaces the list TestMain built rather than adding to it.
var setsCountDirectly = regexp.MustCompile(`Setenv\(\s*"GIT_CONFIG_COUNT"`)

// TestNoTestSetsGitConfigCountDirectly: outside this package, a test adds a
// git config key through AppendConfig, never by setting GIT_CONFIG_COUNT.
func TestNoTestSetsGitConfigCountDirectly(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	self, err := filepath.Abs(".")
	require.NoError(t, err)

	var hits []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path == self || (path != root && strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if setsCountDirectly.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, rel+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, hits,
		"these tests set GIT_CONFIG_COUNT themselves, which drops TestMain's maintenance.auto/gc.auto (D360); "+
			"use gittest.AppendConfig(t, key, value) instead")
}
