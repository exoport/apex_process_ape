package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// shapeBlock is the block ape appends to a .gitignore with no .claude rule.
const shapeBlock = claudeIgnoreMarker + "\n.claude/*\n!.claude/settings.json\n!.claude/skills/\nCLAUDE.local.md\n"

// D203: the .gitignore text edit. A whole-folder ignore is rewritten in
// place (any spelling); the shape's missing lines are appended; every
// other line is kept byte for byte; a second pass changes nothing.
func TestShapeClaudeIgnore(t *testing.T) {
	for _, c := range []struct{ name, in, want, action string }{
		{"none", "", shapeBlock, ClaudeIgnoreAdded},
		{"no .claude rule", "bin/\n*.log\n", "bin/\n*.log\n\n" + shapeBlock, ClaudeIgnoreAdded},
		{"unterminated", "bin/", "bin/\n\n" + shapeBlock, ClaudeIgnoreAdded},
		{".claude/", "bin/\n.claude/\n*.log\n", "bin/\n" + shapeBlock + "*.log\n", ClaudeIgnoreRewritten},
		{".claude", ".claude\n", shapeBlock, ClaudeIgnoreRewritten},
		{"/.claude", "/.claude\n", shapeBlock, ClaudeIgnoreRewritten},
		{"/.claude/", "/.claude/  \n", shapeBlock, ClaudeIgnoreRewritten},
		{".claude/**", "x\n.claude/**", "x\n" + strings.TrimSuffix(shapeBlock, "\n"), ClaudeIgnoreRewritten},
		{"/.claude/**", "/.claude/**\n", shapeBlock, ClaudeIgnoreRewritten},
		{"two folder ignores", ".claude/\nbin/\n/.claude\n", shapeBlock + "bin/\n", ClaudeIgnoreRewritten},
		{"already", "bin/\n\n" + shapeBlock, "bin/\n\n" + shapeBlock, ClaudeIgnoreAlready},
		{
			"own .claude/agents/ kept", ".claude/\n.claude/agents/\n",
			shapeBlock + ".claude/agents/\n", ClaudeIgnoreRewritten,
		},
		{
			// What the rc.6e lift told people to write (pdfa added skills/ by hand).
			"rc.6e's advice", ".claude/*\n!.claude/settings.json\n",
			".claude/*\n!.claude/settings.json\n\n" + claudeIgnoreMarker + "\n!.claude/skills/\nCLAUDE.local.md\n", ClaudeIgnoreAdded,
		},
		{
			"a negation before .claude/* does not count", "!.claude/skills/\n.claude/*\n!.claude/settings.json\nCLAUDE.local.md\n",
			"!.claude/skills/\n.claude/*\n!.claude/settings.json\nCLAUDE.local.md\n\n" + claudeIgnoreMarker + "\n!.claude/skills/\n", ClaudeIgnoreAdded,
		},
		{"CLAUDE.local.md there", "/CLAUDE.local.md\n.claude/\n", "/CLAUDE.local.md\n" + strings.Replace(shapeBlock, "CLAUDE.local.md\n", "", 1), ClaudeIgnoreRewritten},
		{"CRLF", "bin/\r\n.claude/\r\n", "bin/\r\n" + strings.ReplaceAll(shapeBlock, "\n", "\r\n"), ClaudeIgnoreRewritten},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, action := shapeClaudeIgnore(c.in)
			require.Equal(t, c.want, got)
			require.Equal(t, c.action, action)
			again, action := shapeClaudeIgnore(got)
			require.Equal(t, got, again, "idempotent")
			require.Equal(t, ClaudeIgnoreAlready, action)
		})
	}
}

// What git makes of the shape: settings.json and skills/ shared, the rest
// of .claude/ (settings.local.json, a project's own agents/) and
// CLAUDE.local.md ignored; the file is created when absent and a second
// call writes nothing.
func TestEnsureClaudeIgnoreShape(t *testing.T) {
	needSh(t)
	for name, gitignore := range map[string]string{"no .gitignore": "", ".claude/": ".claude/\n.claude/agents/\n", ".claude": ".claude\n", "/.claude/**": "/.claude/**\n"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			gitEnv(t)
			run(t, root, "init", "-q")
			if gitignore != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(gitignore), 0o644))
			}
			for _, f := range []string{".claude/settings.json", ".claude/settings.local.json", ".claude/skills/mine/SKILL.md", ".claude/agents/a.md", "CLAUDE.local.md"} {
				require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(f)), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(root, f), []byte("x\n"), 0o644))
			}
			res, err := EnsureClaudeIgnoreShape(ctx(), root)
			require.NoError(t, err)
			if gitignore == "" {
				require.Equal(t, ClaudeIgnoreAdded, res.Action)
				require.True(t, res.Created)
				require.Empty(t, res.RevealedFiles, "nothing was ignored before")
			} else {
				require.Equal(t, ClaudeIgnoreRewritten, res.Action)
				require.Equal(t, []string{".claude/settings.json", ".claude/skills/mine/SKILL.md"}, res.RevealedFiles)
				res.SetRevealed(func(rel string) bool { return rel == settingsJSONRel })
				require.Equal(t, []string{".claude/skills/mine/"}, res.Revealed)
			}
			require.ElementsMatch(t, []string{".claude/settings.local.json", ".claude/agents/a.md", "CLAUDE.local.md"},
				gitIgnored(ctx(), root, true, ".claude/settings.json", claudeSkillsProbe, ".claude/settings.local.json", ".claude/agents/a.md", "CLAUDE.local.md"))
			data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
			require.NoError(t, err)
			res, err = EnsureClaudeIgnoreShape(ctx(), root)
			require.NoError(t, err)
			require.Equal(t, ClaudeIgnoreAlready, res.Action)
			again, err := os.ReadFile(filepath.Join(root, ".gitignore"))
			require.NoError(t, err)
			require.Equal(t, string(data), string(again))
		})
	}
}

// init applies the shape in its own commit: a committed .claude/ ignore
// rewritten, the settings.json it un-ignored committed with the hooks, the
// person's own skill listed; a .gitignore with uncommitted changes is
// shaped but left out of the commit.
func TestInit_ClaudeIgnoreShape(t *testing.T) {
	needSh(t)
	t.Run("committed .claude/ ignore", func(t *testing.T) {
		root := t.TempDir()
		gitEnv(t)
		run(t, root, "init", "-q")
		require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("bin/\n.claude/\n"), 0o644))
		run(t, root, "add", ".gitignore")
		run(t, root, "commit", "-q", "-m", "ignore .claude")
		for _, f := range []string{".claude/settings.json", ".claude/skills/mine/SKILL.md"} {
			require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(f)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, f), []byte("{}\n"), 0o644))
		}
		res, err := Init(ctx(), root, true)
		require.NoError(t, err)
		require.Equal(t, ClaudeIgnoreRewritten, res.GitIgnore.Action)
		require.False(t, res.GitIgnore.Uncommitted)
		require.Equal(t, []string{".claude/skills/mine/"}, res.GitIgnore.Revealed)
		require.Equal(t, "bin/\n"+shapeBlock, read(t, &Project{Root: root}, ".gitignore"))
		committed := strings.Split(run(t, root, "show", "--name-only", "--format=", "HEAD"), "\n")
		require.Contains(t, committed, ".gitignore")
		require.Contains(t, committed, ".claude/settings.json")
		require.Equal(t, "?? .claude/skills/mine/SKILL.md", run(t, root, "status", "--porcelain", "-uall"))
		res, err = Init(ctx(), root, true)
		require.NoError(t, err)
		require.Equal(t, ClaudeIgnoreAlready, res.GitIgnore.Action)
	})
	t.Run("uncommitted .gitignore", func(t *testing.T) {
		root := t.TempDir()
		gitEnv(t)
		run(t, root, "init", "-q")
		require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("bin/\n"), 0o644))
		res, err := Init(ctx(), root, false)
		require.NoError(t, err)
		require.Equal(t, ClaudeIgnoreAdded, res.GitIgnore.Action)
		require.True(t, res.GitIgnore.Uncommitted)
		require.NotContains(t, strings.Split(run(t, root, "show", "--name-only", "--format=", "HEAD"), "\n"), ".gitignore")
		require.Equal(t, "bin/\n\n"+shapeBlock, read(t, &Project{Root: root}, ".gitignore"))
	})
}

// Q114: seq-07 applies the shape to a v1 project made before rc.7b — here
// one rc.7 made whose person ignored .claude/ — in its one commit, and
// --check reports it pending until then.
func TestOperatorInterface_ClaudeIgnore(t *testing.T) {
	p := rc6eProject(t, strings.Replace(rc6eConfig, "apex-orchestrator:", "apex-loop:", 1), rc6eLiftedAgents+"\n"+OperatorLine+"\n")
	write(t, p, ".gitignore", ".claude/\n")
	commitAll(t, p, "ignore .claude")
	require.Contains(t, findingMessages(p.Check()), ".claude/skills/ is ignored by git, so the framework's skills ape framework setup|update install there reach no other checkout"+
		" — `ape project init` puts the .claude/ shape in the root .gitignore (.claude/*, !.claude/settings.json, !.claude/skills/); "+
		"an ignore it does not rewrite (a nested .gitignore, .git/info/exclude, core.excludesFile; `git check-ignore -v <path>` names it) is removed by hand; then commit it")

	res, err := p.OperatorInterfaceCheck()
	requireCode(t, err, CodeOperatorInterfacePending)
	require.Equal(t, ClaudeIgnoreRewritten, res.GitIgnore)
	require.Contains(t, err.Error(), ".gitignore ignores the whole .claude folder")
	require.Empty(t, res.Models)
	require.False(t, res.AgentsLine)

	res, err = p.OperatorInterface(ctx())
	require.NoError(t, err)
	require.NotEmpty(t, res.Commit)
	require.Equal(t, ".gitignore", run(t, p.Root, "show", "--name-only", "--format=", "HEAD"))
	require.Equal(t, shapeBlock, read(t, p, ".gitignore"))
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	require.NotContains(t, findingMessages(p.Check()), "is ignored by git")
	_, err = p.OperatorInterfaceCheck()
	require.NoError(t, err)
}
