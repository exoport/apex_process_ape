package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// D155 Q73: project check's notices — v1Repo's M1 gate is ./gate.sh,
// outside development/gates/ — are listed beside the findings, in JSON
// and as notice: lines, and never change the exit.
func TestProjectCheck_Notices(t *testing.T) {
	root := v1Repo(t)
	type result struct {
		OK       bool                `json:"ok"`
		Exit     int                 `json:"exit"`
		Findings []apexv1.Finding    `json:"findings"`
		Notices  []apexv1.Diagnostic `json:"notices"`
	}

	stdout, stderr, err := v1Exec(t, newProjectCmd, "check", "--cwd", root)
	require.NoError(t, err, "a notice is not a finding")
	require.Equal(t, "project check: green\n", stdout)
	require.Contains(t, stderr, "notice: development/roadmap.md:3: M1 gate runs scripts outside development/gates/: gate.sh — "+
		"move them into development/gates/M1-<slug>/ with the milestone's next re-approval")

	stdout, _, err = v1Exec(t, newProjectCmd, "check", "--json", "--cwd", root)
	require.NoError(t, err)
	var green result
	require.NoError(t, json.Unmarshal([]byte(stdout), &green), stdout)
	require.True(t, green.OK)
	require.Empty(t, green.Findings)
	require.Len(t, green.Notices, 1)
	n := green.Notices[0]
	require.Equal(t, apexv1.SeverityNotice, n.Severity)
	require.Equal(t, "gate.outside-gates", n.Code)
	require.Equal(t, apexv1.ActionPerson, n.Action)

	// With a finding, the exit is the finding's, and the notices are there.
	require.NoError(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# no import\n"), 0o644))
	stdout, _, err = v1Exec(t, newProjectCmd, "check", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	var red result
	require.NoError(t, json.Unmarshal([]byte(stdout), &red), stdout)
	require.False(t, red.OK)
	require.Equal(t, 1, red.Exit)
	require.Len(t, red.Findings, 1)
	require.Equal(t, green.Notices, red.Notices)

	// The gate in its folder: no notice, and "notices" is still a list.
	gate := "development/gates/M1-skeleton/gate.sh"
	require.NoError(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("@AGENTS.md\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(filepath.FromSlash(gate))), 0o755))
	require.NoError(t, os.Rename(filepath.Join(root, "gate.sh"), filepath.Join(root, filepath.FromSlash(gate))))
	roadmap := filepath.Join(root, "development", "roadmap.md")
	doc, err := os.ReadFile(roadmap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(roadmap, []byte(strings.Replace(string(doc), "Gate: ./gate.sh", "Gate: ./"+gate, 1)), 0o644))
	_, _, err = v1Exec(t, newMilestoneCmd, "approve", "M1", "--by", "eval", "--cwd", root)
	require.NoError(t, err)
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "gate folder")
	stdout, stderr, err = v1Exec(t, newProjectCmd, "check", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	require.NotContains(t, stderr, "notice:")
	require.Contains(t, stdout, `"notices": []`)
}
