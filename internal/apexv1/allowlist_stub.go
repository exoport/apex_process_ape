package apexv1

// ChildSkillAllowList is the skills an apex-orchestrator run may start as
// its child sessions (D62): the core skills. rc3-d's real helper adds the
// skills declared under tools:; this stub, with the same signature, is
// deleted when the branches merge.
func (p *Project) ChildSkillAllowList(_ Config) []string {
	return append([]string(nil), CoreChildSkills...)
}
