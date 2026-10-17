package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// v0Repo is a minimal APEX v0 project: config, framework record, one ADR,
// one Go file. No tag.
func v0Repo(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the lift's fixtures are POSIX")
	}
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("APEX_GOVERNANCE_REPO", "")
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@t"}, {"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@t"}} {
		t.Setenv(kv[0], kv[1])
	}
	for rel, content := range map[string]string{
		"_apex/config.yaml":                               "project_name: g\noutput_folder: _output\ndevelopment_folder: development\nimplementation_folder: development/implementation\ngovernance_folder: development/governance\n",
		"_apex/framework.yaml":                            "framework:\n  version_tag: v0.29.3\n  git_hash: abc\nsources:\n  skills:\n    paths: [.claude/skills/apex-dev-story]\n",
		".claude/skills/apex-dev-story/SKILL.md":          "x\n",
		"development/governance/adrs/adr-0003_use-go.md":  "---\nid: ADR-0003\nstatus: accepted\ntitle: Use Go\n---\n## Context\nc\n## Decision\nGo.\n## Consequences\nq\n",
		"development/implementation/3-2_retry-publish.md": "story\n",
		"main.go": "package main\n\n// Story 3.2: retry (ADR-0003).\nfunc main() {}\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	v1Git(t, root, "init", "-q", "-b", "main")
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "v0")
	return root
}

func TestProjectLift_CLI(t *testing.T) {
	root := v0Repo(t)

	// The dry run plans everything and reports the missing tag.
	stdout, _, err := v1Exec(t, newProjectCmd, "lift", "--dry-run", "--json", "--cwd", root)
	require.NoError(t, err)
	var plan apexv1.LiftResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &plan), stdout)
	require.True(t, plan.DryRun)
	require.Equal(t, "v0.29.3", plan.Version)
	require.Equal(t, "lift.no-tag", plan.Preflight[0].Code)
	require.Equal(t, "ADR-0003", plan.ADRs[0].Old)
	require.Equal(t, "ADR-0001", plan.ADRs[0].New)
	require.Equal(t, "main.go", plan.Changed[0].File)
	require.FileExists(t, filepath.Join(root, "development/implementation/3-2_retry-publish.md"), "nothing written")

	// The real run refuses, with the code and its action.
	stdout, _, err = v1Exec(t, newProjectCmd, "lift", "--json", "--cwd", root)
	require.Error(t, err)
	require.Equal(t, 1, exitCodeOf(t, err))
	var env struct {
		Diagnostics []apexv1.Diagnostic `json:"diagnostics"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &env), stdout)
	require.Equal(t, "lift.no-tag", env.Diagnostics[0].Code)
	require.Equal(t, apexv1.ActionPerson, env.Diagnostics[0].Action)

	// Tagged and pushed, it lifts; human output names the commit.
	remote := filepath.Join(t.TempDir(), "r.git")
	v1Git(t, root, "init", "-q", "--bare", remote)
	v1Git(t, root, "remote", "add", "origin", remote)
	v1Git(t, root, "push", "-q", "-u", "origin", "main")
	v1Git(t, root, "tag", "-a", apexv1.LiftTag, "-m", "final")
	v1Git(t, root, "push", "-q", "origin", apexv1.LiftTag)
	stdout, _, err = v1Exec(t, newProjectCmd, "lift", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "adr       ADR-0003 -> ADR-0001  development/decisions/0001-use-go.md  [accepted]")
	require.Contains(t, stdout, "apex: lift from APEX v0 v0.29.3")
	require.Contains(t, stdout, "no ## Code Validation found")
	main, err := os.ReadFile(filepath.Join(root, "main.go"))
	require.NoError(t, err)
	require.Equal(t, "package main\n\n// Retry (ADR-0001).\nfunc main() {}\n", string(main))
}
