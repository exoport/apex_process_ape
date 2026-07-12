package framework

import (
	"errors"
	"fmt"
	"strings"
)

// Managed-block markers for the repo-root CLAUDE.md (PLAN-47 Workstream
// C). ape owns everything between these two lines; content outside them
// is the user's and is preserved byte-for-byte across setup/update.
// HTML comments so the markers stay invisible in rendered Markdown.
const (
	ManagedBlockBegin = "<!-- apex:managed:begin -->"
	ManagedBlockEnd   = "<!-- apex:managed:end -->"
)

// OperatingRulesImport is the managed-block body ape writes into the
// repo-root CLAUDE.md: a Claude Code @import of the framework-maintained
// operating-rules fragment, so every session in the project loads the
// APEX discipline rules. The @import path is repo-root-relative, which is
// why the block must live in the repo-root CLAUDE.md.
const OperatingRulesImport = "@" + ProjectOperatingRules

// ErrMalformedManagedBlock signals the target file has unbalanced
// apex:managed markers (a begin without an end, an end without a begin,
// or a nested begin). ape refuses to edit such a file rather than risk
// mangling user content — the human must fix the markers first.
var ErrMalformedManagedBlock = errors.New("malformed apex:managed block markers")

// managedRegion is one begin→end marker pair, by logical-line index.
type managedRegion struct{ begin, end int }

// UpsertManagedBlock returns content with the apex:managed block set to
// body, preserving everything outside the markers. It is idempotent:
// UpsertManagedBlock(UpsertManagedBlock(x)) == UpsertManagedBlock(x), and
// the second call reports changed=false.
//
// Behavior by prior state:
//   - absent/blank content → a minimal file containing only the block;
//   - no block yet → the block is appended after existing content with a
//     single blank separator line;
//   - exactly one block → its body is replaced in place (bytes before and
//     after the markers are preserved);
//   - duplicate blocks → collapsed to a single fresh block at the end,
//     preserving every non-block line;
//   - unbalanced markers → ErrMalformedManagedBlock, content untouched.
//
// The file's newline convention (LF or CRLF) and trailing-newline state
// are preserved for uniformly-terminated files.
func UpsertManagedBlock(existing []byte, body string) (out []byte, changed bool, err error) {
	raw := string(existing)

	// Absent/blank file → a minimal file containing only the block.
	if strings.TrimSpace(raw) == "" {
		nl := newlineOf(raw)
		res := strings.Join(blockLines(body), nl) + nl
		return []byte(res), res != raw, nil
	}

	lines, nl := splitLogicalLines(raw)
	regions, err := findManagedRegions(lines)
	if err != nil {
		return nil, false, err
	}

	blk := blockLines(body)
	var newLines []string
	switch len(regions) {
	case 0:
		newLines = appendBlock(lines, blk)
	case 1:
		r := regions[0]
		newLines = append(newLines, lines[:r.begin]...)
		newLines = append(newLines, blk...)
		newLines = append(newLines, lines[r.end+1:]...)
	default:
		// Duplicate blocks — drop every managed region (preserving all
		// non-region lines) and append one fresh block at the end.
		keep := make([]string, 0, len(lines))
		for i, l := range lines {
			if !inAnyRegion(i, regions) {
				keep = append(keep, l)
			}
		}
		newLines = appendBlock(keep, blk)
	}

	res := strings.Join(newLines, nl)
	return []byte(res), res != raw, nil
}

// FindManagedBlock returns the body between the first begin/end marker
// pair. ok is false when no block is present; err is
// ErrMalformedManagedBlock when the markers are unbalanced.
func FindManagedBlock(content []byte) (body string, ok bool, err error) {
	lines, _ := splitLogicalLines(string(content))
	regions, err := findManagedRegions(lines)
	if err != nil {
		return "", false, err
	}
	if len(regions) == 0 {
		return "", false, nil
	}
	r := regions[0]
	return strings.Join(lines[r.begin+1:r.end], "\n"), true, nil
}

// appendBlock returns lines (with trailing blank lines trimmed) followed
// by a single blank separator, the block, and a final trailing newline.
func appendBlock(lines, blk []string) []string {
	core := trimTrailingEmpty(lines)
	out := make([]string, 0, len(core)+len(blk)+2)
	out = append(out, core...)
	if len(core) > 0 {
		out = append(out, "") // blank separator before the block
	}
	out = append(out, blk...)
	out = append(out, "") // trailing newline
	return out
}

// blockLines renders the begin marker, the body (split into lines), and
// the end marker as a slice of logical lines.
func blockLines(body string) []string {
	out := []string{ManagedBlockBegin}
	out = append(out, strings.Split(body, "\n")...)
	out = append(out, ManagedBlockEnd)
	return out
}

// findManagedRegions pairs begin/end markers via a single scan. A marker
// is matched only when it is the sole non-whitespace content of a line.
func findManagedRegions(lines []string) ([]managedRegion, error) {
	var regions []managedRegion
	open := -1
	for i, l := range lines {
		switch strings.TrimSpace(l) {
		case ManagedBlockBegin:
			if open != -1 {
				return nil, fmt.Errorf("%w: nested begin marker at line %d", ErrMalformedManagedBlock, i+1)
			}
			open = i
		case ManagedBlockEnd:
			if open == -1 {
				return nil, fmt.Errorf("%w: end marker without a begin at line %d", ErrMalformedManagedBlock, i+1)
			}
			regions = append(regions, managedRegion{begin: open, end: i})
			open = -1
		}
	}
	if open != -1 {
		return nil, fmt.Errorf("%w: begin marker without an end", ErrMalformedManagedBlock)
	}
	return regions, nil
}

func inAnyRegion(i int, regions []managedRegion) bool {
	for _, r := range regions {
		if i >= r.begin && i <= r.end {
			return true
		}
	}
	return false
}

// splitLogicalLines splits raw into logical lines (newline stripped) and
// returns the newline style to rejoin with. A uniformly LF- or
// CRLF-terminated file round-trips exactly through strings.Join(lines, nl).
func splitLogicalLines(raw string) (lines []string, nl string) {
	nl = newlineOf(raw)
	parts := strings.Split(raw, "\n")
	for i := range parts {
		parts[i] = strings.TrimSuffix(parts[i], "\r")
	}
	return parts, nl
}

func newlineOf(raw string) string {
	if strings.Contains(raw, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func trimTrailingEmpty(lines []string) []string {
	end := len(lines)
	for end > 0 && lines[end-1] == "" {
		end--
	}
	return lines[:end]
}
