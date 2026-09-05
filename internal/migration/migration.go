// Package migration reads and orders the framework's per-version upgrade
// list — `_apex/migrations/*.md` — and decides, per entry, whether a
// project still owes it.
//
// A framework release changes what project data must look like. Until
// this existed the only upgrade path was prose in a CHANGELOG bullet and
// whatever the operator remembered to run, and a project that skipped a
// version had no way to find out.
//
// Four rules govern the whole package, agreed with the framework
// maintainer and not re-derived here:
//
//  1. **The ledger decides applied-ness, not the check.** Applied ids
//     live in `_apex/framework.yaml` as an ORDERED list of
//     `{id, version, applied_at}` — a list rather than a set, because
//     "which migrations ran, in what order" is what an operator asks when
//     one half-applies.
//  2. **`kind:` gates whether the runner may ACT.** `derivable` runs
//     unattended. `judged` is listed and NEVER executed, under any flag,
//     with the skill to dispatch named instead.
//  3. **`check:` reports and never gates.** A check that cannot run
//     leaves the entry unapplied-and-unverifiable — CANNOT-TELL — and
//     blocks nothing. Collapsing cannot-tell into pending is what builds
//     a runner that re-applies things; a check with no basis to judge
//     must not invent a verdict, an acquittal included.
//  4. **Ordering is semantic, never the filename's.** `version` compares
//     as semver, `seq` as an integer, and `after:` is honoured — `v0.9.0`
//     sorts after `v0.10.0` lexically, which is exactly the bug the rule
//     exists to prevent. The filename is for uniqueness and humans, and
//     one that disagrees with its frontmatter is a reported finding.
//
// Nothing here HALTs. An unparseable entry, an `after:` cycle and an
// unreadable folder are all reported, because a runner that stopped on
// one bad entry could not report the others.
package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

// DirName is the migration folder under `{apex_folder}`.
const DirName = "migrations"

// Entry kinds. The distinction is the runner's whole authority model.
const (
	// KindDerivable — the runner may execute `command:` unattended.
	KindDerivable = "derivable"
	// KindJudged — listed and never executed under any flag. The entry's
	// body is the judgement instructions and `skill:` names who reads them.
	KindJudged = "judged"
)

// Entry states, as `--plan` reports them.
const (
	// StateApplied — the ledger records it, or (absent a ledger record)
	// its check says the shape is already there. Source says which.
	StateApplied = "applied"
	// StateHalfApplied — the ledger says it ran and the check says the
	// post-condition does not hold. The one state the ordered ledger
	// exists to make visible.
	StateHalfApplied = "half-applied"
	// StatePending — not in the ledger, and either the check says
	// unsatisfied or no check is declared. The only state the runner acts
	// on, and only for a derivable entry.
	StatePending = "pending"
	// StateCannotTell — not in the ledger, and the check could NOT RUN.
	// Unapplied and unverifiable. Never acted on: acting here is exactly
	// the re-application the state exists to prevent.
	StateCannotTell = "cannot-tell"
	// StateSuperseded — named in a present entry's `supersedes:`.
	StateSuperseded = "superseded"
)

// Where an `applied` verdict came from.
const (
	SourceLedger = "ledger"
	SourceCheck  = "check"
)

// Check verdicts. `cannot-run` is not a failure of the migration — it is
// the absence of evidence, and it is reported as such.
const (
	CheckSatisfied   = "satisfied"
	CheckUnsatisfied = "unsatisfied"
	CheckCannotRun   = "cannot-run"
	CheckNone        = "none"
	// CheckNotRun is the verdict on a --plan of an entry whose check was
	// deliberately not executed, and on every entry when checks are off.
	CheckNotRun = "not-run"
)

// Entry is one parsed migration file.
type Entry struct {
	Path string `json:"path" yaml:"path"`

	ID         string   `json:"id"                   yaml:"id"`
	Version    string   `json:"version"              yaml:"version"`
	Seq        int      `json:"seq"                  yaml:"seq"`
	Kind       string   `json:"kind"                 yaml:"kind"`
	Blocking   bool     `json:"blocking"             yaml:"blocking"`
	After      []string `json:"after,omitempty"      yaml:"after,omitempty"`
	Supersedes []string `json:"supersedes,omitempty" yaml:"supersedes,omitempty"`
	Check      string   `json:"check,omitempty"      yaml:"check,omitempty"`
	Command    string   `json:"command,omitempty"    yaml:"command,omitempty"`
	Skill      string   `json:"skill,omitempty"      yaml:"skill,omitempty"`
	Summary    string   `json:"summary,omitempty"    yaml:"summary,omitempty"`

	// Findings are this entry's own defects — an unparseable file, a
	// filename disagreeing with the frontmatter, a kind with no runnable.
	// Reported, never fatal. An entry with a finding that makes it
	// unrunnable carries Unrunnable too.
	Findings []string `json:"findings,omitempty" yaml:"findings,omitempty"`
	// Unrunnable marks an entry the runner must never execute even when
	// its state is pending — because it does not say what to run, or
	// because the file could not be parsed.
	Unrunnable bool `json:"unrunnable,omitempty" yaml:"unrunnable,omitempty"`
}

// IsJudged reports whether the runner is forbidden from executing this
// entry. An unrecognised kind counts as judged: the safe reading of
// "ape does not understand what this is" is "ape does not run it".
func (e *Entry) IsJudged() bool { return e.Kind != KindDerivable }

// Dir is the migration folder inside a project.
func Dir(apexDir string) string {
	if apexDir == "" {
		return ""
	}
	return filepath.Join(apexDir, DirName)
}

// seqAnchorRe pulls the sequence out of a filename. `_seq-` is the
// parsing anchor, deliberately: the version before it may carry any
// character — `-rc.1`, even `_` — so the anchor is the only reliable
// split point. The digits after it, up to the next `_`, are the sequence.
var seqAnchorRe = regexp.MustCompile(`_seq-(\d+)(?:_|$)`)

// Load reads and parses every `*.md` under the migration folder.
//
// An absent folder is not an error and not a finding: a project on a
// framework that ships no migration list is in a normal state. Load
// returns entries in FILE-SYSTEM order; call Order for the semantic one.
func Load(dir string) ([]Entry, error) {
	if dir == "" {
		return nil, nil
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []Entry
	for _, n := range names {
		if n.IsDir() || !strings.HasSuffix(n.Name(), ".md") {
			continue
		}
		out = append(out, loadOne(filepath.Join(dir, n.Name())))
	}
	return out, nil
}

func loadOne(path string) Entry {
	e := Entry{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		e.findingf("unreadable: %v", err)
		e.Unrunnable = true
		return e
	}
	fm, _, err := frontmatter.Split(data)
	if err != nil {
		e.findingf("no YAML frontmatter block")
		e.Unrunnable = true
		return e
	}
	var parsed Entry
	if err := yaml.Unmarshal(fm, &parsed); err != nil {
		e.findingf("frontmatter is not valid YAML: %v", err)
		e.Unrunnable = true
		return e
	}
	parsed.Path = path
	parsed.validate()
	return parsed
}

// validate records the entry's own defects. Every one is a finding rather
// than a rejection: the framework authors these files, and a project must
// still be able to see an entry it cannot run.
func (e *Entry) validate() {
	base := filepath.Base(e.Path)

	if e.ID == "" {
		e.findingf("no id — an entry with no id cannot be recorded as applied")
		e.Unrunnable = true
	}
	if e.Version == "" {
		e.findingf("no version — ordering falls back to seq alone")
	} else if !semver.IsValid("v" + strings.TrimPrefix(e.Version, "v")) {
		e.findingf("version %q is not semver — it orders after every valid version", e.Version)
	}
	switch e.Kind {
	case KindDerivable:
		if e.Command == "" {
			e.findingf("kind: derivable with no command — nothing to run")
			e.Unrunnable = true
		}
	case KindJudged:
		if e.Skill == "" {
			e.findingf("kind: judged with no skill — nothing to dispatch")
		}
	case "":
		e.findingf("no kind — treated as judged, so it is never executed")
	default:
		e.findingf("unknown kind %q — treated as judged, so it is never executed", e.Kind)
	}
	if e.Kind == KindDerivable && e.Skill != "" {
		e.findingf("kind: derivable also names a skill — the skill is ignored")
	}

	// The filename is for uniqueness and humans; the frontmatter is
	// authoritative. A disagreement is a finding and never a re-read: a
	// runner that trusted the filename would order by it, which is the
	// one thing the ordering rule forbids.
	if m := seqAnchorRe.FindStringSubmatch(base); m == nil {
		e.findingf("filename %q carries no _seq- anchor", base)
	} else if n, convErr := strconv.Atoi(m[1]); convErr == nil && n != e.Seq {
		e.findingf("filename says seq %d, frontmatter says %d — the frontmatter wins", n, e.Seq)
	}
	if e.ID != "" && !strings.HasPrefix(base, e.ID) {
		e.findingf("filename %q does not start with the id %q", base, e.ID)
	}
}

func (e *Entry) findingf(format string, args ...any) {
	e.Findings = append(e.Findings, fmt.Sprintf(format, args...))
}

// Order sorts entries semantically and then honours `after:`.
//
// The base order is semver on `version`, then `seq`, then id — never the
// filename's lexical order, under which `v0.9.0` sorts after `v0.10.0`.
// `after:` then moves an entry behind its dependencies without disturbing
// anything it does not name, and a cycle is reported rather than broken:
// with the order undefined the runner has no basis to act, and inventing
// one is how a migration runs before what it depends on.
//
// cycle names the ids involved when one exists; it is empty otherwise.
func Order(entries []Entry) (ordered []Entry, cycle []string) {
	base := make([]Entry, len(entries))
	copy(base, entries)
	sort.SliceStable(base, func(i, j int) bool {
		return less(&base[i], &base[j])
	})

	index := make(map[string]int, len(base))
	for i := range base {
		if base[i].ID != "" {
			index[base[i].ID] = i
		}
	}

	// Kahn's algorithm over the `after:` edges, draining ready nodes in
	// base order so an entry naming nothing keeps its semantic position.
	indeg := make([]int, len(base))
	deps := make([][]int, len(base))
	for i := range base {
		for _, dep := range base[i].After {
			j, ok := index[dep]
			if !ok {
				// An `after:` naming an entry that is not here is not a
				// cycle and not a reason to refuse an order — it is a
				// finding on the entry, so the operator sees the dangling
				// name rather than an unexplained reordering.
				base[i].findingf("after: names %q, which is not in the migration list", dep)
				continue
			}
			deps[j] = append(deps[j], i)
			indeg[i]++
		}
	}

	ordered = make([]Entry, 0, len(base))
	placed := make([]bool, len(base))
	for len(ordered) < len(base) {
		progressed := false
		for i := range base {
			if placed[i] || indeg[i] != 0 {
				continue
			}
			ordered = append(ordered, base[i])
			placed[i] = true
			progressed = true
			for _, d := range deps[i] {
				indeg[d]--
			}
			break // restart the scan so base order is preserved
		}
		if !progressed {
			for i := range base {
				if !placed[i] {
					cycle = append(cycle, entryLabel(&base[i]))
				}
			}
			// The un-orderable entries are appended in base order so the
			// caller can still report them; they are marked unrunnable
			// because their position is unknown.
			for i := range base {
				if !placed[i] {
					base[i].Unrunnable = true
					base[i].findingf("after: cycle — this entry's position is undefined, so it is never run")
					ordered = append(ordered, base[i])
				}
			}
			return ordered, cycle
		}
	}
	return ordered, nil
}

// less is the semantic comparison. An unparseable version sorts LAST, so
// a malformed entry never silently jumps the queue.
func less(a, b *Entry) bool {
	av, bv := "v"+strings.TrimPrefix(a.Version, "v"), "v"+strings.TrimPrefix(b.Version, "v")
	aok, bok := semver.IsValid(av), semver.IsValid(bv)
	switch {
	case aok && !bok:
		return true
	case !aok && bok:
		return false
	case aok && bok:
		if c := semver.Compare(av, bv); c != 0 {
			return c < 0
		}
	}
	if a.Seq != b.Seq {
		return a.Seq < b.Seq
	}
	return a.ID < b.ID
}

func entryLabel(e *Entry) string {
	if e.ID != "" {
		return e.ID
	}
	return filepath.Base(e.Path)
}

// Label is the entry's id, falling back to its filename so an entry with
// no id is still nameable in a report.
func (e *Entry) Label() string { return entryLabel(e) }
