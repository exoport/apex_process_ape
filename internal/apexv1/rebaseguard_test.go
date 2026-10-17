package apexv1

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// D40: the model never rebases. On a change branch, git rebase and git
// merge are denied; getting out of one, merge-base and the like, and any
// other branch are allowed.
func TestRebaseGuard(t *testing.T) {
	p := fixture(t, baseFiles())
	repo := p.Root
	run(t, repo, "switch", "-q", "-c", "change/add-wave")
	sub := filepath.Join(repo, "development")
	for cmd, deny := range map[string]bool{
		"git rebase main":                       true,
		"git rebase -i HEAD~2":                  true,
		"git merge main":                        true,
		"git merge --no-ff main && echo done":   true,
		"cd x; git -C " + repo + " rebase main": true,
		"sh -c 'git rebase main'":               true,
		"git -C " + sub + " merge main":         true,
		"git rebase --abort":                    false,
		"git merge --quit":                      false,
		"git cherry-pick abc123":                true,
		"git cherry-pick --no-commit main":      true,
		"git cherry-pick --abort":               false,
		"git merge-base HEAD main":              false,
		"git merge-tree main HEAD":              false,
		"echo \"git rebase main\"":              false,
		"git log --merges":                      false,
		"git status":                            false,
	} {
		got := RebaseGuard(cmd, repo)
		require.Equal(t, deny, got.Deny, cmd)
		if deny {
			require.Contains(t, got.Reason, "`ape change start add-wave`", cmd)
		}
	}
	run(t, repo, "switch", "-q", "main")
	require.False(t, RebaseGuard("git rebase change/add-wave", repo).Deny, "rebase off a change branch is not the guard's")

	// Through the hook: the Bash payload is judged by both guards.
	run(t, repo, "switch", "-q", "change/add-wave")
	d := PreTool(HookPayload{ToolName: "Bash", Cwd: repo, ToolInput: []byte(`{"command":"git rebase main"}`)})
	require.True(t, d.Deny)
}
