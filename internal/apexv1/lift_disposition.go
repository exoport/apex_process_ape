package apexv1

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// D47 Q4: the lift writes development/lift-disposition.md, committed in
// the lift commit. apex-plan fills its disposition column, a fresh
// read-only session reviews it, then the person does; the person deletes
// it. Its full lists go to _apex/state/lift-disposition.json, which git
// ignores (R3). It holds the binding-rule candidates — the rules v0 kept in
// CLAUDE.md, project-context's "don't miss" list and team-memory's
// gotchas, which v1 has no file for — and everything the lift left for a
// reader: ids it did not edit, ADR gaps, references to removed paths.

var (
	dontMissRe     = regexp.MustCompile(`(?i)don'?t[- ]?miss`)
	gotchaRe       = regexp.MustCompile(`(?i)gotcha`)
	entryDateTail  = regexp.MustCompile(`\s+[—–-]+\s+\d{4}-\d{2}-\d{2}\s*$`)
	minRuleWordsRe = regexp.MustCompile(`\S+\s+\S+\s+\S+`)
)

// listItems returns the list items of lines[from:to] outside code, each
// with its indented continuation lines folded in.
func listItems(lines []mdLine) []LiftCandidate {
	var out []LiftCandidate
	cur := -1
	for _, l := range lines {
		if l.InCode {
			cur = -1
			continue
		}
		if m := listItemRe.FindStringSubmatch(l.Text); m != nil {
			out = append(out, LiftCandidate{Text: strings.TrimSpace(m[1]), Source: itoaLine(l.N)})
			cur = len(out) - 1
			continue
		}
		t := strings.TrimSpace(l.Text)
		if cur >= 0 && t != "" && (strings.HasPrefix(l.Text, " ") || strings.HasPrefix(l.Text, "\t")) {
			out[cur].Text += " " + t
			continue
		}
		cur = -1
	}
	var keep []LiftCandidate
	for _, c := range out {
		if minRuleWordsRe.MatchString(c.Text) {
			keep = append(keep, c)
		}
	}
	return keep
}

func itoaLine(n int) string { return strconv.Itoa(n) }

// sectionLines returns the body of every heading (any level) whose title
// matches re, up to the next heading of the same or a higher level.
func sectionLines(doc string, re *regexp.Regexp) [][]mdLine {
	lines := splitLines(doc)
	var out [][]mdLine
	for i := 0; i < len(lines); i++ {
		lv, t := headingLevel(lines[i])
		if lv == 0 || !re.MatchString(t) {
			continue
		}
		j := i + 1
		for j < len(lines) {
			if lv2, _ := headingLevel(lines[j]); lv2 > 0 && lv2 <= lv {
				break
			}
			j++
		}
		out = append(out, lines[i+1:j])
		i = j - 1
	}
	return out
}

func (lf *lifter) candidates() []LiftCandidate {
	var out []LiftCandidate
	add := func(src string, cs []LiftCandidate) {
		for _, c := range cs {
			c.Source = src + ":" + c.Source
			out = append(out, c)
		}
	}
	if doc := lf.claudeBefore; doc != "" {
		// The managed block is the v0 framework's, not the project's.
		var lines []mdLine
		in := false
		for _, l := range splitLines(doc) {
			switch strings.TrimSpace(l.Text) {
			case v0ManagedBegin:
				in = true
				continue
			case v0ManagedEnd:
				in = false
				continue
			}
			if !in {
				lines = append(lines, l)
			}
		}
		add("CLAUDE.md", listItems(lines))
	}
	if doc, ok := lf.texts["AGENTS.md"]; ok {
		add("AGENTS.md", listItems(splitLines(doc)))
	}
	if lf.projectContext != "" {
		for _, sec := range sectionLines(lf.readRel(lf.projectContext), dontMissRe) {
			add(LiftTag+":"+lf.projectContext, listItems(sec))
		}
	}
	if tm := lf.rel(lf.layout.Cfg.Paths.TeamMemory); tm != "" {
		if doc := lf.readRel(tm); doc != "" {
			for _, sec := range sectionLines(doc, gotchaRe) {
				for _, l := range sec {
					if lv, t := headingLevel(l); lv >= 3 {
						out = append(out, LiftCandidate{Text: entryDateTail.ReplaceAllString(t, ""), Source: LiftTag + ":" + tm + ":" + itoaLine(l.N)})
					}
				}
			}
		}
	}
	return out
}

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
}

// The disposition file is what apex-plan, a fresh reviewer and the person
// read whole, so it holds summaries (L11): the dry runs wrote 12–379 KB,
// most of it references to removed paths inside evidence logs, past any
// reader's cap. On rc.3 it was still 109 KB in radix, 78.6 KB of it the
// code and CI references listed line by line, and the JSON beside it was
// a 5.8 MB commit in tenax (R3). So the file has a ceiling, its code and
// CI section is one line per file, and the full lists go to the ignored
// _apex/state/lift-disposition.json: in the checkout that lifted, never in
// history.
//
// R14 (the rc.4 dry runs): cut to its first 10 rows, the binding-rule
// table left 36–68 of each project's candidates in that checkout-local
// JSON — out of reach of the fresh reviewer, and of an apex-plan run in
// any other checkout. The candidates are the list the file exists for, so
// they are never cut to fit the cap: the summaries go down to counts
// first, then the ADR table to the ADRs with gaps, and if the candidates
// still overflow the cap, the file grows past it — up to
// liftDispositionCeiling, and only past that are candidates cut, the file
// saying how many and where they are.
const liftDispositionCap = 16 << 10

// liftDispositionCeiling is the most the disposition file grows to hold
// every candidate: the hard ceiling of the caps on project.md and
// AGENTS.md (caps.go). The file is transient — deleted once its
// candidates have a home.
const liftDispositionCeiling = 48 << 10

// dispLimits bound one rendering of the disposition file: files is how
// many files a summary names (the rest are counted; the report has them
// all), adrs how many ADR rows are listed, rules how many binding-rule
// candidates (-1: all); readerCounts puts the v0 state readers as counts,
// not one line per file.
type dispLimits struct {
	files, adrs, rules int
	readerCounts       bool
}

// dispSteps are tried in order until a rendering fits the cap: the
// summaries down to counts, then the ADR table. The candidates and the v0
// state readers are cut only past the ceiling (disposition).
var dispSteps = []dispLimits{ //nolint:mnd // a table of render steps: each number is a row, not a tunable
	{files: 40, adrs: -1, rules: -1},
	{files: 20, adrs: -1, rules: -1},
	{files: 10, adrs: -1, rules: -1},
	{files: 5, adrs: -1, rules: -1},
	{files: 3, adrs: -1, rules: -1},
	{files: 0, adrs: -1, rules: -1},
	{files: 0, adrs: 60, rules: -1},
	{files: 0, adrs: 30, rules: -1},
	{files: 0, adrs: 10, rules: -1},
}

// liftReport is lift-disposition.json: every list the lift made, whole.
type liftReport struct {
	Version             string           `json:"v0_version"`
	Tag                 string           `json:"tag"`
	TagSHA              string           `json:"tag_sha,omitempty"`
	Deleted             []LiftDeletion   `json:"deleted"`
	ADRs                []LiftADR        `json:"adrs"`
	Changed             []LiftFileChange `json:"changed"`
	Skipped             []LiftSkip       `json:"skipped,omitempty"`
	GoUnformatted       []string         `json:"go_not_formatted,omitempty"`
	Hashed              []LiftHashed     `json:"edited_but_hashed,omitempty"`
	V0StateReaders      []LiftV0Reader   `json:"v0_state_readers,omitempty"`
	UnresolvedCitations []LiftIDRef      `json:"unresolved_adr_citations,omitempty"`
	IDsLeft             []LiftIDRef      `json:"ids_left,omitempty"`
	DanglingRefs        []LiftIDRef      `json:"references_to_removed_paths,omitempty"`
	Candidates          []LiftCandidate  `json:"rule_candidates"`
	Gates               LiftGates        `json:"gates"`
	Readings            []string         `json:"readings,omitempty"`
	// Regression is the lift's regression verdict (RG-6).
	Regression *LiftRegressionRecord `json:"regression,omitempty"`
}

func (lf *lifter) report() string {
	r := lf.res
	data, err := json.MarshalIndent(liftReport{
		Version: r.Version, Tag: LiftTag, TagSHA: r.TagSHA, Deleted: r.Deleted, ADRs: r.ADRs, Changed: r.Changed,
		Skipped: r.Skipped, GoUnformatted: r.GoUnformatted, Hashed: lf.hashedAll, V0StateReaders: r.V0StateReaders, UnresolvedCitations: r.UnresolvedCitations,
		IDsLeft: r.IDsLeft, DanglingRefs: r.DanglingRefs, Candidates: r.Candidates, Gates: r.Gates, Readings: r.Readings,
		Regression: lf.regression,
	}, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(data) + "\n"
}

// disposition renders the disposition file: within liftDispositionCap
// when any step of dispSteps fits; else, every candidate and v0 state
// reader kept, within liftDispositionCeiling; else with the readers as
// counts; else with as many candidates as fit it.
func (lf *lifter) disposition() string {
	var out string
	for _, l := range dispSteps {
		if out = lf.dispositionWith(l); len(out) <= liftDispositionCap {
			return out
		}
	}
	if len(out) <= liftDispositionCeiling {
		return out
	}
	last := dispSteps[len(dispSteps)-1]
	last.readerCounts = true
	if out = lf.dispositionWith(last); len(out) <= liftDispositionCeiling {
		return out
	}
	n := sort.Search(len(lf.res.Candidates), func(n int) bool {
		last.rules = n + 1
		return len(lf.dispositionWith(last)) > liftDispositionCeiling
	})
	last.rules = n
	return lf.dispositionWith(last)
}

func (lf *lifter) dispositionWith(lim dispLimits) string {
	r := lf.res
	report := r.Report
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	p("# Lift disposition\n\n")
	p("Lifted from apex-v0 %s; the apex-v0 state is archived at the tag `%s` (%s), read it as `git show %s:<path>`.\n\n", r.Version, LiftTag, short(r.TagSHA), LiftTag)
	p("This file is the lift's hand-off. apex-plan fills the Disposition column and carries each kept rule to its one home;\n")
	p("a fresh read-only session reviews the result; then the person does, and deletes this file.\n")
	p("Long lists are summarised here; `%s` holds every one whole. Git ignores it: it is in the checkout that lifted, never committed.\n\n", report)

	p("## Binding-rule candidates\n\n")
	p("Disposition is one of: `kept in AGENTS.md` · `gated by <cmd>` · `ADR-NNNN` · `docs/<page>` · `dropped: <reason>`.\n\n")
	if len(r.Candidates) == 0 {
		p("None found (no CLAUDE.md list items, no project-context \"don't miss\" section, no team-memory gotchas).\n\n")
	} else {
		p("| # | Candidate | Source | Disposition |\n|---|---|---|---|\n")
		for i, c := range r.Candidates {
			if i == lim.rules {
				p("\n… and %d more candidate(s), numbered on from %d, past this file's %d KB ceiling: in `%s` (rule_candidates), in the checkout that lifted\n",
					len(r.Candidates)-i, i+1, liftDispositionCeiling>>10, report)
				break
			}
			p("| %d | %s | `%s` | |\n", i+1, cell(c.Text), c.Source)
		}
		p("\n")
	}

	p("## Gate commands\n\n")
	switch r.Gates.Source {
	case "":
		p("No `## Code Validation` section was found: AGENTS.md has no commands and `regression:` is not set.\n\n")
	default:
		p("From `%s` at %s, into AGENTS.md `## Commands`:\n\n", r.Gates.Source, LiftTag)
		for _, c := range r.Gates.Commands {
			p("- `%s`\n", strings.ReplaceAll(c, "\n", " ⏎ "))
		}
		if r.Gates.Regression != "" {
			p("\n`regression:` in _apex/config.yaml is `%s` — check it is the suite a change must keep green.\n", r.Gates.Regression)
		}
		for _, c := range r.Gates.NotRunnable {
			p("- not in `regression:` (it has a placeholder): `%s`\n", c)
		}
		p("\n")
	}

	// What can break the build or a guard comes before what only reads.
	writeV0Readers(&b, r.V0StateReaders, lim.readerCounts, report)
	readers := map[string]bool{}
	for _, v := range r.V0StateReaders {
		readers[v.File] = true
	}
	code, records := lf.splitRefs(r.DanglingRefs)
	code = slices.DeleteFunc(code, func(x LiftIDRef) bool { return readers[x.File] })
	p("## Code and CI that still read removed paths\n\n")
	if len(code) == 0 {
		p("None.\n\n")
	} else {
		p("Kept code, scripts and CI files that name a path the lift removed in a comment, or in a file that is not code (.json, .gitignore, …): repoint or drop them (%d reference(s) in %d file(s); per file, the count and the first; the readers above are not repeated):\n\n", len(code), countFiles(code))
		writeFirstRefs(&b, code, lim.files, report)
	}
	if len(r.Hashed) > 0 {
		p("Edited files a test, CI script or lock file seems to pin the bytes of (re-run that guard; if it is red, the file belonged under --exclude):\n\n")
		lf.writeHashed(&b, lim.files, report)
		p("\n")
	}
	if len(r.GoUnformatted) > 0 {
		p("Go files that were not gofmt-clean at %s: edited, not formatted — `gofmt -l` flags them as it did before:\n\n", LiftTag)
		writeList(&b, r.GoUnformatted, lim.files, report)
	}

	p("## ADRs\n\n")
	if sa := r.StandardADR; sa != nil {
		p("[%s](%s) is APEX's standard ADR, Record architecture decisions (%s), written by the lift%s.\n\n",
			StandardADRID, strings.TrimPrefix(sa.Path, lf.devRel+"/"), standardSourceNote(sa), standardReplacesNote(sa))
	}
	if len(r.ADRs) == 0 {
		p("No ADRs.\n\n")
	} else {
		writeADRTable(&b, r.ADRs, lim.adrs, lf.devRel, report)
	}
	if r.Canon != "" {
		p("Canon: %s.\n\n", r.Canon)
	}
	for _, f := range r.ADRsNotConverted {
		p("- not converted (no ADR id in it), left in place: `%s`\n", f)
	}
	if len(r.UnresolvedCitations) > 0 {
		p("ADR citations that name no ADR of the project's, left as written — another log's short id (ADR-08) or a number no record has (%d in %d file(s)):\n\n",
			len(r.UnresolvedCitations), countFiles(r.UnresolvedCitations))
		writeCounts(&b, r.UnresolvedCitations, func(x LiftIDRef) string { return x.Text }, lim.files, report)
	}

	p("## Ids left in code\n\n")
	p("The lift edits comments only (D37). These ids sit in identifiers, strings, directives, data-shaped comments, comments that use one as a possessive (\"Story 3.2's\": no removal keeps that sentence) or files it does not edit:\n")
	p("rename them in a change if they should go. An ADR id here still has its OLD number.\n\n")
	if len(r.IDsLeft) == 0 {
		p("None.\n\n")
	} else {
		p("%d id(s) in %d file(s), by file and kind:\n\n", len(r.IDsLeft), countFiles(r.IDsLeft))
		writeCounts(&b, r.IDsLeft, func(x LiftIDRef) string { return x.Where }, lim.files, report)
	}
	writeSkipped(&b, r.Skipped, lim.files, report)

	p("## References to removed paths in records and docs\n\n")
	if len(records) == 0 {
		p("None.\n\n")
	} else {
		p("Evidence logs, research notes and docs that name a removed path (%d reference(s) in %d file(s)); they read at %s. Counted, not listed:\n\n",
			len(records), countFiles(records), LiftTag)
		writeCounts(&b, records, nil, lim.files, report)
	}
	if len(r.KeptApex) > 0 {
		p("Left under `_apex/` (not apex-v0 framework tables): %s\n\n", "`"+strings.Join(r.KeptApex, "`, `")+"`")
	}

	p("## Readings at %s\n\n", LiftTag)
	if len(r.Readings) == 0 {
		p("None found.\n")
	}
	for _, s := range r.Readings {
		p("- %s\n", s)
	}
	// No regression runs: the section says why, in the lift commit (RG-6).
	if lf.regression != nil {
		writeRegression(&b, lf.regression, report, false)
	}
	return b.String()
}

// writeRegression is the disposition file's `## Regression` (RG-6): the
// lift's regression verdict and note, in the names the v1.0.0 migration
// entry reads, then one line per command with its exits and logs, each
// with its note. compact (when the file would pass its ceiling) keeps only
// the worst command's note — the one that entry branches on ("failing as
// it did there", "the output changed after the lift"), which the .md must
// carry, the JSON being in the lifting checkout only (RG-6a, D259); the
// report has every one.
func writeRegression(b *strings.Builder, rec *LiftRegressionRecord, report string, compact bool) {
	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	p("\n## Regression\n\n")
	if rec.Verdict == "" {
		p("Verdict: none — %s; nothing was compared with a run at %s.\n", rec.Note, LiftTag)
		return
	}
	p("The `regression:` command ran at %s, before the lift wrote anything, and at the lift commit (%s); the second run is judged against the first. ", LiftTag, short(rec.Commit))
	p("The whole record is `regression` in `%s`, in the checkout that lifted.\n\n", report)
	worst := 0
	for i := range rec.Commands {
		if liftVerdictRank(rec.Commands[i].Verdict) == liftVerdictRank(rec.Verdict) {
			worst = i
			break
		}
	}
	if n := len(rec.Commands); n > 1 {
		// The notes are on the commands' lines: the chain's is not repeated.
		p("Verdict: `%s` — the worst of the %d commands below, each run on its own at both points, past a red one: %d, `%s`.\n\n",
			rec.Verdict, n, worst+1, strings.ReplaceAll(rec.Commands[worst].Cmd, "\n", " ⏎ "))
	} else {
		p("Verdict: `%s` — %s\n\n", rec.Verdict, rec.Note)
	}
	if len(rec.NewFailures) > 0 {
		p("regression_new_failures: `%s`\n\n", strings.Join(rec.NewFailures, "`, `"))
	}
	for i, c := range rec.Commands {
		at := "not run at " + LiftTag
		if c.ExitAtTag != nil {
			at = fmt.Sprintf("exit %d at %s", *c.ExitAtTag, LiftTag)
		}
		p("%d. `%s` — `%s`: %s, exit %d after the lift", i+1, strings.ReplaceAll(c.Cmd, "\n", " ⏎ "), c.Verdict, at, c.ExitAfter)
		if len(c.NewFailures) > 0 {
			p("; regression_new_failures: `%s`", strings.Join(c.NewFailures, "`, `"))
		}
		p(". Logs: `%s`", c.Log)
		if c.LogAtTag != "" {
			p(", `%s`", c.LogAtTag)
		}
		if len(rec.Commands) > 1 && (!compact || i == worst) {
			p(". %s", c.Note)
		}
		p("\n")
	}
}

// writeADRTable is the disposition's ADR table: old → new, status and
// gaps, at most maxRows rows (all of them when maxRows < 0).
func writeADRTable(b *strings.Builder, adrs []LiftADR, maxRows int, devRel, report string) {
	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	p("| Old | New | Status | Gaps |\n|---|---|---|---|\n")
	// Short of room, the ADRs with gaps are the ones listed: every
	// old → new is in the lift commit's message too.
	var shown, rest []*LiftADR
	for i := range adrs {
		if a := &adrs[i]; maxRows < 0 || len(a.Gaps) > 0 && len(shown) < maxRows {
			shown = append(shown, a)
		} else {
			rest = append(rest, a)
		}
	}
	for _, a := range shown {
		old := a.Old
		if a.Canon {
			old += " (canon)"
		}
		p("| %s | [%s](%s) | %s | %s |\n", old, a.New, strings.TrimPrefix(a.Path, devRel+"/"), cell(a.Status), cell(strings.Join(a.Gaps, "; ")))
	}
	if len(rest) > 0 {
		p("\n… and %d more ADR(s); every old → new is in the lift commit's message and `%s`\n", len(rest), report)
	}
	p("\n")
}

// writeSkipped summarises the files the lift did not edit: a count per
// reason, then the files left for a reason other than their type.
func writeSkipped(b *strings.Builder, skipped []LiftSkip, maxFiles int, report string) {
	if len(skipped) == 0 {
		return
	}
	reasons := map[string]int{}
	var order, other []string
	for _, sk := range skipped {
		why := sk.Reason
		if i := strings.Index(why, "):"); i > 0 && strings.HasPrefix(why, "not tokenized") {
			why = why[:i+1]
		} else {
			other = append(other, fmt.Sprintf("%#q — %s (%d id(s), %d ADR citation(s))", sk.File, sk.Reason, sk.IDs, sk.ADRCitations))
		}
		if reasons[why] == 0 {
			order = append(order, why)
		}
		reasons[why]++
	}
	fmt.Fprintf(b, "Files not edited (%d), by reason:\n\n", len(skipped))
	for _, why := range order {
		fmt.Fprintf(b, "- %d × %s\n", reasons[why], why)
	}
	b.WriteString("\n")
	if len(other) == 0 {
		return
	}
	b.WriteString("Left for a reason other than the file type:\n\n")
	for i, l := range other {
		if i == maxFiles {
			fmt.Fprintf(b, "- … and %d more (in `%s`)\n", len(other)-maxFiles, report)
			break
		}
		fmt.Fprintf(b, "- %s\n", l)
	}
	b.WriteString("\n")
}

// splitRefs parts references to removed paths into those in code and CI
// (to act on) and those in records and docs: the development folder,
// docs/, and prose and log files.
func (lf *lifter) splitRefs(refs []LiftIDRef) (code, records []LiftIDRef) {
	for _, r := range refs {
		ext := strings.ToLower(path.Ext(r.File))
		switch {
		case strings.HasPrefix(r.File, lf.devRel+"/"), strings.HasPrefix(r.File, "docs/"),
			ext == ".md", ext == ".markdown", ext == ".txt", ext == ".log", ext == ".jsonl", ext == ".rst", ext == ".adoc":
			records = append(records, r)
		default:
			code = append(code, r)
		}
	}
	return code, records
}

func countFiles(refs []LiftIDRef) int {
	seen := map[string]bool{}
	for _, r := range refs {
		seen[r.File] = true
	}
	return len(seen)
}

// byFile groups refs by file, files sorted by count (most first), then
// name.
func byFile(refs []LiftIDRef) (files []string, m map[string][]LiftIDRef) {
	m = map[string][]LiftIDRef{}
	for _, r := range refs {
		if _, ok := m[r.File]; !ok {
			files = append(files, r.File)
		}
		m[r.File] = append(m[r.File], r)
	}
	sort.Slice(files, func(i, j int) bool {
		if a, b := len(m[files[i]]), len(m[files[j]]); a != b {
			return a > b
		}
		return files[i] < files[j]
	})
	return files, m
}

// writeFirstRefs writes one line per file, at most maxFiles: its count
// and its first reference.
func writeFirstRefs(b *strings.Builder, refs []LiftIDRef, maxFiles int, report string) {
	files, m := byFile(refs)
	for fi, f := range files {
		if fi == maxFiles {
			fmt.Fprintf(b, "- … and %d more file(s) (in `%s`)\n", len(files)-maxFiles, report)
			break
		}
		first := slices.MinFunc(m[f], func(x, y LiftIDRef) int { return x.Line - y.Line })
		fmt.Fprintf(b, "- `%s` — %d, first at line %d: `%s`\n", f, len(m[f]), first.Line, strings.ReplaceAll(first.Text, "`", "'"))
	}
	b.WriteString("\n")
}

// writeCounts writes one line per file: its count, and with key the
// count of each key ("identifier ×3, string ×2").
func writeCounts(b *strings.Builder, refs []LiftIDRef, key func(LiftIDRef) string, maxFiles int, report string) {
	files, m := byFile(refs)
	for fi, f := range files {
		if fi == maxFiles {
			rest := 0
			for _, g := range files[maxFiles:] {
				rest += len(m[g])
			}
			fmt.Fprintf(b, "- … and %d more in %d more file(s) (in `%s`)\n", rest, len(files)-maxFiles, report)
			break
		}
		rs := m[f]
		if key == nil {
			fmt.Fprintf(b, "- `%s` — %d\n", f, len(rs))
			continue
		}
		counts := map[string]int{}
		var order []string
		for _, r := range rs {
			k := strings.ReplaceAll(key(r), "`", "'")
			if counts[k] == 0 {
				order = append(order, k)
			}
			counts[k]++
		}
		parts := make([]string, 0, len(order))
		for _, k := range order {
			if counts[k] > 1 {
				parts = append(parts, fmt.Sprintf("%s ×%d", k, counts[k]))
			} else {
				parts = append(parts, k)
			}
		}
		fmt.Fprintf(b, "- `%s` — %d: %s\n", f, len(rs), strings.Join(parts, ", "))
	}
	b.WriteString("\n")
}

func writeList(b *strings.Builder, items []string, maxItems int, report string) {
	for i, it := range items {
		if i == maxItems {
			fmt.Fprintf(b, "- … and %d more (in `%s`)\n", len(items)-maxItems, report)
			break
		}
		fmt.Fprintf(b, "- `%s`\n", it)
	}
	b.WriteString("\n")
}

// writeHashed lists the hashed edits: those a lock file or a hasher names
// one line per file, at most hashedCap and maxFiles; then those only a
// folder ties to a hasher, one line per hasher and folder (R22: mdpdf's
// 57 files beside one guard that hashes its fixtures, not its sources,
// were 50 lines of the file).
func (lf *lifter) writeHashed(b *strings.Builder, maxFiles int, report string) {
	var named []LiftHashed
	type group struct {
		by, dir string
		n       int
	}
	var groups []*group
	at := map[[2]string]*group{}
	for _, h := range lf.hashedAll {
		if lf.hashedRank[h.File] != hashedByFolder {
			named = append(named, h)
			continue
		}
		dir := path.Dir(h.File)
		for _, by := range h.By {
			k := [2]string{by, dir}
			if at[k] == nil {
				at[k] = &group{by: by, dir: dir}
				groups = append(groups, at[k])
			}
			at[k].n++
		}
	}
	shown := min(len(named), hashedCap, max(maxFiles, 1))
	for _, h := range named[:shown] {
		fmt.Fprintf(b, "- `%s` — %s\n", h.File, "`"+strings.Join(h.By, "`, `")+"`")
	}
	if more := len(named) - shown; more > 0 {
		fmt.Fprintf(b, "- … and %d more (in `%s`)\n", more, report)
	}
	for _, g := range groups {
		if path.Dir(g.by) == g.dir {
			fmt.Fprintf(b, "- %s in `%s` share a folder with `%s` (folder match only)\n", nFiles(g.n), g.dir, path.Base(g.by))
		} else {
			fmt.Fprintf(b, "- %s in `%s` sit in a folder `%s` names near a hash (folder match only)\n", nFiles(g.n), g.dir, g.by)
		}
	}
}

// nFiles is "1 file" or "n files".
func nFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return strconv.Itoa(n) + " files"
}

// v0ReaderReadsShown is how many paths a reader's line names; the report
// has every one.
const v0ReaderReadsShown = 10

// writeV0Readers is the disposition's section on code and tests that read
// deleted v0 state (D141): one line per file with what it reads, never cut
// to fit the cap; as counts, saying so, past the ceiling.
func writeV0Readers(b *strings.Builder, readers []LiftV0Reader, counts bool, report string) {
	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	p("## Code and tests that read deleted apex-v0 state\n\n")
	if len(readers) == 0 {
		p("None.\n\n")
		return
	}
	p("Kept code, tests, scripts and CI files that name apex-v0 state the lift deletes outside their comments — the tracker, ADR folders, deferred records — so they fail once it is gone: re-point each to its apex-v1 state, or retire it (%s; it reads it at %s):\n\n",
		nFiles(len(readers)), LiftTag)
	if !counts {
		for _, v := range readers {
			shown := v.Reads[:min(len(v.Reads), v0ReaderReadsShown)]
			line := "`" + strings.Join(shown, "`, `") + "`"
			if more := len(v.Reads) - len(shown); more > 0 {
				line += fmt.Sprintf(" and %d more (in %#q)", more, report)
			}
			p("- `%s` reads %s\n", v.File, line)
		}
		p("\n")
		return
	}
	n := map[string]int{}
	var paths []string
	for _, v := range readers {
		for _, r := range v.Reads {
			if n[r] == 0 {
				paths = append(paths, r)
			}
			n[r]++
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		if a, b := n[paths[i]], n[paths[j]]; a != b {
			return a > b
		}
		return paths[i] < paths[j]
	})
	p("Counted, not listed, past this file's %d KB ceiling: every file and what it reads is in `%s` (v0_state_readers), in the checkout that lifted. By what they read:\n\n", liftDispositionCeiling>>10, report)
	for i, r := range paths {
		if i == v0ReaderReadsShown {
			p("- … and %d more path(s)\n", len(paths)-i)
			break
		}
		p("- `%s` — %s\n", r, nFiles(n[r]))
	}
	p("\n")
}

// standardSourceNote names where the standard's text came from.
func standardSourceNote(sa *StandardADRResult) string {
	if sa.Source == StandardSourceApe {
		return "ape's own copy: no framework payload was installed"
	}
	if sa.FrameworkVersion != "" {
		return sa.Source + ", framework " + sa.FrameworkVersion
	}
	return sa.Source
}
