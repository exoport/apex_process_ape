package apexv1

import (
	"fmt"
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

// listCap bounds a per-file list in the Markdown; the JSON report is whole.
const listCap = 20

// disposition renders the disposition file.
func (lf *lifter) disposition() string {
	r := lf.res
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	p("# Lift disposition\n\n")
	p("Lifted from APEX v0 %s; the v0 state is archived at the tag `%s` (%s), read it as `git show %s:<path>`.\n\n", r.Version, LiftTag, short(r.TagSHA), LiftTag)
	p("This file is the lift's hand-off. apex-plan fills the Disposition column and carries each kept rule to its one home;\n")
	p("a fresh read-only session reviews the result; then the person does, and deletes this file.\n\n")

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
		p("ADR citations that name no ADR (left as written):\n\n")
		writeRefs(&b, r.UnresolvedCitations)
	}

	p("## Ids left in code\n\n")
	p("The lift edits comments only (D37). These ids sit in identifiers, strings, directives or files it does not edit:\n")
	p("rename them in a change if they should go. An ADR id here still has its OLD number.\n\n")
	if len(r.IDsLeft) == 0 {
		p("None.\n\n")
	} else {
		writeRefs(&b, r.IDsLeft)
	}
	if len(r.Skipped) > 0 {
		p("Files not edited:\n\n")
		for _, s := range r.Skipped {
			p("- `%s` — %s (%d id(s), %d ADR citation(s))\n", s.File, s.Reason, s.IDs, s.ADRCitations)
		}
		p("\n")
	}

	if len(r.GoUnformatted) > 0 {
		p("Go files that were not gofmt-clean at %s: edited, not formatted — `gofmt -l` flags them as it did before:\n\n", LiftTag)
		for _, f := range r.GoUnformatted {
			p("- `%s`\n", f)
		}
		p("\n")
	}

	p("## References to removed paths\n\n")
	if len(r.DanglingRefs) == 0 {
		p("None.\n\n")
	} else {
		p("Kept files that still name a path the lift removed — CI guards and scripts that police the v0 corpus show up here; retire or repoint them:\n\n")
		writeRefs(&b, r.DanglingRefs)
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

// writeRefs lists refs grouped by file, at most listCap per file.
func writeRefs(b *strings.Builder, refs []LiftIDRef) {
	byFile := map[string][]LiftIDRef{}
	var files []string
	for _, r := range refs {
		if _, ok := byFile[r.File]; !ok {
			files = append(files, r.File)
		}
		byFile[r.File] = append(byFile[r.File], r)
	}
	sort.Strings(files)
	for _, f := range files {
		rs := byFile[f]
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Line < rs[j].Line })
		for i, r := range rs {
			if i == listCap {
				fmt.Fprintf(b, "- `%s`: … and %d more\n", f, len(rs)-listCap)
				break
			}
			fmt.Fprintf(b, "- `%s:%d` `%s` — %s\n", f, r.Line, strings.ReplaceAll(r.Text, "`", "'"), r.Where)
		}
	}
	b.WriteString("\n")
}
