package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func testVocab() *liftVocab {
	return newLiftVocab(map[string]bool{"3-2": true, "35-1": true}, []string{"development/deferred", "development/implementation"})
}

func TestStripIDsFromText(t *testing.T) {
	v := testVocab()
	for in, want := range map[string]string{
		" Story 3.2: retry the publish after a failure.":          " Retry the publish after a failure.",
		" Retry the publish (Story 3.2).":                         " Retry the publish.",
		" Retry the publish (see DW-20261001-ab12_lost-result).":  " Retry the publish.",
		" Retry the publish — story 35.1":                         " Retry the publish",
		" Retry the publish, see DW-42.":                          " Retry the publish.",
		" Introduced in Story 3.2 to avoid a storm.":              " Introduced to avoid a storm.",
		" Retries twice. See Epic 12. Then gives up.":             " Retries twice. Then gives up.",
		" Stories 3.1 and 3.2 AC4: bounded backoff":               " Bounded backoff",
		" Bounded backoff (3-2).":                                 " Bounded backoff.",
		" Ranges are (0-1) inclusive.":                            " Ranges are (0-1) inclusive.",
		" Keep in step with development/implementation/3-2_x.md.": " Keep in step.",
		" Bounded. See DW-42 for the history. Then retry.":        " Bounded. Then retry.",
		" An epic 12-hour soak.":                                  " An epic 12-hour soak.",
		" History2 is not a story.":                               " History2 is not a story.",
	} {
		spans := v.spans(in)
		got, empty := stripIDsFromText(in, spans)
		if len(spans) == 0 {
			got = in
		}
		require.False(t, empty, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{" Story 3.2", " (Story 3.2)", " DW-42.", " - Epic 12", " Story 3.2, story 3.3 and Epic 4", " See DW-42.", " See development/deferred/DW-42_x.md for the history."} {
		_, empty := stripIDsFromText(in, v.spans(in))
		require.True(t, empty, in)
	}
}

func TestEditComments_GoCommentOnly(t *testing.T) {
	v := testVocab()
	adr := newADRRewriter(map[int]int{17: 3, 3: 1}, nil, "")
	src := "// Package x does things (Story 3.2).\npackage x\n\n" +
		"// Story 35.1\n" +
		"func TestStory35_1Retry() {} // Epic 12\n\n" +
		"//go:generate echo Story 3.2\n" +
		"/* DW-42: keep this.\n * Story 3.2\n */\n" +
		"var s = \"Story 3.2 in a string\" // see ADR-0017 and ADR-0003; ADR-0099\n" +
		"var x = 1 /* Epic 4 */ + 2\n"
	res, err := editComments("x.go", src, langGo, v, adr)
	require.NoError(t, err)
	want := "// Package x does things.\npackage x\n\n" +
		"func TestStory35_1Retry() {}\n\n" +
		"//go:generate echo Story 3.2\n" +
		"/* Keep this.\n */\n" +
		"var s = \"Story 3.2 in a string\" // see ADR-0003 and ADR-0001; ADR-0099\n" +
		"var x = 1 + 2\n"
	require.Equal(t, want, res.New)
	require.NoError(t, verifyCommentOnly(langGo, src, res.New))
	where := make([]string, 0, len(res.Left))
	for _, l := range res.Left {
		where = append(where, l.Where+":"+l.Text)
	}
	require.Contains(t, where, "identifier:Story35")
	require.Contains(t, where, "string:Story 3.2")
	require.Contains(t, where, "directive:Story 3.2")
	require.Equal(t, 2, res.ADRsRewritten)
	require.Len(t, res.Unresolved, 1)
	require.Equal(t, "ADR-0099", res.Unresolved[0].Text)
}

func TestEditComments_OtherLanguages(t *testing.T) {
	v := testVocab()
	cases := []struct {
		name string
		lang liftLang
		src  string
		want string
	}{
		{
			"shell", langShell,
			"#!/bin/sh\n# Story 3.2: build it\necho \"# Story 3.2\" # Epic 4\ncat <<EOF\n# Story 3.2 stays\nEOF\nx=${y#Story 3.2}\n",
			"#!/bin/sh\n# Build it\necho \"# Story 3.2\"\ncat <<EOF\n# Story 3.2 stays\nEOF\nx=${y#Story 3.2}\n",
		},
		{
			"shell continuation", langShell,
			"run \\\n# Story 3.2\n  --flag\n",
			"run \\\n# Story 3.2\n  --flag\n",
		},
		{
			"python", langPython,
			"def f():\n    # Story 3.2\n    s = '''# Story 3.2'''  # retry (DW-42)\n    return s\n",
			"def f():\n    s = '''# Story 3.2'''  # retry\n    return s\n",
		},
		{
			"js", langJS,
			"const r = /\\/\\/ Story 3.2/; // Story 3.2: regex\nconst t = `a ${b /* Epic 1 */} // Story 3.2`;\n",
			"const r = /\\/\\/ Story 3.2/; // Regex\nconst t = `a ${b} // Story 3.2`;\n",
		},
		{
			"jsx", langJSX,
			"<a>http://x Story 3.2</a>\n// Story 3.2\n",
			"<a>http://x Story 3.2</a>\n",
		},
		{
			"dart", langDart,
			"/// Story 3.2: shows the form.\nfinal s = 'a ${b} // Story 3.2'; /* /* Epic 1 */ nested */\n",
			"/// Shows the form.\nfinal s = 'a ${b} // Story 3.2'; /* /* */ nested */\n",
		},
		{
			"yaml", langYAML,
			"# Story 3.2\nkey: value # Epic 4: the key\nrun: |\n  # Story 3.2 stays\n  echo hi\nq: \"a # Story 3.2\"\n",
			"key: value # The key\nrun: |\n  # Story 3.2 stays\n  echo hi\nq: \"a # Story 3.2\"\n",
		},
		{
			"make", langMake,
			"# Story 3.2: the build\nbuild:\n\t# Story 3.2 recipe\n\tgo build ./...\nX := $(subst #,x,y) # Epic 4\n",
			"# The build\nbuild:\n\t# Story 3.2 recipe\n\tgo build ./...\nX := $(subst #,x,y)\n",
		},
		{
			"c", langC,
			"int x = 1'000; // Story 3.2\nconst char *s = R\"(// Story 3.2)\"; /* Epic 4: why */\n",
			"int x = 1'000;\nconst char *s = R\"(// Story 3.2)\"; /* Why */\n",
		},
	}
	for _, c := range cases {
		res, err := editComments("f", c.src, c.lang, v, nil)
		require.NoError(t, err, c.name)
		require.Equal(t, c.want, res.New, c.name)
	}
}

func TestEditComments_Untokenizable(t *testing.T) {
	v := testVocab()
	for lang, src := range map[liftLang]string{
		langShell:  "cat <<EOF\nno end\n",
		langPython: "s = 'open\n",
		langC:      "// Story 3.2 \\\nint x;\n",
		langMake:   "# Story 3.2 \\\nX := 1\n",
		langJS:     "const s = `open\n",
		langGo:     "package x\nvar s = \"open\n",
	} {
		_, err := editComments("f", src, lang, v, nil)
		var ue *untokenizableError
		require.ErrorAs(t, err, &ue, lang.String())
	}
}

func TestIdentIDs(t *testing.T) {
	require.Equal(t, []string{"Story35"}, identIDs("TestStory35_1Retry"))
	require.Equal(t, []string{"epic12"}, identIDs("epic12Gate"))
	require.Empty(t, identIDs("History2"))
	require.Equal(t, []string{"ADR0017"}, identIDs("TestADR0017Honesty"))
}

// CRLF files: go/scanner strips carriage returns from comments and raw
// strings, so their ends come from the source; the edit keeps the CRs.
func TestEditComments_GoCRLF(t *testing.T) {
	src := "package x\r\n\r\n/* Story 3.2: why\r\n * Epic 4\r\n */\r\nvar s = `a\r\nb` // Story 3.2\r\nvar y = 1\r\n"
	res, err := editComments("x.go", src, langGo, testVocab(), nil)
	require.NoError(t, err)
	require.Equal(t, "package x\r\n\r\n/* Why\r\n */\r\nvar s = `a\r\nb`\r\nvar y = 1\r\n", res.New)
}
