package apexdoc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSlugify_GoldenAgainstThePython is the retirement gate for the whole
// deliverable. A slug differing by one character produces a different
// filename, and `assemble` then cannot find its own shards — so these
// expectations are shard-doc.py's slugify output, character for character.
func TestSlugify_GoldenAgainstThePython(t *testing.T) {
	cases := map[string]string{
		"Simple Heading":              "simple-heading",
		"UPPER CASE":                  "upper-case",
		"With  Multiple   Spaces":     "with-multiple-spaces",
		"With_Underscores_Here":       "with-underscores-here",
		"Punctuation! Here? Yes.":     "punctuation-here-yes",
		"Already-Hyphenated":          "already-hyphenated",
		"Double--Hyphen":              "double-hyphen",
		"-Leading and trailing-":      "leading-and-trailing",
		"Numbers 123 and 456":         "numbers-123-and-456",
		"Mixed: colons; semicolons":   "mixed-colons-semicolons",
		"(Parenthesised)":             "parenthesised",
		"Slashes/and\\backslashes":    "slashesandbackslashes",
		"Quotes \"here\" and 'there'": "quotes-here-and-there",
		"Trailing spaces   ":          "trailing-spaces",
		"!!!":                         "section",
		"":                            "section",
		"---":                         "section",
		"Ampersand & More":            "ampersand-more",
		"Em—dash":                     "emdash",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			require.Equal(t, want, Slugify(input))
		})
	}
}

// docWithSections is a document in the shape apex-shard-doc really gets:
// a preamble with relative links, then H2 sections.
const docWithSections = `# Product Requirements

See the [architecture](architecture.md) and the [external spec](https://example.com/spec).
Also ![a diagram](assets/diagram.png) and a <img src="assets/logo.png"> tag.

[ref]: notes.md "Notes"

An already-parent link to [the sibling](../sibling/thing.md) must survive.

## Goals

Ship the thing. See [architecture](architecture.md#goals).

## Non-goals

Not shipping the other thing.

## Constraints

Budget and time.
`

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// --- Verify ---

func TestVerify_Clean(t *testing.T) {
	dir := t.TempDir()
	src := write(t, dir, "prd.md", docWithSections)

	res, err := Verify(src, DefaultLevel)
	require.NoError(t, err)
	require.True(t, res.OK())
	require.Equal(t, 3, res.Headings)
}

func TestVerify_DuplicatesWithLineNumbers(t *testing.T) {
	dir := t.TempDir()
	src := write(t, dir, "prd.md", "# T\n\n## Goals\n\nx\n\n## Non-goals\n\ny\n\n## GOALS!\n\nz\n")

	res, err := Verify(src, DefaultLevel)
	require.NoError(t, err)
	require.False(t, res.OK())
	require.Len(t, res.Duplicates, 1)
	d := res.Duplicates[0]
	require.Equal(t, "goals", d.Slug)
	require.Equal(t, "Goals", d.First)
	require.Equal(t, 3, d.FirstLine)
	require.Equal(t, "GOALS!", d.Second)
	require.Equal(t, 11, d.SecondLine)
}

func TestVerify_LevelVariants(t *testing.T) {
	dir := t.TempDir()
	src := write(t, dir, "d.md", "# A\n\n## B\n\n### C\n\n### C again\n")

	l2, err := Verify(src, 2)
	require.NoError(t, err)
	require.Equal(t, 1, l2.Headings)
	require.True(t, l2.OK())

	l3, err := Verify(src, 3)
	require.NoError(t, err)
	require.Equal(t, 2, l3.Headings)
	require.True(t, l3.OK(), "'C' and 'C again' slug differently")
}

func TestVerify_MissingFile(t *testing.T) {
	_, err := Verify(filepath.Join(t.TempDir(), "nope.md"), 2)
	require.Error(t, err)
}

// --- Shard / Assemble ---

// TestRoundTrip_ByteIdentical is the strongest available test and it is
// cheap: shard then assemble must return the source unchanged.
func TestRoundTrip_ByteIdentical(t *testing.T) {
	for _, level := range []int{2, 3} {
		for _, numbered := range []bool{false, true} {
			name := fmt.Sprintf("level%d-numbered=%v", level, numbered)
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				body := docWithSections
				if level == 3 {
					body = strings.ReplaceAll(body, "## ", "### ")
				}
				src := write(t, dir, "prd.md", body)
				// The link targets have to exist for the reverse rewrite to
				// strip their ../ — that existence test is what lets a
				// pre-existing ../ link survive untouched.
				write(t, dir, "architecture.md", "# Arch\n")
				write(t, dir, "notes.md", "# Notes\n")
				write(t, dir, "assets/diagram.png", "png")
				write(t, dir, "assets/logo.png", "png")

				shardDir := filepath.Join(dir, "prd")
				_, err := Shard(src, shardDir, ShardOptions{Level: level, Numbered: numbered})
				require.NoError(t, err)

				out := filepath.Join(dir, "reassembled.md")
				_, err = Assemble(shardDir, out)
				require.NoError(t, err)

				got, err := os.ReadFile(out)
				require.NoError(t, err)
				require.Equal(t, body, string(got), "the round trip must be byte-identical")
			})
		}
	}
}

// TestShard_AlwaysWritesIndex is the contract apex-shard-doc verifies in
// its own Step 4: index.md always exists and lists every section file. A
// replacement that split perfectly but omitted it passes a round-trip test
// and then fails its real caller.
func TestShard_AlwaysWritesIndex(t *testing.T) {
	dir := t.TempDir()
	src := write(t, dir, "prd.md", docWithSections)
	shardDir := filepath.Join(dir, "prd")

	res, err := Shard(src, shardDir, ShardOptions{})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(shardDir, IndexFileName), res.Index)

	index, err := os.ReadFile(res.Index)
	require.NoError(t, err)
	text := string(index)
	require.Contains(t, text, sectionsHeading)
	require.Len(t, res.Sections, 3)
	for _, sec := range res.Sections {
		require.Contains(t, text, fmt.Sprintf("- [%s](%s)", sec.Heading, sec.FileName),
			"every section is listed and linked")
		_, statErr := os.Stat(filepath.Join(shardDir, sec.FileName))
		require.NoError(t, statErr)
	}
	require.Contains(t, text, "# Product Requirements", "the preamble is carried into the index")
}

// TestAssemble_IgnoresIndexAsAShard: index.md drives the ordering and
// contributes its preamble, and must never also be concatenated as a
// section.
func TestAssemble_IgnoresIndexAsAShard(t *testing.T) {
	dir := t.TempDir()
	src := write(t, dir, "prd.md", docWithSections)
	write(t, dir, "architecture.md", "# Arch\n")
	write(t, dir, "notes.md", "# Notes\n")
	write(t, dir, "assets/diagram.png", "png")
	write(t, dir, "assets/logo.png", "png")
	shardDir := filepath.Join(dir, "prd")
	_, err := Shard(src, shardDir, ShardOptions{})
	require.NoError(t, err)

	out := filepath.Join(dir, "out.md")
	res, err := Assemble(shardDir, out)
	require.NoError(t, err)
	require.Equal(t, 3, res.Sections, "three shards, not four")

	body, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(body), "# Product Requirements"),
		"the preamble appears exactly once")
	require.NotContains(t, string(body), sectionsHeading,
		"the index's own section list is not part of the document")
}

func TestShard_LinkRewriting(t *testing.T) {
	dir := t.TempDir()
	src := write(t, dir, "prd.md", docWithSections)
	shardDir := filepath.Join(dir, "prd")
	_, err := Shard(src, shardDir, ShardOptions{})
	require.NoError(t, err)

	goals, err := os.ReadFile(filepath.Join(shardDir, "goals.md"))
	require.NoError(t, err)
	require.Contains(t, string(goals), "(../architecture.md#goals)",
		"a relative link gains ../ for the extra depth")

	index, err := os.ReadFile(filepath.Join(shardDir, IndexFileName))
	require.NoError(t, err)
	text := string(index)
	require.Contains(t, text, "(../architecture.md)")
	require.Contains(t, text, "(https://example.com/spec)", "an absolute URL is untouched")
	require.Contains(t, text, "(../assets/diagram.png)")
	require.Contains(t, text, `src="../assets/logo.png"`)
	require.Contains(t, text, `[ref]: ../notes.md "Notes"`,
		"a reference definition keeps its title")
	require.Contains(t, text, "(../sibling/thing.md)",
		"a link that was already ../ is not doubled")
}

func TestShard_RefusesOnDuplicateSlugs(t *testing.T) {
	dir := t.TempDir()
	src := write(t, dir, "prd.md", "# T\n\n## Goals\n\nx\n\n## GOALS\n\ny\n")

	_, err := Shard(src, filepath.Join(dir, "prd"), ShardOptions{})
	require.Error(t, err)
	var dup *DuplicateSlugError
	require.ErrorAs(t, err, &dup)
	require.Contains(t, err.Error(), "Fix the source first")
	require.Contains(t, err.Error(), "goals")

	_, statErr := os.Stat(filepath.Join(dir, "prd"))
	require.Error(t, statErr, "nothing is written when it refuses")
}

func TestShard_NoHeadingsAtLevelWritesWholeDocument(t *testing.T) {
	dir := t.TempDir()
	body := "# Only an H1\n\nNo H2 anywhere.\n"
	src := write(t, dir, "d.md", body)
	shardDir := filepath.Join(dir, "d")

	res, err := Shard(src, shardDir, ShardOptions{})
	require.NoError(t, err)
	require.True(t, res.WholeDocument)
	require.Empty(t, res.Sections)

	index, err := os.ReadFile(res.Index)
	require.NoError(t, err)
	require.Equal(t, body, string(index), "so assemble still has something to work from")
}

func TestShard_NumberedPrefixes(t *testing.T) {
	dir := t.TempDir()
	src := write(t, dir, "prd.md", docWithSections)
	shardDir := filepath.Join(dir, "prd")

	res, err := Shard(src, shardDir, ShardOptions{Numbered: true})
	require.NoError(t, err)
	require.Equal(t, "01-goals.md", res.Sections[0].FileName)
	require.Equal(t, "02-non-goals.md", res.Sections[1].FileName)
	require.Equal(t, "03-constraints.md", res.Sections[2].FileName)
}

func TestAssemble_MissingShardIsReportedNotFatal(t *testing.T) {
	dir := t.TempDir()
	src := write(t, dir, "prd.md", docWithSections)
	shardDir := filepath.Join(dir, "prd")
	_, err := Shard(src, shardDir, ShardOptions{})
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(shardDir, "non-goals.md")))

	res, err := Assemble(shardDir, filepath.Join(dir, "out.md"))
	require.NoError(t, err)
	require.Equal(t, 2, res.Sections)
	require.Equal(t, []string{"non-goals.md"}, res.Missing)
}

func TestAssemble_MissingIndex(t *testing.T) {
	_, err := Assemble(t.TempDir(), filepath.Join(t.TempDir(), "out.md"))
	require.Error(t, err)
}

func TestIsRelative(t *testing.T) {
	relative := []string{"a.md", "./a.md", "dir/a.md", `a.md "Title"`}
	absolute := []string{
		"", "#anchor", "/abs.md", "//host/x", "../up.md",
		"https://example.com", "http://example.com", "mailto:x@y.z", "data:text/plain,x",
	}
	for _, u := range relative {
		require.True(t, isRelative(u), "%q should be relative", u)
	}
	for _, u := range absolute {
		require.False(t, isRelative(u), "%q should not be rewritten", u)
	}
}

// --- Analyze ---

func TestAnalyze_FilesFoldersAndGlobs(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a-brief.md", strings.Repeat("x", 400))
	write(t, dir, "b-prd.md", strings.Repeat("x", 800))
	write(t, dir, "notes.txt", strings.Repeat("x", 200))
	write(t, dir, "ignored.png", "binary")
	write(t, dir, "node_modules/pkg/readme.md", "should be skipped")
	write(t, dir, ".git/COMMIT_EDITMSG", "skipped")

	t.Run("directory", func(t *testing.T) {
		res, err := Analyze([]string{dir}, AnalyzeOptions{})
		require.NoError(t, err)
		require.Equal(t, 3, res.Summary.TotalFiles,
			"only source extensions, and never node_modules or .git")
	})

	t.Run("explicit files", func(t *testing.T) {
		res, err := Analyze([]string{filepath.Join(dir, "a-brief.md")}, AnalyzeOptions{})
		require.NoError(t, err)
		require.Equal(t, 1, res.Summary.TotalFiles)
	})

	t.Run("glob", func(t *testing.T) {
		res, err := Analyze([]string{filepath.Join(dir, "*.md")}, AnalyzeOptions{})
		require.NoError(t, err)
		require.Equal(t, 2, res.Summary.TotalFiles)
	})

	t.Run("deduplicated", func(t *testing.T) {
		f := filepath.Join(dir, "a-brief.md")
		res, err := Analyze([]string{f, f, dir}, AnalyzeOptions{})
		require.NoError(t, err)
		require.Equal(t, 3, res.Summary.TotalFiles)
	})
}

func TestAnalyze_SizesAndTokens(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.md", strings.Repeat("x", 400))

	res, err := Analyze([]string{dir}, AnalyzeOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(400), res.Summary.TotalSizeBytes)
	require.Equal(t, int64(100), res.Summary.TotalEstimatedTokens, "bytes/4, an estimate")
	require.Equal(t, int64(100), res.Files[0].EstimatedTokens)
}

func TestDocType(t *testing.T) {
	cases := map[string]string{
		"product-brief.md":         "brief",
		"prd.md":                   "prd",
		"architecture.md":          "architecture",
		"epic-1-retro-20260101.md": "retrospective",
		"1-1_story.md":             "story",
		"x-discovery-notes.md":     "discovery-notes",
		"x-discovery.md":           "discovery",
		"handoff-20260101.md":      "handoff",
		"plan-25_thing.md":         "plan",
		"adr-0001_x.md":            "adr",
		"pat-0001_x.md":            "pattern",
		"something-else.md":        "document",
	}
	for name, want := range cases {
		require.Equal(t, want, DocType(name), "name %q", name)
	}
}

func TestAnalyze_GroupsPairCompanionsWithTheirPrimary(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "feature-brief.md", "x")
	write(t, dir, "feature-brief-discovery-notes.md", "x")
	write(t, dir, "unrelated.md", "x")

	res, err := Analyze([]string{dir}, AnalyzeOptions{})
	require.NoError(t, err)

	byKey := map[string]Group{}
	for _, g := range res.Groups {
		byKey[g.Key] = g
	}
	pair, ok := byKey["feature-brief"]
	require.True(t, ok, "the companion shares its primary's group key: %+v", res.Groups)
	require.Len(t, pair.Files, 2)
	roles := map[string]string{}
	for _, f := range pair.Files {
		roles[filepath.Base(f.Path)] = f.Role
	}
	require.Equal(t, RolePrimary, roles["feature-brief.md"])
	require.Equal(t, RoleCompanion, roles["feature-brief-discovery-notes.md"])

	solo := byKey["unrelated"]
	require.Len(t, solo.Files, 1)
	require.Equal(t, RoleStandalone, solo.Files[0].Role, "a group of one is not a grouping")
}

// TestAnalyze_RoutingOnTheBoundary tests AT the boundary, because <= vs <
// is the whole reason to test a threshold.
func TestAnalyze_RoutingOnTheBoundary(t *testing.T) {
	opts := AnalyzeOptions{SingleMaxFiles: 3, SingleMaxTokens: 15000, SplitMinTokens: 5000}

	cases := []struct {
		name   string
		files  int
		tokens int64
		want   string
	}{
		{"exactly the file limit", 3, 100, RoutingSingle},
		{"one file over", 4, 100, RoutingFanOut},
		{"exactly the token limit", 1, 15000, RoutingSingle},
		{"one token over", 1, 15001, RoutingFanOut},
		{"both at the limit", 3, 15000, RoutingSingle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideRouting(Summary{
				TotalFiles: tc.files, TotalEstimatedTokens: tc.tokens,
			}, opts)
			require.Equal(t, tc.want, got.Recommendation, got.Reason)
		})
	}
}

func TestAnalyze_SplitPredictionOnTheBoundary(t *testing.T) {
	opts := AnalyzeOptions{SplitMinTokens: 5000}.withDefaults()

	// The distillate is estimated at a third of the sources.
	exactly := predictSplit(Summary{TotalEstimatedTokens: 15000}, opts)
	require.Equal(t, SplitUnlikely, exactly.Prediction, "5000 is not > 5000")
	require.Equal(t, int64(5000), exactly.EstimatedDistillateTokens)

	over := predictSplit(Summary{TotalEstimatedTokens: 15003}, opts)
	require.Equal(t, SplitLikely, over.Prediction)
	require.Equal(t, int64(5001), over.EstimatedDistillateTokens)
}

func TestAnalyze_ThresholdOverrides(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		write(t, dir, fmt.Sprintf("f%d.md", i), "x")
	}
	res, err := Analyze([]string{dir}, AnalyzeOptions{SingleMaxFiles: 10})
	require.NoError(t, err)
	require.Equal(t, RoutingSingle, res.Routing.Recommendation)

	res, err = Analyze([]string{dir}, AnalyzeOptions{})
	require.NoError(t, err)
	require.Equal(t, RoutingFanOut, res.Routing.Recommendation, "the default is 3 files")
}

func TestAnalyze_EmptyInput(t *testing.T) {
	res, err := Analyze([]string{t.TempDir()}, AnalyzeOptions{})
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status)
	require.Zero(t, res.Summary.TotalFiles)
	require.Equal(t, RoutingSingle, res.Routing.Recommendation, "nothing is small enough for one pass")
}
