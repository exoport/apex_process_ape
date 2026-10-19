package apexv1

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func preflightCodes(res *LiftResult) []string {
	out := make([]string, 0, len(res.Preflight))
	for _, p := range res.Preflight {
		out = append(out, p.Code)
	}
	return out
}

// L14: the dry run reports every pre-flight problem, not only the first.
// The research dry runs, with no remote and no tag, said only
// lift.no-tag.
func TestLift_DryRunReportsEveryPreflightProblem(t *testing.T) {
	t.Run("no tag, no upstream, dirty", func(t *testing.T) {
		root := liftFixture(t, false)
		run(t, root, "branch", "--unset-upstream")
		require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))
		res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
		require.NoError(t, err)
		require.Equal(t, []string{"lift.dirty", "lift.no-tag", "lift.tag-not-pushed"}, preflightCodes(res))
	})
	t.Run("tag not at HEAD and not pushed", func(t *testing.T) {
		root := liftFixture(t, false)
		run(t, root, "tag", "-a", LiftTag, "-m", "final")
		require.NoError(t, os.WriteFile(filepath.Join(root, "x.txt"), []byte("x"), 0o644))
		run(t, root, "add", "x.txt")
		run(t, root, "commit", "-q", "-m", "after the tag")
		res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
		require.NoError(t, err)
		require.Equal(t, []string{"lift.tag-not-head", "lift.tag-not-pushed"}, preflightCodes(res))
		// The real run refuses with the first.
		_, err = Lift(ctx(), root, LiftOptions{})
		require.Equal(t, "lift.tag-not-head", liftCode(err))
	})
}

// L2, from mdpdf and pdfa: .claude/ was git-ignored, with the v0 skills
// on disk untracked. The dry run passed; the real lift failed at `git
// add` on .claude/settings.json and left the tree half-staged; since L2
// it refused instead, and pdfa un-ignored settings.json and skills/ by
// hand. Now (D203) the lift rewrites the whole-folder ignore to the
// .claude/ shape in its own commit: the dry run plans it and writes
// nothing, the real lift commits settings.json, removes the manifest's
// untracked v0 skills, and lists the person's own skills it un-ignored.
func TestLift_IgnoredClaudeFolder(t *testing.T) {
	root := liftFixture(t, false)
	run(t, root, "rm", "-r", "-q", "--cached", ".claude")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("bin/\n.claude/\n*.log\n"), 0o644))
	run(t, root, "add", ".gitignore")
	run(t, root, "commit", "-q", "-m", "ignore .claude")
	run(t, root, "tag", "-a", LiftTag, "-m", "final")
	run(t, root, "push", "-q", "origin", LiftTag)

	res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
	require.NoError(t, err)
	require.Empty(t, res.Preflight, "judged against the rules as the lift's rewrite leaves them")
	require.Equal(t, ClaudeIgnoreRewritten, res.GitIgnore.Action)
	require.Contains(t, res.CommitMessage, "- rewrites the whole-folder .claude ignore to the .claude/ shape in .gitignore")
	require.Equal(t, "bin/\n.claude/\n*.log\n", read(t, &Project{Root: root}, ".gitignore"), "a dry run writes nothing")
	var untracked []string
	for _, d := range res.Deleted {
		if d.Untracked {
			untracked = append(untracked, d.Path)
		}
	}
	require.Equal(t, []string{".claude/skills/apex-dev-story", ".claude/skills/apex-orchestrator"}, untracked, "the manifest's, not apex-unlisted or my-own")

	res, err = Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	require.Empty(t, res.Preflight)
	require.Equal(t, "bin/\n"+claudeIgnoreMarker+"\n.claude/*\n!.claude/settings.json\n!.claude/skills/\nCLAUDE.local.md\n*.log\n",
		read(t, &Project{Root: root}, ".gitignore"), "rewritten in place, every other line kept")
	shown := run(t, root, "show", "--name-status", "--format=", "HEAD")
	require.Contains(t, shown, "A\t.claude/settings.json")
	require.Contains(t, shown, "M\t.gitignore", "the rewrite rides in the lift commit")
	require.NoDirExists(t, filepath.Join(root, ".claude", "skills", "apex-dev-story"))
	require.NoDirExists(t, filepath.Join(root, ".claude", "skills", "apex-orchestrator"))
	require.DirExists(t, filepath.Join(root, ".claude", "skills", "my-own"), "not the manifest's: kept")
	require.Equal(t, ClaudeIgnoreRewritten, res.GitIgnore.Action)
	require.Equal(t, []string{".claude/skills/apex-unlisted/", ".claude/skills/my-own/"}, res.GitIgnore.Revealed,
		"un-ignored, not the lift's to commit: listed")
	require.Equal(t, "?? .claude/skills/apex-unlisted/SKILL.md\n?? .claude/skills/my-own/SKILL.md", run(t, root, "status", "--porcelain", "-uall"))
}

// What `lift.claude-ignored` still refuses (D203): an ignore ape does not
// rewrite — core.excludesFile ignoring .claude/ (settings.json with it), a
// nested .gitignore ignoring skills/ — named with its rule.
func TestLift_IgnoredOutsideRootGitignore(t *testing.T) {
	for _, tc := range []struct{ name, want, rule string }{
		{"core.excludesFile", ".claude/settings.json (", ":1: .claude/"},
		{"nested .gitignore", ".claude/skills/ (the framework's skills) (.claude/.gitignore:1: skills/)", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := liftFixture(t, false)
			rule := ""
			if tc.rule != "" {
				excludes := filepath.Join(t.TempDir(), "global-ignore")
				require.NoError(t, os.WriteFile(excludes, []byte(".claude/\n"), 0o644))
				run(t, root, "config", "core.excludesFile", excludes)
				rule = excludes + tc.rule
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", ".gitignore"), []byte("skills/\n"), 0o644))
				run(t, root, "add", "-f", ".claude/.gitignore")
				run(t, root, "commit", "-q", "-m", "ignore skills")
			}
			run(t, root, "tag", "-a", LiftTag, "-m", "final")
			run(t, root, "push", "-q", "origin", LiftTag)
			head := run(t, root, "rev-parse", "HEAD")
			res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
			require.NoError(t, err)
			require.Equal(t, []string{CodeClaudeIgnored}, preflightCodes(res))
			require.Contains(t, res.Preflight[0].Message, tc.want)
			require.Contains(t, res.Preflight[0].Message, rule, "names the rule")
			_, err = Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
			require.Equal(t, CodeClaudeIgnored, liftCode(err))
			require.Equal(t, head, run(t, root, "rev-parse", "HEAD"))
			require.Empty(t, run(t, root, "status", "--porcelain"), "refused before anything was written")
		})
	}
}

// Inside `ape framework update` (Q113), the update puts the .claude/
// shape in .gitignore before the v1.0.0 lift runs: the .gitignore, the
// install's skills and a settings.json the shape un-ignored are all
// uncommitted when the lift starts. The lift takes settings.json (with the
// v1 hooks) and leaves the .gitignore and the skills to the update's
// commit.
func TestLift_FromUpdateAfterTheShape(t *testing.T) {
	root := liftFixture(t, false)
	run(t, root, "rm", "-r", "-q", "--cached", ".claude")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".claude/\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte("{\"model\": \"opus\"}\n"), 0o644))
	run(t, root, "add", ".gitignore")
	run(t, root, "commit", "-q", "-m", "ignore .claude")
	run(t, root, "tag", "-a", LiftTag, "-m", "final")
	run(t, root, "push", "-q", "origin", LiftTag)
	// The update: the shape, then the v1 install, which wipes every
	// apex-* skill and copies its own.
	gi, err := EnsureClaudeIgnoreShape(ctx(), root)
	require.NoError(t, err)
	require.Equal(t, ClaudeIgnoreRewritten, gi.Action)
	for _, s := range []string{"apex-dev-story", "apex-orchestrator", "apex-unlisted"} {
		require.NoError(t, os.RemoveAll(filepath.Join(root, ".claude", "skills", s)))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude", "skills", "apex-new"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "skills", "apex-new", "SKILL.md"), []byte("# v1\n"), 0o644))

	res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true, FromUpdate: true})
	require.NoError(t, err)
	require.Equal(t, ClaudeIgnoreAlready, res.GitIgnore.Action)
	shown := strings.Split(run(t, root, "show", "--name-status", "--format=", "HEAD"), "\n")
	require.Contains(t, shown, "A\t.claude/settings.json")
	require.NotContains(t, shown, "M\t.gitignore", "the update's commit carries it")
	require.Contains(t, read(t, &Project{Root: root}, ".claude/settings.json"), `"model": "opus"`, "the person's settings kept, hooks merged")
	require.Equal(t, "M .gitignore\n?? .claude/skills/apex-new/SKILL.md\n?? .claude/skills/my-own/SKILL.md", run(t, root, "status", "--porcelain", "-uall"))
}

// L1: the lift commit moves the project's ape pin to the running version
// when it can; when moving fails, .bingo/ is left as the tag had it and
// the note says the command.
func TestLift_MovesThePin(t *testing.T) {
	const pinned = "module _\n\ngo 1.27.0\n\nrequire github.com/exoport/apex_process_ape v0.9.0 // cmd/ape\n"
	setup := func(t *testing.T) string {
		t.Helper()
		root := liftFixture(t, false)
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".bingo"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".bingo", "ape.mod"), []byte(pinned), 0o644))
		run(t, root, "add", "-A")
		run(t, root, "commit", "-q", "-m", "pin")
		run(t, root, "tag", "-a", LiftTag, "-m", "final")
		run(t, root, "push", "-q", "origin", LiftTag)
		return root
	}
	t.Run("moved", func(t *testing.T) {
		root := setup(t)
		ape := &LiftApe{
			Version: "1.0.0", Path: "/bin/ape", PinFile: ".bingo/ape.mod", Pinned: "v0.9.0", PinCommand: "bingo get x@v1.0.0",
			MovePin: func(context.Context) error {
				return os.WriteFile(filepath.Join(root, ".bingo", "ape.mod"), []byte(strings.Replace(pinned, "v0.9.0", "v1.0.0", 1)), 0o644)
			},
		}
		res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true, Ape: ape})
		require.NoError(t, err)
		require.True(t, res.Ape.PinMoved)
		require.Contains(t, run(t, root, "show", "--name-status", "--format=", "HEAD"), "M\t.bingo/ape.mod")
		require.Contains(t, run(t, root, "log", "-1", "--format=%B"), "- moves the ape pin (.bingo/ape.mod) from v0.9.0 to v1.0.0\n")
		require.Empty(t, run(t, root, "status", "--porcelain"))
	})
	t.Run("could not move", func(t *testing.T) {
		root := setup(t)
		ape := &LiftApe{
			Version: "1.0.0", PinFile: ".bingo/ape.mod", Pinned: "v0.9.0", PinCommand: "bingo get x@v1.0.0",
			MovePin: func(context.Context) error {
				_ = os.WriteFile(filepath.Join(root, ".bingo", "ape.mod"), []byte("half"), 0o644)
				return errors.New("no such version")
			},
		}
		res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true, Ape: ape})
		require.NoError(t, err)
		require.False(t, res.Ape.PinMoved)
		require.Contains(t, res.Ape.PinNote, "could not move the pin (no such version): after the lift, run bingo get x@v1.0.0")
		require.Contains(t, res.Ape.PinNote, "so "+LiftPinLocal, "R33: commits and Claude sessions need it too")
		require.NotContains(t, run(t, root, "show", "--name-status", "--format=", "HEAD"), ".bingo")
		require.Empty(t, run(t, root, "status", "--porcelain"), ".bingo/ is back to the tag's")
	})
}
