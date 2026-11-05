package apecmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// pauseRepo is a project in an ensemble (NATS with the lease buckets and
// the event stream) with M1 claimed by this checkout.
func pauseRepo(t *testing.T) (root string, js jetstream.JetStream) {
	t.Helper()
	root = v1Repo(t)
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err = jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))
	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	block := "coordination:\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: " + url + "\n"
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))
	_, _, err = v1Exec(t, newMilestoneCmd, "claim", "M1", "--cwd", root)
	require.NoError(t, err)
	return root, js
}

// Q-1C-1 (D758): a person pauses and resumes a milestone (OQ6: a hold on
// one milestone is a person act of the person whose checkout holds it).
// The T1: paused, M1's change new and close refuse coord.milestone-paused,
// naming who paused it, why, and how to resume; resumed, change new goes
// through. The controls: from a model session it is refused
// (session.person-only); a checkout whose book no longer holds M1 cannot
// pause it; OQ24's automatic pause still pauses, in its own words.
func TestMilestonePauseResume_APersonAct(t *testing.T) {
	root, js := pauseRepo(t)

	_, _, err := v1Exec(t, newMilestoneCmd, "pause", "M1", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), "a pause names its reason")

	t.Setenv("APE_SESSION", "test-session")
	_, stderr, err := v1Exec(t, newMilestoneCmd, "pause", "M1", "--reason", "x", "--cwd", root)
	require.Error(t, err, "control: never from a model session")
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, stderr+err.Error(), "ape milestone pause is for a person")
	require.NoError(t, os.Unsetenv("APE_SESSION"))

	stdout, _, err := v1Exec(t, newMilestoneCmd, "pause", "M1", "--reason", "wait for M2's gate", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	var res map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Equal(t, true, res["paused"])
	require.Equal(t, "dana", res["by"])

	// D816+: `ape nats pause-state` reads what the gate reads (row 3a).
	pauseState := func() map[string]any {
		t.Helper()
		stdout, _, err := v1Exec(t, newNatsCmd, "pause-state", "M1", "--cwd", root)
		require.NoError(t, err, stdout)
		var st map[string]any
		require.NoError(t, json.Unmarshal([]byte(stdout), &st), stdout)
		return st
	}
	st := pauseState()
	require.Equal(t, true, st["paused"])
	require.Equal(t, "person", st["kind"])
	require.Equal(t, "dana", st["by"])
	require.Equal(t, "dana", st["read_as"])

	for _, args := range [][]string{{"change", "new", "add-wave", "--milestone", "M1", "--touches", "greeting"}, {"milestone", "close", "M1"}} {
		c := newChangeCmd
		if args[0] == "milestone" {
			c = newMilestoneCmd
		}
		stdout, _, err := v1Exec(t, c, append(args[1:], "--json", "--cwd", root)...)
		require.Error(t, err, "%v", args)
		require.Contains(t, stdout, apexv1.CodeCoordMilestonePaused, "%v", args)
		require.Contains(t, stdout, "paused by dana")
		require.Contains(t, stdout, "wait for M2's gate")
		require.Contains(t, stdout, "ape milestone resume M1")
		require.Contains(t, stdout, `"action": "person"`)
	}

	_, _, err = v1Exec(t, newMilestoneCmd, "resume", "M1", "--cwd", root)
	require.NoError(t, err)
	_, _, err = v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err, "resumed: change new goes through")

	// V12-2: a kind-less pause from another member (ann; dana holds M1)
	// is not the holder's conductor's, so it does not pause M1.
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	m, err := member.Load(p)
	require.NoError(t, err)
	_, err = js.Publish(context.Background(), events.Subject("ann", m.Project, events.KindProject, "M1", apexv1.EventMilestonePaused), []byte(`{"by":"ann/conductor","user":"ann"}`))
	require.NoError(t, err)
	_, _, err = v1Exec(t, newChangeCmd, "new", "add-mist", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err, "V12-2: another member's conductor-kind pause does not hold the holder")
	require.Equal(t, false, pauseState()["paused"], "pause-state agrees with the gate")

	// OQ24's pause, as the holder's conductor publishes it, still pauses M1.
	_, err = js.Publish(context.Background(), events.Subject("dana", m.Project, events.KindProject, "M1", apexv1.EventMilestonePaused), []byte(`{"by":"dana/conductor","user":"dana"}`))
	require.NoError(t, err)
	stdout, _, err = v1Exec(t, newChangeCmd, "new", "add-tide", "--milestone", "M1", "--touches", "greeting", "--json", "--cwd", root)
	require.Error(t, err, "control: OQ24's pause still pauses")
	require.Contains(t, stdout, "a question to the conductor went unanswered")
	st = pauseState()
	require.Equal(t, true, st["paused"], "pause-state agrees with the gate")
	require.Nil(t, st["kind"], "a conductor's pause")
	_, _, err = v1Exec(t, newMilestoneCmd, "resume", "M1", "--cwd", root)
	require.NoError(t, err, "a person may resume an OQ24 pause from the holding checkout")

	_, _, err = v1Exec(t, newMilestoneCmd, "release", "M1", "--cwd", root)
	require.NoError(t, err)
	stdout, _, err = v1Exec(t, newMilestoneCmd, "pause", "M1", "--reason", "x", "--json", "--cwd", root)
	require.Error(t, err, "control: a checkout that no longer holds M1 cannot pause it")
	require.Contains(t, stdout, apexv1.CodeNotLeaseHolder)
}
