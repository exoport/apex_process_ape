package service

import (
	"errors"
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
		{
			name:    "prompt kind unavailable",
			kind:    KindPrompt,
			req:     RunRequest{ProjectRoot: "/p", Prompt: "hi"},
			wantErr: ErrKindUnavailable,
		},
		{
			name:    "script kind unavailable",
			kind:    KindScript,
			req:     RunRequest{ProjectRoot: "/p", ScriptPath: "x.star"},
			wantErr: ErrKindUnavailable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildArgs(tc.kind, tc.req)
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
