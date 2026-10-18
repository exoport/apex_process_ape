package apexv1

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// D47 Q4: the lift writes development/lift-disposition.md, committed in
// the lift commit. apex-plan fills its disposition column, a fresh
// read-only session reviews it, then the person does; the person deletes
// it. It holds the binding-rule candidates — the rules v0 kept in
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
// reader's cap. The full lists go to lift-disposition.json beside it.
const (
	// summaryFiles bounds how many files a summary names; the rest are
	// counted, and the JSON report has them all.
	summaryFiles = 40
	// codeRefsPerFile bounds the code and CI references listed for one
	// file: those are the ones to act on, so they are shown, not counted.
	codeRefsPerFile = 10
)

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
	UnresolvedCitations []LiftIDRef      `json:"unresolved_adr_citations,omitempty"`
	IDsLeft             []LiftIDRef      `json:"ids_left,omitempty"`
	DanglingRefs        []LiftIDRef      `json:"references_to_removed_paths,omitempty"`
	Candidates          []LiftCandidate  `json:"rule_candidates"`
	Gates               LiftGates        `json:"gates"`
	Readings            []string         `json:"readings,omitempty"`
}

func (lf *lifter) report() string {
	r := lf.res
	data, err := json.MarshalIndent(liftReport{
		Version: r.Version, Tag: LiftTag, TagSHA: r.TagSHA, Deleted: r.Deleted, ADRs: r.ADRs, Changed: r.Changed,
		Skipped: r.Skipped, GoUnformatted: r.GoUnformatted, Hashed: lf.hashedAll, UnresolvedCitations: r.UnresolvedCitations,
		IDsLeft: r.IDsLeft, DanglingRefs: r.DanglingRefs, Candidates: r.Candidates, Gates: r.Gates, Readings: r.Readings,
	}, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(data) + "\n"
}

// disposition renders the disposition file.
func (lf *lifter) disposition() string {
	r := lf.res
	report := path.Base(r.Report)
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	p("# Lift disposition\n\n")
	p("Lifted from APEX v0 %s; the v0 state is archived at the tag `%s` (%s), read it as `git show %s:<path>`.\n\n", r.Version, LiftTag, short(r.TagSHA), LiftTag)
	p("This file is the lift's hand-off. apex-plan fills the Disposition column and carries each kept rule to its one home;\n")
	p("a fresh read-only session reviews the result; then the person does, and deletes this file and `%s`.\n", report)
	p("Long lists are summarised here; `%s`, beside this file, holds every one whole.\n\n", report)

	p("## Binding-rule candidates\n\n")
	p("Disposition is one of: `kept in AGENTS.md` · `gated by <cmd>` · `ADR-NNNN` · `docs/<page>` · `dropped: <reason>`.\n\n")
	if len(r.Candidates) == 0 {
		p("None found (no CLAUDE.md list items, no project-context \"don't miss\" section, no team-memory gotchas).\n\n")
	} else {
		p("| # | Candidate | Source | Disposition |\n|---|---|---|---|\n")
		for i, c := range r.Candidates {
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
	code, records := lf.splitRefs(r.DanglingRefs)
	p("## Code and CI that still read removed paths\n\n")
	if len(code) == 0 {
		p("None.\n\n")
	} else {
		p("Kept code, scripts and CI files that name a path the lift removed — guards over the v0 corpus, tests reading the tracker: retire or repoint them (%d reference(s) in %d file(s)):\n\n", len(code), countFiles(code))
		writeRefs(&b, code, codeRefsPerFile, summaryFiles, report)
	}
	if len(r.Hashed) > 0 {
		p("Edited files a test, CI script or lock file seems to pin the bytes of (re-run that guard; if it is red, the file belonged under --exclude):\n\n")
		for _, h := range r.Hashed {
			p("- `%s` — %s\n", h.File, "`"+strings.Join(h.By, "`, `")+"`")
		}
		if more := r.HashedTotal - len(r.Hashed); more > 0 {
			p("- … and %d more (in `%s`)\n", more, report)
		}
		p("\n")
	}
	if len(r.GoUnformatted) > 0 {
		p("Go files that were not gofmt-clean at %s: edited, not formatted — `gofmt -l` flags them as it did before:\n\n", LiftTag)
		writeList(&b, r.GoUnformatted, summaryFiles, report)
	}

	p("## ADRs\n\n")
	if len(r.ADRs) == 0 {
		p("No ADRs.\n\n")
	} else {
		p("| Old | New | Status | Gaps |\n|---|---|---|---|\n")
		for i := range r.ADRs {
			a := &r.ADRs[i]
			old := a.Old
			if a.Canon {
				old += " (canon)"
			}
			p("| %s | [%s](%s) | %s | %s |\n", old, a.New, strings.TrimPrefix(a.Path, lf.devRel+"/"), cell(a.Status), cell(strings.Join(a.Gaps, "; ")))
		}
		p("\n")
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
		writeCounts(&b, r.UnresolvedCitations, func(x LiftIDRef) string { return x.Text }, summaryFiles, report)
	}

	p("## Ids left in code\n\n")
	p("The lift edits comments only (D37). These ids sit in identifiers, strings, directives, data-shaped comments, comments that use one as a possessive (\"Story 3.2's\": no removal keeps that sentence) or files it does not edit:\n")
	p("rename them in a change if they should go. An ADR id here still has its OLD number.\n\n")
	if len(r.IDsLeft) == 0 {
		p("None.\n\n")
	} else {
		p("%d id(s) in %d file(s), by file and kind:\n\n", len(r.IDsLeft), countFiles(r.IDsLeft))
		writeCounts(&b, r.IDsLeft, func(x LiftIDRef) string { return x.Where }, summaryFiles, report)
	}
	writeSkipped(&b, r.Skipped, report)

	p("## References to removed paths in records and docs\n\n")
	if len(records) == 0 {
		p("None.\n\n")
	} else {
		p("Evidence logs, research notes and docs that name a removed path (%d reference(s) in %d file(s)); they read at %s. Counted, not listed:\n\n",
			len(records), countFiles(records), LiftTag)
		writeCounts(&b, records, nil, summaryFiles, report)
	}
	if len(r.KeptApex) > 0 {
		p("Left under `_apex/` (not v0 framework tables): %s\n\n", "`"+strings.Join(r.KeptApex, "`, `")+"`")
	}

	p("## Readings at %s\n\n", LiftTag)
	if len(r.Readings) == 0 {
		p("None found.\n")
	}
	for _, s := range r.Readings {
		p("- %s\n", s)
	}
	return b.String()
}

// writeSkipped summarises the files the lift did not edit: a count per
// reason, then the files left for a reason other than their type.
func writeSkipped(b *strings.Builder, skipped []LiftSkip, report string) {
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
		if i == summaryFiles {
			fmt.Fprintf(b, "- … and %d more (in `%s`)\n", len(other)-summaryFiles, report)
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

// writeRefs lists refs grouped by file, at most perFile per file and
// maxFiles files; the rest are counted.
func writeRefs(b *strings.Builder, refs []LiftIDRef, perFile, maxFiles int, report string) {
	files, m := byFile(refs)
	for fi, f := range files {
		if fi == maxFiles {
			fmt.Fprintf(b, "- … and %d more file(s) (in `%s`)\n", len(files)-maxFiles, report)
			break
		}
		rs := m[f]
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Line < rs[j].Line })
		for i, r := range rs {
			if i == perFile {
				fmt.Fprintf(b, "- `%s`: … and %d more\n", f, len(rs)-perFile)
				break
			}
			fmt.Fprintf(b, "- `%s:%d` `%s` — %s\n", f, r.Line, strings.ReplaceAll(r.Text, "`", "'"), r.Where)
		}
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
