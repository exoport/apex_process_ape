package deferred

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// realLedgerShapes is a minimal corpus carrying one instance of every shape
// that the first real-project migration (axon_tenax_engine, 2,535 lines,
// 456,144 bytes, 124 records) got wrong. Every heading, ID and field value
// here is taken from that run rather than invented.
//
// Each subtest below asserts the CORRECTED behaviour, so the whole file is
// expected to fail at v0.0.60. It is the acceptance criteria of the fix,
// written as a test.
const realLedgerShapes = "# Deferred Work\n" +
	"\n" +
	"> **Reconciliation banner (2026-07-07).** Document history: which sweeps\n" +
	"> reconciled which claims. Not a deferred item.\n" +
	"\n" +
	"## Still open\n" +
	"\n" +
	"## Deferred from: story review of 64-1_combinators-future-tree-ordering-over-the-wire (2026-07-18)\n" +
	"\n" +
	"- [Defer] A genuine defer inside the 64-1 section [pkg/a.go:12] — defer: outside-story=no\n" +
	"\n" +
	"## OPEN — ADR-0050 index-membership gap (Story 67.2 AC3, 2026-07-18)\n" +
	"\n" +
	"- the content of the OPEN section, written as a bullet\n" +
	"\n" +
	"## OPEN — Story 69.2 review cycle 2: normative text drifted (2026-07-19)\n" +
	"\n" +
	"Prose content directly under the heading, with no bullet.\n" +
	"\n" +
	"## Deferred from: story review of 86-1_create-branch-cas-conflict-classification (retroactively backfilled by Story 89.2, 2026-07-26)\n" +
	"\n" +
	"- [Defer] The retroactively backfilled item — defer: outside-story=yes\n" +
	"\n" +
	"## Deferred from: story review of 90-1 (2026-07-28)\n" +
	"\n" +
	"> ✅ **RESOLVED — 2026-07-20, by Story 70.5's dev cycle 2, commit 71e2635.**\n" +
	"> A section-annotation blockquote that announces its own resolution.\n" +
	"\n" +
	"- [Defer] Wrapped tail — defer: outside-story=none of the 4 files are in\n" +
	"  the story's File List; cross-cycle=no\n" +
	"\n" +
	"- A plain bullet with no [Defer] marker, whose prose happens to mention\n" +
	"  outside-story=yes while describing something else entirely.\n"

// findByBodyContains returns the single record whose body contains needle.
func findByBodyContains(t *testing.T, recs []Record, needle string) Record {
	t.Helper()
	var hits []Record
	for i := range recs {
		if strings.Contains(recs[i].Body, needle) {
			hits = append(hits, recs[i])
		}
	}
	require.Lenf(t, hits, 1, "expected exactly one record containing %q, got %d", needle, len(hits))
	return hits[0]
}

// FW-1a: a `##` heading that is not a recognised `Deferred from:` form must
// RESET section context. Inheriting the previous section's story is worse
// than absence, and the inherited date is baked into the record id, so it
// cannot be repaired by editing frontmatter afterwards.
func TestParseLegacy_NonMatchingHeadingResetsSectionContext(t *testing.T) {
	recs := ParseLegacy([]byte(realLedgerShapes))

	openBullet := findByBodyContains(t, recs, "the content of the OPEN section")
	require.Empty(t, openBullet.SourceStory,
		"a record under `## OPEN — …` must not inherit the previous `## Deferred from:` story")
	require.Empty(t, openBullet.Created,
		"nor its date — the date is hashed into the id, so a wrong one is unrepairable")
	require.NotContains(t, openBullet.ID, "20260718",
		"an inherited date must not reach the id")

	openProse := findByBodyContains(t, recs, "Prose content directly under the heading")
	require.Empty(t, openProse.SourceStory)
	require.Empty(t, openProse.Created)
}

// FW-1b: the slug and the date must be extracted INDEPENDENTLY. Both are
// present in this heading; today the prose parenthetical defeats the date
// group, and lazy backtracking then collapses the slug into the label too,
// so both are lost at once.
func TestParseLegacy_HeadingWithProseParenthetical(t *testing.T) {
	recs := ParseLegacy([]byte(realLedgerShapes))
	rec := findByBodyContains(t, recs, "The retroactively backfilled item")

	require.Equal(t, "86-1_create-branch-cas-conflict-classification", rec.SourceStory,
		"the slug anchor is `of <slug>`, whatever follows it")
	require.Equal(t, "2026-07-26", rec.Created,
		"the date is the last YYYY-MM-DD anywhere in the heading")
	require.Equal(t, SourceStoryReview, rec.Source)
	require.NotContains(t, rec.ID, "00000000",
		"a heading that carries a date must not produce an undated id")
}

// FW-2: a `##` heading is never a record on its own. Today it becomes one
// when the next line is a top-level bullet (flush fires) and merges with
// the content when the next line is unindented prose (flush does not) — so
// the store holds titles with no body AND bodies with no title.
func TestParseLegacy_HeadingIsNeverAStandaloneRecord(t *testing.T) {
	recs := ParseLegacy([]byte(realLedgerShapes))

	for i := range recs {
		body := strings.TrimSpace(recs[i].Body)
		require.Falsef(t, strings.HasPrefix(body, "## ") && !strings.Contains(body, "\n"),
			"record %s has a lone heading as its entire body: %q", recs[i].ID, body)
	}

	// The heading titles the record that follows it, in BOTH shapes.
	openBullet := findByBodyContains(t, recs, "the content of the OPEN section")
	require.Contains(t, openBullet.Title, "ADR-0050 index-membership gap",
		"the heading above a bullet must title that bullet's record")

	openProse := findByBodyContains(t, recs, "Prose content directly under the heading")
	require.Contains(t, openProse.Title, "normative text drifted",
		"and the heading above prose must behave identically")
}

// FW-3: a blockquote-only block is a section annotation, not a deferred
// item — and four of the seven in the real corpus announce that the work
// is already done. A record whose own first line says `RESOLVED` must not
// be written `status: open`.
func TestParseLegacy_ResolvedBlockquoteIsNotOpen(t *testing.T) {
	recs := ParseLegacy([]byte(realLedgerShapes))
	rec := findByBodyContains(t, recs, "RESOLVED — 2026-07-20")

	require.NotEqual(t, StatusOpen, rec.Status,
		"a body announcing its own resolution cannot be an open record")
	require.False(t, rec.IsOpen(),
		"and IsOpen must agree — an empty status reads as open")
}

// FW-4: content above the first record boundary is preamble. In the real
// corpus this produced a single 434-line / 37,822-byte record that was the
// store's largest open "item" and was pure document history.
func TestParseLegacy_PreambleIsNotARecord(t *testing.T) {
	recs := ParseLegacy([]byte(realLedgerShapes))

	for i := range recs {
		require.NotContainsf(t, recs[i].Body, "Reconciliation banner (2026-07-07)",
			"record %s is the document preamble", recs[i].ID)
		require.NotContainsf(t, recs[i].Body, "# Deferred Work",
			"record %s carries the ledger's own H1", recs[i].ID)
	}
}

// FW-5: a frontmatter value is the whole value or it is absent. The real
// corpus wraps the `— defer:` tail across continuation lines, and
// `[^;\n]+` stops at the wrap, so three records present a truncated
// half-sentence as an authoritative field.
func TestParseLegacy_WrappedTailValueIsNotTruncated(t *testing.T) {
	recs := ParseLegacy([]byte(realLedgerShapes))
	rec := findByBodyContains(t, recs, "Wrapped tail")

	require.NotEqual(t, "none of the 4 files are in", rec.OutsideStory,
		"the value was cut at the line wrap")
	if rec.OutsideStory != "" {
		require.Contains(t, rec.OutsideStory, "File List",
			"a present value must span the wrap and reach its own terminator")
	}
	require.Equal(t, "no", rec.CrossCycle,
		"the field after the wrapped one must still be found")
}

// FW-5 (converse): `free_form: true` means the parser could not complete
// the record. Today applyTail runs over the ENTIRE BODY for free-form
// records, so any body containing `outside-story=` anywhere acquires a
// field — which is how a record is simultaneously flagged unparseable and
// carrying parsed values.
func TestParseLegacy_FreeFormRecordsCarryNoParsedFields(t *testing.T) {
	recs := ParseLegacy([]byte(realLedgerShapes))

	for i := range recs {
		rec := recs[i]
		if !rec.FreeForm {
			continue
		}
		require.Emptyf(t, rec.OutsideStory,
			"free-form record %s carries a parsed outside_story", rec.ID)
		require.Emptyf(t, rec.CrossCycle,
			"free-form record %s carries a parsed cross_cycle", rec.ID)
		require.Emptyf(t, rec.NonBlocking,
			"free-form record %s carries a parsed non_blocking", rec.ID)
	}
}

// The guarantee that HELD, and the reason the bad migration was
// recoverable at all: a line multiset compared in both directions. It must
// keep passing through every change above.
//
// ONE DEVIATION from the handoff's version of this test, because that
// version cannot pass alongside its own FW-4:
// TestParseLegacy_PreambleIsNotARecord requires the preamble to stop being
// a record, while this test as written requires every non-blank,
// non-heading source line to appear in some record body — and
// `# Deferred Work` is both. The two assertions are in direct conflict.
//
// Resolved by keeping the preamble instead of dropping it: it is no longer
// a RECORD, and it is no longer LOST either. The comparison is against
// records plus preamble, which is what the migration actually writes, so
// the guarantee gets stronger rather than being relaxed to accommodate a
// gap.
//
// The only permitted asymmetry is a `##` heading line, which the parser
// lifts into frontmatter as provenance or as a title. That exemption is
// now uniform in both directions — no heading is ever kept — which is what
// let the same comparison be promoted into Migrate itself as a pre-write
// assertion (TestMigrate_RefusesToWriteALossyParse).
func TestParseLegacy_BodiesSurviveAsALineMultiset(t *testing.T) {
	doc := ParseLegacyDocument([]byte(realLedgerShapes))
	recs, preamble := doc.Records, doc.Preamble

	count := func(lines []string) map[string]int {
		m := map[string]int{}
		for _, l := range lines {
			if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "## ") {
				continue
			}
			m[l]++
		}
		return m
	}

	source := count(strings.Split(realLedgerShapes, "\n"))
	bodyLines := strings.Split(preamble, "\n")
	for i := range recs {
		bodyLines = append(bodyLines, strings.Split(recs[i].Body, "\n")...)
	}
	emitted := count(bodyLines)

	for line, n := range source {
		require.Equalf(t, n, emitted[line], "source line lost or duplicated: %q", line)
	}
	for line, n := range emitted {
		require.Equalf(t, n, source[line], "record line not present in the source: %q", line)
	}
}
