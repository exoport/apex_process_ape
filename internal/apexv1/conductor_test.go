package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// conductorProject: M1 proposed with a red gate, a fix tweak and a feat
// tweak, Dana's orders (answer, tweak-approve for fixes, milestone-approve)
// accepted, run from Dana's own Claude Code session (the conductor).
func conductorProject(t *testing.T) *Project {
	t.Helper()
	p := e2Project(t)
	addChange(t, p, "fix-hug", strings.Replace(tweakDoc("fix-hug"), "type: feat", "type: fix", 1))
	addChange(t, p, "fix-wave", tweakDoc("fix-wave")) // type: feat
	orders := "---\nperson: dana@example.com\nacts: [answer, tweak-approve, milestone-approve]\nconditions:\n  tweak-approve: {types: [fix]}\n---\nApprove small fixes.\n"
	write(t, p, OrdersRel("dana@example.com"), orders)
	run(t, p.Root, "add", "-A")
	run(t, p.Root, "commit", "-qm", "orders and tweaks")
	_, err := p.AcceptOrders(ctx(), "dana@example.com", "dana@example.com")
	require.NoError(t, err)
	asClaudeCode(t)
	return p
}

func act(a, target string, more ...func(*DelegatedAct)) DelegatedAct {
	d := DelegatedAct{Act: a, Person: "dana@example.com", Target: target, Reason: "ape status shows it waiting; the brief says go"}
	for _, f := range more {
		f(&d)
	}
	return d
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	return Diagnostics(err)[0].Code
}

// D659 B3's $0 drills and D660.1, each refused, with nothing done.
func TestConductorAct_Refusals(t *testing.T) {
	p := conductorProject(t)
	head := run(t, p.Root, "rev-parse", "HEAD")

	_, err := p.RunDelegated(ctx(), act(OrderMilestoneApprove, "M1", func(d *DelegatedAct) { d.Reason = "  " }), nil)
	require.Equal(t, CodeOrdersNoReason, codeOf(t, err), "no reason")

	for _, a := range []string{OrderAcceptRed, OrderAcceptConfig, OrderAllowPending, OrderOrders, OrderPush} {
		_, err = p.RunDelegated(ctx(), act(a, "M1"), nil)
		require.Equal(t, CodeOrdersNonDelegable, codeOf(t, err), "non-delegable: "+a)
	}

	_, err = p.RunDelegated(ctx(), act(OrderDecisionAccept, "ADR-0002"), nil)
	require.Equal(t, CodeOrdersNotNamed, codeOf(t, err), "tier 2, not named")

	_, err = p.RunDelegated(ctx(), act(OrderAnswer, "fix-hug", func(d *DelegatedAct) { d.Person = "eve@example.com" }), nil)
	require.Equal(t, CodeOrdersMissing, codeOf(t, err), "out of scope: another person's orders")

	_, err = p.RunDelegated(ctx(), act(OrderTweakApprove, "fix-wave"), nil)
	require.Equal(t, CodeOrdersCondition, codeOf(t, err), "a feat tweak, orders approve fixes only")

	_, err = p.RunDelegated(ctx(), act(OrderMilestoneApprove, "M1", func(d *DelegatedAct) { d.Evidence = []string{"development/no-such-brief.md"} }), nil)
	require.Equal(t, CodeOrdersEvidence, codeOf(t, err), "evidence that does not exist")

	// The script route: the person-act command itself, from the model
	// session, with no grant — refused as before (A7).
	_, err = p.ApproveMilestone(ctx(), "M1", "Ann Person", nil)
	require.Equal(t, CodePersonOnly, codeOf(t, err), "direct call from a model session")

	// Orders changed by a model (committed past the pre-tool guard, by a
	// script): unaccepted, so nothing runs under them.
	asPerson(t)
	path := filepath.Join(p.Root, OrdersRel("dana@example.com"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(data), "acts: [answer, tweak-approve, milestone-approve]", "acts: [answer, tweak-approve, milestone-approve, decision-accept]", 1)), 0o644))
	run(t, p.Root, "commit", "-qam", "widen")
	asClaudeCode(t)
	_, err = p.RunDelegated(ctx(), act(OrderMilestoneApprove, "M1"), nil)
	require.Equal(t, CodeOrdersUnaccepted, codeOf(t, err), "changed since accepted")

	require.Equal(t, MilestoneProposed, p.roadmap().Find("M1").Status, "nothing was approved")
	require.Equal(t, "widen", run(t, p.Root, "log", "--format=%s", head+"..HEAD"), "no commit of ape's: only the person's")
}

// A revocation counts at the next act.
func TestConductorAct_Revoked(t *testing.T) {
	p := conductorProject(t)
	asPerson(t)
	_, err := p.RevokeOrders(ctx(), "dana@example.com", "dana@example.com")
	require.NoError(t, err)
	asClaudeCode(t)
	_, err = p.RunDelegated(ctx(), act(OrderTweakApprove, "fix-hug"), nil)
	require.Equal(t, CodeOrdersRevoked, codeOf(t, err))
}

// An act that holds runs on the person's code path, by the conductor under
// the orders; its event is logged (the notifier runs) and its decision-log
// line carries the reason and the evidence's digest.
func TestConductorAct_Runs(t *testing.T) {
	p := conductorProject(t)
	write(t, p, "development/briefs/fix-hug.md", "the person wants hugs fixed\n")
	res, err := p.RunDelegated(ctx(), act(OrderTweakApprove, "fix-hug", func(d *DelegatedAct) {
		d.Evidence = []string{"development/briefs/fix-hug.md"}
		d.Reason = "brief development/briefs/fix-hug.md asks for it; change check is clean"
	}), nil)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(res.By, "conductor under orders dana-example-com@"), res.By)
	require.Contains(t, res.By, "change check is clean")
	require.Len(t, res.Evidence, 1)
	require.Len(t, res.Evidence[0].SHA256, 64)
	require.Equal(t, res.By, state(t, p, "fix-hug").Approved.By, "recorded as the tweak's approval")

	evs := p.EventsSince(0)
	last := evs[len(evs)-1]
	require.Equal(t, EventDelegated, last.Event)
	require.Equal(t, "tweak-approve fix-hug", last.Action)
	require.Equal(t, res.Orders, last.Orders)

	l, err := p.ReadDecisionLog("dana@example.com")
	require.NoError(t, err)
	require.Len(t, l.Entries, 1)
	require.Equal(t, DecisionAct, l.Entries[0].Kind)
	require.Equal(t, res.Evidence, l.Entries[0].Evidence)
	md, err := os.ReadFile(p.DecisionLogPath("dana@example.com"))
	require.NoError(t, err)
	require.Contains(t, string(md), "act tweak-approve fix-hug")
}

// D660.3: holds and questions stay open until resolved; a resumed
// conductor reads what is open.
func TestDecisionLog_Notes(t *testing.T) {
	p := fixture(t, baseFiles())
	h, err := p.NoteDecision("dana@example.com", DecisionHold, "do not approve M3 before the pricing call", 0)
	require.NoError(t, err)
	q, err := p.NoteDecision("dana@example.com", DecisionQuestion, "which currency for invoices?", 0)
	require.NoError(t, err)
	_, err = p.NoteDecision("dana@example.com", DecisionResolved, "EUR, per Dana", 99)
	require.Equal(t, "usage.resolves", codeOf(t, err))
	_, err = p.NoteDecision("dana@example.com", DecisionResolved, "EUR, per Dana", q.N)
	require.NoError(t, err)

	l, err := p.ReadDecisionLog("dana@example.com")
	require.NoError(t, err)
	open := l.Open()
	require.Len(t, open, 1)
	require.Equal(t, h.N, open[0].N)
	md, err := os.ReadFile(p.DecisionLogPath("dana@example.com"))
	require.NoError(t, err)
	require.Contains(t, string(md), "hold: do not approve M3")
}

// D662.2: ape re-runs the mechanical check behind the act, whatever the
// conductor was told: a tweak whose change check is red, a gate folder
// whose plain run is green — refused, nothing done; a gate folder as it
// must be lets the approve run, its self-test logs in the evidence.
func TestConductorAct_Rechecks(t *testing.T) {
	p := conductorProject(t)
	asPerson(t)
	write(t, p, "development/changes/fix-hug/change.md", strings.Replace(strings.Replace(tweakDoc("fix-hug"), "type: feat", "type: fix", 1), "## Tasks\n- T1", "## Tasks\n- X1", 1))
	commitAll(t, p, "break fix-hug")
	asClaudeCode(t)
	_, err := p.RunDelegated(ctx(), act(OrderTweakApprove, "fix-hug"), nil)
	require.Equal(t, CodeOrdersCheckRed, codeOf(t, err))
	require.ErrorContains(t, err, "ape change check fix-hug is red")

	asPerson(t)
	gate := filepath.Join(p.GatesDir(), "M1-skeleton", "gate.sh")
	require.NoError(t, os.MkdirAll(filepath.Dir(gate), 0o755))
	require.NoError(t, os.WriteFile(gate, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	commitAll(t, p, "a gate that passes before any work")
	asClaudeCode(t)
	_, err = p.RunDelegated(ctx(), act(OrderMilestoneApprove, "M1"), nil)
	require.Equal(t, CodeOrdersCheckRed, codeOf(t, err), "the orchestrator may have said 'all good'; ape ran it")
	require.ErrorContains(t, err, "ape milestone gate-test")
	require.Equal(t, MilestoneProposed, p.roadmap().Find("M1").Status)

	asPerson(t)
	require.NoError(t, os.WriteFile(gate, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	commitAll(t, p, "the gate is red until the work is done")
	asClaudeCode(t)
	res, err := p.RunDelegated(ctx(), act(OrderMilestoneApprove, "M1"), nil)
	require.NoError(t, err)
	require.Len(t, res.Checks, 1)
	require.Contains(t, res.Checks[0], "ape milestone gate-test")
	require.Contains(t, res.Checks[0], "plain exit 1 pass")
	require.NotEmpty(t, res.Evidence, "the self-test's log is evidence")
	require.Equal(t, MilestoneApproved, p.roadmap().Find("M1").Status)
}

// D702: the conductor's push-branch never pushes a milestone's branch or
// an apex/* head (one writer: the milestone's lease holder's ape); the
// control, a work branch, gets as far as git push.
func TestPushBranch_RefusesApeBranches(t *testing.T) {
	p := fixture(t, baseFiles())
	for _, b := range []string{"milestone/M1", "apex/add-wave", "feat/x"} {
		run(t, p.Root, "branch", b)
	}
	for _, b := range []string{"milestone/M1", "apex/add-wave"} {
		_, err := p.pushBranch(ctx(), b)
		require.ErrorContains(t, err, "never milestone/* or apex/*", b)
	}
	_, err := p.pushBranch(ctx(), "feat/x")
	require.ErrorContains(t, err, "git push failed", "a work branch reaches git push (no remote here)")
}
