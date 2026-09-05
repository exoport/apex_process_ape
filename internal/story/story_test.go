package story

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/stretchr/testify/require"
)

type fixture struct {
	t    *testing.T
	root string
	cfg  *apexcfg.Resolved
}

// newFixture builds a project with the four extensions active.
func newFixture(t *testing.T, extensions string) *fixture {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, apexcfg.DirName), 0o755))
	body := "config_schema_version: \"1\"\nproject_name: fx\nextensions: [" + extensions + "]\n" +
		"development_folder: development\nimplementation_folder: development/implementation\n" +
		"governance_folder: development/governance\nfunctionality_folder: development/functionality\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(root, apexcfg.DirName, apexcfg.BaseFile), []byte(body), 0o644,
	))
	cfg, err := apexcfg.ResolveAt(root, nil)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(cfg.Paths.Implementation, 0o755))
	return &fixture{t: t, root: root, cfg: cfg}
}

// story writes a story file with the given frontmatter body plus a
// generous prose body, so a body-reading implementation is measurably
// more expensive.
func (f *fixture) story(name, fm string) {
	f.t.Helper()
	body := "---\n" + fm + "---\n\n## Story\n\n" + strings.Repeat("prose ", 500)
	f.write(name, body)
}

func (f *fixture) write(name, body string) {
	f.t.Helper()
	path := filepath.Join(f.cfg.Paths.Implementation, name)
	require.NoError(f.t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(f.t, os.WriteFile(path, []byte(body), 0o644))
}

// record writes a governance/functionality record so referential checks
// have something to resolve against.
func (f *fixture) record(dir, name, id string) {
	f.t.Helper()
	require.NoError(f.t, os.MkdirAll(dir, 0o755))
	require.NoError(f.t, os.WriteFile(filepath.Join(dir, name),
		[]byte("---\nid: "+id+"\n---\n\nx\n"), 0o644))
}

// validStory is the frontmatter of a story that passes every check with
// all four extensions active.
const validStory = `story_id: 1-1
epic: 1
status: done
requirement_ids:
  - FR-1
output_document: development/implementation/1-1_thing.md
governance:
  adrs:
    - ADR-0001
  patterns:
    - PAT-0001
capabilities:
  - CAP-1
features:
  - id: FEAT-1-1
    contribution: creates
`

func (f *fixture) seedRecords() {
	f.record(f.cfg.Paths.ADRs, "adr-0001_x.md", "ADR-0001")
	f.record(f.cfg.Paths.Patterns, "pat-0001_x.md", "PAT-0001")
	f.record(f.cfg.Paths.Capabilities, "cap-1_x.md", "CAP-1")
	f.record(f.cfg.Paths.Features, "feat-1-1_x.md", "FEAT-1-1")
}

const allExts = "ext-adrs, ext-patterns, ext-capabilities, ext-features"

// gating returns the findings that decide a verdict.
//
// Advisory classes are excluded from a default corpus verify, so this is
// a no-op today. It stays as the explicit statement of what these tests
// assert on, and so that a class added to AdvisoryChecks later cannot
// quietly change what a count here means.
func gating(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if IsAdvisory(f.Check) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// --- Fields (D4) ---

// TestProject_NeverOpensABody is the test that catches the naive
// implementation: a story_id line planted deep in the BODY must not be
// mistaken for frontmatter, which proves both the 8 KiB cap and
// stop-at-closing-delimiter.
func TestProject_NeverOpensABody(t *testing.T) {
	f := newFixture(t, allExts)
	f.write("1-1_real.md",
		"---\nstory_id: 1-1\nepic: 1\n---\n\n"+
			strings.Repeat("filler line to push past any small read\n", 3000)+
			"\nstory_id: 9-9\nepic: 99\n")

	res, err := Project(f.cfg.Paths.Implementation, []string{"story_id", "epic"})
	require.NoError(t, err)
	require.Len(t, res.Stories, 1)
	require.Equal(t, "1-1", res.Stories[0].StoryID, "the body's story_id must not win")
	require.Equal(t, 1, res.Stories[0].Values["epic"])
	require.LessOrEqual(t, res.Trailer.BytesRead, FrontmatterCap,
		"one file must cost at most one capped read")
}

func TestProject_NoStoryIDIsNotAStory(t *testing.T) {
	f := newFixture(t, allExts)
	f.story("1-1_thing.md", validStory)
	f.write("epic-1-retro-20260101.md", "---\nepic: 1\ntype: retrospective\n---\n\nx\n")
	f.write("deferred-work.md", "# Deferred\n\nSee ../deferred/\n")

	res, err := Project(f.cfg.Paths.Implementation, []string{"story_id"})
	require.NoError(t, err)
	require.Equal(t, 3, res.Trailer.FilesScanned, "every .md is looked at")
	require.Equal(t, 1, res.Trailer.StoriesMatched, "only the one with story_id is a story")
	require.Empty(t, res.Warnings, "a file with no frontmatter is not a story, not a warning")
}

func TestProject_NestedBlockExtraction(t *testing.T) {
	f := newFixture(t, allExts)
	f.story("1-1_thing.md", validStory)

	res, err := Project(f.cfg.Paths.Implementation, []string{"governance", "features"})
	require.NoError(t, err)
	require.Len(t, res.Stories, 1)

	gov, ok := res.Stories[0].Values["governance"].(map[string]any)
	require.True(t, ok, "a nested block comes back whole")
	require.Contains(t, gov, "adrs")

	feats, ok := res.Stories[0].Values["features"].([]any)
	require.True(t, ok)
	require.Len(t, feats, 1)
}

// TestProject_FieldPresentInZeroStories is exit-0-with-a-true-answer: the
// caller asked a legitimate question.
func TestProject_FieldPresentInZeroStories(t *testing.T) {
	f := newFixture(t, allExts)
	f.story("1-1_thing.md", "story_id: 1-1\nepic: 1\n")

	res, err := Project(f.cfg.Paths.Implementation, []string{"story_id", "nonexistent_key"})
	require.NoError(t, err)
	require.Equal(t, 0, res.Trailer.PerFieldPresent["nonexistent_key"],
		"a field nobody has reports 0, not absence from the payload")
	require.Equal(t, 1, res.Trailer.PerFieldPresent["story_id"])
	require.NotContains(t, res.Stories[0].Values, "nonexistent_key",
		"and the column is empty rather than null-filled")
}

// TestProject_OneMalformedFileLosesOneFile is the degradation contract.
func TestProject_OneMalformedFileLosesOneFile(t *testing.T) {
	f := newFixture(t, allExts)
	f.story("1-1_ok.md", "story_id: 1-1\nepic: 1\n")
	f.write("1-2_broken.md", "---\nstory_id: [unterminated\nepic: 2\n---\n\nx\n")
	f.story("1-3_ok.md", "story_id: 1-3\nepic: 1\n")

	res, err := Project(f.cfg.Paths.Implementation, []string{"story_id"})
	require.NoError(t, err, "one bad file must not fail the run")
	require.Len(t, res.Stories, 2, "the other two are still returned")
	require.Len(t, res.Warnings, 1)
	require.Contains(t, res.Warnings[0], "1-2_broken.md")
}

// TestProject_OversizeFrontmatterIsReported: a block that does not close
// inside the cap is a finding, never a silent truncation.
func TestProject_OversizeFrontmatterIsReported(t *testing.T) {
	f := newFixture(t, allExts)
	f.write("1-1_huge.md", "---\nstory_id: 1-1\n"+
		strings.Repeat("padding_key_that_is_quite_long: value\n", 400)+"---\n\nbody\n")

	res, err := Project(f.cfg.Paths.Implementation, []string{"story_id"})
	require.NoError(t, err)
	require.Empty(t, res.Stories)
	require.Len(t, res.Warnings, 1)
	require.Contains(t, res.Warnings[0], "does not close within the first 8192 bytes")
}

func TestProject_CRLFAndBOM(t *testing.T) {
	f := newFixture(t, allExts)
	f.write("1-1_crlf.md", "---\r\nstory_id: 1-1\r\nepic: 1\r\n---\r\n\r\nbody\r\n")
	f.write("1-2_bom.md", "\xEF\xBB\xBF---\nstory_id: 1-2\nepic: 1\n---\n\nbody\n")

	res, err := Project(f.cfg.Paths.Implementation, []string{"story_id"})
	require.NoError(t, err)
	require.Len(t, res.Stories, 2)
	require.Empty(t, res.Warnings)
}

// TestProject_ReadCostIsBoundedByTheCap asserts the MECHANISM behind the
// 377x claim rather than the ratio itself.
//
// The bound is the code's property: at most one chunk read per file,
// never more than FrontmatterCap. The RATIO is the corpus's property —
// the reference project reaches 377x because its stories average tens of
// KB against a few hundred bytes of frontmatter, and this fixture's
// smaller bodies give a smaller multiple from the identical mechanism. So
// the tight assertion is on bytes-per-file, and the ratio only guards
// against a regression that starts reading bodies again.
func TestProject_ReadCostIsBoundedByTheCap(t *testing.T) {
	f := newFixture(t, allExts)
	const files = 465
	corpusBytes := 0
	for i := 1; i <= files; i++ {
		name := fmt.Sprintf("%d-%d_story.md", (i/10)+1, i%10)
		body := "---\nstory_id: " + fmt.Sprintf("%d-%d", (i/10)+1, i%10) +
			"\nepic: 1\nstatus: done\n---\n\n" + strings.Repeat("body prose\n", 4000)
		f.write(name, body)
		corpusBytes += len(body)
	}

	res, err := Project(f.cfg.Paths.Implementation, []string{"story_id", "epic", "status", "features"})
	require.NoError(t, err)
	require.Equal(t, files, res.Trailer.StoriesMatched)
	require.LessOrEqual(t, res.Trailer.BytesRead, files*chunkSize,
		"one chunk per file: %d bytes for %d files", res.Trailer.BytesRead, files)
	require.LessOrEqual(t, res.Trailer.BytesRead, files*FrontmatterCap,
		"and never past the cap")
	require.Greater(t, corpusBytes/max(res.Trailer.BytesRead, 1), 20,
		"an order-of-magnitude reduction, not a body read: %d vs %d", corpusBytes, res.Trailer.BytesRead)
}

func TestParseSelect(t *testing.T) {
	got, err := ParseSelect(" story_id , epic ,, story_id ")
	require.NoError(t, err)
	require.Equal(t, []string{"story_id", "epic"}, got, "trimmed and de-duplicated, order kept")

	for _, bad := range []string{"", "  ", ",,,"} {
		_, err := ParseSelect(bad)
		require.Error(t, err, "input %q", bad)
	}
}

// --- VerifyCorpus (D5) ---

func TestVerifyCorpus_CleanStory(t *testing.T) {
	f := newFixture(t, allExts)
	f.seedRecords()
	f.story("1-1_thing.md", validStory)

	report, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	require.Empty(t, report.Findings, "%+v", report.Findings)
	require.Equal(t, 1, report.Summary.StoriesChecked)
	require.True(t, report.OK())
}

// TestVerifyCorpus_ReproducesTheFourteenKnownFailures mirrors the real
// corpus: 13 stories missing `features` (53-1..53-6, 54-1..54-5, 55-1,
// 55-2) plus 28-1 missing `capabilities`. Exactly 14 findings, no more.
func TestVerifyCorpus_ReproducesTheFourteenKnownFailures(t *testing.T) {
	f := newFixture(t, allExts)
	f.seedRecords()

	base := `story_id: %s
epic: %s
status: done
output_document: development/implementation/%s.md
governance:
  adrs:
    - ADR-0001
  patterns:
    - PAT-0001
capabilities:
  - CAP-1
features:
  - id: FEAT-1-1
    contribution: creates
`
	// A healthy story, to prove the check is not simply flagging everything.
	f.story("1-1_ok.md", fmt.Sprintf(base, "1-1", "1", "1-1_ok"))

	missingFeatures := []string{
		"53-1", "53-2", "53-3", "53-4", "53-5", "53-6",
		"54-1", "54-2", "54-3", "54-4", "54-5",
		"55-1", "55-2",
	}
	for _, id := range missingFeatures {
		epic, _, _ := strings.Cut(id, "-")
		fm := fmt.Sprintf(base, id, epic, id)
		fm = strings.Split(fm, "features:")[0] // drop the features block
		f.story(id+"_story.md", fm)
	}
	// 28-1 has features but no capabilities.
	fm := fmt.Sprintf(base, "28-1", "28", "28-1")
	fm = strings.Replace(fm, "capabilities:\n  - CAP-1\n", "", 1)
	f.story("28-1_story.md", fm)

	report, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	found := gating(report.Findings)
	require.Len(t, found, 14, "exactly the 14 known failures: %+v", found)
	require.Equal(t, map[string]int{CheckExtKeyMissing: 14}, report.Summary.ByCheck,
		"the advisory class is absent by default, so it cannot pad this count")

	var featureGaps, capabilityGaps int
	for _, finding := range report.Findings {
		switch finding.Field {
		case "features":
			featureGaps++
		case "capabilities":
			capabilityGaps++
		}
	}
	require.Equal(t, 13, featureGaps)
	require.Equal(t, 1, capabilityGaps)
}

// TestVerifyCorpus_BareStringFeatures is the type check: three skills
// emit {id, contribution} objects and one governance doc specified a flat
// list, so the corpus carries both shapes.
func TestVerifyCorpus_BareStringFeatures(t *testing.T) {
	f := newFixture(t, allExts)
	f.seedRecords()
	fm := strings.Replace(validStory,
		"features:\n  - id: FEAT-1-1\n    contribution: creates\n",
		"features:\n  - FEAT-1-1\n  - FEAT-1-2\n", 1)
	f.story("1-1_thing.md", fm)

	report, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	types := 0
	for _, finding := range report.Findings {
		if finding.Check == CheckTypeMismatch {
			types++
			require.Contains(t, finding.Message, "expected an object, got string")
		}
	}
	require.Equal(t, 2, types, "one finding per bare-string entry")
}

// TestVerifyCorpus_NumericDependsOn: `- 53.1` decodes as float64, and the
// point is to REPORT it rather than coerce it back to a string.
func TestVerifyCorpus_NumericDependsOn(t *testing.T) {
	f := newFixture(t, allExts)
	f.seedRecords()
	f.story("1-1_thing.md", validStory+"depends_on:\n  - 53.1\n  - 54.2\n  - \"55-1\"\n")

	report, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	var msgs []string
	for _, finding := range report.Findings {
		if finding.Check == CheckTypeMismatch {
			msgs = append(msgs, finding.Message)
		}
	}
	require.Len(t, msgs, 2, "the two floats, not the quoted string")
	require.Contains(t, msgs[0], "expected a string, got float")
	require.Contains(t, msgs[0], "quote it")
}

// TestVerifyCorpus_ContributionVocabularyIsNotChecked locks C5. Any
// future patch that adds an enum here fails this test, which is the point.
func TestVerifyCorpus_ContributionVocabularyIsNotChecked(t *testing.T) {
	f := newFixture(t, allExts)
	f.seedRecords()
	for _, word := range []string{"creates", "contributes", "extends", "banana"} {
		t.Run(word, func(t *testing.T) {
			fm := strings.Replace(validStory, "contribution: creates", "contribution: "+word, 1)
			f.story("1-1_thing.md", fm)
			report, err := VerifyCorpus(f.cfg)
			require.NoError(t, err)
			require.Empty(t, report.Findings,
				"the contribution vocabulary has no normative source — coercing it would fabricate a lifecycle edge")
		})
	}
}

func TestVerifyCorpus_ExtGatingSuppressesPresenceChecks(t *testing.T) {
	f := newFixture(t, "ext-adrs")
	f.record(f.cfg.Paths.ADRs, "adr-0001_x.md", "ADR-0001")
	f.story("1-1_thing.md", `story_id: 1-1
epic: 1
status: done
output_document: x.md
governance:
  adrs:
    - ADR-0001
`)

	report, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	require.Empty(t, gating(report.Findings),
		"with only ext-adrs active, missing features/capabilities/patterns are not findings")
}

func TestVerifyCorpus_RequiredKeys(t *testing.T) {
	f := newFixture(t, "")
	f.story("1-1_thing.md", "story_id: 1-1\n")

	report, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	found := gating(report.Findings)
	require.Len(t, found, 3, "epic, status and output_document are absent")
	for _, finding := range found {
		require.Equal(t, CheckRequiredKeyMissing, finding.Check)
	}
}

func TestVerifyCorpus_UnresolvedReference(t *testing.T) {
	f := newFixture(t, allExts)
	f.seedRecords()
	fm := strings.Replace(validStory, "- ADR-0001", "- ADR-9999", 1)
	f.story("1-1_thing.md", fm)

	report, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	require.Len(t, report.Findings, 1)
	require.Equal(t, CheckUnresolvedRef, report.Findings[0].Check)
	require.Contains(t, report.Findings[0].Message, "ADR-9999")
}

// TestVerifyCorpus_MissingFamilyDirIsNotAWaveOfUnresolvedRefs: if the ADR
// directory does not exist, that is the registry verifier's finding, not
// one per citation here.
func TestVerifyCorpus_MissingFamilyDirIsSilentHere(t *testing.T) {
	f := newFixture(t, allExts)
	f.story("1-1_thing.md", validStory)

	report, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	for _, finding := range report.Findings {
		require.NotEqual(t, CheckUnresolvedRef, finding.Check)
	}
}

func TestVerifyCorpus_NonStoryFilesAreNotFindings(t *testing.T) {
	f := newFixture(t, "")
	f.write("epic-1-retro-20260101.md", "# Retrospective\n\nNo frontmatter at all.\n")
	f.write("deferred-work.md", "# Deferred work\n\nMoved to ../deferred/\n")

	report, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	require.Empty(t, report.Findings)
	require.Equal(t, 2, report.Summary.FilesScanned)
	require.Zero(t, report.Summary.StoriesChecked)
}

func TestVerifyCorpus_MalformedFrontmatterIsAFinding(t *testing.T) {
	f := newFixture(t, "")
	f.write("1-1_broken.md", "---\nstory_id: [oops\n---\n\nx\n")

	report, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	require.Len(t, report.Findings, 1)
	require.Equal(t, CheckUnparsable, report.Findings[0].Check)
}

func TestVerifyCorpus_OptionalKeyFormat(t *testing.T) {
	f := newFixture(t, "")
	good := "story_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\ndev_started_at_sha: a1b2c3d\n"
	f.story("1-1_good.md", good)
	report, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	require.Empty(t, gating(report.Findings))

	f.story("1-1_good.md", strings.Replace(good, "a1b2c3d", "not-a-sha", 1))
	report, err = VerifyCorpus(f.cfg)
	require.NoError(t, err)
	bad := gating(report.Findings)
	require.Len(t, bad, 1)
	require.Equal(t, CheckOptionalKeyMalformed, bad[0].Check)
}

func TestVerifyCorpus_FindingOrderIsStable(t *testing.T) {
	f := newFixture(t, allExts)
	f.seedRecords()
	for _, id := range []string{"3-1", "1-1", "2-1"} {
		f.story(id+"_x.md", "story_id: "+id+"\n")
	}
	first, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	second, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	require.Equal(t, first.Findings, second.Findings)
}

// --- VerifyFile (D5, the retirement replacement) ---

// conformingBody is a story body carrying every section the derived set
// requires with no extensions active. The frontmatter cases below pair
// with it so they test what they are named for — the KEY classes — and
// do not incidentally trip the body classes that arrived with PLAN-26.
const conformingBody = `
# Story 1.1: A thing

## Story

As a user, I want a thing.

## Acceptance Criteria

1. It works.

## Tasks / Subtasks

- [ ] Task 1 (AC: 1)

## Dev Notes

Notes.

## Dev Agent Record

### Agent Model Used

_(populated during dev)_

### File List

_(populated during dev)_

### Completion Notes List

_(populated during dev)_

### Debug Log References

_No issues encountered._

## Change Log

| Date | Change |
| ---- | ------ |
`

// governanceBody adds the sections ext_adrs makes members of the set.
const governanceBody = `
# Story 1.1: A thing

## Story

As a user, I want a thing.

## Acceptance Criteria

1. It works.

### Governance Compliance Criteria

- [ ] **[ADR-0001]** Verify the thing is wired -- ` + "`internal/**`" + `

## Tasks / Subtasks

- [ ] Task 1 (AC: 1)

## Dev Notes

Notes.

## Governance

### ADR Compliance Table

| ADR | Why it applies | Key constraints |
| --- | -------------- | --------------- |

## Dev Agent Record

### Agent Model Used

_(populated during dev)_

### File List

_(populated during dev)_

### Completion Notes List

_(populated during dev)_

### Debug Log References

_No issues encountered._

## Change Log

| Date | Change |
| ---- | ------ |
`

// TestVerifyFile_ExitCodesMatchThePython is the retirement gate: 0 valid,
// 2 parse failure, 3 key problem — verify-story-frontmatter.py's table,
// unchanged and unrenumbered by the body classes added beside it.
func TestVerifyFile_ExitCodesMatchThePython(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
		return path
	}

	cases := []struct {
		name string
		body string
		exts string
		want int
	}{
		{
			name: "valid, no extensions",
			body: "---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n---\n" + conformingBody,
			want: FileOK,
		},
		{
			name: "no frontmatter delimiters",
			body: "# Just a heading\n\nx\n",
			want: FileParseFailure,
		},
		{
			name: "unterminated frontmatter",
			body: "---\nstory_id: 1-1\n",
			want: FileParseFailure,
		},
		{
			name: "malformed YAML",
			body: "---\nstory_id: [oops\n---\n\nx\n",
			want: FileParseFailure,
		},
		{
			name: "required key absent",
			body: "---\nstory_id: 1-1\nepic: 1\n---\n\nx\n",
			want: FileKeyProblem,
		},
		{
			name: "extension-conditional key absent",
			body: "---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n---\n\nx\n",
			exts: "ext-adrs",
			want: FileKeyProblem,
		},
		{
			name: "optional key malformed",
			body: "---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\ndev_started_at_sha: ZZZ\n---\n\nx\n",
			want: FileKeyProblem,
		},
		{
			name: "extension satisfied",
			body: "---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\ngovernance:\n  adrs: [ADR-1]\n---\n" + governanceBody,
			exts: "ext-adrs",
			want: FileOK,
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := write(fmt.Sprintf("case-%d.md", i), tc.body)
			verdict := VerifyFile(path, ParseActiveExtensions(tc.exts))
			require.Equal(t, tc.want, verdict.Code, "findings: %+v", verdict.Findings)
		})
	}
}

// TestVerifyFile_DoesNotAssertReferentialIntegrity: the per-file gate
// cannot see the corpus, exactly as the Python could not — a citation to a
// nonexistent ADR is not this mode's business.
func TestVerifyFile_DoesNotAssertReferentialIntegrity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.md")
	require.NoError(t, os.WriteFile(path, []byte(
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n"+
			"governance:\n  adrs: [ADR-9999]\n---\n"+governanceBody,
	), 0o644))

	verdict := VerifyFile(path, ParseActiveExtensions("ext-adrs"))
	require.Equal(t, FileOK, verdict.Code)
}

func TestParseActiveExtensions(t *testing.T) {
	require.Equal(t, apexcfg.Ext{}, ParseActiveExtensions(""))
	require.Equal(t, apexcfg.Ext{ADRs: true, Features: true},
		ParseActiveExtensions("ext-adrs,ext-features"))
	require.Equal(t, apexcfg.Ext{Patterns: true},
		ParseActiveExtensions(" ext-patterns , ext-nonsense "))
}

func TestCitedIDs(t *testing.T) {
	require.Equal(t, []string{"ADR-1"}, citedIDs("ADR-1"))
	require.Equal(t, []string{"ADR-1", "ADR-2"}, citedIDs([]any{"ADR-2", "ADR-1"}))
	require.Equal(t, []string{"FEAT-1"}, citedIDs([]any{map[string]any{"id": "FEAT-1"}}))
	require.Empty(t, citedIDs(nil))
	require.Empty(t, citedIDs([]any{""}))
}

func TestLookupPath(t *testing.T) {
	raw := map[string]any{"governance": map[string]any{"adrs": []any{"ADR-1"}}}
	v, ok := lookupPath(raw, []string{"governance", "adrs"})
	require.True(t, ok)
	require.Equal(t, []any{"ADR-1"}, v)

	_, ok = lookupPath(raw, []string{"governance", "patterns"})
	require.False(t, ok)
	_, ok = lookupPath(raw, []string{"nope", "deeper"})
	require.False(t, ok)
}

// fixContents reads a story back after a fix.
func (f *fixture) contents(name string) string {
	f.t.Helper()
	body, err := os.ReadFile(filepath.Join(f.cfg.Paths.Implementation, name))
	require.NoError(f.t, err)
	return string(body)
}

// fixBase carries requirement_ids so these tests stay about --fix. The
// report-only story.requirement_ids_missing class fires on any story
// without the key and lands in Remaining, which is its delivery route to
// apex-frontmatter-repair — correct behaviour, and pure noise in a test
// asserting which findings --fix owns.
const fixBase = "story_id: \"112.2\"\nepic: \"112\"\nstatus: done\n" +
	"requirement_ids: [FR-1]\n" +
	"output_document: \"development/implementation/s.md\"\n"

// TestFix_QuotesBothSequenceShapes covers the two ways a corpus writes
// depends_on. The already-quoted item in the block case is the control: a
// fixer that rewrites it has stopped being a repair and started being a
// reformatter.
func TestFix_QuotesBothSequenceShapes(t *testing.T) {
	f := newFixture(t, "")
	f.story("flow.md", fixBase+"depends_on: [112.1, 112.2]\n")
	f.story("block.md", fixBase+"depends_on:\n  - 112.1\n  - \"112.3\"\n")

	res, err := FixCorpus(f.cfg, false)
	require.NoError(t, err)
	require.Len(t, res.Changes, 2)

	require.Contains(t, f.contents("flow.md"), `depends_on: ["112.1", "112.2"]`,
		"every item must be quoted, not just the first")
	block := f.contents("block.md")
	require.Contains(t, block, `  - "112.1"`)
	require.Contains(t, block, `  - "112.3"`, "an already-quoted item is left exactly as authored")

	after, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	require.Empty(t, after.Findings)
}

// TestFix_LeavesTheJudgmentClassesAlone is the scope gate. `features` needs a
// contribution that lives in another document, and a missing key needs a
// value that is a registry question — writing either would be fabrication,
// so both must survive --fix untouched and be reported as remaining.
func TestFix_LeavesTheJudgmentClassesAlone(t *testing.T) {
	f := newFixture(t, "ext-features")
	f.seedRecords()
	before := "---\n" + fixBase + "features: [FEAT-1-1]\n---\n\nbody\n"
	f.write("featstring.md", before)
	f.story("nokey.md", "story_id: \"53.1\"\nepic: \"53\"\nstatus: done\n"+
		"output_document: \"development/implementation/nokey.md\"\n")

	res, err := FixCorpus(f.cfg, false)
	require.NoError(t, err)
	require.Empty(t, res.Changes)
	require.NotEmpty(t, res.Remaining, "declined findings are reported, never dropped")

	require.Equal(t, before, f.contents("featstring.md"),
		"a features item must survive --fix byte-for-byte")
	require.NotContains(t, f.contents("nokey.md"), "features:",
		"--fix must not invent an empty features list")
}

// TestFix_CheckWritesNothing is the dry-run contract.
func TestFix_CheckWritesNothing(t *testing.T) {
	f := newFixture(t, "")
	f.story("flow.md", fixBase+"depends_on: [112.1]\n")
	before := f.contents("flow.md")

	res, err := FixCorpus(f.cfg, true)
	require.NoError(t, err)
	require.Len(t, res.Changes, 1)
	require.Equal(t, before, f.contents("flow.md"))
}

// TestFix_TouchesNothingOutsideTheDependsOnLine: the repair is lexical, so
// the gate is that the rest of the document — body, key order, quoting
// style, the hand-wrapped values a node round-trip would reflow — is
// identical afterwards.
func TestFix_TouchesNothingOutsideTheDependsOnLine(t *testing.T) {
	f := newFixture(t, "")
	before := "---\n" + fixBase +
		"depends_on: [112.1]\n" +
		"governance:\n  adrs:\n    [\n      ADR-0001,\n      ADR-0002,\n    ]\n" +
		"---\n\n## Story\n\nprose that must not move.\n"
	f.write("wrapped.md", before)

	_, err := FixCorpus(f.cfg, false)
	require.NoError(t, err)

	after := f.contents("wrapped.md")
	require.Equal(t,
		strings.Replace(before, "depends_on: [112.1]", `depends_on: ["112.1"]`, 1),
		after,
		"exactly one line may differ")
}

// TestFix_IgnoresValuesThatOnlyLookNumeric guards the anchoring. A version
// string, a suffixed key and an already-quoted item are not float-decoded
// story keys, and rewriting them would corrupt real values.
func TestFix_IgnoresValuesThatOnlyLookNumeric(t *testing.T) {
	f := newFixture(t, "")
	before := "---\n" + fixBase + `depends_on: ["112.1", v1.2, 112.1a]` + "\n---\n\nbody\n"
	f.write("mixed.md", before)

	res, err := FixCorpus(f.cfg, false)
	require.NoError(t, err)
	require.Empty(t, res.Changes, "nothing here is a bare number")
	require.Equal(t, before, f.contents("mixed.md"))
}

// TestFix_RepairsDependsOnBesideAnUnfixableFeaturesItem pins the scope of
// the rollback guard.
//
// The guard re-verifies a touched file and reverts a write that did not
// reduce its findings — the right instinct for a lexical rewrite. But
// `story.type_mismatch` covers `features[i]` as well as `depends_on[i]`,
// and --fix owns only the second. Counting both made one surviving
// features finding cancel out one repaired depends_on finding, trip the
// `>=`, and revert a correct repair — leaving the fixable finding reported
// as "remaining" with nothing to distinguish it from one --fix refuses on
// purpose. The two classes are separate files in every other test here,
// which is why nothing caught it; a story carrying both is the case the
// command exists for.
func TestFix_RepairsDependsOnBesideAnUnfixableFeaturesItem(t *testing.T) {
	f := newFixture(t, "ext-features")
	f.seedRecords()
	before := "---\n" + fixBase +
		"depends_on: [112.1]\n" +
		"features: [FEAT-1-1]\n" +
		"---\n\nbody\n"
	f.write("both.md", before)

	res, err := FixCorpus(f.cfg, false)
	require.NoError(t, err)

	require.Len(t, res.Changes, 1, "the derivable repair must survive the guard")
	require.Contains(t, f.contents("both.md"), `depends_on: ["112.1"]`)

	// The refused class is still reported. Asserted by identity rather
	// than by a count of the whole list: report-only classes such as
	// story.requirement_ids_missing also land in Remaining — that IS
	// their delivery route to apex-frontmatter-repair — so a bare length
	// check here would break every time one is added, without saying
	// anything about the guard this test exists to pin.
	var refused []Finding
	for _, r := range res.Remaining {
		if r.Check == CheckTypeMismatch {
			refused = append(refused, r)
		}
	}
	require.Len(t, refused, 1, "the refused class is still reported")
	require.Equal(t, "features[0]", refused[0].Field)
	require.Contains(t, f.contents("both.md"), "features: [FEAT-1-1]",
		"the refused item stays byte-for-byte as authored")
}
