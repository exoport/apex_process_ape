package apexv1

// CoreChildSkills are the core skills ape's apex-orchestrator run may start
// as a child session at depth 1 (D62), in the order of the loop, then
// apex-accept, the default human-review delegate (D76).
//
// apex-handoff and apex-orchestrator are deliberately absent: handoff is a
// person's (or a peer conductor's) tool for clearing a session, never a
// step of the loop, and an orchestrator starting another orchestrator is
// the nesting D62 exists to prevent.
var CoreChildSkills = []string{"apex-plan", "apex-change", "apex-apply", "apex-review", "apex-release", "apex-accept"}

// ChildSkillAllowList is the depth-1 allow-list (D62, D59 Q4, D76): the
// core skills, then every skill the config's tools: slots name (design,
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
	// rc3-f: + human_review.delegate (the configured delegate skill, when
	// set), appended here once Config carries HumanReview.
	for _, slot := range ToolSlots {
		for _, t := range cfg.Tools.Slot(slot) {
			if !seen[t.Skill] {
				seen[t.Skill] = true
				out = append(out, t.Skill)
			}
		}
	}
	return out
}
