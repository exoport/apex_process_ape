package apexv1

import (
	"regexp"
	"sort"
	"strings"
)

// v1.4: two things change check (and start, which runs it) now finds at
// plan time that used to surface only once a change was applied.
//
//   - change.guarded-config-task (B6): a task whose own words — the text
//     before its `— check:` — name _apex/config.yaml and a guarded key. A
//     guarded key is a person's (config.changed), so a loop that applies
//     the task is certain to stop at it; twice a plan did exactly that with
//     regression: (eval F-Q4-1, ledger r1). Found by words, so a task that
//     only reads the config ("the check greps it") is not one: its check is
//     not read. Action replan: the change drops the task and leaves the
//     value under ## Notes for the person.
//   - change.touches-gate (v1.2 F4's residual): the change's frontmatter
//     paths:, the files it says it will edit, cover a gate file a person
//     approved. Verify refuses the edit (RK-6 R2) one apply later; the
//     plan can say so before it.

// CodeGuardedConfigTask: a change task edits a guarded config key.
const CodeGuardedConfigTask = "change.guarded-config-task"

var (
	configFileRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_./-])_apex/config\.ya?ml(?:$|[^A-Za-z0-9_-])`)
	// guardedKeyRe is a guarded key written as a YAML key: `regression:`,
	// timeouts.check:, never part of a longer word or a `::` scope.
	guardedKeyRe = regexp.MustCompile("(?:^|[^A-Za-z0-9_.-])`?(" + strings.Join(guardedConfigKeys, "|") + ")(?:\\.[a-z_.]+)?`?\\s*:(?:[^:]|$)")
)

func init() {
	findingRules = append(findingRules,
		rule("", `^task T\d+ edits a guarded config key`, CodeGuardedConfigTask),
		rule("", `^paths: ".*" covers .*, a gate (file|glob) a person approved`, CodeTouchesGate),
	)
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeGuardedConfigTask, 1, "replan", "change check / start (v1.4, B6): a task's own words (before its `— check:`) name _apex/config.yaml and a guarded key (limits, checks, retries, requires, timeouts, caps, regression, commit, evidence, marketplaces, plugins, tools, human_review, release, secrets) — a guarded key is a person's (config.changed), so the loop would stop at it: drop the task and leave the proposed value under ## Notes for the person, who sets it and runs `ape project accept-config`"},
	)
}

// guardedConfigTaskFindings are change check's findings for tasks that
// edit a guarded config key.
func guardedConfigTaskFindings(file string, c *Change) []Finding {
	var out []Finding
	for _, t := range c.Tasks {
		if !configFileRe.MatchString(t.What) {
			continue
		}
		m := guardedKeyRe.FindStringSubmatch(t.What)
		if m == nil {
			continue
		}
		out = append(out, Finding{File: file, Line: t.Line, Message: "task " + t.ID + " edits a guarded config key (" + m[1] + ":) in _apex/config.yaml" +
			" — a guarded key is a person's (config.changed), so the loop would stop at it: drop the task and leave the value under ## Notes for the person" +
			" (they set it and run `ape project accept-config --by <who>`)"})
	}
	return out
}

// gateFrozen is what a person approved of the gates c may not edit: the
// recorded files (gate_files, gate_own_files) and globs (gate_globs) of
// its milestone's approval, or of every approved milestone for a tweak,
// each with the milestone it belongs to.
func (p *Project) gateFrozen(milestone string) (files, globs map[string]string) {
	files, globs = map[string]string{}, map[string]string{}
	rm := p.roadmap()
	if rm == nil {
		return files, globs
	}
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return files, globs
	}
	for i := range rm.Milestones {
		m := &rm.Milestones[i]
		if m.Status != MilestoneApproved || milestone != m.ID && milestone != MilestoneNone {
			continue
		}
		rec := recs[m.ID]
		if rec == nil || rec.Approval == nil {
			continue
		}
		for _, set := range []map[string]string{rec.Approval.GateFiles, rec.Approval.GateOwnFiles} {
			for f := range set {
				files[f] = m.ID
			}
		}
		for _, g := range rec.Approval.GateGlobs {
			globs[g] = m.ID
		}
	}
	return files, globs
}

// gateFrozenFindings are change check's change.touches-gate findings: a
// paths: entry that covers a recorded gate file, or meets a recorded glob.
func (p *Project) gateFrozenFindings(file string, c *Change) []Finding {
	if len(c.Meta.Paths) == 0 || c.Meta.Milestone == "" {
		return nil
	}
	files, globs := p.gateFrozen(c.Meta.Milestone)
	if len(files) == 0 && len(globs) == 0 {
		return nil
	}
	var out []Finding
	for _, g := range c.Meta.Paths {
		re, err := globRegexp(g)
		if err != nil {
			continue
		}
		var hit []string
		for f, m := range files {
			if re.MatchString(f) {
				hit = append(hit, f+" ("+m+"'s gate)")
			}
		}
		kind := "file"
		if len(hit) == 0 {
			for fg, m := range globs {
				if globsMeet(g, fg) {
					hit = append(hit, fg+" ("+m+"'s gate)")
				}
			}
			kind = "glob"
		}
		if len(hit) == 0 {
			continue
		}
		sort.Strings(hit)
		out = append(out, Finding{File: file, Line: 1, Message: "paths: \"" + g + "\" covers " + strings.Join(limit(hit, 4), ", ") + ", a gate " + kind + " a person approved" +
			" — verify would refuse the edit (change.touches-gate): only a person or apex-plan changes a gate, and a person approves it again;" +
			" drop it from paths: and the tasks, or, if the gate itself is wrong, say so (`gate defect: <evidence>`)"})
	}
	return out
}

// GateFrozen is an approved milestone's frozen gate set as ape status
// shows it (v1.2 F4's residual): what a change of the milestone may not
// edit, so apex-change sees it when it plans.
type GateFrozen struct {
	Files []string `json:"files"`
	Globs []string `json:"globs,omitempty"`
}

// gateFrozenOf is rec's approval's frozen set, nil when it records none.
func gateFrozenOf(rec *MilestoneRecord) *GateFrozen {
	if rec == nil || rec.Approval == nil {
		return nil
	}
	a := rec.Approval
	if len(a.GateFiles) == 0 && len(a.GateOwnFiles) == 0 && len(a.GateGlobs) == 0 {
		return nil
	}
	g := &GateFrozen{Files: []string{}}
	for _, set := range []map[string]string{a.GateFiles, a.GateOwnFiles} {
		for f := range set {
			g.Files = append(g.Files, rec.ArchivedPath(f))
		}
	}
	sort.Strings(g.Files)
	g.Globs = append(g.Globs, a.GateGlobs...)
	return g
}
