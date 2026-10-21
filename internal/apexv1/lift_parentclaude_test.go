package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const parentV0Block = "# Workspace\n\n<!-- apex:managed:begin -->\n@_apex/apex-operating-rules.md\n<!-- apex:managed:end -->\n"

// RG-2 (D249, D277): a CLAUDE.md in a folder above the project whose
// apex-v0 managed block imports the v0 operating rules is a warning,
// named relative to the project; one without the block, or the project's
// own CLAUDE.md, is none.
func TestParentV0Claude(t *testing.T) {
	top := t.TempDir()
	ws := filepath.Join(top, "ws")
	root := filepath.Join(ws, "group", "proj")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.Empty(t, parentV0Claude(root))

	require.NoError(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(parentV0Block), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "group", "CLAUDE.md"), []byte("# Group\n\n- plain rules, no block\n"), 0o644))
	require.Empty(t, parentV0Claude(root), "the project's own CLAUDE.md is the lift's to clean; a parent without the block is fine")

	require.NoError(t, os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte(parentV0Block), 0o644))
	w := parentV0Claude(root)
	require.Len(t, w, 1)
	require.Equal(t, CodeLiftParentClaude, w[0].Code)
	require.Equal(t, filepath.Join(ws, "CLAUDE.md"), w[0].Path)
	require.Equal(t, "../../CLAUDE.md", w[0].Rel)
	require.True(t, strings.HasPrefix(w[0].Message, "../../CLAUDE.md holds an apex-v0 managed block"), w[0].Message)

	require.False(t, holdsV0ManagedImport("<!-- apex:managed:begin -->\n@AGENTS.md\n<!-- apex:managed:end -->\n"), "another import is not the v0 rules")
	require.False(t, holdsV0ManagedImport("@_apex/apex-operating-rules.md\n"), "outside a managed block")
}

// D282 (a): the committed disposition names the parent file relative to
// the project root, never an absolute home path; the JSON keeps both.
func TestLift_ParentV0ClaudeWarnsRelative(t *testing.T) {
	root := liftFixture(t, true)
	parent := filepath.Dir(root)
	require.NoError(t, os.WriteFile(filepath.Join(parent, "CLAUDE.md"), []byte(parentV0Block), 0o644))
	res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	require.Equal(t, filepath.Join(parent, "CLAUDE.md"), res.Warnings[0].Path)
	disp, err := os.ReadFile(filepath.Join(root, "development", LiftDisposition))
	require.NoError(t, err)
	require.Contains(t, string(disp), "**Warning (lift.parent-v0-claude):** ../CLAUDE.md holds an apex-v0 managed block")
	require.NotContains(t, string(disp), parent, "no absolute path in the committed file")
}
