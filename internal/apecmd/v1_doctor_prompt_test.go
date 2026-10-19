package apecmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// ape doctor's hooks.prompt row.
func TestDoctor_HooksPrompt(t *testing.T) {
	root := v1Repo(t)
	res := checkV1PromptHook(t.Context(), doctorEnv{ProjectRoot: root})
	require.Equal(t, StatusOK, res.Status, res.Message)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"hooks":{}}`), 0o644))
	res = checkV1PromptHook(t.Context(), doctorEnv{ProjectRoot: root})
	require.Equal(t, StatusWarn, res.Status)
	require.Contains(t, res.Message, "is not registered for UserPromptSubmit")
	require.Contains(t, res.FixCommand, "ape project init")
}
