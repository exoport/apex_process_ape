package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveSecretEnv(t *testing.T) {
	t.Setenv("APE_TEST_SECRET", "  s3cr3t\n")
	v, err := ResolveSecret("env:APE_TEST_SECRET")
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", v, "value should be trimmed")
}

func TestResolveSecretEnvMissing(t *testing.T) {
	_, err := ResolveSecret("env:APE_TEST_DEFINITELY_UNSET")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not set")
}

func TestResolveSecretEnvEmpty(t *testing.T) {
	t.Setenv("APE_TEST_EMPTY", "   ")
	_, err := ResolveSecret("env:APE_TEST_EMPTY")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestResolveSecretFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(path, []byte("ghp_abc\n"), 0o600))
	v, err := ResolveSecret("file:" + path)
	require.NoError(t, err)
	assert.Equal(t, "ghp_abc", v)
}

func TestResolveSecretBadForms(t *testing.T) {
	for _, src := range []string{"", "env:", "literal", "vault:x"} {
		_, err := ResolveSecret(src)
		require.Error(t, err, "src %q should error", src)
	}
}
