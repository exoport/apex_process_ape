package apexv1

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Required and optional project.md sections (brief 4.1).
var (
	projectRequired = []string{"Vision", "Users", "Scope", "Out of scope", "Constraints"}
)

// ValidateProjectMD checks project.md's shape and caps.
func ValidateProjectMD(file, doc string) []Finding {
	var out []Finding
	const remedy = " — move detail to specs/, decisions/ or docs/explanation/"
	if n := strings.Count(strings.TrimRight(doc, "\n"), "\n") + 1; n > ProjectMaxLines {
		out = append(out, Finding{File: file, Message: "project.md is " + strconv.Itoa(n) + " lines, over the 150-line cap" + remedy})
	}
	if len(doc) > ProjectMaxBytes {
		out = append(out, Finding{File: file, Message: "project.md is " + sizeKB(len(doc)) + ", over the 8 KB cap" + remedy})
	}
	lines := splitLines(doc)
	title := ""
	for _, l := range lines {
		if lv, t := headingLevel(l); lv == 1 {
			title = t
			break
		}
	}
	if title == "" {
		out = append(out, Finding{File: file, Line: 1, Message: "missing `# <Project name>`"})
	}
	h2 := h2Sections(lines)
	byTitle := map[string]section{}
	for _, s := range h2 {
		byTitle[strings.ToLower(s.Title)] = s
	}
	for _, want := range projectRequired {
		s, ok := byTitle[strings.ToLower(want)]
		switch {
		case !ok:
			out = append(out, Finding{File: file, Message: "missing `## " + want + "`"})
		case !hasProse(s.Body):
			out = append(out, Finding{File: file, Line: s.Line, Message: "`## " + want + "` is empty"})
		}
	}
	return out
}

// ValidateAgentsMD checks AGENTS.md exists and stays a map.
func ValidateAgentsMD(file, doc string) []Finding {
	if n := strings.Count(strings.TrimRight(doc, "\n"), "\n") + 1; n > AgentsMaxLines {
		return []Finding{{File: file, Message: "AGENTS.md is " + strconv.Itoa(n) +
			" lines, over the 120-line cap — it is a map: move detail to docs/ or development/"}}
	}
	return nil
}

var (
	adrFileRe    = regexp.MustCompile(`^(\d{4})-([a-z0-9][a-z0-9-]*)\.md$`)
	adrTitleRe   = regexp.MustCompile(`^ADR-(\d{4}):\s*\S`)
	adrStatusRe  = regexp.MustCompile(`^Status:\s*(proposed|accepted|superseded by ADR-\d{4})\s*$`)
	adrDateRe    = regexp.MustCompile(`^Date:\s*\d{4}-\d{2}-\d{2}\s*$`)
	adrEnforceRe = regexp.MustCompile(`^Enforced by:\s*\S`)
	adrIDRe      = regexp.MustCompile(`^ADR-\d{4}$`)
)

// ValidateDecision checks one ADR (brief 4.5).
func ValidateDecision(file, name, doc string) []Finding {
	var out []Finding
	m := adrFileRe.FindStringSubmatch(name)
	if m == nil {
		return []Finding{{File: file, Message: "decision files are named NNNN-<slug>.md — use `ape decision new <slug>`"}}
	}
	lines := splitLines(doc)
	var title string
	var status, date, enforced bool
	for _, l := range lines {
		if l.InCode {
			continue
		}
		if lv, t := headingLevel(l); lv == 1 && title == "" {
			title = t
		}
		t := strings.TrimSpace(l.Text)
		status = status || adrStatusRe.MatchString(t)
		date = date || adrDateRe.MatchString(t)
		enforced = enforced || adrEnforceRe.MatchString(t)
	}
	if tm := adrTitleRe.FindStringSubmatch(title); len(tm) < 2 || tm[1] != m[1] {
		out = append(out, Finding{File: file, Line: 1, Message: "first heading must be `# ADR-" + m[1] + ": <title>`"})
	}
	if !status {
		out = append(out, Finding{File: file, Message: "missing `Status: proposed | accepted | superseded by ADR-NNNN`"})
	}
	if !date {
		out = append(out, Finding{File: file, Message: "missing `Date: YYYY-MM-DD`"})
	}
	if !enforced {
		out = append(out, Finding{File: file, Message: "missing `Enforced by: <command> | hook | review`"})
	}
	have := map[string]bool{}
	for _, s := range h2Sections(lines) {
		have[strings.ToLower(s.Title)] = true
	}
	for _, want := range []string{"Context", "Decision", "Consequences"} {
		if !have[strings.ToLower(want)] {
			out = append(out, Finding{File: file, Message: "missing `## " + want + "`"})
		}
	}
	return out
}

// decisionExists reports whether ADR-NNNN has a file in decisions/.
func (p *Project) decisionExists(id string) bool {
	num := strings.TrimPrefix(id, "ADR-")
	matches, _ := filepath.Glob(filepath.Join(p.DecisionsDir(), num+"-*.md"))
	return len(matches) > 0
}

// Capabilities lists the capability folders under specs/.
func (p *Project) Capabilities() []string {
	entries, err := os.ReadDir(p.SpecsDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// LoadSpec reads and parses a capability's spec; ok is false when absent.
func (p *Project) LoadSpec(capability string) (Spec, string, bool) {
	doc, err := readFileString(p.SpecFile(capability))
	if err != nil {
		return Spec{}, "", false
	}
	return ParseSpec(doc), doc, true
}

// Check validates the whole project (`ape project check`).
func (p *Project) Check() []Finding {
	var out []Finding
	add := func(fs ...Finding) { out = append(out, fs...) }
	missing := func(path, remedy string) {
		add(Finding{File: p.Rel(path), Message: "missing — " + remedy})
	}
	add(p.layoutFindings()...)
	add(p.liftDispositionFindings(context.Background())...)
	add(p.docsFindings()...)
	add(p.settingsIgnoredFindings(context.Background())...)
	add(p.provenanceFindings(context.Background())...)

	if doc, err := readFileString(p.ProjectMD()); err != nil {
		missing(p.ProjectMD(), "run `ape project init`, then write it (apex-plan)")
	} else {
		add(ValidateProjectMD(p.Rel(p.ProjectMD()), doc)...)
	}
	if doc, err := readFileString(p.AgentsMD()); err != nil {
		missing(p.AgentsMD(), "write the project map: commands, NEVER rules, pointers")
	} else {
		add(ValidateAgentsMD("AGENTS.md", doc)...)
	}
	if doc, err := readFileString(p.ClaudeMD()); err == nil {
		if !hasAgentsImport(doc) {
			add(Finding{File: "CLAUDE.md", Message: "must import the map: a line `@AGENTS.md` (move its content into AGENTS.md)"})
		}
	}
	if doc, err := readFileString(p.RoadmapMD()); err != nil {
		missing(p.RoadmapMD(), "run `ape project init`, then `ape milestone new`")
	} else {
		add(ValidateRoadmap(p.Rel(p.RoadmapMD()), doc)...)
		add(p.milestoneRecordFindings()...)
	}
	add(p.briefFindings(context.Background())...)

	for _, capName := range p.Capabilities() {
		file := p.Rel(p.SpecFile(capName))
		if !slugRe.MatchString(capName) {
			add(Finding{File: p.Rel(filepath.Join(p.SpecsDir(), capName)), Message: "capability folder must be a kebab-case slug"})
		}
		doc, err := readFileString(p.SpecFile(capName))
		if err != nil {
			add(Finding{File: file, Message: "capability folder without spec.md"})
			continue
		}
		add(ValidateSpec(file, doc)...)
	}

	if entries, err := os.ReadDir(p.DecisionsDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			path := filepath.Join(p.DecisionsDir(), e.Name())
			doc, err := readFileString(path)
			if err != nil {
				continue
			}
			add(ValidateDecision(p.Rel(path), e.Name(), doc)...)
			add(p.checkDecisionLife(path, e.Name(), doc)...)
		}
	}

	for _, c := range p.Changes() {
		// The tree checked out is what is checked: a change held only
		// by its branch (D38) is checked there.
		if dirExists(p.ChangeDir(c.ID)) {
			add(p.CheckChange(c.ID)...)
		}
	}
	return out
}

// docsFindings reports a docs: path in _apex/config.yaml that does not
// exist (H21): a skill told to update it would create it, or fail.
func (p *Project) docsFindings() []Finding {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return nil
	}
	var out []Finding
	for _, d := range []struct{ key, path string }{{"changelog", cfg.Docs.Changelog}, {"user_docs", cfg.Docs.UserDocs}} {
		if d.path != "" && !fileOrDirExists(filepath.Join(p.Root, filepath.FromSlash(d.path))) {
			out = append(out, Finding{File: configYAML, Message: "docs." + d.key + ": " + d.path +
				" does not exist — create it, or fix (or drop) docs." + d.key + " in _apex/config.yaml"})
		}
	}
	return out
}

// layoutFindings reports the places an older ape kept things, which this
// one no longer reads: the move is the project's to make, once.
func (p *Project) layoutFindings() []Finding {
	var out []Finding
	if dirExists(p.OldArchiveDir()) {
		old, dev := p.Rel(p.OldArchiveDir()), p.DevRel
		out = append(out, Finding{File: old, Message: old + "/ is the old archive location (archived changes now live in " + dev +
			"/archive/) — move " + old + "/* to " + dev + "/archive/"})
	}
	if dirExists(p.OldStateDir()) {
		old, state := p.Rel(p.OldStateDir()), p.StateRel()
		out = append(out, Finding{File: old, Message: old + "/ is the old state folder (ape's records now live in " + state +
			"/) — move " + old + "/{milestones,decisions}.json to " + state + "/ and delete " + old + "/"})
	}
	return out
}

// liftDispositionFindings reports the lift's disposition list while it
// awaits review (D49 Q14): the file exists, and the project's v1 epoch
// is a lift commit. The person deletes it after the review; until then
// the project is not done lifting.
func (p *Project) liftDispositionFindings(ctx context.Context) []Finding {
	path := filepath.Join(p.Dev(), LiftDisposition)
	if !fileExists(path) {
		return nil
	}
	if out, err := git(ctx, p.Root, "log", "-1", "--format=%h", "--grep=^"+LiftSubjectPrefix); err != nil || out == "" {
		return nil
	}
	return []Finding{{File: p.Rel(path), Message: "the lift's disposition list awaits review: apex-plan fills it, a fresh read-only session reviews it, then the operator — delete the file when done"}}
}

var adrSupersedesRe = regexp.MustCompile(`(?m)^Supersedes:[ \t]*(ADR-\d{4})[ \t]*$`)

// checkDecisionLife checks what an ADR's history allows (backlog item 7):
// an accepted ADR's body is fixed — a different decision is a new ADR
// that supersedes it — and a Supersedes: line names an ADR that exists.
func (p *Project) checkDecisionLife(path, name, doc string) []Finding {
	m := adrFileRe.FindStringSubmatch(name)
	if m == nil {
		return nil
	}
	id := "ADR-" + m[1]
	file := p.Rel(path)
	var out []Finding
	out = append(out, p.decisionRecordFinding(id, file, doc)...)
	if p.acceptedBodyChanged(context.Background(), id, path, doc) {
		out = append(out, Finding{File: file, Message: id + " was accepted and its text has changed since — restore it; " +
			"a different decision is a new ADR: `ape decision new <slug> --supersedes " + id + "`"})
	}
	if sm := adrSupersedesRe.FindStringSubmatch(doc); len(sm) == 2 && !p.decisionExists(sm[1]) {
		out = append(out, Finding{File: file, Message: "Supersedes: " + sm[1] + " names no decision in decisions/"})
	}
	return out
}
