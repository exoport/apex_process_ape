package deferred

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "deferred"))
}

// realBullet is a defer bullet in the exact shape apex-review-story
// emits: the checkbox, the [Defer] marker, a citation bracket, backticks
// in the title, and the em-dash `— defer:` tail carrying five fields.
const realBullet = "- [ ] [Defer] Tighten `resolveModelArg` fallback when `--model` is empty " +
	"[internal/apecmd/modelarg.go:42] — defer: outside-story=yes ; cross-cycle=no ; " +
	"non-blocking=yes ; owner=platform ; trigger: story 54-2 lands\n"

// --- Ingest ---

// TestIngest_BodyIsByteIntact is the whole point of stdin-not-argv: 108
// of 109 real bodies contain backticks, which shell-expand inside an
// argument, and the em-dash tail plus the `- [ ]` checkbox must survive
// verbatim. The current single-file append path strips that checkbox in
// 100% of the 109 live records.
func TestIngest_BodyIsByteIntact(t *testing.T) {
	s := newStore(t)
	res, err := s.Ingest([]byte(realBullet), IngestOptions{
		Story: "54-1", Skill: "apex-review-story", Cycle: 2, Date: "2026-08-22",
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.Count)

	rec := res.Records[0]
	require.Equal(t, realBullet, rec.Body, "the body is byte-identical to what was piped in")
	require.Contains(t, rec.Body, "`resolveModelArg`", "backticks survive")
	require.Contains(t, rec.Body, "— defer:", "the em-dash tail survives")
	require.Contains(t, rec.Body, "- [ ]", "the checkbox survives")

	// And re-reading from disk gives the same bytes back.
	reread, err := ReadRecord(rec.Path)
	require.NoError(t, err)
	require.Equal(t, realBullet, reread.Body)
}

func TestIngest_ParsesEveryTailField(t *testing.T) {
	s := newStore(t)
	res, err := s.Ingest([]byte(realBullet), IngestOptions{
		Story: "54-1", Skill: "apex-review-story", Cycle: 2, Date: "2026-08-22",
	})
	require.NoError(t, err)
	rec := res.Records[0]

	require.Equal(t, "Tighten `resolveModelArg` fallback when `--model` is empty", rec.Title)
	require.Equal(t, "yes", rec.OutsideStory)
	require.Equal(t, "no", rec.CrossCycle)
	require.Equal(t, "yes", rec.NonBlocking)
	require.Equal(t, "platform", rec.Owner)
	require.Equal(t, "story 54-2 lands", rec.Trigger)
	require.Equal(t, []string{"internal/apecmd/modelarg.go:42"}, rec.Anchors)
	require.Equal(t, "54-1", rec.SourceStory)
	require.Equal(t, "apex-review-story", rec.Skill)
	require.Equal(t, SourceStoryReview, rec.Source)
	require.Equal(t, 2, rec.Cycle)
	require.Equal(t, "2026-08-22", rec.Created)
	require.Equal(t, StatusOpen, rec.Status)
	require.False(t, rec.FreeForm)
}

// TestIngest_EmptyStdinIsANoOp: a review that produced no defer must not
// fail. A non-zero exit here converts a defer into a patch and demotes the
// story.
func TestIngest_EmptyStdinIsANoOp(t *testing.T) {
	s := newStore(t)
	for _, input := range []string{"", "\n\n", "   \n"} {
		res, err := s.Ingest([]byte(input), IngestOptions{Date: "2026-08-22"})
		require.NoError(t, err, "input %q", input)
		require.Zero(t, res.Count)
	}
}

// TestIngest_MalformedLineIsStoredVerbatim is the C1 contract: a bullet
// whose shape is unrecognised is kept, warned about, and never a failure.
func TestIngest_MalformedLineIsStoredVerbatim(t *testing.T) {
	s := newStore(t)
	input := "this is not even a bullet, just prose someone pasted\n"
	res, err := s.Ingest([]byte(input), IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err, "a content problem must never fail this command")
	require.Equal(t, 1, res.Count)
	require.True(t, res.Records[0].FreeForm)
	require.Equal(t, input, res.Records[0].Body, "kept verbatim")
	require.Equal(t, "this is not even a bullet, just prose someone pasted", res.Records[0].Title)
	require.Len(t, res.Warnings, 1)
	require.Contains(t, res.Warnings[0], "free-form")
}

func TestIngest_MultipleBulletsWithContinuations(t *testing.T) {
	s := newStore(t)
	input := "- [Defer] First thing [a.go:1] — defer: owner=alice\n" +
		"  continuation line for the first\n" +
		"\n" +
		"  ```go\n  code inside the first\n  ```\n" +
		"- [ ] [Defer] Second thing [b.go:2] — defer: owner=bob\n"

	res, err := s.Ingest([]byte(input), IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)
	require.Equal(t, 2, res.Count)
	require.Equal(t, "First thing", res.Records[0].Title)
	require.Contains(t, res.Records[0].Body, "code inside the first",
		"indented continuation lines stay with their bullet")
	require.Equal(t, "alice", res.Records[0].Owner)
	require.Equal(t, "Second thing", res.Records[1].Title)
	require.Equal(t, "bob", res.Records[1].Owner)
}

// TestIngest_IdenticalBulletsBothStored: the operator wrote two, so two
// are stored — a content-addressed id must not silently collapse them.
func TestIngest_IdenticalBulletsBothStored(t *testing.T) {
	s := newStore(t)
	bullet := "- [Defer] Same text [a.go:1]\n"
	res, err := s.Ingest([]byte(bullet+bullet), IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)
	require.Equal(t, 2, res.Count)
	require.NotEqual(t, res.Records[0].ID, res.Records[1].ID)

	loaded, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, loaded.Records, 2)
}

// TestIngest_SameBulletSameDayIsIdempotent: re-running an ingest must not
// duplicate the record.
func TestIngest_SameBulletSameDayIsIdempotent(t *testing.T) {
	s := newStore(t)
	opts := IngestOptions{Story: "1-1", Date: "2026-08-22"}
	first, err := s.Ingest([]byte(realBullet), opts)
	require.NoError(t, err)
	second, err := s.Ingest([]byte(realBullet), opts)
	require.NoError(t, err)
	require.Equal(t, first.Records[0].ID, second.Records[0].ID)

	loaded, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, loaded.Records, 1, "the same bullet ingested twice is one record")
}

func TestIngest_UnwritableStoreIsTheOnlyFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	base := t.TempDir()
	require.NoError(t, os.Chmod(base, 0o500))
	t.Cleanup(func() { _ = os.Chmod(base, 0o700) })

	s := New(filepath.Join(base, "deferred"))
	_, err := s.Ingest([]byte(realBullet), IngestOptions{Date: "2026-08-22"})
	require.Error(t, err, "a broken environment IS allowed to fail — that is C2's distinction")
}

func TestSplitBullets(t *testing.T) {
	input := "preamble prose\n- first\n  cont\n- second\n"
	chunks := SplitBullets([]byte(input))
	require.Len(t, chunks, 3, "the preamble is its own chunk rather than being dropped")
	require.Contains(t, chunks[0], "preamble")
	require.Contains(t, chunks[1], "first")
	require.Contains(t, chunks[1], "cont")
	require.Contains(t, chunks[2], "second")
}

func TestSplitBullets_DeeplyIndentedBulletStaysWithParent(t *testing.T) {
	chunks := SplitBullets([]byte("- parent\n    - child\n- sibling\n"))
	require.Len(t, chunks, 2)
	require.Contains(t, chunks[0], "child")
}

// --- Load / degradation ---

// TestLoad_OneMalformedRecordLosesOneRecord is the headline degradation
// test, and the reason for one-file-per-record: the single-store
// predecessor returned ZERO records when handed one bad entry.
func TestLoad_OneMalformedRecordLosesOneRecord(t *testing.T) {
	s := newStore(t)
	_, err := s.Ingest([]byte("- [Defer] Good one [a.go:1]\n- [Defer] Another good [b.go:2]\n"),
		IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(s.Dir, "DW-broken_x.md"),
		[]byte("this file has no frontmatter at all\n"), 0o644))

	res, err := s.Load(LoadOptions{})
	require.NoError(t, err, "one bad record must not fail the read")
	require.Len(t, res.Records, 2, "the other two are returned")
	require.Len(t, res.Warnings, 1)
	require.Contains(t, res.Warnings[0], "DW-broken_x.md")
}

func TestLoad_AbsentStoreIsEmpty(t *testing.T) {
	res, err := New(filepath.Join(t.TempDir(), "nope")).Load(LoadOptions{})
	require.NoError(t, err)
	require.Empty(t, res.Records)
}

func TestLoad_SkipsNonRecords(t *testing.T) {
	s := newStore(t)
	_, err := s.Ingest([]byte("- [Defer] Real [a.go:1]\n"), IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(s.Dir, "README.md"), []byte("# Deferred\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(s.Dir, IndexFileName), []byte("records: []\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(s.Dir, ".gitignore"), []byte("index.yaml\n"), 0o644))

	res, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Records, 1)
	require.Empty(t, res.Warnings, "a README and an index are not malformed records")
}

// --- Close ---

func TestClose_MovesToClosedAndNeverDeletes(t *testing.T) {
	s := newStore(t)
	res, err := s.Ingest([]byte(realBullet), IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)
	id := res.Records[0].ID
	openPath := res.Records[0].Path

	closed, err := s.Close(id, "54-2", "2026-08-23")
	require.NoError(t, err)
	require.Equal(t, StatusClosed, closed.Status)
	require.Equal(t, "54-2", closed.ResolvedBy)
	require.Equal(t, "2026-08-23", closed.ResolvedAt)
	require.Contains(t, closed.Body, "- [x] [Defer] resolved: 54-2 (2026-08-23)",
		"the discharge marker STAYS — an LLM that cannot see a closed defer re-files it")
	require.Contains(t, closed.Body, "— defer:", "the original body survives underneath")

	_, err = os.Stat(openPath)
	require.Error(t, err, "the record left the open set")
	_, err = os.Stat(filepath.Join(s.ClosedDir(), closed.FileName()))
	require.NoError(t, err, "and landed in closed/, not the bin")
}

func TestClose_IsIdempotent(t *testing.T) {
	s := newStore(t)
	res, err := s.Ingest([]byte(realBullet), IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)
	id := res.Records[0].ID

	first, err := s.Close(id, "54-2", "2026-08-23")
	require.NoError(t, err)
	second, err := s.Close(id, "54-9", "2026-08-24")
	require.NoError(t, err)

	require.Equal(t, first.Body, second.Body, "no second marker is appended")
	require.Equal(t, "54-2", second.ResolvedBy, "the first resolution stands")
	require.Equal(t, 1, strings.Count(second.Body, dischargeMarkerPrefix))
}

func TestClose_UnknownID(t *testing.T) {
	s := newStore(t)
	_, err := s.Close("DW-nope", "x", "2026-08-23")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestLoad_ClosedInvisibleByDefault(t *testing.T) {
	s := newStore(t)
	res, err := s.Ingest([]byte("- [Defer] A [a.go:1]\n- [Defer] B [b.go:2]\n"),
		IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)
	_, err = s.Close(res.Records[0].ID, "1-1", "2026-08-23")
	require.NoError(t, err)

	open, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, open.Records, 1, "closed records leave the working set")

	all, err := s.Load(LoadOptions{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, all.Records, 2, "but stay on disk")
}

// --- Select ---

func TestSelect_Filters(t *testing.T) {
	s := newStore(t)
	_, err := s.Ingest([]byte(
		"- [Defer] Alpha [pkg/a/x.go:1] — defer: owner=alice\n"+
			"- [Defer] Beta [pkg/b/y.go:2] — defer: owner=bob\n",
	),
		IngestOptions{Story: "1-1", Date: "2026-08-22"})
	require.NoError(t, err)
	_, err = s.Ingest([]byte("- [Defer] Gamma [pkg/a/z.go:3] — defer: owner=alice\n"),
		IngestOptions{Story: "2-2", Date: "2026-08-22"})
	require.NoError(t, err)

	all, err := s.Load(LoadOptions{IncludeClosed: true})
	require.NoError(t, err)

	require.Len(t, Select(all.Records, Filter{}), 3, "empty filter means open")
	require.Len(t, Select(all.Records, Filter{Owner: "alice"}), 2)
	require.Len(t, Select(all.Records, Filter{Owner: "ALICE"}), 2, "owner match is case-insensitive")
	require.Len(t, Select(all.Records, Filter{Story: "2-2"}), 1)
	require.Len(t, Select(all.Records, Filter{Path: "pkg/a/"}), 2)
	require.Empty(t, Select(all.Records, Filter{Path: "pkg/z/"}))
	require.Len(t, Select(all.Records, Filter{Status: "all"}), 3)
	require.Empty(t, Select(all.Records, Filter{Status: StatusClosed}))
}

func TestSelect_Group(t *testing.T) {
	rec := Record{ID: "DW-1", Status: StatusOpen, Group: "auth"}
	require.Len(t, Select([]Record{rec}, Filter{Group: "auth"}), 1)
	require.Empty(t, Select([]Record{rec}, Filter{Group: "other"}))
}

// --- Verify ---

func TestVerify_CleanStore(t *testing.T) {
	s := newStore(t)
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "internal", "apecmd"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "internal", "apecmd", "modelarg.go"), []byte("package apecmd\n"), 0o644,
	))

	_, err := s.Ingest([]byte(realBullet), IngestOptions{Story: "54-1", Date: "2026-08-22"})
	require.NoError(t, err)

	report, err := s.Verify(VerifyOptions{ProjectRoot: root})
	require.NoError(t, err)
	require.True(t, report.OK(), "%+v", report.Findings)
	require.Equal(t, 1, report.Summary.Open)
}

func TestVerify_DanglingRefIsCertain(t *testing.T) {
	s := newStore(t)
	_, err := s.Write(Record{
		ID: "DW-1", Title: "A", Status: StatusOpen,
		Related: []string{"DW-999"}, Supersedes: []string{"DW-888"},
		Body: "x\n",
	})
	require.NoError(t, err)

	report, err := s.Verify(VerifyOptions{})
	require.NoError(t, err)
	require.Len(t, report.Findings, 2)
	for _, f := range report.Findings {
		require.Equal(t, CheckDanglingRef, f.Check)
		require.Equal(t, ConfidenceCertain, f.Confidence, "a pointer to nowhere is a fact, not a guess")
	}
}

func TestVerify_DeadAnchorIsACandidate(t *testing.T) {
	s := newStore(t)
	_, err := s.Ingest([]byte("- [Defer] Gone [pkg/removed.go:12]\n"), IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)

	report, err := s.Verify(VerifyOptions{ProjectRoot: t.TempDir()})
	require.NoError(t, err)
	require.Len(t, report.Findings, 1)
	require.Equal(t, CheckDeadAnchor, report.Findings[0].Check)
	require.Equal(t, ConfidenceCandidate, report.Findings[0].Confidence,
		"the code may simply have moved — this is never auto-actionable")
	require.Equal(t, 1, report.Summary.Candidates)
}

func TestVerify_TriggerFiredIsACandidate(t *testing.T) {
	s := newStore(t)
	_, err := s.Ingest([]byte("- [Defer] Wait for it [a.go:1] — defer: trigger: story 54-2 lands\n"),
		IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)

	report, err := s.Verify(VerifyOptions{DoneStories: map[string]bool{"54-2": true}})
	require.NoError(t, err)
	fired := 0
	for _, f := range report.Findings {
		if f.Check == CheckTriggerFired {
			fired++
			require.Equal(t, ConfidenceCandidate, f.Confidence)
			require.Contains(t, f.Message, "verify against HEAD before closing")
		}
	}
	require.Equal(t, 1, fired)
}

func TestVerify_DuplicateCandidate(t *testing.T) {
	s := newStore(t)
	_, err := s.Write(Record{ID: "DW-1", Title: "Fix the thing", Status: StatusOpen, Body: "a\n"})
	require.NoError(t, err)
	_, err = s.Write(Record{ID: "DW-2", Title: "Fix  the  THING!", Status: StatusOpen, Body: "b\n"})
	require.NoError(t, err)

	report, err := s.Verify(VerifyOptions{})
	require.NoError(t, err)
	dupes := 0
	for _, f := range report.Findings {
		if f.Check == CheckDuplicate {
			dupes++
			require.Equal(t, ConfidenceCandidate, f.Confidence)
		}
	}
	require.Equal(t, 1, dupes)
}

func TestVerify_ClosedRecordsSkipHeuristics(t *testing.T) {
	s := newStore(t)
	res, err := s.Ingest([]byte("- [Defer] Gone [pkg/removed.go:12]\n"), IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)
	_, err = s.Close(res.Records[0].ID, "1-1", "2026-08-23")
	require.NoError(t, err)

	report, err := s.Verify(VerifyOptions{ProjectRoot: t.TempDir()})
	require.NoError(t, err)
	require.True(t, report.OK(), "a closed record's dead anchor is not news")
	require.Equal(t, 1, report.Summary.Closed)
}

func TestVerify_SchemaProblems(t *testing.T) {
	s := newStore(t)
	_, err := s.Write(Record{ID: "DW-1", Title: "", Status: "weird", Body: "x\n"})
	require.NoError(t, err)
	_, err = s.Write(Record{ID: "DW-2", Title: "Closed with no reason", Status: StatusClosed, Body: "x\n"})
	require.NoError(t, err)

	report, err := s.Verify(VerifyOptions{})
	require.NoError(t, err)
	msgs := map[string]bool{}
	for _, f := range report.Findings {
		require.Equal(t, CheckSchema, f.Check)
		msgs[f.Message] = true
	}
	require.Len(t, msgs, 3, "no title, bad status, closed with no reason")
}

func TestVerify_UnparsableRecordIsReported(t *testing.T) {
	s := newStore(t)
	require.NoError(t, os.MkdirAll(s.Dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(s.Dir, "DW-bad_x.md"), []byte("no frontmatter\n"), 0o644))

	report, err := s.Verify(VerifyOptions{})
	require.NoError(t, err)
	require.Len(t, report.Findings, 1)
	require.Equal(t, CheckUnparsable, report.Findings[0].Check)
}

func TestVerify_FreeFormIsFlagged(t *testing.T) {
	s := newStore(t)
	_, err := s.Ingest([]byte("some prose nobody formatted\n"), IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)

	report, err := s.Verify(VerifyOptions{})
	require.NoError(t, err)
	require.Len(t, report.Findings, 1)
	require.Equal(t, CheckFreeForm, report.Findings[0].Check)
	require.Contains(t, report.Findings[0].Message, "ape deferred repair")
}

// --- Ids, slugs, index ---

func TestSlug_IsWindowsSafe(t *testing.T) {
	// Every character here is illegal in a Windows filename.
	slug := Slug(`Fix: "quoted" path\to/thing? <maybe> |pipe| *star*`)
	require.NotContains(t, slug, ":")
	require.NotContains(t, slug, `"`)
	require.NotContains(t, slug, "/")
	require.NotContains(t, slug, `\`)
	require.NotContains(t, slug, "?")
	require.NotContains(t, slug, "*")
	require.NotContains(t, slug, "|")
	require.NotContains(t, slug, "<")
	require.LessOrEqual(t, len(slug), slugMaxLen)
	require.False(t, strings.HasPrefix(slug, "-"))
	require.False(t, strings.HasSuffix(slug, "-"))
}

func TestSlug_Degenerate(t *testing.T) {
	require.Equal(t, "record", Slug(""))
	require.Equal(t, "record", Slug("!!! ???"))
	require.Equal(t, "abc", Slug("  abc  "))
}

func TestNewID_DeterministicAndContentAddressed(t *testing.T) {
	a := NewID("2026-08-22", "title", "body")
	b := NewID("2026-08-22", "title", "body")
	c := NewID("2026-08-22", "title", "different body")
	d := NewID("2026-08-23", "title", "body")
	require.Equal(t, a, b)
	require.NotEqual(t, a, c)
	require.NotEqual(t, a, d, "the date is part of the namespace")
	require.True(t, strings.HasPrefix(a, "DW-20260822-"))
}

func TestRebuildIndex(t *testing.T) {
	s := newStore(t)
	res, err := s.Ingest([]byte("- [Defer] A [a.go:1]\n- [Defer] B [b.go:2]\n"),
		IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)
	_, err = s.Close(res.Records[0].ID, "1-1", "2026-08-23")
	require.NoError(t, err)

	require.NoError(t, s.RebuildIndex())
	body, err := os.ReadFile(filepath.Join(s.Dir, IndexFileName))
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "never a source of truth")
	require.Contains(t, text, res.Records[0].ID)
	require.Contains(t, text, res.Records[1].ID)
	require.Contains(t, text, ClosedDirName+"/", "a closed record's path is relative to the store")

	// And the index is not itself read back as a record.
	loaded, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, loaded.Records, 1)
}

func TestRender_FrontmatterThenVerbatimBody(t *testing.T) {
	rec := Record{
		ID: "DW-1", Title: "T", Status: StatusOpen,
		Body: "- [ ] [Defer] `backticks` — defer: outside-story=yes\n",
	}
	data, err := Render(rec)
	require.NoError(t, err)
	text := string(data)
	require.True(t, strings.HasPrefix(text, "---\n"))
	require.Contains(t, text, "id: DW-1")
	require.True(t, strings.HasSuffix(text, rec.Body), "the body is the tail of the file, verbatim")
	require.NotContains(t, text, "body:", "the body is never serialised into frontmatter")
}

func TestParseBullet_Forms(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantTitle string
		freeForm  bool
	}{
		{"unchecked defer", "- [ ] [Defer] Thing\n", "Thing", false},
		{"bare defer", "- [Defer] Thing\n", "Thing", false},
		{"checked defer", "- [x] [Defer] Thing\n", "Thing", false},
		{"asterisk bullet", "* [Defer] Thing\n", "Thing", false},
		{"plain bullet", "- Just a note\n", "Just a note", true},
		{"prose", "Just prose\n", "Just prose", true},
		{"anchors stripped from title", "- [Defer] Thing [a.go:1]\n", "Thing", false},
		{"tail stripped from title", "- [Defer] Thing — defer: owner=x\n", "Thing", false},
		{"empty", "\n", "(untitled)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := ParseBullet(tc.input)
			require.Equal(t, tc.wantTitle, rec.Title)
			require.Equal(t, tc.freeForm, rec.FreeForm)
			require.Equal(t, tc.input, rec.Body, "the body is always the input verbatim")
		})
	}
}

func TestParseBullet_MultipleAnchorsDeduplicatedAndSorted(t *testing.T) {
	rec := ParseBullet("- [Defer] Thing [b.go:2] [a.go:1] [b.go:2]\n")
	require.Equal(t, []string{"a.go:1", "b.go:2"}, rec.Anchors)
}

func TestSourceForSkill(t *testing.T) {
	require.Equal(t, SourceStoryReview, sourceForSkill("apex-review-story"))
	require.Equal(t, SourceStoryReview, sourceForSkill("apex-code-review"))
	require.Equal(t, SourceCorrectCourse, sourceForSkill("apex-correct-course"))
	require.Equal(t, SourceUnknown, sourceForSkill(""))
	require.Equal(t, "apex-something", sourceForSkill("apex-something"))
}

// TestStore_ScaleOfTheRealCorpus: 109 open records plus 118 tombstones is
// the reference project's shape. The point is that `list` projects the
// open set without loading the corpus.
func TestStore_ScaleOfTheRealCorpus(t *testing.T) {
	s := newStore(t)
	var input strings.Builder
	for i := 1; i <= 109; i++ {
		fmt.Fprintf(&input, "- [ ] [Defer] Item %d [pkg/f%d.go:%d] — defer: owner=team%d\n", i, i, i, i%4)
	}
	_, err := s.Ingest([]byte(input.String()), IngestOptions{Story: "1-1", Date: "2026-08-22"})
	require.NoError(t, err)

	open, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, open.Records, 109)
	require.Len(t, Select(open.Records, Filter{Owner: "team1"}), 28)

	report, err := s.Verify(VerifyOptions{})
	require.NoError(t, err)
	require.True(t, report.OK(), "%+v", report.Findings)
}
