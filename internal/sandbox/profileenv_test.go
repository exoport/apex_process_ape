package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileEnvLinesCarryCachesAndProxyButNotSecrets(t *testing.T) {
	// The allowlist is the whole point: an ssh session needs the durable-cache paths and
	// the egress proxy, and must not get a copy of credential material written to a host
	// file. A denylist would fail open the next time a secret-bearing variable is added.
	lines := ProfileEnvLines([]string{
		"GOPATH=/cache/go",
		"GOBIN=/cache/go/bin",
		"GOMODCACHE=/cache/go/pkg/mod",
		"ASDF_DATA_DIR=/cache/asdf",
		"HTTPS_PROXY=http://169.254.42.1:3200",
		"ANTHROPIC_API_KEY=sk-ant-secret",
		"APE_NATS_CREDS=/sandbox/home/.ape/vm.creds",
		"APE_JOB_KEY=sk-also-secret",
		"HOME=/sandbox/home",
	})

	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "export GOPATH='/cache/go'")
	assert.Contains(t, joined, "export GOBIN='/cache/go/bin'")
	assert.Contains(t, joined, "export ASDF_DATA_DIR='/cache/asdf'")
	assert.Contains(t, joined, "export HTTPS_PROXY='http://169.254.42.1:3200'")

	assert.NotContains(t, joined, "sk-ant-secret", "credential material must not reach a file")
	assert.NotContains(t, joined, "sk-also-secret")
	assert.NotContains(t, joined, "APE_NATS_CREDS")
	assert.NotContains(t, joined, "HOME=", "HOME is the shell's own; overriding it from here would be hostile")
}

func TestWrittenProfileEnvCannotExecuteItsValues(t *testing.T) {
	// The file is `source`d by a login shell, so quoting is a security property, not
	// cosmetics. Asserted by actually sourcing it in /bin/sh: a substring check would only
	// prove the text is present, which it is — the question is whether the shell RUNS it.
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("no sh to source with: %v", err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	hostile := `/cache/go'; touch ` + marker + `; echo '`

	require.NoError(t, WriteGuestProfileEnv(dir, []string{"GOPATH=" + hostile}))

	out, err := exec.CommandContext(t.Context(), "sh", "-c",
		`. "$1" >/dev/null 2>&1; printf %s "$GOPATH"`, "sh", filepath.Join(dir, GuestProfileEnvFile)).Output()
	require.NoError(t, err)

	assert.NoFileExists(t, marker, "sourcing the file must not run anything the value contains")
	assert.Equal(t, hostile, string(out), "and the value must survive intact, not mangled")
}

func TestWriteGuestProfileEnvIsRewrittenEveryTime(t *testing.T) {
	// A stale file pointing at caches this workspace no longer mounts is worse than none:
	// go would silently write somewhere the workspace does not persist.
	dir := t.TempDir()
	require.NoError(t, WriteGuestProfileEnv(dir, []string{"GOPATH=/cache/go"}))
	first, err := os.ReadFile(filepath.Join(dir, GuestProfileEnvFile))
	require.NoError(t, err)
	assert.Contains(t, string(first), "/cache/go")

	require.NoError(t, WriteGuestProfileEnv(dir, nil))
	second, err := os.ReadFile(filepath.Join(dir, GuestProfileEnvFile))
	require.NoError(t, err)
	assert.NotContains(t, string(second), "/cache/go", "the previous create's env must not survive")

	info, err := os.Stat(filepath.Join(dir, GuestProfileEnvFile))
	require.NoError(t, err)
	if runtimeIsPOSIX() {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func runtimeIsPOSIX() bool { return os.PathSeparator == '/' }
