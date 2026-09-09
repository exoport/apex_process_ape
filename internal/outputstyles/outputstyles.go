// Package outputstyles implements the framework-owned per-skill
// output-style table (`_apex/output-styles.csv`).
//
// An output style changes what a session's main thread emits, and the
// framework measured that the effect is not uniform: some skills finish
// materially faster under a terse style and some finish materially
// slower. Which skill takes which style is a framework judgement, made
// from framework measurements, so the vocabulary lives in a framework
// file and ape supplies only the mechanism — the same division as
// `_apex/terminal-contracts.csv`.
//
// ape deliberately does NOT know that "Concise" exists, which skills
// benefit from it, or that any particular style is desirable. It reads a
// name and pins it on the spawned session.
//
// # Scope: `ape task` only
//
// The table is keyed by skill, and a pipeline stage is a chain of
// skills sharing ONE spawned process. A stage whose steps mapped to
// different styles could not honour them — the process is launched once
// and the style is a launch-time setting — so consulting this table for
// pipeline steps would silently apply one step's style to its
// neighbours. That is the same defect class as a step-level `model:`
// that never reaches the running session (see
// Spec.StageModelConflicts). Pipelines therefore declare their own
// style at pipeline or stage level, where the granularity matches what
// a process spawn can actually deliver.
package outputstyles

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/bridge/config"
)

// TableFile is the project-relative path of the framework-owned table.
// Installed by `ape framework setup|update`; absent on a project whose
// framework predates it, which enrols nothing.
const TableFile = "_apex/output-styles.csv"

// Table maps a skill name to the output style the framework declares
// for it. A nil or empty Table enrols nothing — every skill then takes
// config.DefaultOutputStyle, which is exactly today's behaviour.
type Table struct {
	styles map[string]string
	// Warnings records rows that could not be used, and rows whose
	// style name ape has reason to believe will not resolve. Surfaced
	// at load and by `ape doctor`, never fatal: a malformed framework
	// file must not fail a user's run.
	Warnings []string
}

// Len reports how many skills are enrolled.
func (t *Table) Len() int {
	if t == nil {
		return 0
	}
	return len(t.styles)
}

// Skills lists the enrolled skill names, sorted.
func (t *Table) Skills() []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.styles))
	for s := range t.styles {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Style returns the declared style for a skill and whether one exists.
// A skill with no row is not enrolled and takes the pinned default.
func (t *Table) Style(skill string) (string, bool) {
	if t == nil || skill == "" {
		return "", false
	}
	style, ok := t.styles[skill]
	return style, ok
}

// Entries returns the enrolled skill→style pairs, sorted by skill. For
// `ape doctor`, which has to show what each skill resolves to rather
// than only how many rows parsed.
func (t *Table) Entries() [][2]string {
	if t == nil {
		return nil
	}
	out := make([][2]string, 0, len(t.styles))
	for _, skill := range t.Skills() {
		out = append(out, [2]string{skill, t.styles[skill]})
	}
	return out
}

// Load reads the per-skill output-style table from a project root.
//
// An absent file is not an error: it yields an empty Table, so a project
// on an older framework behaves exactly as it did before this table
// existed. Only an unreadable-but-present file, or one that is not
// parseable as CSV at all, returns an error.
func Load(projectRoot string) (*Table, error) {
	path := filepath.Join(projectRoot, TableFile)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Table{styles: map[string]string{}}, nil
		}
		return nil, fmt.Errorf("open %s: %w", TableFile, err)
	}
	defer f.Close()
	return parse(f)
}

// parse reads the CSV body. Format is two columns, `skill,style`, with
// an optional header row and `#` comment lines — the same shape and the
// same per-row tolerance as internal/contract.
func parse(r io.Reader) (*Table, error) {
	t := &Table{styles: map[string]string{}}
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
			t.warnf("row %d: want 2 columns (skill,style), got %d — ignored", i+1, len(row))
			continue
		}
		skill := strings.TrimSpace(row[0])
		style := strings.TrimSpace(row[1])
		// Tolerate the header row without requiring it.
		if i == 0 && strings.EqualFold(skill, "skill") && strings.EqualFold(style, "style") {
			continue
		}
		if skill == "" || style == "" {
			t.warnf("row %d: empty skill or style — ignored", i+1)
			continue
		}
		// A style name Claude Code cannot resolve is IGNORED by Claude
		// Code, silently — verified against 2.1.259 and recorded in
		// internal/bridge/config. So the row that is most likely to be
		// wrong is the one that looks right: a built-in spelled in the
		// wrong case resolves to nothing and the skill quietly runs the
		// default. ape reports it and keeps the value verbatim rather
		// than correcting it, because a project may legitimately ship a
		// custom style whose name differs from a built-in only by case,
		// and silently retargeting a row to a different style is a worse
		// failure than the one being reported.
		if canonical, builtin := config.BuiltinOutputStyleSpelling(style); builtin && canonical != style {
			t.warnf("skill %q: style %q is not the built-in spelling %q — "+
				"Claude Code ignores a style name it cannot resolve, so this row would leave the skill on %s",
				skill, style, canonical, config.DefaultOutputStyle)
		}
		t.styles[skill] = style
	}
	return t, nil
}

func (t *Table) warnf(format string, args ...any) {
	t.Warnings = append(t.Warnings, fmt.Sprintf(format, args...))
}
