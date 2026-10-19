//go:build !windows

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Trial decision D38, through the CLI: start creates change/<id> from the
// base branch, verify and review record there, and archive — run from the
// base branch after the base moved — rebases, re-verifies and lands the
// change as ONE commit on the base branch, then deletes the branch. None
// of the change branch's status commits reaches the base.
func TestV1_ChangeBranchLifecycle(t *testing.T) {
	ape, project, _, env := reviewProject(t)
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = project
		c.Env = env
		out, err := c.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	require.Equal(t, "change/add-wave", git("branch", "--show-current"), "start checked out the change's branch")
	st := changeState(t, project)
	base, _ := st["base_branch"].(string)
	require.NotEmpty(t, base)
	require.Equal(t, git("rev-parse", base), st["base_sha"], "the base did not move when the change started")

	// Review on the change's branch (review.md committed there, as the
	// review skill does).
	require.NoError(t, writeFile(filepath.Join(project, "development", "changes", "add-wave", "review.md"), "no findings\n"))
	git("add", "development/changes/add-wave/review.md")
	git("commit", "-q", "-m", "review: add-wave")
	_, code := runApe(t, ape, project, env, "change", "review", "add-wave", "--verdict", "pass", "--findings", "development/changes/add-wave/review.md")
	require.Equal(t, 0, code)

	// Back on the base branch, someone else lands code; status still shows
	// the change's real status, read from its branch.
	git("switch", "-q", base)
	require.NoError(t, os.WriteFile(filepath.Join(project, "hug.sh"), []byte("#!/bin/sh\necho hug\n"), 0o755))
	git("add", "hug.sh")
	git("commit", "-q", "-m", "feat: hug")
	baseBefore := git("rev-parse", "HEAD")
	out, code := runApe(t, ape, project, env, "status", "--json")
	require.Equal(t, 0, code)
	var status struct {
		Branch  string `json:"branch"`
		Changes []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Branch string `json:"branch"`
		} `json:"changes"`
		Archived int `json:"archived"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &status), out)
	require.Equal(t, base, status.Branch)
	require.Len(t, status.Changes, 1)
	require.Equal(t, "reviewed", status.Changes[0].Status)
	require.Equal(t, "change/add-wave", status.Changes[0].Branch)

	out, code = runApe(t, ape, project, env, "change", "archive", "add-wave", "--json")
	require.Equal(t, 0, code, out)
	//nolint:tagliatelle // ape's snake_case --json contract
	var arch struct {
		Commit     string `json:"commit"`
		Subject    string `json:"subject"`
		BaseBranch string `json:"base_branch"`
		Rebased    bool   `json:"rebased"`
		Reverified bool   `json:"reverified"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &arch), out)
	require.True(t, arch.Rebased, "the base had moved")
	require.True(t, arch.Reverified)
	require.Equal(t, "feat: Add wave", arch.Subject)
	require.Equal(t, base, arch.BaseBranch)

	require.Equal(t, base, git("branch", "--show-current"))
	require.Equal(t, arch.Commit, git("rev-parse", "HEAD"))
	require.Equal(t, baseBefore, git("rev-parse", "HEAD~1"), "exactly one commit per archived change")
	require.Equal(t, "feat: Add wave\n\n- feat: wave\n- review: add-wave\n\nChange: add-wave", git("log", "-1", "--format=%B"),
		"the subject is {type}: {title} (D193); the body the branch's own commit subjects (not ape's), then the trailer (H15)")
	require.Empty(t, git("branch", "--list", "change/*"), "the change branch is deleted")
	for s := range strings.SplitSeq(git("log", "--format=%s"), "\n") {
		require.NotRegexp(t, `^apex: add-wave (in-progress|verified|reviewed|notes)`, s, "a change-branch status commit reached the base")
	}
	require.FileExists(t, filepath.Join(project, "wave.sh"))
	require.FileExists(t, filepath.Join(project, "hug.sh"))
	require.Empty(t, git("status", "--porcelain"))

	out, code = runApe(t, ape, project, env, "status", "--json")
	require.Equal(t, 0, code)
	require.NoError(t, json.Unmarshal([]byte(out), &status), out)
	require.Empty(t, status.Changes)
	require.Equal(t, 1, status.Archived)
}
