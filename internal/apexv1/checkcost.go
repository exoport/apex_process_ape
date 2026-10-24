package apexv1

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// v1.1 W2: what the checks cost at test time. A gate runs the whole spec,
// and a scenario check is a shell command: two checks that start with the
// same slow command pay for it twice, as RunChecks shares only a command
// that is identical as a whole (Part 3). ape memoizes nothing: the notice
// names the scenarios, and the plan merges or splits them.

// slowCheckSecs is "ran over a minute at the last gate" (W2 Parts 2, 3).
const slowCheckSecs = 60.0

// firstSimpleCommand is the command a check runs first: cmd up to its
// first top-level && or | (|| and |& included), trimmed, its words joined
// by single spaces. A command holding no such operator is itself; "" for
// one ape does not split — a multi-line command, unbalanced quotes, a
// leading ( … ) group.
func firstSimpleCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || strings.Contains(cmd, "\n") {
		return ""
	}
	toks, _, balanced := shellTokens(cmd)
	if !balanced {
		return ""
	}
	var words []string
	for _, t := range toks {
		if t.op == "" {
			words = append(words, t.word)
			continue
		}
		if t.op == "&&" || t.op == "||" || t.op == "|" || t.op == "|&" {
			break
		}
		// ; & ( ): a list or a group ape does not cut.
		return ""
	}
	return strings.Join(words, " ")
}

// sharedFirstCommand is one first command two or more scenario checks of
// the spec start with, each check otherwise its own.
type sharedFirstCommand struct {
	first  string
	checks []PlannedCheck
}

// sharedFirstCommands groups the scenario checks by first command, kept
// where two or more different commands share one. Identical commands run
// once (RunChecks) and count once.
func sharedFirstCommands(checks []PlannedCheck) []sharedFirstCommand {
	byFirst := map[string][]PlannedCheck{}
	var order []string
	for i := range checks {
		pc := checks[i]
		first := firstSimpleCommand(pc.Cmd)
		if first == "" {
			continue
		}
		if _, ok := byFirst[first]; !ok {
			order = append(order, first)
		}
		byFirst[first] = append(byFirst[first], pc)
	}
	var out []sharedFirstCommand
	for _, first := range order {
		group := byFirst[first]
		distinct := map[string]bool{}
		for i := range group {
			distinct[group[i].Cmd] = true
		}
		if len(distinct) >= 2 {
			out = append(out, sharedFirstCommand{first: first, checks: group})
		}
	}
	return out
}

// duplicateRunNotices are project check's checks.duplicate-run notices (W2
// Part 3): scenario checks of the spec that start with the same command,
// which a gate then runs once per check, when one of them is full or each
// of them ran over a minute at the last gate. One notice per shared
// command, at its first scenario, naming the scenarios and what they cost
// per gate.
func (p *Project) duplicateRunNotices() []Diagnostic {
	gateID, gate := p.latestGate()
	var out []Diagnostic
	for _, g := range sharedFirstCommands(p.EffectiveSpecChecks(nil)) {
		var total float64
		names := make([]string, 0, len(g.checks))
		slow, full := gate != nil, false
		seen := map[string]bool{}
		for i := range g.checks {
			pc := &g.checks[i]
			names = append(names, pc.Ref)
			full = full || pc.Full
			var secs float64
			ok := false
			if gate != nil {
				secs, ok = gate.ScenarioSecs[pc.Ref]
			}
			slow = slow && ok && secs > slowCheckSecs
			if ok && !seen[pc.Cmd] {
				seen[pc.Cmd] = true
				total += secs
			}
		}
		if !slow && !full {
			continue
		}
		sort.Strings(names)
		cost := "not timed at a gate yet"
		if len(seen) > 0 {
			cost = fmt.Sprintf("%s of scenario time at %s's last gate", roundSecs(total), gateID)
		}
		why := "each over a minute"
		if full {
			why = "full"
		}
		pc := &g.checks[0]
		out = append(out, notice(Diagnostic{Code: CodeChecksDuplicateRun, File: p.Rel(p.SpecFile(pc.Capability)), Line: pc.Line, Message: fmt.Sprintf(
			"%d scenario checks start with `%s` (%s), so a gate runs it %d times: %s, %s"+
				" — give the command one scenario and have the others check what it leaves under development/, or merge the scenarios; ape runs a shared command once only when the whole check is the same",
			len(g.checks), g.first, strings.Join(limit(names, 5), ", "), distinctCmds(g.checks), cost, why)}))
	}
	return out
}

// distinctCmds counts the different commands of checks: the runs a gate
// makes of them.
func distinctCmds(checks []PlannedCheck) int {
	seen := map[string]bool{}
	for i := range checks {
		seen[checks[i].Cmd] = true
	}
	return len(seen)
}

// fullSuggestedNotices are project check's check.full-suggested notices
// (W2 Part 2): a scenario check of the spec, not full, that ran over a
// minute at the last gate — every verify of a change touching its
// capability pays for it. Marked full, verify runs it only when it is the
// change's own or was red at the last gate.
func (p *Project) fullSuggestedNotices() []Diagnostic {
	gateID, gate := p.latestGate()
	if gate == nil {
		return nil
	}
	var out []Diagnostic
	spec := p.EffectiveSpecChecks(nil)
	for i := range spec {
		pc := &spec[i]
		secs, ok := gate.ScenarioSecs[pc.Ref]
		if pc.Full || !ok || secs <= slowCheckSecs {
			continue
		}
		out = append(out, notice(Diagnostic{Code: CodeCheckFullSuggested, File: p.Rel(p.SpecFile(pc.Capability)), Line: pc.Line, Message: fmt.Sprintf(
			"scenario %s's check took %s at %s's last gate, and every verify of a change touching %s runs it"+
				" — if it need not run on every change (it needs a service, a network or a browser, or it is long), write it `- check (full): %s`: verify then runs it only for the change's own scenarios or when it was red at the last gate, and a gate whose Gate: command is red skips it",
			pc.Ref, roundSecs(secs), gateID, pc.Capability, pc.Cmd)}))
	}
	return out
}

// roundSecs renders seconds as a duration to the second.
func roundSecs(secs float64) time.Duration {
	return time.Duration(secs * float64(time.Second)).Round(time.Second)
}
