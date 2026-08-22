package memory

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// realShapedFile mirrors the framework's own template: H2 sections, and
// entries written as `### {area} — {YYYY-MM-DD}` with a Source line.
const realShapedFile = `# Team Memory

Lessons carried forward across epics. Append-only; compaction is the
retrospective's closing step.

## Process

### Story sizing — 2026-03-01

Stories over 8 tasks reliably slip. Split at the seam.

Source: Epic 3 retrospective

### Review cadence — 2026-04-12

Review the day the story lands, not at epic close.

Source: Epic 7 retrospective

## Technical

### Migration ordering — 2026-05-02

Run schema migrations before the deploy, never with it.

Source: Epic 11 retrospective
`

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "team-memory.md")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// grepCount is what `grep -c '^### '` would report — the baseline the
// acceptance criterion compares against.
func grepCount(body string) int {
	n := 0
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "### ") {
			n++
		}
	}
	return n
}

func TestLoad_MatchesGrepOnARealShapedFile(t *testing.T) {
	idx, err := Load(write(t, realShapedFile))
	require.NoError(t, err)
	require.Equal(t, grepCount(realShapedFile), idx.Count)
	require.Equal(t, 3, idx.Count)
}

func TestLoad_EntryFields(t *testing.T) {
	idx, err := Load(write(t, realShapedFile))
	require.NoError(t, err)

	require.Equal(t, 1, idx.Entries[0].Ordinal)
	require.Equal(t, "Process", idx.Entries[0].Section)
	require.Equal(t, "2026-03-01", idx.Entries[0].Date)
	require.Equal(t, "Story sizing", idx.Entries[0].Title)
	require.Positive(t, idx.Entries[0].Bytes)

	require.Equal(t, "Technical", idx.Entries[2].Section, "the section advances with the H2")
	require.Equal(t, "Migration ordering", idx.Entries[2].Title)

	// The heading's line number lets an operator jump straight to it.
	lines := strings.Split(realShapedFile, "\n")
	require.Equal(t, "### Story sizing — 2026-03-01", lines[idx.Entries[0].Line-1])
}

// TestLoad_ByteConservation is the losslessness proof: every byte of the
// file is attributed to exactly one of the three buckets.
func TestLoad_ByteConservation(t *testing.T) {
	for name, body := range map[string]string{
		"real shape":     realShapedFile,
		"no preamble":    "### A — 2026-01-01\n\nx\n",
		"only preamble":  "# Title\n\nnothing else\n",
		"trailing entry": "## S\n\n### A — 2026-01-01\n\nlast line no newline",
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			idx, err := Load(write(t, body))
			require.NoError(t, err)
			require.Equal(t, len(body), idx.TotalBytes)
			require.Equal(t, idx.TotalBytes, idx.EntryBytes+idx.HeadingBytes+idx.OtherBytes,
				"entry + heading + other must partition the file exactly")
		})
	}
}

// TestLoad_FencedHeadingIsNotAnEntry is the hazard the upstream plan does
// not name. A `### ` pasted inside a code fence would shift every ordinal
// after it, and `show <n>` would hand a skill the wrong entry with nothing
// to signal it.
func TestLoad_FencedHeadingIsNotAnEntry(t *testing.T) {
	body := "## S\n\n" +
		"### Real entry — 2026-01-01\n\n" +
		"Here is the diff that caused it:\n\n" +
		"```markdown\n### Not an entry — 2026-01-02\n```\n\n" +
		"### Second real entry — 2026-01-03\n\nx\n"

	idx, err := Load(write(t, body))
	require.NoError(t, err)
	require.Equal(t, 2, idx.Count, "the fenced heading is content, not an entry")
	require.Equal(t, 3, grepCount(body), "a naive grep would count three — that is the bug")
	require.Equal(t, "Real entry", idx.Entries[0].Title)
	require.Equal(t, "Second real entry", idx.Entries[1].Title)

	// And the fenced text stays inside the first entry's body.
	bodies, err := Bodies(write(t, body), []int{1})
	require.NoError(t, err)
	require.Contains(t, bodies[0], "### Not an entry")
}

func TestLoad_TildeFenceAndMismatchedMarkers(t *testing.T) {
	body := "~~~\n### inside tilde fence\n~~~\n\n### Real — 2026-01-01\n\nx\n"
	idx, err := Load(write(t, body))
	require.NoError(t, err)
	require.Equal(t, 1, idx.Count)

	// A ``` inside a ~~~ block is content, so the tilde block stays open
	// until its own closer.
	body2 := "~~~\n```\n### still inside\n~~~\n\n### Real — 2026-01-01\n\nx\n"
	idx2, err := Load(write(t, body2))
	require.NoError(t, err)
	require.Equal(t, 1, idx2.Count)
}

func TestLoad_IndentedFence(t *testing.T) {
	body := "### Real — 2026-01-01\n\n- example:\n\n  ```\n  ### not an entry\n  ```\n\nx\n"
	idx, err := Load(write(t, body))
	require.NoError(t, err)
	require.Equal(t, 1, idx.Count, "a fence inside a list item is still a fence")
}

func TestLoad_DeeperHeadingsDoNotCount(t *testing.T) {
	body := "## S\n\n### Entry — 2026-01-01\n\n#### Sub-heading\n\nx\n\n##### Deeper\n"
	idx, err := Load(write(t, body))
	require.NoError(t, err)
	require.Equal(t, 1, idx.Count)
	require.Equal(t, "S", idx.Entries[0].Section, "#### must not reset the section")
}

func TestLoad_HeadingWithoutDate(t *testing.T) {
	idx, err := Load(write(t, "### A hand-written heading\n\nx\n"))
	require.NoError(t, err)
	require.Equal(t, 1, idx.Count, "a heading with no date still gets an ordinal")
	require.Equal(t, "A hand-written heading", idx.Entries[0].Title)
	require.Empty(t, idx.Entries[0].Date)
}

func TestLoad_HeadingDateSeparators(t *testing.T) {
	for _, sep := range []string{"—", "–", "-"} {
		t.Run(sep, func(t *testing.T) {
			idx, err := Load(write(t, "### Area "+sep+" 2026-07-04\n\nx\n"))
			require.NoError(t, err)
			require.Equal(t, "Area", idx.Entries[0].Title)
			require.Equal(t, "2026-07-04", idx.Entries[0].Date)
		})
	}
}

func TestLoad_CRLF(t *testing.T) {
	body := "## S\r\n\r\n### Entry — 2026-01-01\r\n\r\nx\r\n"
	idx, err := Load(write(t, body))
	require.NoError(t, err)
	require.Equal(t, 1, idx.Count)
	require.Equal(t, "Entry", idx.Entries[0].Title, "the CR must not end up in the title")
	require.Equal(t, "S", idx.Entries[0].Section)
	require.Equal(t, idx.TotalBytes, idx.EntryBytes+idx.HeadingBytes+idx.OtherBytes)
}

func TestLoad_EmptyAndAbsent(t *testing.T) {
	idx, err := Load(write(t, ""))
	require.NoError(t, err)
	require.Zero(t, idx.Count)

	idx, err = Load(filepath.Join(t.TempDir(), "nope.md"))
	require.NoError(t, err, "an absent file is an empty index, not an error")
	require.Zero(t, idx.Count)
}

func TestBodies_Verbatim(t *testing.T) {
	path := write(t, realShapedFile)
	bodies, err := Bodies(path, []int{1})
	require.NoError(t, err)
	require.Equal(t,
		"### Story sizing — 2026-03-01\n\nStories over 8 tasks reliably slip. Split at the seam.\n\nSource: Epic 3 retrospective\n\n",
		bodies[0])
}

func TestBodies_OrderPreservedAndDuplicatesAllowed(t *testing.T) {
	path := write(t, realShapedFile)
	bodies, err := Bodies(path, []int{3, 1, 3})
	require.NoError(t, err)
	require.Len(t, bodies, 3)
	require.Contains(t, bodies[0], "Migration ordering")
	require.Contains(t, bodies[1], "Story sizing")
	require.Equal(t, bodies[0], bodies[2])
}

func TestBodies_OutOfRange(t *testing.T) {
	path := write(t, realShapedFile)
	for _, n := range []int{0, -1, 4, 999} {
		_, err := Bodies(path, []int{n})
		require.ErrorIs(t, err, ErrOutOfRange, "ordinal %d", n)
	}
}

// TestBodies_ConcatenationReconstructsTheEntryRegion ties Bodies to the
// index: reading every entry back must yield exactly EntryBytes.
func TestBodies_ConcatenationReconstructsTheEntryRegion(t *testing.T) {
	path := write(t, realShapedFile)
	idx, err := Load(path)
	require.NoError(t, err)
	ordinals := make([]int, idx.Count)
	for i := range ordinals {
		ordinals[i] = i + 1
	}
	bodies, err := Bodies(path, ordinals)
	require.NoError(t, err)
	total := 0
	for _, b := range bodies {
		total += len(b)
	}
	require.Equal(t, idx.EntryBytes, total)
}

func TestCheckSize_States(t *testing.T) {
	cases := []struct {
		name  string
		size  int
		soft  int64
		hard  int64
		state State
	}{
		{"under soft", 100, 1000, 2000, StateOK},
		{"exactly soft is still ok", 1000, 1000, 2000, StateOK},
		{"over soft", 1001, 1000, 2000, StateOverSoft},
		{"exactly hard is still over-soft", 2000, 1000, 2000, StateOverSoft},
		{"over hard", 2001, 1000, 2000, StateOverHard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := write(t, strings.Repeat("x", tc.size))
			c := CheckSize(path, tc.soft, tc.hard)
			require.Equal(t, tc.state, c.State)
			require.True(t, c.Exists)
			require.Equal(t, int64(tc.size), c.Bytes)
			require.Equal(t, int64(tc.size/4), c.EstimatedTokens)
		})
	}
}

func TestCheckSize_AbsentFile(t *testing.T) {
	c := CheckSize(filepath.Join(t.TempDir(), "nope.md"), 0, 0)
	require.Equal(t, StateAbsent, c.State)
	require.False(t, c.Exists)
	require.Zero(t, c.Bytes)
	require.Equal(t, int64(DefaultSoftBudget), c.SoftBudget, "zero means take the default")
	require.Equal(t, int64(DefaultHardCeiling), c.HardCeiling)
}

// TestCheckSize_DoesNotReadTheFile is what makes the check cheap enough
// to run on every retrospective: a file far past the Read cap is
// classified from its stat alone.
func TestCheckSize_DoesNotReadTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "team-memory.md")
	f, err := os.Create(path)
	require.NoError(t, err)
	// A sparse file: 8 MiB of logical size, ~no blocks. Reading it would
	// be obvious; stat is instant.
	require.NoError(t, f.Truncate(8<<20))
	require.NoError(t, f.Close())

	c := CheckSize(path, 0, 0)
	require.Equal(t, StateOverHard, c.State)
	require.Equal(t, int64(8<<20), c.Bytes)
	require.Greater(t, c.Bytes, int64(ReadCap), "well past the Read cap and still classified")
}

// TestCheckSize_ReferenceProjectSize is the acceptance case: the real
// 431,950-byte file reports over-hard.
func TestCheckSize_ReferenceProjectSize(t *testing.T) {
	path := write(t, strings.Repeat("x", 431_950))
	c := CheckSize(path, 0, 0)
	require.Equal(t, StateOverHard, c.State)
	require.Equal(t, int64(431_950), c.Bytes)
}

// TestLoad_LargeFileStillIndexes proves the index path is not subject to
// the Read cap that defeats the skills.
func TestLoad_LargeFileStillIndexes(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("# Team Memory\n\n## Process\n\n")
	const want = 180
	for i := 1; i <= want; i++ {
		fmt.Fprintf(&b, "### Lesson %d — 2026-01-%02d\n\n%s\n\nSource: Epic %d retrospective\n\n",
			i, (i%28)+1, strings.Repeat("prose ", 400), i)
	}
	path := write(t, b.String())

	require.Greater(t, len(b.String()), ReadCap, "the fixture is past the Read cap")
	idx, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, want, idx.Count, "180 entries, the reference project's count")
	require.Equal(t, grepCount(b.String()), idx.Count)
	require.Equal(t, idx.TotalBytes, idx.EntryBytes+idx.HeadingBytes+idx.OtherBytes)
}

func TestParseOrdinals(t *testing.T) {
	got, err := ParseOrdinals("3,7,12")
	require.NoError(t, err)
	require.Equal(t, []int{3, 7, 12}, got)

	got, err = ParseOrdinals("5")
	require.NoError(t, err)
	require.Equal(t, []int{5}, got)

	got, err = ParseOrdinals(" 1, 2 ,3 ")
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, got)

	for _, bad := range []string{"", ",", "abc", "1,x"} {
		_, err := ParseOrdinals(bad)
		require.Error(t, err, "input %q", bad)
	}
}
