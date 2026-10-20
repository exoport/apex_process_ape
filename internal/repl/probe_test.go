package repl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The verdict rules. The distinction that matters is BROKEN versus
// UNDETERMINED: a broken claude stops the run and a probe that could not
// tell does not — so "claude drew nothing" (offline, not logged in) must
// never be reported as a claude ape cannot drive, and a screen ape cannot
// get past must never be waved through as "could not tell".
func TestJudgeNotReady(t *testing.T) {
	t.Parallel()
	timeout := func(pane string) error {
		return &NotReadyError{Name: "p", Pane: pane, Err: context.DeadlineExceeded}
	}
	for _, tc := range []struct {
		name        string
		interrupted error
		alive       bool
		pane        string
		err         error
		want        ProbeVerdict
		detail      string
	}{
		{
			"a trust walk that could not finish", nil, true, "❯ No, exit",
			&NotReadyError{Err: errors.New("repl: dismiss trust-folder modal: could not reach a trust-granting option")},
			ProbeBroken, "could not reach a trust-granting option",
		},
		{"nothing drawn before the deadline", nil, true, "  \n ", timeout(""), ProbeUndetermined, "drew nothing"},
		{"claude exited on a screen", nil, false, "Invalid API key", timeout("Invalid API key"), ProbeBroken, "exited before"},
		{
			"an unknown screen that never became a REPL", nil, true, "Choose a theme", timeout("Choose a theme"), ProbeBroken,
			"not a screen ape knows how to get past",
		},
		{"the probe was cancelled", context.Canceled, true, "Choose a theme", timeout("Choose a theme"), ProbeUndetermined, "interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verdict, detail := judgeNotReady(tc.interrupted, tc.alive, tc.pane, tc.err)
			require.Equal(t, tc.want, verdict)
			require.Contains(t, detail, tc.detail)
		})
	}
}

// probeHintCaptures are claude startup probes whose REPL came up with a
// placeholder hint on the prompt line, raw PTY output, not written by hand;
// only user@host and the user name in the scratch path are replaced, byte for
// byte (`person@host`, `-home-person--`).
//
//   - 2.1.294, the probe that declared a working claude broken (E-R7e-1):
//     `❯` U+00A0, then the hint FAINT — SGR 2 `Try "refactor <filepath>"`.
//   - 2.1.304, captured under tmux with the hint forced on
//     (CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=1, on the probe's spawn only):
//     claude's own cursor drawn reverse video over the hint's first
//     character — SGR 7 `T` SGR 27, SGR 2 `ry "how does <filepath> work?"`.
var probeHintCaptures = []struct{ file, hint string }{
	{"claude-2.1.294-probe-hint.bin", "Try \"refactor <filepath>\""},
	{"claude-2.1.304-probe-hint-cursor.bin", "Try \"how does <filepath> work?\""},
}

func readProbeHintCapture(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", file))
	require.NoError(t, err)
	return b
}

// The probe's judgement on the real bytes is verified: both ready signals,
// the prompt one through the cells, since the text is not empty. It fails on
// the code before E-R7e-1, which read the text alone.
func TestProbeSignals_AHintIsAnEmptyPrompt(t *testing.T) {
	t.Parallel()
	for _, c := range probeHintCaptures {
		t.Run(c.file, func(t *testing.T) {
			t.Parallel()
			stream := readProbeHintCapture(t, c.file)
			s := newScreen()
			defer s.close()
			s.write(stream)
			pane := s.text()

			require.Contains(t, pane, ReadyGlyph+"\u00a0"+c.hint, "the capture shows the hint on the prompt line")
			require.False(t, emptyPromptRe.MatchString(pane), "the text alone is not an empty prompt — the premise of the fixture")
			footer, prompt := probeSignals(pane, s)
			require.True(t, footer, "pane:\n%s", pane)
			require.True(t, prompt, "a ❯ line holding only a hint is an empty prompt; pane:\n%s", pane)
			require.True(t, replReady(pane), "replReady is unchanged: the footer alone")

			// Every captured fixture renders the same in the oracle (R4a).
			oracle := newVT10xOracle()
			oracle.write(stream)
			require.Equal(t, oracle.text(), pane, "x/vt and vt10x+filter render the capture differently")
			require.Equal(t, paneReadsOf(oracle.text()), paneReadsOf(pane))
		})
	}
}

// A PTY read can end anywhere: the cells read the same however the bytes
// were split.
func TestProbeSignals_AHintSplitAcrossReads(t *testing.T) {
	t.Parallel()
	for _, c := range probeHintCaptures {
		stream := readProbeHintCapture(t, c.file)
		for cut := 1; cut < len(stream); cut += 7 {
			s := newScreen()
			s.write(stream[:cut])
			s.write(stream[cut:])
			_, prompt := probeSignals(s.text(), s)
			s.close()
			require.True(t, prompt, "%s: cut at byte %d", c.file, cut)
		}
	}
}

// What the rule must not accept: anything typed — drawn in the normal weight
// after the ❯, or a lone reverse-video character, which is one typed with the
// cursor on it — and a menu row. And what it shares with emptyPromptRe: a
// blank prompt line.
func TestProbeSignals_OnlyBlankOrFaintCellsAreEmpty(t *testing.T) {
	t.Parallel()
	const footerRow = "\r\n\x1b[3G\x1b[38;5;211m⏵⏵ bypass permissions on\x1b[39m"
	for _, tc := range []struct {
		name, row string
		empty     bool
	}{
		{"a blank prompt line", "❯\u00a0", true},
		{"a blank prompt line with claude's cursor", "❯\u00a0\x1b[7m \x1b[27m", true},
		{"a faint hint", "❯\u00a0\x1b[2mTry\x1b[7G\"something new\"\x1b[22m", true},
		{"indented, a faint hint", "  ❯ \x1b[2mhint\x1b[22m", true},
		{"claude's cursor over a faint hint", "❯\u00a0\x1b[7mT\x1b[27m\x1b[2mry this\x1b[22m", true},
		{"typed text", "❯\u00a0hello", false},
		{"typed text, claude's cursor after it", "❯\u00a0hello\x1b[7m \x1b[27m", false},
		{"typed text before a faint tail", "❯\u00a0h\x1b[2mint\x1b[22m", false},
		{"one typed character under claude's cursor", "❯\u00a0\x1b[7mh\x1b[27m", false},
		{"typed text under and after claude's cursor", "❯\u00a0\x1b[7mh\x1b[27mello", false},
		{"a reverse cell that is not where the cursor sits", "❯\u00a0\x1b[2mTry\x1b[22m \x1b[7mx\x1b[27m", false},
		{"a menu row", "\x1b[38;5;153m❯\x1b[4G1. Yes, I trust this folder\x1b[39m", false},
		{"a bold hint is not faint", "❯\u00a0\x1b[1mTry this\x1b[22m", false},
		{"no prompt at all", "Choose a theme", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newScreen()
			defer s.close()
			s.write([]byte("\x1b[1;1H" + tc.row + footerRow))
			footer, prompt := probeSignals(s.text(), s)
			require.True(t, footer)
			require.Equal(t, tc.empty, prompt, "pane:\n%s", s.text())
		})
	}
}
