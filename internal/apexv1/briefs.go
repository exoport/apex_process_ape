package apexv1

import (
	"context"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// D39: planning inputs are dated, immutable files. Every apex-plan run
// saves its input (the brief or the request, verbatim) as
// development/briefs/YYYY-MM-DD-<slug>.md, committed with the plan, and
// the roadmap's `Delivery:` line links the current one. project.md and
// the specs stay the living intent; a brief is history and is never
// edited. The pre-tool hook denies edits to an existing brief; project
// check covers the rest (links, names, and edits the hook cannot see).

// BriefPattern is the name every brief has.
const BriefPattern = "YYYY-MM-DD-<slug>.md"

var briefNameRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-[a-z0-9][a-z0-9-]*\.md$`)

// validBriefName reports whether name is YYYY-MM-DD-<slug>.md with a real date.
func validBriefName(name string) bool {
	m := briefNameRe.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	_, err := time.Parse("2006-01-02", m[1])
	return err == nil
}

// briefFiles lists the files under development/briefs/ at any depth,
// sorted. Dotfiles (init's .gitkeep) are not briefs.
func (p *Project) briefFiles() []string {
	root := p.BriefsDir()
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir // absent or unreadable: nothing to list there
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// briefLink is one brief a roadmap `Delivery:` line points at.
type briefLink struct {
	Target string
	Line   int
}

var (
	// [text](target) or [text](<target> "title").
	mdLinkRe = regexp.MustCompile(`\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	// A bare path token ending in .md with a briefs/ segment.
	bareBriefRe = regexp.MustCompile("(?:^|[\\s(\\[`'\"])((?:[A-Za-z0-9_.-]+/)*briefs/[^\\s()\\[\\]`'\"<>,;]+\\.md)")
)

// isBriefTarget reports whether a link target names a brief.
func isBriefTarget(t string) bool {
	if strings.Contains(t, "://") || !strings.HasSuffix(t, ".md") {
		return false
	}
	return strings.HasPrefix(t, "briefs/") || strings.Contains(t, "/briefs/")
}

// deliveryLinks finds the roadmap's `Delivery:` lines (outside code) and
// the brief links on them: Markdown links whose target names a brief, and
// bare briefs/….md path tokens.
func deliveryLinks(doc string) (lines []int, links []briefLink) {
	for _, l := range splitLines(doc) {
		if l.InCode {
			continue
		}
		t := strings.TrimLeft(strings.TrimSpace(l.Text), "*_")
		if !strings.HasPrefix(t, "Delivery:") {
			continue
		}
		lines = append(lines, l.N)
		for _, m := range mdLinkRe.FindAllStringSubmatch(t, -1) {
			target, _, _ := strings.Cut(m[1], "#")
			if isBriefTarget(target) {
				links = append(links, briefLink{Target: target, Line: l.N})
			}
		}
		rest := mdLinkRe.ReplaceAllString(t, " ")
		for _, m := range bareBriefRe.FindAllStringSubmatch(rest, -1) {
			links = append(links, briefLink{Target: m[1], Line: l.N})
		}
	}
	return lines, links
}

// briefResolves reports whether a Delivery: link names an existing file:
// relative to roadmap.md's folder, or (unless it starts with ./ or ../)
// to the project root.
func (p *Project) briefResolves(target string) bool {
	if u, err := url.PathUnescape(target); err == nil {
		target = u
	}
	rel := filepath.FromSlash(target)
	cands := []string{filepath.Join(filepath.Dir(p.RoadmapMD()), rel)}
	if !strings.HasPrefix(target, "./") && !strings.HasPrefix(target, "../") {
		cands = append(cands, filepath.Join(p.Root, rel))
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// briefLinkFor is how roadmap.md links brief: relative to its folder.
func (p *Project) briefLinkFor(brief string) string {
	rel, err := filepath.Rel(filepath.Dir(p.RoadmapMD()), brief)
	if err != nil {
		rel = p.Rel(brief)
	}
	return "[" + filepath.Base(brief) + "](" + filepath.ToSlash(rel) + ")"
}

// currentBrief is the newest well-named brief (by its date and name), or
// the last one when none is well named.
func currentBrief(briefs []string) string {
	cur := ""
	for _, b := range briefs {
		if validBriefName(filepath.Base(b)) && (cur == "" || filepath.Base(b) > filepath.Base(cur)) {
			cur = b
		}
	}
	if cur == "" && len(briefs) > 0 {
		cur = briefs[len(briefs)-1]
	}
	return cur
}

// briefFindings checks development/briefs/ and the roadmap's Delivery: link.
func (p *Project) briefFindings(ctx context.Context) []Finding {
	briefs := p.briefFiles()
	out := p.deliveryFindings(briefs)
	gitOK := HeadSHA(ctx, p.Root) != ""
	for _, b := range briefs {
		rel := p.Rel(b)
		if !validBriefName(filepath.Base(b)) {
			out = append(out, Finding{File: rel, Message: "brief file name is not " + BriefPattern +
				" (a real date, a kebab-case slug) — rename it with git mv, and update the roadmap's Delivery: link"})
		}
		if gitOK {
			out = append(out, p.briefHistoryFindings(ctx, b, rel)...)
		}
	}
	return out
}

// deliveryFindings checks the roadmap's Delivery: links against briefs.
// No roadmap is file.missing's finding, not this one's.
func (p *Project) deliveryFindings(briefs []string) []Finding {
	doc, err := readFileString(p.RoadmapMD())
	if err != nil {
		return nil
	}
	var out []Finding
	file := p.Rel(p.RoadmapMD())
	lines, links := deliveryLinks(doc)
	fix := "save the planning input as " + p.DevRel + "/briefs/" + BriefPattern + " and link it"
	if cur := currentBrief(briefs); cur != "" {
		fix = "link an existing brief, relative to roadmap.md: " + p.briefLinkFor(cur)
	}
	for _, l := range links {
		if !p.briefResolves(l.Target) {
			out = append(out, Finding{
				File: file, Line: l.Line,
				Message: "`Delivery:` links " + l.Target + ", which does not resolve to a file — " + fix,
			})
		}
	}
	if len(links) == 0 && len(briefs) > 0 {
		line := 0
		if len(lines) > 0 {
			line = lines[0]
		}
		out = append(out, Finding{
			File: file, Line: line,
			Message: "no `Delivery:` line links a brief, but " + p.DevRel + "/briefs/ holds " + strconv.Itoa(len(briefs)) +
				" — link the current one under `# Roadmap`: Delivery: … " + p.briefLinkFor(currentBrief(briefs)),
		})
	}
	return out
}

// shortSHA is how many hex digits a finding's restore command uses.
const shortSHA = 12

// briefHistoryFindings holds a brief to the text of the commit that added
// it: this catches the edits the pre-tool hook cannot see (Bash, an
// editor), whether committed since or still in the working tree.
func (p *Project) briefHistoryFindings(ctx context.Context, path, rel string) []Finding {
	newBrief := p.DevRel + "/briefs/" + BriefPattern
	out, err := git(ctx, p.Root, "log", "--diff-filter=A", "--format=%H", "--", rel)
	if err != nil {
		return nil
	}
	if out == "" {
		return []Finding{{File: rel, Message: "brief is not committed — briefs are committed with the plan they are the input to: commit it"}}
	}
	shas := strings.Split(out, "\n")
	added := shas[len(shas)-1] // the first time the path was added
	orig, err := gitRaw(ctx, p.Root, "show", added+":./"+rel)
	if err != nil {
		return nil
	}
	cur, err := readFileString(path)
	if err != nil {
		return nil
	}
	norm := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	if norm(cur) == norm(orig) {
		return nil
	}
	short := added
	if len(short) > shortSHA {
		short = short[:shortSHA]
	}
	return []Finding{{File: rel, Message: "brief differs from the text it was committed with (" + short +
		") — briefs are history: restore it (git show " + short + ":./" + rel + " > " + rel +
		") and put the new or changed request in a new dated brief, " + newBrief + ", linked from the roadmap's Delivery: line"}}
}

// briefReason names why an existing brief may not be edited, or "" when
// abs is not one. Writing a new file under briefs/ is allowed. abs, its
// parent-resolved and its fully resolved forms are all tried, so a link
// into or out of the folder does not hide a brief.
func (p *Project) briefReason(abs string) string {
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		return ""
	}
	dirs := []string{p.BriefsDir()}
	if r, err := filepath.EvalSymlinks(p.BriefsDir()); err == nil && r != dirs[0] {
		dirs = append(dirs, r)
	}
	targets := []string{abs}
	if r, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		targets = append(targets, filepath.Join(r, filepath.Base(abs)))
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		targets = append(targets, r)
	}
	for _, dir := range dirs {
		for _, t := range targets {
			rel, err := filepath.Rel(dir, t)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			return p.DevRel + "/briefs/" + filepath.ToSlash(rel) +
				" is a brief: briefs are history, the planning input as it was given, and are never edited — " +
				"a new or changed request is a new dated brief (" + p.DevRel + "/briefs/" + BriefPattern +
				"), linked from the roadmap's Delivery: line"
		}
	}
	return ""
}
