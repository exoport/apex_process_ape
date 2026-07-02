package pipeline

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestNewSingleStepSpecEffective verifies the synthesized spec plugs
// into the precedence machinery exactly like a loaded one.
func TestNewSingleStepSpecEffective(t *testing.T) {
	step := Step{
		Skill:      "apex-create-prd",
		Agent:      testAgentPM,
		Model:      testModelOpus1M,
		Args:       "--from-status draft",
		PromptFlag: "--prompt",
	}
	spec := NewSingleStepSpec("apex-create-prd", step, nil)

	if got := len(spec.Stages()); got != 1 {
		t.Fatalf("expected 1 stage, got %d", got)
	}
	if got := len(spec.Stages()[0].Chain); got != 1 {
		t.Fatalf("expected 1 step, got %d", got)
	}

	model, agent, commit, err := spec.Effective("apex-create-prd", 0)
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if model != testModelOpus1M || agent != testAgentPM {
		t.Fatalf("Effective returned model=%q agent=%q", model, agent)
	}
	if commit.Boundary != CommitBoundaryNone {
		t.Fatalf("nil commit directive must resolve to no boundary, got %v", commit.Boundary)
	}
}

// TestNewSingleStepSpecCommitPlan pins the two task commit layers:
// nil directive → no commit anywhere; explicit directive → one
// stage-boundary commit with the given message.
func TestNewSingleStepSpecCommitPlan(t *testing.T) {
	step := Step{Skill: "apex-shard-doc"}

	noCommit := NewSingleStepSpec("apex-shard-doc", step, nil)
	if noCommit.PipelineWantsCommits() {
		t.Fatalf("nil directive must not want commits")
	}
	plan, err := noCommit.PlanStageCommits("apex-shard-doc")
	if err != nil {
		t.Fatalf("PlanStageCommits: %v", err)
	}
	if plan.StageDirective != nil || len(plan.StepDirectives) != 0 {
		t.Fatalf("nil directive must produce an empty plan, got %+v", plan)
	}

	withCommit := NewSingleStepSpec("apex-shard-doc", step, &CommitDirective{
		Mode:    CommitModeExplicit,
		Message: "chore: shard prd",
	})
	if !withCommit.PipelineWantsCommits() {
		t.Fatalf("explicit directive must want commits")
	}
	plan, err = withCommit.PlanStageCommits("apex-shard-doc")
	if err != nil {
		t.Fatalf("PlanStageCommits: %v", err)
	}
	if plan.StageDirective == nil || plan.StageDirective.Message != "chore: shard prd" {
		t.Fatalf("expected stage directive with explicit message, got %+v", plan.StageDirective)
	}
}

// TestDerivedTaskCommitMessage pins the bare --task-commit derivation.
func TestDerivedTaskCommitMessage(t *testing.T) {
	if got := DerivedTaskCommitMessage("apex-shard-doc"); got != "ape:task/apex-shard-doc" {
		t.Fatalf("DerivedTaskCommitMessage = %q", got)
	}
	if got := DerivedTaskCommitMessage("weird skill"); got != "ape:task/weird_skill" {
		t.Fatalf("sanitization: got %q", got)
	}
}

// TestSingleStepInteractiveEndToEnd drives a NewSingleStepSpec run
// through the REAL interactive PTY runner against a bash stand-in
// (PS1='❯ ' satisfies the empty-prompt ready fallback), with the
// grace-window step completion (no bridge) and a task-layer commit
// directive. Asserts: manifest written under <base>/<skill>/<run-id>,
// step recorded completed, and exactly one boundary commit with the
// explicit message.
func TestSingleStepInteractiveEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX PTY test; skipping on Windows")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	ctx := context.Background()

	// Git repo with a base commit so the boundary commit is observable.
	git := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	// Repo-local identity: the RUNNER's boundary commit runs without
	// this test's per-call env vars, and CI has no global git config.
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("commit", "--allow-empty", "-m", "base")

	// Framework skill so PreflightSkills resolves.
	skillDir := filepath.Join(root, ".claude", "skills", "apex-shard-doc")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# shard"), 0o644); err != nil {
		t.Fatal(err)
	}

	// bash stand-in for claude: ignores argv, presents a ❯ prompt.
	shim := filepath.Join(t.TempDir(), "claude-shim.sh")
	shimBody := "#!/bin/sh\nPS1='❯ '\nexport PS1\nexec bash --noprofile --norc\n"
	if err := os.WriteFile(shim, []byte(shimBody), 0o755); err != nil {
		t.Fatal(err)
	}

	// The shim leaves a file change behind so the commit has a diff:
	// pre-create it — the boundary commit does `git add -A` of the tree,
	// and manifests land under _output which we point elsewhere.
	if err := os.WriteFile(filepath.Join(root, "artifact.md"), []byte("produced"), 0o644); err != nil {
		t.Fatal(err)
	}

	manifestBase := filepath.Join(t.TempDir(), "tasks")
	spec := NewSingleStepSpec("apex-shard-doc",
		Step{Skill: "apex-shard-doc", Args: "--doc prd"},
		&CommitDirective{Mode: CommitModeExplicit, Message: "chore: shard prd"},
	)

	err := Run(ctx, spec, RunOptions{
		ProjectRoot:          root,
		ClaudeBin:            shim,
		ManifestDir:          manifestBase,
		ApeVersion:           "test",
		Interactive:          true,
		InteractiveStepGrace: 1200 * time.Millisecond,
		AllowDirty:           true, // artifact.md is intentionally pre-dirty
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Manifest layout: <base>/<skill>/<run-id>/manifest.yaml + latest.
	runDir := ResolveLatestRunDir(root, "apex-shard-doc", manifestBase)
	if runDir == "" {
		t.Fatalf("latest symlink not resolved under %s", manifestBase)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.Status != StatusCompleted {
		t.Fatalf("run status = %v, want completed", m.Status)
	}
	if len(m.Stages) != 1 || len(m.Stages[0].Steps) != 1 {
		t.Fatalf("expected 1 stage / 1 step, got %+v", m.Stages)
	}
	step := m.Stages[0].Steps[0]
	if step.Skill != "apex-shard-doc" || step.Status != StatusCompleted {
		t.Fatalf("step record: %+v", step)
	}
	if step.CommitStatus != CommitStatusCommitted {
		t.Fatalf("commit status = %q, want committed", step.CommitStatus)
	}
	if m.Totals.CommitsMade != 1 {
		t.Fatalf("commits_made = %d, want 1", m.Totals.CommitsMade)
	}

	// The boundary commit is on HEAD with the explicit message.
	var out bytes.Buffer
	logCmd := exec.CommandContext(ctx, "git", "log", "-1", "--format=%s")
	logCmd.Dir = root
	logCmd.Stdout = &out
	if err := logCmd.Run(); err != nil {
		t.Fatalf("git log: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "chore: shard prd" {
		t.Fatalf("HEAD subject = %q, want %q", got, "chore: shard prd")
	}
}

// TestTaskPromptLineParity pins the slash-line byte-parity contract
// for the four `--no-commit` × agent/no-agent combinations `ape task`
// produces. The skill-layer --no-commit is injected by prefixing
// step.Args on the agent path only; the no-agent path already carries
// it by PAT-25 convention.
func TestTaskPromptLineParity(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		step  Step
		want  string
	}{
		{
			name:  "agent, no skill-no-commit",
			agent: testAgentPM,
			step:  Step{Skill: "apex-create-prd", Args: "--from-status draft"},
			want:  "/apex-agent-pm --autonomous -- apex-create-prd --autonomous --from-status draft",
		},
		{
			name:  "agent, skill-no-commit injected via args prefix",
			agent: testAgentPM,
			step:  Step{Skill: "apex-create-prd", Args: "--no-commit --from-status draft"},
			want:  "/apex-agent-pm --autonomous -- apex-create-prd --autonomous --no-commit --from-status draft",
		},
		{
			name: "no agent (convention adds --no-commit)",
			step: Step{Skill: "apex-shard-doc", Args: "--doc prd"},
			want: "/apex-shard-doc --autonomous --no-commit --doc prd",
		},
		{
			name: "no agent with explicit flag is a no-op (args unchanged)",
			step: Step{Skill: "apex-shard-doc"},
			want: "/apex-shard-doc --autonomous --no-commit",
		},
	}
	for _, tc := range cases {
		if got := assembleInteractivePromptLine(tc.agent, tc.step, ""); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
