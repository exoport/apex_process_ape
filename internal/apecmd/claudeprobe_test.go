package apecmd

import (
	"strings"
	"testing"
)

// ape resolves `haiku` to claude-haiku-5-5 itself, so the installed claude
// can be older than the model ape starts. Claude Code 2.1.292 runs it as an
// unrecognized model with a 200k window; the run must say so.
func TestClaudeTooOldWarnings(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		claude  string
		models  []string
		wantFor []string
	}{
		{"older claude, haiku", "2.1.292", []string{"haiku"}, []string{"claude-haiku-5-5"}},
		{"the first version that knows it", "2.1.294", []string{"haiku"}, nil},
		{"newer claude", "2.2.0", []string{"claude-haiku-5-5"}, nil},
		{"one warning per model, any spelling", "2.1.292", []string{"haiku", "claude-haiku-5-5", "Haiku[1m]"}, []string{"claude-haiku-5-5"}},
		{"no recorded floor", "2.1.200", []string{"opus", "claude-sonnet-5-5"}, nil},
		{"claude version unknown", "", []string{"haiku"}, nil},
		{"not a version", "dev", []string{"haiku"}, nil},
	} {
		got := claudeTooOldWarnings(tc.claude, tc.models)
		if len(got) != len(tc.wantFor) {
			t.Errorf("%s: %d warning(s), want %d: %q", tc.name, len(got), len(tc.wantFor), got)
			continue
		}
		for i, id := range tc.wantFor {
			if !strings.Contains(got[i], id) || !strings.Contains(got[i], tc.claude) {
				t.Errorf("%s: warning %q does not name %s and claude %s", tc.name, got[i], id, tc.claude)
			}
		}
	}
}
