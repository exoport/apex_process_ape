package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// inFlightUntyped is a reviewed change with no type:, as an ape before
// rc.7 started it under the old default "{title}" (Q111): its plan digest
// is the untyped plan's, so the plan reads as frozen.
func inFlightUntyped(t *testing.T, files map[string]string) *Project {
	t.Helper()
	p := reviewedWaveFrom(t, files)
	doc := untyped(read(t, p, "development/changes/add-wave/change.md"))
	write(t, p, "development/changes/add-wave/change.md", doc)
	st := state(t, p, "add-wave")
	st.PlanDigest = ParseChange("change.md", doc).PlanDigest()
	require.NoError(t, SaveState(p.StateFile("add-wave"), st))
	commitAll(t, p, "apex: add-wave started before rc.7")
	return p
}

// Q111: under the default template only, a change in flight with no type:
// archives with its title alone, and the result says so.
func TestArchive_DefaultSubjectFallsBackForAnUntypedChange(t *testing.T) {
	p := inFlightUntyped(t, baseFiles())
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "Add wave", res.Subject)
	require.True(t, res.SubjectFallback)
	require.Equal(t, "Add wave", subjects(t, p)[0])
}

// A configured {type} template still refuses it, as before rc.7.
func TestArchive_ConfiguredTypeTemplateStillRefusesAnUntypedChange(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "commit:\n  subject: \"{type}: {title}\"\n"
	p := inFlightUntyped(t, files)
	_, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	requireCode(t, err, CodeCheckFailed)
	require.ErrorContains(t, err, "configured commit subject")
}

// Q109: an explicit commit.subject always wins; a typed change takes the
// default "{type}: {title}" (TestArchive) and no fallback.
func TestArchive_ExplicitSubjectWins(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "commit:\n  subject: \"{title}\"\n"
	p := reviewedWaveFrom(t, files)
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "Add wave", res.Subject)
	require.False(t, res.SubjectFallback)
	require.Equal(t, "{title}", p.Status().Config.CommitSubject)
}

// Q109: ape status --json config.commit_subject is the effective template.
func TestStatus_CommitSubject(t *testing.T) {
	p := fixture(t, baseFiles())
	require.Equal(t, "{type}: {title}", p.Status().Config.CommitSubject)
	require.Equal(t, DefaultSubject, p.Status().Config.CommitSubject)
}

// Q111: project check passes a started change with no type: under the
// default (its archive takes the title); a draft, change check and start
// still refuse.
func TestCheck_UntypedChangeInFlight(t *testing.T) {
	p := inFlightUntyped(t, baseFiles())
	for _, f := range p.Check() {
		require.NotContains(t, f.Message, "has no `type:`", "project check passes the in-flight change")
	}
	require.Contains(t, findingMessages(p.CheckChange("add-wave")), "the default archive subject (D193) \"{type}: {title}\" uses {type}")

	q := fixture(t, baseFiles())
	addChange(t, q, "add-hug", untyped(changeDoc("add-hug", "greeting", addedWave, "- T1 x — check: true")))
	commitAll(t, q, "plan: add-hug")
	require.Contains(t, findingMessages(q.Check()), "has no `type:`", "a draft is still flagged: start will refuse it")
	_, err := q.Start(ctx(), "add-hug", RunOptions{})
	requireCode(t, err, CodeCheckFailed)
}

// Q110: change new writes type: — --type, else fix for a tweak and feat
// otherwise; an unknown --type is usage.type; a project whose types lack
// the default gets none.
func TestNewChange_Type(t *testing.T) {
	p := fixture(t, baseFiles())
	for _, c := range []struct{ id, milestone, typ, want string }{
		{"add-wave", "M1", "", "feat"},
		{"fix-typo", MilestoneNone, "", "fix"},
		{"document-it", "M1", "docs", "docs"},
	} {
		res, err := p.NewChangeOfType(ctx(), c.id, c.milestone, c.typ, []string{"greeting"}, nil)
		require.NoError(t, err, c.id)
		require.Equal(t, c.want, res.Type, c.id)
		ch := ParseChange("change.md", read(t, p, "development/changes/"+c.id+"/change.md"))
		require.Equal(t, c.want, ch.Meta.Type, c.id)
	}
	require.NotContains(t, findingMessages(p.CheckChange("add-wave")), "type", "a skeleton's type passes")
	_, err := p.NewChangeOfType(ctx(), "add-hug", "M1", "feature", []string{"greeting"}, nil)
	requireCode(t, err, "usage.type")
	require.ErrorContains(t, err, "feat, fix, perf")

	files := baseFiles()
	files["_apex/config.yaml"] = "commit:\n  types: [draft, edit]\n"
	q := fixture(t, files)
	res, err := q.NewChange(ctx(), "add-wave", "M1", []string{"greeting"}, nil)
	require.NoError(t, err)
	require.Empty(t, res.Type)
	require.NotContains(t, read(t, q, "development/changes/add-wave/change.md"), "type:")
	res, err = q.NewChangeOfType(ctx(), "add-hug", "M1", "edit", []string{"greeting"}, nil)
	require.NoError(t, err)
	require.Equal(t, "edit", res.Type)
	require.Contains(t, read(t, q, "development/changes/add-hug/change.md"), "\ntype: edit\n")
}
