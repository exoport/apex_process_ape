package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// L8, from radix: its ADR-NN comments cite a research repository's
// decision log, "not renumbered onto this repository's own governance
// identifiers". Only the project's own form, ADR-NNNN, is rewritten; a
// short id is listed, even when the project has a record of that number.
func TestADRRewriter_OnlyTheProjectsOwnForm(t *testing.T) {
	rw := newADRRewriter(map[int]int{1: 1, 8: 2}, nil, "")
	out, n, unresolved := rw.rewrite("Per ADR-08 and ADR-01/05, ADR-18 (research); ours: ADR-0008, ADR-0001/0008, ADR-0099.")
	require.Equal(t, "Per ADR-08 and ADR-01/05, ADR-18 (research); ours: ADR-0002, ADR-0001/0002, ADR-0099.", out)
	require.Equal(t, 2, n)
	require.Equal(t, []string{"ADR-08", "ADR-01", "ADR-18", "ADR-0099"}, unresolved)
	require.Equal(t, []int{8, 1, 8, 99}, citedADRs("ADR-08 ADR-0008 ADR-0001/0008 ADR-0099 ADR-7"), "short forms are no citation of the project's")
}

func TestEditComments_ShortADRIdsListedNotRewritten(t *testing.T) {
	rw := newADRRewriter(map[int]int{8: 2}, nil, "")
	src := "package x\n\n// Follows ADR-08 (research log), unlike ADR-0008.\nvar x = 1\n"
	res, err := editComments("x.go", src, langGo, testVocab(), rw)
	require.NoError(t, err)
	require.Equal(t, "package x\n\n// Follows ADR-08 (research log), unlike ADR-0002.\nvar x = 1\n", res.New)
	require.Equal(t, []LiftIDRef{{File: "x.go", Line: 3, Text: "ADR-08", Where: "comment"}}, res.Unresolved)
}

// L9, from the dry runs (radix, atomix, tenax, cortex): the punctuation
// around a removed id goes with it, and so do the story's fragments.
func TestStripIDsFromText_PunctuationAndFragments(t *testing.T) {
	v := testVocab()
	for in, want := range map[string]string{
		" Bounded (ADR-0019, Story 3.2).":               " Bounded (ADR-0019).",
		" Ratchet (ADR-0180; Story 3.2) holds.":         " Ratchet (ADR-0180) holds.",
		" Carried (Story 3.2, ADR-0017 honesty":         " Carried (ADR-0017 honesty",
		" Per ADR-0011 / Story 4.2, Task 2: the gate":   " Per ADR-0011: the gate",
		" Lives in (tools/verify/, Story 3.2) for now":  " Lives in (tools/verify/) for now",
		" Retry (Story 3.2, Task 2) twice.":             " Retry twice.",
		" Story 5.10 Subtask 3.1: paginate the trace":   " Paginate the trace",
		" Task 2 of Story 3.2 adds a retry.":            " Adds a retry.",
		" Story 3.2 AC 2 and AC 3: bounded":             " Bounded",
		" Retries a, Story 3.2, b and c.":               " Retries a, b and c.",
		" Bounded (ADR-0019 and Story 3.2) on purpose.": " Bounded (ADR-0019) on purpose.",
		" Task 2 stays: no story next to it (Epic 4).":  " Task 2 stays: no story next to it.",
		" Subtask 3.1 of the plan, not of a story id.":  " Subtask 3.1 of the plan, not of a story id.",
	} {
		spans := v.spans(in)
		got, empty := stripIDsFromText(in, spans)
		if len(spans) == 0 {
			got = in
		}
		require.False(t, empty, in)
		require.Equal(t, want, got, in)
	}
}

// R2, from cortex, radix and tenax on rc.3: "This file is Story 15.4's
// own deliverable" became "This file is 's own deliverable" (1,941 lines
// in cortex). A possessive id is listed and its comment left as written,
// its ADR citations still renumbered.
func TestEditComments_PossessiveIDListedNotEdited(t *testing.T) {
	rw := newADRRewriter(map[int]int{17: 3}, nil, "")
	src := "package x\n\n// This file is Story 15.4's own deliverable (ADR-0017).\n// Mirrors Story 15.2’s own generate_test.go.\n// Retry (Story 3.2).\nvar x = 1\n"
	res, err := editComments("x.go", src, langGo, testVocab(), rw)
	require.NoError(t, err)
	require.Equal(t, "package x\n\n// This file is Story 15.4's own deliverable (ADR-0003).\n// Mirrors Story 15.2’s own generate_test.go.\n// Retry.\nvar x = 1\n", res.New)
	require.Equal(t, []LiftIDRef{
		{File: "x.go", Line: 3, Text: "Story 15.4", Where: "possessive"},
		{File: "x.go", Line: 4, Text: "Story 15.2", Where: "possessive"},
	}, res.Left)
	require.Equal(t, 1, res.IDsRemoved)
	require.Equal(t, 1, res.ADRsRewritten)
}

// R2: an aside that opens on one comment line and closes on another lost
// one bracket — 26–34 comment lines per project began with an orphaned
// ")" on rc.3. A "(" whose id went goes with its ")", across lines.
func TestEditComments_ParenthesesAcrossLines(t *testing.T) {
	v := testVocab()
	for _, c := range []struct {
		lang     liftLang
		in, want string
	}{
		{langGo, "// Bounded (Story 3.2,\n// Task 4) holds.\n", "// Bounded\n// Task 4 holds.\n"},
		{langGo, "// Bounded backoff (see\n// Story 3.2)\n", "// Bounded backoff\n"},
		{langGo, "// Bounded backoff (Story 3.2\n// and Story 3.3)\n", "// Bounded backoff\n"},
		{langGo, "// Bounded backoff (\n// Story 3.2, Task 1\n// )\n", "// Bounded backoff\n"},
		{langGo, "// Bounded backoff\n// (Story 3.2,\n// the retry bound).\n", "// Bounded backoff\n// the retry bound.\n"},
		{langGo, "// Bounded backoff (Story 3.2 — the\n// retry bound)\n", "// Bounded backoff (the\n// retry bound)\n"},
		{langGo, "// Steps: 1) parse (Story 3.2,\n// Task 4) then 2) run.\n", "// Steps: 1) parse\n// Task 4 then 2) run.\n"},
		{langShell, "# Bounded (Story 3.2,\n# Epic 4)\n# keeps it.\n", "# Bounded\n# keeps it.\n"},
		// A run no id was stripped from is never touched.
		{langGo, "// Bounded (\n// )\n", "// Bounded (\n// )\n"},
	} {
		head := "package x\n\n"
		if c.lang == langShell {
			head = "#!/bin/sh\n"
		}
		res, err := editComments("x", head+c.in+"var x = 1\n", c.lang, v, nil)
		require.NoError(t, err, c.in)
		require.Equal(t, head+c.want+"var x = 1\n", res.New, c.in)
		for l := range strings.SplitSeq(res.New, "\n") {
			if c.in != c.want {
				require.NotRegexp(t, `^\s*(?://|#)\s*\)`, l, c.in)
			}
		}
	}
}

// liftGo runs the comment edit on a Go file holding comment, and returns
// the edited comment.
func liftGo(t *testing.T, comment string) (string, *liftFileEdit) {
	t.Helper()
	const head, tail = "package x\n\n", "var x = 1\n"
	res, err := editComments("x.go", head+comment+tail, langGo, testVocab(), nil)
	require.NoError(t, err, comment)
	return strings.TrimSuffix(strings.TrimPrefix(res.New, head), tail), res
}

// R7, radix's internal/render/token.go on rc.4: the aside's removal left
// "//. Every other", which gocritic's commentFormatting fails, and radix's
// lint went red in both arms. The "." goes to the end of the line above.
func TestEditComments_LeadingMarkMovesToTheLineAbove(t *testing.T) {
	got, _ := liftGo(t, "// {[a-z_]+}-shaped pattern used to REPLACE anything (Story 3.7 AC1/AC3;\n"+
		"// Story 7.2 AC2). Every other braced literal in the authored tree — most\n")
	require.Equal(t, "// {[a-z_]+}-shaped pattern used to REPLACE anything.\n"+
		"// Every other braced literal in the authored tree — most\n", got)

	for in, want := range map[string]string{
		// The line above ends a sentence already.
		"// Retries twice.\n// (Story 3.2). Then gives up.\n": "// Retries twice.\n// Then gives up.\n",
		// A blank comment line above: nothing to carry it to.
		"//\n// (Story 3.2). Then gives up.\n": "//\n// Then gives up.\n",
		// No line above in the run.
		"// (Story 3.2). Then gives up.\n": "// Then gives up.\n",
		// A line that began on a mark at the tag is left as written.
		"// Retries twice\n// ... then gives up (Story 3.2).\n": "// Retries twice\n// ... then gives up.\n",
	} {
		got, _ := liftGo(t, in)
		require.Equal(t, want, got, in)
	}
}

// R8, from every rc.4 dry run (26–41 lines per project): an aside opening
// on the line above with other content and a separator left "// ),
// extracted …". The ")" goes to the line above, and the separators go.
func TestEditComments_OrphanCloserMovesToTheLineAbove(t *testing.T) {
	got, _ := liftGo(t, "// runBootstrap runs the mandatory pre-substrate bootstrap chain (ADR-0008,\n"+
		"// Story 1.3), extracted from run()'s RunE closure to keep that closure's\n")
	require.Equal(t, "// runBootstrap runs the mandatory pre-substrate bootstrap chain (ADR-0008)\n"+
		"// extracted from run()'s RunE closure to keep that closure's\n", got)
	require.NotRegexp(t, `(?m)^//\s*\)`, got)
}

// R9, present since rc.3: every "()" on an edited line went — "run()'s"
// became "run's". Only a pair the removal emptied goes.
func TestEditComments_OnlyAnEmptiedPairGoes(t *testing.T) {
	got, _ := liftGo(t, "// Retries (Story 3.2) after run()'s RunE closure; main() now preflights\n"+
		"// plus KeyValue() when kv is set (Story 3.3): LaneConsumer.Lane() string.\n"+
		"// Returns a []byte, never nil (Story 3.2); ( ) stays too.\n"+
		"// Bounded ([Story 3.2]) twice.\n")
	require.Equal(t, "// Retries after run()'s RunE closure; main() now preflights\n"+
		"// plus KeyValue() when kv is set: LaneConsumer.Lane() string.\n"+
		"// Returns a []byte, never nil; ( ) stays too.\n"+
		"// Bounded twice.\n", got)
}

// R10, tenax, cortex and radix on rc.4: a removed path's possessive left
// "('s Gates (AC6) paragraph". It is listed and left as written, as R2's
// story-id possessive is.
func TestEditComments_PathPossessiveListedNotEdited(t *testing.T) {
	in := "// Mirrors (development/deferred/'s Gates (AC6) paragraph).\n" +
		"// Reads development/implementation/sprint-status.yaml's `updated_at` (Story 3.2).\n" +
		"// Keep in step with development/deferred/DW-42_x.md.\n"
	got, res := liftGo(t, in)
	require.Equal(t, "// Mirrors (development/deferred/'s Gates (AC6) paragraph).\n"+
		"// Reads development/implementation/sprint-status.yaml's `updated_at` (Story 3.2).\n"+
		"// Keep in step.\n", got)
	require.Equal(t, []LiftIDRef{
		{File: "x.go", Line: 3, Text: "development/deferred/", Where: "possessive"},
		{File: "x.go", Line: 4, Text: "development/implementation/sprint-status.yaml", Where: "possessive"},
		{File: "x.go", Line: 4, Text: "Story 3.2", Where: "possessive"},
	}, res.Left)
	require.Equal(t, 1, res.IDsRemoved)
}

// R7–R10 on one paragraph: the four rules edit the same lines, so they
// must not undo each other, and R2's rules still hold beside them.
func TestEditComments_CommentRulesCompose(t *testing.T) {
	got, res := liftGo(t, "// runBootstrap runs the bootstrap chain (ADR-0008,\n"+
		"// Story 1.3), extracted from run()'s RunE closure; main() now\n"+
		"// preflights, plus KeyValue() when kv is set (Story 3.7 AC1/AC3;\n"+
		"// Story 7.2 AC2). Every other caller uses LaneConsumer.Lane() string\n"+
		"// (see\n"+
		"// Story 3.2), as development/deferred/'s Gates paragraph says.\n"+
		"// Bounded (Story 3.2,\n"+
		"// Task 4) holds.\n")
	require.Equal(t, "// runBootstrap runs the bootstrap chain (ADR-0008)\n"+
		"// extracted from run()'s RunE closure; main() now\n"+
		"// preflights, plus KeyValue() when kv is set.\n"+
		"// Every other caller uses LaneConsumer.Lane() string\n"+
		"// (see\n"+
		"// Story 3.2), as development/deferred/'s Gates paragraph says.\n"+
		"// Bounded\n"+
		"// Task 4 holds.\n", got)
	for l := range strings.SplitSeq(got, "\n") {
		if strings.Contains(l, "development/deferred/'s") {
			continue // kept as written
		}
		require.NotRegexp(t, `^//\S|^//\s*\)`, l)
	}
	require.Contains(t, res.Left, LiftIDRef{File: "x.go", Line: 8, Text: "development/deferred/", Where: "possessive"})
}
