package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Item 5 (Q45): project check reports a models: key that names no skill and
// an entry field other than model and effort — each is otherwise ignored
// in silence. The delegate probe's `models: {effort: high}` pinned a
// skill named "effort".
func TestProjectCheck_UnknownModels(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	type want struct{ code, msg string }
	for name, tc := range map[string]struct {
		config string
		want   []want
	}{
		"defaults and the scalar form pass": {config: strings.Replace(modelDefaultsYAML, "apex-review: {model: opus, effort: medium}", "apex-review: opus", 1)},
		"a project skill passes":            {config: "models:\n  team-skill: {model: haiku, effort: low}\n"},
		"a tool's skill passes": {config: "models:\n  tool-skill: sonnet\n" +
			"tools:\n  review:\n    - {name: lint, skill: tool-skill, when: always}\n"},
		"the delegate probe's effort key": {config: "models:\n  effort: high\n", want: []want{{
			CodeModelsUnknownSkill,
			"models.effort is not a skill — a skill's effort goes inside its entry: models.<skill>: {model: sonnet, effort: high}; the default effort of every session is the top-level effort:",
		}}},
		"a model key": {config: "models:\n  model: opus\n", want: []want{{CodeModelsUnknownSkill, "models.model is not a skill — a skill's model goes inside its entry"}}},
		"a typo'd skill": {config: "models:\n  apex-aply: sonnet\n", want: []want{{
			CodeModelsUnknownSkill,
			"models.apex-aply names no skill installed in the project (.claude/skills/apex-aply/SKILL.md) nor one of ape's defaults (apex-apply, apex-loop, apex-review, apex-plan, apex-change, apex-accept, apex-release), so it pins nothing — fix the skill name, or drop the entry",
		}}},
		"an unknown entry field": {config: "models:\n  apex-apply: {model: sonnet, efort: high, budget: 3}\n", want: []want{
			{CodeModelsUnknownField, "models.apex-apply.efort is not a field of a models entry, so it does nothing — an entry is a model name or {model, effort}: fix or drop efort"},
			{CodeModelsUnknownField, "models.apex-apply.budget is not a field"},
		}},
		"both on one entry": {config: "models:\n  nope: {modl: opus}\n", want: []want{{CodeModelsUnknownSkill, "models.nope names no skill"}, {CodeModelsUnknownField, "models.nope.modl is not a field"}}},
	} {
		t.Run(name, func(t *testing.T) {
			files := baseFiles()
			files[".claude/skills/team-skill/SKILL.md"] = "---\nname: team-skill\ndescription: d\n---\nbody\n"
			files[".claude/skills/tool-skill/SKILL.md"] = "---\nname: tool-skill\ndescription: d\n---\nbody\n"
			files["_apex/config.yaml"] = tc.config
			p := fixture(t, files)
			var got []Finding
			for _, f := range ClassifyFindings(p.Check()) {
				if f.Code == CodeModelsUnknownSkill || f.Code == CodeModelsUnknownField {
					got = append(got, f)
				}
			}
			require.Len(t, got, len(tc.want), "%v", got)
			for i, w := range tc.want {
				require.Equal(t, w.code, got[i].Code)
				require.Equal(t, "_apex/config.yaml", got[i].File)
				require.Contains(t, got[i].Message+" — "+got[i].Fix, w.msg)
				require.Equal(t, ActionPerson, ActionFor(got[i].Code))
			}
		})
	}
}
