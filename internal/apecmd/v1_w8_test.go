package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// v1.1 W8 through the commands.

// D224: milestone approve --json carries the approval's self_check, as
// milestones.json records it — on the approval, and on a no-op after it.
func TestMilestoneApprove_JSONCarriesSelfCheck(t *testing.T) {
	root := v1Repo(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gate2.sh"), []byte("#!/bin/sh\necho not yet\nexit 1\n"), 0o755))
	v1Git(t, root, "add", "gate2.sh")
	v1Git(t, root, "commit", "-q", "-m", "gate2")
	_, _, err := v1Exec(t, newMilestoneCmd, "new", "Second", "--gate", "./gate2.sh", "--goal", "more", "--cwd", root)
	require.NoError(t, err)

	type out struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		SelfCheck *struct {
			At           string  `json:"at"`
			SHA          string  `json:"sha"`
			Exit         int     `json:"exit"`
			Secs         float64 `json:"secs"`
			Log          string  `json:"log"`
			OutputSHA256 string  `json:"output_sha256"`
		} `json:"self_check"`
	}
	approve := func() out {
		t.Helper()
		stdout, stderr, err := v1Exec(t, newMilestoneCmd, "approve", "M2", "--by", "eval", "--json", "--cwd", root)
		require.NoError(t, err, stderr)
		var o out
		require.NoError(t, json.Unmarshal([]byte(stdout), &o), stdout)
		return o
	}
	first := approve()
	require.Equal(t, "M2", first.ID)
	require.Equal(t, "approved", first.Status)
	require.NotNil(t, first.SelfCheck, "D224: approve's JSON carries the self-check")
	require.Equal(t, 1, first.SelfCheck.Exit, "the gate was recorded red")
	require.Equal(t, "_apex/state/logs/gate-M2-approve.log", first.SelfCheck.Log)
	require.Len(t, first.SelfCheck.OutputSHA256, 64)
	require.NotEmpty(t, first.SelfCheck.SHA)

	var recs map[string]struct {
		Approval struct {
			SelfCheck json.RawMessage `json:"self_check"`
		} `json:"approval"`
	}
	data, err := os.ReadFile(filepath.Join(root, "_apex", "state", "milestones.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &recs))
	var recorded struct {
		At, SHA string
		Exit    int
	}
	require.NoError(t, json.Unmarshal(recs["M2"].Approval.SelfCheck, &recorded))
	require.Equal(t, first.SelfCheck.At, recorded.At, "the one milestones.json records")

	again := approve()
	require.NotNil(t, again.SelfCheck, "a no-op approve shows the approval's self-check on record")
	require.Equal(t, first.SelfCheck.At, again.SelfCheck.At)
}
