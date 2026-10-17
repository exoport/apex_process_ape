package mdscan

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The extracted behaviour is already covered where it came from —
// `internal/story`'s section and File List tests and `internal/apexdoc`'s
// shard/assemble round trips all pass textually unchanged, which is the
// evidence that the move preserved behaviour. This file covers only what
// those do not: the link set, which apexdoc exercised solely through
// Shard and Assemble, and the one distinction the package's comments warn
// a reader about.

// TestLinkSet_EachFormIsMatched makes the "move the whole set" decision
// checkable.
//
// The spec graph asked for SectionEntryRe alone. Exporting only that — or
// only MDLinkRe — would have left it blind to link forms apexdoc already
// handles, and a reader that sees fewer links than the tool whose regexes
// it borrowed has gaps that look like the document's. So each form gets a
// case, and a future edit that drops one fails here rather than in a
// graph payload nobody is diffing.
func TestLinkSet_EachFormIsMatched(t *testing.T) {
	t.Run("inline link and image", func(t *testing.T) {
		m := MDLinkRe.FindStringSubmatch("see [the ADR](../adrs/adr-0001.md) for why")
		require.Len(t, m, 3)
		require.Equal(t, "[the ADR]", m[1])
		require.Equal(t, "../adrs/adr-0001.md", m[2])

		img := MDLinkRe.FindStringSubmatch("![a diagram](img/flow.png)")
		require.Len(t, img, 3)
		require.Equal(t, "img/flow.png", img[2])
	})

	t.Run("nested brackets in the label", func(t *testing.T) {
		m := MDLinkRe.FindStringSubmatch("[see [ADR-1] here](x.md)")
		require.Len(t, m, 3)
		require.Equal(t, "x.md", m[2])
	})

	t.Run("reference-style definition", func(t *testing.T) {
		m := MDRefRe.FindStringSubmatch("[adr]: ../adrs/adr-0001.md \"Title\"")
		require.Len(t, m, 4)
		require.Equal(t, "../adrs/adr-0001.md", m[2])
	})

	t.Run("html src, quoted and bare", func(t *testing.T) {
		for _, in := range []string{`<img src="a/b.png">`, `<img src='a/b.png'>`, `<img src=a/b.png>`} {
			m := HTMLSrcRe.FindStringSubmatch(in)
			require.Len(t, m, 5, "input %q", in)
			require.Equal(t, "a/b.png", m[3], "input %q", in)
		}
	})

	t.Run("uri scheme separates rewritable from absolute", func(t *testing.T) {
		require.True(t, URISchemeRe.MatchString("https://example.test/x"))
		require.True(t, URISchemeRe.MatchString("mailto:a@b.test"))
		require.False(t, URISchemeRe.MatchString("../adrs/adr-0001.md"),
			"a relative path must not read as absolute, or shard would decline to rewrite it")
	})

	t.Run("shard index entry", func(t *testing.T) {
		const index = "## Sections\n\n- [Overview](overview.md)\n- [Data Model](data-model.md)\n"
		got := SectionEntryRe.FindAllStringSubmatch(index, -1)
		require.Len(t, got, 2)
		require.Equal(t, "overview.md", got[0][1])
		require.Equal(t, "data-model.md", got[1][1])
	})
}

// TestGCCLineRe_DoesNotCaptureCheckboxState pins the distinction the
// package comment warns about, because the repo has two checkbox regexes
// and they answer different questions.
//
// This one asks "is this a line of the declared GCC form" and matches the
// checkbox without capturing it — so a caller that needs the STATE
// (`internal/deferred`'s `deferBulletRe` shape, which captures `([ xX])`)
// cannot get it from here and must not try. Reading this regex for state
// is a mistake that has been made; the test is what makes the comment
// enforceable.
func TestGCCLineRe_DoesNotCaptureCheckboxState(t *testing.T) {
	unticked := GCCLineRe.FindStringSubmatch("- [ ] **[ADR-1]** do the thing -- scope")
	ticked := GCCLineRe.FindStringSubmatch("- [x] **[ADR-1]** do the thing -- scope")

	require.NotNil(t, unticked)
	require.NotNil(t, ticked)

	// Element 0 is the whole match and of course differs — the checkbox is
	// IN the line. The claim is about the capture groups: everything this
	// regex hands a caller is identical for both states, so state cannot
	// be recovered from it however carefully the caller reads.
	require.Equal(t, unticked[1:], ticked[1:],
		"the two states produce identical captures — this regex cannot tell them apart, "+
			"and a caller needing checkbox state must use a pattern that captures it")
}

// TestSection_StopsAtTheSameOrShallowerHeading is the one structural
// behaviour worth restating here rather than only in story's tests: a
// deeper heading is part of the section, a sibling or shallower one ends
// it. Every consumer that slices a document by heading depends on it.
func TestSection_StopsAtTheSameOrShallowerHeading(t *testing.T) {
	const doc = "## Story\n" +
		"body one\n" +
		"### Nested\n" +
		"still inside\n" +
		"## Acceptance Criteria\n" +
		"outside\n"

	got := Section(doc, "## Story")
	require.Contains(t, got, "body one")
	require.Contains(t, got, "### Nested")
	require.Contains(t, got, "still inside")
	require.NotContains(t, got, "outside")

	require.Empty(t, Section(doc, "## Dev Notes"), "an absent heading yields nothing")
}

// TestSection_NormalisesHeadingWhitespace covers why WSRun travelled with
// Section rather than staying behind: a formatter rewriting
// `## Tasks / Subtasks` to `## Tasks  /  Subtasks` must not change which
// section a caller gets.
func TestSection_NormalisesHeadingWhitespace(t *testing.T) {
	const doc = "## Tasks  /  Subtasks\ninside\n## Dev Notes\nout\n"
	require.Contains(t, Section(doc, "## Tasks / Subtasks"), "inside")
}

// --- fences inside block quotes ---------------------------------------

// A fence in a block quote is a fence. Before this was handled, the
// quoted example below survived into Prose, and `ape story verify --file`
// reported the placeholder inside it as residue — exit 4 on a story whose
// only "residue" was an example. The unquoted control is what shows the
// quote was the only difference.
func TestStripFences_BlockQuotedFence(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ in, want string }{
		"unquoted control": {"a\n```md\nhidden\n```\nb", "a\nb"},
		"quoted":           {"a\n> ```md\n> hidden\n> ```\nb", "a\nb"},
		"no space after >": {"a\n>```md\n>hidden\n>```\nb", "a\nb"},
		"quote indented three spaces": {
			"a\n   > ```\n   > hidden\n   > ```\nb", "a\nb",
		},
		"quote indented four is not a quote": {
			"a\n    > ```\nb", "a\n    > ```\nb",
		},
		"a bare > inside the block keeps it open": {
			"a\n> ```\n> one\n>\n> two\n> ```\nb", "a\nb",
		},
		"quoted prose around the fence stays": {
			"> Example:\n> ```md\n> hidden\n> ```\n> After.", "> Example:\n> After.",
		},
		// Depth is state, both ways. A deeper fence line inside a shallower
		// fence is content: stripping every marker off it would read `> > ```
		// as a close and leave "hidden" in the prose.
		"a deeper fence line inside a shallower fence is content": {
			"a\n> ```\n> > ```\n> hidden\n> ```\nb", "a\nb",
		},
		// And a shallower line ends a deeper fence, because the inner quote
		// holding it has ended; the line itself is prose again.
		"a shallower line ends a deeper fence": {
			"a\n> > ```\n> > hidden\n> text\nb", "a\n> text\nb",
		},
		"an unquoted fence's quoted lines are content": {
			"a\n```\n> ```\nhidden\n```\nb", "a\nb",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, StripFences(tc.in))
		})
	}
}

// The rule that keeps the fix from being worse than the defect. A quoted
// fence nobody closed ends where its block quote ends; if it ran to the
// end of the document instead, every real section after it — a real
// `- [ ] [Patch]`, a real File List — would vanish from Prose, and a
// false positive would have been traded for false negatives.
func TestStripFences_AnUnclosedQuotedFenceEndsWithItsQuote(t *testing.T) {
	t.Parallel()
	in := "## Dev Notes\n\n> ```md\n> an example nobody closed\n\n### Review Findings\n\n- [ ] [Patch] real"
	require.Equal(t, "## Dev Notes\n\n\n### Review Findings\n\n- [ ] [Patch] real", StripFences(in))
}

// The documented residual, pinned so the doc comment cannot go stale
// silently: a fence under a list item indented four or more spaces is an
// indented line here, not a fence, whatever CommonMark says.
func TestStripFences_ListItemFenceIndentedFourIsNotHandled(t *testing.T) {
	t.Parallel()
	in := "- step:\n\n    ```bash\n    make build\n    ```"
	require.Equal(t, in, StripFences(in),
		"if this now strips, list-item fences are handled — update StripFences' NOT-handled note")
}
