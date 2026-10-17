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
