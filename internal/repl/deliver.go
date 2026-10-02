package repl

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// TypedLineBudget is the longest line ape types into claude as-is.
//
// Claude Code treats a burst of more than 800 characters arriving at once
// as a paste: the input box shows a "[Pasted text #N]" placeholder, and a
// submitted placeholder is never parsed as a slash command, so the skill
// body is never injected. Measured on 2.1.285 and 2.1.287 through this
// package's own write path: 799 characters expanded, 801 arrived as a
// plain user message. Under load (eight sessions on one host), lines from
// 750 characters were also seen never submitting at all. 700 stays clear
// of both.
//
// Callers with free text that would push a line past the budget write it
// to a file and type ArgsPointer instead.
const TypedLineBudget = 700

// FitsTypedLine reports whether line is within TypedLineBudget, counted in
// characters, as Claude Code counts them, not bytes.
func FitsTypedLine(line string) bool {
	return utf8.RuneCountInString(line) <= TypedLineBudget
}

// ArgsPointer is what ape types in place of arguments it wrote to path.
// Absolute, like `--handoff`'s pointer, so it resolves whatever the
// session's working directory.
func ArgsPointer(path string) string {
	return "The arguments for this run are in " + path +
		": read that file and treat its contents as if they were written here."
}

// pastedPlaceholder is the input box's stand-in for a collapsed paste.
const pastedPlaceholder = "[Pasted text #"

// typedRenderTimeout bounds how long SendCommand waits for the typed text
// to appear before pressing Enter anyway, and typedRenderPoll is how often
// it looks. Past the timeout it presses Enter regardless: that is the old
// fixed-delay behaviour, only later, and Deliver's confirmation catches a
// line that still did not submit. Vars so a test can shorten them.
var (
	typedRenderTimeout = 10 * time.Second
	typedRenderPoll    = 50 * time.Millisecond
)

// sendTextFn is SendText behind a test seam, like capturePaneFn and
// sendEnterFn.
var sendTextFn = SendText

// typeLine writes text and waits until claude has drawn it, so that the
// Enter that follows arrives in a read of its own.
//
// The wait used to be a fixed PromptSettle. On a starved host claude had
// not read the PTY within it, so the text and the '\r' came out of the
// kernel buffer in ONE read; Claude Code classified that chunk as a paste,
// and inside a paste a CR is a newline, not Enter. The line sat in the
// input box until the idle timeout. Now Enter waits for the text, or a
// "[Pasted text" placeholder, to show in the pane, and never earlier than
// PromptSettle after the write. shown reports whether it was seen before
// typedRenderTimeout.
func typeLine(ctx context.Context, name, text string) (shown bool, err error) {
	before, _ := capturePaneFn(ctx, name)
	key := typedKey(text)
	baseKey := countSquashed(before, key)
	basePaste := strings.Count(before, pastedPlaceholder)

	if err := sendTextFn(ctx, name, text); err != nil {
		return false, err
	}
	wrote := time.Now()
	for {
		pane, _ := capturePaneFn(ctx, name)
		shown = (key != "" && countSquashed(pane, key) > baseKey) ||
			strings.Count(pane, pastedPlaceholder) > basePaste
		elapsed := time.Since(wrote)
		if (shown && elapsed >= PromptSettle) || elapsed >= typedRenderTimeout {
			return shown, nil
		}
		select {
		case <-ctx.Done():
			return shown, ctx.Err()
		case <-time.After(typedRenderPoll):
		}
	}
}

// typedKey is the tail of text the pane is searched for: its last 24
// non-space characters. The tail, because a long line wraps over several
// rows and the input box keeps its end in view; spaces removed, because
// the wrap breaks the text wherever it likes and indents the next row.
func typedKey(text string) string {
	r := []rune(squashSpace(text))
	if len(r) > 24 {
		r = r[len(r)-24:]
	}
	return string(r)
}

// countSquashed counts key in pane once both are stripped of whitespace.
// Counted rather than found: the same text may already be on screen from
// an earlier step, and only a NEW occurrence means this write was drawn.
func countSquashed(pane, key string) int {
	if key == "" {
		return 0
	}
	return strings.Count(squashSpace(pane), key)
}

func squashSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// SubmitProbe reports how many prompts claude has submitted so far, from
// its UserPromptSubmit hook, and the text of the most recent one.
type SubmitProbe func() (count uint64, last string)

// submitConfirmTimeout is how long Deliver waits for the UserPromptSubmit
// hook after each Enter, and submitRetries how many more times it presses
// Enter when none arrives. The hook is a spawned process, so a loaded
// host delays it; an extra Enter on an empty input box does nothing.
// Vars so a test can shorten them.
var (
	submitConfirmTimeout = 20 * time.Second
	submitRetries        = 2
	submitPoll           = 100 * time.Millisecond
)

// NotSubmittedError is a typed line claude never submitted: no
// UserPromptSubmit hook followed it, through every Enter Deliver pressed.
// Without this the run idled to its timeout with the line sitting in the
// input box, and the record said only "idle".
type NotSubmittedError struct {
	Enters int
	Waited time.Duration
	// Shown is whether the typed text, or a paste placeholder, was ever
	// seen in the pane: false means claude never drew it at all.
	Shown bool
	// Input is the pane's input row at the end, for the record.
	Input string
}

func (e *NotSubmittedError) Error() string {
	drawn := "the typed line was drawn in the input box"
	if !e.Shown {
		drawn = "the typed line was never seen in the pane"
	}
	msg := fmt.Sprintf("claude never submitted the typed line: no UserPromptSubmit hook after %d Enter press(es) over %s; %s",
		e.Enters, e.Waited.Round(time.Second), drawn)
	if e.Input != "" {
		msg += "; input row: " + e.Input
	}
	return msg
}

// Deliver types line, presses Enter once claude has drawn it, and — when
// probe is non-nil — confirms the submit through the UserPromptSubmit
// hook, pressing Enter again up to submitRetries times before returning a
// *NotSubmittedError.
//
// A submit of "/clear" does not count: ape types it between steps, and
// its hook can land after the probe's baseline was taken. Nothing else is
// matched on, because the hook's text for a collapsed paste need not be
// the text ape typed.
func Deliver(ctx context.Context, name, line string, probe SubmitProbe) error {
	var base uint64
	if probe != nil {
		base, _ = probe()
	}
	start := time.Now()
	shown, err := typeLine(ctx, name, line)
	if err != nil {
		return err
	}
	if err := sendEnterFn(ctx, name); err != nil {
		return err
	}
	if probe == nil {
		return nil
	}
	for enters := 1; ; enters++ {
		ok, err := awaitSubmit(ctx, probe, base)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if enters > submitRetries {
			pane, _ := capturePaneFn(ctx, name)
			return &NotSubmittedError{Enters: enters, Waited: time.Since(start), Shown: shown, Input: inputRow(pane)}
		}
		if err := sendEnterFn(ctx, name); err != nil {
			return err
		}
	}
}

// awaitSubmit waits up to submitConfirmTimeout for a submit after base
// that is not a /clear.
func awaitSubmit(ctx context.Context, probe SubmitProbe, base uint64) (bool, error) {
	deadline := time.Now().Add(submitConfirmTimeout)
	for {
		if n, last := probe(); n > base && strings.TrimSpace(last) != "/clear" {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(submitPoll):
		}
	}
}

// inputRowCap bounds the input row quoted in a NotSubmittedError.
const inputRowCap = 160

// inputRow returns the pane's last input-box row ("❯ …"), trimmed and
// capped, or "" when there is none.
func inputRow(pane string) string {
	for _, l := range slices.Backward(strings.Split(pane, "\n")) {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "❯") {
			if r := []rune(l); len(r) > inputRowCap {
				l = string(r[:inputRowCap]) + "…"
			}
			return l
		}
	}
	return ""
}
