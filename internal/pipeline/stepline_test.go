package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/stretchr/testify/require"
)

// longChange is the production shape: `ape task apex-correct-course
// --agent apex-agent-sm --args "<what changed and why>"`, newlines and all.
var longChange = "What changed: the payment provider moved to a new API.\n" +
	strings.Repeat("Why: the old one is retired and every checkout story depends on it. ", 14) +
	"\nStories affected: 3.2, 3.4, 4.1."

// TestStepLine_ShortLineIsUnchanged — under the budget nothing moves, so
// every run that fit before types exactly what it typed before.
func TestStepLine_ShortLineIsUnchanged(t *testing.T) {
	step := Step{Skill: "apex-sprint-sync", Args: "--from-status draft"}
	path := filepath.Join(t.TempDir(), "step.args.md")

	line, spilled, err := stepLine("", step, "", path)
	require.NoError(t, err)
	require.False(t, spilled)
	require.Equal(t, assembleInteractivePromptLine("", step, ""), line)
	require.NoFileExists(t, path)
}

// TestStepLine_LongArgsGoToTheFile is fix A on the agent path: the free
// text goes to the file verbatim, newlines kept, and the typed line keeps
// the persona prefix, the skill, ape's own --no-commit, and a pointer.
func TestStepLine_LongArgsGoToTheFile(t *testing.T) {
	step := Step{Skill: "apex-correct-course", Args: "--no-commit " + longChange}
	path := filepath.Join(t.TempDir(), "stages", "01-x", "step-01-apex-correct-course.args.md")
	require.False(t, repl.FitsTypedLine(assembleInteractivePromptLine("apex-agent-sm", step, "")),
		"the fixture must be over the budget to test anything")

	line, spilled, err := stepLine("apex-agent-sm", step, "", path)
	require.NoError(t, err)
	require.True(t, spilled)
	require.Equal(t,
		"/apex-agent-sm --autonomous -- apex-correct-course --autonomous --no-commit "+repl.ArgsPointer(path), line)
	require.True(t, repl.FitsTypedLine(line))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, longChange+"\n", string(got), "verbatim, newlines kept, ape's --no-commit not included")
}

// TestStepLine_NoAgentPath — the PAT-25 no-agent line carries its own
// --no-commit; only the step's text moves.
func TestStepLine_NoAgentPath(t *testing.T) {
	step := Step{Skill: "apex-correct-course", Args: longChange}
	path := filepath.Join(t.TempDir(), "a.args.md")

	line, spilled, err := stepLine("", step, "", path)
	require.NoError(t, err)
	require.True(t, spilled)
	require.Equal(t, "/apex-correct-course --autonomous --no-commit "+repl.ArgsPointer(path), line)
}

// TestStepLine_TaskStepKeepsItsCommitConvention is Q45: `ape task` sets
// ExplicitNoCommit, so its line carries --no-commit only when the caller
// asked for it — whether the text is typed or goes to the file. The file
// branch once rebuilt the step from Skill and Args alone and lost the
// flag, so every args-file task was told not to commit.
func TestStepLine_TaskStepKeepsItsCommitConvention(t *testing.T) {
	for name, args := range map[string]string{
		"long":       longChange,
		"whitespace": "Hello, Ada!  (3)",
		"line break": "a brief\nwith two lines",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "a.args.md")
			line, spilled, err := stepLine("", Step{Skill: "apex-plan", Args: args, ExplicitNoCommit: true}, "", path)
			require.NoError(t, err)
			require.True(t, spilled)
			require.Equal(t, "/apex-plan --autonomous "+repl.ArgsPointer(path), line, "no --no-commit the caller did not ask for")

			line, spilled, err = stepLine("", Step{Skill: "apex-plan", Args: "--no-commit " + args, ExplicitNoCommit: true}, "", path)
			require.NoError(t, err)
			require.True(t, spilled)
			require.Equal(t, "/apex-plan --autonomous --no-commit "+repl.ArgsPointer(path), line, "the caller's own --no-commit is kept")
		})
	}
}

// TestStepLine_PromptFlagMovesToo — a pipeline's --prompt reaches the
// skill as "<flag> <value>", and it is the part most likely to be long.
func TestStepLine_PromptFlagMovesToo(t *testing.T) {
	step := Step{Skill: "apex-create-epics-and-stories", Args: "--from-status draft", PromptFlag: "--prompt"}
	path := filepath.Join(t.TempDir(), "p.args.md")

	line, spilled, err := stepLine("", step, longChange, path)
	require.NoError(t, err)
	require.True(t, spilled)
	require.NotContains(t, line, "--from-status")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "--from-status draft --prompt "+longChange+"\n", string(got))
}

// TestStepLine_NoRunDirTypesTheLongLine — without a run directory there is
// nowhere to put the file, and the line goes out as it did before.
func TestStepLine_NoRunDirTypesTheLongLine(t *testing.T) {
	step := Step{Skill: "apex-correct-course", Args: longChange}
	line, spilled, err := stepLine("", step, "", "")
	require.NoError(t, err)
	require.False(t, spilled)
	require.Equal(t, assembleInteractivePromptLine("", step, ""), line)
}

func TestSplitLeadingNoCommit(t *testing.T) {
	cases := []struct{ in, keep, rest string }{
		{"--no-commit --from-status draft", "--no-commit", "--from-status draft"},
		{"--no-commit", "--no-commit", ""},
		{"  --no-commit\nline one\nline two", "--no-commit", "line one\nline two"},
		{"--no-commitment is a word", "", "--no-commitment is a word"},
		{"text --no-commit later", "", "text --no-commit later"},
		{"", "", ""},
	}
	for _, tc := range cases {
		keep, rest := splitLeadingNoCommit(tc.in)
		require.Equal(t, tc.keep, keep, tc.in)
		require.Equal(t, tc.rest, rest, tc.in)
	}
}

// TestStepArgsPath_SharesTheStepLogStem — the args file sits beside the
// step's event log under the same name, so one cannot move without the
// other.
func TestStepArgsPath_SharesTheStepLogStem(t *testing.T) {
	mw, err := newManifestWriter(t.TempDir(), "task-x", "/tmp/p", "/nonexistent.yaml", "test", time.Now())
	require.NoError(t, err)
	w, logRel, err := mw.OpenStepLog(1, 2, "correct course", "apex-correct-course")
	require.NoError(t, err)
	require.NoError(t, w.Close())

	abs, rel := mw.StepArgsPath(1, 2, "correct course", "apex-correct-course")
	require.Equal(t, strings.TrimSuffix(logRel, ".ndjson")+".args.md", rel)
	require.Equal(t, filepath.Join(mw.runDir, filepath.FromSlash(rel)), abs)
}

// TestTermination_NotSubmitted — a line claude never submitted is recorded
// as such, through the stage's error wrapping, not as a generic error.
func TestTermination_NotSubmitted(t *testing.T) {
	nse := &repl.NotSubmittedError{Enters: 3, Waited: 61 * time.Second, Shown: true, Input: "❯ /apex-x"}
	rec := newTerminationRecord(fmt.Errorf("stage %q step %d: send prompt: %w", "s", 0, nse))
	require.Equal(t, TerminationNotSubmitted, rec.Kind)
	require.InDelta(t, 61, rec.ElapsedSecs, 0.001)
	require.Contains(t, rec.Message, "never submitted")
}

// TestStepLine_WhitespaceGoesToTheFile — short text that would not arrive
// as written when typed (a run of spaces, a tab, a line break, another
// control character) takes the long-line route: the file holds it byte
// for byte, and the typed line keeps ape's own part and a pointer.
func TestStepLine_WhitespaceGoesToTheFile(t *testing.T) {
	cases := []struct{ label, text string }{
		{"double space", "keep  these  gaps"},
		{"tab", "col one\tcol two"},
		{"newline", "line one\nline two"},
		{"carriage return", "line one\r\nline two"},
		{"escape", "a\x1b[Bb"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "stages", "01-x", "step-01-apex-x.args.md")
			step := Step{Skill: "apex-x", Args: "--no-commit " + tc.text}
			require.True(t, repl.FitsTypedLine(assembleInteractivePromptLine("apex-agent-sm", step, "")),
				"the fixture must be within the budget, or it tests the length rule")

			line, spilled, err := stepLine("apex-agent-sm", step, "", path)
			require.NoError(t, err)
			require.True(t, spilled)
			require.Equal(t, "/apex-agent-sm --autonomous -- apex-x --autonomous --no-commit "+repl.ArgsPointer(path), line)
			require.True(t, repl.TypesVerbatim(line))

			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, []byte(tc.text+"\n"), got, "byte-exact, ape's --no-commit not included")
		})
	}
}

// TestStepLine_PromptWhitespaceGoesToTheFile — the prompt is the caller's
// text too, and is judged whole: Args that type fine move with it, so the
// skill reads one file in the order it would have read the line.
func TestStepLine_PromptWhitespaceGoesToTheFile(t *testing.T) {
	step := Step{Skill: "apex-x", Args: "--from-status draft", PromptFlag: "--prompt"}
	path := filepath.Join(t.TempDir(), "p.args.md")
	prompt := "  indented\tand  spaced\n"

	line, spilled, err := stepLine("", step, prompt, path)
	require.NoError(t, err)
	require.True(t, spilled)
	require.Equal(t, "/apex-x --autonomous --no-commit "+repl.ArgsPointer(path), line)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "--from-status draft --prompt "+prompt+"\n", string(got))
}

// TestStepLine_TypeableTextIsUnchanged — single-spaced, one-line text is
// typed exactly as before, up to the budget; edge whitespace around Args
// is not the caller's text (the typed line drops it), and a prompt with no
// prompt flag is never typed, so neither moves anything.
func TestStepLine_TypeableTextIsUnchanged(t *testing.T) {
	prefix := assembleInteractivePromptLine("", Step{Skill: "apex-x"}, "") + " "
	n := repl.TypedLineBudget - len(prefix)
	atBudget := strings.Repeat("a ", n)[:n-1] + "b" // "a a … ab" or "a a … a b"
	cases := []struct {
		label  string
		step   Step
		prompt string
	}{
		{"at the budget", Step{Skill: "apex-x", Args: atBudget}, ""},
		{"edge whitespace", Step{Skill: "apex-x", Args: "\n  --from-status draft\n"}, ""},
		{"unicode", Step{Skill: "apex-x", Args: "café — naïve ✓"}, ""},
		{"prompt with no flag", Step{Skill: "apex-x", Args: "M3"}, "never  typed\n"},
		{"single-spaced prompt", Step{Skill: "apex-x", PromptFlag: "--prompt"}, "fix the parser"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "a.args.md")
			line, spilled, err := stepLine("", tc.step, tc.prompt, path)
			require.NoError(t, err)
			require.False(t, spilled)
			require.Equal(t, assembleInteractivePromptLine("", tc.step, tc.prompt), line)
			require.NoFileExists(t, path)
		})
	}
	require.Len(t, []rune(prefix+atBudget), repl.TypedLineBudget, "the at-budget fixture must be exactly at the budget")
}
