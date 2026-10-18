package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// D120: commit.types replaces the default type list in change check, and
// is validated as it parses; absent, the rc.4 list and messages exactly.
func TestCheckChange_ProjectCommitTypes(t *testing.T) {
	typed := func(id, typ string) string {
		return strings.Replace(changeDoc(id, "greeting", addedWave, "- T1 x — check: true"), "milestone: M1\n", "milestone: M1\ntype: "+typ+"\n", 1)
	}
	files := baseFiles()
	files["_apex/config.yaml"] = "commit:\n  types: [draft, edit, feat]\n"
	p := fixture(t, files)
	addChange(t, p, "add-draft", typed("add-draft", "draft"))
	require.Empty(t, findingMessages(p.CheckChange("add-draft")), "a listed project type passes")
	addChange(t, p, "add-fix", typed("add-fix", "fix"))
	fs := ClassifyFindings(p.CheckChange("add-fix"))
	require.Len(t, fs, 1)
	require.Equal(t, "change.bad-type", fs[0].Code)
	require.Contains(t, fs[0].Message, `type "fix" is not one of draft, edit, feat`, "the project's list replaces the default")

	q := fixture(t, baseFiles())
	addChange(t, q, "add-hug", typed("add-hug", "feature"))
	require.Contains(t, findingMessages(q.CheckChange("add-hug")),
		`frontmatter type "feature" is not one of feat, fix, perf, refactor, docs, test, build, ci, chore — pick the one that fits`,
		"absent: the rc.4 list and message")
}

func TestCommitAndReleaseConfig_Parse(t *testing.T) {
	for name, c := range map[string]struct{ cfg, err string }{
		"types ok":           {"commit:\n  types: [feat, fix, draft-2]\n", ""},
		"types empty":        {"commit:\n  types: []\n", "commit.types: list at least one type"},
		"types upper":        {"commit:\n  types: [Feat]\n", `commit.types: "Feat" is not a lower-case token`},
		"types twice":        {"commit:\n  types: [feat, feat]\n", `commit.types: "feat" is listed twice`},
		"commit unknown key": {"commit:\n  type: [feat]\n", "commit.type: not a key ape knows"},
		"scheme semver":      {"release:\n  scheme: semver\n", ""},
		"scheme date":        {"release:\n  scheme: date\n", ""},
		"scheme edition":     {"release:\n  scheme: edition\n", ""},
		"scheme bad":         {"release:\n  scheme: calver\n", `release.scheme: "calver" is not semver, date or edition`},
		"release unknown":    {"release:\n  schema: date\n", "release.schema: not a key ape knows"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Open(writeConfig(t, c.cfg))
			if c.err == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, c.err)
		})
	}
	cfg, err := LoadConfig(writeConfig(t, "project_name: x\n"))
	require.NoError(t, err)
	require.Equal(t, CommitTypes, cfg.Commit.TypeList(), "absent: the default list")
	require.Equal(t, ReleaseSemver, cfg.Release.SchemeName(), "absent: semver")
}

// D120: release is guarded like commit; a model's edit to either is
// config.changed until a person accepts it.
func TestConfigChanged_GuardsCommitTypesAndRelease(t *testing.T) {
	p := fixture(t, baseFiles())
	epoch := HeadSHA(ctx(), p.Root)
	write(t, p, "_apex/config.yaml", "release:\n  scheme: date\n")
	commitAll(t, p, "chore: date releases")
	fs := p.configFindings(ctx(), epoch)
	require.Len(t, fs, 1)
	require.Contains(t, fs[0].Message, "guarded config keys (release) changed")

	write(t, p, "_apex/config.yaml", "release:\n  scheme: date\ncommit:\n  types: [draft]\n")
	commitAll(t, p, "chore: draft type")
	fs = p.configFindings(ctx(), epoch)
	require.Len(t, fs, 2)
	require.Contains(t, fs[1].Message+fs[0].Message, "guarded config keys (commit) changed")
}

// D120 Q55: ape status --json carries the effective values.
func TestStatus_Config(t *testing.T) {
	p := fixture(t, baseFiles())
	r := p.Status()
	require.Equal(t, CommitTypes, r.Config.CommitTypes)
	require.Equal(t, ReleaseSemver, r.Config.ReleaseScheme)
	write(t, p, "_apex/config.yaml", "commit:\n  types: [draft, edit]\nrelease:\n  scheme: edition\n")
	r = p.Status()
	require.Equal(t, []string{"draft", "edit"}, r.Config.CommitTypes)
	require.Equal(t, ReleaseEdition, r.Config.ReleaseScheme)
}
