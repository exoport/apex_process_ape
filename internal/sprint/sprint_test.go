package sprint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/stretchr/testify/require"
)

// --- Load / classify ---

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"epic-1":               KindEpic,
		"epic-12":              KindEpic,
		"1-1":                  KindStory,
		"12-3":                 KindStory,
		"12-3-slug":            KindStory,
		"12-3_slug":            KindStory,
		"epic-3-retrospective": KindRetrospective,
		"retrospective-2026":   KindRetrospective,
		"something-else":       KindOther,
	}
	for key, want := range cases {
		require.Equal(t, want, classify(key), "key %q", key)
	}
}

func TestLoad_MissingFileIsNotAnError(t *testing.T) {
	tracker, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	require.NoError(t, err)
	require.True(t, tracker.Missing)
}

func TestNormalizeStatus(t *testing.T) {
	require.Equal(t, StatusReadyForDev, NormalizeStatus("drafted"))
	require.Equal(t, StatusReadyForDev, NormalizeStatus(" Drafted "))
	require.Equal(t, StatusReadyForDev, NormalizeStatus("ready-for-dev"))
	require.Equal(t, "done", NormalizeStatus("done"))
}

// --- Project: the truth table ---

func TestProject_TruthTable(t *testing.T) {
	rows := func(statuses ...string) []Row {
		out := make([]Row, 0, len(statuses))
		for i, s := range statuses {
			out = append(out, Row{
				Key: "7-" + string(rune('1'+i)), Status: s, Kind: KindStory, Epic: 7,
			})
		}
		return out
	}
	cases := []struct {
		name  string
		rows  []Row
		want  string
		unrec []string
	}{
		{"no rows at all", nil, "", nil},
		{"all cancelled", rows("cancelled", "cancelled"), "", nil},
		{"all done", rows("done", "done"), StatusDone, nil},
		{"all backlog", rows("backlog", "backlog"), StatusBacklog, nil},
		{"mixed", rows("done", "backlog"), StatusInProgress, nil},
		{"done plus cancelled closes", rows("done", "cancelled"), StatusDone, nil},
		{"backlog plus cancelled stays backlog", rows("backlog", "cancelled"), StatusBacklog, nil},
		{"blocked holds it open", rows("done", "blocked"), StatusInProgress, nil},
		{"blocked alone holds it open", rows("blocked"), StatusInProgress, nil},
		{"in-progress", rows("done", "in-progress"), StatusInProgress, nil},
		{"review holds it open", rows("done", "review"), StatusInProgress, nil},
		{"drafted holds it open", rows("done", "drafted"), StatusInProgress, nil},
		{"single done", rows("done"), StatusDone, nil},
		{
			"unrecognised can only hold open, never close",
			rows("done", "banana"), StatusInProgress,
			[]string{"banana"},
		},
		{
			"unrecognised alone still holds open",
			rows("banana"), StatusInProgress,
			[]string{"banana"},
		},
		{
			"unrecognised is named once",
			rows("banana", "banana"), StatusInProgress,
			[]string{"banana"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, unrec := Project(tc.rows, 7)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.unrec, unrec)
		})
	}
}

func TestProject_IgnoresOtherEpicsAndKinds(t *testing.T) {
	rows := []Row{
		{Key: "7-1", Status: "done", Kind: KindStory, Epic: 7},
		{Key: "8-1", Status: "backlog", Kind: KindStory, Epic: 8},
		{Key: "epic-7", Status: "in-progress", Kind: KindEpic, Epic: 7},
		{Key: "epic-7-retrospective", Status: "backlog", Kind: KindRetrospective, Epic: 0},
	}
	got, _ := Project(rows, 7)
	require.Equal(t, StatusDone, got, "only epic 7's story rows count")
}

// --- Reconcile ---

// --- VerifyRow ---

// --- RunCheck ---

func newCheckFixture(t *testing.T) (cfg *apexcfg.Resolved, write func(name, body string)) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, apexcfg.DirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, apexcfg.DirName, apexcfg.BaseFile),
		[]byte("project_name: fx\nextensions: []\ndevelopment_folder: development\n"+
			"implementation_folder: development/implementation\n"), 0o644))
	cfg, err := apexcfg.ResolveAt(root, nil)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(cfg.Paths.Implementation, 0o755))
	write = func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(cfg.Paths.Implementation, name), []byte(body), 0o644))
	}
	return cfg, write
}

// storyFile writes a story the way the framework does: the file STEM is the
// story key the tracker rows on, and the frontmatter `story_id` is the
// separate dotted form. Every RunCheck fixture below uses it, so a
// regression to joining on story_id fails these tests rather than passing
// them — which is how the join bug survived the first time round.
func storyFile(t *testing.T, write func(name, body string), key, dottedID, status string) {
	t.Helper()
	write(key+".md", "---\nstory_id: \""+dottedID+"\"\nepic: \"1\"\nstatus: "+status+
		"\noutput_document: development/implementation/"+key+".md\n---\n\nx\n")
}

func TestRunCheck_Clean(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  1-1_greet-a-name: done\n  epic-1-retrospective: optional\n")
	storyFile(t, write, "1-1_greet-a-name", "1.1", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.True(t, report.OK(), "%+v", report.Findings)
	require.Equal(t, 1, report.Summary.StoryRows)
	require.Equal(t, 1, report.Summary.StoriesOnDisk)
}

func TestRunCheck_RowWithoutStory(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  1-1_greet-a-name: done\n  9-9_ghost: backlog\n  epic-1-retrospective: optional\n")
	storyFile(t, write, "1-1_greet-a-name", "1.1", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	// The ghost row also puts epic 9 in the epic set with no retrospective,
	// which is a true statement about this tracker and a second finding.
	// Filtered rather than fixtured away: adding a retro row for a phantom
	// epic would be inventing the very thing the ghost row is wrong about.
	rows := findingsOf(report, CheckRowWithoutStory)
	require.Len(t, rows, 1)
	require.Equal(t, "9-9_ghost", rows[0].Key)
}

// findingsOf narrows a report to one check class, for a test about that
// class rather than about the whole report.
func findingsOf(report *CheckReport, check string) []Finding {
	var out []Finding
	for i := range report.Findings {
		if report.Findings[i].Check == check {
			out = append(out, report.Findings[i])
		}
	}
	return out
}

func TestRunCheck_StoryWithoutRow(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  1-1_greet-a-name: done\n  epic-1-retrospective: optional\n")
	storyFile(t, write, "1-1_greet-a-name", "1.1", "done")
	storyFile(t, write, "1-2_present-the-form", "1.2", "backlog")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Len(t, report.Findings, 1)
	require.Equal(t, CheckStoryWithoutRow, report.Findings[0].Check)
	require.Equal(t, "1-2_present-the-form", report.Findings[0].Key)
	require.Equal(t, "1.2", report.Findings[0].StoryID,
		"the dotted id travels alongside the key, because they are different strings")
}

func TestRunCheck_StatusDivergenceNamesBothSides(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  1-1_greet-a-name: done\n  epic-1-retrospective: optional\n")
	storyFile(t, write, "1-1_greet-a-name", "1.1", "in-progress")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Len(t, report.Findings, 1)
	require.Equal(t, CheckStatusDivergence, report.Findings[0].Check)
	require.Equal(t, "done", report.Findings[0].Tracker)
	require.Equal(t, "in-progress", report.Findings[0].Story)
	require.Contains(t, report.Findings[0].Message, "neither is assumed correct")
}

// TestRunCheck_DraftedIsNotADivergence: normalising the tracker's
// `drafted` to a story file's `ready-for-dev` is what stops this reporting
// a divergence on every drafted story.
func TestRunCheck_DraftedIsNotADivergence(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  1-1_greet-a-name: drafted\n  epic-1-retrospective: optional\n")
	storyFile(t, write, "1-1_greet-a-name", "1.1", "ready-for-dev")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.True(t, report.OK())
}

func TestRunCheck_EpicAndRetroRowsClassifiedOut(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml", `development_status:
  epic-1: done
  epic-1-retrospective: done
  1-1_greet-a-name: done
`)
	storyFile(t, write, "1-1_greet-a-name", "1.1", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.True(t, report.OK(), "epic and retro rows have no story file to diverge from")
	require.Equal(t, 1, report.Summary.EpicRows)
	require.Equal(t, 1, report.Summary.RetroRows)
}

// TestStoryKeyFromPath pins the join key itself.
func TestStoryKeyFromPath(t *testing.T) {
	require.Equal(t, "1-1_greet-a-name", StoryKeyFromPath("1-1_greet-a-name.md"))
	require.Equal(t, "1-1_greet-a-name", StoryKeyFromPath("development/implementation/1-1_greet-a-name.md"))
	require.Equal(t, "1-1", StoryKeyFromPath("1-1.md"))
	require.Equal(t, "no-extension", StoryKeyFromPath("no-extension"))
}

// TestRunCheck_BareRowKeyIsItsOwnFinding: `2-1` names no story file AND is
// counted differently by the two live reconcile implementations. One finding
// carries both halves plus the fix — reporting it as row_without_story would
// send a reader off to create `2-1.md` when the row is what is wrong.
func TestRunCheck_BareRowKeyIsItsOwnFinding(t *testing.T) {
	cfg, write := newCheckFixture(t)
	// epic-2 reads `done` and not `backlog` so this fixture carries exactly
	// ONE defect. ape counts a bare row toward its epic, so `2-1: done`
	// projects `done`, and an epic row asserting `backlog` would be a real
	// and separate epic_projection_divergence — see
	// TestRunCheck_BareRowCountsTowardTheProjection, which is where that
	// interaction belongs. Here it would only blunt the count below.
	write("sprint-status.yaml",
		"development_status:\n  epic-2: done\n  2-1: done\n  epic-2-retrospective: optional\n")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Len(t, report.Findings, 1, "one finding, not two: %+v", report.Findings)

	f := report.Findings[0]
	require.Equal(t, CheckNonStandardRowKey, f.Check)
	require.Equal(t, "2-1", f.Key)
	require.Contains(t, f.Message, "reconcile-epic-status.py",
		"the message names the other implementation, because that is why it matters now")
	require.Contains(t, f.Message, "rename the row", "and it names the fix")
	require.Empty(t, findingsWithCheck(report.Findings, CheckRowWithoutStory),
		"the bare key IS why the file lookup failed; saying both would mislead")
}

// TestRunCheck_BareRowKeyNamesItsActualStory is the case the first cut of
// this check got wrong twice, and both times because the fixture's bare row
// was `2-1` — the same string the hardcoded example used, so a generic
// message read as a specific one, and no story file matched it, so the
// second finding never fired.
//
// With `7-3` and `7-3_payment-retry.md` on disk, both faults appear: the
// message must name THIS rename and not some other row's, and the story file
// must not also be reported as having no tracker row when it plainly has one.
func TestRunCheck_BareRowKeyNamesItsActualStory(t *testing.T) {
	cfg, write := newCheckFixture(t)
	// epic-7 reads `done` for the same reason as the fixture above: the
	// bare row projects `done`, so `backlog` here would add a second,
	// unrelated finding and weaken the one-problem-one-finding claim.
	write("sprint-status.yaml",
		"development_status:\n  epic-7: done\n  7-3: done\n  epic-7-retrospective: optional\n")
	storyFile(t, write, "7-3_payment-retry", "7.3", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Len(t, report.Findings, 1,
		"one misnamed row is one problem, not two: %+v", report.Findings)

	f := report.Findings[0]
	require.Equal(t, CheckNonStandardRowKey, f.Check)
	require.Contains(t, f.Message, "rename the row `7-3` to `7-3_payment-retry`",
		"the fix names the pair this run already identified")
	require.NotContains(t, f.Message, "2-1", "no other project's row may appear in this message")
	require.NotContains(t, f.Message, "N-M",
		"the generic form is for callers that cannot know the answer; this one can")

	// The finding stands in for the two it suppressed, so it carries both sides.
	require.Equal(t, "7.3", f.StoryID)
	require.Equal(t, "7-3_payment-retry.md", f.Path)
	require.Equal(t, "done", f.Tracker)
	require.Equal(t, "done", f.Story)
	require.Equal(t, []string{"7-3_payment-retry"}, f.Candidates,
		"one candidate populates the same field two do, so a consumer reads one place")
	require.Empty(t, findingsWithCheck(report.Findings, CheckStoryWithoutRow),
		"the story HAS a row — it is just misnamed. Telling the reader to add one is "+
			"the same misdirection as telling them to create the missing file.")
}

// TestRunCheck_BareRowKeyWithNoCandidateReportsTheSearch: nothing on disk
// claims the row. The honest sentence states THAT — the fact this run
// established — rather than the generic advice, and above all it must not end
// by pointing at `ape sprint check`, which is the command printing it. A
// reader told to run the command they are already reading the output of gets
// the same answer twice.
func TestRunCheck_BareRowKeyWithNoCandidateReportsTheSearch(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  epic-7: backlog\n  7-3: done\n  epic-7-retrospective: optional\n")
	storyFile(t, write, "8-1_unrelated", "8.1", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	nonstandard := findingsWithCheck(report.Findings, CheckNonStandardRowKey)
	require.Len(t, nonstandard, 1)

	msg := nonstandard[0].Message
	require.Contains(t, msg, "no story file matches this row")
	require.Contains(t, msg, "the row is stale", "the second real option is named too")
	require.NotContains(t, msg, "ape sprint check",
		"this IS sprint check; it already looked and found nothing")
	require.Empty(t, nonstandard[0].Candidates)

	require.Len(t, findingsWithCheck(report.Findings, CheckStoryWithoutRow), 1,
		"8-1_unrelated genuinely has no row, and nothing suppressed that")
}

// TestRunCheck_BareRowKeyWithTwoCandidatesPicksNeither: two files could be
// this row. The rename target is a judgment, so the message names both and
// nothing is suppressed — each file really does lack a row of its own name.
func TestRunCheck_BareRowKeyWithTwoCandidatesPicksNeither(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  epic-7: backlog\n  7-3: done\n  epic-7-retrospective: optional\n")
	storyFile(t, write, "7-3_payment-retry", "7.3", "done")
	storyFile(t, write, "7-3_payment-refund", "7.3", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	nonstandard := findingsWithCheck(report.Findings, CheckNonStandardRowKey)
	require.Len(t, nonstandard, 1)
	require.Contains(t, nonstandard[0].Message, "7-3_payment-refund")
	require.Contains(t, nonstandard[0].Message, "7-3_payment-retry")
	require.Empty(t, nonstandard[0].Path, "no single owner was claimed")
	// Not deciding which file the row meant is a deliberate refusal. Making
	// the decider parse prose to learn what the options were is not.
	require.Equal(t, []string{"7-3_payment-refund", "7-3_payment-retry"},
		nonstandard[0].Candidates,
		"the options reach a consumer as data, not only as a sentence")
	require.Len(t, findingsWithCheck(report.Findings, CheckStoryWithoutRow), 2,
		"neither file was claimed, and both genuinely lack a row of their own name")
}

func TestMatchesBareRowKey(t *testing.T) {
	require.True(t, MatchesBareRowKey("7-3", "7-3_payment-retry"))
	require.True(t, MatchesBareRowKey("7-3", "7-3-payment-retry"))
	require.False(t, MatchesBareRowKey("1-1", "1-10_ten"),
		"a prefix test alone would claim epic 1 story 10 as a candidate for story 1")
	require.False(t, MatchesBareRowKey("7-3", "7-3"), "identical is not a rename")
	require.False(t, MatchesBareRowKey("7-3", "8-3_other"))
}

func TestRemediationFor(t *testing.T) {
	none := RemediationFor("7-3", nil)
	require.Contains(t, none, "no story file matches this row")
	require.Contains(t, none, "7-3", "it names the row it searched for")
	require.NotEqual(t, BareRowKeyRemediation, none,
		"RemediationFor is for a caller that HAS looked; the constant is for the one that has not")
	require.NotContains(t, none, "ape sprint check",
		"a caller that already searched must not defer to the command doing the searching")

	require.Contains(t, RemediationFor("7-3", []string{"7-3_payment-retry"}),
		"rename the row `7-3` to `7-3_payment-retry`")

	two := RemediationFor("7-3", []string{"7-3_a", "7-3_b"})
	require.Contains(t, two, "2 story files could be this row")
	require.Contains(t, two, "give the others a row of their own")
}

// TestRunCheck_CanonicalKeyWithNoFileIsStillRowWithoutStory keeps the two
// findings distinct: a well-formed key that names a missing file is a
// genuinely different problem from a malformed key.
func TestRunCheck_CanonicalKeyWithNoFileIsStillRowWithoutStory(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  2-1_refuse-a-long-name: done\n  epic-2-retrospective: optional\n")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Len(t, report.Findings, 1)
	require.Equal(t, CheckRowWithoutStory, report.Findings[0].Check)
}

func TestIsBareStoryRowKey(t *testing.T) {
	for _, key := range []string{"2-1", "12-34"} {
		require.True(t, IsBareStoryRowKey(key), "%q is bare", key)
	}
	for _, key := range []string{
		"2-1_slug", "2-1-slug", "epic-2", "epic-2-retrospective", "not-a-story", "2",
	} {
		require.False(t, IsBareStoryRowKey(key), "%q is not a bare STORY row key", key)
	}
}

func findingsWithCheck(findings []Finding, check string) []Finding {
	var out []Finding
	for i := range findings {
		if findings[i].Check == check {
			out = append(out, findings[i])
		}
	}
	return out
}

func TestRunCheck_MalformedTrackerIsOneFindingNotAnError(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml", "development_status: [oops\n")

	report, err := RunCheck(cfg)
	require.NoError(t, err, "the command still exits 0")
	require.Len(t, report.Findings, 1)
	require.Equal(t, CheckTrackerUnreadable, report.Findings[0].Check)
}

func TestRunCheck_MissingTrackerIsClean(t *testing.T) {
	cfg, _ := newCheckFixture(t)
	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.True(t, report.OK())
}

// --- sprint.epic_without_retro ---

// TestRunCheck_EpicWithoutRetro is the class a framework migration's check
// needs. A count comparison cannot answer it: two retros on one epic and
// none on another satisfies `retrospective_rows >= epic_rows` while the
// thing being checked is false — and a migration that records a false
// "applied" in its ledger never looks again.
func TestRunCheck_EpicWithoutRetro(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml", `development_status:
  epic-1: done
  1-1_greet-a-name: done
  epic-1-retrospective: done
  epic-2: done
  2-1_refuse-a-long-name: done
`)
	storyFile(t, write, "1-1_greet-a-name", "1.1", "done")
	storyFile(t, write, "2-1_refuse-a-long-name", "2.1", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	found := findingsOf(report, CheckEpicWithoutRetro)
	require.Len(t, found, 1, "epic 1 has one, epic 2 has none")
	require.Equal(t, "epic-2", found[0].Key)
	require.Contains(t, found[0].Message, "epic-2-retrospective")
}

// TestRunCheck_EpicWithoutRetro_CountsCannotAnswerIt is the exact shape the
// count comparison passes and this class does not.
func TestRunCheck_EpicWithoutRetro_CountsCannotAnswerIt(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml", `development_status:
  epic-1: done
  1-1_greet-a-name: done
  epic-1-retrospective: done
  epic-1-retrospective-followup: done
  epic-2: done
  2-1_refuse-a-long-name: done
`)
	storyFile(t, write, "1-1_greet-a-name", "1.1", "done")
	storyFile(t, write, "2-1_refuse-a-long-name", "2.1", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.GreaterOrEqual(t, report.Summary.RetroRows, report.Summary.EpicRows,
		"the count comparison a migration would otherwise have used passes here")
	require.Len(t, findingsOf(report, CheckEpicWithoutRetro), 1,
		"and epic 2 still has no retrospective row")
}

// TestRunCheck_EpicWithoutRetro_StoryRowsAloneDefineTheEpic: an epic
// mid-mint, with story rows but no `epic-N` header, is still asked.
func TestRunCheck_EpicWithoutRetro_StoryRowsAloneDefineTheEpic(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml", "development_status:\n  3-1_a: done\n")
	storyFile(t, write, "3-1_a", "3.1", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Len(t, findingsOf(report, CheckEpicWithoutRetro), 1)
}

// TestRunCheck_EpicWithoutRetro_UnattributableRetroCreditsNobody: a retro
// row whose key names no epic is not silently credited to whichever epic
// happens to be missing one.
func TestRunCheck_EpicWithoutRetro_UnattributableRetroCreditsNobody(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml", `development_status:
  epic-4: done
  4-1_a: done
  project-retrospective: done
`)
	storyFile(t, write, "4-1_a", "4.1", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Equal(t, 1, report.Summary.RetroRows, "it is still classified as a retro row")
	require.Len(t, findingsOf(report, CheckEpicWithoutRetro), 1,
		"but it names no epic, so it discharges none")
}

func TestRetroRowEpic(t *testing.T) {
	require.Equal(t, 3, retroRowEpic("epic-3-retrospective"))
	require.Equal(t, 12, retroRowEpic("epic-12-retrospective"))
	require.Equal(t, 3, retroRowEpic("epic-3_retrospective"))
	require.Equal(t, 0, retroRowEpic("project-retrospective"))
	require.Equal(t, 0, retroRowEpic("epic-3-retrospective-followup"))
	require.Equal(t, 0, retroRowEpic("epic-3"))
}

// --- sprint.epic_projection_divergence -------------------------------
//
// The class is a comparison, not a projection: it calls the same Project
// that `ape sprint reconcile` writes from. So these tests are about WHEN
// it speaks and what it says, not about the projection rule — that is
// already covered by the Project and reconcile tests, and duplicating it
// here would create a second place to update when the rule moves.

// TestEpicProjection_ReportsADivergentEpicRow is the base case: the row
// says one thing, the story rows project another.
func TestEpicProjection_ReportsADivergentEpicRow(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  epic-1: in-progress\n"+
			"  1-1_a: done\n  1-2_b: done\n  epic-1-retrospective: optional\n")
	storyFile(t, write, "1-1_a", "1.1", "done")
	storyFile(t, write, "1-2_b", "1.2", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)

	found := findingsWithCheck(report.Findings, CheckEpicProjectionDivergence)
	require.Len(t, found, 1)
	require.Equal(t, "epic-1", found[0].Key)
	require.Equal(t, "in-progress", found[0].Tracker, "the asserted value")
	require.Equal(t, "done", found[0].Projected, "the computed value")

	// The remediation is a command and a skill, never a hand edit — the
	// tracker's own header says epic rows are derived, and a finding that
	// suggested otherwise would contradict the file it is printed about.
	require.Contains(t, found[0].Message, "ape sprint reconcile")
	require.Contains(t, found[0].Message, "apex-sprint-sync")
	require.Contains(t, found[0].Message, "Never hand-edit an epic row.")
}

// TestEpicProjection_AgreementIsSilent — the class must not fire on a
// healthy project, which is the property every check here lives or dies
// by. A checker that cries wolf is one an operator learns to ignore.
func TestEpicProjection_AgreementIsSilent(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  epic-1: done\n  1-1_a: done\n  epic-1-retrospective: optional\n")
	storyFile(t, write, "1-1_a", "1.1", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Empty(t, findingsWithCheck(report.Findings, CheckEpicProjectionDivergence))
}

// TestEpicProjection_ContextedIsALegacySpellingOfInProgress covers the one
// value that needed a mapping, and the reason it got its own normaliser.
func TestEpicProjection_ContextedIsALegacySpellingOfInProgress(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  epic-1: contexted\n"+
			"  1-1_a: done\n  1-2_b: backlog\n  epic-1-retrospective: optional\n")
	storyFile(t, write, "1-1_a", "1.1", "done")
	storyFile(t, write, "1-2_b", "1.2", "backlog")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Empty(t, findingsWithCheck(report.Findings, CheckEpicProjectionDivergence),
		"`contexted` is the legacy epic spelling of in-progress, which is what "+
			"one done and one backlog story project — reporting it would fire on "+
			"every tracker written before the rename")
}

// TestEpicProjection_ContextedDoesNotMoveStatusDivergence is the other
// half of that decision, and the one worth a test of its own.
//
// The mapping lives in NormalizeEpicStatus and NOT in NormalizeStatus,
// which sprint.status_divergence reads on every story row of every
// project. Folding it in would have been one line and would have changed
// a shipped class's output as a side effect of adding a different one. A
// story row written `contexted` must still diverge from an `in-progress`
// story file.
func TestEpicProjection_ContextedDoesNotMoveStatusDivergence(t *testing.T) {
	require.Equal(t, StatusInProgress, NormalizeEpicStatus("contexted"))
	require.Equal(t, "contexted", NormalizeStatus("contexted"),
		"the story vocabulary is untouched; only the epic row maps this value")

	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  epic-1: in-progress\n  1-1_a: contexted\n"+
			"  epic-1-retrospective: optional\n")
	storyFile(t, write, "1-1_a", "1.1", "in-progress")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Len(t, findingsWithCheck(report.Findings, CheckStatusDivergence), 1,
		"a story row reading `contexted` still diverges from an in-progress story file")
}

// TestEpicProjection_NoProjectionMeansNoFinding covers both silences.
//
// An epic with no story rows and an epic whose every row is cancelled
// have NO projection — Project returns "" for each — so there is nothing
// for the asserted value to disagree with. Reporting them would fire on
// every epic mid-mint and on every descoped epic, which are decisions
// rather than drift.
func TestEpicProjection_NoProjectionMeansNoFinding(t *testing.T) {
	t.Run("no story rows", func(t *testing.T) {
		cfg, write := newCheckFixture(t)
		write("sprint-status.yaml",
			"development_status:\n  epic-9: done\n  epic-9-retrospective: optional\n")

		report, err := RunCheck(cfg)
		require.NoError(t, err)
		require.Empty(t, findingsWithCheck(report.Findings, CheckEpicProjectionDivergence))
	})

	t.Run("every story row cancelled", func(t *testing.T) {
		cfg, write := newCheckFixture(t)
		write("sprint-status.yaml",
			"development_status:\n  epic-9: backlog\n  9-1_a: cancelled\n"+
				"  epic-9-retrospective: optional\n")
		storyFile(t, write, "9-1_a", "9.1", "cancelled")

		report, err := RunCheck(cfg)
		require.NoError(t, err)
		require.Empty(t, findingsWithCheck(report.Findings, CheckEpicProjectionDivergence),
			"all-cancelled is a scope decision, and the projection declines to close on it")
	})
}

// TestEpicProjection_NoEpicRowAssertsNothing: story rows but no `epic-N`
// header. Nothing was asserted, so nothing can diverge — and inventing a
// divergence against a value nobody wrote would send a reader to correct
// a row that does not exist. The absent header is a different finding
// with a different fix.
func TestEpicProjection_NoEpicRowAssertsNothing(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  1-1_a: done\n  epic-1-retrospective: optional\n")
	storyFile(t, write, "1-1_a", "1.1", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Empty(t, findingsWithCheck(report.Findings, CheckEpicProjectionDivergence))
}

// TestRunCheck_BareRowCountsTowardTheProjection is the interaction two
// other fixtures in this file were carrying silently until this class
// existed, which is why it now has a test of its own.
//
// ape counts a bare `N-M` row toward its epic and the
// reconcile-epic-status.py it replaces does not. So a tracker with a bare
// row can carry TWO genuine findings: the misnamed row, and an epic row
// that is stale by ape's counting. They are not one problem wearing two
// hats — renaming the row leaves the projection exactly where it was, so
// the epic row is still wrong afterwards.
func TestRunCheck_BareRowCountsTowardTheProjection(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  epic-7: backlog\n  7-3: done\n  epic-7-retrospective: optional\n")
	storyFile(t, write, "7-3_payment-retry", "7.3", "done")

	report, err := RunCheck(cfg)
	require.NoError(t, err)

	require.Len(t, findingsWithCheck(report.Findings, CheckNonStandardRowKey), 1)
	drift := findingsWithCheck(report.Findings, CheckEpicProjectionDivergence)
	require.Len(t, drift, 1,
		"the bare row is counted, so the epic projects done against an asserted backlog")
	require.Equal(t, "backlog", drift[0].Tracker)
	require.Equal(t, "done", drift[0].Projected)
}

// TestEpicProjection_UnrecognisedStatusIsNamedNotSwallowed — an
// unrecognised story status can only ever hold an epic open, so it may be
// the whole cause of a divergence. A reader who cannot see it would read
// the projection as wrong instead of the row.
func TestEpicProjection_UnrecognisedStatusIsNamedNotSwallowed(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  epic-1: done\n  1-1_a: marinating\n"+
			"  epic-1-retrospective: optional\n")
	storyFile(t, write, "1-1_a", "1.1", "marinating")

	report, err := RunCheck(cfg)
	require.NoError(t, err)

	found := findingsWithCheck(report.Findings, CheckEpicProjectionDivergence)
	require.Len(t, found, 1)
	require.Equal(t, "in-progress", found[0].Projected)
	require.Contains(t, found[0].Message, "marinating",
		"the status that held the epic open is named in the finding")
}

// --- brownfield stories: a tracker-claimed file with no frontmatter ------

// TestRunCheck_BrownfieldStoryIsOnDisk — a story with no frontmatter whose
// file name a tracker row claims is a story, and its body `Status:` line is
// its status. RunCheck used to see only frontmatter `story_id`, so it
// reported "tracker row has no <key>.md" with the file right there.
func TestRunCheck_BrownfieldStoryIsOnDisk(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml",
		"development_status:\n  1-1_legacy: in-progress\n  1-2_drifted: done\n  epic-1-retrospective: optional\n")
	write("1-1_legacy.md", "# Legacy import\n\nStatus: in-progress\n\nLifted from the old tracker.\n")
	write("1-2_drifted.md", "# Drifted\n\n**Status:** review\n")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Empty(t, findingsOf(report, CheckRowWithoutStory), "both files are on disk: %+v", report.Findings)
	require.Equal(t, 2, report.Summary.StoriesOnDisk)

	div := findingsOf(report, CheckStatusDivergence)
	require.Len(t, div, 1, "only the drifted story disagrees with its row")
	require.Equal(t, "1-2_drifted", div[0].Key)
	require.Equal(t, "done", div[0].Tracker)
	require.Equal(t, "review", div[0].Story, "the body line is the story's side of the comparison")
}

// A claimed file that states no status anywhere is still counted with the
// missing rows, as the framework's cross-check does — but the message says
// what is actually wrong, not that the file does not exist.
func TestRunCheck_BrownfieldStoryWithNoStatusSaysSo(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml", "development_status:\n  1-1_bare: backlog\n  epic-1-retrospective: optional\n")
	write("1-1_bare.md", "# Bare\n\nNo status line at all.\n\n```md\nStatus: done\n```\n")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	rows := findingsOf(report, CheckRowWithoutStory)
	require.Len(t, rows, 1)
	require.Equal(t, "1-1_bare", rows[0].Key)
	require.Contains(t, rows[0].Message, "is on disk but states no status",
		"a fenced `Status:` example is not the story's own status")
	require.Equal(t, "1-1_bare.md", rows[0].Path)
}

// The second branch is bounded by the tracker. A markdown file nobody's row
// names — a README, a retrospective — is still not a story, so it produces
// no story_without_row.
func TestRunCheck_UnclaimedFileWithoutFrontmatterIsNotAStory(t *testing.T) {
	cfg, write := newCheckFixture(t)
	write("sprint-status.yaml", "development_status:\n  epic-1-retrospective: optional\n")
	write("README.md", "# Notes\n\nStatus: draft\n")

	report, err := RunCheck(cfg)
	require.NoError(t, err)
	require.Empty(t, findingsOf(report, CheckStoryWithoutRow), "%+v", report.Findings)
	require.Zero(t, report.Summary.StoriesOnDisk)
}
