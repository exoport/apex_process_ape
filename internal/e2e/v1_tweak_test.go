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

const v1Tweak = `---
id: fix-hug
milestone: none
type: fix
touches: [greeting]
---
# Hug fix
## Why
A person asked for hugs, now.
## Out of scope
- farewells
## Delta
### ADDED [greeting] Requirement: Hug
The system SHALL hug.
#### Scenario: hug
- check: ./hug.sh
## Tasks
- T1 hug — check: ./hug.sh
`

// Trial decision D40, through the CLI: a milestone change is in progress
// on its branch; a tweak (type fix) is created and approved on main,
// started, verified, reviewed and archived onto main while the milestone
// change is paused; then the milestone change is resumed with `change
// start`, which rebases it onto the tweak and re-baselines it at main's
// tip; it verifies green and archives. main gets exactly two commits: the
// tweak, then the change.
func TestV1_TweakLaneAndResumeAfterTheBaseMoved(t *testing.T) {
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
	apeOK := func(args ...string) string {
		t.Helper()
		out, code := runApe(t, ape, project, env, args...)
		require.Equal(t, 0, code, "ape %v: %s", args, out)
		return out
	}
	base, _ := changeState(t, project)["base_branch"].(string)
	require.NotEmpty(t, base)

	// The milestone change is paused; a person's request becomes a tweak.
	git("switch", "-q", base)
	apeOK("change", "new", "fix-hug", "--milestone", "none", "--touches", "greeting")
	require.NoError(t, writeFile(filepath.Join(project, "development", "changes", "fix-hug", "change.md"), v1Tweak))
	git("add", "-A")
	git("commit", "-q", "-m", "plan: fix-hug")
	_, code := runApe(t, ape, project, env, "change", "start", "fix-hug")
	require.Equal(t, 1, code, "a tweak starts only once a person approved it")
	_, code = runApe(t, ape, project, env, "change", "approve", "add-wave", "--by", "ann")
	require.Equal(t, 1, code, "a milestone change is approved through its milestone")
	apeOK("change", "approve", "fix-hug", "--by", "ann")
	require.Equal(t, "apex: fix-hug approved", git("log", "-1", "--format=%s"))

	var status struct {
		Tweaks  []string `json:"tweaks"`
		Next    string   `json:"next"`
		Changes []struct {
			ID       string          `json:"id"`
			Lane     string          `json:"lane"`
			Status   string          `json:"status"`
			Approved json.RawMessage `json:"approved"`
		} `json:"changes"`
	}
	require.NoError(t, json.Unmarshal([]byte(apeOK("status", "--json")), &status))
	require.Equal(t, []string{"fix-hug"}, status.Tweaks)
	require.Equal(t, "fix-hug", status.Changes[0].ID, "approved tweaks are listed first")
	require.Equal(t, "tweak", status.Changes[0].Lane)
	require.Equal(t, "add-wave", status.Changes[1].ID)
	require.Equal(t, "milestone", status.Changes[1].Lane)
	require.Equal(t, "verified", status.Changes[1].Status)
	require.Contains(t, status.Next, "`ape task apex-loop --args fix-hug`")
	mainBefore := git("rev-parse", base)

	// The tweak runs alone: its branch, checks, review, archive.
	apeOK("change", "start", "fix-hug")
	require.Equal(t, "change/fix-hug", git("branch", "--show-current"))
	require.NoError(t, os.WriteFile(filepath.Join(project, "hug.sh"), []byte("#!/bin/sh\necho hug\n"), 0o755))
	git("add", "hug.sh")
	git("commit", "-q", "-m", "fix: hug")
	apeOK("change", "verify", "fix-hug")
	require.NoError(t, writeFile(filepath.Join(project, "development", "changes", "fix-hug", "review.md"), "no findings\n"))
	git("add", "-A")
	git("commit", "-q", "-m", "review: fix-hug")
	apeOK("change", "review", "fix-hug", "--verdict", "pass", "--findings", "development/changes/fix-hug/review.md")
	apeOK("change", "archive", "fix-hug")
	require.Equal(t, base, git("branch", "--show-current"))
	tweakCommit := git("rev-parse", "HEAD")
	require.Equal(t, mainBefore, git("rev-parse", "HEAD~1"))

	// The milestone change resumes: start catches it up with main.
	out := apeOK("change", "start", "add-wave", "--json")
	//nolint:tagliatelle // ape's snake_case --json contract
	var start struct {
		Status  string `json:"status"`
		Resumed bool   `json:"resumed"`
		CatchUp *struct {
			From     string `json:"from"`
			To       string `json:"to"`
			Rebased  bool   `json:"rebased"`
			Baseline []struct {
				Task string `json:"task"`
				Exit int    `json:"exit"`
			} `json:"baseline"`
			VerifyStale bool `json:"verify_stale"`
		} `json:"catch_up"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &start), out)
	require.True(t, start.Resumed)
	require.NotNil(t, start.CatchUp, out)
	require.True(t, start.CatchUp.Rebased)
	require.Equal(t, tweakCommit, start.CatchUp.To)
	require.True(t, start.CatchUp.VerifyStale)
	require.Equal(t, "in-progress", start.Status, "a verified change caught up goes back to in-progress")
	for _, b := range start.CatchUp.Baseline {
		if b.Task == "T1" {
			require.NotEqual(t, 0, b.Exit, "add-wave's own test is red at main's tip: the baseline is the base's, not the rebased HEAD's")
		}
	}
	require.Equal(t, "change/add-wave", git("branch", "--show-current"))
	git("merge-base", "--is-ancestor", tweakCommit, "HEAD")
	st := changeState(t, project)
	require.Equal(t, tweakCommit, st["base_sha"])

	apeOK("change", "verify", "add-wave")
	require.NoError(t, writeFile(filepath.Join(project, "development", "changes", "add-wave", "review.md"), "no findings\n"))
	git("add", "-A")
	git("commit", "-q", "-m", "review: add-wave")
	apeOK("change", "review", "add-wave", "--verdict", "pass", "--findings", "development/changes/add-wave/review.md")
	apeOK("change", "archive", "add-wave")

	require.Equal(t, base, git("branch", "--show-current"))
	require.Equal(t, "feat: Add wave\nfix: Hug fix", git("log", "--format=%s", mainBefore+".."+base), "exactly two commits: the tweak, then the change")
	require.Empty(t, git("branch", "--list", "change/*"))
	require.FileExists(t, filepath.Join(project, "hug.sh"))
	require.FileExists(t, filepath.Join(project, "wave.sh"))
	spec, err := os.ReadFile(filepath.Join(project, "development", "specs", "greeting", "spec.md"))
	require.NoError(t, err)
	require.Contains(t, string(spec), "### Requirement: Hug")
	require.Contains(t, string(spec), "### Requirement: Wave")
	require.Empty(t, git("status", "--porcelain"))
	require.Equal(t, 1, strings.Count(git("worktree", "list", "--porcelain"), "worktree "), "no baseline worktree is left behind")
}
