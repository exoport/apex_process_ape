package apexv1

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// tweakDoc is a tweak (milestone: none) that adds a requirement, with a
// new test of its own.
func tweakDoc(id string) string {
	doc := changeDoc(id, "greeting",
		"### ADDED [greeting] Requirement: Hug\nThe system SHALL hug.\n#### Scenario: hug\n- GIVEN x WHEN y THEN z\n- check: ./hug.sh\n",
		"- T1 add hug — check: ./hug.sh\n")
	return strings.Replace(doc, "milestone: M1", "milestone: none", 1)
}

// addTweak writes a tweak folder with a draft state and commits it on
// the branch HEAD is on.
func addTweak(t *testing.T, p *Project, id string) {
	t.Helper()
	addChange(t, p, id, tweakDoc(id))
	commitAll(t, p, "plan: "+id)
}

// D40: `change new --milestone none --type <t>` writes a tweak — no
// roadmap lookup, no Out of scope inherited — and `change check` accepts
// milestone: none.
func TestNewChange_Tweak(t *testing.T) {
	files := baseFiles()
	delete(files, "development/roadmap.md")
	p := fixture(t, files)
	res, err := p.NewChangeOfType(ctx(), "fix-hello", MilestoneNone, "fix", []string{"greeting"}, nil)
	require.NoError(t, err, "a tweak needs no roadmap")
	doc := read(t, p, res.Path)
	require.Contains(t, doc, "milestone: none\n")
	require.Contains(t, doc, "the request this tweak answers")
	require.NotContains(t, doc, "- farewells")

	write(t, p, res.Path, tweakDoc("fix-hello"))
	require.Empty(t, findingMessages(p.CheckChange("fix-hello")), "milestone: none is valid")
	write(t, p, res.Path, strings.Replace(tweakDoc("fix-hello"), "milestone: none", "milestone: M9", 1))
	require.Contains(t, findingMessages(p.CheckChange("fix-hello")), "milestone M9 is not in roadmap.md")

	_, err = p.NewChange(ctx(), "fix-other", "M9", []string{"greeting"}, nil)
	require.ErrorContains(t, err, "no roadmap.md", "a milestone change still needs the roadmap")
}

func TestApproveTweak(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	addTweak(t, p, "fix-hug")

	_, err := p.ApproveTweak(ctx(), "fix-hug", " ")
	var ue *UsageError
	require.ErrorAs(t, err, &ue)
	_, err = p.ApproveTweak(ctx(), "add-wave", "ann")
	require.Equal(t, "change.approve-milestone", failCode(t, err))
	require.ErrorContains(t, err, "`ape milestone approve M1 --by <who>`")

	// A tweak that fails change check is not approved.
	write(t, p, "development/changes/fix-hug/change.md", strings.Replace(tweakDoc("fix-hug"), "## Tasks\n- T1", "## Tasks\n- X1", 1))
	_, err = p.ApproveTweak(ctx(), "fix-hug", "ann")
	require.Equal(t, "change.check-failed", failCode(t, err))
	run(t, p.Root, "checkout", "--", ".")

	res, err := p.ApproveTweak(ctx(), "fix-hug", "ann")
	require.NoError(t, err)
	require.False(t, res.Already)
	require.Equal(t, "ann", res.Approved.By)
	require.Equal(t, "apex: fix-hug approved", subjects(t, p)[0], "committed on the base branch")
	require.Contains(t, run(t, p.Root, "log", "-1", "--format=%b"), "Change: fix-hug")
	clean(t, p, "approve")
	st := state(t, p, "fix-hug")
	require.Equal(t, StatusDraft, st.Status)
	require.Equal(t, ParseChange("", read(t, p, "development/changes/fix-hug/change.md")).PlanDigest(), st.Approved.PlanDigest)

	again, err := p.ApproveTweak(ctx(), "fix-hug", "bob")
	require.NoError(t, err)
	require.True(t, again.Already, "the same plan approved again is a no-op")
	require.Equal(t, "ann", again.Approved.By)
	require.Equal(t, "apex: fix-hug approved", subjects(t, p)[0])

	// Started, it is approved already: approve is refused, on its branch
	// or off it.
	_, err = p.Start(ctx(), "fix-hug", RunOptions{})
	require.NoError(t, err)
	_, err = p.ApproveTweak(ctx(), "fix-hug", "ann")
	require.Equal(t, CodeWrongStatus, failCode(t, err))
	run(t, p.Root, "switch", "-q", "main")
	_, err = p.ApproveTweak(ctx(), "fix-hug", "ann")
	require.Equal(t, CodeWrongStatus, failCode(t, err))
}

// A tweak starts on its own approval, not a milestone's: none, or one of
// a plan that changed since, is refused.
func TestStart_TweakNeedsItsApproval(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Status: approved (by eval 2026-10-15T12:00:00Z)", "Status: proposed", 1)
	p := fixture(t, files)
	addTweak(t, p, "fix-hug")

	_, err := p.Start(ctx(), "fix-hug", RunOptions{})
	require.Equal(t, CodeNotApproved, failCode(t, err))
	require.ErrorContains(t, err, "`ape change approve fix-hug --by <who>`")
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root), "a refused start leaves no branch")
	require.False(t, p.branchExists(ctx(), "change/fix-hug"))

	_, err = p.ApproveTweak(ctx(), "fix-hug", "ann")
	require.NoError(t, err)
	write(t, p, "development/changes/fix-hug/change.md", strings.Replace(tweakDoc("fix-hug"), "The system SHALL hug.", "The system SHALL hug twice.", 1))
	commitAll(t, p, "replan: fix-hug")
	_, err = p.Start(ctx(), "fix-hug", RunOptions{})
	require.Equal(t, CodeNotApproved, failCode(t, err))
	require.ErrorContains(t, err, "plan changed since ann approved it")

	_, err = p.ApproveTweak(ctx(), "fix-hug", "bob")
	require.NoError(t, err)
	res, err := p.Start(ctx(), "fix-hug", RunOptions{})
	require.NoError(t, err, "the milestone M1 is not approved, and a tweak does not need it")
	require.Equal(t, "change/fix-hug", res.Branch)
	require.Equal(t, 127, res.Baseline[0].Exit, "the tweak's new test is red at its baseline")
}

// `ape status` lists approved tweaks first, unapproved ones last, and
// points next at the first approved tweak (D40); the lane is "tweak" and
// the queue is JSON "tweaks" (D240).
func TestStatus_TweaksFirst(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	addTweak(t, p, "fix-zed")
	addTweak(t, p, "fix-alpha")
	addTweak(t, p, "fix-hug")
	now = func() time.Time { return time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC) }
	_, err := p.ApproveTweak(ctx(), "fix-zed", "ann")
	require.NoError(t, err)
	now = func() time.Time { return time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	_, err = p.ApproveTweak(ctx(), "fix-hug", "bob")
	require.NoError(t, err)

	r := p.Status()
	ids := func() []string {
		out := make([]string, 0, len(r.Changes))
		for _, c := range r.Changes {
			out = append(out, c.ID)
		}
		return out
	}
	require.Equal(t, []string{"fix-zed", "fix-hug", "add-wave", "fix-alpha"}, ids(), "approved tweaks by approval time, then milestone work, then tweaks awaiting approval")
	require.Equal(t, []string{"fix-zed", "fix-hug"}, r.Tweaks)
	require.Equal(t, LaneTweak, r.Changes[0].Lane)
	require.Equal(t, "tweak", r.Changes[0].Lane)
	js, err := json.Marshal(r)
	require.NoError(t, err)
	require.Contains(t, string(js), `"tweaks":["fix-zed","fix-hug"]`)
	require.Contains(t, string(js), `"lane":"tweak"`)
	require.NotContains(t, string(js), `"fixes"`)
	require.NotContains(t, string(js), `"lane":"fix"`)
	require.Equal(t, "ann", r.Changes[0].Approved.By)
	require.Equal(t, LaneMilestone, r.Changes[2].Lane)
	require.Nil(t, r.Changes[3].Approved)
	require.Contains(t, r.Next, "tweak fix-zed (approved by ann, draft) runs ahead of milestone work")
	require.Contains(t, r.Next, "`ape task apex-loop --args fix-zed`")
	brief := r.Brief()
	require.Contains(t, brief, "tweak (approved by ann)")
	require.Contains(t, brief, "tweak — awaiting approval: `ape change approve fix-alpha --by <who>`")
	require.Less(t, strings.Index(brief, "fix-zed"), strings.Index(brief, "add-wave"))

	// A started tweak runs before an approved draft; a blocked one is no
	// longer next.
	_, err = p.Start(ctx(), "fix-hug", RunOptions{})
	require.NoError(t, err)
	run(t, p.Root, "switch", "-q", "main")
	r = p.Status()
	require.Equal(t, []string{"fix-hug", "fix-zed"}, r.Tweaks)
	require.Contains(t, r.Next, "tweak fix-hug (approved by bob, in-progress, on change/fix-hug)")
	run(t, p.Root, "switch", "-q", "change/fix-hug")
	_, err = p.Block(ctx(), "fix-hug", "needs a person")
	require.NoError(t, err)
	// fix-zed is a draft on main: blocked there from another change's
	// branch (D51 Q17).
	_, err = p.Block(ctx(), "fix-zed", "needs a person")
	require.NoError(t, err)
	require.Equal(t, "apex: fix-zed blocked", run(t, p.Root, "log", "-1", "--format=%s", "main"))
	run(t, p.Root, "switch", "-q", "main")
	require.Equal(t, StatusBlocked, state(t, p, "fix-zed").Status)
	r = p.Status()
	require.Empty(t, r.Tweaks)
	require.Contains(t, r.Next, "apply add-wave", "with no runnable tweak, milestone work is next")
}

// A tweak counts toward no milestone (D40): close ignores a tweak in
// flight, the gate's regression baseline ignores a tweak's, and a tweak's
// runs count toward no milestone's spend or budget.
func TestTweak_CountsTowardNoMilestone(t *testing.T) {
	p := fixture(t, suiteFiles())
	addTweak(t, p, "fix-hug")
	_, err := p.ApproveTweak(ctx(), "fix-hug", "ann")
	require.NoError(t, err)
	t.Setenv("SUITE_RED", "1")
	_, err = p.Start(ctx(), "fix-hug", RunOptions{})
	require.NoError(t, err)
	t.Setenv("SUITE_RED", "")
	require.NotNil(t, state(t, p, "fix-hug").Regression)
	run(t, p.Root, "switch", "-q", "main")

	change, base := p.milestoneRegressionBaseline(ctx(), "M1", "./suite.sh")
	require.Empty(t, change, "a tweak's regression baseline is no milestone's")
	require.Nil(t, base)

	writeRunArgs(t, p, "apex-orchestrator", "20261015-130000-aaaa", "2026-10-15T13:00:00Z", 25, "fix-hug")
	writeRunArgs(t, p, "apex-apply", "20261015-140000-bbbb", "2026-10-15T14:00:00Z", 5, "--no-commit add-wave")
	require.InDelta(t, 5, p.Status().Milestone.SpentUSD, 1e-9, "the tweak's run is not M1's spend")
	write(t, p, "_apex/config.yaml", "project_name: greeter\nregression: ./suite.sh\nlimits:\n  budget: enforce\n")
	tl, err := p.TaskLimitsFor(ctx(), "apex-orchestrator", "fix-hug")
	require.NoError(t, err)
	require.Nil(t, tl, "no milestone budget applies to a tweak's session")
	tl, err = p.TaskLimitsFor(ctx(), "apex-orchestrator", "add-wave")
	require.NoError(t, err)
	require.NotNil(t, tl)
	require.InDelta(t, 5, tl.SpentUSD, 1e-9)
	run(t, p.Root, "checkout", "--", "_apex/config.yaml")

	g, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err, "a tweak in flight does not hold the milestone's close")
	require.Empty(t, g.InFlight)
	require.Equal(t, MilestoneDone, g.Status)
}

// writeRunArgs is writeRun for a run whose step had --args.
func writeRunArgs(t *testing.T, p *Project, skill, runID, startedAt string, costUSD float64, args string) {
	t.Helper()
	dir := filepath.Join(runlog.TasksRoot(p.Root), skill, runID)
	write(t, p, p.Rel(filepath.Join(dir, "manifest.yaml")),
		"run_id: "+runID+"\nstarted_at: "+startedAt+"\nstatus: completed\ntotals:\n  cost_usd: "+strconv.FormatFloat(costUSD, 'f', -1, 64)+
			"\nstages:\n  - steps:\n      - index: 0\n        skill: "+skill+"\n        args: \""+args+"\"\n")
}

func TestRunChangeArg(t *testing.T) {
	require.Equal(t, "fix-hug", RunChangeArg("--no-commit fix-hug --x"))
	require.Empty(t, RunChangeArg("--no-commit"))
	require.Empty(t, RunChangeArg(""))
}
