package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// repoRoot is the ape repository's root, two levels above this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return root
}

// TestRepo_InstructionsAreAgentsMDOnly keeps this repository's instructions
// loadable. Claude Code reads a root AGENTS.md only when no CLAUDE.md exists;
// with both present it reads CLAUDE.md alone, so a CLAUDE.md created by habit
// (`/init`, or `ape framework setup`, which writes one wherever none exists)
// switches AGENTS.md off with no error. This is the free half of that guard
// and runs everywhere; `make check-agents-md` is the live half.
func TestRepo_InstructionsAreAgentsMDOnly(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	require.FileExists(t, filepath.Join(root, "AGENTS.md"))
	_, err := os.Stat(filepath.Join(root, "CLAUDE.md"))
	require.True(t, os.IsNotExist(err),
		"%s/CLAUDE.md exists: Claude Code now reads it INSTEAD of AGENTS.md, so this repo's instructions no "+
			"longer apply. Move anything worth keeping into AGENTS.md and delete CLAUDE.md "+
			"(`/init` and `ape framework setup` both create one)", root)
}
