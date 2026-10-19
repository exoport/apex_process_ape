package apecmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	require.Equal(t, "ADR-0002", plan.ADRs[0].New, "ADR-0001 is APEX's standard (D205 Q119)")
	require.Equal(t, apexv1.StandardCreated, plan.StandardADR.Action)
	require.Equal(t, "main.go", plan.Changed[0].File)
	require.FileExists(t, filepath.Join(root, "development", "implementation", "3-2_retry-publish.md"), "nothing written")

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
	require.Contains(t, stdout, "adr       ADR-0003 -> ADR-0002  development/decisions/0002-use-go.md  [accepted]")
	require.Contains(t, stdout, "apex: lift from apex-v0 v0.29.3")
	require.Contains(t, stdout, "no ## Code Validation found")
	main, err := os.ReadFile(filepath.Join(root, "main.go"))
	require.NoError(t, err)
	require.Equal(t, "package main\n\n// Retry (ADR-0002).\nfunc main() {}\n", string(main))
}

// L1: which ape runs the lift, and what becomes of the project's pin.
func TestLiftApe(t *testing.T) {
	bingo := func() bool { return true }
	noBingo := func() bool { return false }
	root, _ := pinProject(t, "v0.9.0")

	a := liftApe(root, "1.0.0-rc.3", "/bin/ape", true, bingo)
	require.Equal(t, ".bingo/ape.mod", a.PinFile)
	require.Equal(t, "v0.9.0", a.Pinned)
	require.Equal(t, "bingo get github.com/exoport/apex_process_ape/cmd/ape@v1.0.0-rc.3", a.PinCommand)
	require.Contains(t, a.PinNote, "the lift moves the pin from v0.9.0 to v1.0.0-rc.3")
	require.Nil(t, a.MovePin, "a dry run moves nothing")

	a = liftApe(root, "1.0.0", "/bin/ape", false, bingo)
	require.NotNil(t, a.MovePin, "a release, with bingo: the lift moves it")

	a = liftApe(root, "1.0.0", "/bin/ape", false, noBingo)
	require.Nil(t, a.MovePin)
	require.Contains(t, a.PinNote, "bingo is not on PATH")
	require.Contains(t, a.PinNote, "APE_TOOLCHAIN=local")
	require.Contains(t, a.PinNote, "every git commit too (the pre-commit hook runs ape)", "R33")
	require.Contains(t, a.PinNote, "every Claude session in the project", "R33")

	a = liftApe(root, "0.10.1-0.20261017042934-7f19dc3a009e", "/bin/ape", false, bingo)
	require.Nil(t, a.MovePin)
	require.Contains(t, a.PinNote, "is no release a pin can name")
	require.Contains(t, a.PinNote, "until then "+apexv1.LiftPinLocal, "R33: every trial lift leaves the pin")
	require.Equal(t, "bingo get github.com/exoport/apex_process_ape/cmd/ape@v0.10.1-0.20261017042934-7f19dc3a009e", a.PinCommand)

	a = liftApe(root, "0.9.0", "/bin/ape", false, bingo)
	require.Nil(t, a.MovePin)
	require.Empty(t, a.PinCommand)
	require.Equal(t, "the pin is this version already", a.PinNote)

	a = liftApe(t.TempDir(), "1.0.0", "/bin/ape", false, bingo)
	require.Empty(t, a.PinFile, "no pin")
	require.Equal(t, "1.0.0", a.Version)
	require.Equal(t, "/bin/ape", a.Path)
}

func TestProjectLift_SaysWhichApeRuns(t *testing.T) {
	root := v0Repo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".bingo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".bingo", "ape.mod"),
		[]byte("module _\n\ngo 1.27.0\n\nrequire github.com/exoport/apex_process_ape v0.9.0 // cmd/ape\n"), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "pin")
	stdout, _, err := v1Exec(t, newProjectCmd, "lift", "--dry-run", "--cwd", root)
	require.NoError(t, err)
	self, _ := os.Executable()
	if r, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = r
	}
	require.Contains(t, stdout, "ape       "+Version+" ("+self+") runs this lift — exempt from the project's ape pin\n")
	require.Contains(t, stdout, "pin       .bingo/ape.mod pins v0.9.0: ")
}

// R6: a dry run writes no report, so it lists every hashed file, text and
// --json; rc.4 stopped at 50 with "… and N more (in the report)", hiding
// real perimeter files past 50 behind a file that did not exist.
func TestProjectLift_DryRunListsEveryHashed(t *testing.T) {
	root := v0Repo(t)
	guard := "package many\n\nfunc TestBytes(t *testing.T) {\n\tfiles, _ := filepath.Glob(\"*.go\")\n\t_ = sha256.New()\n}\n"
	require.NoError(t, os.MkdirAll(filepath.Join(root, "pkg", "many"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "many", "guard_test.go"), []byte(guard), 0o644))
	for i := range 60 {
		src := fmt.Sprintf("package many\n\n// Story 3.2: part %d\nvar X%d = 1\n", i, i)
		require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "many", fmt.Sprintf("f%02d.go", i)), []byte(src), 0o644))
	}
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "a guarded package")

	stdout, _, err := v1Exec(t, newProjectCmd, "lift", "--dry-run", "--cwd", root)
	require.NoError(t, err)
	require.Equal(t, 60, strings.Count(stdout, "\nhashed    pkg/many/f"), stdout)
	require.Contains(t, stdout, "hashed    pkg/many/f59.go — pkg/many/guard_test.go seems to pin its bytes")
	require.NotContains(t, stdout, "… and", "no pointer to a report a dry run does not write")

	stdout, _, err = v1Exec(t, newProjectCmd, "lift", "--dry-run", "--json", "--cwd", root)
	require.NoError(t, err)
	var plan apexv1.LiftResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &plan), stdout)
	require.Len(t, plan.Hashed, 60)
	require.Equal(t, 60, plan.HashedTotal)
}
