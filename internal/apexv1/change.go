package apexv1

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"gopkg.in/yaml.v3"
)

// Delta operations.
const (
	OpAdded    = "ADDED"
	OpModified = "MODIFIED"
	OpRemoved  = "REMOVED"
)

// ChangeMeta is change.md's frontmatter.
type ChangeMeta struct {
	ID        string   `yaml:"id"`
	Milestone string   `yaml:"milestone"`
	Touches   []string `yaml:"touches"`
	Paths     []string `yaml:"paths"`
	Decisions []string `yaml:"decisions"`
	// Retire lists capabilities this change removes entirely: the only way
	// a change may REMOVE a capability's last requirement (item 8, G7).
	Retire []string `yaml:"retire"`
	// Type is the change's commit type (D47), one of CommitTypes: the
	// archive squash's {type}. Required only when commit.subject uses it.
	Type string `yaml:"type"`
	// ReviewTools names the tools.review entries whose when: this change
	// matches (D59 Q4, D74): the orchestrator runs each before the review.
	ReviewTools []string `yaml:"review_tools"`
	// Advise names the tools.advise entries whose when: this change
	// matches (W6, Q-E1): the change, apply and review sessions read their
	// files (ape status --json, changes[].advise).
	Advise []string `yaml:"advise"`
	// Docs marks the milestone's docs change (D130), which apex-change
	// writes as `docs: true` (D152 Q69): its human-review tasks are the
	// docs-accuracy check, a delegate's by default (PrepareDelegate). Read
	// by docsFlag, which takes only a YAML boolean.
	Docs bool `yaml:"-"`
}

// Delta is one `### ADDED|MODIFIED|REMOVED [cap] Requirement: <name>`.
type Delta struct {
	Op         string `json:"op"`
	Capability string `json:"capability"`
	Name       string `json:"requirement"`
	Line       int    `json:"line"`
	// Req is the requirement as it will read in the spec (ADDED, MODIFIED):
	// heading rewritten to `### Requirement: <name>`, body verbatim.
	Req    Requirement `json:"-"`
	Reason string      `json:"reason,omitempty"`
	// Extra is a REMOVED block's first line of prose other than Reason:
	// — text archive would drop.
	Extra int `json:"-"`
	// Purpose is an ADDED block's Purpose block (purposeBlock): the new
	// capability's Purpose (item 11), kept out of the requirement text,
	// its lines joined with "\n". PurposeLine is where it starts.
	Purpose     string `json:"purpose,omitempty"`
	PurposeLine int    `json:"-"`
}

// Task is one `- T<n> <what> — check: <command|human-review>` line, or
// `- T<n> <what> — regression: <command>` (backlog 43, D47): a regression
// task runs exactly like a check task everywhere — Check holds its
// command — and is only allowed to be green at baseline, where start
// refuses any other task check of a change that adds or modifies
// behaviour (change.vacuous-check).
//
// A design-tool task (D59 Q4, D74) names its tool between <what> and the
// check: `- T<n> <what> — tool: <name> — check: <command|human-review>`;
// <name> is a tools.design entry, which the orchestrator runs before
// apex-apply.
type Task struct {
	ID         string `json:"id"`
	What       string `json:"what"`
	Check      string `json:"check"`
	Regression bool   `json:"regression,omitempty"`
	Tool       string `json:"tool,omitempty"`
	// Full: written `— check (full): <command>` (v1.3, D508; W2 Part 2),
	// or `(live)`, its v1.2 name: a check that does not run on every
	// change. A task's is the change's own, so verify always runs it.
	Full bool `json:"full,omitempty"`
	// Live is Full under its v1.2 JSON name, always the same value
	// (D512). Deprecated: read full; live is removed no earlier than v1.5.
	Live bool `json:"live,omitempty"`
	Line int  `json:"line"`
}

// Human reports whether the task can only be proven by a person.
func (t Task) Human() bool { return t.Check == HumanReview }

// Change is a parsed change.md.
type Change struct {
	Meta      ChangeMeta
	MetaErr   error
	Title     string
	H2        []section
	DeltaNone string
	Deltas    []Delta
	Tasks     []Task
	// parse findings (bad delta headings, malformed task lines), reported
	// by ValidateChange alongside the shape rules.
	// UnknownKeys are frontmatter keys v1 does not define and that do not
	// start with "x-" (item 15, OpenSpec's rule): a typo such as
	// "milestones:" would otherwise be ignored silently.
	UnknownKeys []string
	// BadDocs is a docs: value that is not true or false, as written; the
	// change is then no docs change.
	BadDocs string
	parse   []Finding
	file    string
	raw     string
}

// unknownKeys lists the frontmatter's keys that are neither v1's nor x-*.
func unknownKeys(fm []byte) []string {
	var keys map[string]any
	if yaml.Unmarshal(fm, &keys) != nil {
		return nil
	}
	var out []string
	for k := range keys {
		if !knownChangeKeys[k] && !strings.HasPrefix(k, "x-") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// knownChangeKeys are the frontmatter keys change.md may carry; "x-*"
// keys are the team's own (a tracker id, say) and are ignored.
var knownChangeKeys = map[string]bool{
	"id": true, "milestone": true, "touches": true, "paths": true, "decisions": true, "retire": true, "type": true,
	"review_tools": true, "docs": true, "advise": true,
}

// docsFlag reads the frontmatter's docs: (D152): a YAML boolean, else bad
// holds the value as written ("" when the key is absent).
func docsFlag(fm []byte) (docs bool, bad string) {
	var m struct {
		Docs yaml.Node `yaml:"docs"`
	}
	if yaml.Unmarshal(fm, &m) != nil || m.Docs.Kind == 0 {
		return false, ""
	}
	if n := m.Docs; n.Kind == yaml.ScalarNode && n.ShortTag() == "!!bool" && n.Decode(&docs) == nil {
		return docs, ""
	}
	if m.Docs.Kind != yaml.ScalarNode {
		return false, "a list or mapping"
	}
	return false, fmt.Sprintf("%q", m.Docs.Value)
}

var (
	deltaHeadRe = regexp.MustCompile(`^(ADDED|MODIFIED|REMOVED)\s+\[([^\]]*)\]\s+Requirement:\s*(.*)$`)
	deltaNoneRe = regexp.MustCompile(`^none\s*[—–:-]\s*(\S.*)$`)
	taskItemRe  = regexp.MustCompile(`^\s{0,3}[-*+]\s+(.*)$`)
	taskIDRe    = regexp.MustCompile(`^(\[[ xX]\]\s*)?(T\d+)\b[:.)]?\s*(.*)$`)
	// taskCheckRe is a task line's separator: a dash, then check: or
	// regression:, either with a kind, `check (full):` (W2). The first one
	// ends <what>.
	taskCheckRe = regexp.MustCompile(`(?:^|\s)[—–-]+\s*(check|regression)(?:\s*\(([^)]*)\))?:`)
	// taskToolRe is the `— tool: <name>` segment that ends <what>.
	taskToolRe = regexp.MustCompile(`(?:^|\s)[—–-]+\s*tool:\s*(\S(?:.*\S)?)\s*$`)
)

// ParseChange parses change.md. file is the project-relative path used in
// findings.
func ParseChange(file, doc string) *Change {
	c := &Change{file: file, raw: doc}
	fm, body, err := frontmatter.Split([]byte(doc))
	if err != nil {
		c.MetaErr = errors.New("no YAML frontmatter (--- id/milestone/touches ---)")
		body = []byte(doc)
	} else if err := yaml.Unmarshal(fm, &c.Meta); err != nil {
		c.MetaErr = fmt.Errorf("frontmatter does not parse: %w", err)
	} else {
		c.UnknownKeys = unknownKeys(fm)
		c.Meta.Docs, c.BadDocs = docsFlag(fm)
	}
	// Line numbers in findings should match the file, so count the lines
	// the frontmatter occupied.
	offset := 0
	if len(body) < len(doc) {
		offset = strings.Count(doc[:len(doc)-len(body)], "\n")
	}
	lines := splitLines(string(body))
	for i := range lines {
		lines[i].N += offset
	}
	for _, l := range lines {
		if lv, t := headingLevel(l); lv == 1 {
			c.Title = t
			break
		}
	}
	c.H2 = h2Sections(lines)
	if d := c.section("Delta"); d != nil {
		c.parseDelta(d.Body)
	}
	if t := c.section("Tasks"); t != nil {
		c.parseTasks(t.Body)
	}
	return c
}

func (c *Change) section(title string) *section {
	for i := range c.H2 {
		if strings.EqualFold(c.H2[i].Title, title) {
			return &c.H2[i]
		}
	}
	return nil
}

func (c *Change) parseDelta(body []mdLine) {
	cur := -1
	var curLines []mdLine
	var loose []mdLine
	// skipping is true under a malformed delta heading: its body is part
	// of that one finding, not a second one.
	skipping := false
	flush := func() {
		if cur < 0 {
			return
		}
		d := &c.Deltas[cur]
		head := mdLine{N: d.Line, Text: "### " + RequirementPrefix + " " + d.Name}
		var bodyLines []mdLine
		for i := 0; i < len(curLines); i++ {
			l := curLines[i]
			if last, ok := takePurpose(d, curLines, i, len(bodyLines) == 0); ok {
				i = last
				continue
			}
			if d.Op == OpRemoved {
				if t := strings.TrimSpace(l.Text); !l.InCode && strings.HasPrefix(t, "Reason:") {
					d.Reason = strings.TrimSpace(strings.TrimPrefix(t, "Reason:"))
					continue
				}
				if d.Extra == 0 && strings.TrimSpace(l.Text) != "" && hasProse([]mdLine{l}) {
					d.Extra = l.N
				}
			}
			bodyLines = append(bodyLines, l)
		}
		d.Req = buildRequirement(Requirement{Name: d.Name, Line: d.Line}, append([]mdLine{head}, bodyLines...))
		curLines = nil
	}
	for _, l := range body {
		lv, title := headingLevel(l)
		if lv == 3 {
			flush()
			m := deltaHeadRe.FindStringSubmatch(title)
			if m == nil {
				cur = -1
				skipping = true
				c.parse = append(c.parse, Finding{
					File: c.file, Line: l.N,
					Message: "delta heading must be `### ADDED|MODIFIED|REMOVED [<capability>] Requirement: <name>`",
				})
				continue
			}
			c.Deltas = append(c.Deltas, Delta{
				Op: m[1], Capability: strings.TrimSpace(m[2]), Name: strings.TrimSpace(m[3]), Line: l.N,
			})
			cur = len(c.Deltas) - 1
			skipping = false
			continue
		}
		switch {
		case cur >= 0:
			curLines = append(curLines, l)
		case !skipping:
			loose = append(loose, l)
		}
	}
	flush()
	if len(c.Deltas) > 0 {
		// Prose before the first delta block is not carried into any
		// spec: archive would drop it silently (item 8, OpenSpec G3).
		for _, l := range loose {
			if strings.TrimSpace(l.Text) != "" && hasProse([]mdLine{l}) {
				c.parse = append(c.parse, Finding{
					File: c.file, Line: l.N,
					Message: "text before the first delta block is not carried into any spec — archive would drop it; move it to ## Why or ## Notes",
				})
				break
			}
		}
		return
	}
	if skipping {
		return
	}
	// No delta headings: the only other legal shape is one `none — reason` line.
	for _, l := range loose {
		t := strings.TrimSpace(l.Text)
		if t == "" || !hasProse([]mdLine{l}) {
			continue
		}
		if m := deltaNoneRe.FindStringSubmatch(t); m != nil && c.DeltaNone == "" {
			c.DeltaNone = strings.TrimSpace(m[1])
			continue
		}
		c.parse = append(c.parse, Finding{
			File: c.file, Line: l.N,
			Message: "## Delta holds delta blocks or the single line `none — <reason>`, nothing else",
		})
	}
}

func (c *Change) parseTasks(body []mdLine) {
	for _, l := range body {
		if l.InCode {
			continue
		}
		m := taskItemRe.FindStringSubmatch(l.Text)
		if m == nil || strings.HasPrefix(l.Text, "    ") || strings.HasPrefix(l.Text, "\t") {
			continue
		}
		item := strings.TrimSpace(m[1])
		tm := taskIDRe.FindStringSubmatch(item)
		if tm == nil {
			c.parse = append(c.parse, Finding{
				File: c.file, Line: l.N,
				Message: TaskLineGrammar,
			})
			continue
		}
		if tm[1] != "" {
			c.parse = append(c.parse, Finding{
				File: c.file, Line: l.N,
				Message: "tasks have no checkboxes — progress is state.json, written by `ape change verify`",
			})
		}
		rest := tm[3]
		t := Task{ID: tm[2], Line: l.N}
		var badKind string
		if loc := taskCheckRe.FindStringSubmatchIndex(rest); loc != nil {
			t.What = strings.TrimSpace(rest[:loc[0]])
			t.Check = strings.Trim(strings.TrimSpace(rest[loc[1]:]), "`")
			t.Regression = rest[loc[2]:loc[3]] == "regression"
			if loc[4] >= 0 {
				t.Full, badKind = taskCheckKind(&t, strings.TrimSpace(rest[loc[4]:loc[5]]))
				t.Live = t.Full
			}
		} else if what, check, ok := strings.CutLast(rest, "check:"); ok {
			t.What = strings.TrimRight(strings.TrimSpace(what), " \t—–-")
			t.Check = strings.Trim(strings.TrimSpace(check), "`")
		} else {
			t.What = strings.TrimSpace(rest)
		}
		if m := taskToolRe.FindStringSubmatchIndex(t.What); m != nil {
			t.Tool = strings.Trim(t.What[m[2]:m[3]], "`")
			t.What = strings.TrimSpace(t.What[:m[0]])
		}
		if badKind != "" {
			c.parse = append(c.parse, Finding{File: c.file, Line: l.N, Message: badKind})
		}
		if t.Regression && (t.Check == HumanReview || t.Check == "") {
			c.parse = append(c.parse, Finding{
				File: c.file, Line: l.N,
				Message: TaskLineGrammar + "; " + t.ID + "'s regression: names no command — a regression task runs a command, which may already pass at baseline",
			})
		}
		c.Tasks = append(c.Tasks, t)
	}
}

// taskCheckKind judges a task's `check (<kind>):` (W2): full, or why not.
func taskCheckKind(t *Task, kind string) (full bool, why string) {
	if t.Regression {
		// A kind goes on check: alone.
		return false, "task " + t.ID + "'s regression: kind `(" + kind + ")` is not `check (full):`, as a kind goes on check: alone" +
			" — write `— check (full): <command>`, or a plain `— regression: <command>`"
	}
	if why := badCheckKind("task "+t.ID+"'s check", kind, t.Check, true); why != "" {
		return false, why
	}
	return isFullKind(kind), ""
}

// TaskLineGrammar is the task line's shape, as findings quote it.
const TaskLineGrammar = "task line must read `- T<n> <what> — check: <command> | human-review` or `- T<n> <what> — regression: <command>`" +
	" (a design-tool task: `- T<n> <what> — tool: <name> — check: …`; a check that does not run on every change: `— check (full): <command>`)"

// Task returns the task with id, or nil.
func (c *Change) Task(id string) *Task {
	for i := range c.Tasks {
		if c.Tasks[i].ID == id {
			return &c.Tasks[i]
		}
	}
	return nil
}

// CommandTasks returns the tasks whose check is a command.
func (c *Change) CommandTasks() []Task {
	var out []Task
	for _, t := range c.Tasks {
		if t.Check != "" && !t.Human() {
			out = append(out, t)
		}
	}
	return out
}

// HumanTasks returns the ids of human-review tasks.
func (c *Change) HumanTasks() []string {
	var out []string
	for _, t := range c.Tasks {
		if t.Human() {
			out = append(out, t.ID)
		}
	}
	return out
}

// reqKey identifies a requirement across capabilities.
type reqKey struct{ Capability, Name string }

func (d Delta) key() reqKey { return reqKey{d.Capability, d.Name} }

// takePurpose reads ADDED delta d's Purpose block when lines[i] opens
// its first one: the whole block (Q44), so its second line does not land
// under the requirement heading, and — when no body line precedes it
// (bodyEmpty) — the blank lines ending it, which do not open the
// requirement. It returns the index of the last line it took, and false
// when lines[i] opens no Purpose block.
func takePurpose(d *Delta, lines []mdLine, i int, bodyEmpty bool) (last int, ok bool) {
	l := lines[i]
	if d.Op != OpAdded || l.InCode || d.PurposeLine != 0 || !strings.HasPrefix(strings.TrimSpace(l.Text), "Purpose:") {
		return i, false
	}
	var n int
	d.Purpose, n = purposeBlock(lines, i)
	d.PurposeLine = l.N
	i += n
	for bodyEmpty && i < len(lines) && strings.TrimSpace(lines[i].Text) == "" {
		i++
	}
	return i - 1, true
}

// PlanDigest is the SHA-256 of change.md minus its ## Why and ## Notes
// sections — the plan `ape change start` freezes (items 1 and 12): the
// frontmatter, the title, ## Out of scope, ## Delta, ## Tasks and any other
// section. Lines are right-trimmed and blank lines dropped, so reflowing
// whitespace is not a change. Why and Notes may change freely after start.
func (c *Change) PlanDigest() string {
	h := sha256.New()
	skip := false
	for _, l := range splitLines(c.raw) {
		if lv, title := headingLevel(l); lv > 0 && lv <= 2 {
			skip = lv == 2 && (strings.EqualFold(title, "Why") || strings.EqualFold(title, "Notes"))
			if skip {
				h.Write([]byte("## " + title + "\n"))
				continue
			}
		}
		if skip {
			continue
		}
		if t := strings.TrimRight(l.Text, " \t"); t != "" {
			h.Write([]byte(t + "\n"))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PlanBytes is what caps.change_md measures (D89): change.md's length in
// bytes, less its ## Notes section — the heading line and its body up to
// the next level-1 or level-2 heading, found as PlanDigest finds it. ## Why
// counts. Notes are working notes, bounded only by the file ceiling
// (change.over-ceiling), so a change never has to be trimmed to archive.
func (c *Change) PlanBytes() int {
	// splitLines keeps one line per "\n", so raw[i] is line i as it is on
	// disk, CRLF included.
	raw := strings.SplitAfter(c.raw, "\n")
	n := len(c.raw)
	notes := false
	for i, l := range splitLines(c.raw) {
		if lv, title := headingLevel(l); lv > 0 && lv <= 2 {
			notes = lv == 2 && strings.EqualFold(title, "Notes")
		}
		if notes {
			n -= len(raw[i])
		}
	}
	return n
}

// scenarioLoss lists what a MODIFIED block takes away from the requirement
// it replaces: a scenario it drops, or a scenario whose check it changes.
// Both silently weaken the living spec — the behaviour stays "specified"
// while the proof of it goes — so neither is a modification ape accepts.
// Changed behaviour is a REMOVED plus an ADDED under a new name.
func scenarioLoss(existing, modified *Requirement) []string {
	byName := map[string]*Scenario{}
	for i := range modified.Scenarios {
		byName[modified.Scenarios[i].Name] = &modified.Scenarios[i]
	}
	var out []string
	for i := range existing.Scenarios {
		old := &existing.Scenarios[i]
		now, ok := byName[old.Name]
		if !ok {
			out = append(out, fmt.Sprintf("drops scenario %q", old.Name))
			continue
		}
		if oldCheck, newCheck := firstCheck(old), firstCheck(now); oldCheck != newCheck {
			out = append(out, fmt.Sprintf("changes the check of scenario %q from %#q to %#q", old.Name, oldCheck, newCheck))
		}
	}
	return out
}

func firstCheck(s *Scenario) string {
	if len(s.Checks) == 0 {
		return ""
	}
	return s.Checks[0]
}
