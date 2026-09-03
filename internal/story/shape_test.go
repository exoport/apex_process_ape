package story

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/stretchr/testify/require"
)

func writeStory(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func head(status string) string {
	return "---\nstory_id: 1-1\nepic: 1\nstatus: " + status +
		"\noutput_document: x.md\n---\n"
}

func checkNames(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Check)
	}
	return out
}

// --- the derived section set ------------------------------------------

// TestDerivedSections_IsAFunctionOfConfigAlone is O-5 made executable: no
// writer stamps a story type, so the set cannot depend on one — and the
// signature still takes one, so a later key narrows the set without
// changing the exit contract.
func TestDerivedSections_IsAFunctionOfConfigAlone(t *testing.T) {
	plain := DerivedSections(apexcfg.Ext{}, "")
	require.Equal(t, plain, DerivedSections(apexcfg.Ext{}, "frontend"),
		"no story type exists to read, so declaring one must change nothing today")

	require.NotContains(t, plain, "## UX Specification",
		"UX Specification is the template's frontend-story section — exactly the "+
			"type-dependent judgement no frontmatter key records")
	require.Contains(t, plain, SecStory)
	require.Contains(t, plain, SecChangeLog)
}

func TestDerivedSections_ExtensionGating(t *testing.T) {
	require.NotContains(t, DerivedSections(apexcfg.Ext{}, ""), SecGCC)

	adrs := DerivedSections(apexcfg.Ext{ADRs: true}, "")
	require.Contains(t, adrs, SecGCC)
	require.Contains(t, adrs, SecGovernance)
	require.Contains(t, adrs, SecADRTable)
	require.NotContains(t, adrs, SecPatternTable)

	pats := DerivedSections(apexcfg.Ext{Patterns: true}, "")
	require.Contains(t, pats, SecGCC, "either governance extension emits the GCC header")
	require.Contains(t, pats, SecPatternTable)
	require.NotContains(t, pats, SecADRTable)
}

// TestDerivedSections_FeatureScopeIsOnExtFeaturesAlone settles the
// disagreement PLAN-64 6.3b resolves: the template says always emit, the
// governance resource says omit when zero matched. The plan decides for
// the template — the sentinel line is "no matches", never a missing
// section.
func TestDerivedSections_FeatureScopeIsOnExtFeaturesAlone(t *testing.T) {
	require.Contains(t, DerivedSections(apexcfg.Ext{Features: true}, ""), SecFeatureScope)
	require.NotContains(t, DerivedSections(apexcfg.Ext{ADRs: true}, ""), SecFeatureScope)
}

func TestCheckSections_MissingHeaderIsAFinding(t *testing.T) {
	dir := t.TempDir()
	// The acceptance fixture's shape: story 1.1 with one heading removed.
	body := strings.Replace(conformingBody, "### Debug Log References\n\n_No issues encountered._\n", "", 1)
	path := writeStory(t, dir, "1-1.md", head("review")+body)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Contains(t, checkNames(verdict.Findings), CheckSectionMissing)

	var named bool
	for _, f := range verdict.Findings {
		if f.Check == CheckSectionMissing && f.Field == SecDebugLog {
			named = true
			require.Contains(t, f.Message, SecDebugLog,
				"the message must name the missing derived section")
		}
	}
	require.True(t, named)
}

// TestCheckSections_EmptySectionIsAccepted — the invariant is "an empty
// section is accepted; a missing header is not".
func TestCheckSections_EmptySectionIsAccepted(t *testing.T) {
	dir := t.TempDir()
	emptied := strings.Replace(conformingBody, "Notes.\n", "", 1)
	path := writeStory(t, dir, "1-1.md", head("done")+emptied)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileOK, verdict.Code, "findings: %+v", verdict.Findings)
}

// --- File List markers -------------------------------------------------

// TestCheckFileList_AllFiveMarkers is the correction to the intake's
// three-marker list: (planned) and (deferred) are apex-lift-project's,
// and rejecting them would report a finding on every lifted story.
func TestCheckFileList_AllFiveMarkers(t *testing.T) {
	dir := t.TempDir()
	lines := make([]string, 0, len(FileListMarkers))
	for _, marker := range FileListMarkers {
		lines = append(lines, "- `internal/x/"+marker+".go` ("+marker+") — a note")
	}
	entries := "\n" + strings.Join(lines, "\n") + "\n"
	body := strings.Replace(conformingBody,
		"### File List\n\n_(populated during dev)_\n",
		"### File List\n"+entries, 1)
	path := writeStory(t, dir, "1-1.md", head("done")+body)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileOK, verdict.Code, "findings: %+v", verdict.Findings)
}

func TestCheckFileList_BareEntryIsAFinding(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(conformingBody,
		"### File List\n\n_(populated during dev)_\n",
		"### File List\n\n- `internal/x/thing.go`\n", 1)
	path := writeStory(t, dir, "1-1.md", head("done")+body)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Contains(t, checkNames(verdict.Findings), CheckFileListMarker)
}

// TestCheckFileList_EmDashProseIsForbiddenAsASubstitute — em-dash prose
// is a legal annotation AFTER a marker and never in place of one.
func TestCheckFileList_EmDashProseIsForbiddenAsASubstitute(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(conformingBody,
		"### File List\n\n_(populated during dev)_\n",
		"### File List\n\n- `internal/x/thing.go` — created: the thing\n", 1)
	path := writeStory(t, dir, "1-1.md", head("done")+body)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Contains(t, verdict.Findings[0].Message, "em-dash-prose form is forbidden")
}

func TestCheckFileList_UnknownMarkerIsAFinding(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(conformingBody,
		"### File List\n\n_(populated during dev)_\n",
		"### File List\n\n- `internal/x/thing.go` (touched)\n", 1)
	path := writeStory(t, dir, "1-1.md", head("done")+body)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Contains(t, verdict.Findings[0].Message, "(touched)")
}

// --- placeholder residue ----------------------------------------------

// TestCheckPlaceholders_OnlyAtStatusReview — at ready-for-dev those
// placeholders are exactly what the template is required to write.
func TestCheckPlaceholders_OnlyAtStatusReview(t *testing.T) {
	dir := t.TempDir()

	fresh := writeStory(t, dir, "fresh.md", head("ready-for-dev")+conformingBody)
	require.Equal(t, FileOK, VerifyFile(fresh, apexcfg.Ext{}).Code,
		"asserting non-placeholder at mint inverts the template's own contract")

	inReview := writeStory(t, dir, "review.md", head("review")+conformingBody)
	verdict := VerifyFile(inReview, apexcfg.Ext{})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Contains(t, checkNames(verdict.Findings), CheckPlaceholderResidue)
}

// TestCheckPlaceholders_DebugLogTerminalIsNotResidue — its writer treats
// "no issues" as a complete answer, so reporting it would fail every
// clean story.
func TestCheckPlaceholders_DebugLogTerminalIsNotResidue(t *testing.T) {
	dir := t.TempDir()
	body := conformingBody
	for _, section := range []string{
		"### Agent Model Used\n\n_(populated during dev)_\n",
		"### File List\n\n_(populated during dev)_\n",
		"### Completion Notes List\n\n_(populated during dev)_\n",
	} {
		replacement := strings.Replace(section, Placeholder, "claude-opus-5", 1)
		body = strings.Replace(body, section, replacement, 1)
	}
	require.Contains(t, body, DebugLogTerminal, "the terminal convention stays")

	path := writeStory(t, dir, "1-1.md", head("review")+body)
	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileOK, verdict.Code, "findings: %+v", verdict.Findings)
}

// --- compliance tables -------------------------------------------------

func TestCheckComplianceTables_HeaderShape(t *testing.T) {
	dir := t.TempDir()

	ok := writeStory(t, dir, "ok.md",
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n"+
			"governance:\n  adrs: []\n---\n"+governanceBody)
	require.Equal(t, FileOK, VerifyFile(ok, apexcfg.Ext{ADRs: true}).Code)

	wrong := strings.Replace(governanceBody,
		"| ADR | Why it applies | Key constraints |",
		"| ADR | Title | Notes |", 1)
	bad := writeStory(t, dir, "bad.md",
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n"+
			"governance:\n  adrs: []\n---\n"+wrong)
	verdict := VerifyFile(bad, apexcfg.Ext{ADRs: true})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Contains(t, checkNames(verdict.Findings), CheckComplianceTableHeader)
}

// TestCheckComplianceTables_ToleratesFormatterPadding — Prettier aligns
// these tables to their widest cell, so a byte comparison would fail
// every formatted story.
func TestCheckComplianceTables_ToleratesFormatterPadding(t *testing.T) {
	dir := t.TempDir()
	padded := strings.Replace(governanceBody,
		"| ADR | Why it applies | Key constraints |",
		"| ADR      | Why it applies   | Key constraints    |", 1)
	path := writeStory(t, dir, "1-1.md",
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n"+
			"governance:\n  adrs: []\n---\n"+padded)

	require.Equal(t, FileOK, VerifyFile(path, apexcfg.Ext{ADRs: true}).Code)
}

// --- GCC line form -----------------------------------------------------

func TestCheckGCCLines_EmDashSeparatorIsAFinding(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(governanceBody,
		"- [ ] **[ADR-0001]** Verify the thing is wired -- `internal/**`",
		"- [ ] **[ADR-0001]** Verify the thing is wired — `internal/**`", 1)
	path := writeStory(t, dir, "1-1.md",
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n"+
			"governance:\n  adrs: []\n---\n"+body)

	verdict := VerifyFile(path, apexcfg.Ext{ADRs: true})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Contains(t, verdict.Findings[0].Message, "double hyphen")
}

func TestCheckGCCLines_PATCANIsNeverValid(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(governanceBody, "[ADR-0001]", "[PATCAN-0001]", 1)
	path := writeStory(t, dir, "1-1.md",
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n"+
			"governance:\n  adrs: []\n---\n"+body)

	verdict := VerifyFile(path, apexcfg.Ext{ADRs: true})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Contains(t, verdict.Findings[0].Message, "PATCAN")
}

// TestCheckGCCLines_ReviewMarkingsAreLegal — review ticks the box or
// writes `N/A: <reason>`, and both are markings of the same line rather
// than malformed ones.
func TestCheckGCCLines_ReviewMarkingsAreLegal(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(governanceBody,
		"- [ ] **[ADR-0001]** Verify the thing is wired -- `internal/**`",
		"- [x] **[ADR-0001]** Verify the thing is wired -- `internal/**`\n"+
			"- [x] **[PATLOC-0031]** N/A: nothing in scope was touched", 1)
	path := writeStory(t, dir, "1-1.md",
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n"+
			"governance:\n  adrs: []\n---\n"+body)

	verdict := VerifyFile(path, apexcfg.Ext{ADRs: true})
	require.Equal(t, FileOK, verdict.Code, "findings: %+v", verdict.Findings)
}

// --- the exit contract -------------------------------------------------

// TestVerifyFile_KeyProblemWinsOverShapeProblem — 3 is the more
// fundamental failure and the calling prose routes on it.
func TestVerifyFile_KeyProblemWinsOverShapeProblem(t *testing.T) {
	dir := t.TempDir()
	// Missing output_document AND a body with no sections at all.
	path := writeStory(t, dir, "1-1.md",
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\n---\n\nnothing here\n")

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileKeyProblem, verdict.Code)
	require.Equal(t, []string{CheckRequiredKeyMissing}, checkNames(verdict.Findings),
		"the body classes do not run until the keys are known good")
}

// TestCheckFileList_UnbackticedEntryIsStillSeen — the canonical entry
// wraps its path in backticks, but an entry that forgot to is still an
// entry. Skipping it would let the commonest malformed line — a bare
// path with no marker — pass unexamined, which is the opposite of what
// this class is for.
func TestCheckFileList_UnbackticedEntryIsStillSeen(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(conformingBody,
		"### File List\n\n_(populated during dev)_\n",
		"### File List\n\n- internal/x/thing.go\n", 1)
	path := writeStory(t, dir, "1-1.md", head("done")+body)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Equal(t, CheckFileListMarker, verdict.Findings[0].Check)
	require.Equal(t, "internal/x/thing.go", verdict.Findings[0].Field)
}

// TestCheckFileList_UnbackticedEntryWithAMarkerPasses — missing
// backticks are not this class's finding; the marker vocabulary is.
func TestCheckFileList_UnbackticedEntryWithAMarkerPasses(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(conformingBody,
		"### File List\n\n_(populated during dev)_\n",
		"### File List\n\n- internal/x/thing.go (created)\n", 1)
	path := writeStory(t, dir, "1-1.md", head("done")+body)

	require.Equal(t, FileOK, VerifyFile(path, apexcfg.Ext{}).Code)
}

// TestCheckFileList_PlaceholderIsNotAnEntry — before status: review the
// template's placeholder is what the File List is required to hold, and
// it must not be read as a malformed entry.
func TestCheckFileList_PlaceholderIsNotAnEntry(t *testing.T) {
	dir := t.TempDir()
	path := writeStory(t, dir, "1-1.md", head("ready-for-dev")+conformingBody)
	require.Equal(t, FileOK, VerifyFile(path, apexcfg.Ext{}).Code)
}

// --- fenced code blocks are examples, not content ----------------------

// templateFileListFence is the story template's own "Canonical entry
// shape" block, verbatim. It is written into every minted story and
// `apex-dev-story` often leaves it in place, so reading it as an entry
// reported `(marker)` as an unknown marker on 51 of 60 fixture stories,
// 140 of one real project's 483 and 2 of another's 296.
const templateFileListFence = "\n" +
	"**Status-marker vocabulary** — every populated entry MUST carry exactly one marker.\n" +
	"\n" +
	"| Marker       | Meaning                     |\n" +
	"| ------------ | --------------------------- |\n" +
	"| `(created)`  | Newly created in this story |\n" +
	"\n" +
	"Canonical entry shape:\n" +
	"\n" +
	"```markdown\n" +
	"- `path/to/file.ext` (marker) — short note\n" +
	"```\n" +
	"\n" +
	"- `internal/x/thing.go` (created) — the real entry\n"

func TestStripFences_TemplateFileListExampleIsNotAnEntry(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(conformingBody,
		"### File List\n\n_(populated during dev)_\n",
		"### File List\n"+templateFileListFence, 1)
	path := writeStory(t, dir, "1-1.md", head("done")+body)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileOK, verdict.Code,
		"the template's own example must not be reported against the story: %+v",
		verdict.Findings)
}

// TestStripFences_EveryStructuralClass — a fenced example of ANY body
// shape can appear in Dev Notes, so the strip is not File-List-specific.
func TestStripFences_EveryStructuralClass(t *testing.T) {
	dir := t.TempDir()
	fenced := "\n```markdown\n" +
		"## Story\n" +
		"### Governance Compliance Criteria\n" +
		"- [ ] **[PATCAN-0001]** a bad id — with an em-dash separator\n" +
		"| ADR | Title | Notes |\n" +
		"- bare/path/with/no/marker.go\n" +
		Placeholder + "\n" +
		"```\n"
	// status: review with the real placeholders filled in, so the only
	// candidate for a placeholder finding is the FENCED one.
	body := conformingBody
	for _, section := range []string{
		"### Agent Model Used\n\n_(populated during dev)_\n",
		"### File List\n\n_(populated during dev)_\n",
		"### Completion Notes List\n\n_(populated during dev)_\n",
	} {
		body = strings.Replace(body, section,
			strings.Replace(section, Placeholder, "filled in", 1), 1)
	}
	body = strings.Replace(body, "Notes.\n", "Notes.\n"+fenced, 1)
	path := writeStory(t, dir, "1-1.md", head("review")+body)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileOK, verdict.Code,
		"no structural class may fire on a fenced example: %+v", verdict.Findings)
}

// TestStripFences_ARealDefectOutsideAFenceStillFires is the other half:
// the strip must not become a way to hide findings.
func TestStripFences_ARealDefectOutsideAFenceStillFires(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(conformingBody,
		"### File List\n\n_(populated during dev)_\n",
		"### File List\n"+templateFileListFence+"- bad/entry.go\n", 1)
	path := writeStory(t, dir, "1-1.md", head("done")+body)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Len(t, verdict.Findings, 1, "only the unfenced entry: %+v", verdict.Findings)
	require.Equal(t, "bad/entry.go", verdict.Findings[0].Field)
}

// TestStripFences_AFencedHeadingDoesNotSatisfyTheSectionSet — a fenced
// `## Change Log` is an example of one, not the story's own.
func TestStripFences_AFencedHeadingDoesNotSatisfyTheSectionSet(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(conformingBody,
		"## Change Log\n\n| Date | Change |\n| ---- | ------ |\n",
		"```markdown\n## Change Log\n```\n", 1)
	path := writeStory(t, dir, "1-1.md", head("done")+body)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Equal(t, CheckSectionMissing, verdict.Findings[0].Check)
	require.Equal(t, SecChangeLog, verdict.Findings[0].Field)
}

func TestStripFences_Mechanics(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"tilde fence":                  {"a\n~~~\nhidden\n~~~\nb", "a\nb"},
		"info string":                  {"a\n```go\nhidden\n```\nb", "a\nb"},
		"indented up to three":         {"a\n   ```\nhidden\n   ```\nb", "a\nb"},
		"indented four is not a fence": {"a\n    ```\nb", "a\n    ```\nb"},
		"longer inner run stays open": {
			"a\n````\n```\nstill hidden\n````\nb", "a\nb",
		},
		"tilde does not close a backtick fence": {
			"a\n```\nhidden\n~~~\nalso hidden\n```\nb", "a\nb",
		},
		"unclosed fence runs to the end": {"a\n```\nhidden", "a"},
		"inline code is not a fence":     {"a `x` b", "a `x` b"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, stripFences(tc.in))
		})
	}
}
