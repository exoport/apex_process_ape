package apexv1

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"gopkg.in/yaml.v3"
)

// D36: every v0 ADR — accepted, superseded and proposed — becomes a v1
// decision, renumbered contiguously from 0001 in its original order, and
// every citation of an old id is rewritten to the new one. Nothing is
// invented: a field the v0 record lacks is either derived from something
// it has (the date from git), defaulted to what v1 itself writes for a new
// decision (`Enforced by: review`), or marked as not recorded — and each
// of those is listed as a gap.

// LiftADR is one converted decision.
type LiftADR struct {
	Old    string   `json:"old"`
	New    string   `json:"new"`
	From   string   `json:"from"`
	Path   string   `json:"path"`
	Status string   `json:"status"`
	Canon  bool     `json:"canon,omitempty"`
	Gaps   []string `json:"gaps,omitempty"`
	doc    string
	// unresolved are the ADR citations of the record that name no ADR.
	unresolved []string
}

// v0ADR is one v0 ADR record as read.
type v0ADR struct {
	num        int
	rel        string // project-relative, or the canon path
	slug       string
	title      string
	status     string
	date       string
	supersedes []int
	superBy    int
	body       string
	canon      bool
	// source is the record's output_document: for a canon import, its
	// path in the governance repository (isV0Standard).
	source string
}

var (
	adrNameRe    = regexp.MustCompile(`(?i)^adr-(\d+)(?:[_-](.*))?\.md$`)
	adrIDValueRe = regexp.MustCompile(`(?i)^ADR-(\d+)$`)
	adrH1NumRe   = regexp.MustCompile(`(?i)^ADR-(\d+)\s*[:—–-]?\s*`)
	dateValueRe  = regexp.MustCompile(`^(\d{4})-?(\d{2})-?(\d{2})`)
	slugCleanRe  = regexp.MustCompile(`[^a-z0-9]+`)
)

// fmScalar reads a top-level scalar of a frontmatter mapping as written;
// fmList reads a scalar or a sequence.
func fmScalar(m *yaml.Node, key string) string {
	if v := fmValue(m, key); v != nil && v.Kind == yaml.ScalarNode {
		return strings.TrimSpace(v.Value)
	}
	return ""
}

func fmList(m *yaml.Node, key string) []string {
	v := fmValue(m, key)
	if v == nil {
		return nil
	}
	if v.Kind == yaml.ScalarNode {
		if s := strings.TrimSpace(v.Value); s != "" && s != "null" {
			return []string{s}
		}
		return nil
	}
	var out []string
	for _, c := range v.Content {
		if c.Kind == yaml.ScalarNode && strings.TrimSpace(c.Value) != "" {
			out = append(out, strings.TrimSpace(c.Value))
		}
	}
	return out
}

func fmValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func adrNumOf(s string) int {
	if m := adrIDValueRe.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func kebab(s string) string {
	return strings.Trim(slugCleanRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// parseV0ADR reads one record; ok is false when no id can be derived.
func parseV0ADR(rel, name, data string) (v0ADR, bool) {
	a := v0ADR{rel: rel}
	body := data
	var m *yaml.Node
	if fm, rest, err := frontmatter.Split([]byte(data)); err == nil {
		var doc yaml.Node
		if yaml.Unmarshal(fm, &doc) == nil && len(doc.Content) > 0 {
			m = doc.Content[0]
		}
		body = string(rest)
	}
	a.num = adrNumOf(fmScalar(m, "id"))
	nameSlug := ""
	if nm := adrNameRe.FindStringSubmatch(name); nm != nil {
		if a.num == 0 {
			a.num, _ = strconv.Atoi(nm[1])
		}
		nameSlug = nm[2]
	}
	// The first H1 is the title; v1 writes its own.
	lines := strings.Split(body, "\n")
	fence := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
		}
		if fence || !strings.HasPrefix(t, "# ") {
			continue
		}
		h := strings.TrimSpace(t[2:])
		if hm := adrH1NumRe.FindStringSubmatch(h); hm != nil {
			if a.num == 0 {
				a.num, _ = strconv.Atoi(hm[1])
			}
			h = strings.TrimSpace(h[len(hm[0]):])
		}
		a.title = h
		lines = append(lines[:i], lines[i+1:]...)
		break
	}
	if a.num == 0 {
		return a, false
	}
	if t := fmScalar(m, "title"); t != "" {
		a.title = t
	}
	a.slug = kebab(fmScalar(m, "slug"))
	if a.slug == "" {
		a.slug = kebab(nameSlug)
	}
	if a.slug == "" {
		a.slug = kebab(a.title)
	}
	if a.slug == "" {
		a.slug = "decision"
	}
	if a.title == "" {
		a.title = titleFromSlug(a.slug)
	}
	a.status = strings.ToLower(fmScalar(m, "status"))
	a.source = fmScalar(m, "output_document")
	for _, k := range []string{"date", "created_at", "accepted_at"} {
		if dm := dateValueRe.FindStringSubmatch(fmScalar(m, k)); dm != nil {
			a.date = dm[1] + "-" + dm[2] + "-" + dm[3]
			break
		}
	}
	for _, s := range fmList(m, "supersedes") {
		if n := adrNumOf(s); n > 0 {
			a.supersedes = append(a.supersedes, n)
		}
	}
	if bys := fmList(m, "superseded_by"); len(bys) > 0 {
		a.superBy = adrNumOf(bys[0])
	}
	a.body = strings.Trim(strings.Join(lines, "\n"), "\n")
	return a, true
}

// loadADRs reads the ADR records of a folder (not recursive; README,
// index and changelog files are not records). bad lists the files that
// look like records but give no id.
func loadADRs(root, dirAbs string, canon bool) (out []v0ADR, bad []string) {
	entries, err := os.ReadDir(dirAbs)
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".md") || strings.EqualFold(name, "README.md") {
			continue
		}
		path := filepath.Join(dirAbs, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rel := path
		if r, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(r, "..") {
			rel = filepath.ToSlash(r)
		}
		a, ok := parseV0ADR(rel, name, string(data))
		if !ok {
			if adrNameRe.MatchString(name) || frontmatter.Has(data) {
				bad = append(bad, rel)
			}
			continue
		}
		a.canon = canon
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].num != out[j].num {
			return out[i].num < out[j].num
		}
		return out[i].rel < out[j].rel
	})
	return out, bad
}

// findCanonADRs looks in the governance clone for the records of the
// cited numbers no local ADR has.
func findCanonADRs(govAbs string, want map[int]bool) []v0ADR {
	if govAbs == "" || len(want) == 0 {
		return nil
	}
	dirs := map[string]bool{}
	_ = filepath.WalkDir(govAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable folder holds no records
		}
		if d.IsDir() && (d.Name() == ".git" || strings.Count(strings.TrimPrefix(path, govAbs), string(filepath.Separator)) > 4) {
			return filepath.SkipDir
		}
		if m := adrNameRe.FindStringSubmatch(d.Name()); m != nil {
			if n, _ := strconv.Atoi(m[1]); want[n] {
				dirs[filepath.Dir(path)] = true
			}
		}
		return nil
	})
	var out []v0ADR
	seen := map[int]bool{}
	var ds []string
	for d := range dirs {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	for _, d := range ds {
		adrs, _ := loadADRs(govAbs, d, true)
		for i := range adrs {
			if n := adrs[i].num; want[n] && !seen[n] {
				seen[n] = true
				out = append(out, adrs[i])
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].num < out[j].num })
	return out
}

var headingAliases = map[string]string{
	"problem": "Context", "problem statement": "Context", "context and problem statement": "Context",
	"context & problem statement": "Context", "background": "Context",
	"decision outcome": "Decision", "the decision": "Decision",
	"implications": "Consequences",
}

// convertADR renders a v0 record as a v1 decision. num maps old numbers
// to new; date is the git fallback for a record with no date field.
func convertADR(a v0ADR, newNum int, rw *adrRewriter, gitDate string) LiftADR {
	id := "ADR-" + fmtNum4(newNum)
	res := LiftADR{Old: "ADR-" + fmtNum4(a.num), New: id, From: a.rel, Path: fmtNum4(newNum) + "-" + a.slug + ".md", Canon: a.canon}
	gap := func(s string) { res.Gaps = append(res.Gaps, s) }

	body, _, unresolved := rw.rewrite(a.body)
	res.unresolved = unresolved
	lines := splitLines(body)
	have := map[string]bool{}
	for _, s := range h2Sections(lines) {
		have[strings.ToLower(s.Title)] = true
	}
	raw := strings.Split(body, "\n")
	for _, l := range lines {
		lv, t := headingLevel(l)
		if lv != 2 {
			continue
		}
		if to, ok := headingAliases[strings.ToLower(t)]; ok && !have[strings.ToLower(to)] {
			raw[l.N-1] = "## " + to
			have[strings.ToLower(to)] = true
			gap("heading `## " + t + "` renamed `## " + to + "`")
		}
	}
	var sb strings.Builder
	sb.WriteString(strings.Join(raw, "\n"))
	for _, want := range []string{"Context", "Decision", "Consequences"} {
		if !have[strings.ToLower(want)] {
			sb.WriteString("\n\n## " + want + "\n_Not recorded in the v0 ADR; the section was added at the lift so the record has the v1 shape._")
			gap("no `## " + want + "` in the v0 record: marked not recorded")
		}
	}
	body = sb.String()
	enforced := false
	for _, l := range splitLines(body) {
		if !l.InCode && adrEnforceRe.MatchString(strings.TrimSpace(l.Text)) {
			enforced = true
		}
	}
	if !enforced {
		body += "\n\nEnforced by: review"
		gap("Enforced by: review (the v1 default; name the command or hook where a gate enforces it)")
	}

	status := a.status
	switch {
	case status == ADRAccepted || status == ADRProposed:
	case strings.HasPrefix(status, "superseded"):
		by := a.superBy
		if n := adrNumOf(strings.TrimSpace(strings.TrimPrefix(status, "superseded by"))); n > 0 && by == 0 {
			by = n
		}
		if nb, ok := rw.num[by]; ok && by > 0 {
			status = "superseded by ADR-" + fmtNum4(nb)
		} else {
			status = "superseded"
			gap("superseded, but by no ADR the lift could map: Status left `superseded` (not a v1 status)")
		}
	case status == "":
		gap("no status in the v0 record: Status left empty (not a v1 status)")
	default:
		gap("v0 status `" + status + "` kept as written (not a v1 status)")
	}
	res.Status = status
	date := a.date
	if date == "" {
		date = gitDate
		if date != "" {
			gap("no date in the v0 record: Date is the day git first recorded the file")
		} else {
			gap("no date in the v0 record and none in git")
		}
	}
	var head strings.Builder
	head.WriteString("# " + id + ": " + a.title + "\n")
	head.WriteString("Status: " + status + "\n")
	head.WriteString("Date: " + date + "\n")
	for _, s := range a.supersedes {
		if ns, ok := rw.num[s]; ok {
			head.WriteString("Supersedes: ADR-" + fmtNum4(ns) + "\n")
		} else {
			gap("supersedes ADR-" + fmtNum4(s) + ", which the lift could not map")
		}
	}
	origin := LiftTag
	if a.canon {
		origin = "the governance repository (canon)"
	}
	head.WriteString("Lifted from: " + res.Old + ", " + a.rel + " at " + origin + "\n")
	res.doc = head.String() + "\n" + strings.Trim(body, "\n") + "\n"
	for _, f := range ValidateDecision(res.Path, res.Path, res.doc) {
		gap("shape: " + f.Message)
	}
	return res
}

// gitFirstDate is the day git first recorded a file (YYYY-MM-DD), "" when
// it never did.
func gitFirstDate(ctx context.Context, root, rel string) string {
	out, err := git(ctx, root, "log", "--diff-filter=A", "--follow", "--format=%as", "--", rel)
	if err != nil || out == "" {
		return ""
	}
	lines := strings.Split(out, "\n")
	return lines[len(lines)-1]
}

// recordLiftedDecisions records the converted ADRs that are accepted (or
// were, before being superseded) in _apex/state/decisions.json, by
// LiftBy, so `ape project check` holds their text from here on. Not
// RecordLiftedDecisions (approval.go), which takes only ADRs accepted now:
// a superseded ADR was accepted once, and its text is held too.
func recordLiftedDecisions(p *Project, adrs []LiftADR) error {
	recs := p.loadDecisionRecords()
	for i := range adrs {
		a := &adrs[i]
		if a.Status != ADRAccepted && !strings.HasPrefix(a.Status, "superseded by ") {
			continue
		}
		recs[a.New] = DecisionRecord{AcceptedAt: Stamp(), By: LiftBy, BodySHA256: adrBodyDigest(a.doc)}
	}
	if len(recs) == 0 {
		return nil
	}
	return p.saveDecisionRecords(recs)
}

// numberADRs gives each record its new number (D36, D205 Q119): the
// apex-v0 governance standard — the first record isV0Standard names,
// stdAt, -1 when there is none — becomes ADR-0001, APEX's standard,
// which is written instead of converted; every other record numbers on
// from 0002 in its order, a second record of an id included.
func (lf *lifter) numberADRs(all []v0ADR, firstOf map[int]int) (stdAt int, newNum []int) {
	stdAt = -1
	for i := range all {
		if _, dup := firstOf[i]; !dup && isV0Standard(&all[i]) {
			stdAt = i
			break
		}
	}
	newNum = make([]int, len(all))
	next := 2
	for i := range all {
		if i == stdAt {
			newNum[i] = 1
			continue
		}
		newNum[i] = next
		next++
	}
	return stdAt, newNum
}

// planStandardADR plans the standard ADR-0001 the lift writes, in place
// of all[stdAt] when there is one: its text stays at the tag.
func (lf *lifter) planStandardADR(all []v0ADR, stdAt int) {
	lf.std = standardADR(lf.root)
	rel := lf.devRel + "/decisions/" + StandardADRFile()
	res := &StandardADRResult{Action: StandardCreated, Path: rel, Source: lf.std.source, FrameworkVersion: lf.std.version}
	if stdAt >= 0 {
		a := &all[stdAt]
		res.Action = StandardReplaced
		res.Replaces = "ADR-" + fmtNum4(a.num) + ", " + a.rel
		if a.canon {
			res.Replaces += " (canon)"
		}
	}
	lf.writes[rel] = lf.std.doc
	lf.res.StandardADR = res
}
