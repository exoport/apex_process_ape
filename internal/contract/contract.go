// Package contract implements the terminal-contract check: did a run
// actually reach its summary step, or did it exit having silently done
// nothing?
//
// Some APEX skills end a run with a machine-readable return block — a
// story-batch skill's `run_status:`, an epic-batch review's `epics:`.
// A run that exits without one never reached its summary, whatever its
// exit code said. That makes the block's presence a mechanism-independent
// completion signal: it holds no matter how the run went wrong, and it
// does not depend on any field of Claude Code's hook payload.
//
// ape deliberately does NOT know the word `run_status`. The vocabulary is
// framework-owned, declared per skill in `_apex/terminal-contracts.csv`,
// so the framework can enrol a skill or change a pattern without an ape
// release. ape supplies only the mechanism.
package contract

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// TableFile is the project-relative path of the framework-owned table.
// Installed by `ape framework setup|update`; absent on a project whose
// framework predates it, which disables the check entirely.
const TableFile = "_apex/terminal-contracts.csv"

// Table maps a skill name to the pattern that recognises its terminal
// contract. A nil or empty Table checks nothing — the degradation every
// absent input takes.
type Table struct {
	patterns map[string]*regexp.Regexp
	// Warnings records rows that could not be used (bad regex, wrong
	// column count). Surfaced once at load, never fatal: a malformed
	// framework file must not fail a user's run.
	Warnings []string
}

// Len reports how many skills are enrolled.
func (t *Table) Len() int {
	if t == nil {
		return 0
	}
	return len(t.patterns)
}

// Skills lists the enrolled skill names, sorted.
func (t *Table) Skills() []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.patterns))
	for s := range t.patterns {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Load reads the terminal-contract table from a project root.
//
// An absent file is not an error: it yields an empty Table that enrols
// nothing, so a project on an older framework behaves exactly as it did
// before this check existed. Only an unreadable-but-present file, or one
// that is not parseable as CSV at all, returns an error.
func Load(projectRoot string) (*Table, error) {
	path := filepath.Join(projectRoot, TableFile)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Table{patterns: map[string]*regexp.Regexp{}}, nil
		}
		return nil, fmt.Errorf("open %s: %w", TableFile, err)
	}
	defer f.Close()
	return parse(f)
}

// parse reads the CSV body. Format is two columns, `skill,pattern`, with
// an optional header row and `#` comment lines.
func parse(r io.Reader) (*Table, error) {
	t := &Table{patterns: map[string]*regexp.Regexp{}}
	cr := csv.NewReader(r)
	cr.Comment = '#'
	cr.FieldsPerRecord = -1 // validated per row so one bad row isn't fatal
	cr.TrimLeadingSpace = true

	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", TableFile, err)
	}
	for i, row := range rows {
		if len(row) < 2 {
			if len(row) == 1 && strings.TrimSpace(row[0]) == "" {
				continue // blank line
			}
			t.warnf("row %d: want 2 columns (skill,pattern), got %d — ignored", i+1, len(row))
			continue
		}
		skill := strings.TrimSpace(row[0])
		pattern := strings.TrimSpace(row[1])
		// Tolerate the header row without requiring it.
		if i == 0 && strings.EqualFold(skill, "skill") && strings.EqualFold(pattern, "pattern") {
			continue
		}
		if skill == "" || pattern == "" {
			t.warnf("row %d: empty skill or pattern — ignored", i+1)
			continue
		}
		// (?m) so `^` anchors per line: a contract block sits at the end of
		// a longer message, and every pattern the framework ships is
		// line-anchored. Go's regexp is RE2 — linear time, no catastrophic
		// backtracking — so compiling a pattern from a user-editable file
		// is not a denial-of-service vector and needs no sandboxing.
		re, err := regexp.Compile("(?m)" + pattern)
		if err != nil {
			t.warnf("skill %q: invalid pattern %q: %v — ignored", skill, pattern, err)
			continue
		}
		t.patterns[skill] = re
	}
	return t, nil
}

func (t *Table) warnf(format string, args ...any) {
	t.Warnings = append(t.Warnings, fmt.Sprintf(format, args...))
}

// Status is the outcome of checking one step against the table.
type Status int

const (
	// StatusNotEnrolled: the skill declares no terminal contract, so
	// there is nothing to check. The common case — most skills.
	StatusNotEnrolled Status = iota
	// StatusNoTranscript: enrolled, but no closing message could be read.
	// Reported, never treated as a violation: an unreadable transcript is
	// an ape-side gap, not evidence the run failed.
	StatusNoTranscript
	// StatusPresent: the contract was found. The run reached its summary.
	StatusPresent
	// StatusMissing: enrolled, a closing message was read, and it carries
	// no contract. The run did not reach its summary step.
	StatusMissing
)

// Token is the durable form of a status — what a manifest or an event
// stream records. Empty for StatusNotEnrolled, and that emptiness is the
// design, not an oversight.
//
// Recording "not-enrolled" would make the field mean different things on
// different projects. Check short-circuits an absent or empty table to
// StatusNotEnrolled, and so a project whose framework ships no table would
// record nothing while a project that enrolled one unrelated skill would
// record "not-enrolled" for every other skill it runs. The same step would
// carry two different values for the same reason. Omitting instead gives
// the field one invariant worth having: it is present exactly when the
// skill was enrolled, so a base rate is
//
//	present / (present + missing + no-transcript)
//
// over the rows that have one, with no denominator to reconstruct.
//
// Distinguishing "not enrolled" from "written by an ape that predates the
// field" is the manifest's job, not this token's: every manifest stamps
// ape_version.
func (s Status) Token() string {
	switch s {
	case StatusPresent:
		return "present"
	case StatusMissing:
		return "missing"
	case StatusNoTranscript:
		return "no-transcript"
	case StatusNotEnrolled:
		return ""
	}
	return ""
}

// Result reports one check.
type Result struct {
	Status  Status
	Skill   string
	Pattern string
}

// Check tests a step's closing message against the skill's declared
// contract. text is the last assistant message of the step's own
// transcript; ok reports whether one could be read at all.
func (t *Table) Check(skill, text string, ok bool) Result {
	if t == nil || skill == "" {
		return Result{Status: StatusNotEnrolled, Skill: skill}
	}
	re, enrolled := t.patterns[skill]
	if !enrolled {
		return Result{Status: StatusNotEnrolled, Skill: skill}
	}
	res := Result{Skill: skill, Pattern: re.String()}
	switch {
	case !ok || strings.TrimSpace(text) == "":
		res.Status = StatusNoTranscript
	case re.MatchString(text):
		res.Status = StatusPresent
	default:
		res.Status = StatusMissing
	}
	return res
}

// Diagnostic renders an operator-facing explanation for a result that is
// worth reporting, or "" when there is nothing to say.
func (r Result) Diagnostic() string {
	switch r.Status {
	case StatusMissing:
		return fmt.Sprintf(
			"%s ended without its terminal contract (no match for %s in the final message) — "+
				"the run did not reach its summary step, so it did not necessarily do the work it reports",
			r.Skill, r.Pattern,
		)
	case StatusNoTranscript:
		return r.Skill + " declares a terminal contract but no closing message could be read" +
			" — contract not verified"
	case StatusNotEnrolled, StatusPresent:
		return ""
	}
	return ""
}
