package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Q154: what _apex/config.yaml's parse lets through is project check's to
// report — a limits: typo, an effort that is not a level, and (a notice)
// a top-level key no ape reader knows. Open still opens the project.
func TestProjectCheck_ConfigKeys(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	type want struct{ code, msg string }
	for name, tc := range map[string]struct {
		config  string
		finds   []want
		notices []want
	}{
		"known keys pass": {config: "project_name: x\neffort: high\nlimits: {attempts: 3, relays: 2}\nmodels:\n  apex-apply: {model: sonnet, effort: low}\n" +
			"output_folder: _output\ngovernance_repository_url: https://example.invalid/g.git\n"},
		"a limits typo": {config: "limits:\n  attempt: 3\n", finds: []want{{
			CodeConfigUnknownKey,
			"limits.attempt is not a key of limits:, so it limits nothing — the keys are budget, attempts, attempts_total, replans, consecutive_blocks, context_relay, relays, usage: fix the name, or drop it",
		}}},
		"a top-level effort that is not a level": {config: "effort: extreme\n", finds: []want{{
			CodeConfigInvalid,
			`effort: "extreme" is not a level, and every session ape launches would refuse it — use one of low, medium, high, xhigh, max, or drop it for Claude Code's own default`,
		}}},
		"a models entry effort that is not a level": {config: "models:\n  apex-apply: {model: sonnet, effort: huge}\n", finds: []want{{
			CodeConfigInvalid,
			`models.apex-apply.effort: "huge" is not a level, and apex-apply's session would refuse to launch`,
		}}},
		"an unknown top-level key": {config: "project_name: x\nlimit:\n  attempts: 3\n", notices: []want{{
			CodeConfigUnknownTopKey,
			"limit is not a key ape reads in _apex/config.yaml, so it does nothing",
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			files := baseFiles()
			files["_apex/config.yaml"] = tc.config
			p := fixture(t, files)
			_, err := Open(p.Root)
			require.NoError(t, err, "a config an older ape accepted keeps opening")
			var got []Finding
			for _, f := range ClassifyFindings(p.Check()) {
				if f.Code == CodeConfigUnknownKey || f.Code == CodeConfigInvalid {
					got = append(got, f)
				}
			}
			require.Len(t, got, len(tc.finds), "%v", got)
			for i, w := range tc.finds {
				require.Equal(t, w.code, got[i].Code)
				require.Equal(t, "_apex/config.yaml", got[i].File)
				require.Contains(t, got[i].Message+" — "+got[i].Fix, w.msg)
				require.Equal(t, ActionPerson, ActionFor(got[i].Code))
			}
			var notes []Diagnostic
			for _, d := range p.Notices() {
				if d.Code == CodeConfigUnknownTopKey {
					notes = append(notes, d)
				}
			}
			require.Len(t, notes, len(tc.notices), "%v", notes)
			for i, w := range tc.notices {
				require.Equal(t, SeverityNotice, notes[i].Severity)
				require.Contains(t, notes[i].Message, w.msg)
				require.NotZero(t, notes[i].Line)
			}
		})
	}
}
