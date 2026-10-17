package deferred

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- Ingest ---

// --- Load / degradation ---

func TestLoad_AbsentStoreIsEmpty(t *testing.T) {
	res, err := New(filepath.Join(t.TempDir(), "nope")).Load(LoadOptions{})
	require.NoError(t, err)
	require.Empty(t, res.Records)
}

// --- Close ---

// --- Select ---

// --- Verify ---

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

func TestClassifySource_HeadingsAndSkills(t *testing.T) {
	// The dev half is a whole word, so the development folder is not a
	// provenance.
	require.Equal(t, SourceStoryDev, classifySource("story dev of 30-4_amend-adr-0003"))
	require.Equal(t, SourceUnknown, classifySource("the development folder"))
	// Review wins a heading that says both.
	require.Equal(t, SourceStoryReview, classifySource("story dev review of 12-1"))
	require.Equal(t, SourceCorrectCourse, classifySource("apex-correct-course reconciliation of epic 12"))
}

func TestApplyClosureMarker_AnchoredNotWordSearch(t *testing.T) {
	// The shape that closes: a list item OPENING with the marker.
	closed := Record{Status: StatusOpen, Body: "- [Defer] a thing\n" +
		"  - **RESOLVED (2026-08-20) — closed by Story 3.17, re-verified.**\n"}
	applyClosureMarker(&closed)
	require.Equal(t, StatusClosed, closed.Status)
	require.Equal(t, "2026-08-20", closed.ResolvedAt)
	require.Contains(t, closed.ResolvedBy, "migrated, not verified")

	// The shape that must NOT close: the register describing its own
	// convention. 12 of 32 `RESOLVED` mentions on the reference ledger are
	// this, on records that are correctly open.
	open := Record{Status: StatusOpen, Body: "- [Defer] a thing, cited 2026-08-01.\n" +
		"  Closed, as with every other entry in this register, by an appended\n" +
		"  dated `RESOLVED` clause, never by deletion.\n"}
	applyClosureMarker(&open)
	require.Equal(t, StatusOpen, open.Status)
	require.Empty(t, open.ResolvedAt)

	// Lowercase prose is not the tag.
	prose := Record{Status: StatusOpen, Body: "- [Defer] the resolved role names differ\n"}
	applyClosureMarker(&prose)
	require.Equal(t, StatusOpen, prose.Status)
}

func TestApplyStatusAnnotation(t *testing.T) {
	closed := Record{Status: StatusOpen, Body: "- [Defer] a thing\n- [Closed: 193c4ec] Story 17.8 built it\n"}
	applyStatusAnnotation(&closed)
	require.Equal(t, StatusClosed, closed.Status)
	require.Contains(t, closed.ResolvedBy, "[Closed: 193c4ec]")

	// Superseded is DISCARDED, not closed: the work was never done, it was
	// overtaken. Recording it as closed asserts a delivery that never
	// happened.
	sup := Record{Status: StatusOpen, Body: "- [Defer] a thing\n- [Superseded: docs/atomix.md:50] overtaken\n"}
	applyStatusAnnotation(&sup)
	require.Equal(t, StatusDiscarded, sup.Status)
	require.Equal(t, "docs/atomix.md:50", sup.DiscardEvidence)
	require.NotEmpty(t, sup.DiscardReason)

	// `[Open]` is the register asserting the entry still reproduces.
	stays := Record{Status: StatusOpen, Body: "- [Defer] a thing\n- [Open]\n"}
	applyStatusAnnotation(&stays)
	require.Equal(t, StatusOpen, stays.Status)

	// THE LAST TAG WINS. The convention is append-only, so a run of tags is
	// a chronology — and a `[Closed]` in the middle of one is routinely a
	// comment about a NEIGHBOURING entry, absorbed along with the rest. On
	// the field ledger, taking the first discharging tag closed four records
	// whose final word is `[Open]`.
	reopened := Record{Status: StatusOpen, Body: "- [Defer] a thing\n" +
		"- [Open] re-verified live: still reproduces\n" +
		"- [Closed] the entry ABOVE, not this one\n" +
		"- [Open] filed open at dev time for the same reason\n"}
	applyStatusAnnotation(&reopened)
	require.Equal(t, StatusOpen, reopened.Status,
		"a trailing [Open] is the register's latest word")
	require.Empty(t, reopened.ResolvedBy)

	// And the reverse order still closes.
	late := Record{Status: StatusOpen, Body: "- [Defer] a thing\n" +
		"- [Open] not yet\n- [Closed: bdffb51] Story 23.2 landed it\n"}
	applyStatusAnnotation(&late)
	require.Equal(t, StatusClosed, late.Status)
	require.Contains(t, late.ResolvedBy, "[Closed: bdffb51]")

	// A record already discharged by an appended closure marker is not
	// resurrected by a trailing [Open].
	alreadyClosed := Record{
		Status: StatusClosed, ResolvedBy: "story 3.17",
		Body: "- [Defer] a thing\n- [Open] stale annotation\n",
	}
	applyStatusAnnotation(&alreadyClosed)
	require.Equal(t, StatusClosed, alreadyClosed.Status)
}

// TestApplyStatusAnnotation_DateOffTheWinningLine: `resolved_at` comes off
// the line the winning tag sits on, the same rule applyClosureMarker
// follows. A record cites several dates and only one of them is when it was
// discharged — and the tag itself stops at `]`, so the date can be inside it
// or after it.
func TestApplyStatusAnnotation_DateOffTheWinningLine(t *testing.T) {
	inside := Record{Status: StatusOpen, Body: "- [Defer] filed 2026-01-01, code last moved 2026-02-02\n" +
		"- [Closed, 2026-08-28] Story 23.2 landed it\n"}
	applyStatusAnnotation(&inside)
	require.Equal(t, "2026-08-28", inside.ResolvedAt)

	after := Record{Status: StatusOpen, Body: "- [Defer] filed 2026-01-01\n" +
		"- [Closed: bdffb51] landed on 2026-08-29\n"}
	applyStatusAnnotation(&after)
	require.Equal(t, "2026-08-29", after.ResolvedAt)

	// THE WINNING line, not the first one. A [Closed] earlier in the
	// chronology is routinely a comment about a neighbouring entry, and its
	// date is not this record's.
	last := Record{Status: StatusOpen, Body: "- [Defer] a thing\n" +
		"- [Closed, 2026-03-03] the entry ABOVE, not this one\n" +
		"- [Closed, 2026-09-09] this one, later\n"}
	applyStatusAnnotation(&last)
	require.Equal(t, "2026-09-09", last.ResolvedAt)

	// No date on the line is no date at all, never a guess from elsewhere.
	none := Record{Status: StatusOpen, Body: "- [Defer] filed 2026-01-01\n- [Closed: bdffb51] landed\n"}
	applyStatusAnnotation(&none)
	require.Equal(t, StatusClosed, none.Status)
	require.Empty(t, none.ResolvedAt)

	// `[Superseded]` writes no date: there is no discarded-at field, and
	// resolved_at on a discarded record would assert a delivery.
	sup := Record{Status: StatusOpen, Body: "- [Defer] a thing\n- [Superseded: docs/a.md:5] on 2026-08-28\n"}
	applyStatusAnnotation(&sup)
	require.Equal(t, StatusDiscarded, sup.Status)
	require.Empty(t, sup.ResolvedAt)
}

// TestApplyClosureMarker_LastMarkerWins: markers are appended, never edited,
// so a run of them is a chronology and the last one is the discharge that
// actually happened.
//
// This is the reference ledger's own shape: a disposition note saying the
// entry is NOT yet closed, overruled six days later by a RESOLVED clause
// that calls the earlier note "now-stale". Reading the first marker dated
// the discharge from the note that denies it.
func TestApplyClosureMarker_LastMarkerWins(t *testing.T) {
	rec := Record{Status: StatusOpen, Body: "- [Defer] a thing re-opened once\n" +
		"  - **RESOLVED (2026-08-20) — closed by Story 23.4.**\n" +
		"  - **RESOLVED (2026-08-26, Story 29.3) — re-derived against the tree in this pass, not\n" +
		"    trusted from the earlier clause's own now-stale restatement.**\n"}
	applyClosureMarker(&rec)
	require.Equal(t, StatusClosed, rec.Status)
	require.Equal(t, "2026-08-26", rec.ResolvedAt,
		"the discharge date comes from the LAST marker, not the one it superseded")
}

// TestApplyClosureMarker_DispositionIsNotADischarge: `Disposition recorded`
// asserts that a decision was written down, not that the work was done.
//
// Across the three field ledgers — 730 records — the phrase occurs exactly
// once, and there it says the entry is NOT yet closed. Its whole record as a
// discharge token is one occurrence meaning the opposite, and no anchor can
// rescue it because the negation lives in the prose after the token, which
// this design does not read. Not closing a record is recoverable; wrongly
// closing one asserts a delivery that never happened.
func TestApplyClosureMarker_DispositionIsNotADischarge(t *testing.T) {
	rec := Record{Status: StatusOpen, Body: "- [Defer] the v0.1.0 tag already exists\n" +
		"  - **Disposition recorded (2026-08-20, Story 23.4) — dissolved by construction once the\n" +
		"    operator's tag move happens; not yet closed, because the move has not happened.**\n"}
	applyClosureMarker(&rec)
	require.Equal(t, StatusOpen, rec.Status, "a note that denies discharge must not discharge")
	require.Empty(t, rec.ResolvedAt)

	// The real record carried a later RESOLVED clause too, and that is what
	// discharges it — the disposition line is simply inert.
	rec.Body += "  - **RESOLVED (2026-08-26, Story 29.3) — re-derived against the tree.**\n"
	applyClosureMarker(&rec)
	require.Equal(t, StatusClosed, rec.Status)
	require.Equal(t, "2026-08-26", rec.ResolvedAt)
}

// --- IngestStructured ---
