package apecmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// D162: init names `ape framework setup` while no framework skill is
// installed, and drops it once one is.
func TestInitNext_NamesFrameworkSetupUntilSkillsExist(t *testing.T) {
	root := t.TempDir()
	require.Contains(t, initNext(root), "install the framework's skills with `ape framework setup`, then write development/project.md")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude", "skills", "my-own"), 0o755))
	require.Contains(t, initNext(root), "`ape framework setup`", "a skill that is not apex-* is not the framework")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude", "skills", "apex-plan"), 0o755))
	require.Equal(t, "write development/project.md, AGENTS.md and the roadmap (apex-plan), then `ape project check`", initNext(root))
}
