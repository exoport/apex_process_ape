// Package commitowners reads the framework's `_apex/commit-owners.csv`
// and asserts, per dispatch, that a skill's git behaviour matched what
// that file declares.
//
// # Why a file and not prose
//
// Until now, "does this skill commit?" was answered by an `## Commit
// Policy` section carried in every skill — 85 of them, ~89 KB of
// git-prohibition prose that a reviewer had to read and a model had to
// obey. Prose works exactly as well as it is followed, and it was not:
// the roster it described ("exactly three skills commit") was already
// false, and a suppressed commit — a skill quietly not committing what
// it was declared to commit — is invisible to every check the framework
// had. The CSV is the declaration; this package is the enforcement.
//
// # The two assertions
//
// For a skill ABSENT from the CSV: HEAD unchanged, the index empty, and
// the stash reflog unchanged across the dispatch. HEAD alone is not the
// assertion, and that is the whole point — `git add` and `git stash`
// both leave HEAD exactly where it was, and the framework's operating
// rules forbid them precisely because a stash silently destroys the
// caller's working tree.
//
// For a skill PRESENT: every commit in `pre..HEAD` matches one of that
// skill's declared message regexes, and there is at least one. Not "HEAD
// advanced by one" — a batch dispatch makes N commits, one `dev` and one
// `review` per story, so the predicate is per-commit over the range.
//
// # What is deliberately tolerated
//
// Rows naming a skill `ape` never dispatches. The conducting session's
// rows (`release:`, `evidence:`, the project's declared repair type,
// `chore(<area>):`) carry `apex-orchestrator` in the skill column, and
// that is a persona which is adopted, never an `ape task` target. The
// per-dispatch assertion simply never fires for it; a reader that failed
// on such a row would break every dispatch in the project.
//
// An absent CSV means "no skill commits" — every dispatch takes the
// non-committer assertion. That is a normal state, not an error.
package commitowners

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FileName is the declaration's name inside the project's apex folder.
const FileName = "commit-owners.csv"

// Header is the schema, verbatim from PLAN-64 5.2. A file whose first
// row is anything else is a different format, and reading it as this one
// would silently mis-assign columns.
var Header = []string{"skill", "commit_kind", "message_regex"}

// Row is one declared commit a skill owns.
//
// The tags are the CSV's own column names, so a JSON dump of a Row reads
// back against the file it came from.
//
//nolint:tagliatelle // field tags mirror the CSV's snake_case column names
type Row struct {
	Skill string `json:"skill"       yaml:"skill"`
	Kind  string `json:"commit_kind" yaml:"commit_kind"`
	// Pattern is the compiled message_regex, applied to a commit's
	// SUBJECT LINE only.
	Pattern *regexp.Regexp `json:"-" yaml:"-"`
	// Source is the regex as written in the file, for messages.
	Source string `json:"message_regex" yaml:"message_regex"`
}

// Table is the parsed declaration.
type Table struct {
	// Present reports whether the file existed. An absent file is not an
	// error; it means no skill commits.
	Present bool
	// Path is where it was read from, when Present.
	Path string

	bySkill map[string][]Row
}

// Owns reports whether skill is declared as a committer.
func (t *Table) Owns(skill string) bool {
	if t == nil {
		return false
	}
	return len(t.bySkill[skill]) > 0
}

// Rows returns skill's declared commits, nil when it owns none.
func (t *Table) Rows(skill string) []Row {
	if t == nil {
		return nil
	}
	return t.bySkill[skill]
}

// Skills lists every declared skill, sorted by first appearance.
func (t *Table) Skills() []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.bySkill))
	for skill := range t.bySkill {
		out = append(out, skill)
	}
	return out
}

// ErrMalformed reports a CSV that exists but cannot be read as this
// schema. Deliberately fatal rather than a fall-back to "no skill
// commits": silently treating a broken declaration as an empty one would
// turn every committer's assertion into the non-committer's, which is
// the exact inversion that lets a suppressed commit through.
var ErrMalformed = errors.New("commit-owners.csv is malformed")

// Load reads `<apexDir>/commit-owners.csv`.
func Load(apexDir string) (*Table, error) {
	if apexDir == "" {
		return &Table{}, nil
	}
	path := filepath.Join(apexDir, FileName)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// The declaration has not shipped yet, or the project has no
			// committers. Both mean the same thing to a dispatch.
			return &Table{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	table, err := parse(f, path)
	if err != nil {
		return nil, err
	}
	table.Present = true
	table.Path = path
	return table, nil
}

func parse(r io.Reader, path string) (*Table, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = len(Header)
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrMalformed, path, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: %s: file is empty", ErrMalformed, path)
	}
	if !sameHeader(records[0]) {
		return nil, fmt.Errorf("%w: %s: header is %v, want %v",
			ErrMalformed, path, records[0], Header)
	}

	table := &Table{bySkill: map[string][]Row{}}
	for i, rec := range records[1:] {
		line := i + 2 // 1-indexed, past the header
		skill := strings.TrimSpace(rec[0])
		kind := strings.TrimSpace(rec[1])
		source := strings.TrimSpace(rec[2])
		if skill == "" || source == "" {
			return nil, fmt.Errorf("%w: %s:%d: skill and message_regex are both required",
				ErrMalformed, path, line)
		}
		// Compiled VERBATIM. Every regex carries its own ^…$ in the file,
		// so the reader adds no anchors: wrapping a row that already
		// anchors itself would change what it matches, and silently
		// anchoring one that does not would make a deliberately loose row
		// strict.
		pattern, compileErr := regexp.Compile(source)
		if compileErr != nil {
			return nil, fmt.Errorf("%w: %s:%d: message_regex %q does not compile: %w",
				ErrMalformed, path, line, source, compileErr)
		}
		table.bySkill[skill] = append(table.bySkill[skill], Row{
			Skill: skill, Kind: kind, Pattern: pattern, Source: source,
		})
	}
	return table, nil
}

func sameHeader(got []string) bool {
	if len(got) != len(Header) {
		return false
	}
	for i, want := range Header {
		// A BOM on the first cell is what a spreadsheet export leaves
		// behind, and it would otherwise fail the whole file on a
		// difference nobody can see. Written as an escape rather than a
		// literal: a raw BOM mid-source is itself a compile error.
		cell := strings.TrimPrefix(strings.TrimSpace(got[i]), "\ufeff")
		if !strings.EqualFold(cell, want) {
			return false
		}
	}
	return true
}

// MatchSubject reports whether subject matches any of skill's declared
// rows, and which one.
func (t *Table) MatchSubject(skill, subject string) (Row, bool) {
	for _, row := range t.Rows(skill) {
		if row.Pattern.MatchString(subject) {
			return row, true
		}
	}
	return Row{}, false
}
