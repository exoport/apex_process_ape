package service

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"
)

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name    string
		kind    Kind
		req     RunRequest
		cfg     *Config
		want    []string
		wantErr error
	}{
		{
			name: "pipeline minimal",
			kind: KindPipeline,
			req:  RunRequest{ProjectRoot: "/p", Pipeline: "shard"},
			want: []string{"pipeline", "shard", "--no-tui", "--quiet", "--cwd", "/p"},
		},
		{
			name: "pipeline full",
			kind: KindPipeline,
			req: RunRequest{
				ProjectRoot: "/p", Pipeline: "shard", From: "review", Prompt: "go",
				NoCommit: true, CommitAllowDirty: true, UploadTranscripts: true,
			},
			want: []string{
				"pipeline", "shard", "--no-tui", "--quiet", "--cwd", "/p",
				"--from", "review", "--prompt", "go",
				"--no-commit", "--commit-allow-dirty", "--upload-transcripts",
			},
		},
		{
			name: "task minimal",
			kind: KindTask,
			req:  RunRequest{ProjectRoot: "/p", Skill: "apex-shard-doc"},
			want: []string{"task", "apex-shard-doc", "--quiet", "--cwd", "/p"},
		},
		{
			name: "task full with derived task-commit",
			kind: KindTask,
			req: RunRequest{
				ProjectRoot: "/p", Skill: "apex-create-prd", Agent: "apex-agent-pm",
				Model: "opus[1m]", Args: "--doc prd", Prompt: "a cli", PromptFlag: "--prompt",
				TaskCommit: new(""), NoCommit: true,
			},
			want: []string{
				"task", "apex-create-prd", "--quiet", "--cwd", "/p",
				"--agent", "apex-agent-pm", "--model", "opus[1m]", "--args", "--doc prd",
				"--prompt", "a cli", "--prompt-flag", "--prompt",
				"--task-commit", "--no-commit",
			},
		},
		{
			name: "task explicit task-commit message uses =-form",
			kind: KindTask,
			req:  RunRequest{ProjectRoot: "/p", Skill: "s", TaskCommit: new("chore: shard")},
			want: []string{"task", "s", "--quiet", "--cwd", "/p", "--task-commit=chore: shard"},
		},
		{
			name: "prompt positional",
			kind: KindPrompt,
			req:  RunRequest{ProjectRoot: "/p", Prompt: "add a CHANGELOG entry"},
			want: []string{"prompt", "add a CHANGELOG entry", "--quiet", "--cwd", "/p"},
		},
		{
			name: "prompt handoff full",
			kind: KindPrompt,
			req: RunRequest{
				ProjectRoot: "/p", Handoff: "development/handoffs/resume.md",
				Agent: "apex-agent-dev", Model: "opus[1m]", Workflow: true,
			},
			want: []string{
				"prompt", "--handoff", "development/handoffs/resume.md", "--quiet", "--cwd", "/p",
				"--agent", "apex-agent-dev", "--model", "opus[1m]", "--workflow",
			},
		},
		{
			name:    "prompt requires exactly one selector (neither)",
			kind:    KindPrompt,
			req:     RunRequest{ProjectRoot: "/p"},
			wantErr: ErrValidation,
		},
		{
			name:    "prompt requires exactly one selector (both)",
			kind:    KindPrompt,
			req:     RunRequest{ProjectRoot: "/p", Prompt: "hi", Handoff: "h.md"},
			wantErr: ErrValidation,
		},
		{
			name: "script source enabled",
			kind: KindScript,
			req:  RunRequest{ProjectRoot: "/p", ScriptSource: "package main\n"},
			cfg:  &Config{AllowScriptSource: true},
			want: []string{"script", "-", "--quiet", "--cwd", "/p"},
		},
		{
			name: "script source forced sandbox with args",
			kind: KindScript,
			req: RunRequest{
				ProjectRoot: "/p", ScriptSource: "package main\n",
				ScriptArgs: []string{"--target", "./component-a"},
			},
			cfg:  &Config{AllowScriptSource: true, ForceScriptSandbox: true},
			want: []string{"script", "-", "--quiet", "--cwd", "/p", "--sandbox", "--", "--target", "./component-a"},
		},
		{
			name:    "script source disabled by default",
			kind:    KindScript,
			req:     RunRequest{ProjectRoot: "/p", ScriptSource: "package main\n"},
			cfg:     &Config{},
			wantErr: ErrValidation,
		},
		{
			name:    "script requires exactly one selector (neither)",
			kind:    KindScript,
			req:     RunRequest{ProjectRoot: "/p"},
			cfg:     &Config{AllowScriptSource: true},
			wantErr: ErrValidation,
		},
		{
			name:    "script requires exactly one selector (both)",
			kind:    KindScript,
			req:     RunRequest{ProjectRoot: "/p", ScriptPath: "x.go", ScriptSource: "package main\n"},
			cfg:     &Config{AllowScriptSource: true},
			wantErr: ErrValidation,
		},
		{
			name:    "pipeline missing name",
			kind:    KindPipeline,
			req:     RunRequest{ProjectRoot: "/p"},
			wantErr: ErrValidation,
		},
		{
			name:    "task missing skill",
			kind:    KindTask,
			req:     RunRequest{ProjectRoot: "/p"},
			wantErr: ErrValidation,
		},
		{
			name:    "missing project_root",
			kind:    KindPipeline,
			req:     RunRequest{Pipeline: "x"},
			wantErr: ErrValidation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildArgs(tc.kind, tc.req, tc.cfg)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv mismatch:\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestBuildScriptArgsPath exercises the script_path filesystem boundary: an
// existing file inside an allowlisted root builds argv; a missing file or one
// outside every root is a validation error.
func TestBuildScriptArgsPath(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "ops", "nightly.go")
	if err := os.MkdirAll(filepath.Dir(script), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Allow: []string{root}}

	// Absolute path inside the allowlisted root.
	got, err := BuildArgs(KindScript, RunRequest{ProjectRoot: root, ScriptPath: script}, cfg)
	if err != nil {
		t.Fatalf("absolute in-root path: unexpected err: %v", err)
	}
	want := []string{"script", filepath.Clean(script), "--quiet", "--cwd", root}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}

	// Relative path resolves against project_root.
	if _, err := BuildArgs(KindScript, RunRequest{ProjectRoot: root, ScriptPath: "ops/nightly.go"}, cfg); err != nil {
		t.Fatalf("relative in-root path: unexpected err: %v", err)
	}

	// Missing file → validation.
	if _, err := BuildArgs(KindScript, RunRequest{ProjectRoot: root, ScriptPath: filepath.Join(root, "nope.go")}, cfg); !errors.Is(err, ErrValidation) {
		t.Errorf("missing file: err = %v, want ErrValidation", err)
	}

	// A real file outside every allowlisted root → validation.
	outside := filepath.Join(t.TempDir(), "evil.go")
	if err := os.WriteFile(outside, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildArgs(KindScript, RunRequest{ProjectRoot: root, ScriptPath: outside}, cfg); !errors.Is(err, ErrValidation) {
		t.Errorf("outside root: err = %v, want ErrValidation", err)
	}
}

func TestNewJobIDFormat(t *testing.T) {
	at := time.Date(2026, 7, 9, 8, 30, 15, 0, time.UTC)
	id, err := newJobID(at)
	if err != nil {
		t.Fatalf("newJobID: %v", err)
	}
	re := regexp.MustCompile(`^20260709-083015-[0-9a-f]{7}$`)
	if !re.MatchString(id) {
		t.Fatalf("job id %q does not match %s", id, re)
	}
	// Two mints at the same instant differ in the random suffix.
	id2, _ := newJobID(at)
	if id == id2 {
		t.Fatalf("two job ids at the same instant collided: %q", id)
	}
}
