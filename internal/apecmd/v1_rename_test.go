package apecmd

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/exoport/apex_process_ape/apescript"
	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/stretchr/testify/require"
)

// Q95: apex-orchestrator is the operator's interface since rc.7, and every
// launcher of a skill refuses it with skill.renamed (exit 2, action stop),
// naming both pointers — the loop with the run's own args, and the slash
// command — before anything is read or spawned. Not an alias: an older
// resume line fails loudly.
func TestRename_EveryLauncherRefusesTheOperatorSkill(t *testing.T) {
	outsideEverySession(t)
	root := v1Repo(t)

	_, _, err := v1Exec(t, newTaskCmd, "apex-orchestrator", "--args", "M3", "--cwd", root)
	require.Error(t, err)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, err.Error(), "code skill.renamed, action stop")
	require.Contains(t, err.Error(), "`ape task apex-loop --args M3`", "the run's own args")
	require.Contains(t, err.Error(), "type `/apex-orchestrator`")
	require.Equal(t, apexv1.ActionStop, apexv1.ActionFor(apexv1.CodeSkillRenamed))

	// Detached too: refused before a detached run is created.
	_, _, err = v1Exec(t, newTaskCmd, "apex-orchestrator", "--detach", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, err.Error(), "`ape task apex-loop --args <M>`", "no args: the placeholder")

	// ape script's RunTask.
	runner := &scriptRunner{projectRoot: root, quiet: true, claudeBin: "/nonexistent/claude"}
	_, err = runner.runTask(context.Background(), apescript.TaskOpts{Skill: "apex-orchestrator", Args: "M2"})
	var ue *apexv1.UsageError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, apexv1.CodeSkillRenamed, ue.Code)
	require.Contains(t, ue.Msg, "`ape task apex-loop --args M2`")

	// ape service submit task: refused before any NATS is needed.
	_, _, err = v1Exec(t, newServiceSubmitCmd, "task", `{"project_root":"`+root+`","skill":"apex-orchestrator","args":"M1"}`)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, err.Error(), "code skill.renamed")
	require.Contains(t, err.Error(), "`ape task apex-loop --args M1`")

	// Any other skill passes the rename check (and meets the next rule).
	require.NoError(t, refuseRenamed("ape task", "apex-loop", "M1", root))
	require.NoError(t, refuseRenamed("ape task", "apex-plan", "", root))
}

// In a v0 project apex-orchestrator still names v0's persona: the project's
// own refusal (V0ProjectMsg) answers, not the rename.
func TestRename_V0ProjectKeepsItsOwnRefusal(t *testing.T) {
	outsideEverySession(t)
	root := v0Repo(t)
	require.NoError(t, refuseRenamed("ape task", "apex-orchestrator", "", root))
	_, _, err := v1Exec(t, newTaskCmd, "apex-orchestrator", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, err.Error(), apexv1.V0ProjectMsg)
	require.NotContains(t, err.Error(), "skill.renamed")
}

// A loop run an ape before rc.7 recorded as apex-orchestrator is still the
// loop's: its children are allowed, as apex-loop's are (Q95, history as
// recorded).
func TestRename_ALegacyLoopRunKeepsItsPrivilege(t *testing.T) {
	for _, skill := range []string{"apex-loop", "apex-orchestrator"} {
		at := apexv1.SessionAt{Depth: 1, Run: "r", Skill: skill, Kind: "task"}
		require.True(t, at.Orchestrator(), skill)
		depth, err := apexv1.SessionStart(at, "ape task", "apex-apply", apexv1.CoreChildSkills)
		require.NoError(t, err, skill)
		require.Equal(t, 2, depth)
	}
	require.False(t, apexv1.SessionAt{Depth: 1, Skill: "apex-plan", Kind: "task"}.Orchestrator())
}

// ape costs groups the loop's two names into one row in a v1 project; the
// rollup (JSON) keeps both buckets as recorded.
func TestRename_CostsGroupTheLoopsNames(t *testing.T) {
	tasks := map[string]cost.Bucket{
		"apex-orchestrator": {Totals: cost.Totals{CostUSD: 1.5, NumTurns: 3}, Runs: map[string]cost.Totals{"a": {CostUSD: 1.5}}},
		"apex-loop":         {Totals: cost.Totals{CostUSD: 2, NumTurns: 4}, Runs: map[string]cost.Totals{"b": {CostUSD: 2}}},
		"apex-apply":        {Totals: cost.Totals{CostUSD: 1}},
	}
	got := groupLoopTasks(tasks)
	require.Len(t, got, 2)
	loop := got["apex-loop (+apex-orchestrator)"]
	require.InDelta(t, 3.5, loop.Totals.CostUSD, 1e-9)
	require.Equal(t, 7, loop.Totals.NumTurns)
	require.Len(t, loop.Runs, 2)
	require.Contains(t, got, "apex-apply")
	require.Len(t, tasks, 3, "the rollup itself is left as recorded")

	only := map[string]cost.Bucket{"apex-loop": {Totals: cost.Totals{CostUSD: 2}}}
	require.Equal(t, only, groupLoopTasks(only), "nothing to group without the older name")

	data, err := json.Marshal(tasks)
	require.NoError(t, err)
	require.Contains(t, string(data), `"apex-orchestrator"`)
}
