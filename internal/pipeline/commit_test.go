package pipeline //nolint:testpackage // white-box tests touch unexported decoder helpers

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestCommitDirective_UnmarshalAllShapes covers every accepted YAML
// shape documented in PLAN-4 / C1.
func TestCommitDirective_UnmarshalAllShapes(t *testing.T) {
	cases := []struct {
		name     string
		yamlText string
		wantMode CommitMode
		wantMsg  string
	}{
		{"omitted", "skill: foo\n", CommitModeDefault, ""},
		{"null", "skill: foo\ncommit: ~\n", CommitModeDefault, ""},
		{"bool-true", "skill: foo\ncommit: true\n", CommitModeDefault, ""},
		{"bool-false", "skill: foo\ncommit: false\n", CommitModeSkip, ""},
		{"string", "skill: foo\ncommit: \"docs: add PRD\"\n", CommitModeExplicit, "docs: add PRD"},
		{"unquoted-string", "skill: foo\ncommit: hello\n", CommitModeExplicit, "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var step Step
			if err := yaml.Unmarshal([]byte(tc.yamlText), &step); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if step.Commit.Mode != tc.wantMode {
				t.Errorf("Mode = %v, want %v", step.Commit.Mode, tc.wantMode)
			}
			if step.Commit.Message != tc.wantMsg {
				t.Errorf("Message = %q, want %q", step.Commit.Message, tc.wantMsg)
			}
		})
	}
}

// TestCommitDirective_UnmarshalRejectsInvalid covers the rejection
// paths (PLAN-4 / C1 — multi-line, empty string, wrong-type).
func TestCommitDirective_UnmarshalRejectsInvalid(t *testing.T) {
	cases := []struct {
		name     string
		yamlText string
		wantSub  string
	}{
		{
			name:     "multi-line",
			yamlText: "skill: foo\ncommit: |\n  line1\n  line2\n",
			wantSub:  "single-line",
		},
		{
			name:     "empty-string",
			yamlText: "skill: foo\ncommit: \"\"\n",
			wantSub:  "cannot be empty",
		},
		{
			name:     "mapping",
			yamlText: "skill: foo\ncommit:\n  enabled: true\n",
			wantSub:  "must be a bool or string scalar",
		},
		{
			name:     "sequence",
			yamlText: "skill: foo\ncommit:\n  - a\n  - b\n",
			wantSub:  "must be a bool or string scalar",
		},
		{
			name:     "integer",
			yamlText: "skill: foo\ncommit: 42\n",
			wantSub:  "must be a bool or string",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var step Step
			err := yaml.Unmarshal([]byte(tc.yamlText), &step)
			if err == nil {
				t.Fatalf("expected error, got nil; step=%+v", step)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q missing substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestDerivedCommitMessage exercises the default-message format
// including filesystem-name sanitization on the inputs.
func TestDerivedCommitMessage(t *testing.T) {
	cases := []struct {
		pipeline, stage, skill, want string
	}{
		{"design", "prd", "apex-create-prd", "ape:design/prd/apex-create-prd"},
		{"design", "ux", "apex-create-ux-design", "ape:design/ux/apex-create-ux-design"},
		// Sanitization replaces unsafe chars with underscore.
		{"weird name", "stage/with/slash", "skill_a.b-c", "ape:weird_name/stage_with_slash/skill_a.b-c"},
	}
	for _, tc := range cases {
		got := DerivedCommitMessage(tc.pipeline, tc.stage, tc.skill)
		if got != tc.want {
			t.Errorf("DerivedCommitMessage(%q,%q,%q) = %q, want %q",
				tc.pipeline, tc.stage, tc.skill, got, tc.want)
		}
	}
}

// TestCommitDirective_Resolve covers the three Mode paths.
func TestCommitDirective_Resolve(t *testing.T) {
	t.Run("default-mode-derives-message", func(t *testing.T) {
		c := CommitDirective{Mode: CommitModeDefault}
		msg, skip := c.Resolve("design", "prd", "apex-create-prd")
		if skip {
			t.Fatalf("expected skip=false")
		}
		if msg != "ape:design/prd/apex-create-prd" {
			t.Errorf("msg=%q", msg)
		}
	})
	t.Run("skip-mode", func(t *testing.T) {
		c := CommitDirective{Mode: CommitModeSkip}
		_, skip := c.Resolve("design", "prd", "apex-create-prd")
		if !skip {
			t.Fatalf("expected skip=true")
		}
	})
	t.Run("explicit-mode-uses-message-verbatim", func(t *testing.T) {
		c := CommitDirective{Mode: CommitModeExplicit, Message: "docs: add PRD"}
		msg, skip := c.Resolve("design", "prd", "apex-create-prd")
		if skip {
			t.Fatalf("expected skip=false")
		}
		if msg != "docs: add PRD" {
			t.Errorf("msg=%q, want %q", msg, "docs: add PRD")
		}
	})
}
