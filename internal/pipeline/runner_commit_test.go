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

// Whole file is //go:build !windows because every TestRun_* here drives
// a bash shim as the synthetic claude REPL. Windows can't exec .sh files
// directly and lacks the POSIX PTY these tests depend on. The production
// code paths are exercised indirectly on Windows via the CI smoke step.
//
// PTY-only note (PLAN-9 F2, v0.0.36): ape no longer has a programmatic
// `claude -p` exec path, so these commit-outcome tests run through the
// interactive per-stage REPL (runStagesInteractive). The shim is a
// passive bash presenting a `❯` prompt so repl.WaitForReady succeeds; it
// does NO work itself. In the real interactive path the *model* mutates
// the tree, so — mirroring that — each test PRE-CREATES the file changes
// the commit machinery acts on and passes --commit-allow-dirty when the
// pre-created diff would otherwise trip the dirty-tree gate. Steps are
// advanced by a fast no-op WaitStepDone (no bridge Stop hook is wired in
// these unit tests).

// initGitRepo runs `git init`, writes a `_output/`-ignoring `.gitignore`,
// then stages + commits every file currently in `root`. Tests must write
// their pipeline spec BEFORE calling initGitRepo so it lands in the
// baseline commit; any diff a test wants the runner to commit must be
// created AFTER this call so it shows up as an uncommitted change.
//
// Uses t.Setenv so the author/committer env vars apply to every child
// process spawned during the test — including production code paths like
// gitCommit that don't set their own cmd.Env and would otherwise inherit
// a bare environment in CI runners without git config.
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

// writePipelineSpec writes a single-stage pipeline with the given chain
// YAML body to `<root>/_apex/pipelines/<name>.yaml`.
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

// claudeREPLShim returns a bash stand-in for the claude CLI that presents
// a `❯` prompt (so repl.WaitForReady's empty-prompt fallback matches),
// then execs an interactive shell. It performs no work — tests seed the
// tree diff themselves. Skips when bash is unavailable.
func claudeREPLShim(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	shim := filepath.Join(t.TempDir(), "claude-shim.sh")
	body := "#!/bin/sh\nPS1='❯ '\nexport PS1\nexec bash --noprofile --norc\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	return shim
}

// fastStepDone advances interactive steps immediately. Production wires
// WaitStepDone to the bridge's Stop hook; these tests don't run a bridge,
// and the tree diff is pre-seeded, so returning nil at once is correct
// and keeps the tests fast + deterministic (no grace-window sleep).
func fastStepDone(_ context.Context, _ string, _ int) error { return nil }

// seedFile writes rel (relative to root) with some content so the tree
// gains an uncommitted diff for the commit machinery to act on.
func seedFile(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte("produced by "+rel+"\n"), 0o644); err != nil {
		t.Fatalf("seed %s: %v", rel, err)
	}
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

// runInteractive is the shared driver: interactive PTY exec with the
// passive REPL shim and a fast step-done, matching how every production
// run executes since v0.0.36.
func runInteractive(t *testing.T, root string, spec *Spec, opts RunOptions) error {
	t.Helper()
	opts.ProjectRoot = root
	opts.ClaudeBin = claudeREPLShim(t)
	opts.ApeVersion = "0.1.0-test"
	opts.WaitStepDone = fastStepDone
	stubSpecSkills(t, root, spec)
	return Run(context.Background(), spec, opts)
}

// TestRun_CommitDefaultMessage — default-on commits land with the derived
// `ape:<pipeline>/<stage>/<skill>` message and a recorded SHA.
func TestRun_CommitDefaultMessage(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "smoke",
		"      - skill: apex-write\n        commit: true\n")
	initGitRepo(t, root)
	seedFile(t, root, "note.md")

	spec, err := LoadSpec("smoke", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if err := runInteractive(t, root, spec, RunOptions{AllowDirty: true}); err != nil {
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

// TestRun_CommitExplicitMessage — pipeline YAML `commit: "msg"` overrides
// the default derivation.
func TestRun_CommitExplicitMessage(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "explicit",
		"      - skill: apex-write\n        commit: \"docs: add note\"\n")
	initGitRepo(t, root)
	seedFile(t, root, "note.md")

	spec, _ := LoadSpec("explicit", root)
	if err := runInteractive(t, root, spec, RunOptions{AllowDirty: true}); err != nil {
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

// TestRun_CommitSkippedBySpec — `commit: false` produces no commit even
// with a dirty tree.
func TestRun_CommitSkippedBySpec(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "skipspec",
		"      - skill: apex-write\n        commit: false\n")
	initGitRepo(t, root)
	seedFile(t, root, "note.md")

	spec, _ := LoadSpec("skipspec", root)
	// commit:false ⇒ pipelineWantsCommits is false ⇒ the dirty-tree gate
	// is skipped, so no AllowDirty needed.
	if err := runInteractive(t, root, spec, RunOptions{}); err != nil {
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
	seedFile(t, root, "note.md")

	spec, _ := LoadSpec("nocommit", root)
	if err := runInteractive(t, root, spec, RunOptions{NoCommit: true}); err != nil {
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

// TestRun_CommitNoOpOnEmptyDiff — step succeeds but produces no changes;
// recorded as no-op. No file is seeded, so the tree stays clean.
func TestRun_CommitNoOpOnEmptyDiff(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "noop",
		"      - skill: apex-write\n        commit: true\n")
	initGitRepo(t, root)

	spec, _ := LoadSpec("noop", root)
	if err := runInteractive(t, root, spec, RunOptions{}); err != nil {
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

// TestRun_DirtyTreeGateRefuses — pre-existing uncommitted changes abort
// the pipeline before any step runs (and before any REPL spawn).
func TestRun_DirtyTreeGateRefuses(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "dirty",
		"      - skill: apex-write\n        commit: true\n")
	initGitRepo(t, root)
	seedFile(t, root, "WIP.md") // dirty, and no AllowDirty below

	spec, _ := LoadSpec("dirty", root)
	err := runInteractive(t, root, spec, RunOptions{})
	if err == nil {
		t.Fatalf("expected dirty-tree refusal")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("error %q missing dirty-tree marker", err.Error())
	}
}

// TestRun_AllowDirtyBypassesGate — `--commit-allow-dirty` opts past the
// gate; the commit absorbs the pre-existing WIP.
func TestRun_AllowDirtyBypassesGate(t *testing.T) {
	root := t.TempDir()
	writePipelineSpec(t, root, "allowdirty",
		"      - skill: apex-write\n        commit: true\n")
	initGitRepo(t, root)
	seedFile(t, root, "WIP.md")

	spec, _ := LoadSpec("allowdirty", root)
	if err := runInteractive(t, root, spec, RunOptions{AllowDirty: true}); err != nil {
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
	writePipelineSpec(t, root, "dirtync",
		"      - skill: apex-write\n")
	initGitRepo(t, root)
	seedFile(t, root, "WIP.md")

	spec, _ := LoadSpec("dirtync", root)
	if err := runInteractive(t, root, spec, RunOptions{NoCommit: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRun_StageBoundaryCommit — PLAN-6 / C2 Phase D: a stage-level
// `commit: "msg"` directive produces exactly one commit per stage
// (folding the chain's accumulated diff), attributed to the last step in
// the chain. Earlier steps are recorded as `deferred-to-stage`.
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
	// Seed one file per step so the working tree is dirty across the
	// chain. Only one commit should land at stage end.
	seedFile(t, root, "note-1.md")
	seedFile(t, root, "note-2.md")
	seedFile(t, root, "note-3.md")

	spec, err := LoadSpec("stagecommit", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if err := runInteractive(t, root, spec, RunOptions{AllowDirty: true}); err != nil {
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
// step-level `commit: false` suppresses the stage-end commit even when
// the stage declares one. All steps record skipped-by-spec.
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
	seedFile(t, root, "note-1.md")

	spec, _ := LoadSpec("stagesuppress", root)
	// commit:false somewhere in the chain ⇒ pipelineWantsCommits false ⇒
	// dirty-tree gate skipped, so no AllowDirty needed.
	if err := runInteractive(t, root, spec, RunOptions{}); err != nil {
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

// NOTE: the former TestRun_CommitOnFailedStep (skipped-step-failed) was
// dropped in v0.0.36. In PTY-only interactive mode a step never reaches
// performStepCommit with a non-nil run error — a failed step surfaces as
// a waitStepDone error that breaks the stage loop before the commit
// boundary — so CommitStatusSkippedStepFailed is unreachable from an
// integration run. The resolveCommitOutcome mapping for that input is
// covered by the unit tests in commit_test.go.
