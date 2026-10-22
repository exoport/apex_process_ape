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
			OutputSHA256 string  `json:"output_sha256"` //nolint:tagliatelle // the v1 --json payloads are snake_case by contract
		} `json:"self_check"` //nolint:tagliatelle // the v1 --json payloads are snake_case by contract
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
			SelfCheck json.RawMessage `json:"self_check"` //nolint:tagliatelle // milestones.json is snake_case by contract
		} `json:"approval"`
	}
	data, err := os.ReadFile(filepath.Join(root, "_apex", "state", "milestones.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &recs))
	var recorded struct {
		At   string `json:"at"`
		SHA  string `json:"sha"`
		Exit int    `json:"exit"`
	}
	require.NoError(t, json.Unmarshal(recs["M2"].Approval.SelfCheck, &recorded))
	require.Equal(t, first.SelfCheck.At, recorded.At, "the one milestones.json records")

	again := approve()
	require.NotNil(t, again.SelfCheck, "a no-op approve shows the approval's self-check on record")
	require.Equal(t, first.SelfCheck.At, again.SelfCheck.At)
}

// D226: accept-config accepts a relay threshold under a fresh loop
// session's fill, and gives the notice project check gives.
func TestAcceptConfig_ContextRelayLowNotice(t *testing.T) {
	root := v1Repo(t)
	cfg := filepath.Join(root, "_apex", "config.yaml")
	base, err := os.ReadFile(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, append(base, []byte("limits:\n  context_relay: 0.05\n")...), 0o644))
	v1Git(t, root, "commit", "-qam", "chore: relay early")
	stdout, stderr, err := v1Exec(t, newProjectCmd, "accept-config", "--by", "ann", "--json", "--cwd", root)
	require.NoError(t, err, stderr)
	var out struct {
		OK      bool   `json:"ok"`
		Commit  string `json:"commit"`
		Notices []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Action   string `json:"action"`
			File     string `json:"file"`
			Message  string `json:"message"`
		} `json:"notices"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout)
	require.True(t, out.OK)
	require.Len(t, out.Notices, 1)
	require.Equal(t, "limits.context-relay-low", out.Notices[0].Code)
	require.Equal(t, "person", out.Notices[0].Action)
	require.Contains(t, out.Notices[0].Message, "relays the loop at 50000 tokens")

	stdout, stderr, err = v1Exec(t, newProjectCmd, "check", "--cwd", root)
	require.NoError(t, err, stdout+stderr)
	require.Contains(t, stderr, "notice: _apex/config.yaml: limits.context_relay 0.05 relays the loop at 50000 tokens")
}

// Q134 through the hooks: the person's typed words back a person act in
// their own turn and the one notification turn right after — two
// notification turns on, pre-tool denies and the command refuses; the
// person typing again grants it.
func TestE2_Q134_GrantNarrowedToItsTurn(t *testing.T) {
	root := e2Repo(t, "exit 1")
	s := newE2Session(t, root, "q134")
	cmdline := "ape milestone approve M2 --wait 9m --json"
	notification := "<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n" +
		"<summary>Background command \"run the tests\" completed (exit code 0)</summary>\n</task-notification>"

	s.say(t, "p1", "Run the tests in the background; when they pass, approve M2.")
	s.notify(t, "n1", notification)
	s.notify(t, "n2", notification)
	reason := s.preTool(t, "n2", cmdline)
	require.Contains(t, reason, "it is not the turn right after the person's latest typed prompt")
	require.Contains(t, reason, "never later (Q134)")
	require.Contains(t, reason, "**approve M2**")
	stdout, _, err := v1Exec(t, newMilestoneCmd, "approve", "M2", "--json", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, `"code": "session.person-only"`)

	s.say(t, "p2", "approve M2")
	require.Empty(t, s.preTool(t, "p2", cmdline))
	stdout, _, err = v1Exec(t, newMilestoneCmd, "approve", "M2", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, `: "approve M2")`)
}
