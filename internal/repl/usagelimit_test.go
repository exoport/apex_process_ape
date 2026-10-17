package repl

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Synthetic panes built from Claude Code 2.1.302's templates (see
// usagelimit.go). Unmeasured live: no captured screen of a real limit.
func TestParseUsageLimit(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	require.NoError(t, err)
	now := time.Date(2026, 10, 16, 22, 0, 0, 0, madrid)
	pane := func(line string) string {
		return "╭───╮\n│ > fix the tests │\n╰───╯\n\n⏺ Running the suite…\n\n  " + line + "\n\n> \n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	}
	for _, tc := range []struct {
		name, line, resets string
		at                 time.Time
	}{
		{
			"session limit, today", "⎿  You've hit your usage limit · resets 11pm (Europe/Madrid)", "11pm (Europe/Madrid)",
			time.Date(2026, 10, 16, 23, 0, 0, 0, madrid),
		},
		{
			"a time already past is tomorrow's", "You've hit your limit · resets 3:30pm (Europe/Madrid) · progress saved", "3:30pm (Europe/Madrid)",
			time.Date(2026, 10, 17, 15, 30, 0, 0, madrid),
		},
		{
			"weekly, with a date", "You've hit your usage limit · resets Oct 20, 9am (UTC)", "Oct 20, 9am (UTC)",
			time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC),
		},
		{
			"next year", "You’ve hit your usage limit · resets Jan 2, 2027, 1am (UTC)", "Jan 2, 2027, 1am (UTC)",
			time.Date(2027, 1, 2, 1, 0, 0, 0, time.UTC),
		},
		{
			"auto-continue armed", "Usage limit reached · limit resets 11pm", "11pm",
			time.Date(2026, 10, 16, 23, 0, 0, 0, madrid),
		},
		{"no reset named", "You've hit your org's monthly usage limit", "", time.Time{}},
		{"an unparsed reset is kept as text", "You've hit your usage limit · resets UTC midnight", "UTC midnight", time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ul, ok := ParseUsageLimit(pane(tc.line), now)
			require.True(t, ok)
			require.Equal(t, tc.resets, ul.Resets)
			require.True(t, tc.at.Equal(ul.ResetsAt), "resets at %v, want %v", ul.ResetsAt, tc.at)
		})
	}
}

// The same words inside prose, a quoted string or the grace-window notice
// are not the limit screen.
func TestParseUsageLimit_NotAStop(t *testing.T) {
	for _, line := range []string{
		"⏺ The message reads You've hit your usage limit · resets 3pm when it happens.",
		`	pane := "You've hit your usage limit · resets 3pm"`,
		"[Usage limit reached — grace window active. Checkpoint now: finish the current step]",
		"  Continue automatically at usage limit   true",
		"⏺ Usage limit reached earlier today, so I paused.",
	} {
		_, ok := ParseUsageLimit("> \n"+line+"\n", time.Now())
		require.False(t, ok, line)
	}
}
