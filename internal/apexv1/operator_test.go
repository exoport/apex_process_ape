package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// rc6eConfig and rc6eAgents are what `ape project init` wrote at rc.6e
// (bin-rc6e fa8fdb0, captured from a scratch project): the loop's row under
// its old name, and AGENTS.md without the operator line.
const rc6eConfig = `project_name: "rc6e-init"
development_folder: development
# The model and effort each skill's session runs on (D65); change them freely.
models:
  apex-apply: {model: sonnet, effort: high}
  apex-orchestrator: {model: sonnet, effort: high}
  apex-review: {model: opus, effort: medium}
  apex-plan: {model: opus, effort: medium}
  apex-change: {model: opus, effort: medium}
  apex-accept: {model: opus, effort: medium}
  apex-release: {model: sonnet, effort: high}
`

const rc6eAgents = "# AGENTS.md\n\nProject context: read @development/project.md before planning or changing anything.\n\n" +
	"<!-- The project map, at most 16 KB (caps.agents_md): what this is, how to build, test and run the e2e\n" +
	"     checks, the NEVER rules, and pointers. Detail lives in development/ and docs/. -->\n\n## What this is\n\n## Commands\n" +
	"<!-- build / test / e2e, one line each -->\n\n## NEVER\n\n## Pointers\n- development/project.md — vision, users, scope, constraints\n"

// rc6eLiftedConfig is a project the rc.6e lift made from a v0 project with
// gate commands, whose person then chose another model for the loop: the
// row's value must survive byte for byte.
const rc6eLiftedConfig = `project_name: "greeter"
development_folder: development
regression: make test
# The model and effort each skill's session runs on (D65); change them freely.
models:
  apex-apply: {model: sonnet, effort: high}
  apex-orchestrator: {model: opus,   effort: xhigh}   # chosen after M1
  apex-review: {model: opus, effort: medium}
  apex-release: {model: sonnet, effort: high}
`

const rc6eLiftedAgents = "# AGENTS.md\n\nProject context: read @development/project.md before planning or changing anything.\n\n" +
	"## Commands\n- make build\n- make test\n\n## NEVER\n- never push\n"

// rc6eProject is a v1 project as rc.6e left it: config and AGENTS.md as
// given, ape's hooks merged, committed.
func rc6eProject(t *testing.T, config, agents string) *Project {
	t.Helper()
	p := fixture(t, map[string]string{
		"_apex/config.yaml": config, "AGENTS.md": agents, "CLAUDE.md": "@AGENTS.md\n",
		"development/project.md": projectMD, "development/roadmap.md": "# Roadmap\n",
	})
	_, err := MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	commitAll(t, p, "rc6e hooks")
	return p
}

// Q97: seq-07 on a project init made at rc.6e, and on one the rc.6e lift
// made (D187's pdfa is the first real one): one commit, the row renamed
// with its value byte for byte, the operator line added, hooks merged; a
// second run has nothing to do and commits nothing; --check says pending
// before and nothing after.
func TestOperatorInterface_Rc6eProjects(t *testing.T) {
	for name, c := range map[string]struct{ config, agents, row string }{
		"init":   {rc6eConfig, rc6eAgents, "  apex-loop: {model: sonnet, effort: high}\n"},
		"lifted": {rc6eLiftedConfig, rc6eLiftedAgents, "  apex-loop: {model: opus,   effort: xhigh}   # chosen after M1\n"},
	} {
		t.Run(name, func(t *testing.T) {
			p := rc6eProject(t, c.config, c.agents)
			// Before: the old row is the loop's, with a warning and a notice.
			cfg, err := LoadConfig(p.Root)
			require.NoError(t, err)
			_, legacy := LoopModel(cfg.Models)
			require.True(t, legacy)
			require.Equal(t, []string{CodeModelsOrchestratorRow}, noticeCodesOf(p.operatorRowNotices()))
			require.Contains(t, noticeCodesOf(p.Notices()), CodeModelsOrchestratorRow, "a project check notice")
			require.Contains(t, p.operatorRowNotices()[0].Message, "read as apex-loop's until")

			res, err := p.OperatorInterfaceCheck()
			requireCode(t, err, CodeOperatorInterfacePending)
			require.Equal(t, "moved", res.Models)
			require.True(t, res.AgentsLine)
			require.Empty(t, res.Hooks, "rc.6e merged every hook this ape has")

			n := len(subjects(t, p))
			res, err = p.OperatorInterface(ctx())
			require.NoError(t, err)
			require.NotEmpty(t, res.Commit)
			require.Equal(t, []string{OperatorInterfaceSubject}, subjects(t, p)[:len(subjects(t, p))-n])
			require.Equal(t, strings.Replace(c.config, "  apex-orchestrator: "+strings.TrimPrefix(c.row, "  apex-loop: "), c.row, 1), read(t, p, "_apex/config.yaml"),
				"only the key changes, byte for byte")
			agents := read(t, p, "AGENTS.md")
			require.Contains(t, agents, "anything.\n\n"+OperatorLine+"\n\n", "under the project-context line, set off by blank lines")
			require.Equal(t, c.agents, strings.Replace(agents, OperatorLine+"\n\n", "", 1), "nothing else changes")
			require.Empty(t, run(t, p.Root, "status", "--porcelain"))
			require.Empty(t, p.operatorRowNotices())
			cfg, err = LoadConfig(p.Root)
			require.NoError(t, err)
			_, legacy = LoopModel(cfg.Models)
			require.False(t, legacy)

			// Idempotent.
			res, err = p.OperatorInterface(ctx())
			require.NoError(t, err)
			require.Empty(t, res.Commit)
			require.False(t, res.Pending())
			require.Len(t, subjects(t, p), n+1)
			_, err = p.OperatorInterfaceCheck()
			require.NoError(t, err)
		})
	}
}

// A hook missing from settings.json is merged back with init's merge
// (UserPromptSubmit lands this way once the hook set has it), in the same
// commit; an apex-loop row beside the old one wins and the old row's lines
// go.
func TestOperatorInterface_HooksAndALeftoverRow(t *testing.T) {
	config := strings.Replace(rc6eConfig, "  apex-review:", "  apex-loop:\n    model: haiku\n    effort: low\n  apex-review:", 1)
	p := rc6eProject(t, config, rc6eAgents+"\nTalk to APEX with `/apex-orchestrator`.\n")
	settings := read(t, p, ".claude/settings.json")
	write(t, p, ".claude/settings.json", strings.Replace(settings, `"command": "ape hook session-start"`, `"command": "ape hook session-start-old"`, 1))
	commitAll(t, p, "a hook lost")

	require.Contains(t, p.operatorRowNotices()[0].Message, "is a leftover")
	res, err := p.OperatorInterfaceCheck()
	requireCode(t, err, CodeOperatorInterfacePending)
	require.Equal(t, "removed", res.Models)
	require.Len(t, res.Hooks, 1)
	require.False(t, res.AgentsLine, "a line naming /apex-orchestrator counts, however worded")

	res, err = p.OperatorInterface(ctx())
	require.NoError(t, err)
	require.Equal(t, []string{"SessionStart: ape hook session-start"}, res.Hooks)
	require.Equal(t, strings.Replace(config, "  apex-orchestrator: {model: sonnet, effort: high}\n", "", 1), read(t, p, "_apex/config.yaml"))
	require.Contains(t, run(t, p.Root, "show", "--name-only", "--format=%s", "HEAD"), ".claude/settings.json")
	require.Empty(t, p.CheckHooks())
}

// Refused, writing nothing, while a file it edits has uncommitted changes
// and on a change branch; a models: flow mapping it cannot edit by line is
// a person's.
func TestOperatorInterface_Refusals(t *testing.T) {
	p := rc6eProject(t, rc6eConfig, rc6eAgents)
	write(t, p, "AGENTS.md", rc6eAgents+"\nmine\n")
	_, err := p.OperatorInterface(ctx())
	requireCode(t, err, "tree.dirty")
	require.Equal(t, rc6eConfig, read(t, p, "_apex/config.yaml"))
	run(t, p.Root, "checkout", "-q", "--", "AGENTS.md")

	// Another file's uncommitted change is not its business.
	write(t, p, "notes.txt", "x\n")
	res, err := p.OperatorInterface(ctx())
	require.NoError(t, err)
	require.NotEmpty(t, res.Commit)
	require.Contains(t, run(t, p.Root, "status", "--porcelain"), "notes.txt")

	q := rc6eProject(t, "project_name: q\nmodels: {apex-loop: sonnet, apex-orchestrator: opus}\n", rc6eAgents)
	_, err = q.OperatorInterface(ctx())
	requireCode(t, err, CodeOperatorInterfacePending)
	require.Contains(t, err.Error(), "by hand")
	require.Equal(t, "project_name: q\nmodels: {apex-loop: sonnet, apex-orchestrator: opus}\n", read(t, q, "_apex/config.yaml"))
}

// The row's text edit (Q97): the key only, quoted or not, flow value or
// block; a removal takes the row's continuation lines and nothing else.
func TestMoveOperatorRow(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"models:\n  apex-orchestrator: {model: sonnet, effort: high}\n", "models:\n  apex-loop: {model: sonnet, effort: high}\n"},
		{"models:\n  \"apex-orchestrator\": opus # mine\n", "models:\n  \"apex-loop\": opus # mine\n"},
		{"models:\n  apex-orchestrator:\n    model: opus\n    effort: max\nx: 1\n", "models:\n  apex-loop:\n    model: opus\n    effort: max\nx: 1\n"},
		{"models: {apex-orchestrator: opus}\n", "models: {apex-loop: opus}\n"},
		{
			"models:\n  apex-loop: haiku\n  apex-orchestrator:\n    model: opus\n    effort: max\n  # next\n  apex-plan: opus\n",
			"models:\n  apex-loop: haiku\n  # next\n  apex-plan: opus\n",
		},
		{"models:\n  apex-orchestrator: opus\n  apex-loop: haiku\n", "models:\n  apex-loop: haiku\n"},
		{"models:\n  apex-plan: opus\n", "models:\n  apex-plan: opus\n"},
		{"project_name: x\n", "project_name: x\n"},
	} {
		got, err := moveOperatorRow([]byte(c.in))
		require.NoError(t, err, c.in)
		require.Equal(t, c.want, string(got), c.in)
	}
	_, err := moveOperatorRow([]byte("models: {apex-loop: haiku, apex-orchestrator: opus}\n"))
	require.Error(t, err)
}

// Q96: models.apex-loop is the loop's; models.apex-orchestrator only while
// there is none.
func TestLoopModel(t *testing.T) {
	m, legacy := LoopModel(map[string]SkillModel{"apex-orchestrator": {Model: "opus"}})
	require.Equal(t, "opus", m.Model)
	require.True(t, legacy)
	m, legacy = LoopModel(map[string]SkillModel{"apex-orchestrator": {Model: "opus"}, "apex-loop": {Model: "haiku"}})
	require.Equal(t, "haiku", m.Model)
	require.False(t, legacy)
	m, legacy = LoopModel(nil)
	require.Empty(t, m.Model)
	require.False(t, legacy)
}

// withOperatorLine without a project-context line: after the first H1, or
// at the top.
func TestWithOperatorLine(t *testing.T) {
	require.Equal(t, "# Map\n\n"+OperatorLine+"\n\n## A\n", withOperatorLine("# Map\n## A\n", "development"))
	require.Equal(t, OperatorLine+"\n\nplain\n", withOperatorLine("plain\n", "development"))
	require.Equal(t, OperatorLine+"\n", withOperatorLine("", "development"))
	require.True(t, hasOperatorLine("x\n"+OperatorLine+"\n"))
	require.False(t, hasOperatorLine("```\n/apex-orchestrator\n```\n"), "inside a fence it is not the line")
}

func noticeCodesOf(ds []Diagnostic) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return out
}
