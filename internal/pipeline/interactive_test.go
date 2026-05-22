package pipeline //nolint:testpackage // white-box tests on assembleInteractivePromptLine / buildInteractiveArgv

import (
	"strings"
	"testing"
)

// TestAssembleInteractivePromptLine covers the PAT-25 prompt-string
// shape the runner types into the PTY via Write. PLAN-6: the prompt
// is one slash-command line; `/clear` is sent separately between
// steps by the runner (not assembled here).
func TestAssembleInteractivePromptLine(t *testing.T) {
	cases := []struct {
		name     string
		effAgent string
		step     Step
		prompt   string
		want     string
	}{
		{
			name:     "with-agent",
			effAgent: "apex-agent-pm",
			step:     Step{Skill: "apex-create-prd"},
			want:     "/apex-agent-pm --autonomous -- apex-create-prd --autonomous",
		},
		{
			name:     "no-agent-uses-no-commit",
			effAgent: "",
			step:     Step{Skill: "apex-shard-doc"},
			want:     "/apex-shard-doc --autonomous --no-commit",
		},
		{
			name:     "args-appended-after-prefix",
			effAgent: "",
			step:     Step{Skill: "apex-shard-doc", Args: "--doc prd"},
			want:     "/apex-shard-doc --autonomous --no-commit --doc prd",
		},
		{
			name:     "prompt-flag-with-user-prompt",
			effAgent: "apex-agent-pm",
			step:     Step{Skill: "apex-create-epics-and-stories", PromptFlag: "--prompt"},
			prompt:   "build a TODO app",
			want:     "/apex-agent-pm --autonomous -- apex-create-epics-and-stories --autonomous --prompt build a TODO app",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assembleInteractivePromptLine(tc.effAgent, tc.step, tc.prompt)
			if got != tc.want {
				t.Errorf("\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestBuildInteractiveArgv covers the per-stage argv shape for the
// PTY-driven interactive runner: bridge prepend flags, then
// --dangerously-skip-permissions, then optional --model. No -p, no
// --output-format, no --system-prompt — the model just runs its
// normal REPL inside the PTY.
func TestBuildInteractiveArgv(t *testing.T) {
	cases := []struct {
		name         string
		claudeBin    string
		model        string
		prependFlags []string
		wantContains []string
		wantNot      []string
		wantErr      bool
	}{
		{
			name:         "minimal-no-model-no-prepend",
			claudeBin:    "claude",
			wantContains: []string{"claude", "--dangerously-skip-permissions"},
			wantNot:      []string{"-p", "--model", "stream-json", "--verbose", "--system-prompt"},
		},
		{
			name:         "with-model",
			claudeBin:    "claude",
			model:        "opus[1m]",
			wantContains: []string{"--model", "opus[1m]"},
			wantNot:      []string{"-p", "--system-prompt"},
		},
		{
			name:         "with-prepend-flags-before-skip-permissions",
			claudeBin:    "claude",
			prependFlags: []string{"--strict-mcp-config", "--mcp-config", "{}"},
			wantContains: []string{"--strict-mcp-config", "--mcp-config", "{}", "--dangerously-skip-permissions"},
			wantNot:      []string{"--system-prompt"},
		},
		{
			name:      "empty-bin-fails",
			claudeBin: "",
			wantErr:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv, err := buildInteractiveArgv(tc.claudeBin, tc.model, tc.prependFlags)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got argv=%v", argv)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			joined := strings.Join(argv, " ")
			for _, sub := range tc.wantContains {
				if !strings.Contains(joined, sub) {
					t.Errorf("argv missing %q: %s", sub, joined)
				}
			}
			for _, sub := range tc.wantNot {
				for _, a := range argv {
					if a == sub {
						t.Errorf("argv unexpectedly contains element %q: %v", sub, argv)
					}
				}
			}
		})
	}
}
