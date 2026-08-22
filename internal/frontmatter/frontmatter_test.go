package frontmatter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplit_Simple(t *testing.T) {
	fm, body, err := Split([]byte("---\nid: ADR-0001\nstatus: accepted\n---\n\n## Context\n\nx\n"))
	require.NoError(t, err)
	require.Equal(t, "id: ADR-0001\nstatus: accepted\n", string(fm))
	require.Equal(t, "\n## Context\n\nx\n", string(body))
}

func TestSplit_CRLF(t *testing.T) {
	fm, body, err := Split([]byte("---\r\nid: ADR-0001\r\n---\r\nbody\r\n"))
	require.NoError(t, err)
	require.Equal(t, "id: ADR-0001\r\n", string(fm))
	require.Equal(t, "body\r\n", string(body))
}

func TestSplit_BOM(t *testing.T) {
	fm, _, err := Split([]byte("\xEF\xBB\xBF---\nid: X\n---\n"))
	require.NoError(t, err)
	require.Equal(t, "id: X\n", string(fm))
}

func TestSplit_ClosingDelimiterAtEOFWithoutNewline(t *testing.T) {
	fm, body, err := Split([]byte("---\nid: X\n---"))
	require.NoError(t, err)
	require.Equal(t, "id: X\n", string(fm))
	require.Empty(t, body)
}

func TestSplit_TrailingSpacesOnDelimiter(t *testing.T) {
	fm, _, err := Split([]byte("---  \nid: X\n--- \nbody\n"))
	require.NoError(t, err)
	require.Equal(t, "id: X\n", string(fm))
}

func TestSplit_EmptyFrontmatter(t *testing.T) {
	fm, body, err := Split([]byte("---\n---\nbody\n"))
	require.NoError(t, err)
	require.Empty(t, fm)
	require.Equal(t, "body\n", string(body))
}

func TestSplit_Rejects(t *testing.T) {
	cases := map[string]string{
		"no leading delimiter": "# Title\n\nbody\n",
		"empty":                "",
		"delimiter only":       "---",
		"unclosed":             "---\nid: X\nstill going\n",
		"leading blank line":   "\n---\nid: X\n---\n",
		"indented delimiter":   "  ---\nid: X\n---\n",
		"four dashes":          "----\nid: X\n----\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := Split([]byte(doc))
			require.ErrorIs(t, err, ErrNoFrontmatter)
		})
	}
}

// TestSplit_TruncatedReadIsNotHalfADocument is the guard that makes an
// 8 KB-capped scan safe: a read that stops before the closing delimiter
// must fail, never hand back the partial block as if it were complete.
func TestSplit_TruncatedReadIsNotHalfADocument(t *testing.T) {
	full := []byte("---\nstory_id: 1-1\nepic: 1\n---\n\nbody\n")
	_, _, err := Split(full[:20])
	require.ErrorIs(t, err, ErrNoFrontmatter)

	fm, _, err := Split(full)
	require.NoError(t, err)
	require.Contains(t, string(fm), "story_id: 1-1")
}

// TestSplit_BodyDelimiterDoesNotReopen: a `---` horizontal rule in the
// body belongs to the body, because the first closing delimiter wins.
func TestSplit_BodyDelimiterDoesNotReopen(t *testing.T) {
	fm, body, err := Split([]byte("---\nid: X\n---\nintro\n\n---\n\nmore\n"))
	require.NoError(t, err)
	require.Equal(t, "id: X\n", string(fm))
	require.Equal(t, "intro\n\n---\n\nmore\n", string(body))
}

func TestSplit_ReturnsSubslicesNotCopies(t *testing.T) {
	data := []byte("---\nid: X\n---\nbody\n")
	fm, body, err := Split(data)
	require.NoError(t, err)
	require.Same(t, &data[4], &fm[0], "frontmatter aliases the input")
	require.Same(t, &data[14], &body[0], "body aliases the input")
}

func TestHas(t *testing.T) {
	require.True(t, Has([]byte("---\nid: X\n---\n")))
	require.False(t, Has([]byte("# not a record\n")))
}
