package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// D47: `- T<n> <what> — regression: <command>` is a task line too; it
// runs like a check task.
func TestParseTasks_Regression(t *testing.T) {
	c := ParseChange("c.md", changeDoc("add-wave", "greeting", addedWave,
		"- T1 add wave — check: ./wave.sh\n"+
			"- T2 greet still works — regression: `./greet.sh | grep -q Hello`\n"+
			"- T3 fix the check: wording – check: ./words.sh\n"+
			"- T4 copy reads well — check: human-review\n"+
			"- T5 suite stays green - regression: make test\n"))
	require.Equal(t, []Task{
		{ID: "T1", What: "add wave", Check: "./wave.sh", Line: c.Tasks[0].Line},
		{ID: "T2", What: "greet still works", Check: "./greet.sh | grep -q Hello", Regression: true, Line: c.Tasks[1].Line},
		{ID: "T3", What: "fix the check: wording", Check: "./words.sh", Line: c.Tasks[2].Line},
		{ID: "T4", What: "copy reads well", Check: HumanReview, Line: c.Tasks[3].Line},
		{ID: "T5", What: "suite stays green", Check: "make test", Regression: true, Line: c.Tasks[4].Line},
	}, c.Tasks)
	require.Empty(t, c.parse)
	var cmds []string
	for _, task := range c.CommandTasks() {
		cmds = append(cmds, task.ID)
	}
	require.Equal(t, []string{"T1", "T2", "T3", "T5"}, cmds, "a regression task is a command task")
	require.Equal(t, []string{"T4"}, c.HumanTasks())
}

// A regression task names a command; `change check` reports one that
// does not, and a malformed line names both grammars.
func TestCheckChange_RegressionTaskLines(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave,
		"- T1 add wave — check: ./wave.sh\n- T2 looks right — regression: human-review\n- T3 nothing here — regression:\n- add more\n"))
	got := findingMessages(p.CheckChange("add-wave"))
	require.Contains(t, got, "T2's regression: names no command")
	require.Contains(t, got, "T3's regression: names no command")
	require.Contains(t, got, "or `- T<n> <what> — regression: <command>`")
	for _, f := range ClassifyFindings(p.CheckChange("add-wave")) {
		require.Equal(t, "change.bad-task-line", f.Code, f.Message)
	}
}

// Backlog 43 (D45, D47): start refuses a change that adds or modifies
// behaviour when a task check already passes before any of its code —
// unless the task is a regression task — and writes nothing.
func TestStart_VacuousCheck(t *testing.T) {
	t.Run("an ADDED change with a task green at baseline is refused", func(t *testing.T) {
		p := fixture(t, baseFiles())
		addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave,
			"- T1 add wave — check: ./wave.sh\n- T2 hello is printed — check: ./greet.sh | grep -q Hello\n"))
		commitAll(t, p, "plan")
		_, err := p.Start(ctx(), "add-wave", RunOptions{})
		var fe *FailError
		require.ErrorAs(t, err, &fe)
		require.Equal(t, CodeVacuousCheck, fe.Code)
		require.Equal(t, ActionReplan, ActionFor(fe.Code))
		require.Contains(t, fe.Msg, "T2 `./greet.sh | grep -q Hello`")
		require.NotContains(t, fe.Msg, "T1 ", "T1 is red at baseline")
		require.Contains(t, fe.Msg, "— regression: <command>")
		require.Equal(t, StatusDraft, state(t, p, "add-wave").Status, "nothing written")
		require.Equal(t, "main", CurrentBranch(ctx(), p.Root), "no branch left behind")
		require.False(t, p.branchExists(ctx(), ChangeBranch("add-wave")))
		clean(t, p, "refused start")
	})
	t.Run("marked regression, it starts", func(t *testing.T) {
		p := fixture(t, baseFiles())
		addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave,
			"- T1 add wave — check: ./wave.sh\n- T2 hello still printed — regression: ./greet.sh | grep -q Hello\n"))
		commitAll(t, p, "plan")
		_, err := p.Start(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
		require.Equal(t, StatusInProgress, state(t, p, "add-wave").Status)
	})
	t.Run("a MODIFIED change is judged too", func(t *testing.T) {
		p := fixture(t, baseFiles())
		addChange(t, p, "rework-hello", changeDoc("rework-hello", "greeting",
			"### MODIFIED [greeting] Requirement: Say hello\nThe system SHALL print Hello, kindly.\n"+
				"#### Scenario: plain run\n- check: ./greet.sh | grep -q Hello\n",
			"- T1 hello — check: ./greet.sh | grep -q Hello\n"))
		commitAll(t, p, "plan")
		_, err := p.Start(ctx(), "rework-hello", RunOptions{})
		var fe *FailError
		require.ErrorAs(t, err, &fe)
		require.Equal(t, CodeVacuousCheck, fe.Code)
	})
	for name, delta := range map[string]string{
		"Delta: none (a refactor) is not judged": "none — internal refactor\n",
		"a REMOVED-only change is not judged":    "### REMOVED [greeting] Requirement: Say name\nReason: names are gone\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := fixture(t, baseFiles())
			addChange(t, p, "tidy-up", changeDoc("tidy-up", "greeting", delta, "- T1 still greets — check: ./greet.sh | grep -q Hello\n"))
			commitAll(t, p, "plan")
			_, err := p.Start(ctx(), "tidy-up", RunOptions{})
			require.NoError(t, err)
		})
	}
}
