//go:build !windows

package pipeline //nolint:testpackage // white-box reads internal manifestWriter side effects

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Whole file is //go:build !windows because every TestRun_* here
// drives a bash shim script (.sh) as the synthetic claude binary.
// Windows can't exec .sh files directly (`fork/exec …shim.sh: %1 is
// not a valid Win32 application`) and rewriting these shims as .cmd
// or .ps1 would change the test surface enough to lose the assertion
// fidelity. The production code paths these tests cover are exercised
// indirectly on Windows via the smoke step in CI.

// initGitRepo runs `git init`, writes a `_output/`-ignoring `.gitignore`,
// then stages + commits every file currently in `root`. Tests must
// write their pipeline spec (and any other fixture state) BEFORE
// calling initGitRepo so it lands in the baseline commit; otherwise
// the dirty-tree gate will refuse on entry. Skips when git is absent.
//
// Uses t.Setenv so the author/committer env vars apply to every child
// process spawned during the test — including production code paths
// like gitCommit that don't set their own cmd.Env and would otherwise
// inherit a bare environment in CI runners without git config.
func initGitRepo(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	t.Setenv("GIT_AUTHOR_NAME", "ape-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "ape-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "ape-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "ape-test@example.com")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("_output/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"add", "-A"},
		{"commit", "-m", "init"},
	} {
		var buf bytes.Buffer
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = root
		cmd.Stderr = &buf
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v (stderr: %s)", args, err, buf.String())
		}
	}
}

// writePipelineSpec writes a single-stage pipeline with the given
// chain YAML body to `<root>/_apex/pipelines/<name>.yaml`.
func writePipelineSpec(t *testing.T, root, name, chainBody string) {
	t.Helper()
	pipelinesDir := filepath.Join(root, "_apex", "pipelines")
	if err := os.MkdirAll(pipelinesDir, 0o755); err != nil {
		t.Fatalf("mkdir pipelines: %v", err)
	}
	body := "name: " + name + "\nstages:\n  only:\n    chain:\n" + chainBody
	if err := os.WriteFile(filepath.Join(pipelinesDir, name+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
}

// writeFileShim returns the path to a shell shim that writes a file
// the stream-json events claim it touched. The shim emits one stream
// JSON line plus a terminal `result` event so parseResultEvent can
// surface metrics.
func writeFileShim(t *testing.T, dir, body string) string {
	t.Helper()
	shim := filepath.Join(dir, "shim.sh")
	content := "#!/bin/sh\nset -e\n" + body +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"duration_ms\":1,\"num_turns\":1,\"total_cost_usd\":0.01,\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":0}}'\n" +
		"exit 0\n"
	if err := os.WriteFile(shim, []byte(content), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	return shim
}

// loadLatestManifest reads <root>/_output/pipelines/<name>/latest/manifest.yaml.
func loadLatestManifest(t *testing.T, root, name string) Manifest {
	t.Helper()
	latest := filepath.Join(root, "_output", "pipelines", name, "latest")
	target, err := os.Readlink(latest)
	if err != nil {
		t.Fatalf("readlink latest: %v", err)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(latest), target)
	}
	data, err := os.ReadFile(filepath.Join(target, "manifest.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// TestRun_CommitDefaultMessage — default-on commits land with the
// derived `ape:<pipeline>/<stage>/<skill>` message and a recorded SHA.
func TestRun_CommitDefaultMessage(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "smoke",
		"      - skill: apex-write\n        commit: true\n")
	initGitRepo(t, root)

	shim := writeFileShim(t, t.TempDir(),
		"echo 'hello' > '"+root+"/note.md'\n")

	spec, err := LoadSpec("smoke", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	stubSpecSkills(t, root, spec)
	err = Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		ApeVersion:  "0.1.0-test",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	m := loadLatestManifest(t, root, "smoke")
	if m.SchemaVersion != 2 {
		t.Errorf("schema_version = %d, want 2", m.SchemaVersion)
	}
	if len(m.Stages) != 1 || len(m.Stages[0].Steps) != 1 {
		t.Fatalf("bad shape: %+v", m.Stages)
	}
	step := m.Stages[0].Steps[0]
	if step.CommitStatus != CommitStatusCommitted {
		t.Errorf("commit_status = %q, want committed", step.CommitStatus)
	}
	if step.CommitMessage != "ape:smoke/only/apex-write" {
		t.Errorf("commit_message = %q", step.CommitMessage)
	}
	if step.CommitSHA == "" {
		t.Errorf("commit_sha empty")
	}
	if m.Totals.CommitsMade != 1 {
		t.Errorf("totals.commits_made = %d, want 1", m.Totals.CommitsMade)
	}
}

// TestRun_CommitExplicitMessage — pipeline YAML `commit: "msg"`
// overrides the default derivation.
func TestRun_CommitExplicitMessage(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "explicit",
		"      - skill: apex-write\n        commit: \"docs: add note\"\n")
	initGitRepo(t, root)
	shim := writeFileShim(t, t.TempDir(),
		"echo 'hello' > '"+root+"/note.md'\n")

	spec, _ := LoadSpec("explicit", root)
	stubSpecSkills(t, root, spec)
	if err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		ApeVersion:  "0.1.0-test",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := loadLatestManifest(t, root, "explicit")
	step := m.Stages[0].Steps[0]
	if step.CommitMessage != "docs: add note" {
		t.Errorf("commit_message = %q, want %q", step.CommitMessage, "docs: add note")
	}
	if step.CommitStatus != CommitStatusCommitted {
		t.Errorf("commit_status = %q", step.CommitStatus)
	}
}

// TestRun_CommitSkippedBySpec — `commit: false` produces no commit.
func TestRun_CommitSkippedBySpec(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "skipspec",
		"      - skill: apex-write\n        commit: false\n")
	initGitRepo(t, root)
	shim := writeFileShim(t, t.TempDir(),
		"echo 'hello' > '"+root+"/note.md'\n")

	spec, _ := LoadSpec("skipspec", root)
	stubSpecSkills(t, root, spec)
	if err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		ApeVersion:  "0.1.0-test",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := loadLatestManifest(t, root, "skipspec")
	step := m.Stages[0].Steps[0]
	if step.CommitStatus != CommitStatusSkippedBySpec {
		t.Errorf("commit_status = %q, want skipped-by-spec", step.CommitStatus)
	}
	if step.CommitSHA != "" {
		t.Errorf("commit_sha = %q, want empty", step.CommitSHA)
	}
	if m.Totals.CommitsMade != 0 {
		t.Errorf("totals.commits_made = %d, want 0", m.Totals.CommitsMade)
	}
}

// TestRun_NoCommitFlagSkipsAll — `--no-commit` kills every commit
// regardless of per-step YAML.
func TestRun_NoCommitFlagSkipsAll(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "nocommit",
		"      - skill: apex-write\n        commit: \"docs: would-commit-but\"\n")
	initGitRepo(t, root)
	shim := writeFileShim(t, t.TempDir(),
		"echo 'hello' > '"+root+"/note.md'\n")

	spec, _ := LoadSpec("nocommit", root)
	stubSpecSkills(t, root, spec)
	if err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		NoCommit:    true,
		ApeVersion:  "0.1.0-test",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := loadLatestManifest(t, root, "nocommit")
	step := m.Stages[0].Steps[0]
	if step.CommitStatus != CommitStatusSkippedByFlag {
		t.Errorf("commit_status = %q, want skipped-by-flag", step.CommitStatus)
	}
	if m.Totals.CommitsMade != 0 {
		t.Errorf("totals.commits_made = %d, want 0", m.Totals.CommitsMade)
	}
}

// TestRun_CommitNoOpOnEmptyDiff — step succeeds but produces no
// changes; recorded as no-op.
func TestRun_CommitNoOpOnEmptyDiff(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "noop",
		"      - skill: apex-write\n        commit: true\n")
	initGitRepo(t, root)
	shim := writeFileShim(t, t.TempDir(), "") // body does nothing

	spec, _ := LoadSpec("noop", root)
	stubSpecSkills(t, root, spec)
	if err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		ApeVersion:  "0.1.0-test",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := loadLatestManifest(t, root, "noop")
	step := m.Stages[0].Steps[0]
	if step.CommitStatus != CommitStatusNoOp {
		t.Errorf("commit_status = %q, want no-op", step.CommitStatus)
	}
	if step.CommitSHA != "" {
		t.Errorf("commit_sha = %q, want empty", step.CommitSHA)
	}
}

// TestRun_DirtyTreeGateRefuses — pre-existing uncommitted changes in
// the project root abort the pipeline before any step runs.
func TestRun_DirtyTreeGateRefuses(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	if err := os.WriteFile(filepath.Join(root, "WIP.md"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("write WIP: %v", err)
	}
	writePipelineSpec(t, root, "dirty",
		"      - skill: apex-write\n        commit: true\n")
	shim := writeFileShim(t, t.TempDir(),
		"echo 'hello' > '"+root+"/note.md'\n")

	spec, _ := LoadSpec("dirty", root)
	stubSpecSkills(t, root, spec)
	err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		ApeVersion:  "0.1.0-test",
	})
	if err == nil {
		t.Fatalf("expected dirty-tree refusal")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("error %q missing dirty-tree marker", err.Error())
	}
}

// TestRun_AllowDirtyBypassesGate — `--commit-allow-dirty` opts past
// the gate; the first commit absorbs the WIP.
func TestRun_AllowDirtyBypassesGate(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	if err := os.WriteFile(filepath.Join(root, "WIP.md"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("write WIP: %v", err)
	}
	writePipelineSpec(t, root, "allowdirty",
		"      - skill: apex-write\n        commit: true\n")
	shim := writeFileShim(t, t.TempDir(),
		"echo 'hello' > '"+root+"/note.md'\n")

	spec, _ := LoadSpec("allowdirty", root)
	stubSpecSkills(t, root, spec)
	if err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		AllowDirty:  true,
		ApeVersion:  "0.1.0-test",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := loadLatestManifest(t, root, "allowdirty")
	if m.Stages[0].Steps[0].CommitStatus != CommitStatusCommitted {
		t.Errorf("commit_status = %q", m.Stages[0].Steps[0].CommitStatus)
	}
}

// TestRun_DirtyTreeIgnoredWhenNoCommit — passing --no-commit makes the
// dirty-tree gate moot (the run is commit-free anyway).
func TestRun_DirtyTreeIgnoredWhenNoCommit(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	if err := os.WriteFile(filepath.Join(root, "WIP.md"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("write WIP: %v", err)
	}
	writePipelineSpec(t, root, "dirtync",
		"      - skill: apex-write\n")
	shim := writeFileShim(t, t.TempDir(), "")

	spec, _ := LoadSpec("dirtync", root)
	stubSpecSkills(t, root, spec)
	if err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		NoCommit:    true,
		ApeVersion:  "0.1.0-test",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRun_CommitOnFailedStep — step exits non-zero; no commit
// attempted; status is skipped-step-failed.
func TestRun_CommitOnFailedStep(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "fail",
		"      - skill: apex-write\n")
	initGitRepo(t, root)
	// Shim writes a file then exits non-zero.
	shim := filepath.Join(t.TempDir(), "fail.sh")
	if err := os.WriteFile(shim,
		[]byte("#!/bin/sh\necho 'data' > '"+root+"/note.md'\necho '{\"type\":\"error\"}'\nexit 2\n"),
		0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	spec, _ := LoadSpec("fail", root)
	stubSpecSkills(t, root, spec)
	err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		ApeVersion:  "0.1.0-test",
	})
	if err == nil {
		t.Fatalf("expected step failure to propagate")
	}
	m := loadLatestManifest(t, root, "fail")
	step := m.Stages[0].Steps[0]
	if step.CommitStatus != CommitStatusSkippedStepFailed {
		t.Errorf("commit_status = %q, want skipped-step-failed", step.CommitStatus)
	}
}

// TestRun_StageBoundaryCommit — PLAN-6 / C2 Phase D: a stage-level
// `commit: "msg"` directive produces exactly one commit per stage
// (folding the chain's accumulated diff), attributed to the last
// step in the chain. Earlier steps are recorded as
// `deferred-to-stage`. The explicit message wins over any per-step
// derivation.
func TestRun_StageBoundaryCommit(t *testing.T) {
	root := t.TempDir()
	pipelinesDir := filepath.Join(root, "_apex", "pipelines")
	if err := os.MkdirAll(pipelinesDir, 0o755); err != nil {
		t.Fatalf("mkdir pipelines: %v", err)
	}
	body := `name: stagecommit
stages:
  s1:
    commit: "specs: stage commit"
    chain:
      - skill: step-one
      - skill: step-two
      - skill: step-three
`
	if err := os.WriteFile(filepath.Join(pipelinesDir, "stagecommit.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	initGitRepo(t, root)
	// Each step writes a unique file so the working tree is dirty
	// across the chain. Only one commit should land at stage end.
	shim := filepath.Join(t.TempDir(), "shim.sh")
	content := "#!/bin/sh\nset -e\n" +
		"f=$(mktemp '" + root + "/note-XXXXXX.md')\necho 'change' > \"$f\"\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"duration_ms\":1,\"num_turns\":1,\"total_cost_usd\":0.01,\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":0}}'\n"
	if err := os.WriteFile(shim, []byte(content), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	spec, err := LoadSpec("stagecommit", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	stubSpecSkills(t, root, spec)
	if err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		ApeVersion:  "0.1.0-test",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	m := loadLatestManifest(t, root, "stagecommit")
	if len(m.Stages) != 1 || len(m.Stages[0].Steps) != 3 {
		t.Fatalf("bad shape: %+v", m.Stages)
	}
	if m.Totals.CommitsMade != 1 {
		t.Errorf("totals.commits_made = %d, want 1", m.Totals.CommitsMade)
	}
	for i, want := range []CommitStatus{
		CommitStatusDeferredToStage,
		CommitStatusDeferredToStage,
		CommitStatusCommitted,
	} {
		got := m.Stages[0].Steps[i].CommitStatus
		if got != want {
			t.Errorf("step %d commit_status = %q, want %q", i+1, got, want)
		}
	}
	last := m.Stages[0].Steps[2]
	if last.CommitMessage != "specs: stage commit" {
		t.Errorf("last step commit_message = %q, want %q", last.CommitMessage, "specs: stage commit")
	}
	if last.CommitSHA == "" {
		t.Errorf("last step commit_sha empty")
	}
}

// TestRun_StageBoundaryCommit_Suppressed — PLAN-6 / C2: an explicit
// step-level `commit: false` suppresses the stage-end commit even
// when the stage declares one. All steps record skipped-by-spec.
func TestRun_StageBoundaryCommit_Suppressed(t *testing.T) {
	root := t.TempDir()
	pipelinesDir := filepath.Join(root, "_apex", "pipelines")
	if err := os.MkdirAll(pipelinesDir, 0o755); err != nil {
		t.Fatalf("mkdir pipelines: %v", err)
	}
	body := `name: stagesuppress
stages:
  s1:
    commit: "specs: never fires"
    chain:
      - skill: step-one
      - skill: step-two
        commit: false
      - skill: step-three
`
	if err := os.WriteFile(filepath.Join(pipelinesDir, "stagesuppress.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	initGitRepo(t, root)
	shim := filepath.Join(t.TempDir(), "shim.sh")
	content := "#!/bin/sh\nset -e\n" +
		"f=$(mktemp '" + root + "/note-XXXXXX.md')\necho 'change' > \"$f\"\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"duration_ms\":1,\"num_turns\":1,\"total_cost_usd\":0,\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":0}}'\n"
	if err := os.WriteFile(shim, []byte(content), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	spec, _ := LoadSpec("stagesuppress", root)
	// dirty-tree gate would refuse a suppressed-but-stage-declared
	// pipeline because pipelineWantsCommits is false; safe to run.
	stubSpecSkills(t, root, spec)
	if err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		ApeVersion:  "0.1.0-test",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := loadLatestManifest(t, root, "stagesuppress")
	if m.Totals.CommitsMade != 0 {
		t.Errorf("totals.commits_made = %d, want 0", m.Totals.CommitsMade)
	}
	for i, st := range m.Stages[0].Steps {
		if st.CommitStatus != CommitStatusSkippedBySpec {
			t.Errorf("step %d commit_status = %q, want skipped-by-spec", i+1, st.CommitStatus)
		}
	}
}
