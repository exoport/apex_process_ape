package apecmd

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/member"
)

// PLAN-V15-ASSESSOR step 6 (Q3, research 5): a person acks an impact
// notice, acted or dismissed, from the checkout holding its milestone. The
// T1: the holder's ack lands once, recorded with the credential's user,
// and ape status no longer lists the notice as open. The controls: a
// model session is refused (session.person-only; pre-tool denies it too,
// PersonOnly); a notice to a milestone this checkout does not hold is
// refused; an unknown id is refused; a second ack is refused.
func TestCoordNoticeAck_APersonActFromTheHolder(t *testing.T) {
	root, js := pauseRepo(t)
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	m, err := member.Load(p)
	require.NoError(t, err)
	ctx := context.Background()
	for _, ms := range []string{"M1", "M2"} {
		id := "b-1-" + m.Project + "-" + ms
		_, err := events.Publish(ctx, js, events.Subject("coord", "shop", events.KindCoord, id, "impact-notice"), id+"/impact-notice",
			[]byte(`{"id":"`+id+`","batch":"b-1","target":"`+m.Project+`/`+ms+`","reason":"r","evidence":["a.go"],"hold":false,"events":["e"],"at":"2026-11-03T05:00:00Z"}`))
		require.NoError(t, err)
	}
	m1 := "b-1-" + m.Project + "-M1"
	openOf := func() map[string][]string {
		stdout, _, err := v1Exec(t, newStatusCmd, "--json", "--cwd", root)
		require.NoError(t, err)
		var r struct {
			Milestones []struct {
				ID      string `json:"id"`
				Notices []struct {
					ID string `json:"id"`
				} `json:"notices"`
			} `json:"milestones"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &r))
		out := map[string][]string{}
		for _, ms := range r.Milestones {
			for _, n := range ms.Notices {
				out[ms.ID] = append(out[ms.ID], n.ID)
			}
		}
		return out
	}
	require.Equal(t, []string{m1}, openOf()["M1"], "status lists the open notice")

	t.Setenv("APE_SESSION", "loop-session")
	_, stderr, err := v1Exec(t, newCoordCmd, "notice", "ack", m1, "acted", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), "control: a loop's ack is refused")
	require.Contains(t, stderr+err.Error(), "ape coord notice ack is for a person")
	require.NoError(t, os.Unsetenv("APE_SESSION"))

	_, _, err = v1Exec(t, newCoordCmd, "notice", "ack", m1, "maybe", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	stdout, _, err := v1Exec(t, newCoordCmd, "notice", "ack", "b-9-nope-M1", "acted", "--json", "--cwd", root)
	require.Error(t, err)
	require.Contains(t, stdout, apexv1.CodeNoticeUnknown)
	stdout, _, err = v1Exec(t, newCoordCmd, "notice", "ack", "b-1-"+m.Project+"-M2", "dismissed", "--json", "--cwd", root)
	require.Error(t, err, "control: M2 is not this checkout's claim")
	require.Contains(t, stdout, apexv1.CodeNotLeaseHolder)

	stdout, _, err = v1Exec(t, newCoordCmd, "notice", "ack", m1, "acted", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	var ack member.NoticeAck
	require.NoError(t, json.Unmarshal([]byte(stdout), &ack))
	require.Equal(t, member.NoticeAck{ID: m1, Target: m.Project + "/M1", Milestone: "M1", Outcome: "acted", Kind: "person", By: "dana", User: "dana", At: ack.At}, ack)
	t.Logf("notice-acked payload: %s", stdout)
	require.Empty(t, openOf()["M1"], "an acked notice is no longer open")
	left, ok := member.ReadNotices(ctx, js, "shop", m.Project)
	require.True(t, ok)
	require.Len(t, left["M2"], 1, "another milestone's notice stays open")

	stdout, _, err = v1Exec(t, newCoordCmd, "notice", "ack", m1, "dismissed", "--json", "--cwd", root)
	require.Error(t, err, "control: recorded once")
	require.Contains(t, stdout, apexv1.CodeNoticeAcked)
	require.Contains(t, stdout, "acted, by dana")
}
