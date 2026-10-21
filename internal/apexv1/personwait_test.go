package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const roadmapWithProposedM2 = roadmapApproved + `
## M2 — Farewells
Status: proposed
Gate: ./gate2.sh
Goal: A greeter that says goodbye.
`

func actionsOf(acts []PersonAction) []string {
	out := make([]string, len(acts))
	for i, a := range acts {
		out[i] = a.Action
	}
	return out
}

func personEvents(t *testing.T, p *Project) []Event {
	t.Helper()
	var out []Event
	for _, ev := range p.EventsSince(0) {
		if ev.Event == EventPerson {
			out = append(out, ev)
		}
	}
	return out
}

// W3-A2: each kind of person act is read from the recorded state, with the
// phrase the person types: a proposed milestone, a tweak awaiting approval,
// a blocked change, a human-review task, an unaccepted config change.
func TestPersonActions_EachKind(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = roadmapWithProposedM2
	p := fixture(t, files)
	require.Equal(t, []string{"approve M2"}, actionsOf(p.PersonActions(ctx())))

	addTweak(t, p, "fix-hug")
	reason := "which greeting?"
	addChange(t, p, "add-bow", changeDoc("add-bow", "greeting", "", "- T1 bow — check: true\n"))
	st, err := LoadState(p.StateFile("add-bow"))
	require.NoError(t, err)
	st.Status, st.BlockedReason = StatusBlocked, &reason
	require.NoError(t, SaveState(p.StateFile("add-bow"), st))
	commitAll(t, p, "plan: add-bow")
	acts := p.PersonActions(ctx())
	require.Equal(t, []string{"approve M2", "approve fix-hug", "answer add-bow: <answer>"}, actionsOf(acts))
	require.Equal(t, PersonAction{
		Kind: ActAnswer, Ref: "add-bow", Milestone: "M1", Change: "add-bow", Action: "answer add-bow: <answer>",
		Reason: "add-bow is blocked: which greeting?",
	}, acts[2])
	require.Equal(t, ActTweakApprove, acts[1].Kind)
	require.Empty(t, acts[1].Milestone, "a tweak names no milestone")

	// config.changed: a guarded block changed by a commit that is not ape's.
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	write(t, p, "_apex/config.yaml", "limits:\n  attempts: 9\n")
	commitAll(t, p, "loosen the attempts")
	acts = p.PersonActions(ctx())
	require.Equal(t, "accept the config", acts[len(acts)-1].Action)
	require.Contains(t, acts[len(acts)-1].Reason, "config.changed: guarded config keys (limits)")
}

// A reviewed change's human-review task is the person's to accept — unless
// a delegate may take it now, which `next` tries first.
func TestPersonActions_HumanReviewTask(t *testing.T) {
	p := reviewedWave(t)
	acts := p.PersonActions(ctx())
	require.Equal(t, []string{"accept T2 of add-wave"}, actionsOf(acts))
	require.Equal(t, "add-wave", acts[0].Change)
	require.Equal(t, "T2", acts[0].Task)

	r := p.Status()
	r.Changes[0].Delegable = []DelegableTask{{Task: "T2", Reason: DelegableConfigured}}
	require.Empty(t, p.PersonActionsOf(ctx(), r), "the delegate's first")
}

// The delta logs `person` once per new act: a proposed milestone is logged
// once, a second look logs nothing, an approval drops it from the set (no
// event), and an act that comes back is new again.
func TestEmitPersonDelta_OncePerNewAct(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "notify: 'printf \"%s|%s|%s\\n\" \"$APE_EVENT\" \"$APE_ACTION\" \"$APE_MILESTONE\" >> notified.txt'\n"
	files[".gitignore"] = "notified.txt\n"
	p := fixture(t, files)
	require.Empty(t, p.EmitPersonDelta(ctx(), p.Status(), nil), "nothing waits on a person")

	write(t, p, "development/roadmap.md", roadmapWithProposedM2)
	commitAll(t, p, "plan: M2")
	logged := p.EmitPersonDelta(ctx(), p.Status(), nil)
	require.Len(t, logged, 1)
	require.Equal(t, "approve M2", logged[0].Action)
	require.Equal(t, "M2", logged[0].Milestone)
	require.Contains(t, logged[0].Reason, "M2 (Farewells) is proposed")
	require.Equal(t, "person|approve M2|M2\n", read(t, p, "notified.txt"), "the notifier ran with APE_ACTION")

	require.Empty(t, p.EmitPersonDelta(ctx(), p.Status(), nil), "a second look logs nothing")
	require.Len(t, personEvents(t, p), 1)

	// Approved: gone from the set, no event.
	write(t, p, "development/roadmap.md", strings.Replace(roadmapWithProposedM2, "Status: proposed", "Status: approved (by ann 2026-10-21T12:00:00Z)", 1))
	require.Empty(t, p.EmitPersonDelta(ctx(), p.Status(), nil))
	require.NotContains(t, read(t, p, "_apex/state/person-actions.json"), "approve M2")

	// Proposed again: a new wait.
	write(t, p, "development/roadmap.md", roadmapWithProposedM2)
	require.Len(t, p.EmitPersonDelta(ctx(), p.Status(), nil), 1)
	require.Len(t, personEvents(t, p), 2)
	clean(t, p, "the deltas")
}

// gate-green is logged when a run turns the recorded result green, never
// for a green that stays green nor for a red; close logs milestone-done.
func TestGateEvents(t *testing.T) {
	files := baseFiles()
	files["gate.sh"] = "#!/bin/sh\ntest -f green\n"
	files[".gitignore"] = "green\n"
	p := fixture(t, files)
	gateEvents := func() []string {
		var out []string
		for _, ev := range p.EventsSince(0) {
			out = append(out, ev.Event+" "+ev.Milestone)
		}
		return out
	}

	_, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Empty(t, gateEvents(), "red: no event")

	write(t, p, "green", "")
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, g.ResultChanged)
	require.Equal(t, []string{"gate-green M1"}, gateEvents())
	evs := p.EventsSince(0)
	require.Equal(t, "gate green at "+short(g.Run.SHA), evs[0].Reason)

	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"gate-green M1"}, gateEvents(), "green again: nothing new")

	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"gate-green M1", "milestone-done M1"}, gateEvents())
	require.Contains(t, p.EventsSince(1)[0].Reason, "closed, gate green at ")
}

// A close that turns the gate green logs gate-green, then milestone-done;
// an accept-red close logs milestone-done with the person's reason.
func TestGateEvents_CloseTurnsGreenAndAcceptRed(t *testing.T) {
	files := baseFiles()
	files["gate.sh"] = "#!/bin/sh\nexit 0\n"
	p := fixture(t, files)
	_, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	var names []string
	for _, ev := range p.EventsSince(0) {
		names = append(names, ev.Event)
	}
	require.Equal(t, []string{EventGateGreen, EventMilestoneDone}, names)

	q := fixture(t, baseFiles()) // ./gate.sh is missing: red
	_, err = q.CloseMilestoneAcceptRed(ctx(), "M1", "known drift", "Ann Lee", RunOptions{})
	require.NoError(t, err)
	evs := q.EventsSince(0)
	require.Len(t, evs, 1)
	require.Equal(t, EventMilestoneDone, evs[0].Event)
	require.Equal(t, "closed with its gate red, accepted by Ann Lee: known drift", evs[0].Reason)
}
