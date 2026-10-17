package apexv1

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ChangeInfo is one change folder under changes/.
type ChangeInfo struct {
	ID    string
	State *State
	Doc   *Change
	// Err is a state.json that would not load (ErrNoState included).
	Err error
	// Branch is the change branch State and Doc were read from, when
	// HEAD is not on it (D38); "" when they were read from the working
	// tree.
	Branch string
}

// Changes lists the changes, sorted by id: the folders under changes/,
// and every change/<id> branch's change not archived here. A change with
// a branch HEAD is not on is read from that branch (D38), so its status
// is the one its own commits recorded. An old changes/archive/ (an older
// ape's archive, reported by `project check`) is not a change.
func (p *Project) Changes() []ChangeInfo { return p.changes(context.Background()) }

// changes is Changes under a caller's context.
func (p *Project) changes(ctx context.Context) []ChangeInfo {
	ids := map[string]bool{}
	if entries, err := os.ReadDir(p.ChangesDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != "archive" && !strings.HasPrefix(e.Name(), ".") {
				ids[e.Name()] = true
			}
		}
	}
	var branches map[string]bool
	head := ""
	if p.isWorkTree(ctx) {
		branches = p.changeBranches(ctx)
		head = p.HeadChange(ctx)
	}
	archived := p.ArchivedIDs()
	out := make([]ChangeInfo, 0, len(ids)+len(branches))
	for id := range branches {
		if !ids[id] && archived[id] == "" {
			if ci, ok := p.loadChangeAt(ctx, id, ChangeBranch(id)); ok {
				out = append(out, ci)
			}
		}
	}
	for id := range ids {
		out = append(out, p.readChange(ctx, id, head, branches))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (p *Project) loadChange(id string) ChangeInfo {
	ci := ChangeInfo{ID: id}
	ci.State, ci.Err = LoadState(p.StateFile(id))
	if doc, err := readFileString(p.ChangeMD(id)); err == nil {
		ci.Doc = ParseChange(p.Rel(p.ChangeMD(id)), doc)
	}
	return ci
}

// ArchivedIDs maps every archived change id to its archive folder name.
func (p *Project) ArchivedIDs() map[string]string {
	return archivedIDsIn(p.ArchiveDir())
}

// archivedIDsIn maps the change ids archived in dir to their folder names
// (<YYYY-MM-DD>-<id>).
func archivedIDsIn(dir string) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || len(name) < 12 || name[10] != '-' {
			continue
		}
		out[name[11:]] = name
	}
	return out
}

// ActiveChanges are the changes in flight (in-progress, verified,
// reviewed); blocked ones are not.
func (p *Project) ActiveChanges(ctx context.Context) []ChangeInfo {
	var out []ChangeInfo
	for _, c := range p.changes(ctx) {
		if c.State != nil && c.State.Active() {
			out = append(out, c)
		}
	}
	return out
}

// CheckChange validates change.md against its shape, its caps, the
// roadmap and the current specs (`ape change check`). One flat list of
// rules, each a few lines: splitting it would scatter the contract.
//
//nolint:gocyclo // a checklist, not branching logic
func (p *Project) CheckChange(id string) []Finding {
	file := p.Rel(p.ChangeMD(id))
	doc, err := readFileString(p.ChangeMD(id))
	if err != nil {
		return []Finding{{File: file, Message: "missing — create the change with `ape change new " + id + "`"}}
	}
	var out []Finding
	add := func(line int, msg string) { out = append(out, Finding{File: file, Line: line, Message: msg}) }

	if len(doc) > ChangeMaxBytes {
		add(0, "change.md is "+sizeKB(len(doc))+", over the 8 KB cap — split this change")
	}
	if d, err := os.Stat(p.DesignMD(id)); err == nil && d.Size() > DesignMaxBytes {
		out = append(out, Finding{
			File:    p.Rel(p.DesignMD(id)),
			Message: "design.md is " + sizeKB(int(d.Size())) + ", over the 8 KB cap — split this change, or move bulk to assets/",
		})
	}
	if _, err := LoadState(p.StateFile(id)); err != nil {
		if errors.Is(err, ErrNoState) {
			add(0, "no state.json beside it — changes are created with `ape change new`")
		} else {
			add(0, err.Error())
		}
	}

	c := ParseChange(file, doc)
	if c.MetaErr != nil {
		add(1, c.MetaErr.Error())
	}
	for _, k := range c.UnknownKeys {
		add(1, "frontmatter key \""+k+"\" is not one of id, milestone, touches, paths, decisions, retire, type — fix the name, or prefix your own keys with x-")
	}
	out = append(out, typeFindings(file, c, p.commitConfig().SubjectTemplate())...)
	if c.Meta.ID != id {
		add(1, fmt.Sprintf("frontmatter id %q does not match the folder name %q", c.Meta.ID, id))
	}
	if !ChangeIDRe.MatchString(id) {
		add(1, "change id must match ^[a-z][a-z0-9-]{2,39}$")
	}
	rm := p.roadmap()
	switch {
	case c.Meta.Milestone == "":
		add(1, "frontmatter has no milestone")
	case c.Meta.Milestone == MilestoneNone:
		// A fix (D40): approved on its own, in no milestone.
	case rm == nil || rm.Find(c.Meta.Milestone) == nil:
		add(1, "milestone "+c.Meta.Milestone+" is not in roadmap.md")
	}
	if len(c.Meta.Touches) == 0 {
		add(1, "frontmatter touches: is empty — list the capabilities this change adds to or modifies")
	}
	touches := map[string]bool{}
	for _, t := range c.Meta.Touches {
		touches[t] = true
		if !slugRe.MatchString(t) {
			add(1, "touches entry \""+t+"\" is not a kebab-case capability slug")
		}
	}
	for _, d := range c.Meta.Decisions {
		if !adrIDRe.MatchString(d) {
			add(1, "decisions entry \""+d+"\" is not ADR-NNNN")
		} else if !p.decisionExists(d) {
			add(1, d+" has no file in decisions/ — `ape decision new <slug>`")
		}
	}
	for _, g := range c.Meta.Paths {
		if _, err := path.Match(g, ""); err != nil {
			add(1, "paths entry \""+g+"\" is not a valid glob")
		}
	}

	if c.Title == "" {
		add(0, "missing `# <title>`")
	}
	for _, want := range []string{"Why", "Out of scope", "Delta", "Tasks"} {
		s := c.section(want)
		switch {
		case s == nil:
			add(0, "missing `## "+want+"`")
		case want != "Delta" && want != "Tasks" && !hasProse(s.Body):
			add(s.Line, "`## "+want+"` is empty")
		}
	}
	out = append(out, c.parse...)

	if s := c.section("Delta"); s != nil && len(c.Deltas) == 0 && c.DeltaNone == "" {
		add(s.Line, "## Delta is empty — add ADDED/MODIFIED/REMOVED blocks, or the single line `none — <reason>`")
	}
	out = append(out, p.checkDeltas(file, c, touches)...)

	if s := c.section("Tasks"); s != nil && len(c.Tasks) == 0 {
		add(s.Line, "## Tasks has no tasks — `- T1 <what> — check: <command> | human-review`")
	}
	ids := map[string]bool{}
	for _, t := range c.Tasks {
		if ids[t.ID] {
			add(t.Line, "duplicate task "+t.ID)
		}
		ids[t.ID] = true
		if t.Check == "" {
			add(t.Line, "task "+t.ID+" has no `check: <command> | human-review`")
		}
	}
	return out
}

// checkDeltas checks each delta block against the change's touches and
// the current specs.
func (p *Project) checkDeltas(file string, c *Change, touches map[string]bool) []Finding {
	var out []Finding
	add := func(line int, msg string) { out = append(out, Finding{File: file, Line: line, Message: msg}) }
	seen := map[reqKey]int{}
	for i := range c.Deltas {
		d := &c.Deltas[i]
		if d.Capability == "" || !slugRe.MatchString(d.Capability) {
			add(d.Line, "delta capability ["+d.Capability+"] is not a kebab-case slug")
			continue
		}
		if !touches[d.Capability] {
			add(d.Line, "delta capability ["+d.Capability+"] is not in frontmatter touches:")
		}
		if d.Name == "" {
			add(d.Line, "delta has no requirement name")
			continue
		}
		if prev, dup := seen[d.key()]; dup {
			add(d.Line, fmt.Sprintf("requirement [%s] %q appears twice in the delta (first at line %d)", d.Capability, d.Name, prev))
		}
		seen[d.key()] = d.Line
		spec, _, specExists := p.LoadSpec(d.Capability)
		var existing *Requirement
		if specExists {
			existing = spec.Find(d.Name)
		}
		switch d.Op {
		case OpAdded:
			switch {
			case d.PurposeLine > 0 && specExists:
				add(d.PurposeLine, "`Purpose:` in an ADDED block names a new capability's purpose, and ["+d.Capability+"] already has one — drop it, or change the spec's Purpose through its own change")
			case d.PurposeLine > 0 && d.Purpose == "":
				add(d.PurposeLine, "`Purpose:` is empty — one or more sentences on what the capability is for")
			}
			if existing != nil {
				add(d.Line, fmt.Sprintf("ADDED requirement %q already exists in specs/%s — use MODIFIED", d.Name, d.Capability))
			} else if near := nearMiss(spec, d.Name); specExists && near != "" {
				add(d.Line, fmt.Sprintf("ADDED requirement %q nearly matches the existing %q in specs/%s — MODIFIED %q if it is the same requirement, or pick a clearly different name",
					d.Name, near, d.Capability, near))
			}
			out = append(out, validateRequirementBody(file, d.Req)...)
		case OpModified:
			if existing == nil {
				add(d.Line, fmt.Sprintf("MODIFIED names %q, which is not a requirement heading in specs/%s/spec.md — %s", d.Name, d.Capability, nearMissFix(spec, d.Name)))
			} else {
				for _, loss := range scenarioLoss(existing, &d.Req) {
					add(d.Line, fmt.Sprintf("MODIFIED [%s] %q %s — a MODIFIED block keeps every scenario and its check; changed behaviour is REMOVED + ADDED under a new name",
						d.Capability, d.Name, loss))
				}
			}
			out = append(out, validateRequirementBody(file, d.Req)...)
		case OpRemoved:
			if existing == nil {
				add(d.Line, fmt.Sprintf("REMOVED names %q, which is not a requirement heading in specs/%s/spec.md — %s", d.Name, d.Capability, nearMissFix(spec, d.Name)))
			}
			if d.Reason == "" {
				add(d.Line, "REMOVED needs a `Reason:` line")
			}
			if d.Extra > 0 {
				add(d.Extra, "a REMOVED block carries only `Reason:` — archive would drop this text; move it to ## Why or ## Notes")
			}
		}
	}
	out = append(out, p.checkRetire(file, c)...)
	return out
}

// checkRetire enforces item 8's retirement rule: a change that REMOVES
// every requirement of a capability (adding none back) must list it under
// retire:, and retire: lists only capabilities the change empties.
func (p *Project) checkRetire(file string, c *Change) []Finding {
	var out []Finding
	retire := map[string]bool{}
	for _, r := range c.Meta.Retire {
		retire[r] = true
	}
	emptied := p.emptiedCapabilities(c)
	for _, capName := range sortedKeys(emptied) {
		if !retire[capName] {
			out = append(out, Finding{File: file, Line: 1, Message: "the delta removes every requirement of [" + capName +
				"] — add `retire: [" + capName + "]` to the frontmatter to delete the capability, or keep a requirement"})
		}
	}
	for _, capName := range c.Meta.Retire {
		if !emptied[capName] {
			out = append(out, Finding{File: file, Line: 1, Message: "retire: lists [" + capName +
				"], but the delta does not remove every one of its requirements — REMOVE them all, or drop it from retire:"})
		}
	}
	return out
}

// emptiedCapabilities are the existing capabilities whose every
// requirement c REMOVES, with nothing ADDED or MODIFIED in them.
func (p *Project) emptiedCapabilities(c *Change) map[string]bool {
	removed := map[string]map[string]bool{}
	kept := map[string]bool{}
	for i := range c.Deltas {
		d := &c.Deltas[i]
		if d.Op == OpRemoved {
			if removed[d.Capability] == nil {
				removed[d.Capability] = map[string]bool{}
			}
			removed[d.Capability][d.Name] = true
		} else {
			kept[d.Capability] = true
		}
	}
	out := map[string]bool{}
	for capName, names := range removed {
		if kept[capName] {
			continue
		}
		spec, _, ok := p.LoadSpec(capName)
		if !ok || len(spec.Requirements) == 0 {
			continue
		}
		all := true
		for i := range spec.Requirements {
			if !names[spec.Requirements[i].Name] {
				all = false
				break
			}
		}
		if all {
			out[capName] = true
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// roadmap parses roadmap.md, nil when absent.
func (p *Project) roadmap() *Roadmap {
	doc, err := readFileString(p.RoadmapMD())
	if err != nil {
		return nil
	}
	return ParseRoadmap(p.Rel(p.RoadmapMD()), doc)
}

// Overlap describes an active change sharing ground with another.
type Overlap struct {
	With    string   `json:"with"`
	Touches []string `json:"touches,omitempty"`
	Paths   []string `json:"paths,omitempty"`
}

// overlaps finds active or draft changes (other than id) whose touches or
// paths intersect the given ones.
func (p *Project) overlaps(ctx context.Context, id string, touches, paths []string) []Overlap {
	var out []Overlap
	for _, c := range p.changes(ctx) {
		if c.ID == id || c.Doc == nil || c.State == nil || c.State.Status == StatusArchived {
			continue
		}
		var o Overlap
		for _, t := range touches {
			for _, u := range c.Doc.Meta.Touches {
				if t == u {
					o.Touches = append(o.Touches, t)
				}
			}
		}
		for _, g := range paths {
			for _, h := range c.Doc.Meta.Paths {
				if globsOverlap(g, h) {
					o.Paths = append(o.Paths, g+" ~ "+h)
				}
			}
		}
		if len(o.Touches) > 0 || len(o.Paths) > 0 {
			o.With = c.ID
			out = append(out, o)
		}
	}
	return out
}

// globsOverlap is a conservative test: two globs overlap when their
// literal prefixes (up to the first wildcard) are prefix-related.
func globsOverlap(a, b string) bool {
	lit := func(g string) string {
		if i := strings.IndexAny(g, "*?["); i >= 0 {
			return g[:i]
		}
		return g
	}
	la, lb := lit(a), lit(b)
	return strings.HasPrefix(la, lb) || strings.HasPrefix(lb, la)
}

// changeSkeleton renders a fresh change.md.
func changeSkeleton(id, milestone string, touches, paths, outOfScope []string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("id: " + id + "\n")
	b.WriteString("milestone: " + milestone + "\n")
	b.WriteString("touches: [" + strings.Join(touches, ", ") + "]\n")
	if len(paths) > 0 {
		quoted := make([]string, len(paths))
		for i, g := range paths {
			quoted[i] = `"` + g + `"`
		}
		b.WriteString("paths: [" + strings.Join(quoted, ", ") + "]\n")
	}
	b.WriteString("decisions: []\n")
	b.WriteString("---\n")
	b.WriteString("# " + titleFromSlug(id) + "\n\n")
	if milestone == MilestoneNone {
		b.WriteString("## Why\n<!-- the request this fix answers, and why it cannot wait for a milestone -->\n\n")
	} else {
		b.WriteString("## Why\n<!-- why this change, now, toward " + milestone + " -->\n\n")
	}
	b.WriteString("## Out of scope\n")
	if len(outOfScope) == 0 {
		b.WriteString("<!-- what this change will not do -->\n")
	}
	for _, o := range outOfScope {
		b.WriteString("- " + o + "\n")
	}
	b.WriteString("\n## Delta\n<!-- ### ADDED|MODIFIED|REMOVED [<capability>] Requirement: <name>, with scenarios\n")
	b.WriteString("     and one `- check: <command> | human-review` per scenario; or the single line `none — <reason>` -->\n\n")
	b.WriteString("## Tasks\n<!-- - T1 <what> — check: <command> | human-review   (no checkboxes) -->\n\n")
	b.WriteString("## Notes\n")
	return b.String()
}

// NewChangeResult is what `ape change new` reports.
type NewChangeResult struct {
	ID       string    `json:"id"`
	Path     string    `json:"path"`
	State    string    `json:"state"`
	Commit   string    `json:"commit,omitempty"`
	Overlaps []Overlap `json:"overlaps,omitempty"`
}

// NewChange validates id and writes the change skeleton and a draft state.
func (p *Project) NewChange(ctx context.Context, id, milestone string, touches, paths []string) (*NewChangeResult, error) {
	if !ChangeIDRe.MatchString(id) {
		return nil, &UsageError{Code: "usage.change-id", Msg: fmt.Sprintf("change id %q must match ^[a-z][a-z0-9-]{2,39}$ (verb-led kebab slug, e.g. add-tenant-delete)", id)}
	}
	// A new change is planned on the base branch, not inside another
	// change's branch (D38).
	if err := p.refuseOnChangeBranch(ctx, "a new change"); err != nil {
		return nil, err
	}
	if dirExists(p.ChangeDir(id)) {
		return nil, &FailError{Code: "change.exists", Msg: "change " + id + " already exists — pick another id"}
	}
	// The old archive (changes/archive/, an older ape's) is scanned too:
	// an id is never reused, wherever it was archived.
	for _, dir := range []string{p.ArchiveDir(), p.OldArchiveDir()} {
		if folder, ok := archivedIDsIn(dir)[id]; ok {
			return nil, &FailError{Code: "change.id-archived", Msg: "change " + id + " was already archived as " + p.Rel(filepath.Join(dir, folder)) + " — ids are unique across the archive; pick another"}
		}
	}
	// A fix (D40) belongs to no milestone: nothing to look up, no Out of
	// scope to inherit.
	var outOfScope []string
	if milestone != MilestoneNone {
		rm := p.roadmap()
		if rm == nil {
			return nil, &FailError{Code: "roadmap.missing", Msg: "no roadmap.md — run `ape project init` and `ape milestone new` first"}
		}
		ms := rm.Find(milestone)
		if ms == nil {
			return nil, &FailError{Code: CodeMilestoneNotFound, Msg: "milestone " + milestone + " is not in roadmap.md — `ape milestone new` adds one, or `--milestone none` makes the change a fix"}
		}
		outOfScope = ms.OutOfScope
	}
	if len(touches) == 0 {
		return nil, &UsageError{Code: "usage.touches", Msg: "--touches needs at least one capability slug"}
	}
	for _, t := range touches {
		if !slugRe.MatchString(t) {
			return nil, &UsageError{Code: "usage.touches", Msg: "--touches entry \"" + t + "\" is not a kebab-case capability slug"}
		}
	}
	for _, g := range paths {
		if _, err := path.Match(g, ""); err != nil {
			return nil, &UsageError{Code: "usage.paths", Msg: "--paths entry \"" + g + "\" is not a valid glob"}
		}
	}
	if err := os.MkdirAll(p.ChangeDir(id), 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(p.ChangeMD(id), changeSkeleton(id, milestone, touches, paths, outOfScope)); err != nil {
		return nil, err
	}
	st := &State{ID: id, Status: StatusDraft, CreatedAt: Stamp()}
	if err := SaveState(p.StateFile(id), st); err != nil {
		return nil, err
	}
	// The skeleton and the draft state are committed together, so no v1
	// command leaves the tree dirty (D11); the author's edits to change.md
	// are the skill's own commit.
	commit := ""
	if p.isWorkTree(ctx) {
		sha, err := p.commitPaths(ctx, "apex: "+id+" "+StatusDraft, id, p.Rel(p.ChangeDir(id)))
		if err != nil {
			return nil, &FailError{Code: CodeCommitFailed, Msg: "change written but its commit failed: " + err.Error()}
		}
		commit = sha
	}
	return &NewChangeResult{
		ID: id, Path: p.Rel(p.ChangeMD(id)), State: StatusDraft, Commit: commit,
		Overlaps: p.overlaps(ctx, id, touches, paths),
	}, nil
}

// requireChange loads a change that must exist — from its branch when
// HEAD is not on it (D38): the commands that record its state check the
// branch themselves (requireOnBranch).
func (p *Project) requireChange(ctx context.Context, id string) (ChangeInfo, error) {
	if p.isWorkTree(ctx) && p.HeadChange(ctx) != id && p.branchExists(ctx, ChangeBranch(id)) {
		if ci, ok := p.loadChangeAt(ctx, id, ChangeBranch(id)); ok {
			return checkLoaded(ci)
		}
	}
	if !dirExists(p.ChangeDir(id)) {
		if folder, ok := p.ArchivedIDs()[id]; ok {
			return ChangeInfo{}, &FailError{Code: "change.archived", Msg: "change " + id + " is archived (" + p.Rel(filepath.Join(p.ArchiveDir(), folder)) + ")"}
		}
		return ChangeInfo{}, &FailError{Code: "change.not-found", Msg: "no change " + id + " — `ape status` lists the active ones"}
	}
	return checkLoaded(p.loadChange(id))
}

// checkLoaded refuses a change whose state or change.md did not load.
func checkLoaded(ci ChangeInfo) (ChangeInfo, error) {
	id := ci.ID
	if ci.Err != nil {
		if errors.Is(ci.Err, ErrNoState) {
			return ci, &FailError{Code: "change.no-state", Msg: "change " + id + " has no state.json — it was not created with `ape change new`"}
		}
		return ci, &FailError{Code: "change.state-unreadable", Msg: ci.Err.Error()}
	}
	if ci.Doc == nil {
		return ci, &FailError{Code: "change.no-change-md", Msg: "change " + id + " has no change.md"}
	}
	return ci, nil
}
