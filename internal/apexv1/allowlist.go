package apexv1

// CoreChildSkills are the core skills ape's apex-loop run may start as a
// child session at depth 1 (D62), in the order of the loop, then
// apex-accept, the default human-review delegate (D76).
//
// apex-handoff, apex-loop and apex-orchestrator are deliberately absent:
// handoff is a person's (or a peer conductor's) tool for clearing a
// session, never a step of the loop; a loop starting another loop is the
// nesting D62 exists to prevent; and the operator's apex-orchestrator
// opens only in a person's own session (Q95).
//
// apex-release is absent since v1.4 (D524): a release is prepared only on
// a person's instruction, so a loop run can never start it — the v1.3
// skill rule, made a mechanism. Nothing in the config adds it back
// (NeverChildSkill).
var CoreChildSkills = []string{"apex-plan", "apex-change", "apex-apply", "apex-review", "apex-accept"}

// NeverChildSkill reports whether skill may never join the allow-list,
// whatever the config names: apex-release (D524), as human_review's
// delegate or a tools: entry alike.
func NeverChildSkill(skill string) bool { return skill == ReleaseSkill }

// ReleaseSkill is the skill that prepares a release (D524).
const ReleaseSkill = "apex-release"

// ChildSkillAllowList is the depth-1 allow-list (D62, D59 Q4, D76): the
// core skills, then human_review's delegate, then every skill the config's
// tools: slots name (design,
// then review, in config order), each once. A tool's skill is listed as
// declared — <plugin>:<skill> for a plugin's — which is the name ape task
// types. Only valid tools: entries count (an invalid one is a
// tools.invalid finding, never an allowance).
func (p *Project) ChildSkillAllowList(cfg Config) []string {
	out := append([]string{}, CoreChildSkills...)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s] = true
	}
	// The configured human-review delegate (D75, D76), when it is not
	// apex-accept.
	if d := cfg.HumanReview.Skill(); d != "" && !seen[d] && !NeverChildSkill(d) {
		seen[d] = true
		out = append(out, d)
	}
	for _, slot := range ToolSlots {
		ts := cfg.Tools.Slot(slot)
		for i := range ts {
			if s := ts[i].Skill; !seen[s] && !NeverChildSkill(s) {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}
