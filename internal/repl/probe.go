package repl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ProbeVerdict is what a startup probe concluded about the installed claude.
type ProbeVerdict string

const (
	// ProbeVerified: claude reached an input-accepting REPL through the
	// trust dialog, showing both ready signals ape reads.
	ProbeVerified ProbeVerdict = "verified"
	// ProbeBroken: claude drew a screen ape could not get past, or a REPL
	// missing a signal ape depends on. Every run on this claude will meet it.
	ProbeBroken ProbeVerdict = "broken"
	// ProbeUndetermined: the probe could not reach a verdict — claude drew
	// nothing before the deadline (offline, not logged in, a slow start), or
	// could not be started at all. Not a pass: nothing is cached.
	ProbeUndetermined ProbeVerdict = "undetermined"
)

// ProbeResult is the probe's verdict and the evidence for it.
type ProbeResult struct {
	Verdict ProbeVerdict
	// Detail is the reason, written for a person.
	Detail string
	// Pane is the rendered screen at the verdict; Output the raw PTY bytes
	// behind it (see tailBuffer).
	Pane   string
	Output []byte
}

// probeTimeout bounds the whole probe. claude's REPL is up in under five
// seconds on a warm machine; the rest is headroom for a cold start.
const probeTimeout = 45 * time.Second

// probeFooterWait is how long a ready REPL gets to paint its footer:
// WaitForReady returns on the first ready frame, and the footer lands a beat
// after it.
const probeFooterWait = 5 * time.Second

// ProbeClaude runs the installed claude through ape's startup contract,
// spending no tokens: spawned exactly as a run spawns it, in a fresh
// directory it has never trusted — so the trust dialog is the first screen
// and the walk is exercised — then required to reach a REPL showing both
// ready signals replReady reads.
//
// Resilience remedy R3's PTY half. A claude auto-update that breaks this
// contract used to be found by a run stalling mid-pipeline; the 2.1.269 one
// sat unnoticed for two days after it installed. This is what lets ape find
// it on first use of the new version instead, in seconds, with the pane and
// the bytes in hand.
func ProbeClaude(ctx context.Context, claudeBin string) ProbeResult {
	dir, err := os.MkdirTemp("", "ape-claude-probe-")
	if err != nil {
		return ProbeResult{Verdict: ProbeUndetermined, Detail: "could not create a scratch directory: " + err.Error()}
	}
	defer func() { _ = os.RemoveAll(dir) }()

	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	name := fmt.Sprintf("ape-claude-probe-%d", os.Getpid())
	_ = KillSession(pctx, name)
	if err := NewSession(pctx, name, dir, []string{claudeBin, "--dangerously-skip-permissions"}); err != nil {
		return ProbeResult{Verdict: ProbeUndetermined, Detail: "could not start claude: " + err.Error()}
	}
	defer func() { _ = KillSession(context.Background(), name) }() //nolint:contextcheck // cleanup must outlive a cancelled probe

	readyErr := WaitForReady(pctx, name)
	pane, _ := CapturePane(pctx, name)
	res := ProbeResult{Pane: pane, Output: RecentOutput(name)}
	if readyErr != nil {
		res.Verdict, res.Detail = judgeNotReady(ctx.Err(), HasSession(ctx, name), pane, readyErr)
		return res
	}

	// Ready. Both signals replReady reads, each asserted on its own —
	// WaitForReady accepts either, so a dead fallback would otherwise hide
	// behind a working footer until the footer moved too. That is not
	// hypothetical: the first draft of this probe declared a working claude
	// 2.1.270 broken, because its prompt line is `❯` plus U+00A0 and
	// emptyPromptRe did not accept the no-break space. The live
	// startup_probe subtest caught the probe; the probe caught the regex.
	var grid *screen
	if s, ok := lookup(name); ok {
		grid = s.screen
	}
	deadline := time.Now().Add(probeFooterWait)
	for {
		footer, prompt := probeSignals(pane, grid)
		if footer && prompt {
			res.Verdict, res.Pane = ProbeVerified, pane
			return res
		}
		if time.Now().After(deadline) {
			res.Verdict, res.Pane = ProbeBroken, pane
			switch {
			case !footer:
				res.Detail = "the REPL came up without the `" + readyFooter + "` footer, the primary ready " +
					"signal — ape is running on its ❯ fallback alone"
			default:
				res.Detail = "the REPL came up without an empty ❯ prompt line, the fallback ready signal"
			}
			res.Output = RecentOutput(name)
			return res
		}
		time.Sleep(ReadyPollInterval)
		pane, _ = CapturePane(pctx, name)
	}
}

// probeSignals reads the two ready signals off a ready REPL: the footer, and
// an empty ❯ prompt line. The prompt is empty when emptyPromptRe matches the
// pane, or when the grid's ❯ row holds nothing typed — only blank or faint
// cells, and claude's own cursor (see screen.idlePrompt). claude sometimes
// draws a faint placeholder hint there (`❯ Try "refactor <filepath>"`), and a
// probe reading only the text declared that working claude broken,
// nondeterministically, on the first run after an update (E-R7e-1). Probe
// only: replReady and emptyPromptRe's other callers are unchanged.
func probeSignals(pane string, grid *screen) (footer, prompt bool) {
	footer = strings.Contains(pane, readyFooter)
	prompt = emptyPromptRe.MatchString(pane) || (grid != nil && grid.idlePrompt())
	return footer, prompt
}

// judgeNotReady classifies a readiness failure: a claude ape could not
// drive, or a probe that could not tell.
func judgeNotReady(interrupted error, alive bool, pane string, readyErr error) (verdict ProbeVerdict, detail string) {
	if interrupted != nil {
		return ProbeUndetermined, "the probe was interrupted"
	}
	nr, typed := errors.AsType[*NotReadyError](readyErr)
	timedOut := typed && errors.Is(nr.Err, context.DeadlineExceeded)
	switch {
	case !typed:
		return ProbeBroken, readyErr.Error()
	case !timedOut:
		// A modal ape recognised and could not dismiss — the trust walk.
		return ProbeBroken, nr.Err.Error()
	case strings.TrimSpace(pane) == "":
		return ProbeUndetermined, "claude drew nothing before the deadline — offline, not logged in, or a slow start"
	case !alive:
		return ProbeBroken, "claude exited before its REPL was ready"
	default:
		return ProbeBroken, "claude's first screen never became a ready REPL, and it is not a screen ape knows how to get past"
	}
}
