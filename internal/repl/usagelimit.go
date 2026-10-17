package repl

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Claude Code's usage-limit screen (H20, D47 Q7). When a claude.ai plan's
// session or weekly limit stops a session, the REPL draws one of two lines
// and the turn stops; nothing reaches the hooks or the transcript ape
// watches, so before this the run sat until the idle ceiling.
//
// The text, read from the strings of the installed binary, Claude Code
// 2.1.302 (`claude --version`):
//
//	lt(e,r,n,s) → `You've hit your ${e}${r}${h}`      e: "usage limit" | "limit" |
//	                                                     "org's monthly usage limit" | …
//	                                                  r: ` · resets ${IA(resetsAt,true)}`
//	                                                  h: " · progress saved" (optional)
//	RVr(a)      → `Usage limit reached · limit resets ${eP(a)}`
//	                                                  (autoContinueAtUsageLimit armed)
//
// and IA/eP format the reset as "3pm" / "3:30pm" within 24 hours, else
// "Oct 18, 9am" (", 2027" when the year differs), followed by " (<IANA
// zone>)" when the zone is named. Unmeasured live: no session here has hit
// a limit under ape, so the patterns are the binary's templates, tested
// against synthetic panes (usagelimit_test.go), not a captured screen.
//
// Matched only as a whole pane line, after the glyphs Claude Code prefixes
// a message with: the same words inside a model's prose or a file it shows
// carry other text on the line. The "grace window" notice the model is sent
// before the limit ("[Usage limit reached — grace window active. …") is not
// a stop and does not match.
var usageLimitLineRe = regexp.MustCompile(`^[\s⎿●⏺✻✗✶✳│>*·-]*` +
	`(You(?:'|’)ve hit your [^·\[\]]*?(?:limit|budget)|You(?:'|’)re out of usage credits|Usage limit reached)` +
	`((?: · [^·]+)*)\s*$`)

// usageLimitResetsRe finds the reset in the line's " · …" tail.
var usageLimitResetsRe = regexp.MustCompile(`· (?:limit )?resets (?:at )?([^·]+?)\s*(?:·|$)`)

// UsageLimit is a usage-limit screen read from a pane.
type UsageLimit struct {
	// Line is the pane line, trimmed.
	Line string
	// Resets is the reset as Claude Code printed it ("3pm (Europe/Madrid)"),
	// "" when the line names none.
	Resets string
	// ResetsAt is Resets parsed, zero when it does not parse.
	ResetsAt time.Time
}

// ParseUsageLimit returns the usage-limit line of pane, if it shows one.
// now anchors a reset printed without a date (the next such time).
func ParseUsageLimit(pane string, now time.Time) (UsageLimit, bool) {
	for _, line := range slices.Backward(strings.Split(pane, "\n")) {
		line = strings.TrimSpace(line)
		m := usageLimitLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ul := UsageLimit{Line: line}
		if r := usageLimitResetsRe.FindStringSubmatch(m[2] + " "); r != nil {
			ul.Resets = strings.TrimSpace(r[1])
			ul.ResetsAt = parseResets(ul.Resets, now)
		}
		return ul, true
	}
	return UsageLimit{}, false
}

var (
	resetsClockRe = regexp.MustCompile(`^(?:([A-Z][a-z]{2}) (\d{1,2}), (?:(\d{4}), )?)?(\d{1,2})(?::(\d{2}))?\s*([ap]m)(?: \(([^)]+)\))?$`)
	months        = map[string]time.Month{
		"Jan": time.January, "Feb": time.February, "Mar": time.March, "Apr": time.April,
		"May": time.May, "Jun": time.June, "Jul": time.July, "Aug": time.August,
		"Sep": time.September, "Oct": time.October, "Nov": time.November, "Dec": time.December,
	}
)

// parseResets reads Claude Code's reset format: "3pm", "3:30pm", "Oct 18,
// 9am", "Oct 18, 2027, 9am", each optionally followed by " (<zone>)". A
// time without a date is its next occurrence after now; a date without a
// year is this year's, or next year's when that is long past. Zero when it
// does not parse.
func parseResets(s string, now time.Time) time.Time {
	m := resetsClockRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return time.Time{}
	}
	loc := now.Location()
	if m[7] != "" {
		l, err := time.LoadLocation(m[7])
		if err != nil {
			return time.Time{}
		}
		loc = l
	}
	hour, _ := strconv.Atoi(m[4])
	minute, _ := strconv.Atoi(m[5])
	if hour < 1 || hour > 12 || minute > 59 {
		return time.Time{}
	}
	hour %= 12
	if m[6] == "pm" {
		hour += 12
	}
	local := now.In(loc)
	if m[1] == "" {
		t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
		if !t.After(local) {
			t = t.AddDate(0, 0, 1)
		}
		return t
	}
	month, ok := months[m[1]]
	day, _ := strconv.Atoi(m[2])
	if !ok || day < 1 || day > 31 {
		return time.Time{}
	}
	year := local.Year()
	if m[3] != "" {
		year, _ = strconv.Atoi(m[3])
	}
	t := time.Date(year, month, day, hour, minute, 0, 0, loc)
	if m[3] == "" && t.Before(local.AddDate(0, -6, 0)) {
		t = t.AddDate(1, 0, 0)
	}
	return t
}
