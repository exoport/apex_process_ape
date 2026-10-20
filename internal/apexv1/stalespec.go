package apexv1

import (
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// D232 (R7h-1): `ape decision standard` (the framework's migration
// v1.0.0_seq-08) removes records — the apex-v0 ADR-0001 file it replaces,
// the proposed ADRs that superseded it — and a lasting spec scenario whose
// check: names one is red from then on (pdfa's "ADR-0001 is superseded by
// a v1-format ADR"). The loop must not "fix" such a red by superseding
// ADR-0001 again: an accepted supersede opts the project out of APEX's
// standard (D205). So the verb records what it removed (Q130), and ape
// finds the scenarios that check it (Q131): in the verb's output, as a
// project check notice, and on the red line of a gate or verify. It never
// edits a spec (Q132): a spec changes only through a change's delta.

// StandardRemoval is one file `ape decision standard` removed (Q130): its
// project-relative path and the ADR it held.
type StandardRemoval struct {
	Path string `json:"path"`
	ID   string `json:"id"`
}

// StaleScenario is a scenario of a lasting spec whose check: names what
// `ape decision standard` removed (Q131).
type StaleScenario struct {
	// Spec is the spec's project-relative path, Line its scenario
	// heading's line.
	Spec        string `json:"spec"`
	Requirement string `json:"requirement"`
	Scenario    string `json:"scenario"`
	Line        int    `json:"line"`
	// Names are what its check names that was removed: a path, a file
	// name, an ADR id, or "Supersedes: ADR-0001".
	Names []string `json:"names"`
	ref   string
}

// CodeSpecCheckNamesRemoved: project check's notice for a lasting
// scenario whose check names a record `ape decision standard` removed
// (D232 Q131).
const CodeSpecCheckNamesRemoved = "spec.check-names-removed"

// standardSupersedes is the line a supersede of the standard carries: a
// check that requires it requires what the verb removed, once it removed
// one (Q131).
const standardSupersedes = "Supersedes: " + StandardADRID

// standardMigration names the migration that runs `ape decision standard`
// first, for the pointer.
const standardMigration = "v1.0.0_seq-08"

// recordedRemovals is what `ape decision standard` removed, by its record
// in decisions.json (Q130). legacy reports no such record on a project
// whose ADR-0001 is the standard that verb wrote: a run before rc.7d
// recorded removals (pdfa's seq-08 ran on rc.7b; Q133). A standard init
// or the lift wrote removed nothing.
func (p *Project) recordedRemovals() (removed []StandardRemoval, legacy bool) {
	rec, ok := p.loadDecisionRecords()[StandardADRID]
	if !ok || rec.Standard == nil {
		return nil, false
	}
	if rec.Standard.Removed != nil {
		return *rec.Standard.Removed, false
	}
	return nil, rec.By == StandardBy("ape decision standard") && fileExists(p.standardADRPath())
}

func (p *Project) standardADRPath() string { return filepath.Join(p.DecisionsDir(), StandardADRFile()) }

// StaleScenarios are the lasting specs' scenarios whose check names what
// `ape decision standard` removed (Q131), or — with no record of it, on a
// project whose ADR-0001 is the standard that verb wrote (Q133) — a
// decisions/ file that does not exist, or an ADR-0001 file name that is
// not the standard's.
func (p *Project) StaleScenarios() []StaleScenario {
	removed, legacy := p.recordedRemovals()
	return p.staleScenarios(removed, legacy, false)
}

// staleScenarios scans the lasting specs for checks naming removed (or,
// legacy, the Q133 evidence); planned is `--check`'s scan of what a write
// is to remove, which still exists. A check that asserts the name's
// absence (`! test -e <path>`, `[ ! -f <path> ]`, `! grep -q 'Supersedes:
// ADR-0001' …`) is not stale.
func (p *Project) staleScenarios(removed []StandardRemoval, legacy, planned bool) []StaleScenario {
	needles := p.removalNeedles(removed, planned)
	if len(needles) == 0 && !legacy {
		return nil
	}
	var out []StaleScenario
	for _, capName := range p.Capabilities() {
		spec, _, ok := p.LoadSpec(capName)
		if !ok {
			continue
		}
		for i := range spec.Requirements {
			r := &spec.Requirements[i]
			for j := range r.Scenarios {
				s := &r.Scenarios[j]
				var names []string
				for _, c := range s.Checks {
					found := namedIn(c, needles)
					if legacy {
						found = append(found, p.legacyStaleNames(c)...)
					}
					for _, n := range found {
						if !slices.Contains(names, n) {
							names = append(names, n)
						}
					}
				}
				if len(names) > 0 {
					out = append(out, StaleScenario{
						Spec: p.Rel(p.SpecFile(capName)), Requirement: r.Name, Scenario: s.Name, Line: s.Line,
						Names: dropCoveredNames(names), ref: scenarioRef(capName, r.Name, s.Name),
					})
				}
			}
		}
	}
	return out
}

// needle is one name a check may hold: the text, and whether it is a
// word (an ADR id: ADR-0031 is not in ADR-00310) or a file name (bounded
// by what cannot be part of one).
type needle struct {
	text string
	word bool
}

// removalNeedles are the names of what was removed that are still gone:
// each path, its file name, the ADR id (but ADR-0001's: the standard
// holds that id now), and, once a supersede of the standard was removed,
// its Supersedes: line. A path that exists again, or an id a new decision
// took, names nothing removed — but for planned removals, which exist.
func (p *Project) removalNeedles(removed []StandardRemoval, planned bool) []needle {
	var out []needle
	add := func(n needle) {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	for _, r := range removed {
		if r.Path != "" && (planned || !fileExists(p.abs(r.Path))) {
			add(needle{text: r.Path})
			add(needle{text: path.Base(r.Path)})
		}
		if r.ID != "" && r.ID != StandardADRID {
			if planned || !p.decisionExists(r.ID) {
				add(needle{text: r.ID, word: true})
			}
			add(needle{text: standardSupersedes, word: true})
		}
	}
	return out
}

func (p *Project) abs(rel string) string { return filepath.Join(p.Root, filepath.FromSlash(rel)) }

// namedIn are the needles cmd names other than in an absence assertion.
func namedIn(cmd string, needles []needle) []string {
	neg := absenceAssertions(cmd)
	var out []string
	for _, n := range needles {
		for from := 0; ; {
			i := strings.Index(cmd[from:], n.text)
			if i < 0 {
				break
			}
			i += from
			from = i + len(n.text)
			if !bounded(cmd, i, from, n.word) || neg(i) {
				continue
			}
			out = append(out, n.text)
			break
		}
	}
	return out
}

// bounded reports whether cmd[i:j] stands on its own: a word (an ADR id,
// the Supersedes: line) not inside a longer word, a file name not inside a
// longer file name (a path's last element is one).
func bounded(cmd string, i, j int, word bool) bool {
	in := func(b byte) bool {
		return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-' || !word && b == '.'
	}
	return (i == 0 || !in(cmd[i-1])) && (j == len(cmd) || !in(cmd[j]))
}

// legacyDecisionPathRe finds a decisions/ file a check names; Q133 reads
// one that does not exist as removed.
var legacyDecisionPathRe = regexp.MustCompile(`[A-Za-z0-9._/-]*decisions/[A-Za-z0-9._-]+\.md`)

// legacyADR0001Re finds an ADR-0001 file name a check names.
var legacyADR0001Re = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])(0001-[A-Za-z0-9._-]+\.md)`)

// legacyStaleNames is Q133's evidence in one check: a decisions/ file
// under the project's decisions folder that does not exist, and an
// ADR-0001 file name that is not the standard's, each except where the check
// asserts its absence.
func (p *Project) legacyStaleNames(cmd string) []string {
	neg := absenceAssertions(cmd)
	dec := p.Rel(p.DecisionsDir()) + "/"
	var out []string
	for _, m := range legacyDecisionPathRe.FindAllStringIndex(cmd, -1) {
		rel := strings.TrimPrefix(cmd[m[0]:m[1]], "./")
		if !strings.HasPrefix(rel, dec) || fileExists(p.abs(rel)) || neg(m[0]) {
			continue
		}
		out = append(out, rel)
	}
	for _, m := range legacyADR0001Re.FindAllStringSubmatchIndex(cmd, -1) {
		name := cmd[m[2]:m[3]]
		if name == StandardADRFile() || fileExists(p.abs(dec+name)) || neg(m[2]) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// dropCoveredNames drops a file name whose path is listed too.
func dropCoveredNames(names []string) []string {
	var out []string
	for _, n := range names {
		covered := false
		for _, m := range names {
			if m != n && strings.HasSuffix(m, "/"+n) {
				covered = true
			}
		}
		if !covered {
			out = append(out, n)
		}
	}
	return out
}

// absenceRe is a simple command that asserts absence: negated by `!`, or
// a test of a negated condition (`test ! -e …`, `[ ! -f … ]`).
var absenceRe = regexp.MustCompile(`^\s*(?:!\s|(?:test|\[\[?)\s+!\s)`)

// absenceAssertions reads cmd as sh and returns whether byte i sits in a
// simple command that asserts absence — directly, or inside a group or
// substitution of one (`! { …; }`). It follows quotes, escapes, `$( … )`
// inside double quotes and `${ … }`; it is a reader for pointing at a
// scenario, not a shell parser: on input it cannot follow it errs
// toward "not an absence".
func absenceAssertions(cmd string) func(i int) bool {
	type frame struct {
		neg      bool
		closer   byte
		inDouble bool
		segStart int
	}
	segStart := make([]int, len(cmd))
	frameNeg := make([]bool, len(cmd))
	stack := []frame{{}}
	seg := 0
	sq, dq := false, false
	negHere := func(i int) bool { return stack[len(stack)-1].neg || absenceRe.MatchString(cmd[seg:i]) }
	for i := 0; i < len(cmd); i++ {
		segStart[i], frameNeg[i] = seg, stack[len(stack)-1].neg
		c := cmd[i]
		switch {
		case sq:
			if c == '\'' {
				sq = false
			}
		case c == '\\':
			if i+1 < len(cmd) {
				i++
				segStart[i], frameNeg[i] = seg, stack[len(stack)-1].neg
			}
		case c == '$' && i+1 < len(cmd) && (cmd[i+1] == '(' || cmd[i+1] == '{'):
			closer := byte(')')
			if cmd[i+1] == '{' {
				closer = '}'
			}
			stack = append(stack, frame{neg: negHere(i), closer: closer, inDouble: dq, segStart: seg})
			i++
			segStart[i], frameNeg[i] = seg, stack[len(stack)-2].neg
			seg, dq = i+1, false
		case dq:
			if c == '"' {
				dq = false
			}
		case c == '\'':
			sq = true
		case c == '"':
			dq = true
		case c == '(' || c == '{' && (i+1 == len(cmd) || cmd[i+1] == ' '):
			closer := byte(')')
			if c == '{' {
				closer = '}'
			}
			stack = append(stack, frame{neg: negHere(i), closer: closer, segStart: seg})
			seg = i + 1
		case (c == ')' || c == '}') && len(stack) > 1 && stack[len(stack)-1].closer == c:
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			seg, dq = f.segStart, f.inDouble
		case c == ';' || c == '|' || c == '&' || c == '\n':
			seg = i + 1
		}
	}
	return func(i int) bool {
		if i < 0 || i >= len(cmd) {
			return false
		}
		return frameNeg[i] || absenceRe.MatchString(cmd[segStart[i]:i])
	}
}

// stalePointer is the pointer a stale scenario carries — in project
// check's notice, the verb's output and a red gate or verify line — after
// " — ": how to retire it, and what never to do (Q131).
func stalePointer(req string) string {
	return "retire it with a change whose REMOVED delta of requirement \"" + req + "\" cites the migration " + standardMigration +
		" (re-ADD what still holds under a new requirement name); never recreate the removed records or supersede " + StandardADRID +
		" again: an accepted supersede opts the project out of APEX's standard (D205)"
}

// staleWhat says what a stale scenario's check names.
func staleWhat(names []string) string {
	return "its check names " + strings.Join(names, ", ") + ", which `ape decision standard` (the framework's migration " + standardMigration +
		", APEX's standard ADR-0001) removed, so it can never pass again"
}

// Message is the scenario in prose: which it is, what its check names
// and, after " — ", how to retire it.
func (s *StaleScenario) Message() string {
	return "scenario \"" + s.Scenario + "\" of requirement \"" + s.Requirement + "\": " + staleWhat(s.Names) + " — " + stalePointer(s.Requirement)
}

// staleScenarioNotices are project check's notices spec.check-names-removed
// (Q131): one per stale scenario, at its heading. The exit is unchanged.
func (p *Project) staleScenarioNotices() []Diagnostic {
	var out []Diagnostic
	for _, s := range p.StaleScenarios() {
		out = append(out, notice(Diagnostic{Code: CodeSpecCheckNamesRemoved, File: s.Spec, Line: s.Line, Message: s.Message()}))
	}
	return out
}

// markStale sets StaleNames on the red scenario results of the lasting
// spec that check what `ape decision standard` removed (Q131), so a gate
// or verify's red line says why and what not to do. A change's own delta
// scenarios are its own: not marked.
func (p *Project) markStale(results []CheckResult) {
	var stale map[string][]string
	for i := range results {
		r := &results[i]
		if r.Kind != KindScenario || r.Delta || r.Exit == 0 {
			continue
		}
		if stale == nil {
			stale = map[string][]string{}
			for _, s := range p.StaleScenarios() {
				stale[s.ref] = s.Names
			}
		}
		if names, ok := stale[scenarioRef(r.Capability, r.Requirement, r.Scenario)]; ok {
			r.StaleNames = names
		}
	}
}

// StalePointer is the line a red scenario result whose check names what
// `ape decision standard` removed carries ("" for any other): what it
// names, then how to retire it.
func (r *CheckResult) StalePointer() string {
	if len(r.StaleNames) == 0 {
		return ""
	}
	return staleWhat(r.StaleNames) + " — " + stalePointer(r.Requirement)
}

// mergeRemovals is the record's removed after a run that removed now:
// what earlier runs removed, then now's, each path once, in path order.
func mergeRemovals(prev *[]StandardRemoval, now []StandardRemoval) *[]StandardRemoval {
	out := []StandardRemoval{}
	if prev != nil {
		out = append(out, *prev...)
	}
	for _, r := range now {
		if !slices.ContainsFunc(out, func(o StandardRemoval) bool { return o.Path == r.Path }) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return &out
}
