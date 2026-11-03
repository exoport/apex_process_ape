package apexv1

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type triggerLog struct {
	mu  sync.Mutex
	all []Trigger
}

func (l *triggerLog) of(event string) []Trigger {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Trigger
	for _, t := range l.all {
		if t.Event == event {
			out = append(out, t)
		}
	}
	return out
}

func recordTriggers(t *testing.T) *triggerLog {
	t.Helper()
	l := &triggerLog{}
	old := TriggerSink
	TriggerSink = func(_ context.Context, _ *Project, tr Trigger) {
		l.mu.Lock()
		l.all = append(l.all, tr)
		l.mu.Unlock()
	}
	t.Cleanup(func() { TriggerSink = old })
	return l
}

// PLAN-V15-ASSESSOR step 1: the assessor's three triggers (§3.2), emitted
// by the acts themselves — a coordinated archive that landed
// (change-archived: the change, the landed commit, the paths it changed),
// a gate run that turns the milestone's result red (gate-red), and a
// milestone's approval (plan-approved). Each carries the key its message
// id is built from (Trigger.Key). The control: a run that turns it green
// emits no gate-red, and nothing is emitted twice for one act.
func TestTriggers_EmittedByTheActs(t *testing.T) {
	trig := recordTriggers(t)
	p, _ := coordinatedReviewedWave(t)
	useIntegrator(t, &fakeIntegrator{})

	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	arch := trig.of(EventChangeArchived)
	require.Len(t, arch, 1)
	require.Equal(t, Trigger{Event: EventChangeArchived, Milestone: "M1", Change: "add-wave", Commit: res.Commit, Paths: arch[0].Paths, Key: "add-wave"}, arch[0])
	require.Contains(t, arch[0].Paths, "wave.sh", "the paths the landed commit changed")

	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.False(t, g.Run.Green(), "./gate.sh does not exist: red")
	red := trig.of(EventGateRed)
	require.Len(t, red, 1)
	require.Equal(t, "M1", red[0].Milestone)
	require.Equal(t, g.Run.SHA, red[0].Commit)
	require.Equal(t, g.Run.SHA+"@"+g.Run.At, red[0].Key)

	write(t, p, "gate.sh", "#!/bin/sh\nexit 0\n")
	commitAll(t, p, "test: the gate passes")
	_, _ = p.RunGate(ctx(), "M1", RunOptions{})
	require.Len(t, trig.of(EventGateRed), 1, "control: a green result emits no gate-red")
	t.Logf("keys: change-archived %q, gate-red %q", arch[0].Key, red[0].Key)
}

// plan-approved: a person's approval through ape (here of an M1 whose
// approval ape holds no record of, H2). The control: an approval that
// changes nothing (already recorded, no drift) emits none.
func TestTriggers_PlanApproved(t *testing.T) {
	trig := recordTriggers(t)
	files := baseFiles()
	files["_apex/state/milestones.json"] = "{}\n"
	files["gate.sh"] = "#!/bin/sh\nexit 1\n"
	p := fixture(t, files)
	_, approved, err := p.ApproveMilestoneOutcome(ctx(), "M1", "ann", nil, nil)
	require.NoError(t, err)
	require.True(t, approved)
	ap := trig.of(EventPlanApproved)
	require.Len(t, ap, 1)
	require.Equal(t, "M1", ap[0].Milestone)
	require.NotEmpty(t, ap[0].Key, "the approval's at")
	require.Equal(t, p.roadmap().Find("M1").Gate, ap[0].Gate)
	t.Logf("key: plan-approved %q", ap[0].Key)

	_, approved, err = p.ApproveMilestoneOutcome(ctx(), "M1", "ann", nil, nil)
	require.NoError(t, err)
	require.False(t, approved)
	require.Len(t, trig.of(EventPlanApproved), 1, "control: nothing approved, nothing emitted")
}
